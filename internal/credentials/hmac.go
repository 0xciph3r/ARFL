package credentials

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// DefaultTicketBytes is the default bandwidth per ticket (100 MB).
// Configurable — real usage data will tell us if this needs adjustment.
// Uses decimal (SI) units to match network bandwidth conventions.
const DefaultTicketBytes int64 = 100_000_000 // 100 MB

// DefaultTicketTTL is how long a ticket remains valid after issuance.
const DefaultTicketTTL = 24 * time.Hour

// HMACIssuer creates tickets stamped with HMAC-SHA256.
// This is the Phase 3 implementation — replaced by blind signatures in Phase 4.
// Immutable after construction — TTL is set via functional option at creation time.
type HMACIssuer struct {
	keyID     string
	secret    []byte
	ticketTTL time.Duration
}

// NewHMACIssuer creates an issuer with the given key ID and secret.
// The keyID is embedded in every ticket for rotation support.
// The secret is defensively copied — callers cannot mutate it after construction.
func NewHMACIssuer(keyID string, secret []byte, opts ...IssuerOption) *HMACIssuer {
	h := &HMACIssuer{
		keyID:     keyID,
		secret:    append([]byte(nil), secret...), // defensive copy
		ticketTTL: DefaultTicketTTL,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// IssuerOption configures an HMACIssuer at construction time.
type IssuerOption func(*HMACIssuer)

// WithTicketTTL sets the ticket lifetime. Must be set at construction —
// the issuer is immutable after creation (no race conditions).
func WithTicketTTL(ttl time.Duration) IssuerOption {
	return func(h *HMACIssuer) {
		h.ticketTTL = ttl
	}
}

// Issue creates `count` tickets, each worth `bytesPerTicket` bytes.
// Each ticket gets a unique random ID and is stamped with HMAC-SHA256.
func (h *HMACIssuer) Issue(paymentHash string, bytesPerTicket int64, count int) ([]*Ticket, error) {
	if count <= 0 {
		return nil, fmt.Errorf("count must be positive, got %d", count)
	}
	if count > MaxTicketsPerIssuance {
		return nil, fmt.Errorf("count %d exceeds maximum %d", count, MaxTicketsPerIssuance)
	}
	if bytesPerTicket <= 0 {
		return nil, fmt.Errorf("bytesPerTicket must be positive, got %d", bytesPerTicket)
	}

	now := time.Now().Unix()
	expires := time.Now().Add(h.ticketTTL).Unix()
	tickets := make([]*Ticket, count)

	for i := 0; i < count; i++ {
		id, err := randomTicketID()
		if err != nil {
			return nil, fmt.Errorf("generate ticket ID: %w", err)
		}

		t := &Ticket{
			ID:        id,
			KeyID:     h.keyID,
			Bytes:     bytesPerTicket,
			IssuedAt:  now,
			ExpiresAt: expires,
		}

		t.MAC = h.computeMAC(t)
		tickets[i] = t
	}

	return tickets, nil
}

// computeMAC computes HMAC-SHA256 over the ticket's canonical payload.
func (h *HMACIssuer) computeMAC(t *Ticket) string {
	mac := hmac.New(sha256.New, h.secret)
	mac.Write([]byte(t.Payload()))
	return hex.EncodeToString(mac.Sum(nil))
}

// HMACVerifier validates tickets using HMAC-SHA256.
// Supports multiple keys for rotation — old tickets signed with previous
// keys remain valid until they expire naturally.
type HMACVerifier struct {
	keys map[string][]byte // keyID → secret
	mu   sync.RWMutex
}

// NewHMACVerifier creates a verifier with one or more keys.
// All secrets are defensively copied.
func NewHMACVerifier(keys map[string][]byte) *HMACVerifier {
	copied := make(map[string][]byte, len(keys))
	for k, v := range keys {
		copied[k] = append([]byte(nil), v...) // defensive copy
	}
	return &HMACVerifier{keys: copied}
}

// AddKey registers a new key for verification (rotation support).
// The secret is defensively copied.
func (v *HMACVerifier) AddKey(keyID string, secret []byte) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.keys[keyID] = append([]byte(nil), secret...) // defensive copy
}

// Verify checks structural validity, expiry, and MAC authenticity.
func (v *HMACVerifier) Verify(ticket *Ticket) error {
	// Structural validation first — catch garbage before any crypto.
	if err := ticket.ValidateStructure(); err != nil {
		return err
	}

	// Check expiry — >= means "at the exact second it expires, it's expired."
	if time.Now().Unix() >= ticket.ExpiresAt {
		return ErrExpiredTicket
	}

	v.mu.RLock()
	secret, ok := v.keys[ticket.KeyID]
	v.mu.RUnlock()

	if !ok {
		return ErrUnknownKeyID
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(ticket.Payload()))
	expected := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(expected), []byte(ticket.MAC)) {
		return ErrInvalidMAC
	}

	return nil
}

// randomTicketID generates a 16-byte random hex string.
func randomTicketID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
