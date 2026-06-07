// Package lightning defines the interface between ARFL's economic logic
// and the Lightning payment rail.
//
// WHY AN INTERFACE:
// The economic domain (tickets, settlement, payouts) must not depend on
// LND's gRPC protos, network availability, or implementation details.
// Phase 3 uses a mock for development and testing. The real LND adapter
// connects via Polar (local devnet) or mainnet — same interface, no
// business logic changes.
//
// If connecting real LND requires modifying settlement or payout logic,
// the abstraction has failed.
package lightning

import (
	"context"
	"fmt"
	"time"
)

// Invoice represents a Lightning payment request.
type Invoice struct {
	PaymentHash    string // hex-encoded payment hash (unique identifier)
	PaymentRequest string // BOLT11-encoded invoice string
	AmountSats     int64  // amount in satoshis
	Memo           string // human-readable description
	Status         InvoiceStatus
	CreatedAt      time.Time
	SettledAt      time.Time // zero if not settled
	ExpiresAt      time.Time
}

// InvoiceStatus represents the state of a Lightning invoice.
type InvoiceStatus int

const (
	InvoiceOpen    InvoiceStatus = iota // awaiting payment
	InvoiceSettled                      // payment received
	InvoiceExpired                      // not paid before expiry
)

func (s InvoiceStatus) String() string {
	switch s {
	case InvoiceOpen:
		return "open"
	case InvoiceSettled:
		return "settled"
	case InvoiceExpired:
		return "expired"
	default:
		return "unknown"
	}
}

// PaymentResult is the outcome of sending a payment to a node.
type PaymentResult struct {
	PaymentHash string
	Status      PaymentStatus
	FeeSats     int64  // routing fee paid
	Error       string // non-empty on failure
}

// PaymentStatus represents the outcome of an outbound payment.
type PaymentStatus int

const (
	PaymentSucceeded PaymentStatus = iota
	PaymentFailed
	PaymentInFlight // still routing — caller should retry later
)

func (s PaymentStatus) String() string {
	switch s {
	case PaymentSucceeded:
		return "succeeded"
	case PaymentFailed:
		return "failed"
	case PaymentInFlight:
		return "in_flight"
	default:
		return "unknown"
	}
}

// Client is the interface to a Lightning node.
// Implementations: MockClient (testing), LNDClient (production via Polar/mainnet).
type Client interface {
	// CreateInvoice generates a BOLT11 payment request.
	// The Hub calls this when a client selects a bandwidth tier.
	CreateInvoice(ctx context.Context, amountSats int64, memo string, expiry time.Duration) (*Invoice, error)

	// LookupInvoice checks the current status of an invoice by payment hash.
	// Used on Hub startup to reconcile invoices that settled while offline.
	LookupInvoice(ctx context.Context, paymentHash string) (*Invoice, error)

	// SubscribeInvoices opens a stream of invoice settlement events.
	// The returned channel emits settled invoices. The caller must cancel
	// the context to stop the subscription. If the connection drops,
	// the implementation should reconnect and resume.
	SubscribeInvoices(ctx context.Context) (<-chan *Invoice, error)

	// SendPayment pays a Lightning invoice (BOLT11).
	// Used by the settlement engine to pay nodes for bandwidth served.
	// Must be idempotent — if called twice for the same invoice, the
	// second call should detect the existing payment.
	SendPayment(ctx context.Context, paymentRequest string, amountSats int64) (*PaymentResult, error)

	// Keysend sends a spontaneous payment to a node by public key.
	// No invoice required — the receiver is identified by their Lightning pubkey.
	// Used for node payouts where we don't have an invoice from the node.
	Keysend(ctx context.Context, destPubkey string, amountSats int64) (*PaymentResult, error)
}

// Common errors.
var (
	ErrInvoiceNotFound   = fmt.Errorf("invoice not found")
	ErrPaymentFailed     = fmt.Errorf("payment failed")
	ErrNoRoute           = fmt.Errorf("no route to destination")
	ErrInsufficientFunds = fmt.Errorf("insufficient channel balance")
)
