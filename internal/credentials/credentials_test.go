package credentials

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"
)

func testSecret() []byte {
	secret := make([]byte, 32)
	rand.Read(secret)
	return secret
}

// --- Interface compliance ---

func TestHMACIssuer_ImplementsIssuer(t *testing.T) {
	var _ Issuer = (*HMACIssuer)(nil)
}

func TestHMACVerifier_ImplementsVerifier(t *testing.T) {
	var _ Verifier = (*HMACVerifier)(nil)
}

// --- Issuance ---

func TestIssue_ProducesCorrectCount(t *testing.T) {
	secret := testSecret()
	issuer := NewHMACIssuer("key-v1", secret)

	tickets, err := issuer.Issue("payment_hash_1", DefaultTicketBytes, 10)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if len(tickets) != 10 {
		t.Errorf("expected 10 tickets, got %d", len(tickets))
	}
}

func TestIssue_TicketsHaveUniqueIDs(t *testing.T) {
	secret := testSecret()
	issuer := NewHMACIssuer("key-v1", secret)

	tickets, _ := issuer.Issue("hash1", DefaultTicketBytes, 100)

	seen := make(map[string]bool)
	for _, ticket := range tickets {
		if seen[ticket.ID] {
			t.Fatalf("duplicate ticket ID: %s", ticket.ID)
		}
		seen[ticket.ID] = true
	}
}

func TestIssue_TicketsHaveCorrectFields(t *testing.T) {
	secret := testSecret()
	issuer := NewHMACIssuer("key-v1", secret)

	tickets, _ := issuer.Issue("hash1", DefaultTicketBytes, 1)
	ticket := tickets[0]

	if ticket.KeyID != "key-v1" {
		t.Errorf("expected key_id 'key-v1', got %s", ticket.KeyID)
	}
	if ticket.Bytes != DefaultTicketBytes {
		t.Errorf("expected %d bytes, got %d", DefaultTicketBytes, ticket.Bytes)
	}
	if ticket.MAC == "" {
		t.Error("MAC should not be empty")
	}
	if ticket.IssuedAt == 0 {
		t.Error("IssuedAt should be set")
	}
	if ticket.ExpiresAt <= ticket.IssuedAt {
		t.Error("ExpiresAt should be after IssuedAt")
	}
}

func TestIssue_RejectsZeroCount(t *testing.T) {
	issuer := NewHMACIssuer("key-v1", testSecret())
	_, err := issuer.Issue("hash1", DefaultTicketBytes, 0)
	if err == nil {
		t.Fatal("should reject zero count")
	}
}

func TestIssue_RejectsNegativeBytes(t *testing.T) {
	issuer := NewHMACIssuer("key-v1", testSecret())
	_, err := issuer.Issue("hash1", -100, 10)
	if err == nil {
		t.Fatal("should reject negative bytes")
	}
}

// --- Verification ---

func TestVerify_ValidTicket(t *testing.T) {
	secret := testSecret()
	issuer := NewHMACIssuer("key-v1", secret)
	verifier := NewHMACVerifier(map[string][]byte{"key-v1": secret})

	tickets, _ := issuer.Issue("hash1", DefaultTicketBytes, 1)

	if err := verifier.Verify(tickets[0]); err != nil {
		t.Fatalf("valid ticket should verify: %v", err)
	}
}

func TestVerify_RejectsTamperedBytes(t *testing.T) {
	// STRIDE/Tampering: attacker modifies bytes to get more bandwidth.
	secret := testSecret()
	issuer := NewHMACIssuer("key-v1", secret)
	verifier := NewHMACVerifier(map[string][]byte{"key-v1": secret})

	tickets, _ := issuer.Issue("hash1", DefaultTicketBytes, 1)
	tickets[0].Bytes = 999_999_999_999 // tamper

	if err := verifier.Verify(tickets[0]); err != ErrInvalidMAC {
		t.Fatalf("tampered ticket should fail with ErrInvalidMAC, got: %v", err)
	}
}

func TestVerify_RejectsTamperedExpiry(t *testing.T) {
	// STRIDE/Tampering: attacker extends expiry to keep using expired tickets.
	secret := testSecret()
	issuer := NewHMACIssuer("key-v1", secret)
	verifier := NewHMACVerifier(map[string][]byte{"key-v1": secret})

	tickets, _ := issuer.Issue("hash1", DefaultTicketBytes, 1)
	tickets[0].ExpiresAt = time.Now().Add(365 * 24 * time.Hour).Unix() // tamper

	if err := verifier.Verify(tickets[0]); err != ErrInvalidMAC {
		t.Fatalf("tampered expiry should fail with ErrInvalidMAC, got: %v", err)
	}
}

