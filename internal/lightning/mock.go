package lightning

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// MockClient simulates a Lightning node for testing.
// It stores invoices in memory and can be configured to inject failures.
//
// This is NOT just a happy-path stub. It simulates:
//   - Payment settlement with configurable delay
//   - Payment failures (no route, insufficient funds)
//   - Invoice expiry
//   - Concurrent access from multiple goroutines
//   - Broadcast to multiple subscribers
type MockClient struct {
	mu       sync.Mutex
	invoices map[string]*Invoice // paymentHash → invoice
	payments map[string]*PaymentResult

	// Subscriber management — each subscriber gets its own channel.
	subs map[chan *Invoice]struct{}

	// Chaos injection — set these to simulate failures.
	CreateInvoiceErr error         // if set, CreateInvoice always fails
	SendPaymentErr   error         // if set, SendPayment always fails
	KeysendErr       error         // if set, Keysend always fails
	KeysendResult    *PaymentResult // if set, Keysend returns this instead of default success
	PaymentDelay     time.Duration // artificial delay on SendPayment/Keysend
}

// NewMockClient creates a mock Lightning client.
func NewMockClient() *MockClient {
	return &MockClient{
		invoices: make(map[string]*Invoice),
		payments: make(map[string]*PaymentResult),
		subs:     make(map[chan *Invoice]struct{}),
	}
}

// CreateInvoice generates a mock BOLT11 invoice.
func (m *MockClient) CreateInvoice(ctx context.Context, amountSats int64, memo string, expiry time.Duration) (*Invoice, error) {
	if m.CreateInvoiceErr != nil {
		return nil, m.CreateInvoiceErr
	}
	if amountSats <= 0 {
		return nil, fmt.Errorf("amount must be positive")
	}

	hash, err := randomHash()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	inv := &Invoice{
		PaymentHash:    hash,
		PaymentRequest: "lnbc" + hash[:16], // fake but recognizable
		AmountSats:     amountSats,
		Memo:           memo,
		Status:         InvoiceOpen,
		CreatedAt:      now,
		ExpiresAt:      now.Add(expiry),
	}

	m.mu.Lock()
	m.invoices[hash] = inv
	m.mu.Unlock()

	return inv, nil
}

// LookupInvoice returns the current state of an invoice.
func (m *MockClient) LookupInvoice(ctx context.Context, paymentHash string) (*Invoice, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	inv, ok := m.invoices[paymentHash]
	if !ok {
		return nil, ErrInvoiceNotFound
	}

	// Check if expired.
	if inv.Status == InvoiceOpen && time.Now().After(inv.ExpiresAt) {
		inv.Status = InvoiceExpired
	}

	// Return a copy to prevent mutation.
	copy := *inv
	return &copy, nil
}

// SubscribeInvoices returns a channel that emits settled invoices.
// Each subscriber gets its own channel and receives all settlements (broadcast).
func (m *MockClient) SubscribeInvoices(ctx context.Context) (<-chan *Invoice, error) {
	ch := make(chan *Invoice, 64)

	m.mu.Lock()
	m.subs[ch] = struct{}{}
	m.mu.Unlock()

	// Clean up on context cancellation.
	go func() {
		<-ctx.Done()
		m.mu.Lock()
		delete(m.subs, ch)
		m.mu.Unlock()
		close(ch)
	}()

	return ch, nil
}

// SendPayment simulates paying a Lightning invoice.
func (m *MockClient) SendPayment(ctx context.Context, paymentRequest string, amountSats int64) (*PaymentResult, error) {
	if m.SendPaymentErr != nil {
		return nil, m.SendPaymentErr
	}

	// Simulate payment delay.
	if m.PaymentDelay > 0 {
		select {
		case <-time.After(m.PaymentDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	hash, _ := randomHash()
	result := &PaymentResult{
		PaymentHash: hash,
		Status:      PaymentSucceeded,
		FeeSats:     1, // 1 sat routing fee
	}

	m.mu.Lock()
	m.payments[paymentRequest] = result
	m.mu.Unlock()

	return result, nil
}

// Keysend simulates a spontaneous payment to a node by public key.
func (m *MockClient) Keysend(ctx context.Context, destPubkey string, amountSats int64) (*PaymentResult, error) {
	if m.KeysendErr != nil {
		return nil, m.KeysendErr
	}

	if m.PaymentDelay > 0 {
		select {
		case <-time.After(m.PaymentDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// Allow tests to inject custom results (e.g., PaymentInFlight).
	if m.KeysendResult != nil {
		copy := *m.KeysendResult
		return &copy, nil
	}

	hash, _ := randomHash()
	result := &PaymentResult{
		PaymentHash: hash,
		Status:      PaymentSucceeded,
		FeeSats:     1,
	}

	m.mu.Lock()
	m.payments["keysend:"+destPubkey] = result
	m.mu.Unlock()

	return result, nil
}

// --- Test helpers (not part of the Client interface) ---

// SimulateSettlement marks an invoice as settled and notifies all subscribers.
// Call this from tests to simulate a client paying an invoice.
func (m *MockClient) SimulateSettlement(paymentHash string) error {
	m.mu.Lock()
	inv, ok := m.invoices[paymentHash]
	if !ok {
		m.mu.Unlock()
		return ErrInvoiceNotFound
	}
	if inv.Status != InvoiceOpen {
		m.mu.Unlock()
		return fmt.Errorf("invoice is %s, not open", inv.Status)
	}
	inv.Status = InvoiceSettled
	inv.SettledAt = time.Now()

	// Broadcast to all subscribers (non-blocking per channel).
	for ch := range m.subs {
		copy := *inv
		select {
		case ch <- &copy:
		default:
		}
	}
	m.mu.Unlock()

	return nil
}

// SimulateSettlementWithDelay settles an invoice after a delay.
// Useful for testing async settlement flows.
func (m *MockClient) SimulateSettlementWithDelay(paymentHash string, delay time.Duration) {
	go func() {
		time.Sleep(delay)
		m.SimulateSettlement(paymentHash)
	}()
}

// GetPayment returns the result of a previous SendPayment call.
func (m *MockClient) GetPayment(paymentRequest string) (*PaymentResult, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.payments[paymentRequest]
	return r, ok
}

// InvoiceCount returns the number of invoices created.
func (m *MockClient) InvoiceCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.invoices)
}

func randomHash() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
