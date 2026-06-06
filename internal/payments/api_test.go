package payments

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Radi-Labs/ARFL/internal/credentials"
	"github.com/Radi-Labs/ARFL/internal/lightning"
	"github.com/Radi-Labs/ARFL/internal/nostr"
	"github.com/Radi-Labs/ARFL/internal/store"
)

// testEnv bundles all dependencies for a test.
type testEnv struct {
	store  *store.Store
	mock   *lightning.MockClient
	issuer credentials.Issuer
	api    *PurchaseAPI
	server *httptest.Server
}

func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()

	// Temp database.
	tmpDir, err := os.MkdirTemp("", "arfl-api-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	s, err := store.Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	mock := lightning.NewMockClient()
	secret := []byte("test-secret-key-for-hmac-32bytes!")
	issuer := credentials.NewHMACIssuer("key-1", secret)

	api := NewPurchaseAPI(s, mock, issuer)
	api.StartSettlementListener(context.Background())
	t.Cleanup(func() { api.Stop() })

	server := httptest.NewServer(api.Handler())
	t.Cleanup(func() { server.Close() })

	return &testEnv{
		store:  s,
		mock:   mock,
		issuer: issuer,
		api:    api,
		server: server,
	}
}

// --- POST /purchase ---

func TestPurchase_Success(t *testing.T) {
	env := setupTestEnv(t)

	body := `{"tier_id": "1gb"}`
	resp, err := http.Post(env.server.URL+"/purchase", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /purchase: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	var result PurchaseResponse
	json.NewDecoder(resp.Body).Decode(&result)

	if result.PaymentHash == "" {
		t.Error("payment_hash should not be empty")
	}
	if result.PaymentRequest == "" {
		t.Error("payment_request should not be empty")
	}
	if result.AmountSats != 500 {
		t.Errorf("expected 500 sats, got %d", result.AmountSats)
	}
	if result.Tier != "1gb" {
		t.Errorf("expected tier 1gb, got %s", result.Tier)
	}
}

func TestPurchase_AllTiers(t *testing.T) {
	env := setupTestEnv(t)

	tiers := []struct {
		id   string
		sats int64
	}{
		{"1gb", 500},
		{"10gb", 4000},
		{"50gb", 15000},
	}

	for _, tier := range tiers {
		body := fmt.Sprintf(`{"tier_id": "%s"}`, tier.id)
		resp, _ := http.Post(env.server.URL+"/purchase", "application/json", strings.NewReader(body))
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusCreated {
			t.Errorf("tier %s: expected 201, got %d", tier.id, resp.StatusCode)
			continue
		}

		var result PurchaseResponse
		json.NewDecoder(resp.Body).Decode(&result)
		if result.AmountSats != tier.sats {
			t.Errorf("tier %s: expected %d sats, got %d", tier.id, tier.sats, result.AmountSats)
		}
	}
}

func TestPurchase_UnknownTier(t *testing.T) {
	env := setupTestEnv(t)

	body := `{"tier_id": "999tb"}`
	resp, _ := http.Post(env.server.URL+"/purchase", "application/json", strings.NewReader(body))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestPurchase_InvalidJSON(t *testing.T) {
	env := setupTestEnv(t)

	resp, _ := http.Post(env.server.URL+"/purchase", "application/json", strings.NewReader("{bad"))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestPurchase_WrongMethod(t *testing.T) {
	env := setupTestEnv(t)

	resp, _ := http.Get(env.server.URL + "/purchase")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}
}

func TestPurchase_LNDDown(t *testing.T) {
	env := setupTestEnv(t)
	env.mock.CreateInvoiceErr = fmt.Errorf("lnd: connection refused")

	body := `{"tier_id": "1gb"}`
	resp, _ := http.Post(env.server.URL+"/purchase", "application/json", strings.NewReader(body))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
}

// --- GET /purchase/:id ---

func TestPurchaseStatus_Open(t *testing.T) {
	env := setupTestEnv(t)

	// Create a purchase.
	body := `{"tier_id": "1gb"}`
	resp, _ := http.Post(env.server.URL+"/purchase", "application/json", strings.NewReader(body))
	var purchase PurchaseResponse
	json.NewDecoder(resp.Body).Decode(&purchase)
	resp.Body.Close()

	// Poll status — should be open.
	resp, _ = http.Get(env.server.URL + "/purchase/" + purchase.PaymentHash)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var status PurchaseStatusResponse
	json.NewDecoder(resp.Body).Decode(&status)

	if status.Status != "open" {
		t.Errorf("expected open, got %s", status.Status)
	}
	if status.Tickets != nil {
		t.Error("should not have tickets when open")
	}
}

func TestPurchaseStatus_Settled_DeliversTickets(t *testing.T) {
	env := setupTestEnv(t)

	// Create and pay.
	body := `{"tier_id": "1gb"}`
	resp, _ := http.Post(env.server.URL+"/purchase", "application/json", strings.NewReader(body))
	var purchase PurchaseResponse
	json.NewDecoder(resp.Body).Decode(&purchase)
	resp.Body.Close()

	// Simulate payment.
	env.mock.SimulateSettlement(purchase.PaymentHash)
	// Give the settlement listener time to process.
	time.Sleep(100 * time.Millisecond)

	// Poll status — should be settled with tickets.
	resp, _ = http.Get(env.server.URL + "/purchase/" + purchase.PaymentHash)
	defer resp.Body.Close()

	var status PurchaseStatusResponse
	json.NewDecoder(resp.Body).Decode(&status)

	if status.Status != "settled" {
		t.Fatalf("expected settled, got %s", status.Status)
	}
	if len(status.Tickets) != 10 {
		t.Fatalf("expected 10 tickets (1gb tier), got %d", len(status.Tickets))
	}
	for _, ticket := range status.Tickets {
		if ticket.ID == "" {
			t.Error("ticket ID should not be empty")
		}
		if ticket.Bytes != 100_000_000 {
			t.Errorf("expected 100MB per ticket, got %d", ticket.Bytes)
		}
		if ticket.MAC == "" {
			t.Error("ticket MAC should not be empty")
		}
	}
}

func TestPurchaseStatus_NotFound(t *testing.T) {
	env := setupTestEnv(t)

	resp, _ := http.Get(env.server.URL + "/purchase/nonexistent")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestPurchaseStatus_MissingHash(t *testing.T) {
	env := setupTestEnv(t)

	resp, _ := http.Get(env.server.URL + "/purchase/")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestPurchaseStatus_WrongMethod(t *testing.T) {
	env := setupTestEnv(t)

	resp, _ := http.Post(env.server.URL+"/purchase/abc", "application/json", nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}
}

// --- Idempotent settlement ---

func TestSettlement_Idempotent_NoDoubleTickets(t *testing.T) {
	env := setupTestEnv(t)

	body := `{"tier_id": "1gb"}`
	resp, _ := http.Post(env.server.URL+"/purchase", "application/json", strings.NewReader(body))
	var purchase PurchaseResponse
	json.NewDecoder(resp.Body).Decode(&purchase)
	resp.Body.Close()

	// Settle once.
	env.mock.SimulateSettlement(purchase.PaymentHash)
	time.Sleep(100 * time.Millisecond)

	// Check ticket count.
	count1, _ := env.store.CountTicketsByPaymentHash(purchase.PaymentHash)

	// Attempt to process again (simulating re-delivery).
	// The onInvoiceSettled method should detect existing tickets and skip.
	inv := &lightning.Invoice{
		PaymentHash: purchase.PaymentHash,
		Status:      lightning.InvoiceSettled,
	}
	env.api.onInvoiceSettled(inv)

	count2, _ := env.store.CountTicketsByPaymentHash(purchase.PaymentHash)

	if count1 != count2 {
		t.Fatalf("double settlement created extra tickets: %d → %d", count1, count2)
	}
	if count1 != 10 {
		t.Fatalf("expected 10 tickets, got %d", count1)
	}
}

// --- POST /report ---

func TestReport_ValidSignature(t *testing.T) {
	env := setupTestEnv(t)

	// Create a purchase and settle it so we have a real ticket_id.
	body := `{"tier_id": "1gb"}`
	resp, _ := http.Post(env.server.URL+"/purchase", "application/json", strings.NewReader(body))
	var purchase PurchaseResponse
	json.NewDecoder(resp.Body).Decode(&purchase)
	resp.Body.Close()

	env.mock.SimulateSettlement(purchase.PaymentHash)
	time.Sleep(100 * time.Millisecond)

	// Get the actual ticket IDs.
	resp, _ = http.Get(env.server.URL + "/purchase/" + purchase.PaymentHash)
	var status PurchaseStatusResponse
	json.NewDecoder(resp.Body).Decode(&status)
	resp.Body.Close()

	if len(status.Tickets) == 0 {
		t.Fatal("no tickets issued")
	}
	ticketID := status.Tickets[0].ID

	kp, _ := nostr.GenerateKeyPair()
	report := &UsageReport{
		SessionID:     "session-1",
		TicketID:      ticketID,
		NodeRole:      "entry",
		BytesReported: 50_000_000,
		ReportedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	report.Sign(kp)

	reqBody := ReportRequest{
		SessionID:     report.SessionID,
		TicketID:      report.TicketID,
		NodePubkey:    report.NodePubkey,
		NodeRole:      report.NodeRole,
		BytesReported: report.BytesReported,
		ReportedAt:    report.ReportedAt,
		Signature:     report.Signature,
	}

	b, _ := json.Marshal(reqBody)
	resp, _ = http.Post(env.server.URL+"/report", "application/json", bytes.NewReader(b))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}
}

func TestReport_InvalidSignature(t *testing.T) {
	env := setupTestEnv(t)

	reqBody := ReportRequest{
		SessionID:     "session-1",
		TicketID:      "ticket-1",
		NodePubkey:    "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		NodeRole:      "entry",
		BytesReported: 50_000_000,
		ReportedAt:    time.Now().UTC().Format(time.RFC3339),
		Signature:     "0000000000000000000000000000000000000000000000000000000000000000" +
			"0000000000000000000000000000000000000000000000000000000000000000",
	}

	b, _ := json.Marshal(reqBody)
	resp, _ := http.Post(env.server.URL+"/report", "application/json", bytes.NewReader(b))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestReport_MissingFields(t *testing.T) {
	env := setupTestEnv(t)

	// Missing session_id.
	reqBody := ReportRequest{
		TicketID:      "ticket-1",
		NodePubkey:    "abc",
		NodeRole:      "entry",
		BytesReported: 100,
		ReportedAt:    time.Now().UTC().Format(time.RFC3339),
		Signature:     "deadbeef",
	}

	b, _ := json.Marshal(reqBody)
	resp, _ := http.Post(env.server.URL+"/report", "application/json", bytes.NewReader(b))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing fields, got %d", resp.StatusCode)
	}
}

func TestReport_InvalidJSON(t *testing.T) {
	env := setupTestEnv(t)

	resp, _ := http.Post(env.server.URL+"/report", "application/json", strings.NewReader("{bad"))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestReport_WrongMethod(t *testing.T) {
	env := setupTestEnv(t)

	resp, _ := http.Get(env.server.URL + "/report")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}
}

// --- Rate limiting ---

func TestPurchase_RateLimit(t *testing.T) {
	env := setupTestEnv(t)

	body := `{"tier_id": "1gb"}`
	// Exhaust the 10-per-minute rate limit.
	for i := 0; i < 10; i++ {
		resp, _ := http.Post(env.server.URL+"/purchase", "application/json", strings.NewReader(body))
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("request %d: expected 201, got %d", i+1, resp.StatusCode)
		}
	}

	// 11th request should be rate-limited.
	resp, _ := http.Post(env.server.URL+"/purchase", "application/json", strings.NewReader(body))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", resp.StatusCode)
	}
}

// --- Full purchase lifecycle ---

func TestFullLifecycle_PurchasePayPollGetTickets(t *testing.T) {
	env := setupTestEnv(t)

	// Step 1: Purchase.
	body := `{"tier_id": "10gb"}`
	resp, _ := http.Post(env.server.URL+"/purchase", "application/json", strings.NewReader(body))
	var purchase PurchaseResponse
	json.NewDecoder(resp.Body).Decode(&purchase)
	resp.Body.Close()

	if purchase.AmountSats != 4000 {
		t.Fatalf("expected 4000 sats, got %d", purchase.AmountSats)
	}

	// Step 2: Check open.
	resp, _ = http.Get(env.server.URL + "/purchase/" + purchase.PaymentHash)
	var status PurchaseStatusResponse
	json.NewDecoder(resp.Body).Decode(&status)
	resp.Body.Close()
	if status.Status != "open" {
		t.Fatalf("expected open, got %s", status.Status)
	}

	// Step 3: Pay.
	env.mock.SimulateSettlement(purchase.PaymentHash)
	time.Sleep(100 * time.Millisecond)

	// Step 4: Check settled + tickets.
	resp, _ = http.Get(env.server.URL + "/purchase/" + purchase.PaymentHash)
	json.NewDecoder(resp.Body).Decode(&status)
	resp.Body.Close()

	if status.Status != "settled" {
		t.Fatalf("expected settled, got %s", status.Status)
	}
	if len(status.Tickets) != 100 {
		t.Fatalf("expected 100 tickets (10gb tier), got %d", len(status.Tickets))
	}

	// Verify economic invariant: total purchased sats matches.
	totalPurchased, _ := env.store.TotalPurchasedSats()
	if totalPurchased != 4000 {
		t.Fatalf("expected 4000 total purchased sats, got %d", totalPurchased)
	}
}
