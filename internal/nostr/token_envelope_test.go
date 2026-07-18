package nostr

import (
	"fmt"
	"testing"

	"github.com/elnosh/gonuts/cashu"
)

func TestTokenEnvelope_SealAndOpen(t *testing.T) {
	clientKP, _ := GenerateKeyPair()
	nodeKP, _ := GenerateKeyPair()

	payload := &TokenPayload{
		Proofs: cashu.Proofs{
			{Amount: 10, Id: "keyset-1", Secret: "secret-abc", C: "02deadbeef"},
			{Amount: 5, Id: "keyset-1", Secret: "secret-def", C: "02cafebabe"},
		},
		WGPubkey: "clientWgPubKey123==",
		Role:     "entry",
		Version:  1,
	}

	event, err := SealTokenEnvelope(clientKP, nodeKP.PubkeyHex(), payload)
	if err != nil {
		t.Fatalf("SealTokenEnvelope: %v", err)
	}

	// Event should be kind 21000 with a p-tag.
	if event.Kind != TokenEnvelopeKind {
		t.Errorf("kind = %d, want %d", event.Kind, TokenEnvelopeKind)
	}
	if len(event.Tags) == 0 || event.Tags[0][0] != "p" || event.Tags[0][1] != nodeKP.PubkeyHex() {
		t.Error("missing or incorrect p-tag")
	}

	// Content should be encrypted (not plaintext JSON).
	if event.Content == "" {
		t.Fatal("content is empty")
	}

	// Node decrypts.
	opened, err := OpenTokenEnvelope(event, nodeKP)
	if err != nil {
		t.Fatalf("OpenTokenEnvelope: %v", err)
	}

	if opened.WGPubkey != "clientWgPubKey123==" {
		t.Errorf("WGPubkey = %q, want %q", opened.WGPubkey, "clientWgPubKey123==")
	}
	if opened.Role != "entry" {
		t.Errorf("Role = %q, want %q", opened.Role, "entry")
	}
	if opened.Version != 1 {
		t.Errorf("Version = %d, want 1", opened.Version)
	}
	if len(opened.Proofs) != 2 {
		t.Fatalf("got %d proofs, want 2", len(opened.Proofs))
	}
	if opened.Proofs[0].Amount != 10 {
		t.Errorf("proof[0].Amount = %d, want 10", opened.Proofs[0].Amount)
	}
}

func TestTokenEnvelope_WrongRecipient(t *testing.T) {
	clientKP, _ := GenerateKeyPair()
	nodeKP, _ := GenerateKeyPair()
	evilKP, _ := GenerateKeyPair()

	payload := &TokenPayload{
		Proofs:   cashu.Proofs{{Amount: 1, Id: "ks", Secret: "s", C: "02c"}},
		WGPubkey: "pk==",
		Role:     "exit",
		Version:  1,
	}

	event, err := SealTokenEnvelope(clientKP, nodeKP.PubkeyHex(), payload)
	if err != nil {
		t.Fatalf("SealTokenEnvelope: %v", err)
	}

	// Evil node tries to decrypt — should fail.
	_, err = OpenTokenEnvelope(event, evilKP)
	if err == nil {
		t.Fatal("expected decryption to fail with wrong recipient key")
	}
}

func TestTokenEnvelope_WrongKind(t *testing.T) {
	nodeKP, _ := GenerateKeyPair()

	event := &Event{Kind: 1} // Regular text note, not token envelope.

	_, err := OpenTokenEnvelope(event, nodeKP)
	if err == nil {
		t.Fatal("expected error for wrong kind")
	}
}

func TestTokenEnvelope_MultipleProofs(t *testing.T) {
	clientKP, _ := GenerateKeyPair()
	nodeKP, _ := GenerateKeyPair()

	// Simulate a large token batch (64 proofs, max allowed).
	proofs := make(cashu.Proofs, 64)
	for i := range proofs {
		proofs[i] = cashu.Proof{
			Amount: uint64(1 << (i % 10)), // Powers of 2.
			Id:     "keyset-1",
			Secret: fmt.Sprintf("secret-%d", i),
			C:      "02aabbccdd",
		}
	}

	payload := &TokenPayload{
		Proofs:   proofs,
		WGPubkey: "bigBatchPubkey==",
		Role:     "exit",
		Version:  1,
	}

	event, err := SealTokenEnvelope(clientKP, nodeKP.PubkeyHex(), payload)
	if err != nil {
		t.Fatalf("SealTokenEnvelope with 64 proofs: %v", err)
	}

	opened, err := OpenTokenEnvelope(event, nodeKP)
	if err != nil {
		t.Fatalf("OpenTokenEnvelope: %v", err)
	}

	if len(opened.Proofs) != 64 {
		t.Errorf("got %d proofs, want 64", len(opened.Proofs))
	}
}

func TestPubkeyFromHex_Valid(t *testing.T) {
	kp, _ := GenerateKeyPair()
	hex := kp.PubkeyHex()

	pub, err := PubkeyFromHex(hex)
	if err != nil {
		t.Fatalf("PubkeyFromHex: %v", err)
	}
	if pub == nil {
		t.Fatal("pubkey is nil")
	}
}

func TestPubkeyFromHex_Invalid(t *testing.T) {
	_, err := PubkeyFromHex("not-a-hex-pubkey")
	if err == nil {
		t.Fatal("expected error for invalid hex")
	}

	_, err = PubkeyFromHex("aabbcc") // Too short.
	if err == nil {
		t.Fatal("expected error for short hex")
	}
}
