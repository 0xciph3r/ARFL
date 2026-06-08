package payments

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Radi-Labs/ARFL/internal/credentials"
)

// STRIDE tests for the blind signature endpoints.
// These test adversarial scenarios beyond happy-path functionality.

// --- Spoofing ---

func TestSTRIDE_Redeem_SpoofedPreimage(t *testing.T) {
	// An attacker who doesn't know the real preimage cannot redeem.
	env := setupBlindTestEnv(t)
	_, _ = env.createEntitlement(t, 10) // real entitlement exists

	// Attacker uses a random preimage that doesn't match any payment.
	fakePreimage := make([]byte, 32)
	fakePreimage[0] = 0xDE
	fakePreimage[1] = 0xAD

	secret, _ := credentials.GenerateTokenSecret()
	bm, _ := credentials.BlindTokenSecret(env.denomKey.PublicKey, secret)

	body, _ := json.Marshal(RedeemRequest{
		PaymentPreimage: hex.EncodeToString(fakePreimage),
		KeyID:           "key-100mb",
		BlindedMessages: []string{hex.EncodeToString(bm.Blinded)},
		Nonce:           "nonce-spoof",
	})

	resp := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader(body))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("spoofed preimage should be rejected: expected 404, got %d", resp.StatusCode)
	}
}

func TestSTRIDE_Spend_ForgedSignature(t *testing.T) {
	// An attacker tries to spend a token with a forged signature.
	env := setupBlindTestEnv(t)

	secret, _ := credentials.GenerateTokenSecret()
	fakeSig := make([]byte, 256) // RSA signature size but all zeros
	for i := range fakeSig {
		fakeSig[i] = byte(i % 256)
	}

	body, _ := json.Marshal(SpendRequest{
		KeyID:       "key-100mb",
		TokenSecret: hex.EncodeToString(secret),
		Signature:   hex.EncodeToString(fakeSig),
		NodePubkey:  "attacker-node",
	})

	resp := httpPost(t, env.server.URL+"/spend", "application/json", bytes.NewReader(body))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("forged signature should be rejected: expected 401, got %d", resp.StatusCode)
	}
}

// --- Tampering ---

func TestSTRIDE_Spend_TamperedTokenSecret(t *testing.T) {
	// Attacker gets a valid signature but tampers with the token secret.
	env := setupBlindTestEnv(t)
	_, preimage := env.createEntitlement(t, 10)

	// Redeem legitimately.
	secret, _ := credentials.GenerateTokenSecret()
	bm, _ := credentials.BlindTokenSecret(env.denomKey.PublicKey, secret)

	redeemBody, _ := json.Marshal(RedeemRequest{
		PaymentPreimage: preimage,
		KeyID:           "key-100mb",
		BlindedMessages: []string{hex.EncodeToString(bm.Blinded)},
		Nonce:           "nonce-tamper",
	})
	redeemResp := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader(redeemBody))
	var redeemResult RedeemResponse
	json.NewDecoder(redeemResp.Body).Decode(&redeemResult)
	redeemResp.Body.Close()

	blindSig, _ := hex.DecodeString(redeemResult.BlindSignatures[0])
	unblinded := credentials.UnblindSignature(env.denomKey.PublicKey, blindSig, bm.Unblinder)

	// Tamper: flip a bit in the token secret.
	tamperedSecret := make([]byte, len(secret))
	copy(tamperedSecret, secret)
	tamperedSecret[0] ^= 0xFF

	body, _ := json.Marshal(SpendRequest{
		KeyID:       "key-100mb",
		TokenSecret: hex.EncodeToString(tamperedSecret),
		Signature:   hex.EncodeToString(unblinded),
		NodePubkey:  "node-1",
	})

	resp := httpPost(t, env.server.URL+"/spend", "application/json", bytes.NewReader(body))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("tampered token should be rejected: expected 401, got %d", resp.StatusCode)
	}
}

