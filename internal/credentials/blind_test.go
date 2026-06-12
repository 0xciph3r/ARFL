package credentials

import (
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"
)

// --- Key generation ---

func TestGenerateDenominationKey(t *testing.T) {
	key, err := GenerateDenominationKey("key-100mb", 100_000_000)
	if err != nil {
		t.Fatalf("GenerateDenominationKey: %v", err)
	}
	if key.KeyID != "key-100mb" {
		t.Errorf("KeyID = %q, want %q", key.KeyID, "key-100mb")
	}
	if key.BytesPerToken != 100_000_000 {
		t.Errorf("BytesPerToken = %d, want %d", key.BytesPerToken, 100_000_000)
	}
	if key.PrivateKey == nil {
		t.Fatal("PrivateKey should not be nil")
	}
	if key.PublicKey == nil {
		t.Fatal("PublicKey should not be nil")
	}
	if key.PrivateKey.N.BitLen() < RSAKeySize {
		t.Errorf("key size %d bits, want >= %d", key.PrivateKey.N.BitLen(), RSAKeySize)
	}
}

func TestGenerateDenominationKey_EmptyID(t *testing.T) {
	_, err := GenerateDenominationKey("", 100_000_000)
	if err == nil {
		t.Fatal("expected error for empty key_id")
	}
}

func TestGenerateDenominationKey_ZeroBytes(t *testing.T) {
	_, err := GenerateDenominationKey("key-1", 0)
	if err == nil {
		t.Fatal("expected error for zero bytes_per_token")
	}
}

// --- Full blind signature flow ---

func TestBlindSignature_FullFlow(t *testing.T) {
	// 1. Generate denomination key (Hub).
	denomKey, err := GenerateDenominationKey("key-100mb", 100_000_000)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	// 2. Create mint (Hub-side).
	mint := NewRSABlindMint([]*DenominationKey{denomKey})

	// 3. Create verifier (Node-side, public key only).
	verifier := NewRSABlindVerifier([]*DenominationKey{{
		KeyID:         denomKey.KeyID,
		BytesPerToken: denomKey.BytesPerToken,
		PublicKey:     denomKey.PublicKey,
	}})

	// 4. Client generates token secret.
	tokenSecret, err := GenerateTokenSecret()
	if err != nil {
		t.Fatalf("generate secret: %v", err)
	}

	// 5. Client blinds the token secret.
	bm, err := BlindTokenSecret(denomKey.PublicKey, tokenSecret)
	if err != nil {
		t.Fatalf("blind: %v", err)
	}

	// 6. Hub signs the blinded message (blind — doesn't see tokenSecret).
	blindSigs, err := mint.SignBlinded("key-100mb", [][]byte{bm.Blinded})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if len(blindSigs) != 1 {
		t.Fatalf("got %d sigs, want 1", len(blindSigs))
	}

	// 7. Client unblinds the signature.
	sig := UnblindSignature(denomKey.PublicKey, blindSigs[0], bm.Unblinder)

	// 8. Create the blind token.
	token := &BlindToken{
		Version:     BlindTokenVersion,
		KeyID:       "key-100mb",
		TokenSecret: tokenSecret,
		Signature:   sig,
	}

	// 9. Node verifies the token.
	if err := verifier.Verify(token); err != nil {
		t.Fatalf("verify failed: %v", err)
	}

	// 10. Verify denomination lookup.
	denom, err := verifier.Denomination("key-100mb")
	if err != nil {
		t.Fatalf("denomination: %v", err)
	}
	if denom != 100_000_000 {
		t.Errorf("denomination = %d, want 100_000_000", denom)
	}
}

// --- Batch signing ---

func TestBlindSignature_BatchFlow(t *testing.T) {
	denomKey, _ := GenerateDenominationKey("key-batch", 100_000_000)
	mint := NewRSABlindMint([]*DenominationKey{denomKey})
	verifier := NewRSABlindVerifier([]*DenominationKey{{
		KeyID: denomKey.KeyID, BytesPerToken: denomKey.BytesPerToken,
		PublicKey: denomKey.PublicKey,
	}})

	count := 10
	secrets := make([][]byte, count)
	blindMsgs := make([][]byte, count)
	unblinderMap := make(map[int]*BlindedMessage, count)

	for i := 0; i < count; i++ {
		secret, _ := GenerateTokenSecret()
		secrets[i] = secret
		bm, err := BlindTokenSecret(denomKey.PublicKey, secret)
		if err != nil {
			t.Fatalf("blind %d: %v", i, err)
		}
		blindMsgs[i] = bm.Blinded
		unblinderMap[i] = bm
	}

	blindSigs, err := mint.SignBlinded("key-batch", blindMsgs)
	if err != nil {
		t.Fatalf("batch sign: %v", err)
	}
	if len(blindSigs) != count {
		t.Fatalf("got %d sigs, want %d", len(blindSigs), count)
	}

	for i := 0; i < count; i++ {
		sig := UnblindSignature(denomKey.PublicKey, blindSigs[i], unblinderMap[i].Unblinder)
		token := &BlindToken{
			Version: BlindTokenVersion, KeyID: "key-batch",
			TokenSecret: secrets[i], Signature: sig,
		}
		if err := verifier.Verify(token); err != nil {
			t.Errorf("token %d verify failed: %v", i, err)
		}
	}
}

