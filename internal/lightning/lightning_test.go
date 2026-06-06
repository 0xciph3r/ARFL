package lightning

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// --- Interface compliance ---

func TestMockClient_ImplementsClient(t *testing.T) {
	var _ Client = (*MockClient)(nil)
}

// --- Invoice creation ---

func TestCreateInvoice_Success(t *testing.T) {
	m := NewMockClient()
	ctx := context.Background()

	inv, err := m.CreateInvoice(ctx, 500, "1gb bandwidth", 10*time.Minute)
	if err != nil {
		t.Fatalf("CreateInvoice: %v", err)
	}
	if inv.AmountSats != 500 {
		t.Errorf("expected 500 sats, got %d", inv.AmountSats)
	}
	if inv.Status != InvoiceOpen {
		t.Errorf("expected open status, got %s", inv.Status)
	}
	if inv.PaymentHash == "" {
		t.Error("payment hash should not be empty")
	}
	if inv.PaymentRequest == "" {
		t.Error("payment request should not be empty")
	}
}

func TestCreateInvoice_RejectsZeroAmount(t *testing.T) {
	m := NewMockClient()
	_, err := m.CreateInvoice(context.Background(), 0, "free", time.Minute)
	if err == nil {
		t.Fatal("should reject zero amount")
	}
}

func TestCreateInvoice_RejectsNegativeAmount(t *testing.T) {
	m := NewMockClient()
	_, err := m.CreateInvoice(context.Background(), -100, "negative", time.Minute)
	if err == nil {
		t.Fatal("should reject negative amount")
	}
}

func TestCreateInvoice_InjectedFailure(t *testing.T) {
	// Chaos: simulate LND being down.
	m := NewMockClient()
	m.CreateInvoiceErr = fmt.Errorf("lnd: connection refused")

	_, err := m.CreateInvoice(context.Background(), 500, "test", time.Minute)
	if err == nil {
		t.Fatal("should propagate injected error")
	}
}

func TestCreateInvoice_UniqueHashes(t *testing.T) {
	m := NewMockClient()
	ctx := context.Background()
	seen := make(map[string]bool)

	for i := 0; i < 100; i++ {
		inv, _ := m.CreateInvoice(ctx, 500, "test", time.Minute)
		if seen[inv.PaymentHash] {
			t.Fatalf("duplicate payment hash: %s", inv.PaymentHash)
		}
		seen[inv.PaymentHash] = true
	}
}

// --- Invoice lookup ---

func TestLookupInvoice_Found(t *testing.T) {
	m := NewMockClient()
	ctx := context.Background()

	inv, _ := m.CreateInvoice(ctx, 500, "test", time.Minute)
	found, err := m.LookupInvoice(ctx, inv.PaymentHash)
	if err != nil {
		t.Fatalf("LookupInvoice: %v", err)
	}
	if found.AmountSats != 500 {
		t.Errorf("expected 500, got %d", found.AmountSats)
	}
}

func TestLookupInvoice_NotFound(t *testing.T) {
	m := NewMockClient()
	_, err := m.LookupInvoice(context.Background(), "nonexistent")
	if err != ErrInvoiceNotFound {
		t.Fatalf("expected ErrInvoiceNotFound, got: %v", err)
	}
}