func TestSTRIDE_Redeem_BodyTooLarge(t *testing.T) {
	// Attacker sends an oversized body to exhaust memory.
	env := setupBlindTestEnv(t)

	// 300KB of garbage (exceeds 256KB limit).
	largeBody := strings.Repeat("A", 300*1024)
	resp := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader([]byte(largeBody)))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized body should be rejected: expected 400, got %d", resp.StatusCode)
	}
}

// --- Repudiation ---

func TestSTRIDE_Redeem_CachedRedemptionPersists(t *testing.T) {
	// After a successful redemption, the nonce + request_hash are cached.
	// This ensures the Hub can prove the redemption happened.
	env := setupBlindTestEnv(t)
	_, preimage := env.createEntitlement(t, 5)

	secret, _ := credentials.GenerateTokenSecret()
	bm, _ := credentials.BlindTokenSecret(env.denomKey.PublicKey, secret)

	body, _ := json.Marshal(RedeemRequest{
		PaymentPreimage: preimage,
		KeyID:           "key-100mb",
		BlindedMessages: []string{hex.EncodeToString(bm.Blinded)},
		Nonce:           "nonce-audit-trail",
	})
	resp := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader(body))
	resp.Body.Close()

	// Verify redemption exists in DB.
	rec, err := env.store.GetRedemption("nonce-audit-trail")
	if err != nil {
		t.Fatalf("GetRedemption: %v", err)
	}
	if rec == nil {
		t.Fatal("redemption not cached — no audit trail")
	}
	if rec.TokensCount != 1 {
		t.Errorf("expected 1 token, got %d", rec.TokensCount)
	}
}

func TestSTRIDE_Spend_SpentTokenRecorded(t *testing.T) {
	// After spending, the token is recorded in spent_tokens for audit.
	env := setupBlindTestEnv(t)
	_, preimage := env.createEntitlement(t, 10)

	secret, _ := credentials.GenerateTokenSecret()
	bm, _ := credentials.BlindTokenSecret(env.denomKey.PublicKey, secret)

	redeemBody, _ := json.Marshal(RedeemRequest{
		PaymentPreimage: preimage,
		KeyID:           "key-100mb",
		BlindedMessages: []string{hex.EncodeToString(bm.Blinded)},
		Nonce:           "nonce-audit-spend",
	})
	redeemResp := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader(redeemBody))
	var redeemResult RedeemResponse
	json.NewDecoder(redeemResp.Body).Decode(&redeemResult)
	redeemResp.Body.Close()

	blindSig, _ := hex.DecodeString(redeemResult.BlindSignatures[0])
	unblinded := credentials.UnblindSignature(env.denomKey.PublicKey, blindSig, bm.Unblinder)

	spendBody, _ := json.Marshal(SpendRequest{
		KeyID:       "key-100mb",
		TokenSecret: hex.EncodeToString(secret),
		Signature:   hex.EncodeToString(unblinded),
		NodePubkey:  "node-audit",
	})
	resp := httpPost(t, env.server.URL+"/spend", "application/json", bytes.NewReader(spendBody))
	resp.Body.Close()

	// Verify spent token is in DB.
	token := &credentials.BlindToken{
		Version:     credentials.BlindTokenVersion,
		KeyID:       "key-100mb",
		TokenSecret: secret,
	}
	spent, err := env.store.IsSpent(token.TokenID())
	if err != nil {
		t.Fatalf("IsSpent: %v", err)
	}
	if !spent {
		t.Fatal("token not recorded as spent — no audit trail")
	}
}

// --- Denial of Service ---

func TestSTRIDE_Redeem_TooManyMessages(t *testing.T) {
	// Attacker tries to redeem more messages than MaxBlindMessagesPerRequest.
	env := setupBlindTestEnv(t)
	_, preimage := env.createEntitlement(t, 1000)

	msgs := make([]string, credentials.MaxBlindMessagesPerRequest+1)
	for i := range msgs {
		msgs[i] = hex.EncodeToString(make([]byte, 32))
	}

	body, _ := json.Marshal(RedeemRequest{
		PaymentPreimage: preimage,
		KeyID:           "key-100mb",
		BlindedMessages: msgs,
		Nonce:           "nonce-dos",
	})

	resp := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader(body))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("too many messages should be rejected: expected 400, got %d", resp.StatusCode)
	}
}