// --- Token ID derivation ---

func TestTokenID_Deterministic(t *testing.T) {
	token := &BlindToken{
		Version:     BlindTokenVersion,
		KeyID:       "key-1",
		TokenSecret: []byte("test-secret"),
	}

	id1 := token.TokenID()
	id2 := token.TokenID()
	if id1 != id2 {
		t.Fatalf("TokenID not deterministic: %s != %s", id1, id2)
	}
	if len(id1) != 64 { // SHA256 hex
		t.Errorf("TokenID length = %d, want 64", len(id1))
	}
}

func TestTokenID_DomainSeparation(t *testing.T) {
	// Same secret, different key_id → different token ID.
	secret := []byte("same-secret")
	t1 := &BlindToken{Version: 1, KeyID: "key-a", TokenSecret: secret}
	t2 := &BlindToken{Version: 1, KeyID: "key-b", TokenSecret: secret}

	if t1.TokenID() == t2.TokenID() {
		t.Fatal("different key_id must produce different token IDs")
	}
}

func TestTokenID_MatchesManualComputation(t *testing.T) {
	token := &BlindToken{
		Version:     1,
		KeyID:       "key-1",
		TokenSecret: []byte{0xab, 0xcd},
	}
	expected := sha256.Sum256([]byte("ARFL|v1|key-1|abcd"))
	if token.TokenID() != hex.EncodeToString(expected[:]) {
		t.Fatal("TokenID doesn't match manual computation")
	}
}

// --- Verification failures ---

func TestBlindVerifier_WrongSignature(t *testing.T) {
	denomKey, _ := GenerateDenominationKey("key-1", 100_000_000)
	verifier := NewRSABlindVerifier([]*DenominationKey{{
		KeyID: denomKey.KeyID, BytesPerToken: denomKey.BytesPerToken,
		PublicKey: denomKey.PublicKey,
	}})

	token := &BlindToken{
		Version:     BlindTokenVersion,
		KeyID:       "key-1",
		TokenSecret: []byte("real-secret"),
		Signature:   []byte("garbage-signature-not-valid"),
	}
	if err := verifier.Verify(token); err == nil {
		t.Fatal("expected verification failure for wrong signature")
	}
}

func TestBlindVerifier_UnknownKeyID(t *testing.T) {
	verifier := NewRSABlindVerifier(nil)
	token := &BlindToken{
		Version:     BlindTokenVersion,
		KeyID:       "nonexistent",
		TokenSecret: []byte("secret"),
		Signature:   []byte("sig"),
	}
	if err := verifier.Verify(token); err == nil {
		t.Fatal("expected error for unknown key_id")
	}
}

func TestBlindVerifier_EmptyToken(t *testing.T) {
	verifier := NewRSABlindVerifier(nil)
	if err := verifier.Verify(nil); err == nil {
		t.Fatal("expected error for nil token")
	}
}

func TestBlindVerifier_WrongVersion(t *testing.T) {
	denomKey, _ := GenerateDenominationKey("key-1", 100_000_000)
	verifier := NewRSABlindVerifier([]*DenominationKey{{
		KeyID: denomKey.KeyID, BytesPerToken: denomKey.BytesPerToken,
		PublicKey: denomKey.PublicKey,
	}})

	token := &BlindToken{
		Version:     99,
		KeyID:       "key-1",
		TokenSecret: []byte("secret"),
		Signature:   []byte("sig"),
	}
	if err := verifier.Verify(token); err == nil {
		t.Fatal("expected error for wrong version")
	}
}