func TestVerify_RejectsTamperedID(t *testing.T) {
	// STRIDE/Tampering: attacker swaps ticket ID to replay a different ticket.
	secret := testSecret()
	issuer := NewHMACIssuer("key-v1", secret)
	verifier := NewHMACVerifier(map[string][]byte{"key-v1": secret})

	tickets, _ := issuer.Issue("hash1", DefaultTicketBytes, 2)
	tickets[0].ID = tickets[1].ID // swap IDs

	if err := verifier.Verify(tickets[0]); err != ErrInvalidMAC {
		t.Fatalf("swapped ID should fail with ErrInvalidMAC, got: %v", err)
	}
}

func TestVerify_RejectsExpiredTicket(t *testing.T) {
	secret := testSecret()
	issuer := NewHMACIssuer("key-v1", secret)
	verifier := NewHMACVerifier(map[string][]byte{"key-v1": secret})

	tickets, _ := issuer.Issue("hash1", DefaultTicketBytes, 1)

	// Set timestamps so structural validation passes (ExpiresAt > IssuedAt)
	// but expiry check fails (ExpiresAt < now).
	tickets[0].IssuedAt = time.Now().Add(-2 * time.Hour).Unix()
	tickets[0].ExpiresAt = time.Now().Add(-1 * time.Hour).Unix()
	// Re-stamp with valid MAC so we test expiry check, not MAC check.
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(tickets[0].Payload()))
	tickets[0].MAC = hex.EncodeToString(mac.Sum(nil))

	if err := verifier.Verify(tickets[0]); err != ErrExpiredTicket {
		t.Fatalf("expired ticket should fail with ErrExpiredTicket, got: %v", err)
	}
}

func TestVerify_RejectsUnknownKeyID(t *testing.T) {
	// STRIDE/Spoofing: attacker forges a ticket with a made-up key_id.
	secret := testSecret()
	issuer := NewHMACIssuer("key-v1", secret)
	verifier := NewHMACVerifier(map[string][]byte{"key-v2": testSecret()}) // different key

	tickets, _ := issuer.Issue("hash1", DefaultTicketBytes, 1)

	if err := verifier.Verify(tickets[0]); err != ErrUnknownKeyID {
		t.Fatalf("unknown key_id should fail with ErrUnknownKeyID, got: %v", err)
	}
}

func TestVerify_RejectsWrongSecret(t *testing.T) {
	// STRIDE/Spoofing: attacker has a valid key_id but wrong secret.
	issuer := NewHMACIssuer("key-v1", testSecret())
	verifier := NewHMACVerifier(map[string][]byte{"key-v1": testSecret()}) // different secret

	tickets, _ := issuer.Issue("hash1", DefaultTicketBytes, 1)

	if err := verifier.Verify(tickets[0]); err != ErrInvalidMAC {
		t.Fatalf("wrong secret should fail with ErrInvalidMAC, got: %v", err)
	}
}

// --- Key rotation ---

func TestVerify_KeyRotation_OldTicketsStillValid(t *testing.T) {
	// Key rotation: old tickets signed with v1 should still verify
	// after v2 is added, until they expire naturally.
	secretV1 := testSecret()
	secretV2 := testSecret()

	issuerV1 := NewHMACIssuer("key-v1", secretV1)
	verifier := NewHMACVerifier(map[string][]byte{
		"key-v1": secretV1,
		"key-v2": secretV2,
	})

	// Issue with v1.
	tickets, _ := issuerV1.Issue("hash1", DefaultTicketBytes, 1)

	// Should still verify even though v2 exists.
	if err := verifier.Verify(tickets[0]); err != nil {
		t.Fatalf("old key ticket should still verify: %v", err)
	}
}

func TestVerify_KeyRotation_AddKeyAtRuntime(t *testing.T) {
	secretV1 := testSecret()
	secretV2 := testSecret()

	issuerV2 := NewHMACIssuer("key-v2", secretV2)
	verifier := NewHMACVerifier(map[string][]byte{"key-v1": secretV1})

	tickets, _ := issuerV2.Issue("hash1", DefaultTicketBytes, 1)

	// v2 ticket should fail before key is added.
	if err := verifier.Verify(tickets[0]); err != ErrUnknownKeyID {
		t.Fatalf("expected ErrUnknownKeyID, got: %v", err)
	}

	// Add v2 key at runtime.
	verifier.AddKey("key-v2", secretV2)

	// Now it should verify.
	if err := verifier.Verify(tickets[0]); err != nil {
		t.Fatalf("should verify after key added: %v", err)
	}
}

// --- Tier tests ---

