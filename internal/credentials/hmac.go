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
type HMACIssuer struct {
	keyID     string
	secret    []byte
	ticketTTL time.Duration
}

// NewHMACIssuer creates an issuer with the given key ID and secret.
// The keyID is embedded in every ticket for rotation support.
func NewHMACIssuer(keyID string, secret []byte) *HMACIssuer {
	return &HMACIssuer{
		keyID:     keyID,
		secret:    secret,
		ticketTTL: DefaultTicketTTL,
	}
}

// SetTicketTTL overrides the default ticket lifetime.
func (h *HMACIssuer) SetTicketTTL(ttl time.Duration) {
	h.ticketTTL = ttl
}

// Issue creates `count` tickets, each worth `bytesPerTicket` bytes.
// Each ticket gets a unique random ID and is stamped with HMAC-SHA256.
func (h *HMACIssuer) Issue(paymentHash string, bytesPerTicket int64, count int) ([]*Ticket, error) {
	if count <= 0 {
		return nil, fmt.Errorf("count must be positive, got %d", count)
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
func NewHMACVerifier(keys map[string][]byte) *HMACVerifier {
	copied := make(map[string][]byte, len(keys))
	for k, v := range keys {
		copied[k] = v
	}
	return &HMACVerifier{keys: copied}
}

// AddKey registers a new key for verification (rotation support).
func (v *HMACVerifier) AddKey(keyID string, secret []byte) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.keys[keyID] = secret
}

// Verify checks that the ticket's MAC is valid and the ticket hasn't expired.
func (v *HMACVerifier) Verify(ticket *Ticket) error {
	// Check expiry first — no point verifying MAC on expired tickets.
	if time.Now().Unix() > ticket.ExpiresAt {
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
