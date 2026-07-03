package lightning

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeLND is a test double that simulates LND's REST API.
type fakeLND struct {
	mu       sync.Mutex
	invoices map[string]lndInvoice // r_hash (base64) → invoice
	macaroon string                // expected macaroon hex

	// Track subscribe connections for test control.
	subWriters []http.ResponseWriter
	subFlush   []http.Flusher
}

func newFakeLND(macaroon string) *fakeLND {
	return &fakeLND{
		invoices: make(map[string]lndInvoice),
		macaroon: macaroon,
	}
}

func (f *fakeLND) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/invoices", f.handleAddInvoice)
	mux.HandleFunc("/v1/invoice/", f.handleLookupInvoice)
	mux.HandleFunc("/v1/invoices/subscribe", f.handleSubscribe)
	mux.HandleFunc("/v2/router/send", f.handleSendPayment)
	return mux
}

func (f *fakeLND) checkMacaroon(w http.ResponseWriter, r *http.Request) bool {
	mac := r.Header.Get("Grpc-Metadata-macaroon")
	if mac != f.macaroon {
		http.Error(w, `{"error":"permission denied","message":"invalid macaroon"}`, http.StatusUnauthorized)
		return false
	}
	return true
}

func (f *fakeLND) handleAddInvoice(w http.ResponseWriter, r *http.Request) {
	if !f.checkMacaroon(w, r) {
		return
	}
	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Value  json.Number `json:"value"`
		Memo   string      `json:"memo"`
		Expiry json.Number `json:"expiry"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Generate a deterministic hash from the memo for test predictability.
	rHash := make([]byte, 32)
	copy(rHash, []byte("test-hash-"+req.Memo))
	rHashB64 := base64.StdEncoding.EncodeToString(rHash)

	now := time.Now()
	exp, _ := req.Expiry.Int64()

	inv := lndInvoice{
		Memo:           req.Memo,
		RHash:          rHashB64,
		Value:          req.Value.String(),
		State:          "OPEN",
		CreationDate:   fmt.Sprintf("%d", now.Unix()),
		Expiry:         fmt.Sprintf("%d", exp),
		PaymentRequest: "lnbcrt" + hex.EncodeToString(rHash[:8]),
	}

	f.mu.Lock()
	f.invoices[rHashB64] = inv
	f.mu.Unlock()

	json.NewEncoder(w).Encode(map[string]string{
		"r_hash":          rHashB64,
		"payment_request": inv.PaymentRequest,
		"add_index":       "1",
	})
}

func (f *fakeLND) handleLookupInvoice(w http.ResponseWriter, r *http.Request) {
	if !f.checkMacaroon(w, r) {
		return
	}

	// Extract hash from path: /v1/invoice/{r_hash_str}
	parts := strings.Split(r.URL.Path, "/v1/invoice/")
	if len(parts) < 2 || parts[1] == "" {
		http.Error(w, `{"message":"unable to locate invoice"}`, http.StatusNotFound)
		return
	}
	hashB64 := parts[1]

	// URL-safe base64 → standard base64 for lookup.
	hashBytes, err := base64.URLEncoding.DecodeString(hashB64)
	if err != nil {
		http.Error(w, `{"message":"unable to locate invoice"}`, http.StatusNotFound)
		return
	}
	stdB64 := base64.StdEncoding.EncodeToString(hashBytes)

	f.mu.Lock()
	inv, ok := f.invoices[stdB64]
	f.mu.Unlock()

	if !ok {
		http.Error(w, `{"message":"unable to locate invoice"}`, http.StatusNotFound)
		return
	}

	json.NewEncoder(w).Encode(inv)
}

func (f *fakeLND) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	if !f.checkMacaroon(w, r) {
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	f.mu.Lock()
	f.subWriters = append(f.subWriters, w)
	f.subFlush = append(f.subFlush, flusher)
	f.mu.Unlock()

	// Block until context is cancelled.
	<-r.Context().Done()
}

// settleInvoice simulates invoice settlement — notifies all subscribers.
func (f *fakeLND) settleInvoice(rHashB64 string) {
	f.mu.Lock()
	inv, ok := f.invoices[rHashB64]
	if ok {
		inv.State = "SETTLED"
		inv.SettleDate = fmt.Sprintf("%d", time.Now().Unix())
		f.invoices[rHashB64] = inv
	}

	// Write to all subscriber streams.
	for i, w := range f.subWriters {
		line, _ := json.Marshal(map[string]interface{}{"result": inv})
		fmt.Fprintf(w, "%s\n", line)
		f.subFlush[i].Flush()
	}
	f.mu.Unlock()
}

func (f *fakeLND) handleSendPayment(w http.ResponseWriter, r *http.Request) {
	if !f.checkMacaroon(w, r) {
		return
	}

	var req map[string]interface{}
	json.NewDecoder(r.Body).Decode(&req)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Simulate a successful payment.
	payHash := base64.StdEncoding.EncodeToString([]byte("payment-hash-result-000000000000"))

	result := map[string]interface{}{
		"result": map[string]interface{}{
			"payment_hash": payHash,
			"status":       "SUCCEEDED",
			"fee_sat":      "2",
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	line, _ := json.Marshal(result)
	fmt.Fprintf(w, "%s\n", line)
	flusher.Flush()
}

// --- Test helpers ---

func setupTestLND(t *testing.T) (*LNDClient, *fakeLND) {
	t.Helper()
	macaroon := "deadbeef1234"
	fake := newFakeLND(macaroon)

	srv := httptest.NewTLSServer(fake.handler())
	t.Cleanup(srv.Close)

	client := newLNDClientDirect(srv.URL, macaroon, srv.Client())
	return client, fake
}

// --- Interface compliance ---

func TestLNDClient_ImplementsClient(t *testing.T) {
	var _ Client = (*LNDClient)(nil)
}

// --- CreateInvoice ---

func TestLND_CreateInvoice_Success(t *testing.T) {
	client, _ := setupTestLND(t)
	ctx := context.Background()

	inv, err := client.CreateInvoice(ctx, 500, "bandwidth-1gb", 10*time.Minute)
	if err != nil {
		t.Fatalf("CreateInvoice: %v", err)
	}
	if inv.AmountSats != 500 {
		t.Errorf("amount = %d, want 500", inv.AmountSats)
	}
	if inv.Memo != "bandwidth-1gb" {
		t.Errorf("memo = %q, want %q", inv.Memo, "bandwidth-1gb")
	}
	if inv.Status != InvoiceOpen {
		t.Errorf("status = %s, want open", inv.Status)
	}
	if inv.PaymentHash == "" {
		t.Error("payment hash is empty")
	}
	if !strings.HasPrefix(inv.PaymentRequest, "lnbcrt") {
		t.Errorf("payment request = %q, want lnbcrt prefix", inv.PaymentRequest)
	}
}

func TestLND_CreateInvoice_BadMacaroon(t *testing.T) {
	macaroon := "deadbeef1234"
	fake := newFakeLND(macaroon)
	srv := httptest.NewTLSServer(fake.handler())
	t.Cleanup(srv.Close)

	// Wrong macaroon.
	client := newLNDClientDirect(srv.URL, "wrongmacaroon", srv.Client())

	_, err := client.CreateInvoice(context.Background(), 500, "test", time.Minute)
	if err == nil {
		t.Fatal("expected auth error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 error, got: %v", err)
	}
}

// --- LookupInvoice ---

func TestLND_LookupInvoice_Found(t *testing.T) {
	client, _ := setupTestLND(t)
	ctx := context.Background()

	inv, err := client.CreateInvoice(ctx, 250, "lookup-test", 5*time.Minute)
	if err != nil {
		t.Fatalf("CreateInvoice: %v", err)
	}

	found, err := client.LookupInvoice(ctx, inv.PaymentHash)
	if err != nil {
		t.Fatalf("LookupInvoice: %v", err)
	}
	if found.Memo != "lookup-test" {
		t.Errorf("memo = %q, want %q", found.Memo, "lookup-test")
	}
	if found.Status != InvoiceOpen {
		t.Errorf("status = %s, want open", found.Status)
	}
}

func TestLND_LookupInvoice_NotFound(t *testing.T) {
	client, _ := setupTestLND(t)

	_, err := client.LookupInvoice(context.Background(), hex.EncodeToString(make([]byte, 32)))
	if err != ErrInvoiceNotFound {
		t.Fatalf("expected ErrInvoiceNotFound, got: %v", err)
	}
}

func TestLND_LookupInvoice_Settled(t *testing.T) {
	client, fake := setupTestLND(t)
	ctx := context.Background()

	inv, _ := client.CreateInvoice(ctx, 500, "settle-test", 5*time.Minute)

	// Settle it server-side.
	hashBytes, _ := hex.DecodeString(inv.PaymentHash)
	rHashB64 := base64.StdEncoding.EncodeToString(hashBytes)
	fake.settleInvoice(rHashB64)

	found, err := client.LookupInvoice(ctx, inv.PaymentHash)
	if err != nil {
		t.Fatalf("LookupInvoice: %v", err)
	}
	if found.Status != InvoiceSettled {
		t.Errorf("status = %s, want settled", found.Status)
	}
}

// --- SubscribeInvoices ---

func TestLND_SubscribeInvoices_ReceivesSettlement(t *testing.T) {
	client, fake := setupTestLND(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := client.SubscribeInvoices(ctx)
	if err != nil {
		t.Fatalf("SubscribeInvoices: %v", err)
	}

	// Create and settle an invoice.
	inv, _ := client.CreateInvoice(ctx, 500, "sub-test", 5*time.Minute)

	// Give the subscribe goroutine time to connect.
	time.Sleep(200 * time.Millisecond)

	hashBytes, _ := hex.DecodeString(inv.PaymentHash)
	rHashB64 := base64.StdEncoding.EncodeToString(hashBytes)
	fake.settleInvoice(rHashB64)

	select {
	case settled := <-ch:
		if settled == nil {
			t.Fatal("received nil invoice")
		}
		if settled.Status != InvoiceSettled {
			t.Errorf("status = %s, want settled", settled.Status)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for settlement")
	}
}

func TestLND_SubscribeInvoices_ContextCancellation(t *testing.T) {
	client, _ := setupTestLND(t)
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := client.SubscribeInvoices(ctx)
	if err != nil {
		t.Fatalf("SubscribeInvoices: %v", err)
	}

	// Give subscribe time to connect.
	time.Sleep(100 * time.Millisecond)
	cancel()

	// Channel should close.
	select {
	case _, ok := <-ch:
		if ok {
			// Might get a stale event, but eventually the channel closes.
		}
	case <-time.After(3 * time.Second):
		t.Fatal("channel did not close after context cancellation")
	}
}

// --- SendPayment ---

func TestLND_SendPayment_Success(t *testing.T) {
	client, _ := setupTestLND(t)
	ctx := context.Background()

	result, err := client.SendPayment(ctx, "lnbcrt500...", 500)
	if err != nil {
		t.Fatalf("SendPayment: %v", err)
	}
	if result.Status != PaymentSucceeded {
		t.Errorf("status = %s, want succeeded", result.Status)
	}
	if result.PaymentHash == "" {
		t.Error("payment hash is empty")
	}
	if result.FeeSats != 2 {
		t.Errorf("fee = %d, want 2", result.FeeSats)
	}
}

func TestLND_SendPayment_BadMacaroon(t *testing.T) {
	macaroon := "deadbeef1234"
	fake := newFakeLND(macaroon)
	srv := httptest.NewTLSServer(fake.handler())
	t.Cleanup(srv.Close)

	client := newLNDClientDirect(srv.URL, "wrong", srv.Client())

	_, err := client.SendPayment(context.Background(), "lnbcrt500...", 500)
	if err == nil {
		t.Fatal("expected auth error")
	}
}

func TestLND_SendPayment_ContextTimeout(t *testing.T) {
	// Server that blocks until signalled (simulates slow LND).
	done := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-done:
		}
	}))
	t.Cleanup(func() {
		close(done)
		srv.Close()
	})

	client := newLNDClientDirect(srv.URL, "mac", srv.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := client.SendPayment(ctx, "lnbcrt...", 500)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

// --- Keysend ---

func TestLND_Keysend_Success(t *testing.T) {
	client, _ := setupTestLND(t)
	ctx := context.Background()

	destPubkey := hex.EncodeToString(make([]byte, 33))
	result, err := client.Keysend(ctx, destPubkey, 100)
	if err != nil {
		t.Fatalf("Keysend: %v", err)
	}
	if result.Status != PaymentSucceeded {
		t.Errorf("status = %s, want succeeded", result.Status)
	}
}

// --- SendPayment failure simulation ---

func TestLND_SendPayment_Failed(t *testing.T) {
	// Server returns a FAILED payment.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Grpc-Metadata-macaroon") == "" {
			http.Error(w, "no auth", http.StatusUnauthorized)
			return
		}

		flusher := w.(http.Flusher)
		result := map[string]interface{}{
			"result": map[string]interface{}{
				"payment_hash":   base64.StdEncoding.EncodeToString(make([]byte, 32)),
				"status":         "FAILED",
				"failure_reason": "FAILURE_REASON_NO_ROUTE",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		line, _ := json.Marshal(result)
		fmt.Fprintf(w, "%s\n", line)
		flusher.Flush()
	}))
	t.Cleanup(srv.Close)

	client := newLNDClientDirect(srv.URL, "mac", srv.Client())

	result, err := client.SendPayment(context.Background(), "lnbcrt...", 500)
	if err != nil {
		t.Fatalf("SendPayment: %v (expected result, not error)", err)
	}
	if result.Status != PaymentFailed {
		t.Errorf("status = %s, want failed", result.Status)
	}
	if result.Error == "" {
		t.Error("expected error message on failed payment")
	}
}

// --- base64ToHex helper ---

func TestBase64ToHex(t *testing.T) {
	// Standard base64.
	data := []byte{0xde, 0xad, 0xbe, 0xef}
	b64 := base64.StdEncoding.EncodeToString(data)
	got := base64ToHex(b64)
	want := "deadbeef"
	if got != want {
		t.Errorf("base64ToHex(%q) = %q, want %q", b64, got, want)
	}

	// URL-safe base64.
	b64url := base64.URLEncoding.EncodeToString(data)
	got2 := base64ToHex(b64url)
	if got2 != want {
		t.Errorf("base64ToHex(%q) = %q, want %q", b64url, got2, want)
	}

	// Already hex — returned unchanged.
	got3 := base64ToHex("not-valid-base64!")
	if got3 != "not-valid-base64!" {
		t.Errorf("expected passthrough for non-base64")
	}
}

// --- Connection refused ---

func TestLND_CreateInvoice_ConnectionRefused(t *testing.T) {
	client := newLNDClientDirect("https://127.0.0.1:1", "mac", &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 2 * time.Second,
	})

	_, err := client.CreateInvoice(context.Background(), 500, "test", time.Minute)
	if err == nil {
		t.Fatal("expected connection error")
	}
}
