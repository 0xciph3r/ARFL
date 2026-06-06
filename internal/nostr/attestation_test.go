package nostr

import (
	"testing"
	"time"
)

func TestAttestation_CreateAndVerify(t *testing.T) {
	hubKP, _ := GenerateKeyPair()
	nodeKP, _ := GenerateKeyPair()

	att, err := CreateAttestation(hubKP, nodeKP.PubkeyHex(), "wg-pubkey-abc", "operator-1", []string{"entry", "exit"})
	if err != nil {
		t.Fatalf("CreateAttestation: %v", err)
	}

	// Verify against the hub's pubkey.
	if err := att.Verify([]string{hubKP.PubkeyHex()}); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestAttestation_RejectsUntrustedHub(t *testing.T) {
	// STRIDE: Spoofing — a rogue hub tries to vouch for a node.
	hubKP, _ := GenerateKeyPair()
	rogueKP, _ := GenerateKeyPair()
	nodeKP, _ := GenerateKeyPair()

	att, _ := CreateAttestation(hubKP, nodeKP.PubkeyHex(), "wg-pubkey", "op-1", []string{"entry"})

	// Verify against the ROGUE's pubkey — should fail.
	if err := att.Verify([]string{rogueKP.PubkeyHex()}); err == nil {
		t.Error("should reject attestation from untrusted hub")
	}
}

func TestAttestation_RejectsExpired(t *testing.T) {
	// STRIDE: Spoofing — compromised node tries to use an old attestation.
	hubKP, _ := GenerateKeyPair()
	nodeKP, _ := GenerateKeyPair()

	att, _ := CreateAttestation(hubKP, nodeKP.PubkeyHex(), "wg-pubkey", "op-1", []string{"entry"})

	// Expire the attestation by backdating it.
	att.ExpiresAt = time.Now().Add(-1 * time.Hour).Unix()

	// Re-sign would be needed for a real attack, but here we just check expiry.
	if err := att.Verify([]string{hubKP.PubkeyHex()}); err == nil {
		t.Error("should reject expired attestation")
	}
}

func TestAttestation_RejectsTamperedContent(t *testing.T) {
	// STRIDE: Tampering — attacker modifies attestation fields after signing.
	hubKP, _ := GenerateKeyPair()
	nodeKP, _ := GenerateKeyPair()

	att, _ := CreateAttestation(hubKP, nodeKP.PubkeyHex(), "wg-pubkey", "op-1", []string{"entry"})

	// Tamper: change the allowed roles.
	att.AllowedRoles = []string{"entry", "exit"}

	// Attestation ID won't match because content changed.
	if err := att.Verify([]string{hubKP.PubkeyHex()}); err == nil {
		t.Error("should reject tampered attestation")
	}
}

func TestAttestation_NodeBinding_CorrectNode(t *testing.T) {
	hubKP, _ := GenerateKeyPair()
	nodeKP, _ := GenerateKeyPair()

	att, _ := CreateAttestation(hubKP, nodeKP.PubkeyHex(), "wg-pubkey-abc", "op-1", []string{"entry"})

	if err := att.VerifyNodeBinding(nodeKP.PubkeyHex(), "wg-pubkey-abc", "entry"); err != nil {
		t.Fatalf("VerifyNodeBinding should pass for correct node: %v", err)
	}
}

func TestAttestation_NodeBinding_RejectsWrongPubkey(t *testing.T) {
	// STRIDE: Spoofing — node B tries to use node A's attestation.
	hubKP, _ := GenerateKeyPair()
	nodeA, _ := GenerateKeyPair()
	nodeB, _ := GenerateKeyPair()

	att, _ := CreateAttestation(hubKP, nodeA.PubkeyHex(), "wg-pubkey-a", "op-1", []string{"entry"})

	// Node B tries to claim this attestation.
	if err := att.VerifyNodeBinding(nodeB.PubkeyHex(), "wg-pubkey-a", "entry"); err == nil {
		t.Error("should reject attestation for different node pubkey")
	}
}

func TestAttestation_NodeBinding_RejectsWrongWGKey(t *testing.T) {
	// STRIDE: Tampering — node changes WireGuard key without getting re-attested.
	hubKP, _ := GenerateKeyPair()
	nodeKP, _ := GenerateKeyPair()

	att, _ := CreateAttestation(hubKP, nodeKP.PubkeyHex(), "wg-pubkey-original", "op-1", []string{"entry"})

	if err := att.VerifyNodeBinding(nodeKP.PubkeyHex(), "wg-pubkey-CHANGED", "entry"); err == nil {
		t.Error("should reject when WG pubkey doesn't match attestation")
	}
}

func TestAttestation_NodeBinding_RejectsUnallowedRole(t *testing.T) {
	// STRIDE: Elevation of Privilege — entry-only node advertises as exit.
	hubKP, _ := GenerateKeyPair()
	nodeKP, _ := GenerateKeyPair()

	att, _ := CreateAttestation(hubKP, nodeKP.PubkeyHex(), "wg-pubkey", "op-1", []string{"entry"})

	if err := att.VerifyNodeBinding(nodeKP.PubkeyHex(), "wg-pubkey", "exit"); err == nil {
		t.Error("should reject node advertising a role not in attestation")
	}
}

func TestAttestation_JSONRoundTrip(t *testing.T) {
	// STRIDE: Repudiation — attestation must survive serialization for storage/relay.
	hubKP, _ := GenerateKeyPair()
	nodeKP, _ := GenerateKeyPair()

	att, _ := CreateAttestation(hubKP, nodeKP.PubkeyHex(), "wg-pubkey", "op-1", []string{"entry", "exit"})

	encoded, err := att.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	decoded, err := DecodeAttestation(encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	// Verification must still pass after round-trip.
	if err := decoded.Verify([]string{hubKP.PubkeyHex()}); err != nil {
		t.Errorf("Verify after round-trip: %v", err)
	}
}

func TestAttestation_MultipleTrustedHubs(t *testing.T) {
	// v2 scenario: client trusts multiple hubs.
	hub1, _ := GenerateKeyPair()
	hub2, _ := GenerateKeyPair()
	nodeKP, _ := GenerateKeyPair()

	att, _ := CreateAttestation(hub2, nodeKP.PubkeyHex(), "wg-pubkey", "op-1", []string{"entry"})

	// Should verify against a list that includes hub2.
	trusted := []string{hub1.PubkeyHex(), hub2.PubkeyHex()}
	if err := att.Verify(trusted); err != nil {
		t.Errorf("should accept attestation from any trusted hub: %v", err)
	}
}