func TestLookupTier_ValidTiers(t *testing.T) {
	for _, id := range []string{"1gb", "10gb", "50gb"} {
		tier, err := LookupTier(id)
		if err != nil {
			t.Fatalf("LookupTier(%s): %v", id, err)
		}
		if tier.PriceSats <= 0 {
			t.Errorf("tier %s should have positive price", id)
		}
		if tier.TicketCount <= 0 {
			t.Errorf("tier %s should have positive ticket count", id)
		}
		if tier.TicketBytes*int64(tier.TicketCount) != tier.Bytes {
			t.Errorf("tier %s: tickets * ticket_bytes != total bytes", id)
		}
	}
}

func TestLookupTier_UnknownReturnsError(t *testing.T) {
	_, err := LookupTier("999gb")
	if err == nil {
		t.Fatal("unknown tier should return error")
	}
}

func TestTier_SatsPerByte(t *testing.T) {
	tier, _ := LookupTier("1gb")
	rate := tier.SatsPerByte()
	if rate <= 0 {
		t.Errorf("sats per byte should be positive, got %f", rate)
	}
}

func TestTier_EconomicsAreConsistent(t *testing.T) {
	// Larger tiers should have a lower sats-per-byte rate (volume discount).
	t1gb, _ := LookupTier("1gb")
	t10gb, _ := LookupTier("10gb")
	t50gb, _ := LookupTier("50gb")

	if t10gb.SatsPerByte() >= t1gb.SatsPerByte() {
		t.Error("10gb tier should be cheaper per byte than 1gb")
	}
	if t50gb.SatsPerByte() >= t10gb.SatsPerByte() {
		t.Error("50gb tier should be cheaper per byte than 10gb")
	}
}

// --- Payload determinism ---

func TestTicket_PayloadDeterministic(t *testing.T) {
	ticket := &Ticket{
		ID:        "abc123",
		KeyID:     "key-v1",
		Bytes:     100_000_000,
		IssuedAt:  1717700000,
		ExpiresAt: 1717786400,
	}

	p1 := ticket.Payload()
	p2 := ticket.Payload()

	if p1 != p2 {
		t.Fatalf("payload must be deterministic: got %q and %q", p1, p2)
	}
}

// --- Adversarial / edge case tests ---

func TestVerify_NilTicket(t *testing.T) {
	// STRIDE/DoS: nil ticket must not panic, must return error.
	verifier := NewHMACVerifier(map[string][]byte{"key-v1": testSecret()})
	err := verifier.Verify(nil)
	if err == nil {
		t.Fatal("nil ticket should return error")
	}
	if !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("expected ErrInvalidTicket, got: %v", err)
	}
}

func TestVerify_EmptyID(t *testing.T) {
	secret := testSecret()
	verifier := NewHMACVerifier(map[string][]byte{"key-v1": secret})

	ticket := &Ticket{
		ID: "", KeyID: "key-v1", Bytes: 100, IssuedAt: time.Now().Unix(),
		ExpiresAt: time.Now().Add(time.Hour).Unix(), MAC: "a" + string(make([]byte, 63)),
	}
	err := verifier.Verify(ticket)
	if !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("empty ID should be structurally invalid, got: %v", err)
	}
}

func TestVerify_NegativeBytes(t *testing.T) {
	// STRIDE/Elevation: attacker crafts ticket with negative bytes to
	// corrupt accounting if the system doesn't validate.
	secret := testSecret()
	verifier := NewHMACVerifier(map[string][]byte{"key-v1": secret})

	ticket := &Ticket{
		ID: "abc", KeyID: "key-v1", Bytes: -100_000_000,
		IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix(),
		MAC: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}

	err := verifier.Verify(ticket)
	if !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("negative bytes should be rejected, got: %v", err)
	}
}

func TestVerify_ZeroBytes(t *testing.T) {
	secret := testSecret()
	verifier := NewHMACVerifier(map[string][]byte{"key-v1": secret})

	ticket := &Ticket{
		ID: "abc", KeyID: "key-v1", Bytes: 0,
		IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix(),
		MAC: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}

	err := verifier.Verify(ticket)
	if !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("zero bytes should be rejected, got: %v", err)
	}
}

func TestVerify_MalformedMAC(t *testing.T) {
	secret := testSecret()
	verifier := NewHMACVerifier(map[string][]byte{"key-v1": secret})

	ticket := &Ticket{
		ID: "abc", KeyID: "key-v1", Bytes: 100,
		IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix(),
		MAC: "tooshort",
	}

	err := verifier.Verify(ticket)
	if !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("short MAC should be structurally invalid, got: %v", err)
	}
}

func TestVerify_EmptyMAC(t *testing.T) {
	secret := testSecret()
	verifier := NewHMACVerifier(map[string][]byte{"key-v1": secret})

	ticket := &Ticket{
		ID: "abc", KeyID: "key-v1", Bytes: 100,
		IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix(),
		MAC: "",
	}

	err := verifier.Verify(ticket)
	if !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("empty MAC should be rejected, got: %v", err)
	}
}

