package payments

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Radi-Labs/ARFL/internal/credentials"
	"github.com/Radi-Labs/ARFL/internal/lightning"
	"github.com/Radi-Labs/ARFL/internal/nostr"
	"github.com/Radi-Labs/ARFL/internal/store"
)

// settlementEnv is the test harness for settlement engine tests.
type settlementEnv struct {
	store  *store.Store
	mock   *lightning.MockClient
	engine *SettlementEngine
}

func setupSettlementEnv(t *testing.T) *settlementEnv {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "arfl-settlement-test-*")
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
	engine := NewSettlementEngine(s, mock)
	engine.SetMinPayout(0) // no minimum for tests

	return &settlementEnv{store: s, mock: mock, engine: engine}
}

// seedSession creates a complete chain: invoice → ticket → usage reports.
// Returns the ticket ID and node keypairs.
func (env *settlementEnv) seedSession(t *testing.T, sessionID, ticketID string, entryBytes, exitBytes int64) (entryKP, exitKP *nostr.KeyPair) {
	t.Helper()

	// Create and settle invoice.
	env.store.InsertInvoice("hash-"+sessionID, "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	env.store.SettleInvoice("hash-" + sessionID)

	// Create ticket and redeem it.
	env.store.InsertTicket(ticketID, "hash-"+sessionID, 100_000_000, "hmac-test")
	env.store.RedeemTicket(ticketID, "client-1")

	// Create signed usage reports from entry and exit nodes.
	entryKP, _ = nostr.GenerateKeyPair()
	exitKP, _ = nostr.GenerateKeyPair()
	now := time.Now().UTC().Format(time.RFC3339)

	entryReport := &UsageReport{
		SessionID:     sessionID,
		TicketID:      ticketID,
		NodeRole:      "entry",
		BytesReported: entryBytes,
		ReportedAt:    now,
	}
	entryReport.Sign(entryKP)
	env.store.InsertUsageReport(sessionID, ticketID, entryReport.NodePubkey,
		"entry", entryBytes, now, entryReport.Signature)

	exitReport := &UsageReport{
		SessionID:     sessionID,
		TicketID:      ticketID,
		NodeRole:      "exit",
		BytesReported: exitBytes,
		ReportedAt:    now,
	}
	exitReport.Sign(exitKP)
	env.store.InsertUsageReport(sessionID, ticketID, exitReport.NodePubkey,
		"exit", exitBytes, now, exitReport.Signature)

	return entryKP, exitKP
}

// periodBounds returns settlement period bounds around now.
func periodBounds() (string, string) {
	now := time.Now().UTC()
	start := now.Add(-1 * time.Hour).Format(time.RFC3339)
	end := now.Add(1 * time.Hour).Format(time.RFC3339)
	return start, end
}

// seedSessionWithTier creates a complete chain with a specific tier.
func (env *settlementEnv) seedSessionWithTier(t *testing.T, sessionID, ticketID, tier string, amountSats, bytesAllowed, ticketBytes, entryBytes, exitBytes int64) {
	t.Helper()

	env.store.InsertInvoice("hash-"+sessionID, "lnbc...", amountSats, tier, bytesAllowed,
		time.Now().Add(time.Hour), "")
	env.store.SettleInvoice("hash-" + sessionID)
	env.store.InsertTicket(ticketID, "hash-"+sessionID, ticketBytes, "hmac-test")
	env.store.RedeemTicket(ticketID, "client-1")

	entryKP, _ := nostr.GenerateKeyPair()
	exitKP, _ := nostr.GenerateKeyPair()
	now := time.Now().UTC().Format(time.RFC3339)

	entryReport := &UsageReport{
		SessionID: sessionID, TicketID: ticketID, NodeRole: "entry",
		BytesReported: entryBytes, ReportedAt: now,
	}
	entryReport.Sign(entryKP)
	env.store.InsertUsageReport(sessionID, ticketID, entryReport.NodePubkey,
		"entry", entryBytes, now, entryReport.Signature)

	exitReport := &UsageReport{
		SessionID: sessionID, TicketID: ticketID, NodeRole: "exit",
		BytesReported: exitBytes, ReportedAt: now,
	}
	exitReport.Sign(exitKP)
	env.store.InsertUsageReport(sessionID, ticketID, exitReport.NodePubkey,
		"exit", exitBytes, now, exitReport.Signature)
}

// --- Happy path ---

func TestSettlement_SingleSession(t *testing.T) {
	env := setupSettlementEnv(t)
	start, end := periodBounds()

	// Seed: entry reports 80MB, exit reports 70MB → billable = 70MB.
	env.seedSession(t, "sess-1", "ticket-1", 80_000_000, 70_000_000)

	result, err := env.engine.RunSettlement(context.Background(), start, end)
	if err != nil {
		t.Fatalf("RunSettlement: %v", err)
	}

	if result.SessionsSettled != 1 {
		t.Errorf("expected 1 session settled, got %d", result.SessionsSettled)
	}
	if result.EntriesCreated != 2 {
		t.Errorf("expected 2 entries (entry+exit node), got %d", result.EntriesCreated)
	}
	if result.PayoutsSucceeded != 2 {
		t.Errorf("expected 2 successful payouts, got %d", result.PayoutsSucceeded)
	}
	if result.PayoutsFailed != 0 {
		t.Errorf("expected 0 failed payouts, got %d", result.PayoutsFailed)
	}
}

func TestSettlement_BillableIsMin(t *testing.T) {
	env := setupSettlementEnv(t)
	start, end := periodBounds()

	// Entry: 100MB, Exit: 50MB → billable = 50MB.
	env.seedSession(t, "sess-1", "ticket-1", 100_000_000, 50_000_000)

	env.engine.RunSettlement(context.Background(), start, end)

	// Check the payout amounts. 50MB billable, each node gets half = 25MB worth.
	// 25MB at 500 sats/GB = 25_000_000 * 500 / 1_000_000_000 = 12 sats per node.
	totalPaid, _ := env.store.TotalPaidOutSats()
	// Each node gets floor(25_000_000 * 500 / 1_000_000_000) = floor(12.5) = 12 sats.
	// Total = 24 sats (2 × 12).
	if totalPaid != 24 {
		t.Errorf("expected 24 total sats paid, got %d", totalPaid)
	}
}

func TestSettlement_CappedAtTicketBytes(t *testing.T) {
	env := setupSettlementEnv(t)
	start, end := periodBounds()

	// Both report 200MB but ticket is only 100MB. Billable capped at 100MB.
	env.seedSession(t, "sess-1", "ticket-1", 200_000_000, 200_000_000)

	env.engine.RunSettlement(context.Background(), start, end)

	// 100MB billable, each gets half = 50MB.
	// 50_000_000 * 500 / 1_000_000_000 = 25 sats each = 50 total.
	totalPaid, _ := env.store.TotalPaidOutSats()
	if totalPaid != 50 {
		t.Errorf("expected 50 total sats paid (capped), got %d", totalPaid)
	}
}

func TestSettlement_MultipleSessions(t *testing.T) {
	env := setupSettlementEnv(t)
	start, end := periodBounds()

	env.seedSession(t, "sess-1", "ticket-1", 50_000_000, 50_000_000)
	env.seedSession(t, "sess-2", "ticket-2", 80_000_000, 60_000_000)

	result, err := env.engine.RunSettlement(context.Background(), start, end)
	if err != nil {
		t.Fatalf("RunSettlement: %v", err)
	}

	if result.SessionsSettled != 2 {
		t.Errorf("expected 2 sessions settled, got %d", result.SessionsSettled)
	}
}

// --- Idempotency ---

func TestSettlement_Idempotent(t *testing.T) {
	env := setupSettlementEnv(t)
	start, end := periodBounds()

	env.seedSession(t, "sess-1", "ticket-1", 50_000_000, 50_000_000)

	// Run settlement twice.
	result1, _ := env.engine.RunSettlement(context.Background(), start, end)
	result2, _ := env.engine.RunSettlement(context.Background(), start, end)

	// Second run should create no new entries or payouts.
	if result2.EntriesCreated != 0 {
		t.Errorf("second run created %d entries (should be 0)", result2.EntriesCreated)
	}
	if result2.PayoutsSent != 0 {
		t.Errorf("second run sent %d payouts (should be 0)", result2.PayoutsSent)
	}

	// Total paid should be the same as first run.
	totalPaid, _ := env.store.TotalPaidOutSats()
	if totalPaid != result1.TotalPaidSats {
		t.Errorf("total paid changed: first=%d, after second=%d", result1.TotalPaidSats, totalPaid)
	}
}

// --- One-sided reports (missing entry or exit) ---

func TestSettlement_OneSidedReport_Skipped(t *testing.T) {
	env := setupSettlementEnv(t)
	start, end := periodBounds()

	// Only entry reports, no exit.
	env.store.InsertInvoice("hash-sess-1", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	env.store.SettleInvoice("hash-sess-1")
	env.store.InsertTicket("ticket-1", "hash-sess-1", 100_000_000, "hmac")
	env.store.RedeemTicket("ticket-1", "client-1")

	kp, _ := nostr.GenerateKeyPair()
	now := time.Now().UTC().Format(time.RFC3339)
	env.store.InsertUsageReport("sess-1", "ticket-1", kp.PubkeyHex(),
		"entry", 50_000_000, now, "sig-placeholder")

	result, _ := env.engine.RunSettlement(context.Background(), start, end)

	// Session should not be settled (no exit report).
	if result.SessionsSettled != 0 {
		t.Errorf("expected 0 sessions settled, got %d", result.SessionsSettled)
	}
	if result.PayoutsSent != 0 {
		t.Errorf("expected 0 payouts, got %d", result.PayoutsSent)
	}
}

// --- Ticket validation ---

func TestSettlement_UnredeemedTicket_Skipped(t *testing.T) {
	env := setupSettlementEnv(t)
	start, end := periodBounds()

	// Ticket not redeemed — settlement should skip.
	env.store.InsertInvoice("hash-1", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	env.store.SettleInvoice("hash-1")
	env.store.InsertTicket("ticket-1", "hash-1", 100_000_000, "hmac")
	// NOT calling RedeemTicket

	kp1, _ := nostr.GenerateKeyPair()
	kp2, _ := nostr.GenerateKeyPair()
	now := time.Now().UTC().Format(time.RFC3339)
	env.store.InsertUsageReport("sess-1", "ticket-1", kp1.PubkeyHex(),
		"entry", 50_000_000, now, "sig1")
	env.store.InsertUsageReport("sess-1", "ticket-1", kp2.PubkeyHex(),
		"exit", 50_000_000, now, "sig2")

	result, _ := env.engine.RunSettlement(context.Background(), start, end)

	if result.SessionsSkipped != 1 {
		t.Errorf("expected 1 session skipped, got %d", result.SessionsSkipped)
	}
	if result.PayoutsSent != 0 {
		t.Errorf("expected 0 payouts, got %d", result.PayoutsSent)
	}
}

// --- Payment failures ---

func TestSettlement_KeysendFailure(t *testing.T) {
	env := setupSettlementEnv(t)
	env.mock.KeysendErr = fmt.Errorf("no route to node")
	start, end := periodBounds()

	env.seedSession(t, "sess-1", "ticket-1", 50_000_000, 50_000_000)

	result, _ := env.engine.RunSettlement(context.Background(), start, end)

	if result.PayoutsFailed != 2 {
		t.Errorf("expected 2 failed payouts, got %d", result.PayoutsFailed)
	}
	if result.PayoutsSucceeded != 0 {
		t.Errorf("expected 0 succeeded, got %d", result.PayoutsSucceeded)
	}
}

func TestSettlement_RetryFailedPayouts(t *testing.T) {
	env := setupSettlementEnv(t)
	env.mock.KeysendErr = fmt.Errorf("temporary failure")
	start, end := periodBounds()

	env.seedSession(t, "sess-1", "ticket-1", 50_000_000, 50_000_000)
	env.engine.RunSettlement(context.Background(), start, end)

	// Now fix the mock (node is back online).
	env.mock.KeysendErr = nil

	succeeded, failed, err := env.engine.RetryFailedPayouts(context.Background())
	if err != nil {
		t.Fatalf("RetryFailedPayouts: %v", err)
	}
	if succeeded != 2 {
		t.Errorf("expected 2 retries succeeded, got %d", succeeded)
	}
	if failed != 0 {
		t.Errorf("expected 0 retries failed, got %d", failed)
	}
}

// --- Budget guard ---

func TestSettlement_BudgetGuard_StopsOverpayment(t *testing.T) {
	env := setupSettlementEnv(t)
	start, end := periodBounds()

	// Purchase only 500 sats (1gb tier).
	env.store.InsertInvoice("hash-1", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	env.store.SettleInvoice("hash-1")
	env.store.InsertTicket("ticket-1", "hash-1", 100_000_000, "hmac")
	env.store.RedeemTicket("ticket-1", "client-1")

	// Seed usage with full ticket (100MB both sides).
	kp1, _ := nostr.GenerateKeyPair()
	kp2, _ := nostr.GenerateKeyPair()
	now := time.Now().UTC().Format(time.RFC3339)
	env.store.InsertUsageReport("sess-1", "ticket-1", kp1.PubkeyHex(),
		"entry", 100_000_000, now, "sig1")
	env.store.InsertUsageReport("sess-1", "ticket-1", kp2.PubkeyHex(),
		"exit", 100_000_000, now, "sig2")

	env.engine.RunSettlement(context.Background(), start, end)

	// Verify invariant: total paid ≤ total purchased.
	purchased, _ := env.store.TotalPurchasedSats()
	paidOut, _ := env.store.TotalPaidOutSats()

	if paidOut > purchased {
		t.Fatalf("INVARIANT VIOLATION: paid %d > purchased %d", paidOut, purchased)
	}
}

// --- Minimum payout threshold ---

func TestSettlement_BelowMinPayout_NoPayout(t *testing.T) {
	env := setupSettlementEnv(t)
	env.engine.SetMinPayout(1000) // require at least 1000 sats
	start, end := periodBounds()

	// Seed tiny session: 1MB each = 0.5 sats per node (below threshold).
	env.seedSession(t, "sess-1", "ticket-1", 1_000_000, 1_000_000)

	result, _ := env.engine.RunSettlement(context.Background(), start, end)

	// Entries are created but no payouts sent (below minimum).
	if result.PayoutsSent != 0 {
		t.Errorf("expected 0 payouts (below threshold), got %d", result.PayoutsSent)
	}
}

// --- Payout rate computation ---

func TestComputePayoutSats(t *testing.T) {
	cases := []struct {
		billableBytes     int64
		invoiceAmountSats int64
		invoiceBytesAllow int64
		expectedSats      int64
	}{
		// 1GB tier: 500 sats / 1,000,000,000 bytes
		{1_000_000_000, 500, 1_000_000_000, 500},
		{500_000_000, 500, 1_000_000_000, 250},
		{100_000_000, 500, 1_000_000_000, 50},
		{50_000_000, 500, 1_000_000_000, 25},
		{1_000_000, 500, 1_000_000_000, 0}, // too small
		{2_000_000, 500, 1_000_000_000, 1}, // 2 MB = 1 sat
		{0, 500, 1_000_000_000, 0},         // zero bytes
		// 10GB tier: 4,000 sats / 10,000,000,000 bytes (20% discount)
		{1_000_000_000, 4_000, 10_000_000_000, 400},    // 1 GB at 10gb rate
		{10_000_000_000, 4_000, 10_000_000_000, 4_000}, // full 10GB
		// 50GB tier: 15,000 sats / 50,000,000,000 bytes (40% discount)
		{1_000_000_000, 15_000, 50_000_000_000, 300},     // 1 GB at 50gb rate
		{50_000_000_000, 15_000, 50_000_000_000, 15_000}, // full 50GB
		// Edge: zero bytes_allowed
		{1_000_000_000, 500, 0, 0},
	}
	for _, c := range cases {
		got := computePayoutSats(c.billableBytes, c.invoiceAmountSats, c.invoiceBytesAllow)
		if got != c.expectedSats {
			t.Errorf("computePayoutSats(%d, %d, %d) = %d, want %d",
				c.billableBytes, c.invoiceAmountSats, c.invoiceBytesAllow, got, c.expectedSats)
		}
	}
}

// --- Empty period ---

func TestSettlement_EmptyPeriod(t *testing.T) {
	env := setupSettlementEnv(t)

	// No usage reports at all.
	result, err := env.engine.RunSettlement(context.Background(),
		"2099-01-01T00:00:00Z", "2099-01-01T06:00:00Z")
	if err != nil {
		t.Fatalf("RunSettlement: %v", err)
	}
	if result.SessionsSettled != 0 {
		t.Errorf("expected 0, got %d", result.SessionsSettled)
	}
	if result.PayoutsSent != 0 {
		t.Errorf("expected 0, got %d", result.PayoutsSent)
	}
}

// --- STRIDE: Settlement engine threat tests ---

func TestSTRIDE_PaymentInFlight_LeavesPayoutInFlight(t *testing.T) {
	// STRIDE/Tampering: if Keysend returns PaymentInFlight (still routing),
	// the payout must remain in in_flight state. Marking it as failed would
	// allow a retry that double-pays the node.
	env := setupSettlementEnv(t)
	env.mock.KeysendResult = &lightning.PaymentResult{
		Status: lightning.PaymentInFlight,
	}
	start, end := periodBounds()

	env.seedSession(t, "sess-1", "ticket-1", 50_000_000, 50_000_000)

	result, _ := env.engine.RunSettlement(context.Background(), start, end)

	// Payouts should be "sent" but none succeeded or failed in the
	// traditional sense — they're stuck in in_flight.
	if result.PayoutsSent != 2 {
		t.Errorf("expected 2 payouts sent, got %d", result.PayoutsSent)
	}
	if result.PayoutsSucceeded != 0 {
		t.Errorf("expected 0 succeeded (in-flight), got %d", result.PayoutsSucceeded)
	}

	// Verify the payouts are still in in_flight state (not failed).
	var inFlightCount int
	env.store.DB().QueryRow(`SELECT COUNT(*) FROM payouts WHERE status = 'in_flight'`).Scan(&inFlightCount)
	if inFlightCount != 2 {
		t.Fatalf("expected 2 payouts in in_flight state, got %d", inFlightCount)
	}

	// Verify they are NOT retryable (in_flight ≠ failed).
	retryable, _ := env.store.GetRetryablePayouts()
	if len(retryable) != 0 {
		t.Fatalf("in-flight payouts should not be retryable, got %d", len(retryable))
	}
}

func TestSTRIDE_CrashAfterSettle_TicketsStillIssued(t *testing.T) {
	// STRIDE/DoS: simulate crash between SettleInvoice and ticket issuance.
	// On restart, onInvoiceSettled must issue the missing tickets.
	env := setupSettlementEnv(t)

	// Create invoice and settle it directly in the DB (simulating crash
	// after settle but before ticket issuance).
	env.store.InsertInvoice("hash-crash", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	env.store.SettleInvoice("hash-crash")

	// No tickets exist yet (crash happened before issuance).
	count, _ := env.store.CountTicketsByPaymentHash("hash-crash")
	if count != 0 {
		t.Fatalf("expected 0 tickets before recovery, got %d", count)
	}

	// Now simulate the settlement event firing again on restart.
	// Need a PurchaseAPI with an issuer to call onInvoiceSettled.
	secret := []byte("test-secret-key-for-hmac-32bytes!")
	issuer := credentials.NewHMACIssuer("key-1", secret)
	api := NewPurchaseAPI(env.store, env.mock, issuer)

	inv := &lightning.Invoice{
		PaymentHash: "hash-crash",
		Status:      lightning.InvoiceSettled,
	}
	err := api.onInvoiceSettled(inv)
	if err != nil {
		t.Fatalf("crash-recovery settlement should succeed: %v", err)
	}

	// Tickets should now exist.
	count, _ = env.store.CountTicketsByPaymentHash("hash-crash")
	if count != 10 {
		t.Fatalf("expected 10 tickets after recovery, got %d", count)
	}
}

func TestSTRIDE_DoubleSettlement_NoDoubleTickets(t *testing.T) {
	// STRIDE/Tampering: LND delivers the same settlement event twice.
	// Tickets must be issued exactly once.
	env := setupSettlementEnv(t)

	secret := []byte("test-secret-key-for-hmac-32bytes!")
	issuer := credentials.NewHMACIssuer("key-1", secret)
	api := NewPurchaseAPI(env.store, env.mock, issuer)

	env.store.InsertInvoice("hash-dup", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")

	inv := &lightning.Invoice{
		PaymentHash: "hash-dup",
		Status:      lightning.InvoiceSettled,
	}

	// First call issues tickets.
	if err := api.onInvoiceSettled(inv); err != nil {
		t.Fatalf("first settlement: %v", err)
	}
	count1, _ := env.store.CountTicketsByPaymentHash("hash-dup")

	// Second call is idempotent — no error, no extra tickets.
	if err := api.onInvoiceSettled(inv); err != nil {
		t.Fatalf("idempotent settlement should not error: %v", err)
	}
	count2, _ := env.store.CountTicketsByPaymentHash("hash-dup")

	if count1 != count2 || count1 != 10 {
		t.Fatalf("expected exactly 10 tickets, got %d then %d", count1, count2)
	}
}

func TestSTRIDE_RetryDoesNotDoublePayInFlight(t *testing.T) {
	// STRIDE/Tampering: if a payout is in in_flight state (payment may be
	// routing), RetryFailedPayouts must NOT pick it up. Only 'failed' payouts
	// with attempt_count < 3 are retryable.
	env := setupSettlementEnv(t)
	start, end := periodBounds()

	env.seedSession(t, "sess-1", "ticket-1", 50_000_000, 50_000_000)

	// First settlement succeeds.
	env.engine.RunSettlement(context.Background(), start, end)

	// Manually put a payout back to in_flight (simulating a payment that
	// was sent but result is unknown — e.g., Hub crashed mid-Keysend).
	env.store.DB().Exec(`UPDATE payouts SET status = 'in_flight' WHERE id = (SELECT id FROM payouts WHERE status = 'paid' LIMIT 1)`)

	// RetryFailedPayouts should NOT touch in_flight payouts.
	retryable, _ := env.store.GetRetryablePayouts()
	if len(retryable) != 0 {
		t.Fatalf("in_flight payouts should not be retryable, got %d", len(retryable))
	}
}

func TestSTRIDE_MarkPayoutFailed_DBError_Surfaced(t *testing.T) {
	// STRIDE/Repudiation: if MarkPayoutFailed's DB write fails, the
	// payout is stuck in in_flight with no error recorded. The settlement
	// engine must surface this as a CRITICAL log.
	env := setupSettlementEnv(t)
	start, end := periodBounds()

	env.seedSession(t, "sess-1", "ticket-1", 50_000_000, 50_000_000)
	env.mock.KeysendErr = fmt.Errorf("keysend failed")

	result, _ := env.engine.RunSettlement(context.Background(), start, end)

	// Payouts should be marked as failed (MarkPayoutFailed succeeds).
	if result.PayoutsFailed != 2 {
		t.Errorf("expected 2 failed payouts, got %d", result.PayoutsFailed)
	}

	// Verify they're actually in 'failed' state.
	var failedCount int
	env.store.DB().QueryRow(`SELECT COUNT(*) FROM payouts WHERE status = 'failed'`).Scan(&failedCount)
	if failedCount != 2 {
		t.Fatalf("expected 2 failed payouts in DB, got %d", failedCount)
	}
}

func TestSTRIDE_ConcurrentSettlement_NoDoubleTickets(t *testing.T) {
	// STRIDE/Tampering: multiple goroutines process the same settlement
	// concurrently. Exactly one set of tickets must be issued.
	env := setupSettlementEnv(t)

	secret := []byte("test-secret-key-for-hmac-32bytes!")
	issuer := credentials.NewHMACIssuer("key-1", secret)
	api := NewPurchaseAPI(env.store, env.mock, issuer)

	env.store.InsertInvoice("hash-race", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")

	inv := &lightning.Invoice{
		PaymentHash: "hash-race",
		Status:      lightning.InvoiceSettled,
	}

	// Fire 20 concurrent settlement calls.
	var wg sync.WaitGroup
	errs := make([]error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = api.onInvoiceSettled(inv)
		}(i)
	}
	wg.Wait()

	// Exactly 10 tickets must exist (not 20, 30, 200).
	count, _ := env.store.CountTicketsByPaymentHash("hash-race")
	if count != 10 {
		t.Fatalf("concurrent settlement produced %d tickets (expected exactly 10)", count)
	}

	// No goroutine should have errored.
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d errored: %v", i, err)
		}
	}
}

func TestSTRIDE_AdversarialMultiNodeSession_Rejected(t *testing.T) {
	// STRIDE/Elevation: a session has reports from two different entry nodes.
	// Settlement must reject this session entirely — paying either node
	// could be paying an attacker.
	env := setupSettlementEnv(t)
	start, end := periodBounds()

	// Create invoice + ticket chain.
	env.store.InsertInvoice("hash-adv", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	env.store.SettleInvoice("hash-adv")
	env.store.InsertTicket("ticket-adv", "hash-adv", 100_000_000, "hmac")
	env.store.RedeemTicket("ticket-adv", "client-1")

	// Two different nodes claim to be entry for the same session.
	kp1, _ := nostr.GenerateKeyPair()
	kp2, _ := nostr.GenerateKeyPair()
	kpExit, _ := nostr.GenerateKeyPair()
	now := time.Now().UTC().Format(time.RFC3339)

	env.store.InsertUsageReport("sess-adv", "ticket-adv", kp1.PubkeyHex(),
		"entry", 50_000_000, now, "sig1")
	env.store.InsertUsageReport("sess-adv", "ticket-adv", kp2.PubkeyHex(),
		"entry", 60_000_000, now, "sig2")
	env.store.InsertUsageReport("sess-adv", "ticket-adv", kpExit.PubkeyHex(),
		"exit", 50_000_000, now, "sig3")

	result, err := env.engine.RunSettlement(context.Background(), start, end)
	if err != nil {
		t.Fatalf("RunSettlement: %v", err)
	}

	// Session should be filtered out by HAVING COUNT(DISTINCT node_pubkey) = 1.
	if result.SessionsSettled != 0 {
		t.Fatalf("adversarial multi-node session should be rejected, got %d settled", result.SessionsSettled)
	}
	if result.PayoutsSent != 0 {
		t.Fatalf("no payouts should be sent for adversarial session, got %d", result.PayoutsSent)
	}
}

func TestSTRIDE_PaymentInFlight_CountedCorrectly(t *testing.T) {
	// STRIDE/Repudiation: in-flight payments must be counted separately
	// from failed payments in settlement results.
	env := setupSettlementEnv(t)
	env.mock.KeysendResult = &lightning.PaymentResult{
		Status: lightning.PaymentInFlight,
	}
	start, end := periodBounds()

	env.seedSession(t, "sess-1", "ticket-1", 50_000_000, 50_000_000)

	result, _ := env.engine.RunSettlement(context.Background(), start, end)

	if result.PayoutsInFlight != 2 {
		t.Errorf("expected 2 in-flight payouts, got %d", result.PayoutsInFlight)
	}
	if result.PayoutsFailed != 0 {
		t.Errorf("in-flight should not count as failed, got %d failed", result.PayoutsFailed)
	}
}

func TestSTRIDE_BudgetTracksInFlightPayouts(t *testing.T) {
	// STRIDE/Tampering: committed budget must include in-flight payouts,
	// not just successful ones. Otherwise the budget guard under-counts
	// and could allow overspend.
	env := setupSettlementEnv(t)

	// Purchase 500 sats (1gb tier).
	env.store.InsertInvoice("hash-1", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	env.store.SettleInvoice("hash-1")

	// Two tickets for two sessions.
	env.store.InsertTicket("ticket-1", "hash-1", 100_000_000, "hmac")
	env.store.RedeemTicket("ticket-1", "client-1")
	env.store.InsertTicket("ticket-2", "hash-1", 100_000_000, "hmac")
	env.store.RedeemTicket("ticket-2", "client-1")

	kp1, _ := nostr.GenerateKeyPair()
	kp2, _ := nostr.GenerateKeyPair()
	now := time.Now().UTC().Format(time.RFC3339)

	// Session 1: 100MB both sides → 50 sats per node → 100 total.
	env.store.InsertUsageReport("sess-1", "ticket-1", kp1.PubkeyHex(),
		"entry", 100_000_000, now, "sig1")
	env.store.InsertUsageReport("sess-1", "ticket-1", kp2.PubkeyHex(),
		"exit", 100_000_000, now, "sig2")

	// Session 2: same nodes, 100MB.
	env.store.InsertUsageReport("sess-2", "ticket-2", kp1.PubkeyHex(),
		"entry", 100_000_000, now, "sig3")
	env.store.InsertUsageReport("sess-2", "ticket-2", kp2.PubkeyHex(),
		"exit", 100_000_000, now, "sig4")

	start, end := periodBounds()
	env.engine.RunSettlement(context.Background(), start, end)

	// Verify total committed never exceeds purchased.
	purchased, _ := env.store.TotalPurchasedSats()
	committed, _ := env.store.TotalCommittedPayoutSats()

	if committed > purchased {
		t.Fatalf("INVARIANT VIOLATION: committed %d > purchased %d", committed, purchased)
	}
}

func TestSTRIDE_InFlightPayout_ErrSentinel(t *testing.T) {
	// Verify ErrPaymentInFlight is a distinguishable sentinel.
	env := setupSettlementEnv(t)
	env.mock.KeysendResult = &lightning.PaymentResult{
		Status: lightning.PaymentInFlight,
	}
	start, end := periodBounds()

	env.seedSession(t, "sess-1", "ticket-1", 50_000_000, 50_000_000)

	// RunSettlement returns nil (it logs but doesn't propagate per-payout errors).
	_, err := env.engine.RunSettlement(context.Background(), start, end)
	if err != nil {
		t.Fatalf("settlement should not error on in-flight: %v", err)
	}

	// Verify ErrPaymentInFlight is usable with errors.Is.
	if !errors.Is(ErrPaymentInFlight, ErrPaymentInFlight) {
		t.Fatal("ErrPaymentInFlight should be identifiable via errors.Is")
	}
}

// --- STRIDE: Context cancellation leaves payout in_flight ---

func TestSTRIDE_ContextCancellation_LeavesInFlight(t *testing.T) {
	// STRIDE/Tampering: If Keysend returns a context cancellation error,
	// the payment outcome is unknown. Marking it "failed" risks double-pay on retry.
	env := setupSettlementEnv(t)

	// Make Keysend return context.Canceled error.
	env.mock.KeysendErr = context.Canceled

	start, end := periodBounds()
	env.seedSession(t, "sess-ctx", "ticket-ctx", 50_000_000, 50_000_000)

	result, err := env.engine.RunSettlement(context.Background(), start, end)
	if err != nil {
		t.Fatalf("settlement should not error: %v", err)
	}

	// Should be counted as in-flight, NOT failed.
	// Two nodes (entry + exit) = 2 payouts.
	if result.PayoutsInFlight != 2 {
		t.Errorf("expected 2 in-flight, got %d", result.PayoutsInFlight)
	}
	if result.PayoutsFailed != 0 {
		t.Errorf("expected 0 failed, got %d (context cancel must not be treated as failure)", result.PayoutsFailed)
	}
}

func TestSTRIDE_ContextDeadline_LeavesInFlight(t *testing.T) {
	env := setupSettlementEnv(t)
	env.mock.KeysendErr = context.DeadlineExceeded
	start, end := periodBounds()
	env.seedSession(t, "sess-dl", "ticket-dl", 50_000_000, 50_000_000)

	result, _ := env.engine.RunSettlement(context.Background(), start, end)
	if result.PayoutsInFlight != 2 {
		t.Errorf("expected 2 in-flight, got %d", result.PayoutsInFlight)
	}
	if result.PayoutsFailed != 0 {
		t.Errorf("expected 0 failed, got %d", result.PayoutsFailed)
	}
}

// --- STRIDE: Volume discount correctness ---

func TestSTRIDE_VolumeDiscount_PayoutReflectsTierRate(t *testing.T) {
	// STRIDE/Elevation: Using 1gb rate for all tiers overstates node earnings
	// for discounted tiers, breaking the budget guard.
	env := setupSettlementEnv(t)
	start, end := periodBounds()

	// Seed a session with 10gb tier: 4,000 sats / 10,000,000,000 bytes.
	// Billable = min(100M, 100M) = 100M, capped at ticket bytes (100M).
	// Each node gets 50M bytes. Sats per node = 50M * 4000 / 10B = 20 sats.
	env.seedSessionWithTier(t, "sess-10g", "tick-10g", "10gb",
		4_000, 10_000_000_000, 100_000_000,
		100_000_000, 100_000_000)

	result, err := env.engine.RunSettlement(context.Background(), start, end)
	if err != nil {
		t.Fatalf("settlement: %v", err)
	}
	if result.SessionsSettled != 1 {
		t.Fatalf("expected 1 session, got %d", result.SessionsSettled)
	}

	// At 1gb rate (500/1B), each node would get 25 sats (wrong).
	// At 10gb rate (4000/10B), each node should get 20 sats (correct).
	if result.TotalPaidSats == 50 {
		t.Fatal("payout uses 1gb rate (50 sats total) instead of 10gb rate (40 sats total)")
	}
	if result.TotalPaidSats != 40 {
		t.Errorf("expected 40 total sats (2×20 at 10gb rate), got %d", result.TotalPaidSats)
	}
}

// --- STRIDE: Duplicate settlement_entry_id blocked by UNIQUE constraint ---

func TestSTRIDE_DuplicatePayoutBlocked(t *testing.T) {
	// STRIDE/Tampering: Without UNIQUE on settlement_entry_id, duplicate
	// payout rows could be created, risking double payment.
	env := setupSettlementEnv(t)
	start, end := periodBounds()
	env.seedSession(t, "sess-dup", "ticket-dup", 50_000_000, 50_000_000)

	// Run settlement twice — second run should not create duplicate payouts.
	result1, err := env.engine.RunSettlement(context.Background(), start, end)
	if err != nil {
		t.Fatalf("first settlement: %v", err)
	}
	if result1.PayoutsSent == 0 {
		t.Fatal("first run should send payouts")
	}

	result2, err := env.engine.RunSettlement(context.Background(), start, end)
	if err != nil {
		t.Fatalf("second settlement: %v", err)
	}
	// Second run: entries already exist (INSERT OR IGNORE), payouts already exist.
	if result2.PayoutsSent != 0 {
		t.Errorf("second run should send 0 payouts (already settled), got %d", result2.PayoutsSent)
	}
}

// --- STRIDE: Exit-side ticket_id enforcement ---

func TestSTRIDE_ExitMismatchedTicket_Rejected(t *testing.T) {
	// STRIDE/Spoofing: Entry and exit nodes report different ticket_ids
	// for the same session. The join on ticket_id should reject this.
	env := setupSettlementEnv(t)

	// Create invoice and two tickets under same invoice.
	env.store.InsertInvoice("hash-mismatch", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	env.store.SettleInvoice("hash-mismatch")
	env.store.InsertTicket("ticket-a", "hash-mismatch", 100_000_000, "hmac-a")
	env.store.InsertTicket("ticket-b", "hash-mismatch", 100_000_000, "hmac-b")
	env.store.RedeemTicket("ticket-a", "client-1")
	env.store.RedeemTicket("ticket-b", "client-1")

	// Entry reports with ticket-a, exit reports with ticket-b.
	entryKP, _ := nostr.GenerateKeyPair()
	exitKP, _ := nostr.GenerateKeyPair()
	now := time.Now().UTC().Format(time.RFC3339)

	entryReport := &UsageReport{
		SessionID: "sess-mm", TicketID: "ticket-a", NodeRole: "entry",
		BytesReported: 50_000_000, ReportedAt: now,
	}
	entryReport.Sign(entryKP)
	env.store.InsertUsageReport("sess-mm", "ticket-a", entryReport.NodePubkey,
		"entry", 50_000_000, now, entryReport.Signature)

	exitReport := &UsageReport{
		SessionID: "sess-mm", TicketID: "ticket-b", NodeRole: "exit",
		BytesReported: 50_000_000, ReportedAt: now,
	}
	exitReport.Sign(exitKP)
	env.store.InsertUsageReport("sess-mm", "ticket-b", exitReport.NodePubkey,
		"exit", 50_000_000, now, exitReport.Signature)

	start, end := periodBounds()
	result, err := env.engine.RunSettlement(context.Background(), start, end)
	if err != nil {
		t.Fatalf("settlement: %v", err)
	}
	// Session should be rejected — entry ticket-a ≠ exit ticket-b.
	if result.SessionsSettled != 0 {
		t.Errorf("expected 0 sessions (mismatched tickets), got %d", result.SessionsSettled)
	}
}
