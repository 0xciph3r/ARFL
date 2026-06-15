package credentials

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDenominationKey_SaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key-100mb.json")

	// Generate a fresh key.
	original, err := GenerateDenominationKey("key-100mb", 100_000_000)
	if err != nil {
		t.Fatalf("GenerateDenominationKey: %v", err)
	}

	// Save to disk.
	if err := SaveDenominationKey(path, original); err != nil {
		t.Fatalf("SaveDenominationKey: %v", err)
	}

	// Check file permissions (owner read/write only).
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Errorf("expected 0600 permissions, got %o", info.Mode().Perm())
	}

	// Load back.
	loaded, err := LoadDenominationKey(path)
	if err != nil {
		t.Fatalf("LoadDenominationKey: %v", err)
	}

	// Verify metadata.
	if loaded.KeyID != original.KeyID {
		t.Errorf("KeyID: got %s, want %s", loaded.KeyID, original.KeyID)
	}
	if loaded.BytesPerToken != original.BytesPerToken {
		t.Errorf("BytesPerToken: got %d, want %d", loaded.BytesPerToken, original.BytesPerToken)
	}

	// Verify the loaded key can sign and the original key can verify.
	mint := NewRSABlindMint([]*DenominationKey{loaded})
	verifier := NewRSABlindVerifier([]*DenominationKey{ExportPublicKey(original)})

	secret, _ := GenerateTokenSecret()
	bm, err := BlindTokenSecret(loaded.PublicKey, secret)
	if err != nil {
		t.Fatalf("BlindTokenSecret: %v", err)
	}

	blindSigs, err := mint.SignBlinded("key-100mb", [][]byte{bm.Blinded})
	if err != nil {
		t.Fatalf("SignBlinded: %v", err)
	}

	unblinded := UnblindSignature(loaded.PublicKey, blindSigs[0], bm.Unblinder)

	token := &BlindToken{
		Version:     BlindTokenVersion,
		KeyID:       "key-100mb",
		TokenSecret: secret,
		Signature:   unblinded,
	}

	if err := verifier.Verify(token); err != nil {
		t.Fatalf("verification failed after round-trip: %v", err)
	}
}

func TestPublicKey_SaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	privPath := filepath.Join(dir, "key-100mb.json")
	pubPath := filepath.Join(dir, "key-100mb.pub.json")

	// Generate and save private key.
	key, err := GenerateDenominationKey("key-100mb", 100_000_000)
	if err != nil {
		t.Fatalf("GenerateDenominationKey: %v", err)
	}
	SaveDenominationKey(privPath, key)

	// Export and save public key.
	if err := SavePublicKey(pubPath, key); err != nil {
		t.Fatalf("SavePublicKey: %v", err)
	}

	// Public key file should be world-readable.
	info, _ := os.Stat(pubPath)
	if info.Mode().Perm() != 0644 {
		t.Errorf("expected 0644 permissions, got %o", info.Mode().Perm())
	}

	// Load public key.
	pub, err := LoadPublicKey(pubPath)
	if err != nil {
		t.Fatalf("LoadPublicKey: %v", err)
	}

	if pub.KeyID != "key-100mb" {
		t.Errorf("KeyID: got %s, want key-100mb", pub.KeyID)
	}
	if pub.BytesPerToken != 100_000_000 {
		t.Errorf("BytesPerToken: got %d, want 100000000", pub.BytesPerToken)
	}
	if pub.PrivateKey != nil {
		t.Error("public key file should not contain private key")
	}

	// Verify: sign with private key, verify with loaded public key.
	mint := NewRSABlindMint([]*DenominationKey{key})
	verifier := NewRSABlindVerifier([]*DenominationKey{pub})

	secret, _ := GenerateTokenSecret()
	bm, _ := BlindTokenSecret(key.PublicKey, secret)
	blindSigs, _ := mint.SignBlinded("key-100mb", [][]byte{bm.Blinded})
	unblinded := UnblindSignature(key.PublicKey, blindSigs[0], bm.Unblinder)

	token := &BlindToken{
		Version:     BlindTokenVersion,
		KeyID:       "key-100mb",
		TokenSecret: secret,
		Signature:   unblinded,
	}

	if err := verifier.Verify(token); err != nil {
		t.Fatalf("public key verification failed: %v", err)
	}
}

func TestLoadDenominationKey_NotFound(t *testing.T) {
	_, err := LoadDenominationKey("/nonexistent/path.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestSaveDenominationKey_CreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "keys", "blind", "key.json")

	key, _ := GenerateDenominationKey("test", 1000)
	if err := SaveDenominationKey(nested, key); err != nil {
		t.Fatalf("SaveDenominationKey with nested dir: %v", err)
	}

	// Verify file exists.
	if _, err := os.Stat(nested); os.IsNotExist(err) {
		t.Fatal("file not created")
	}
}