// --- Elevation of Privilege ---

func TestSTRIDE_Redeem_CrossKeyRedemption(t *testing.T) {
	// Attacker tries to redeem with a different key_id than the entitlement's.
	env := setupBlindTestEnv(t)
	_, preimage := env.createEntitlement(t, 10) // entitlement is for key-100mb

	secret, _ := credentials.GenerateTokenSecret()
	bm, _ := credentials.BlindTokenSecret(env.denomKey.PublicKey, secret)

	body, _ := json.Marshal(RedeemRequest{
		PaymentPreimage: preimage,
		KeyID:           "key-1gb", // wrong key
		BlindedMessages: []string{hex.EncodeToString(bm.Blinded)},
		Nonce:           "nonce-cross-key",
	})

	resp := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader(body))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("cross-key redemption should be rejected: expected 400, got %d", resp.StatusCode)
	}
}

func TestSTRIDE_Redeem_ConsumeMoreThanEntitled(t *testing.T) {
	// Attacker redeems all tokens, then tries to redeem more.
	env := setupBlindTestEnv(t)
	_, preimage := env.createEntitlement(t, 2) // only 2 tokens

	// First redemption: use all 2 tokens.
	msgs := make([]string, 2)
	for i := 0; i < 2; i++ {
		s, _ := credentials.GenerateTokenSecret()
		bm, _ := credentials.BlindTokenSecret(env.denomKey.PublicKey, s)
		msgs[i] = hex.EncodeToString(bm.Blinded)
	}

	body1, _ := json.Marshal(RedeemRequest{
		PaymentPreimage: preimage,
		KeyID:           "key-100mb",
		BlindedMessages: msgs,
		Nonce:           "nonce-exhaust",
	})
	resp1 := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader(body1))
	resp1.Body.Close()

	// Second redemption: try to get 1 more.
	s, _ := credentials.GenerateTokenSecret()
	bm, _ := credentials.BlindTokenSecret(env.denomKey.PublicKey, s)

	body2, _ := json.Marshal(RedeemRequest{
		PaymentPreimage: preimage,
		KeyID:           "key-100mb",
		BlindedMessages: []string{hex.EncodeToString(bm.Blinded)},
		Nonce:           "nonce-overreach",
	})
	resp2 := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader(body2))
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("over-entitlement should be rejected: expected 402, got %d", resp2.StatusCode)
	}
}

func TestSTRIDE_Spend_CrossKeyReplay(t *testing.T) {
	// Attacker redeems with key-100mb, tries to spend pretending it's a
	// different key. The signature won't verify against the wrong key.
	env := setupBlindTestEnv(t)
	_, preimage := env.createEntitlement(t, 10)

	secret, _ := credentials.GenerateTokenSecret()
	bm, _ := credentials.BlindTokenSecret(env.denomKey.PublicKey, secret)

	redeemBody, _ := json.Marshal(RedeemRequest{
		PaymentPreimage: preimage,
		KeyID:           "key-100mb",
		BlindedMessages: []string{hex.EncodeToString(bm.Blinded)},
		Nonce:           "nonce-cross-spend",
	})
	redeemResp := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader(redeemBody))
	var redeemResult RedeemResponse
	json.NewDecoder(redeemResp.Body).Decode(&redeemResult)
	redeemResp.Body.Close()

	blindSig, _ := hex.DecodeString(redeemResult.BlindSignatures[0])
	unblinded := credentials.UnblindSignature(env.denomKey.PublicKey, blindSig, bm.Unblinder)

	// Try to spend with a different key_id.
	body, _ := json.Marshal(SpendRequest{
		KeyID:       "key-1gb", // wrong key
		TokenSecret: hex.EncodeToString(secret),
		Signature:   hex.EncodeToString(unblinded),
		NodePubkey:  "node-1",
	})

	resp := httpPost(t, env.server.URL+"/spend", "application/json", bytes.NewReader(body))
	defer resp.Body.Close()

	// Should fail because key-1gb doesn't exist in the verifier.
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("cross-key spend should fail: got %d", resp.StatusCode)
	}
}

