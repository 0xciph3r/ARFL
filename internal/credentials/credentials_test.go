package credentials

import (
	"crypto/rand"
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

	// Directly set expiry in the past — don't rely on sleep timing.
	tickets[0].ExpiresAt = time.Now().Add(-1 * time.Hour).Unix()
	// Re-stamp with valid MAC so we test expiry check, not MAC check.
	tickets[0].MAC = issuer.computeMAC(tickets[0])

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
