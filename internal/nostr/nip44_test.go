package nostr

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestNIP44_RoundTrip(t *testing.T) {
	alice, _ := GenerateKeyPair()
	bob, _ := GenerateKeyPair()

	convKeyAB, err := GetConversationKey(alice.PrivateKey, bob.PublicKey)
	if err != nil {
		t.Fatalf("GetConversationKey(a,B): %v", err)
	}
	convKeyBA, err := GetConversationKey(bob.PrivateKey, alice.PublicKey)
	if err != nil {
		t.Fatalf("GetConversationKey(b,A): %v", err)
	}

	// Conversation key must be symmetric.
	if hex.EncodeToString(convKeyAB) != hex.EncodeToString(convKeyBA) {
		t.Fatal("conversation keys are not symmetric")
	}

	plaintext := "Hello from ARFL! Here are your Cashu tokens: {\"proofs\":[{\"amount\":10}]}"

	encrypted, err := Encrypt(plaintext, convKeyAB)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Must be base64 and non-empty.
	if encrypted == "" || encrypted == plaintext {
		t.Fatal("encrypted output is empty or same as plaintext")
	}

	// Decrypt with the same conversation key.
	decrypted, err := Decrypt(encrypted, convKeyBA)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	if decrypted != plaintext {
		t.Fatalf("roundtrip failed: got %q, want %q", decrypted, plaintext)
	}
}

func TestNIP44_WrongKey(t *testing.T) {
	alice, _ := GenerateKeyPair()
	bob, _ := GenerateKeyPair()
	charlie, _ := GenerateKeyPair()

	convKeyAB, _ := GetConversationKey(alice.PrivateKey, bob.PublicKey)
	convKeyAC, _ := GetConversationKey(alice.PrivateKey, charlie.PublicKey)

	encrypted, err := Encrypt("secret message", convKeyAB)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Decrypt with wrong key should fail.
	_, err = Decrypt(encrypted, convKeyAC)
	if err == nil {
		t.Fatal("expected decryption to fail with wrong key")
	}
}

func TestNIP44_TamperedPayload(t *testing.T) {
	alice, _ := GenerateKeyPair()
	bob, _ := GenerateKeyPair()

	convKey, _ := GetConversationKey(alice.PrivateKey, bob.PublicKey)

	encrypted, err := Encrypt("do not tamper", convKey)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Flip a character in the middle of the base64.
	mid := len(encrypted) / 2
	tampered := encrypted[:mid] + "X" + encrypted[mid+1:]

	_, err = Decrypt(tampered, convKey)
	if err == nil {
		t.Fatal("expected decryption to fail with tampered payload")
	}
}

func TestNIP44_Padding(t *testing.T) {
	tests := []struct {
		inputLen  int
		expectPad int
	}{
		{1, 32},
		{2, 32},
		{31, 32},
		{32, 32},
		{33, 64},
		{64, 64},
		{65, 96},
		{100, 128},
		{255, 256},
		{256, 256},
		{257, 320},
	}

	for _, tt := range tests {
		got := calcPaddedLen(tt.inputLen)
		if got != tt.expectPad {
			t.Errorf("calcPaddedLen(%d) = %d, want %d", tt.inputLen, got, tt.expectPad)
		}
	}
}

func TestNIP44_PadUnpadRoundTrip(t *testing.T) {
	messages := []string{
		"a",
		"hello",
		strings.Repeat("x", 32),
		strings.Repeat("y", 33),
		strings.Repeat("z", 100),
		strings.Repeat("w", 1000),
		`{"proofs":[{"amount":10,"id":"ks1","secret":"s1","C":"02abc"}]}`,
	}

	for _, msg := range messages {
		padded := pad([]byte(msg))
		unpadded, err := unpad(padded)
		if err != nil {
			t.Fatalf("unpad(%q len=%d): %v", msg[:min(len(msg), 20)], len(msg), err)
		}
		if string(unpadded) != msg {
			t.Fatalf("pad/unpad roundtrip failed for len=%d", len(msg))
		}
	}
}

func TestNIP44_MinPayloadLength(t *testing.T) {
	_, err := Decrypt("short", make([]byte, 32))
	if err != ErrNIP44InvalidPayload {
		t.Errorf("expected ErrNIP44InvalidPayload for short payload, got %v", err)
	}
}

func TestNIP44_UnknownVersion(t *testing.T) {
	_, err := Decrypt("#future-encoding", make([]byte, 32))
	if err != ErrNIP44UnknownVersion {
		t.Errorf("expected ErrNIP44UnknownVersion for # prefix, got %v", err)
	}
}

func TestNIP44_EncryptDecrypt_LargeMessage(t *testing.T) {
	alice, _ := GenerateKeyPair()
	bob, _ := GenerateKeyPair()
	convKey, _ := GetConversationKey(alice.PrivateKey, bob.PublicKey)

	// 10KB message (simulating a large token batch).
	msg := strings.Repeat("token-data-", 1000)

	encrypted, err := Encrypt(msg, convKey)
	if err != nil {
		t.Fatalf("Encrypt large: %v", err)
	}

	decrypted, err := Decrypt(encrypted, convKey)
	if err != nil {
		t.Fatalf("Decrypt large: %v", err)
	}

	if decrypted != msg {
		t.Fatal("large message roundtrip failed")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