// --- Concurrent STRIDE ---

func TestSTRIDE_ConcurrentRedeem_NoOverdraw(t *testing.T) {
	// Multiple concurrent redemptions must not overdraw the entitlement.
	env := setupBlindTestEnv(t)
	_, preimage := env.createEntitlement(t, 5) // 5 tokens total

	var wg sync.WaitGroup
	var successCount int32
	var failCount int32

	// Launch 10 concurrent requests, each asking for 1 token.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			s, _ := credentials.GenerateTokenSecret()
			bm, _ := credentials.BlindTokenSecret(env.denomKey.PublicKey, s)

			body, _ := json.Marshal(RedeemRequest{
				PaymentPreimage: preimage,
				KeyID:           "key-100mb",
				BlindedMessages: []string{hex.EncodeToString(bm.Blinded)},
				Nonce:           fmt.Sprintf("nonce-concurrent-%d", idx),
			})

			resp, err := http.Post(env.server.URL+"/redeem", "application/json", bytes.NewReader(body))
			if err != nil {
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				atomic.AddInt32(&successCount, 1)
			} else {
				atomic.AddInt32(&failCount, 1)
			}
		}(i)
	}

	wg.Wait()

	if successCount > 5 {
		t.Fatalf("overdraw: %d successful redemptions from 5-token entitlement", successCount)
	}
	if successCount < 1 {
		t.Fatal("no successful redemptions — test setup error")
	}
}

func TestSTRIDE_ConcurrentSpend_ExactlyOneFirstSpend(t *testing.T) {
	// Multiple concurrent spends of the same token: exactly one is first_spend=true.
	env := setupBlindTestEnv(t)
	_, preimage := env.createEntitlement(t, 10)

	// Redeem a token.
	secret, _ := credentials.GenerateTokenSecret()
	bm, _ := credentials.BlindTokenSecret(env.denomKey.PublicKey, secret)

	redeemBody, _ := json.Marshal(RedeemRequest{
		PaymentPreimage: preimage,
		KeyID:           "key-100mb",
		BlindedMessages: []string{hex.EncodeToString(bm.Blinded)},
		Nonce:           "nonce-race-spend",
	})
	redeemResp := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader(redeemBody))
	var redeemResult RedeemResponse
	json.NewDecoder(redeemResp.Body).Decode(&redeemResult)
	redeemResp.Body.Close()

	blindSig, _ := hex.DecodeString(redeemResult.BlindSignatures[0])
	unblinded := credentials.UnblindSignature(env.denomKey.PublicKey, blindSig, bm.Unblinder)

	spendBody, _ := json.Marshal(SpendRequest{
		KeyID:       "key-100mb",
		TokenSecret: hex.EncodeToString(secret),
		Signature:   hex.EncodeToString(unblinded),
		NodePubkey:  "node-race",
	})

	var wg sync.WaitGroup
	var firstSpendCount int32

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			resp, err := http.Post(env.server.URL+"/spend", "application/json", bytes.NewReader(spendBody))
			if err != nil {
				return
			}
			defer resp.Body.Close()

			var result SpendResponse
			json.NewDecoder(resp.Body).Decode(&result)
			if result.FirstSpend {
				atomic.AddInt32(&firstSpendCount, 1)
			}
		}()
	}

	wg.Wait()

	if firstSpendCount != 1 {
		t.Fatalf("expected exactly 1 first_spend, got %d", firstSpendCount)
	}
}

// --- Information Disclosure ---

