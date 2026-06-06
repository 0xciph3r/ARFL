// Package credentials defines the ticket abstraction for bandwidth purchases.
//
// WHY THIS EXISTS:
// A ticket is a "redeemable economic object" — proof that someone paid for
// bandwidth. Phase 3 stamps tickets with HMAC-SHA256. Phase 4 will swap to
// blind signatures. The rest of the system (nodes, settlement, payouts) MUST
// NOT care which crypto is behind the interface.
//
// DESIGN RULES:
//   - Tickets are atomic: fully consumed or not consumed at all.
//   - Tickets are single-use: redeemed once, then destroyed.
//   - Ticket format is crypto-agnostic: ID, bytes, timestamps, opaque MAC.
//   - key_id supports rotation without breaking outstanding tickets.
package credentials

import "fmt"

// Ticket is the wire format clients present to redeem bandwidth.
// The MAC field is opaque to consumers — they don't know or care if it's
// an HMAC or a blind signature. That's the Phase 4 migration path.
type Ticket struct {
	ID        string `json:"id"`         // globally unique ticket ID
	KeyID     string `json:"key_id"`     // which key signed this (rotation support)
	Bytes     int64  `json:"bytes"`      // bandwidth value in bytes (atomic — all or nothing)
	IssuedAt  int64  `json:"issued_at"`  // unix timestamp
	ExpiresAt int64  `json:"expires_at"` // unix timestamp
	MAC       string `json:"mac"`        // hex-encoded; HMAC-SHA256 in Phase 3, blind sig in Phase 4
}

// Payload returns the canonical byte string that the MAC covers.
// This MUST be deterministic — same fields → same bytes → same MAC.
// Order matters: changing the field order breaks all outstanding tickets.
func (t *Ticket) Payload() string {
	return fmt.Sprintf("%s|%s|%d|%d|%d", t.ID, t.KeyID, t.Bytes, t.IssuedAt, t.ExpiresAt)
}

// Issuer creates tickets after a payment is verified.
// Phase 3: HMACIssuer. Phase 4: BlindSignatureIssuer.
type Issuer interface {
	// Issue creates `count` tickets, each worth `bytesPerTicket` bytes,
	// linked to the given payment hash. Returns the tickets or an error.
	Issue(paymentHash string, bytesPerTicket int64, count int) ([]*Ticket, error)
}

// Verifier checks whether a ticket is authentic and untampered.
// Nodes call this — they MUST NOT know what crypto is inside.
// Phase 3: HMACVerifier (runs on Hub). Phase 4: BlindSigVerifier (can run on nodes).
type Verifier interface {
	// Verify checks the ticket's MAC against its payload.
	// Returns nil if valid, or an error describing the failure.
	Verify(ticket *Ticket) error
}

// Common errors returned by Verifier implementations.
var (
	ErrInvalidMAC   = fmt.Errorf("ticket MAC is invalid")
	ErrExpiredTicket = fmt.Errorf("ticket has expired")
	ErrUnknownKeyID = fmt.Errorf("ticket key_id is not recognized")
)