func TestVerify_ExpiresAtBeforeIssuedAt(t *testing.T) {
	secret := testSecret()
	verifier := NewHMACVerifier(map[string][]byte{"key-v1": secret})

	ticket := &Ticket{
		ID: "abc", KeyID: "key-v1", Bytes: 100,
		IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(-time.Hour).Unix(),
		MAC: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}

	err := verifier.Verify(ticket)
	// Should fail on either expiry or structural validation.
	if err == nil {
		t.Fatal("ExpiresAt < IssuedAt should be rejected")
	}
}

func TestIssue_RejectsExcessiveCount(t *testing.T) {
	// STRIDE/DoS: prevent memory exhaustion from huge count.
	issuer := NewHMACIssuer("key-v1", testSecret())
	_, err := issuer.Issue("hash1", DefaultTicketBytes, MaxTicketsPerIssuance+1)
	if err == nil {
		t.Fatal("should reject count exceeding MaxTicketsPerIssuance")
	}
}

func TestIssue_MaxCountSucceeds(t *testing.T) {
	issuer := NewHMACIssuer("key-v1", testSecret())
	tickets, err := issuer.Issue("hash1", DefaultTicketBytes, MaxTicketsPerIssuance)
	if err != nil {
		t.Fatalf("max count should succeed: %v", err)
	}
	if len(tickets) != MaxTicketsPerIssuance {
		t.Errorf("expected %d tickets, got %d", MaxTicketsPerIssuance, len(tickets))
	}
}

func TestSecret_MutationAfterConstruction(t *testing.T) {
	// Defensive copy: mutating the original secret after construction
	// must not affect issuance or verification.
	secret := []byte("this-is-a-secret-key-for-testing!")
	issuer := NewHMACIssuer("key-v1", secret)
	verifier := NewHMACVerifier(map[string][]byte{"key-v1": secret})

	// Issue a ticket before mutation.
	tickets, _ := issuer.Issue("hash1", DefaultTicketBytes, 1)

	// Mutate the original secret.
	secret[0] = 'X'
	secret[1] = 'X'

	// Ticket should still verify — issuer/verifier hold copies.
	if err := verifier.Verify(tickets[0]); err != nil {
		t.Fatalf("secret mutation should not affect verification: %v", err)
	}

	// New tickets should also still be consistent.
	tickets2, _ := issuer.Issue("hash2", DefaultTicketBytes, 1)
	if err := verifier.Verify(tickets2[0]); err != nil {
		t.Fatalf("post-mutation issuance should still verify: %v", err)
	}
}

func TestAddKey_MutationAfterRegistration(t *testing.T) {
	secret := []byte("another-secret-key-for-rotation!")
	verifier := NewHMACVerifier(map[string][]byte{})
	verifier.AddKey("key-v2", secret)

	issuer := NewHMACIssuer("key-v2", secret)
	tickets, _ := issuer.Issue("hash1", DefaultTicketBytes, 1)

	// Mutate the original slice.
	secret[0] = 'Z'

	// Should still verify.
	if err := verifier.Verify(tickets[0]); err != nil {
		t.Fatalf("AddKey mutation should not affect verification: %v", err)
	}
}

func TestIssuer_WithTicketTTLOption(t *testing.T) {
	// Functional option replaces SetTicketTTL — no race possible.
	secret := testSecret()
	ttl := 2 * time.Hour
	issuer := NewHMACIssuer("key-v1", secret, WithTicketTTL(ttl))

	tickets, _ := issuer.Issue("hash1", DefaultTicketBytes, 1)

	expectedExpiry := time.Now().Add(ttl).Unix()
	if abs(tickets[0].ExpiresAt-expectedExpiry) > 2 {
		t.Errorf("ticket expiry should be ~%d, got %d", expectedExpiry, tickets[0].ExpiresAt)
	}
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

func TestConcurrent_VerificationSafe(t *testing.T) {
	// Verify is called from multiple goroutines — must not race.
	secret := testSecret()
	issuer := NewHMACIssuer("key-v1", secret)
	verifier := NewHMACVerifier(map[string][]byte{"key-v1": secret})

	tickets, _ := issuer.Issue("hash1", DefaultTicketBytes, 50)

	var wg sync.WaitGroup
	for _, ticket := range tickets {
		wg.Add(1)
		go func(tk *Ticket) {
			defer wg.Done()
			if err := verifier.Verify(tk); err != nil {
				t.Errorf("concurrent verify failed: %v", err)
			}
		}(ticket)
	}
	wg.Wait()
}

// Helper to craft a ticket with a valid MAC for structural validation tests.
func craftValidMAC(secret []byte, t *Ticket) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(t.Payload()))
	return hex.EncodeToString(mac.Sum(nil))
}
