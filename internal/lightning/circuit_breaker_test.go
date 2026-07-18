package lightning

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// failingClient is a mock that fails on demand.
type failingClient struct {
	*MockClient
	shouldFail atomic.Bool
	callCount  atomic.Int64
}

func newFailingClient() *failingClient {
	return &failingClient{MockClient: NewMockClient()}
}

func (f *failingClient) CreateInvoice(ctx context.Context, amountSats int64, memo string, expiry time.Duration) (*Invoice, error) {
	f.callCount.Add(1)
	if f.shouldFail.Load() {
		return nil, fmt.Errorf("connection refused")
	}
	return f.MockClient.CreateInvoice(ctx, amountSats, memo, expiry)
}

func (f *failingClient) LookupInvoice(ctx context.Context, paymentHash string) (*Invoice, error) {
	f.callCount.Add(1)
	if f.shouldFail.Load() {
		return nil, fmt.Errorf("connection refused")
	}
	return f.MockClient.LookupInvoice(ctx, paymentHash)
}

func TestCircuitBreaker_ClosedPassesThrough(t *testing.T) {
	mock := newFailingClient()
	cb := NewCircuitBreaker(mock)
	defer cb.Stop()

	if cb.State() != CircuitClosed {
		t.Fatalf("expected closed, got %s", cb.State())
	}

	ctx := context.Background()
	inv, err := cb.CreateInvoice(ctx, 100, "test", time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inv == nil {
		t.Fatal("expected invoice")
	}
}

func TestCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	mock := newFailingClient()
	mock.shouldFail.Store(true)

	cb := NewCircuitBreaker(mock, CircuitBreakerConfig{
		FailureThreshold: 3,
		OpenTimeout:      100 * time.Millisecond,
		ProbeInterval:    50 * time.Millisecond,
		HalfOpenMax:      1,
	})
	defer cb.Stop()

	ctx := context.Background()

	// 3 failures should open the circuit
	for i := 0; i < 3; i++ {
		_, err := cb.CreateInvoice(ctx, 100, "test", time.Minute)
		if err == nil {
			t.Fatal("expected error")
		}
	}

	if cb.State() != CircuitOpen {
		t.Fatalf("expected open, got %s", cb.State())
	}

	// Next call should fail fast with ErrCircuitOpen (no call to backend)
	callsBefore := mock.callCount.Load()
	_, err := cb.CreateInvoice(ctx, 100, "test", time.Minute)
	if err != ErrCircuitOpen {
		t.Fatalf("expected ErrCircuitOpen, got: %v", err)
	}
	if mock.callCount.Load() != callsBefore {
		t.Fatal("circuit breaker should not have called backend")
	}
}

func TestCircuitBreaker_SelfHeals(t *testing.T) {
	mock := newFailingClient()
	mock.shouldFail.Store(true)

	cb := NewCircuitBreaker(mock, CircuitBreakerConfig{
		FailureThreshold: 2,
		OpenTimeout:      50 * time.Millisecond,
		ProbeInterval:    30 * time.Millisecond,
		HalfOpenMax:      1,
	})
	defer cb.Stop()

	ctx := context.Background()

	// Trip the breaker
	for i := 0; i < 2; i++ {
		cb.CreateInvoice(ctx, 100, "test", time.Minute)
	}
	if cb.State() != CircuitOpen {
		t.Fatalf("expected open, got %s", cb.State())
	}

	// Fix the backend
	mock.shouldFail.Store(false)

	// Wait for probe to recover
	time.Sleep(200 * time.Millisecond)

	if cb.State() != CircuitClosed {
		t.Fatalf("expected closed after self-heal, got %s", cb.State())
	}

	// Normal calls should work again
	inv, err := cb.CreateInvoice(ctx, 100, "test", time.Minute)
	if err != nil {
		t.Fatalf("unexpected error after recovery: %v", err)
	}
	if inv == nil {
		t.Fatal("expected invoice after recovery")
	}
}

func TestCircuitBreaker_HalfOpenFailsGoesBack(t *testing.T) {
	mock := newFailingClient()
	mock.shouldFail.Store(true)

	cb := NewCircuitBreaker(mock, CircuitBreakerConfig{
		FailureThreshold: 1,
		OpenTimeout:      50 * time.Millisecond,
		ProbeInterval:    5 * time.Second, // long probe so we control transition
		HalfOpenMax:      1,
	})
	defer cb.Stop()

	ctx := context.Background()

	// Trip the breaker
	cb.CreateInvoice(ctx, 100, "test", time.Minute)
	if cb.State() != CircuitOpen {
		t.Fatalf("expected open, got %s", cb.State())
	}

	// Wait for open timeout → half-open transition
	time.Sleep(80 * time.Millisecond)

	// Next call transitions to half-open and tries the backend (still failing)
	_, err := cb.CreateInvoice(ctx, 100, "test", time.Minute)
	if err == nil {
		t.Fatal("expected error from failing backend")
	}

	// Should be back to open
	if cb.State() != CircuitOpen {
		t.Fatalf("expected open after failed probe, got %s", cb.State())
	}
}

func TestCircuitBreaker_SuccessResetsFailCount(t *testing.T) {
	mock := newFailingClient()
	cb := NewCircuitBreaker(mock, CircuitBreakerConfig{
		FailureThreshold: 3,
		OpenTimeout:      time.Second,
		ProbeInterval:    time.Second,
		HalfOpenMax:      1,
	})
	defer cb.Stop()

	ctx := context.Background()

	// 2 failures (below threshold)
	mock.shouldFail.Store(true)
	cb.CreateInvoice(ctx, 100, "test", time.Minute)
	cb.CreateInvoice(ctx, 100, "test", time.Minute)

	// 1 success resets counter
	mock.shouldFail.Store(false)
	cb.CreateInvoice(ctx, 100, "test", time.Minute)

	// 2 more failures — should NOT open (counter was reset)
	mock.shouldFail.Store(true)
	cb.CreateInvoice(ctx, 100, "test", time.Minute)
	cb.CreateInvoice(ctx, 100, "test", time.Minute)

	if cb.State() != CircuitClosed {
		t.Fatalf("expected closed (counter reset), got %s", cb.State())
	}
}