func TestBlindVerifier_WrongKey(t *testing.T) {
	// Sign with one key, verify with another.
	key1, _ := GenerateDenominationKey("key-1", 100_000_000)
	key2, _ := GenerateDenominationKey("key-2", 100_000_000)

	mint := NewRSABlindMint([]*DenominationKey{key1})
	verifier := NewRSABlindVerifier([]*DenominationKey{{
		KeyID: "key-1", BytesPerToken: key2.BytesPerToken,
		PublicKey: key2.PublicKey, // wrong key!
	}})

	secret, _ := GenerateTokenSecret()
	bm, _ := BlindTokenSecret(key1.PublicKey, secret)
	sigs, _ := mint.SignBlinded("key-1", [][]byte{bm.Blinded})
	sig := UnblindSignature(key1.PublicKey, sigs[0], bm.Unblinder)

	token := &BlindToken{
		Version: BlindTokenVersion, KeyID: "key-1",
		TokenSecret: secret, Signature: sig,
	}
	if err := verifier.Verify(token); err == nil {
		t.Fatal("expected verification failure when verifier has wrong public key")
	}
}

// --- Mint edge cases ---

func TestMint_EmptyBatch(t *testing.T) {
	key, _ := GenerateDenominationKey("k", 100_000_000)
	mint := NewRSABlindMint([]*DenominationKey{key})

	_, err := mint.SignBlinded("k", nil)
	if err == nil {
		t.Fatal("expected error for empty batch")
	}
}

func TestMint_TooManyMessages(t *testing.T) {
	key, _ := GenerateDenominationKey("k", 100_000_000)
	mint := NewRSABlindMint([]*DenominationKey{key})

	msgs := make([][]byte, MaxBlindMessagesPerRequest+1)
	for i := range msgs {
		msgs[i] = []byte("msg")
	}
	_, err := mint.SignBlinded("k", msgs)
	if err == nil {
		t.Fatal("expected error for too many messages")
	}
}

func TestMint_UnknownKeyID(t *testing.T) {
	mint := NewRSABlindMint(nil)
	_, err := mint.SignBlinded("nonexistent", [][]byte{[]byte("msg")})
	if err == nil {
		t.Fatal("expected error for unknown key_id")
	}
}

// --- Public key serialization ---

func TestPublicKey_RoundTrip(t *testing.T) {
	key, _ := GenerateDenominationKey("k", 100_000_000)
	mint := NewRSABlindMint([]*DenominationKey{key})

	der, err := mint.PublicKeyBytes("k")
	if err != nil {
		t.Fatalf("PublicKeyBytes: %v", err)
	}

	pub, err := PublicKeyFromDER(der)
	if err != nil {
		t.Fatalf("PublicKeyFromDER: %v", err)
	}

	if pub.N.Cmp(key.PublicKey.N) != 0 || pub.E != key.PublicKey.E {
		t.Fatal("round-tripped key doesn't match original")
	}
}

// --- STRIDE: Concurrent signing ---

func TestSTRIDE_ConcurrentSigning(t *testing.T) {
	key, _ := GenerateDenominationKey("k", 100_000_000)
	mint := NewRSABlindMint([]*DenominationKey{key})
	verifier := NewRSABlindVerifier([]*DenominationKey{{
		KeyID: key.KeyID, BytesPerToken: key.BytesPerToken,
		PublicKey: key.PublicKey,
	}})

	var wg sync.WaitGroup
	errs := make([]error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			secret, _ := GenerateTokenSecret()
			bm, err := BlindTokenSecret(key.PublicKey, secret)
			if err != nil {
				errs[idx] = err
				return
			}
			sigs, err := mint.SignBlinded("k", [][]byte{bm.Blinded})
			if err != nil {
				errs[idx] = err
				return
			}
			sig := UnblindSignature(key.PublicKey, sigs[0], bm.Unblinder)
			token := &BlindToken{
				Version: BlindTokenVersion, KeyID: "k",
				TokenSecret: secret, Signature: sig,
			}
			errs[idx] = verifier.Verify(token)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}
}

// --- STRIDE: Token forgery ---

func TestSTRIDE_CannotForgeToken(t *testing.T) {
	key, _ := GenerateDenominationKey("k", 100_000_000)
	verifier := NewRSABlindVerifier([]*DenominationKey{{
		KeyID: key.KeyID, BytesPerToken: key.BytesPerToken,
		PublicKey: key.PublicKey,
	}})

	// Try to forge a token without the private key.
	secret, _ := GenerateTokenSecret()
	fakeToken := &BlindToken{
		Version:     BlindTokenVersion,
		KeyID:       "k",
		TokenSecret: secret,
		Signature:   make([]byte, 256), // zeroed "signature"
	}
	if err := verifier.Verify(fakeToken); err == nil {
		t.Fatal("forged token should not verify")
	}
}

// --- STRIDE: Cross-key replay ---