func TestSTRIDE_Redeem_PreimageNotLeakedInErrors(t *testing.T) {
	// Error responses must not leak the payment hash derived from the preimage.
	env := setupBlindTestEnv(t)

	preimage := make([]byte, 32)
	preimage[0] = 0xBE
	preimage[1] = 0xEF
	hash := sha256.Sum256(preimage)
	paymentHash := hex.EncodeToString(hash[:])

	secret, _ := credentials.GenerateTokenSecret()
	bm, _ := credentials.BlindTokenSecret(env.denomKey.PublicKey, secret)

	body, _ := json.Marshal(RedeemRequest{
		PaymentPreimage: hex.EncodeToString(preimage),
		KeyID:           "key-100mb",
		BlindedMessages: []string{hex.EncodeToString(bm.Blinded)},
		Nonce:           "nonce-leak-test",
	})

	resp := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader(body))
	defer resp.Body.Close()

	var errResp map[string]string
	json.NewDecoder(resp.Body).Decode(&errResp)

	// The error message should NOT contain the payment hash.
	if msg, ok := errResp["error"]; ok {
		if strings.Contains(msg, paymentHash) {
			t.Errorf("error response leaks payment_hash: %s", msg)
		}
		if strings.Contains(msg, hex.EncodeToString(preimage)) {
			t.Errorf("error response leaks preimage: %s", msg)
		}
	}
}

// --- Replay Resistance ---

func TestSTRIDE_Redeem_OldPreimageNewEntitlement(t *testing.T) {
	// After exhausting entitlement, buying a new one with a different preimage
	// should not allow reuse of old nonces.
	env := setupBlindTestEnv(t)

	// First entitlement: 1 token.
	preimage1 := make([]byte, 32)
	for i := range preimage1 {
		preimage1[i] = byte(i + 10)
	}
	hash1 := sha256.Sum256(preimage1)
	paymentHash1 := hex.EncodeToString(hash1[:])

	env.store.InsertInvoice(paymentHash1, "lnbc...", 500, "1gb", 1_000_000_000,
		time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC), "127.0.0.1")
	env.store.SettleInvoice(paymentHash1)
	env.store.InsertEntitlement("ent-1", paymentHash1, 1, 100_000_000, "key-100mb")

	// Redeem from first entitlement.
	s1, _ := credentials.GenerateTokenSecret()
	bm1, _ := credentials.BlindTokenSecret(env.denomKey.PublicKey, s1)
	body1, _ := json.Marshal(RedeemRequest{
		PaymentPreimage: hex.EncodeToString(preimage1),
		KeyID:           "key-100mb",
		BlindedMessages: []string{hex.EncodeToString(bm1.Blinded)},
		Nonce:           "shared-nonce",
	})
	resp1 := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader(body1))
	resp1.Body.Close()

	// Second entitlement: 1 token, different preimage.
	preimage2 := make([]byte, 32)
	for i := range preimage2 {
		preimage2[i] = byte(i + 20)
	}
	hash2 := sha256.Sum256(preimage2)
	paymentHash2 := hex.EncodeToString(hash2[:])

	env.store.InsertInvoice(paymentHash2, "lnbc...", 500, "1gb", 1_000_000_000,
		time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC), "127.0.0.2")
	env.store.SettleInvoice(paymentHash2)
	env.store.InsertEntitlement("ent-2", paymentHash2, 1, 100_000_000, "key-100mb")

	// Try to use the same nonce with the second entitlement.
	s2, _ := credentials.GenerateTokenSecret()
	bm2, _ := credentials.BlindTokenSecret(env.denomKey.PublicKey, s2)
	body2, _ := json.Marshal(RedeemRequest{
		PaymentPreimage: hex.EncodeToString(preimage2),
		KeyID:           "key-100mb",
		BlindedMessages: []string{hex.EncodeToString(bm2.Blinded)},
		Nonce:           "shared-nonce", // same nonce, different payment
	})
	resp2 := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader(body2))
	defer resp2.Body.Close()

	// Should conflict because nonce is already used (even for different payment).
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("reused nonce should conflict: expected 409, got %d", resp2.StatusCode)
	}
}
