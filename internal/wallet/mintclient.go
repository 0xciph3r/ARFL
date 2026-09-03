// Package wallet implements the client side of the ARFL Cashu flow.
//
// The hub runs the mint (internal/ecash). This package is its counterpart:
// it talks to any hub's NUT-compliant HTTP API, blinds secrets locally,
// and unblinds the returned signatures into spendable proofs.
//
// Privacy: secrets and blinding factors never leave the client. The hub
// signs blinded messages and therefore cannot link a minted proof back to
// the Lightning payment that funded it.
//
// The client is hub-agnostic by design — ARFL is a protocol, so the desktop
// app points at whichever hub the user chooses.
package wallet

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/elnosh/gonuts/cashu"
)

// maxResponseBytes caps hub responses so a hostile hub cannot exhaust client memory.
const maxResponseBytes = 1 << 20 // 1 MiB

// QuoteState mirrors the mint quote lifecycle exposed by the hub (NUT-04).
type QuoteState string

const (
	QuoteUnpaid  QuoteState = "UNPAID"
	QuotePaid    QuoteState = "PAID"
	QuoteIssued  QuoteState = "ISSUED"
	QuoteExpired QuoteState = "EXPIRED"
)

// MintQuote is a hub-issued Lightning invoice for minting ecash.
type MintQuote struct {
	ID             string     `json:"quote"`
	Amount         uint64     `json:"amount"`
	PaymentRequest string     `json:"request"`
	PaymentHash    string     `json:"payment_hash"`
	State          QuoteState `json:"state"`
	Expiry         int64      `json:"expiry"`
}

// Paid reports whether the invoice has been settled. A quote that has already
// been issued was necessarily paid first.
func (q *MintQuote) Paid() bool {
	return q.State == QuotePaid || q.State == QuoteIssued
}

// Keyset is a hub's active set of denomination public keys (NUT-01).
type Keyset struct {
	ID   string            `json:"id"`
	Unit string            `json:"unit"`
	Keys map[uint64]string `json:"keys"` // denomination (sats) → compressed pubkey hex
}

// HubInfo is the hub metadata returned by GET /info.
type HubInfo struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	NodeCount    int    `json:"node_count"`
	HubMarginPct int    `json:"hub_margin_pct"`
}

// MintClient is an HTTP client for a single hub's Cashu API.
type MintClient struct {
	hubURL     string
	httpClient *http.Client
}

// NewMintClient creates a client for the given hub base URL (e.g.
// "https://hub.example.com"). A trailing slash is tolerated.
func NewMintClient(hubURL string) (*MintClient, error) {
	normalised, err := normaliseHubURL(hubURL)
	if err != nil {
		return nil, err
	}
	return &MintClient{
		hubURL:     normalised,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// HubURL returns the normalised base URL this client targets.
func (c *MintClient) HubURL() string { return c.hubURL }

// normaliseHubURL validates a user-supplied hub URL and strips trailing slashes.
// Users type hub addresses by hand, so reject malformed input early with a
// clear message rather than failing later on an opaque request error.
func normaliseHubURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("hub URL is required")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid hub URL %q: %w", raw, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("hub URL must use http or https, got %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("hub URL %q is missing a host", raw)
	}

	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

// Info fetches hub metadata. Useful as a connectivity probe before purchase.
func (c *MintClient) Info(ctx context.Context) (*HubInfo, error) {
	var info HubInfo
	if err := c.do(ctx, http.MethodGet, "/info", nil, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// ActiveKeyset fetches the hub's active keyset (NUT-01). The returned keys
// are required to unblind signatures, so they must be fetched before minting.
func (c *MintClient) ActiveKeyset(ctx context.Context) (*Keyset, error) {
	var resp struct {
		Keysets []Keyset `json:"keysets"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/keys", nil, &resp); err != nil {
		return nil, err
	}
	if len(resp.Keysets) == 0 {
		return nil, fmt.Errorf("hub returned no keysets")
	}

	// The hub publishes exactly one active keyset on /v1/keys, but prefer a
	// sat-denominated one if that ever changes.
	for i := range resp.Keysets {
		if resp.Keysets[i].Unit == "sat" {
			return &resp.Keysets[i], nil
		}
	}
	return &resp.Keysets[0], nil
}

// CreateMintQuote asks the hub for a Lightning invoice covering amountSats.
func (c *MintClient) CreateMintQuote(ctx context.Context, amountSats uint64) (*MintQuote, error) {
	if amountSats == 0 {
		return nil, fmt.Errorf("amount must be greater than zero")
	}

	body := map[string]any{"amount": amountSats, "unit": "sat"}
	var quote MintQuote
	if err := c.do(ctx, http.MethodPost, "/v1/mint/quote/bolt11", body, &quote); err != nil {
		return nil, err
	}
	if quote.ID == "" || quote.PaymentRequest == "" {
		return nil, fmt.Errorf("hub returned an incomplete mint quote")
	}
	return &quote, nil
}

// MintQuoteStatus polls the current state of a quote. The hub re-checks the
// Lightning backend, so this is how the client learns the invoice was paid.
func (c *MintClient) MintQuoteStatus(ctx context.Context, quoteID string) (*MintQuote, error) {
	if quoteID == "" {
		return nil, fmt.Errorf("quote ID is required")
	}
	var quote MintQuote
	path := "/v1/mint/quote/bolt11/" + url.PathEscape(quoteID)
	if err := c.do(ctx, http.MethodGet, path, nil, &quote); err != nil {
		return nil, err
	}
	return &quote, nil
}

// MintTokens exchanges blinded messages for blinded signatures on a paid quote.
func (c *MintClient) MintTokens(
	ctx context.Context,
	quoteID string,
	outputs cashu.BlindedMessages,
) (cashu.BlindedSignatures, error) {
	if quoteID == "" {
		return nil, fmt.Errorf("quote ID is required")
	}
	if len(outputs) == 0 {
		return nil, fmt.Errorf("at least one output is required")
	}

	body := map[string]any{"quote": quoteID, "outputs": outputs}
	var resp struct {
		Signatures cashu.BlindedSignatures `json:"signatures"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/mint/bolt11", body, &resp); err != nil {
		return nil, err
	}
	if len(resp.Signatures) != len(outputs) {
		return nil, fmt.Errorf("hub returned %d signatures for %d outputs",
			len(resp.Signatures), len(outputs))
	}
	return resp.Signatures, nil
}

// do performs a JSON request against the hub and decodes the response.
func (c *MintClient) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.hubURL+path, reader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("read response from %s: %w", path, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HubError{
			StatusCode: resp.StatusCode,
			Path:       path,
			Message:    extractErrorMessage(payload),
		}
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("decode response from %s: %w", path, err)
	}
	return nil
}

// HubError is returned when the hub responds with a non-2xx status.
type HubError struct {
	StatusCode int
	Path       string
	Message    string
}

func (e *HubError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("hub returned %d for %s", e.StatusCode, e.Path)
	}
	return fmt.Sprintf("hub returned %d for %s: %s", e.StatusCode, e.Path, e.Message)
}

// extractErrorMessage pulls a human-readable message out of an error body,
// tolerating both the hub's {"error":...} shape and Cashu's {"detail":...}.
func extractErrorMessage(payload []byte) string {
	var parsed struct {
		Error  string `json:"error"`
		Detail string `json:"detail"`
	}
	if json.Unmarshal(payload, &parsed) == nil {
		if parsed.Error != "" {
			return parsed.Error
		}
		if parsed.Detail != "" {
			return parsed.Detail
		}
	}
	return strings.TrimSpace(string(payload))
}
