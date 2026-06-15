// Package client provides the bandwidth purchase SDK for arfl-client.
//
// BandwidthClient encapsulates the full purchase flow:
//
//  1. Purchase a bandwidth tier (gets a Lightning invoice)
//  2. Wait for the user to pay the invoice externally
//  3. Redeem blind tokens (send blinded messages, get blind signatures)
//  4. Unblind signatures to produce spendable BlindToken envelopes
//
// The client never reveals its token secrets to the Hub. The Hub signs
// blinded messages and cannot link token usage back to the buyer.
//
// Usage:
//
//	client := client.NewBandwidthClient("http://hub:8080", pubKey)
//	purchase, _ := client.Purchase(ctx, "1gb")
//	// User pays purchase.PaymentRequest externally
//	settled, _ := client.WaitForSettlement(ctx, purchase.PaymentHash)
//	tokens, _ := client.RedeemTokens(ctx, purchase.PaymentHash, preimage, "key-100mb", 5)
//	// tokens are ready to present to nodes
package client

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Radi-Labs/ARFL/internal/credentials"
)

// BandwidthClient talks to the Hub's HTTP API to purchase and redeem
// bandwidth tokens.
type BandwidthClient struct {
	hubURL     string
	pubKey     *rsa.PublicKey // Hub's blind signature public key
	keyID      string         // denomination key ID
	httpClient *http.Client
}

// NewBandwidthClient creates a client for the given hub URL.
// pubKey is the Hub's RSA public key for blind signature verification.
// keyID is the denomination key identifier (e.g., "key-100mb").
func NewBandwidthClient(hubURL string, pubKey *rsa.PublicKey, keyID string) *BandwidthClient {
	return &BandwidthClient{
		hubURL: hubURL,
		pubKey: pubKey,
		keyID:  keyID,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// --- Purchase ---

// PurchaseResult holds the response from POST /purchase.
type PurchaseResult struct {
	PaymentHash    string `json:"payment_hash"`
	PaymentRequest string `json:"payment_request"` // BOLT11 invoice to pay
	AmountSats     int64  `json:"amount_sats"`
	Tier           string `json:"tier"`
	ExpiresAt      string `json:"expires_at"`
}

// Purchase creates a new bandwidth purchase for the given tier.
// Returns a Lightning invoice the user must pay externally.
func (c *BandwidthClient) Purchase(ctx context.Context, tierID string) (*PurchaseResult, error) {
	body, _ := json.Marshal(map[string]string{"tier_id": tierID})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.hubURL+"/purchase", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("purchase request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return nil, readAPIError(resp)
	}

	var result PurchaseResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode purchase response: %w", err)
	}
	return &result, nil
}

// --- Poll for settlement ---

// PurchaseStatus holds the response from GET /purchase/:id.
type PurchaseStatus struct {
	PaymentHash string `json:"payment_hash"`
	Status      string `json:"status"` // "open", "settled", "expired"
	AmountSats  int64  `json:"amount_sats"`
	Tier        string `json:"tier"`
}

// GetPurchaseStatus checks the current status of a purchase.
func (c *BandwidthClient) GetPurchaseStatus(ctx context.Context, paymentHash string) (*PurchaseStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.hubURL+"/purchase/"+paymentHash, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("status request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, readAPIError(resp)
	}

	var result PurchaseStatus
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode status response: %w", err)
	}
	return &result, nil
}

