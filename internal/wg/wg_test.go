package wg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateKeyPair(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	// WireGuard keys are 32 bytes, base64-encoded = 44 characters
	if len(kp.PrivateKey) != 44 {
		t.Errorf("private key length = %d, want 44", len(kp.PrivateKey))
	}
	if len(kp.PublicKey) != 44 {
		t.Errorf("public key length = %d, want 44", len(kp.PublicKey))
	}

	// Private and public keys must be different
	if kp.PrivateKey == kp.PublicKey {
		t.Error("private key equals public key — this should never happen")
	}
}

func TestGenerateKeyPair_Unique(t *testing.T) {
	// Two keypairs should never be identical.
	// The probability of collision on Curve25519 is ~2^-256 — effectively zero.
	kp1, _ := GenerateKeyPair()
	kp2, _ := GenerateKeyPair()

	if kp1.PrivateKey == kp2.PrivateKey {
		t.Error("two generated private keys are identical — CSPRNG is broken")
	}
	if kp1.PublicKey == kp2.PublicKey {
		t.Error("two generated public keys are identical — CSPRNG is broken")
	}
}

func TestParseKey_Valid(t *testing.T) {
	kp, _ := GenerateKeyPair()

	key, err := ParseKey(kp.PublicKey)
	if err != nil {
		t.Fatalf("ParseKey: %v", err)
	}
	if len(key) != 32 {
		t.Errorf("parsed key length = %d, want 32", len(key))
	}
}

func TestParseKey_Invalid(t *testing.T) {
	_, err := ParseKey("not-a-valid-base64-key!!!")
	if err == nil {
		t.Error("ParseKey should reject invalid base64")
	}

	_, err = ParseKey("dG9vc2hvcnQ=") // valid base64 but wrong length
	if err == nil {
		t.Error("ParseKey should reject wrong-length keys")
	}
}

func TestEncryptedKeyStore_RoundTrip(t *testing.T) {
	// Generate a keypair, encrypt it, save it, load it back, verify it matches.
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.key")
	passphrase := "correct horse battery staple"

	// Save encrypted
	if err := SaveKeyPairEncrypted(path, kp, passphrase); err != nil {
		t.Fatalf("SaveKeyPairEncrypted: %v", err)
	}

	// Verify file exists and is not the plaintext private key
	data, _ := os.ReadFile(path)
	if string(data) == kp.PrivateKey {
		t.Fatal("key file contains plaintext private key — encryption failed")
	}

	// Load with correct passphrase
	loaded, err := LoadKeyPairEncrypted(path, passphrase)
	if err != nil {
		t.Fatalf("LoadKeyPairEncrypted: %v", err)
	}

	if loaded.PrivateKey != kp.PrivateKey {
		t.Error("decrypted private key does not match original")
	}
	if loaded.PublicKey != kp.PublicKey {
		t.Error("public key does not match original")
	}
}

func TestEncryptedKeyStore_WrongPassphrase(t *testing.T) {
	// Encrypting with one passphrase and decrypting with another must fail.
	// This is the most important security test — if this passes with the wrong
	// passphrase, the encryption is broken.
	kp, _ := GenerateKeyPair()

	dir := t.TempDir()
	path := filepath.Join(dir, "test.key")

	SaveKeyPairEncrypted(path, kp, "right passphrase")

	_, err := LoadKeyPairEncrypted(path, "wrong passphrase")
	if err == nil {
		t.Fatal("decryption succeeded with wrong passphrase — CRITICAL SECURITY BUG")
	}
}

func TestEncryptedKeyStore_FilePermissions(t *testing.T) {
	// Key file must be readable only by owner (0600).
	kp, _ := GenerateKeyPair()

	dir := t.TempDir()
	path := filepath.Join(dir, "test.key")

	SaveKeyPairEncrypted(path, kp, "passphrase")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("key file permissions = %o, want 0600", perm)
	}
}