func TestSTRIDE_CrossKeyReplay(t *testing.T) {
	// Token signed by key-1 should NOT verify under key-2.
	key1, _ := GenerateDenominationKey("key-1", 100_000_000)
	key2, _ := GenerateDenominationKey("key-2", 200_000_000)

	mint := NewRSABlindMint([]*DenominationKey{key1})
	verifier := NewRSABlindVerifier([]*DenominationKey{
		{KeyID: "key-1", BytesPerToken: key1.BytesPerToken, PublicKey: key1.PublicKey},
		{KeyID: "key-2", BytesPerToken: key2.BytesPerToken, PublicKey: key2.PublicKey},
	})

	secret, _ := GenerateTokenSecret()
	bm, _ := BlindTokenSecret(key1.PublicKey, secret)
	sigs, _ := mint.SignBlinded("key-1", [][]byte{bm.Blinded})
	sig := UnblindSignature(key1.PublicKey, sigs[0], bm.Unblinder)

	// Present token as key-2 (different denomination).
	token := &BlindToken{
		Version: BlindTokenVersion, KeyID: "key-2",
		TokenSecret: secret, Signature: sig,
	}
	if err := verifier.Verify(token); err == nil {
		t.Fatal("cross-key replay should fail verification")
	}
}

// --- STRIDE: Denomination immutability ---

func TestSTRIDE_DenominationConsistency(t *testing.T) {
	key, _ := GenerateDenominationKey("k", 100_000_000)
	mint := NewRSABlindMint([]*DenominationKey{key})

	// Verify denomination is consistent.
	d1, _ := mint.Denomination("k")
	d2, _ := mint.Denomination("k")
	if d1 != d2 || d1 != 100_000_000 {
		t.Fatalf("denomination not consistent: %d, %d", d1, d2)
	}
}

// --- Multiple denominations ---

func TestMultipleDenominations(t *testing.T) {
	key100, _ := GenerateDenominationKey("100mb", 100_000_000)
	key256, _ := GenerateDenominationKey("256mb", 256_000_000)

	mint := NewRSABlindMint([]*DenominationKey{key100, key256})
	verifier := NewRSABlindVerifier([]*DenominationKey{
		{KeyID: "100mb", BytesPerToken: 100_000_000, PublicKey: key100.PublicKey},
		{KeyID: "256mb", BytesPerToken: 256_000_000, PublicKey: key256.PublicKey},
	})

	for _, tc := range []struct {
		keyID string
		key   *DenominationKey
	}{
		{"100mb", key100},
		{"256mb", key256},
	} {
		secret, _ := GenerateTokenSecret()
		bm, _ := BlindTokenSecret(tc.key.PublicKey, secret)
		sigs, err := mint.SignBlinded(tc.keyID, [][]byte{bm.Blinded})
		if err != nil {
			t.Fatalf("%s: sign: %v", tc.keyID, err)
		}
		sig := UnblindSignature(tc.key.PublicKey, sigs[0], bm.Unblinder)
		token := &BlindToken{
			Version: BlindTokenVersion, KeyID: tc.keyID,
			TokenSecret: secret, Signature: sig,
		}
		if err := verifier.Verify(token); err != nil {
			t.Errorf("%s: verify failed: %v", tc.keyID, err)
		}
		denom, _ := verifier.Denomination(tc.keyID)
		if (tc.keyID == "100mb" && denom != 100_000_000) ||
			(tc.keyID == "256mb" && denom != 256_000_000) {
			t.Errorf("%s: denomination = %d", tc.keyID, denom)
		}
	}
}

// --- Verifier key rotation ---

func TestVerifier_AddKey(t *testing.T) {
	key1, _ := GenerateDenominationKey("key-1", 100_000_000)
	verifier := NewRSABlindVerifier([]*DenominationKey{{
		KeyID: "key-1", BytesPerToken: key1.BytesPerToken,
		PublicKey: key1.PublicKey,
	}})

	// Initially key-2 is unknown.
	key2, _ := GenerateDenominationKey("key-2", 200_000_000)
	if _, err := verifier.Denomination("key-2"); err == nil {
		t.Fatal("key-2 should not exist yet")
	}

	// Add key-2.
	verifier.AddKey(&DenominationKey{
		KeyID: "key-2", BytesPerToken: 200_000_000,
		PublicKey: key2.PublicKey,
	})

	denom, err := verifier.Denomination("key-2")
	if err != nil {
		t.Fatalf("denomination after AddKey: %v", err)
	}
	if denom != 200_000_000 {
		t.Errorf("denomination = %d, want 200_000_000", denom)
	}
}

// Helper to suppress "unused" errors for the rsa import.
var _ *rsa.PublicKey

// Helper to suppress "unused" errors for the fmt import.
var _ = fmt.Sprintf