// WaitForSettlement polls until the invoice is settled or the context is cancelled.
// pollInterval controls how often to check (default 2 seconds).
func (c *BandwidthClient) WaitForSettlement(ctx context.Context, paymentHash string, pollInterval time.Duration) (*PurchaseStatus, error) {
	if pollInterval == 0 {
		pollInterval = 2 * time.Second
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		status, err := c.GetPurchaseStatus(ctx, paymentHash)
		if err != nil {
			return nil, err
		}

		switch status.Status {
		case "settled":
			return status, nil
		case "expired":
			return nil, fmt.Errorf("invoice expired")
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// --- Redeem blind tokens ---

// RedeemResult holds the blinded tokens from a successful redemption,
// along with the unblinding material needed to produce final tokens.
type RedeemResult struct {
	Tokens          []*credentials.BlindToken // Ready-to-spend tokens
	BytesPerToken   int64
	TokensRedeemed  int
	TokensRemaining int
}

// RedeemTokens generates random token secrets, blinds them, sends them
// to the Hub for signing, unblinds the signatures, and returns usable tokens.
//
// preimage is the hex-encoded Lightning payment preimage (proof of payment).
// count is how many tokens to redeem from the entitlement.
// nonce is the idempotency key — use a unique value per redemption request.
func (c *BandwidthClient) RedeemTokens(ctx context.Context, preimage string, count int, nonce string) (*RedeemResult, error) {
	if count <= 0 {
		return nil, fmt.Errorf("count must be positive")
	}

	// Step 1: Generate random token secrets and blind them.
	type pending struct {
		secret    []byte
		unblinder []byte
	}
	pendings := make([]pending, count)
	blindedMsgs := make([]string, count)

	for i := 0; i < count; i++ {
		secret, err := credentials.GenerateTokenSecret()
		if err != nil {
			return nil, fmt.Errorf("generate secret %d: %w", i, err)
		}

		bm, err := credentials.BlindTokenSecret(c.pubKey, secret)
		if err != nil {
			return nil, fmt.Errorf("blind secret %d: %w", i, err)
		}

		pendings[i] = pending{secret: secret, unblinder: bm.Unblinder}
		blindedMsgs[i] = hex.EncodeToString(bm.Blinded)
	}

	// Step 2: Send blinded messages to Hub.
	reqBody, _ := json.Marshal(map[string]interface{}{
		"payment_preimage": preimage,
		"key_id":           c.keyID,
		"blinded_messages": blindedMsgs,
		"nonce":            nonce,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.hubURL+"/redeem", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("redeem request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, readAPIError(resp)
	}

	var redeemResp struct {
		BlindSignatures []string `json:"blind_signatures"`
		BytesPerToken   int64    `json:"bytes_per_token"`
		TokensRedeemed  int      `json:"tokens_redeemed"`
		TokensRemaining int      `json:"tokens_remaining"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&redeemResp); err != nil {
		return nil, fmt.Errorf("decode redeem response: %w", err)
	}

	if len(redeemResp.BlindSignatures) != count {
		return nil, fmt.Errorf("expected %d signatures, got %d", count, len(redeemResp.BlindSignatures))
	}

	// Step 3: Unblind signatures and assemble tokens.
	tokens := make([]*credentials.BlindToken, count)
	for i := 0; i < count; i++ {
		blindSig, err := hex.DecodeString(redeemResp.BlindSignatures[i])
		if err != nil {
			return nil, fmt.Errorf("decode signature %d: %w", i, err)
		}

		unblinded := credentials.UnblindSignature(c.pubKey, blindSig, pendings[i].unblinder)

		tokens[i] = &credentials.BlindToken{
			Version:     credentials.BlindTokenVersion,
			KeyID:       c.keyID,
			TokenSecret: pendings[i].secret,
			Signature:   unblinded,
		}
	}

	return &RedeemResult{
		Tokens:          tokens,
		BytesPerToken:   redeemResp.BytesPerToken,
		TokensRedeemed:  redeemResp.TokensRedeemed,
		TokensRemaining: redeemResp.TokensRemaining,
	}, nil
}

// --- Helpers ---

// readAPIError extracts the error message from a non-2xx response.
func readAPIError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))

	var errResp struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
		return fmt.Errorf("hub API error (%d): %s", resp.StatusCode, errResp.Error)
	}

	return fmt.Errorf("hub API error (%d): %s", resp.StatusCode, string(body))
}
