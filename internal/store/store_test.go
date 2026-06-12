package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// --- Schema and trigger tests ---

func TestMigrateIdempotent(t *testing.T) {
	// Running migrate twice must not fail.
	s := testStore(t)
	if err := s.migrate(); err != nil {
		t.Fatalf("second migrate should be idempotent: %v", err)
	}
}

func TestAppendOnly_InvoiceDeleteBlocked(t *testing.T) {
	// Invariant: invoices table is append-only — DELETE is blocked by trigger.
	s := testStore(t)
	s.InsertInvoice("hash1", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "127.0.0.1")

	_, err := s.db.Exec(`DELETE FROM invoices WHERE payment_hash = 'hash1'`)
	if err == nil {
		t.Fatal("DELETE on invoices should be blocked by trigger")
	}
}

func TestAppendOnly_UsageReportUpdateBlocked(t *testing.T) {
	// Invariant: usage_reports is append-only — UPDATE is blocked.
	s := testStore(t)
	s.InsertInvoice("hash1", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	s.SettleInvoice("hash1")
	s.InsertTicket("t1", "hash1", 100_000_000, "hmac1")
	s.InsertUsageReport("sess1", "t1", "node1", "entry", 50_000_000,
		time.Now().Format(time.RFC3339), "sig1")

	_, err := s.db.Exec(`UPDATE usage_reports SET bytes_reported = 999 WHERE session_id = 'sess1'`)
	if err == nil {
		t.Fatal("UPDATE on usage_reports should be blocked by trigger")
	}
}

func TestAppendOnly_SettlementUpdateBlocked(t *testing.T) {
	// Invariant: settlement_entries is append-only — UPDATE blocked.
	s := testStore(t)
	s.InsertSettlementEntry("2026-06-06T00", "node1", 100_000_000, 500, 100_000_000, 100_000_000, 1)

	_, err := s.db.Exec(`UPDATE settlement_entries SET amount_sats = 9999 WHERE node_pubkey = 'node1'`)
	if err == nil {
		t.Fatal("UPDATE on settlement_entries should be blocked by trigger")
	}
}

func TestAppendOnly_SettlementDeleteBlocked(t *testing.T) {
	s := testStore(t)
	s.InsertSettlementEntry("2026-06-06T00", "node1", 100_000_000, 500, 100_000_000, 100_000_000, 1)

	_, err := s.db.Exec(`DELETE FROM settlement_entries WHERE node_pubkey = 'node1'`)
	if err == nil {
		t.Fatal("DELETE on settlement_entries should be blocked by trigger")
	}
}

func TestAppendOnly_CompensatingEntryUpdateBlocked(t *testing.T) {
	s := testStore(t)
	s.InsertCompensatingEntry("settlement", 1, -500, "overbilled")

	_, err := s.db.Exec(`UPDATE compensating_entries SET adjustment_sats = 0 WHERE id = 1`)
	if err == nil {
		t.Fatal("UPDATE on compensating_entries should be blocked by trigger")
	}
}

// --- Invoice lifecycle tests ---

func TestInvoice_Lifecycle(t *testing.T) {
	s := testStore(t)
	expires := time.Now().Add(time.Hour)

	// Insert
	if err := s.InsertInvoice("hash1", "lnbc500...", 500, "1gb", 1_000_000_000, expires, "1.2.3.4"); err != nil {
		t.Fatalf("insert invoice: %v", err)
	}

	// Read
	inv, err := s.GetInvoice("hash1")
	if err != nil {
		t.Fatalf("get invoice: %v", err)
	}
	if inv.Status != "open" {
		t.Errorf("expected status 'open', got %s", inv.Status)
	}
	if inv.AmountSats != 500 {
		t.Errorf("expected 500 sats, got %d", inv.AmountSats)
	}

	// Settle
	if err := s.SettleInvoice("hash1"); err != nil {
		t.Fatalf("settle invoice: %v", err)
	}
	inv, _ = s.GetInvoice("hash1")
	if inv.Status != "settled" {
		t.Errorf("expected status 'settled', got %s", inv.Status)
	}

	// Double-settle is idempotent (crash-safe — no error).
	if err := s.SettleInvoice("hash1"); err != nil {
		t.Fatalf("idempotent settle should not error: %v", err)
	}
	inv, _ = s.GetInvoice("hash1")
	if inv.Status != "settled" {
		t.Errorf("status should remain 'settled', got %s", inv.Status)
	}
}

func TestInvoice_Expire(t *testing.T) {
	s := testStore(t)
	s.InsertInvoice("hash1", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")

	if err := s.ExpireInvoice("hash1"); err != nil {
		t.Fatalf("expire invoice: %v", err)
	}
	inv, _ := s.GetInvoice("hash1")
	if inv.Status != "expired" {
		t.Errorf("expected 'expired', got %s", inv.Status)
	}
}

func TestInvoice_DuplicatePaymentHashRejected(t *testing.T) {
	s := testStore(t)
	s.InsertInvoice("hash1", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")

	err := s.InsertInvoice("hash1", "lnbc2...", 1000, "10gb", 10_000_000_000,
		time.Now().Add(time.Hour), "")
	if err == nil {
		t.Fatal("duplicate payment_hash should be rejected")
	}
}

// --- Ticket lifecycle tests ---

func TestTicket_SingleUseEnforced(t *testing.T) {
	// Invariant 4: A ticket can only be redeemed once.
	s := testStore(t)
	s.InsertInvoice("hash1", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	s.SettleInvoice("hash1")
	s.InsertTicket("t1", "hash1", 100_000_000, "hmac1")

	// First redemption succeeds.
	if err := s.RedeemTicket("t1", "node1"); err != nil {
		t.Fatalf("first redeem: %v", err)
	}

	// Second redemption fails (single-use).
	if err := s.RedeemTicket("t1", "node2"); err == nil {
		t.Fatal("double-redeem should fail — tickets are single-use")
	}
}

func TestTicket_RedeemedCannotBeModified(t *testing.T) {
	// Invariant: redeemed tickets are immutable (trigger).
	s := testStore(t)
	s.InsertInvoice("hash1", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	s.InsertTicket("t1", "hash1", 100_000_000, "hmac1")
	s.RedeemTicket("t1", "node1")

	_, err := s.db.Exec(`UPDATE tickets SET bytes_value = 999 WHERE id = 't1'`)
	if err == nil {
		t.Fatal("modifying a redeemed ticket should be blocked by trigger")
	}
}

// --- Settlement idempotency ---

func TestSettlement_Idempotent(t *testing.T) {
	// Invariant 10: Running settlement twice produces the same result.
	s := testStore(t)

	_, err := s.InsertSettlementEntry("2026-06-06T00", "node1", 1_000_000_000, 500, 1_000_000_000, 1_000_000_000, 10)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}

	// Second insert for same (period, node) is a no-op, not an error.
	_, err = s.InsertSettlementEntry("2026-06-06T00", "node1", 2_000_000_000, 1000, 2_000_000_000, 2_000_000_000, 20)
	if err != nil {
		t.Fatalf("idempotent insert should not error: %v", err)
	}
}

// --- Payout state machine ---

func TestPayout_StateMachine(t *testing.T) {
	// Invariant 11: Money is never in an ambiguous state.
	s := testStore(t)
	s.InsertSettlementEntry("2026-06-06T00", "node1", 1_000_000_000, 2000, 1_000_000_000, 1_000_000_000, 10)

	// Get the settlement entry ID.
	var entryID int64
	s.db.QueryRow(`SELECT id FROM settlement_entries WHERE node_pubkey = 'node1'`).Scan(&entryID)

	// Create payout.
	payoutID, err := s.InsertPayout(entryID, "node1", 2000)
	if err != nil {
		t.Fatalf("insert payout: %v", err)
	}

	// Mark in-flight (about to send).
	if err := s.MarkPayoutInFlight(payoutID); err != nil {
		t.Fatalf("mark in_flight: %v", err)
	}

	// Fail it.
	if err := s.MarkPayoutFailed(payoutID, "no route"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}

	// Retry it.
	if err := s.MarkPayoutRetrying(payoutID); err != nil {
		t.Fatalf("mark retrying: %v", err)
	}

	// Mark in-flight again.
	if err := s.MarkPayoutInFlight(payoutID); err != nil {
		t.Fatalf("mark in_flight (retry): %v", err)
	}

	// Pay it.
	if err := s.MarkPayoutPaid(payoutID, "payment_hash_abc"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
}

func TestPayout_MaxRetriesEnforced(t *testing.T) {
	// Invariant: max 3 retry attempts.
	s := testStore(t)
	s.InsertSettlementEntry("2026-06-06T00", "node1", 1_000_000_000, 2000, 1_000_000_000, 1_000_000_000, 10)

	var entryID int64
	s.db.QueryRow(`SELECT id FROM settlement_entries WHERE node_pubkey = 'node1'`).Scan(&entryID)

	payoutID, _ := s.InsertPayout(entryID, "node1", 2000)

	// Fail 3 times (incrementing attempt_count each time).
	for i := 0; i < 3; i++ {
		s.MarkPayoutInFlight(payoutID)
		s.MarkPayoutFailed(payoutID, "fail")
		if i < 2 {
			s.MarkPayoutRetrying(payoutID)
		}
	}

	// Fourth retry should fail — max attempts reached.
	if err := s.MarkPayoutRetrying(payoutID); err == nil {
		t.Fatal("should reject retry after 3 attempts")
	}
}

// --- Audit invariants ---

func TestAudit_TotalPayoutsCannotExceedPurchases(t *testing.T) {
	// Invariant 2: Total node payouts ≤ total customer purchases.
	s := testStore(t)

	// Customer purchases 500 sats.
	s.InsertInvoice("hash1", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	s.SettleInvoice("hash1")

	// Settlement + payout for 500 sats.
	s.InsertSettlementEntry("2026-06-06T00", "node1", 1_000_000_000, 500, 1_000_000_000, 1_000_000_000, 10)
	var entryID int64
	s.db.QueryRow(`SELECT id FROM settlement_entries WHERE node_pubkey = 'node1'`).Scan(&entryID)
	payoutID, _ := s.InsertPayout(entryID, "node1", 500)
	s.MarkPayoutInFlight(payoutID)
	s.MarkPayoutPaid(payoutID, "hash_paid")

	purchased, _ := s.TotalPurchasedSats()
	paidOut, _ := s.TotalPaidOutSats()

	if paidOut > purchased {
		t.Fatalf("INVARIANT VIOLATION: paid out %d sats > purchased %d sats", paidOut, purchased)
	}
}

func TestAudit_CompensatingEntries(t *testing.T) {
	// Verify compensating entries work as corrections.
	s := testStore(t)

	// Original settlement overpaid by 500.
	s.InsertSettlementEntry("2026-06-06T00", "node1", 1_000_000_000, 1500, 1_000_000_000, 1_000_000_000, 10)

	// Compensating entry corrects it.
	s.InsertCompensatingEntry("settlement", 1, -500, "overbilled: correct amount is 1000 sats")

	total, _ := s.TotalCompensationSats()
	if total != -500 {
		t.Errorf("expected compensation of -500, got %d", total)
	}
}

// --- Platform path tests ---

func TestDefaultDBPath(t *testing.T) {
	path, err := defaultDBPath()
	if err != nil {
		t.Fatalf("defaultDBPath: %v", err)
	}
	if filepath.Base(path) != "hub.db" {
		t.Errorf("expected hub.db, got %s", filepath.Base(path))
	}
}

func TestOpenCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "nested", "deep", "hub.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open should create directories: %v", err)
	}
	s.Close()

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatal("database file should exist")
	}
}

// --- Adversarial / schema enforcement tests ---

func TestSchema_RejectsNegativeInvoiceAmount(t *testing.T) {
	s := testStore(t)
	err := s.InsertInvoice("hash1", "lnbc...", -500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	if err == nil {
		t.Fatal("should reject negative invoice amount")
	}
}

func TestSchema_RejectsZeroInvoiceAmount(t *testing.T) {
	s := testStore(t)
	err := s.InsertInvoice("hash1", "lnbc...", 0, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	if err == nil {
		t.Fatal("should reject zero invoice amount")
	}
}

func TestSchema_RejectsNegativeBytesAllowed(t *testing.T) {
	s := testStore(t)
	err := s.InsertInvoice("hash1", "lnbc...", 500, "1gb", -1,
		time.Now().Add(time.Hour), "")
	if err == nil {
		t.Fatal("should reject negative bytes_allowed")
	}
}

func TestSchema_RejectsNegativeTicketBytes(t *testing.T) {
	s := testStore(t)
	s.InsertInvoice("hash1", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	_, err := s.db.Exec(`INSERT INTO tickets (id, payment_hash, bytes_value, hmac) VALUES ('t1', 'hash1', -100, 'hmac')`)
	if err == nil {
		t.Fatal("should reject negative ticket bytes_value")
	}
}

func TestSchema_RejectsInvalidInvoiceStatus(t *testing.T) {
	s := testStore(t)
	_, err := s.db.Exec(`INSERT INTO invoices (payment_hash, payment_request, amount_sats, tier, bytes_allowed, status, expires_at)
		VALUES ('h1', 'lnbc', 500, '1gb', 1000000000, 'hacked', '2026-01-01T00:00:00Z')`)
	if err == nil {
		t.Fatal("should reject invalid invoice status")
	}
}

func TestSchema_RejectsInvalidTicketStatus(t *testing.T) {
	s := testStore(t)
	s.InsertInvoice("hash1", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	_, err := s.db.Exec(`INSERT INTO tickets (id, payment_hash, bytes_value, hmac, status) VALUES ('t1', 'hash1', 100, 'h', 'forged')`)
	if err == nil {
		t.Fatal("should reject invalid ticket status")
	}
}

func TestTrigger_InvoiceAmountImmutable(t *testing.T) {
	s := testStore(t)
	s.InsertInvoice("hash1", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")

	_, err := s.db.Exec(`UPDATE invoices SET amount_sats = 9999 WHERE payment_hash = 'hash1'`)
	if err == nil {
		t.Fatal("should not allow changing invoice amount_sats")
	}
}

func TestTrigger_InvoiceTierImmutable(t *testing.T) {
	s := testStore(t)
	s.InsertInvoice("hash1", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")

	_, err := s.db.Exec(`UPDATE invoices SET tier = '50gb' WHERE payment_hash = 'hash1'`)
	if err == nil {
		t.Fatal("should not allow changing invoice tier")
	}
}

func TestTrigger_TicketBytesImmutable(t *testing.T) {
	s := testStore(t)
	s.InsertInvoice("hash1", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	s.InsertTicket("t1", "hash1", 100_000_000, "hmac1")

	_, err := s.db.Exec(`UPDATE tickets SET bytes_value = 999999999 WHERE id = 't1'`)
	if err == nil {
		t.Fatal("should not allow changing ticket bytes_value")
	}
}

func TestTrigger_TicketHMACImmutable(t *testing.T) {
	s := testStore(t)
	s.InsertInvoice("hash1", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	s.InsertTicket("t1", "hash1", 100_000_000, "hmac1")

	_, err := s.db.Exec(`UPDATE tickets SET hmac = 'forged' WHERE id = 't1'`)
	if err == nil {
		t.Fatal("should not allow changing ticket hmac")
	}
}

func TestPayout_PaidCannotBeMarkedFailed(t *testing.T) {
	// STRIDE/Tampering: once paid, a payout is final — cannot be reverted.
	s := testStore(t)
	s.InsertSettlementEntry("2026-06-06T00", "node1", 1_000_000_000, 2000, 1_000_000_000, 1_000_000_000, 10)

	var entryID int64
	s.db.QueryRow(`SELECT id FROM settlement_entries WHERE node_pubkey = 'node1'`).Scan(&entryID)
	payoutID, _ := s.InsertPayout(entryID, "node1", 2000)
	s.MarkPayoutInFlight(payoutID)
	s.MarkPayoutPaid(payoutID, "hash_paid")

	// Attempting to mark as failed must error (not silently succeed).
	err := s.MarkPayoutFailed(payoutID, "too late")
	if err == nil {
		t.Fatal("MarkPayoutFailed on a paid payout should return an error")
	}
	// Status must remain 'paid'.
	var status string
	s.db.QueryRow(`SELECT status FROM payouts WHERE id = ?`, payoutID).Scan(&status)
	if status != "paid" {
		t.Fatalf("paid payout should not be changeable, got status: %s", status)
	}
}

func TestConcurrent_TicketRedemption(t *testing.T) {
	// Invariant 4: A ticket can only be redeemed once.
	// Under concurrency, exactly one goroutine should succeed.
	s := testStore(t)
	s.InsertInvoice("hash1", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	s.InsertTicket("t1", "hash1", 100_000_000, "hmac1")

	var wg sync.WaitGroup
	var successCount int64
	var mu sync.Mutex

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(nodeID string) {
			defer wg.Done()
			err := s.RedeemTicket("t1", nodeID)
			if err == nil {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}(fmt.Sprintf("node-%d", i))
	}
	wg.Wait()

	if successCount != 1 {
		t.Fatalf("exactly 1 goroutine should redeem, got %d", successCount)
	}
}

func TestSchema_RejectsZeroPayout(t *testing.T) {
	s := testStore(t)
	s.InsertSettlementEntry("2026-06-06T00", "node1", 1_000_000_000, 2000, 1_000_000_000, 1_000_000_000, 10)

	var entryID int64
	s.db.QueryRow(`SELECT id FROM settlement_entries WHERE node_pubkey = 'node1'`).Scan(&entryID)

	_, err := s.InsertPayout(entryID, "node1", 0)
	if err == nil {
		t.Fatal("should reject zero-amount payout")
	}
}

func TestSchema_RejectsNegativePayout(t *testing.T) {
	s := testStore(t)
	s.InsertSettlementEntry("2026-06-06T00", "node1", 1_000_000_000, 2000, 1_000_000_000, 1_000_000_000, 10)

	var entryID int64
	s.db.QueryRow(`SELECT id FROM settlement_entries WHERE node_pubkey = 'node1'`).Scan(&entryID)

	_, err := s.InsertPayout(entryID, "node1", -100)
	if err == nil {
		t.Fatal("should reject negative-amount payout")
	}
}

// --- STRIDE: Settlement state machine attacks ---

func TestSTRIDE_SettleExpiredInvoice(t *testing.T) {
	// STRIDE/Elevation: attacker tries to settle an expired invoice
	// to unlock tickets without paying.
	s := testStore(t)
	s.InsertInvoice("hash1", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	s.ExpireInvoice("hash1")

	err := s.SettleInvoice("hash1")
	if err == nil {
		t.Fatal("settling an expired invoice should fail")
	}
}

func TestSTRIDE_SettleNonexistentInvoice(t *testing.T) {
	// STRIDE/Spoofing: attacker invents a payment hash to trigger settlement.
	s := testStore(t)

	err := s.SettleInvoice("invented-hash")
	if err == nil {
		t.Fatal("settling a nonexistent invoice should fail")
	}
}

func TestSTRIDE_InFlightFromPaid_Blocked(t *testing.T) {
	// STRIDE/Elevation: attempt to move a paid payout back to in_flight
	// to trigger a double-payment.
	s := testStore(t)
	s.InsertSettlementEntry("2026-06-06T00", "node1", 1_000_000_000, 2000, 1_000_000_000, 1_000_000_000, 10)

	var entryID int64
	s.db.QueryRow(`SELECT id FROM settlement_entries WHERE node_pubkey = 'node1'`).Scan(&entryID)

	payoutID, _ := s.InsertPayout(entryID, "node1", 2000)
	s.MarkPayoutInFlight(payoutID)
	s.MarkPayoutPaid(payoutID, "hash_paid")

	err := s.MarkPayoutInFlight(payoutID)
	if err == nil {
		t.Fatal("should not allow transitioning paid payout to in_flight")
	}
}

func TestSTRIDE_InFlightFromFailed_Blocked(t *testing.T) {
	// STRIDE/Elevation: attempt to skip the retrying state by going
	// directly from failed → in_flight.
	s := testStore(t)
	s.InsertSettlementEntry("2026-06-06T00", "node1", 1_000_000_000, 2000, 1_000_000_000, 1_000_000_000, 10)

	var entryID int64
	s.db.QueryRow(`SELECT id FROM settlement_entries WHERE node_pubkey = 'node1'`).Scan(&entryID)

	payoutID, _ := s.InsertPayout(entryID, "node1", 2000)
	s.MarkPayoutInFlight(payoutID)
	s.MarkPayoutFailed(payoutID, "no route")

	// Direct failed → in_flight is not allowed; must go through retrying.
	err := s.MarkPayoutInFlight(payoutID)
	if err == nil {
		t.Fatal("should not allow transitioning failed payout directly to in_flight")
	}
}

func TestSTRIDE_PaidFromPending_Blocked(t *testing.T) {
	// STRIDE/Elevation: skip the in_flight state to mark paid without
	// actually sending a Lightning payment.
	s := testStore(t)
	s.InsertSettlementEntry("2026-06-06T00", "node1", 1_000_000_000, 2000, 1_000_000_000, 1_000_000_000, 10)

	var entryID int64
	s.db.QueryRow(`SELECT id FROM settlement_entries WHERE node_pubkey = 'node1'`).Scan(&entryID)

	payoutID, _ := s.InsertPayout(entryID, "node1", 2000)

	// pending → paid should fail (must go through in_flight).
	err := s.MarkPayoutPaid(payoutID, "fake_hash")
	if err == nil {
		t.Fatal("should not allow skipping in_flight to mark paid")
	}
}

func TestSTRIDE_FailedFromPending_Blocked(t *testing.T) {
	// STRIDE/Denial: marking a payout as failed without attempting payment
	// denies the node their earned sats.
	s := testStore(t)
	s.InsertSettlementEntry("2026-06-06T00", "node1", 1_000_000_000, 2000, 1_000_000_000, 1_000_000_000, 10)

	var entryID int64
	s.db.QueryRow(`SELECT id FROM settlement_entries WHERE node_pubkey = 'node1'`).Scan(&entryID)

	payoutID, _ := s.InsertPayout(entryID, "node1", 2000)

	// pending → failed should fail (must go through in_flight).
	err := s.MarkPayoutFailed(payoutID, "fake failure")
	if err == nil {
		t.Fatal("should not allow marking a pending payout as failed without attempting payment")
	}
}

func TestSTRIDE_PayoutAmountImmutable(t *testing.T) {
	// STRIDE/Tampering: attacker or bug modifies payout amount after creation.
	s := testStore(t)
	s.InsertSettlementEntry("2026-06-06T00", "node1", 1_000_000_000, 2000, 1_000_000_000, 1_000_000_000, 10)

	var entryID int64
	s.db.QueryRow(`SELECT id FROM settlement_entries WHERE node_pubkey = 'node1'`).Scan(&entryID)

	payoutID, _ := s.InsertPayout(entryID, "node1", 2000)

	_, err := s.db.Exec(`UPDATE payouts SET amount_sats = 9999 WHERE id = ?`, payoutID)
	if err == nil {
		t.Fatal("should not allow changing payout amount_sats")
	}
}

func TestSTRIDE_PayoutNodeImmutable(t *testing.T) {
	// STRIDE/Tampering: redirect a payout to a different node.
	s := testStore(t)
	s.InsertSettlementEntry("2026-06-06T00", "node1", 1_000_000_000, 2000, 1_000_000_000, 1_000_000_000, 10)

	var entryID int64
	s.db.QueryRow(`SELECT id FROM settlement_entries WHERE node_pubkey = 'node1'`).Scan(&entryID)

	payoutID, _ := s.InsertPayout(entryID, "node1", 2000)

	_, err := s.db.Exec(`UPDATE payouts SET node_pubkey = 'attacker' WHERE id = ?`, payoutID)
	if err == nil {
		t.Fatal("should not allow changing payout node_pubkey")
	}
}

func TestSTRIDE_PayoutSettlementRefImmutable(t *testing.T) {
	// STRIDE/Tampering: re-point a payout to a different settlement entry.
	s := testStore(t)
	s.InsertSettlementEntry("2026-06-06T00", "node1", 1_000_000_000, 2000, 1_000_000_000, 1_000_000_000, 10)
	s.InsertSettlementEntry("2026-06-06T00", "node2", 1_000_000_000, 5000, 1_000_000_000, 1_000_000_000, 10)

	var entryID1, entryID2 int64
	s.db.QueryRow(`SELECT id FROM settlement_entries WHERE node_pubkey = 'node1'`).Scan(&entryID1)
	s.db.QueryRow(`SELECT id FROM settlement_entries WHERE node_pubkey = 'node2'`).Scan(&entryID2)

	payoutID, _ := s.InsertPayout(entryID1, "node1", 2000)

	_, err := s.db.Exec(`UPDATE payouts SET settlement_entry_id = ? WHERE id = ?`, entryID2, payoutID)
	if err == nil {
		t.Fatal("should not allow changing payout settlement_entry_id")
	}
}

// ============================================================
// Phase 4: Entitlement tests
// ============================================================

func TestEntitlement_InsertAndGet(t *testing.T) {
	s := testStore(t)
	s.InsertInvoice("hash-e1", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	s.SettleInvoice("hash-e1")

	err := s.InsertEntitlement("ent-1", "hash-e1", 10, 100_000_000, "key-100mb")
	if err != nil {
		t.Fatalf("InsertEntitlement: %v", err)
	}

	e, err := s.GetEntitlementByPaymentHash("hash-e1")
	if err != nil {
		t.Fatalf("GetEntitlement: %v", err)
	}
	if e.TokensRemaining != 10 || e.TokensTotal != 10 {
		t.Errorf("tokens: remaining=%d, total=%d", e.TokensRemaining, e.TokensTotal)
	}
	if e.BytesPerToken != 100_000_000 {
		t.Errorf("bytes_per_token = %d", e.BytesPerToken)
	}
	if e.KeyID != "key-100mb" {
		t.Errorf("key_id = %s", e.KeyID)
	}
}

func TestEntitlement_ConsumeDecrementsRemaining(t *testing.T) {
	s := testStore(t)
	s.InsertInvoice("hash-c1", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	s.SettleInvoice("hash-c1")
	s.InsertEntitlement("ent-c1", "hash-c1", 10, 100_000_000, "key-1")

	// Consume 3 tokens.
	if err := s.ConsumeEntitlement("ent-c1", 3); err != nil {
		t.Fatalf("ConsumeEntitlement(3): %v", err)
	}

	e, _ := s.GetEntitlementByPaymentHash("hash-c1")
	if e.TokensRemaining != 7 {
		t.Errorf("remaining = %d, want 7", e.TokensRemaining)
	}

	// Consume remaining 7.
	if err := s.ConsumeEntitlement("ent-c1", 7); err != nil {
		t.Fatalf("ConsumeEntitlement(7): %v", err)
	}

	e, _ = s.GetEntitlementByPaymentHash("hash-c1")
	if e.TokensRemaining != 0 {
		t.Errorf("remaining = %d, want 0", e.TokensRemaining)
	}
}

func TestEntitlement_ConsumeRejectsOverdraw(t *testing.T) {
	s := testStore(t)
	s.InsertInvoice("hash-od", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	s.SettleInvoice("hash-od")
	s.InsertEntitlement("ent-od", "hash-od", 5, 100_000_000, "key-1")

	// Try to consume more than available.
	err := s.ConsumeEntitlement("ent-od", 6)
	if err == nil {
		t.Fatal("expected error for overdraw")
	}

	// Verify remaining unchanged.
	e, _ := s.GetEntitlementByPaymentHash("hash-od")
	if e.TokensRemaining != 5 {
		t.Errorf("remaining = %d after rejected overdraw, want 5", e.TokensRemaining)
	}
}

func TestEntitlement_ImmutableFields(t *testing.T) {
	s := testStore(t)
	s.InsertInvoice("hash-imm", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	s.SettleInvoice("hash-imm")
	s.InsertEntitlement("ent-imm", "hash-imm", 10, 100_000_000, "key-1")

	// Try to modify bytes_per_token.
	_, err := s.db.Exec(`UPDATE entitlements SET bytes_per_token = 999 WHERE id = 'ent-imm'`)
	if err == nil {
		t.Fatal("expected trigger to block bytes_per_token change")
	}
}

func TestEntitlement_NoDeletion(t *testing.T) {
	s := testStore(t)
	s.InsertInvoice("hash-nd", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	s.SettleInvoice("hash-nd")
	s.InsertEntitlement("ent-nd", "hash-nd", 10, 100_000_000, "key-1")

	_, err := s.db.Exec(`DELETE FROM entitlements WHERE id = 'ent-nd'`)
	if err == nil {
		t.Fatal("expected trigger to block deletion")
	}
}

// ============================================================
// Phase 4: Spent token tests
// ============================================================

func TestSpentToken_MarkSpent_FirstSpend(t *testing.T) {
	s := testStore(t)

	first, err := s.MarkSpent("token-1", "key-1", "node-abc")
	if err != nil {
		t.Fatalf("MarkSpent: %v", err)
	}
	if !first {
		t.Fatal("expected first_spend=true")
	}
}

func TestSpentToken_MarkSpent_DoubleSpend(t *testing.T) {
	s := testStore(t)

	s.MarkSpent("token-ds", "key-1", "node-a")

	// Second spend of same token — should return false.
	first, err := s.MarkSpent("token-ds", "key-1", "node-b")
	if err != nil {
		t.Fatalf("MarkSpent double: %v", err)
	}
	if first {
		t.Fatal("expected first_spend=false for double spend")
	}
}

func TestSpentToken_IsSpent(t *testing.T) {
	s := testStore(t)

	spent, _ := s.IsSpent("token-x")
	if spent {
		t.Fatal("unspent token reported as spent")
	}

	s.MarkSpent("token-x", "key-1", "node-a")

	spent, _ = s.IsSpent("token-x")
	if !spent {
		t.Fatal("spent token reported as unspent")
	}
}

func TestSpentToken_NoDeletion(t *testing.T) {
	s := testStore(t)
	s.MarkSpent("token-del", "key-1", "node-a")

	_, err := s.db.Exec(`DELETE FROM spent_tokens WHERE token_id = 'token-del'`)
	if err == nil {
		t.Fatal("expected trigger to block deletion")
	}
}

func TestSpentToken_NoUpdate(t *testing.T) {
	s := testStore(t)
	s.MarkSpent("token-upd", "key-1", "node-a")

	_, err := s.db.Exec(`UPDATE spent_tokens SET node_pubkey = 'node-b' WHERE token_id = 'token-upd'`)
	if err == nil {
		t.Fatal("expected trigger to block update")
	}
}

// ============================================================
// Phase 4: Redemption cache tests
// ============================================================

func TestRedemption_InsertAndGet(t *testing.T) {
	s := testStore(t)
	s.InsertInvoice("hash-r1", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	s.SettleInvoice("hash-r1")
	s.InsertEntitlement("ent-r1", "hash-r1", 10, 100_000_000, "key-1")

	err := s.InsertRedemption("nonce-1", "ent-r1", "reqhash-abc", 5, `["sig1","sig2"]`)
	if err != nil {
		t.Fatalf("InsertRedemption: %v", err)
	}

	r, err := s.GetRedemption("nonce-1")
	if err != nil {
		t.Fatalf("GetRedemption: %v", err)
	}
	if r.EntitlementID != "ent-r1" {
		t.Errorf("entitlement_id = %s", r.EntitlementID)
	}
	if r.TokensCount != 5 {
		t.Errorf("tokens_count = %d", r.TokensCount)
	}
	if r.RequestHash != "reqhash-abc" {
		t.Errorf("request_hash = %s", r.RequestHash)
	}
}

func TestRedemption_NotFound(t *testing.T) {
	s := testStore(t)

	r, err := s.GetRedemption("nonexistent")
	if err != nil {
		t.Fatalf("GetRedemption: %v", err)
	}
	if r != nil {
		t.Fatal("expected nil for nonexistent nonce")
	}
}

func TestRedemption_NoDeletion(t *testing.T) {
	s := testStore(t)
	s.InsertInvoice("hash-rd", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	s.SettleInvoice("hash-rd")
	s.InsertEntitlement("ent-rd", "hash-rd", 10, 100_000_000, "key-1")
	s.InsertRedemption("nonce-rd", "ent-rd", "rh", 1, `["sig"]`)

	_, err := s.db.Exec(`DELETE FROM redemptions WHERE nonce = 'nonce-rd'`)
	if err == nil {
		t.Fatal("expected trigger to block deletion")
	}
}

func TestRedemption_NoUpdate(t *testing.T) {
	s := testStore(t)
	s.InsertInvoice("hash-ru", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	s.SettleInvoice("hash-ru")
	s.InsertEntitlement("ent-ru", "hash-ru", 10, 100_000_000, "key-1")
	s.InsertRedemption("nonce-ru", "ent-ru", "rh", 1, `["sig"]`)

	_, err := s.db.Exec(`UPDATE redemptions SET tokens_count = 99 WHERE nonce = 'nonce-ru'`)
	if err == nil {
		t.Fatal("expected trigger to block update")
	}
}

// ============================================================
// Phase 4 STRIDE: Concurrent entitlement consumption
// ============================================================

func TestSTRIDE_ConcurrentConsumeEntitlement(t *testing.T) {
	s := testStore(t)
	s.InsertInvoice("hash-cc", "lnbc...", 500, "1gb", 1_000_000_000,
		time.Now().Add(time.Hour), "")
	s.SettleInvoice("hash-cc")
	s.InsertEntitlement("ent-cc", "hash-cc", 10, 100_000_000, "key-1")

	// 20 goroutines each try to consume 1 token.
	// Exactly 10 should succeed, 10 should fail.
	var wg sync.WaitGroup
	successes := int32(0)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.ConsumeEntitlement("ent-cc", 1); err == nil {
				atomic.AddInt32(&successes, 1)
			}
		}()
	}
	wg.Wait()

	if successes != 10 {
		t.Errorf("expected exactly 10 successes, got %d", successes)
	}

	e, _ := s.GetEntitlementByPaymentHash("hash-cc")
	if e.TokensRemaining != 0 {
		t.Errorf("remaining = %d, want 0", e.TokensRemaining)
	}
}

func TestSTRIDE_ConcurrentMarkSpent(t *testing.T) {
	s := testStore(t)

	// 20 goroutines race to spend the same token.
	// Exactly 1 should win.
	var wg sync.WaitGroup
	wins := int32(0)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			first, err := s.MarkSpent("race-token", "key-1", fmt.Sprintf("node-%d", idx))
			if err == nil && first {
				atomic.AddInt32(&wins, 1)
			}
		}(i)
	}
	wg.Wait()

	if wins != 1 {
		t.Errorf("expected exactly 1 winner, got %d", wins)
	}
}