func TestLookupInvoice_DetectsExpiry(t *testing.T) {
	m := NewMockClient()
	ctx := context.Background()

	// Create invoice with very short expiry.
	inv, _ := m.CreateInvoice(ctx, 500, "test", 1*time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	found, err := m.LookupInvoice(ctx, inv.PaymentHash)
	if err != nil {
		t.Fatalf("LookupInvoice: %v", err)
	}
	if found.Status != InvoiceExpired {
		t.Errorf("expected expired, got %s", found.Status)
	}
}

func TestLookupInvoice_ReturnsACopy(t *testing.T) {
	// Returned invoice must be a copy — callers mutating it
	// should not affect the mock's internal state.
	m := NewMockClient()
	ctx := context.Background()

	inv, _ := m.CreateInvoice(ctx, 500, "test", time.Minute)
	found, _ := m.LookupInvoice(ctx, inv.PaymentHash)
	found.AmountSats = 999999 // mutate the returned copy

	original, _ := m.LookupInvoice(ctx, inv.PaymentHash)
	if original.AmountSats != 500 {
		t.Fatal("mutation of returned invoice should not affect internal state")
	}
}

// --- Settlement subscription ---

func TestSubscribeInvoices_ReceivesSettlement(t *testing.T) {
	m := NewMockClient()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch, err := m.SubscribeInvoices(ctx)
	if err != nil {
		t.Fatalf("SubscribeInvoices: %v", err)
	}

	inv, _ := m.CreateInvoice(ctx, 500, "test", time.Minute)
	m.SimulateSettlement(inv.PaymentHash)

	select {
	case settled := <-ch:
		if settled.PaymentHash != inv.PaymentHash {
			t.Errorf("wrong hash: got %s, want %s", settled.PaymentHash, inv.PaymentHash)
		}
		if settled.Status != InvoiceSettled {
			t.Errorf("expected settled, got %s", settled.Status)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for settlement notification")
	}
}

func TestSubscribeInvoices_DelayedSettlement(t *testing.T) {
	m := NewMockClient()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch, _ := m.SubscribeInvoices(ctx)
	inv, _ := m.CreateInvoice(ctx, 500, "test", time.Minute)

	// Settle after a delay (simulates real-world payment latency).
	m.SimulateSettlementWithDelay(inv.PaymentHash, 100*time.Millisecond)

	select {
	case settled := <-ch:
		if settled.PaymentHash != inv.PaymentHash {
			t.Errorf("wrong hash")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for delayed settlement")
	}
}

func TestSubscribeInvoices_ContextCancellation(t *testing.T) {
	m := NewMockClient()
	ctx, cancel := context.WithCancel(context.Background())

	ch, _ := m.SubscribeInvoices(ctx)

	// Cancel immediately.
	cancel()

	// Channel should close.
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("channel should be closed after context cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("channel did not close after cancellation")
	}
}

func TestSimulateSettlement_NonexistentInvoice(t *testing.T) {
	m := NewMockClient()
	err := m.SimulateSettlement("nonexistent")
	if err != ErrInvoiceNotFound {
		t.Fatalf("expected ErrInvoiceNotFound, got: %v", err)
	}
}

func TestSimulateSettlement_DoubleSettlement(t *testing.T) {
	m := NewMockClient()
	ctx := context.Background()

	inv, _ := m.CreateInvoice(ctx, 500, "test", time.Minute)
	m.SimulateSettlement(inv.PaymentHash)

	// Second settlement should fail.
	err := m.SimulateSettlement(inv.PaymentHash)
	if err == nil {
		t.Fatal("double settlement should fail")
	}
}

// --- Outbound payments ---

func TestSendPayment_Success(t *testing.T) {
	m := NewMockClient()
	ctx := context.Background()

	result, err := m.SendPayment(ctx, "lnbc500...", 500)
	if err != nil {
		t.Fatalf("SendPayment: %v", err)
	}
	if result.Status != PaymentSucceeded {
		t.Errorf("expected succeeded, got %s", result.Status)
	}
	if result.PaymentHash == "" {
		t.Error("payment hash should not be empty")
	}
}

func TestSendPayment_InjectedFailure(t *testing.T) {
	// Chaos: simulate no route to node.
	m := NewMockClient()
	m.SendPaymentErr = ErrNoRoute

	_, err := m.SendPayment(context.Background(), "lnbc500...", 500)
	if err != ErrNoRoute {
		t.Fatalf("expected ErrNoRoute, got: %v", err)
	}
}

func TestSendPayment_WithDelay(t *testing.T) {
	m := NewMockClient()
	m.PaymentDelay = 100 * time.Millisecond
	ctx := context.Background()

	start := time.Now()
	result, err := m.SendPayment(ctx, "lnbc500...", 500)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("SendPayment: %v", err)
	}
	if result.Status != PaymentSucceeded {
		t.Errorf("expected succeeded, got %s", result.Status)
	}
	if elapsed < 80*time.Millisecond {
		t.Errorf("expected at least 80ms delay, got %v", elapsed)
	}
}

func TestSendPayment_ContextTimeout(t *testing.T) {
	// Chaos: payment takes too long, caller gives up.
	m := NewMockClient()
	m.PaymentDelay = 5 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := m.SendPayment(ctx, "lnbc500...", 500)
	if err == nil {
		t.Fatal("should fail on context timeout")
	}
}

// --- Concurrency ---

func TestConcurrent_CreateAndSettle(t *testing.T) {
	m := NewMockClient()
	ctx := context.Background()

	ch, _ := m.SubscribeInvoices(ctx)

	var wg sync.WaitGroup
	settled := make(map[string]bool)
	var mu sync.Mutex

	// Create and settle 50 invoices concurrently.
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inv, err := m.CreateInvoice(ctx, 500, "test", time.Minute)
			if err != nil {
				t.Errorf("CreateInvoice: %v", err)
				return
			}
			if err := m.SimulateSettlement(inv.PaymentHash); err != nil {
				t.Errorf("SimulateSettlement: %v", err)
			}
		}()
	}

	// Collect settlements.
	go func() {
		for inv := range ch {
			mu.Lock()
			settled[inv.PaymentHash] = true
			mu.Unlock()
		}
	}()

	wg.Wait()
	time.Sleep(100 * time.Millisecond) // let subscriber catch up

	mu.Lock()
	count := len(settled)
	mu.Unlock()

	if count < 40 {
		// Allow some drops due to channel buffer, but most should arrive.
		t.Errorf("expected at least 40 settlements, got %d", count)
	}
	if m.InvoiceCount() != 50 {
		t.Errorf("expected 50 invoices, got %d", m.InvoiceCount())
	}
}

