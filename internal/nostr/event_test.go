package nostr

import (
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

func TestGenerateKeyPair(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}

	pubHex := kp.PubkeyHex()
	if len(pubHex) != 64 {
		t.Errorf("pubkey hex should be 64 chars (32 bytes), got %d", len(pubHex))
	}

	privHex := kp.PrivkeyHex()
	if len(privHex) != 64 {
		t.Errorf("privkey hex should be 64 chars (32 bytes), got %d", len(privHex))
	}
}

func TestKeyPairFromPrivHex_RoundTrip(t *testing.T) {
	kp1, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}

	kp2, err := KeyPairFromPrivHex(kp1.PrivkeyHex())
	if err != nil {
		t.Fatalf("KeyPairFromPrivHex failed: %v", err)
	}

	if kp1.PubkeyHex() != kp2.PubkeyHex() {
		t.Error("round-tripped keypair has different pubkey")
	}
}

func TestKeyPairFromPrivHex_InvalidLength(t *testing.T) {
	_, err := KeyPairFromPrivHex("deadbeef")
	if err == nil {
		t.Error("expected error for short private key")
	}
}

func TestEvent_ComputeID(t *testing.T) {
	// Create a deterministic event and verify the ID is a valid 64-char hex SHA-256.
	e := &Event{
		Pubkey:    "0000000000000000000000000000000000000000000000000000000000000001",
		CreatedAt: 1700000000,
		Kind:      1,
		Tags:      Tags{},
		Content:   "hello world",
	}

	id, err := e.ComputeID()
	if err != nil {
		t.Fatalf("ComputeID failed: %v", err)
	}
	if len(id) != 64 {
		t.Errorf("event ID should be 64 hex chars, got %d", len(id))
	}

	// Same inputs must produce same ID (deterministic).
	id2, err := e.ComputeID()
	if err != nil {
		t.Fatalf("second ComputeID failed: %v", err)
	}
	if id != id2 {
		t.Error("same event produced different IDs")
	}
}

func TestEvent_ComputeID_DifferentContent(t *testing.T) {
	// Changing any field must change the ID — this is the tamper-proof guarantee.
	e1 := &Event{
		Pubkey:    "0000000000000000000000000000000000000000000000000000000000000001",
		CreatedAt: 1700000000,
		Kind:      1,
		Tags:      Tags{},
		Content:   "hello",
	}
	e2 := &Event{
		Pubkey:    "0000000000000000000000000000000000000000000000000000000000000001",
		CreatedAt: 1700000000,
		Kind:      1,
		Tags:      Tags{},
		Content:   "world",
	}

	id1, _ := e1.ComputeID()
	id2, _ := e2.ComputeID()
	if id1 == id2 {
		t.Error("different content should produce different IDs")
	}
}

func TestEvent_SignAndVerify(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	e := &Event{
		CreatedAt: time.Now().Unix(),
		Kind:      30078,
		Tags:      Tags{{"d", "test-node"}},
		Content:   `{"wg_pubkey":"abc123"}`,
	}

	if err := e.Sign(kp); err != nil {
		t.Fatalf("Sign failed: %v", err)
	}

	// Pubkey should be set by Sign.
	if e.Pubkey != kp.PubkeyHex() {
		t.Error("Sign should set event pubkey to signer's pubkey")
	}

	// ID should be set.
	if e.ID == "" {
		t.Error("Sign should set event ID")
	}

	// Sig should be set (64 bytes = 128 hex chars).
	if len(e.Sig) != 128 {
		t.Errorf("signature should be 128 hex chars, got %d", len(e.Sig))
	}

	// Verification should pass.
	if err := e.Verify(); err != nil {
		t.Errorf("Verify failed on valid event: %v", err)
	}
}

func TestEvent_Verify_TamperedContent(t *testing.T) {
	// Sign an event, then tamper with the content.
	// Verify must fail — this proves tamper detection works.
	kp, _ := GenerateKeyPair()

	e := &Event{
		CreatedAt: time.Now().Unix(),
		Kind:      1,
		Tags:      Tags{},
		Content:   "original",
	}
	e.Sign(kp)

	// Tamper with content after signing.
	e.Content = "tampered"

	if err := e.Verify(); err == nil {
		t.Error("Verify should fail on tampered event")
	}
}

