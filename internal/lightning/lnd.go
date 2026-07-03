package lightning

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// LNDConfig holds connection details for an LND node.
type LNDConfig struct {
	Host         string // e.g. "localhost"
	Port         int    // e.g. 8080 (REST port)
	TLSCertPath  string // path to tls.cert
	MacaroonPath string // path to admin.macaroon
	FeeLimitSat  int64  // max routing fee in sats (default 100)
}

// LNDClient connects to an LND node via its REST API.
// Uses macaroon authentication and TLS from LND's self-signed cert.
type LNDClient struct {
	baseURL     string
	macaroonHex string
	httpClient  *http.Client
	feeLimitSat int64
}

// NewLNDClient creates a lightning.Client backed by a real LND node.
func NewLNDClient(cfg LNDConfig) (*LNDClient, error) {
	// Read TLS certificate.
	certPEM, err := os.ReadFile(cfg.TLSCertPath)
	if err != nil {
		return nil, fmt.Errorf("read TLS cert %s: %w", cfg.TLSCertPath, err)
	}
	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(certPEM) {
		return nil, fmt.Errorf("failed to parse TLS cert from %s", cfg.TLSCertPath)
	}

	// Read macaroon.
	macBytes, err := os.ReadFile(cfg.MacaroonPath)
	if err != nil {
		return nil, fmt.Errorf("read macaroon %s: %w", cfg.MacaroonPath, err)
	}
	macaroonHex := hex.EncodeToString(macBytes)

	feeLimitSat := cfg.FeeLimitSat
	if feeLimitSat <= 0 {
		feeLimitSat = 100
	}

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs: certPool,
			},
		},
		Timeout: 30 * time.Second,
	}

	return &LNDClient{
		baseURL:     fmt.Sprintf("https://%s:%d", cfg.Host, cfg.Port),
		macaroonHex: macaroonHex,
		httpClient:  client,
		feeLimitSat: feeLimitSat,
	}, nil
}

// newLNDClientDirect creates a client from pre-configured components (for tests).
func newLNDClientDirect(baseURL, macaroonHex string, httpClient *http.Client) *LNDClient {
	return &LNDClient{
		baseURL:     baseURL,
		macaroonHex: macaroonHex,
		httpClient:  httpClient,
		feeLimitSat: 100,
	}
}

// CreateInvoice generates a BOLT11 payment request via LND.
func (c *LNDClient) CreateInvoice(ctx context.Context, amountSats int64, memo string, expiry time.Duration) (*Invoice, error) {
	body := map[string]interface{}{
		"value":  amountSats,
		"memo":   memo,
		"expiry": int64(expiry.Seconds()),
	}

	var resp lndAddInvoiceResponse
	if err := c.post(ctx, "/v1/invoices", body, &resp); err != nil {
		return nil, fmt.Errorf("create invoice: %w", err)
	}

	paymentHash := base64ToHex(resp.RHash)

	now := time.Now()
	return &Invoice{
		PaymentHash:    paymentHash,
		PaymentRequest: resp.PaymentRequest,
		AmountSats:     amountSats,
		Memo:           memo,
		Status:         InvoiceOpen,
		CreatedAt:      now,
		ExpiresAt:      now.Add(expiry),
	}, nil
}

// LookupInvoice checks the current status of an invoice by payment hash.
func (c *LNDClient) LookupInvoice(ctx context.Context, paymentHash string) (*Invoice, error) {
	// LND REST expects the hash as URL-safe base64 in the path.
	hashBytes, err := hex.DecodeString(paymentHash)
	if err != nil {
		return nil, fmt.Errorf("invalid payment hash: %w", err)
	}
	hashB64 := base64.URLEncoding.EncodeToString(hashBytes)

	var resp lndInvoice
	if err := c.get(ctx, "/v1/invoice/"+hashB64, &resp); err != nil {
		if strings.Contains(err.Error(), "unable to locate invoice") {
			return nil, ErrInvoiceNotFound
		}
		return nil, fmt.Errorf("lookup invoice: %w", err)
	}

	return lndInvoiceToInvoice(&resp), nil
}