// --- Status string tests ---

func TestInvoiceStatus_String(t *testing.T) {
	cases := []struct {
		s    InvoiceStatus
		want string
	}{
		{InvoiceOpen, "open"},
		{InvoiceSettled, "settled"},
		{InvoiceExpired, "expired"},
		{InvoiceStatus(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.s.String(); got != c.want {
			t.Errorf("InvoiceStatus(%d).String() = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestPaymentStatus_String(t *testing.T) {
	cases := []struct {
		s    PaymentStatus
		want string
	}{
		{PaymentSucceeded, "succeeded"},
		{PaymentFailed, "failed"},
		{PaymentInFlight, "in_flight"},
		{PaymentStatus(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.s.String(); got != c.want {
			t.Errorf("PaymentStatus(%d).String() = %q, want %q", c.s, got, c.want)
		}
	}
}

// --- Adversarial: multi-subscriber broadcast ---

func TestSubscribeInvoices_MultipleSubscribers(t *testing.T) {
	m := NewMockClient()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch1, _ := m.SubscribeInvoices(ctx)
	ch2, _ := m.SubscribeInvoices(ctx)

	inv, _ := m.CreateInvoice(ctx, 500, "test", time.Minute)
	m.SimulateSettlement(inv.PaymentHash)

	// Both subscribers should receive the settlement.
	for i, ch := range []<-chan *Invoice{ch1, ch2} {
		select {
		case settled := <-ch:
			if settled.PaymentHash != inv.PaymentHash {
				t.Errorf("subscriber %d: wrong hash", i)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timeout", i)
		}
	}
}

// --- Adversarial: settle expired invoice ---

func TestSimulateSettlement_ExpiredInvoice(t *testing.T) {
	m := NewMockClient()
	ctx := context.Background()

	inv, _ := m.CreateInvoice(ctx, 500, "test", 1*time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	// Lookup triggers expiry detection.
	m.LookupInvoice(ctx, inv.PaymentHash)

	err := m.SimulateSettlement(inv.PaymentHash)
	if err == nil {
		t.Fatal("should not settle expired invoice")
	}
}

// --- Adversarial: payment records stored ---

func TestSendPayment_RecordStored(t *testing.T) {
	m := NewMockClient()
	ctx := context.Background()

	m.SendPayment(ctx, "lnbc500-test", 500)
	result, ok := m.GetPayment("lnbc500-test")
	if !ok {
		t.Fatal("payment should be stored")
	}
	if result.Status != PaymentSucceeded {
		t.Errorf("expected succeeded, got %s", result.Status)
	}
}

func TestGetPayment_NotFound(t *testing.T) {
	m := NewMockClient()
	_, ok := m.GetPayment("nonexistent")
	if ok {
		t.Fatal("should not find nonexistent payment")
	}
}

// --- Adversarial: InsufficientFunds ---

func TestSendPayment_InsufficientFunds(t *testing.T) {
	m := NewMockClient()
	m.SendPaymentErr = ErrInsufficientFunds

	_, err := m.SendPayment(context.Background(), "lnbc500...", 500)
	if err != ErrInsufficientFunds {
		t.Fatalf("expected ErrInsufficientFunds, got: %v", err)
	}
}