func TestEvent_Verify_WrongPubkey(t *testing.T) {
	// Sign with one key, replace pubkey with another.
	// This simulates a spoofing attack.
	kp1, _ := GenerateKeyPair()
	kp2, _ := GenerateKeyPair()

	e := &Event{
		CreatedAt: time.Now().Unix(),
		Kind:      1,
		Tags:      Tags{},
		Content:   "test",
	}
	e.Sign(kp1)

	// Replace pubkey with a different key (spoofing attempt).
	e.Pubkey = kp2.PubkeyHex()

	if err := e.Verify(); err == nil {
		t.Error("Verify should fail when pubkey doesn't match signer")
	}
}

func TestEvent_Verify_InvalidSignature(t *testing.T) {
	kp, _ := GenerateKeyPair()
	e := &Event{
		CreatedAt: time.Now().Unix(),
		Kind:      1,
		Tags:      Tags{},
		Content:   "test",
	}
	e.Sign(kp)

	// Corrupt the signature.
	sigBytes, _ := hex.DecodeString(e.Sig)
	sigBytes[0] ^= 0xFF
	e.Sig = hex.EncodeToString(sigBytes)

	if err := e.Verify(); err == nil {
		t.Error("Verify should fail on corrupted signature")
	}
}

func TestEvent_JSON_RoundTrip(t *testing.T) {
	// Events must serialize to JSON correctly for Nostr relay compatibility.
	kp, _ := GenerateKeyPair()
	e := &Event{
		CreatedAt: time.Now().Unix(),
		Kind:      30078,
		Tags:      Tags{{"d", "node-1"}, {"hub", "abc123"}},
		Content:   `{"endpoint":"1.2.3.4:51820"}`,
	}
	e.Sign(kp)

	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded Event
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.ID != e.ID {
		t.Error("ID mismatch after round-trip")
	}
	if decoded.Pubkey != e.Pubkey {
		t.Error("Pubkey mismatch after round-trip")
	}
	if decoded.Sig != e.Sig {
		t.Error("Sig mismatch after round-trip")
	}
	if err := decoded.Verify(); err != nil {
		t.Errorf("Verify failed after JSON round-trip: %v", err)
	}
}

func TestEvent_GetTagValue(t *testing.T) {
	e := &Event{
		Tags: Tags{
			{"d", "node-123"},
			{"hub", "hub-pubkey"},
			{"attestation", "sig-data"},
		},
	}

	if got := e.GetTagValue("d"); got != "node-123" {
		t.Errorf("GetTagValue('d') = %q, want 'node-123'", got)
	}
	if got := e.GetTagValue("hub"); got != "hub-pubkey" {
		t.Errorf("GetTagValue('hub') = %q, want 'hub-pubkey'", got)
	}
	if got := e.GetTagValue("missing"); got != "" {
		t.Errorf("GetTagValue('missing') = %q, want empty", got)
	}
}

func TestEvent_NIP01Serialization(t *testing.T) {
	// Verify the serialization format matches NIP-01 spec:
	// [0, pubkey, created_at, kind, tags, content]
	e := &Event{
		Pubkey:    "aabbccdd",
		CreatedAt: 1234567890,
		Kind:      1,
		Tags:      Tags{{"p", "deadbeef"}},
		Content:   "hi",
	}

	data, err := e.Serialize()
	if err != nil {
		t.Fatalf("Serialize failed: %v", err)
	}

	// Parse it back as a generic JSON array and verify structure.
	var arr []json.RawMessage
	if err := json.Unmarshal(data, &arr); err != nil {
		t.Fatalf("serialized form is not a JSON array: %v", err)
	}
	if len(arr) != 6 {
		t.Fatalf("NIP-01 array must have 6 elements, got %d", len(arr))
	}

	// Element 0 must be 0 (version).
	if string(arr[0]) != "0" {
		t.Errorf("element 0 should be 0, got %s", arr[0])
	}
	// Element 1 must be the pubkey.
	if string(arr[1]) != `"aabbccdd"` {
		t.Errorf("element 1 should be pubkey, got %s", arr[1])
	}
	// Element 2 must be created_at.
	if string(arr[2]) != "1234567890" {
		t.Errorf("element 2 should be created_at, got %s", arr[2])
	}
}