// SubscribeInvoices opens a streaming connection to LND's invoice subscription.
// Emits only settled invoices. Reconnects on connection drops.
func (c *LNDClient) SubscribeInvoices(ctx context.Context) (<-chan *Invoice, error) {
	ch := make(chan *Invoice, 64)

	go func() {
		defer close(ch)
		for {
			if err := c.subscribeLoop(ctx, ch); err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Printf("[lnd] subscribe stream error: %v — reconnecting in 5s", err)
				select {
				case <-time.After(5 * time.Second):
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return ch, nil
}

func (c *LNDClient) subscribeLoop(ctx context.Context, ch chan<- *Invoice) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/v1/invoices/subscribe", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Grpc-Metadata-macaroon", c.macaroonHex)

	// Use a client without timeout for streaming.
	streamClient := *c.httpClient
	streamClient.Timeout = 0

	resp, err := streamClient.Do(req)
	if err != nil {
		return fmt.Errorf("subscribe request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("subscribe: HTTP %d: %s", resp.StatusCode, body)
	}

	scanner := bufio.NewScanner(resp.Body)
	// LND can send large invoice payloads.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var wrapper lndStreamWrapper
		if err := json.Unmarshal([]byte(line), &wrapper); err != nil {
			log.Printf("[lnd] subscribe: unmarshal error: %v", err)
			continue
		}

		if wrapper.Result.State == "SETTLED" {
			inv := lndInvoiceToInvoice(&wrapper.Result)
			select {
			case ch <- inv:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("subscribe scanner: %w", err)
	}
	return fmt.Errorf("subscribe stream ended")
}

// SendPayment pays a BOLT11 invoice via LND's router.
func (c *LNDClient) SendPayment(ctx context.Context, paymentRequest string, amountSats int64) (*PaymentResult, error) {
	body := map[string]interface{}{
		"payment_request":     paymentRequest,
		"timeout_seconds":     60,
		"fee_limit_sat":       c.feeLimitSat,
		"no_inflight_updates": true,
	}

	return c.sendPaymentV2(ctx, body)
}

// Keysend sends a spontaneous payment to a node by public key.
func (c *LNDClient) Keysend(ctx context.Context, destPubkey string, amountSats int64) (*PaymentResult, error) {
	destBytes, err := hex.DecodeString(destPubkey)
	if err != nil {
		return nil, fmt.Errorf("invalid dest pubkey: %w", err)
	}

	// Generate a random preimage for keysend.
	preimage := make([]byte, 32)
	if _, err := io.ReadFull(strings.NewReader(randomHex()[:64]), preimage); err != nil {
		return nil, err
	}
	// Actually use crypto/rand properly.
	preimageHex, _, err := randomPreimageHash()
	if err != nil {
		return nil, err
	}
	preimageBytes, _ := hex.DecodeString(preimageHex)

	body := map[string]interface{}{
		"dest":                base64.StdEncoding.EncodeToString(destBytes),
		"amt":                 amountSats,
		"timeout_seconds":     60,
		"fee_limit_sat":       c.feeLimitSat,
		"no_inflight_updates": true,
		"dest_custom_records": map[string]string{
			// TLV type 5482373484 is the keysend preimage record.
			"5482373484": base64.StdEncoding.EncodeToString(preimageBytes),
		},
	}

	return c.sendPaymentV2(ctx, body)
}

// sendPaymentV2 calls the v2/router/send streaming endpoint and collects the final status.
func (c *LNDClient) sendPaymentV2(ctx context.Context, body map[string]interface{}) (*PaymentResult, error) {
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v2/router/send", strings.NewReader(string(data)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Grpc-Metadata-macaroon", c.macaroonHex)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send payment: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("send payment: HTTP %d: %s", resp.StatusCode, errBody)
	}

	// v2/router/send is a server-streaming endpoint.
	// Read all lines and use the final payment status.
	var lastPayment lndPayment
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var wrapper struct {
			Result lndPayment `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &wrapper); err != nil {
			continue
		}
		lastPayment = wrapper.Result
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("payment stream: %w", err)
	}

	result := &PaymentResult{
		PaymentHash: base64ToHex(lastPayment.PaymentHash),
	}

	switch lastPayment.Status {
	case "SUCCEEDED":
		result.Status = PaymentSucceeded
	case "FAILED":
		result.Status = PaymentFailed
		result.Error = lastPayment.FailureReason
		if result.Error == "" {
			result.Error = "payment failed"
		}
	case "IN_FLIGHT":
		result.Status = PaymentInFlight
	default:
		result.Status = PaymentFailed
		result.Error = fmt.Sprintf("unexpected status: %s", lastPayment.Status)
	}

	// Parse fee from the response.
	if lastPayment.FeeSat != "" {
		fmt.Sscanf(lastPayment.FeeSat, "%d", &result.FeeSats)
	}

	return result, nil
}

// --- HTTP helpers ---

func (c *LNDClient) post(ctx context.Context, path string, body interface{}, out interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+path, strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	req.Header.Set("Grpc-Metadata-macaroon", c.macaroonHex)
	req.Header.Set("Content-Type", "application/json")

	return c.doJSON(req, out)
}

func (c *LNDClient) get(ctx context.Context, path string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Grpc-Metadata-macaroon", c.macaroonHex)

	return c.doJSON(req, out)
}

func (c *LNDClient) doJSON(req *http.Request, out interface{}) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// Try to extract LND error message.
		var lndErr struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		json.Unmarshal(body, &lndErr)
		msg := lndErr.Message
		if msg == "" {
			msg = lndErr.Error
		}
		if msg == "" {
			msg = string(body)
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}

	return json.Unmarshal(body, out)
}

// --- LND REST API response types ---

type lndAddInvoiceResponse struct {
	RHash          string `json:"r_hash"`
	PaymentRequest string `json:"payment_request"`
	AddIndex       string `json:"add_index"`
}

type lndInvoice struct {
	Memo           string `json:"memo"`
	RHash          string `json:"r_hash"`
	Value          string `json:"value"`
	State          string `json:"state"` // OPEN, SETTLED, CANCELLED, ACCEPTED
	CreationDate   string `json:"creation_date"`
	SettleDate     string `json:"settle_date"`
	Expiry         string `json:"expiry"`
	PaymentRequest string `json:"payment_request"`
}

type lndStreamWrapper struct {
	Result lndInvoice `json:"result"`
}

type lndPayment struct {
	PaymentHash   string `json:"payment_hash"`
	Status        string `json:"status"` // IN_FLIGHT, SUCCEEDED, FAILED
	FeeSat        string `json:"fee_sat"`
	FailureReason string `json:"failure_reason"`
}

// --- Conversion helpers ---

func lndInvoiceToInvoice(lnd *lndInvoice) *Invoice {
	inv := &Invoice{
		PaymentHash:    base64ToHex(lnd.RHash),
		PaymentRequest: lnd.PaymentRequest,
		Memo:           lnd.Memo,
	}

	fmt.Sscanf(lnd.Value, "%d", &inv.AmountSats)

	switch lnd.State {
	case "SETTLED":
		inv.Status = InvoiceSettled
	case "CANCELLED", "CANCELED":
		inv.Status = InvoiceExpired
	default:
		inv.Status = InvoiceOpen
	}

	if lnd.CreationDate != "" {
		var ts int64
		fmt.Sscanf(lnd.CreationDate, "%d", &ts)
		inv.CreatedAt = time.Unix(ts, 0)
	}
	if lnd.SettleDate != "" && lnd.SettleDate != "0" {
		var ts int64
		fmt.Sscanf(lnd.SettleDate, "%d", &ts)
		inv.SettledAt = time.Unix(ts, 0)
	}
	if lnd.Expiry != "" && lnd.CreationDate != "" {
		var created, expiry int64
		fmt.Sscanf(lnd.CreationDate, "%d", &created)
		fmt.Sscanf(lnd.Expiry, "%d", &expiry)
		inv.ExpiresAt = time.Unix(created+expiry, 0)
	}

	return inv
}

// base64ToHex converts a base64 (standard or URL-safe) string to hex.
// Returns the input unchanged if decoding fails.
func base64ToHex(b64 string) string {
	// Try standard base64 first, then URL-safe.
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		data, err = base64.URLEncoding.DecodeString(b64)
		if err != nil {
			// Might already be hex.
			return b64
		}
	}
	return hex.EncodeToString(data)
}
