package payments

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Radi-Labs/ARFL/internal/credentials"
)

// blindTestEnv extends testEnv with blind signature support.
type blindTestEnv struct {
	*testEnv
	denomKey *credentials.DenominationKey
	mint     *credentials.RSABlindMint
	verifier *credentials.RSABlindVerifier
}

func setupBlindTestEnv(t *testing.T) *blindTestEnv {
	t.Helper()

	env := setupTestEnv(t)

	// Generate a denomination key for testing.
	denomKey, err := credentials.GenerateDenominationKey("key-100mb", 100_000_000)
	if err != nil {
		t.Fatalf("GenerateDenominationKey: %v", err)
	}

	mint := credentials.NewRSABlindMint([]*credentials.DenominationKey{denomKey})
	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		{KeyID: denomKey.KeyID, BytesPerToken: denomKey.BytesPerToken, PublicKey: denomKey.PublicKey},
	})

	env.api.EnableBlindSignatures(mint, verifier, "key-100mb")

	// Re-create server to pick up new routes.
	env.server.Close()
	env.server = httptest.NewServer(env.api.Handler())
	t.Cleanup(func() { env.server.Close() })

	return &blindTestEnv{
		testEnv:  env,
		denomKey: denomKey,
		mint:     mint,
		verifier: verifier,
	}
}

// createEntitlement inserts a settled invoice + entitlement for testing.
// Returns the payment_hash and preimage.
func (e *blindTestEnv) createEntitlement(t *testing.T, tokens int) (paymentHash, preimageHex string) {
	t.Helper()

	// Generate a fake preimage.
	preimage := make([]byte, 32)
	for i := range preimage {
		preimage[i] = byte(i + 1)
	}
	preimageHex = hex.EncodeToString(preimage)

	hashBytes := sha256.Sum256(preimage)
	paymentHash = hex.EncodeToString(hashBytes[:])

	// Insert a settled invoice.
	err := e.store.InsertInvoice(paymentHash, "lnbc...", 500, "1gb", 1_000_000_000,
		time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC), "127.0.0.1")
	if err != nil {
		t.Fatalf("InsertInvoice: %v", err)
	}
	err = e.store.SettleInvoice(paymentHash)
	if err != nil {
		t.Fatalf("SettleInvoice: %v", err)
	}

	// Create entitlement.
	err = e.store.InsertEntitlement("ent-"+paymentHash[:8], paymentHash, tokens, 100_000_000, "key-100mb")
	if err != nil {
		t.Fatalf("InsertEntitlement: %v", err)
	}

	return paymentHash, preimageHex
}

// --- /redeem tests ---

func TestRedeem_FullFlow(t *testing.T) {
	env := setupBlindTestEnv(t)
	_, preimage := env.createEntitlement(t, 10)

	// Generate blinded messages.
	secret, err := credentials.GenerateTokenSecret()
	if err != nil {
		t.Fatalf("GenerateTokenSecret: %v", err)
	}
	bm, err := credentials.BlindTokenSecret(env.denomKey.PublicKey, secret)
	if err != nil {
		t.Fatalf("BlindTokenSecret: %v", err)
	}

	body, _ := json.Marshal(RedeemRequest{
		PaymentPreimage: preimage,
		KeyID:           "key-100mb",
		BlindedMessages: []string{hex.EncodeToString(bm.Blinded)},
		Nonce:           "nonce-1",
	})

	resp := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader(body))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errResp map[string]string
		json.NewDecoder(resp.Body).Decode(&errResp)
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, errResp)
	}

	var result RedeemResponse
	json.NewDecoder(resp.Body).Decode(&result)

	if len(result.BlindSignatures) != 1 {
		t.Fatalf("expected 1 signature, got %d", len(result.BlindSignatures))
	}
	if result.BytesPerToken != 100_000_000 {
		t.Errorf("expected 100MB denomination, got %d", result.BytesPerToken)
	}
	if result.TokensRedeemed != 1 {
		t.Errorf("expected 1 redeemed, got %d", result.TokensRedeemed)
	}
	if result.TokensRemaining != 9 {
		t.Errorf("expected 9 remaining, got %d", result.TokensRemaining)
	}

	// Unblind and verify the signature.
	blindSig, _ := hex.DecodeString(result.BlindSignatures[0])
	unblinded := credentials.UnblindSignature(env.denomKey.PublicKey, blindSig, bm.Unblinder)
	token := &credentials.BlindToken{
		Version:     credentials.BlindTokenVersion,
		KeyID:       "key-100mb",
		TokenSecret: secret,
		Signature:   unblinded,
	}
	if err := env.verifier.Verify(token); err != nil {
		t.Errorf("unblinded signature failed verification: %v", err)
	}
}

func TestRedeem_BatchMessages(t *testing.T) {
	env := setupBlindTestEnv(t)
	_, preimage := env.createEntitlement(t, 5)

	// Generate 3 blinded messages.
	msgs := make([]string, 3)
	for i := 0; i < 3; i++ {
		secret, _ := credentials.GenerateTokenSecret()
		bm, _ := credentials.BlindTokenSecret(env.denomKey.PublicKey, secret)
		msgs[i] = hex.EncodeToString(bm.Blinded)
	}

	body, _ := json.Marshal(RedeemRequest{
		PaymentPreimage: preimage,
		KeyID:           "key-100mb",
		BlindedMessages: msgs,
		Nonce:           "nonce-batch",
	})

	resp := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader(body))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result RedeemResponse
	json.NewDecoder(resp.Body).Decode(&result)

	if len(result.BlindSignatures) != 3 {
		t.Fatalf("expected 3 signatures, got %d", len(result.BlindSignatures))
	}
	if result.TokensRedeemed != 3 {
		t.Errorf("expected 3 redeemed, got %d", result.TokensRedeemed)
	}
	if result.TokensRemaining != 2 {
		t.Errorf("expected 2 remaining, got %d", result.TokensRemaining)
	}
}

func TestRedeem_IdempotentReplay(t *testing.T) {
	env := setupBlindTestEnv(t)
	_, preimage := env.createEntitlement(t, 10)

	secret, _ := credentials.GenerateTokenSecret()
	bm, _ := credentials.BlindTokenSecret(env.denomKey.PublicKey, secret)

	body, _ := json.Marshal(RedeemRequest{
		PaymentPreimage: preimage,
		KeyID:           "key-100mb",
		BlindedMessages: []string{hex.EncodeToString(bm.Blinded)},
		Nonce:           "nonce-idempotent",
	})

	// First request.
	resp1 := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader(body))
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request: expected 200, got %d", resp1.StatusCode)
	}
	var result1 RedeemResponse
	json.NewDecoder(resp1.Body).Decode(&result1)

	// Second request with same nonce + same request.
	resp2 := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader(body))
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("replay: expected 200, got %d", resp2.StatusCode)
	}
	var result2 RedeemResponse
	json.NewDecoder(resp2.Body).Decode(&result2)

	// Same signatures returned.
	if len(result2.BlindSignatures) != len(result1.BlindSignatures) {
		t.Fatalf("replay returned different sig count")
	}
	if result2.BlindSignatures[0] != result1.BlindSignatures[0] {
		t.Error("replay returned different signature")
	}

	// Tokens should NOT be consumed again.
	if result2.TokensRemaining != 9 {
		t.Errorf("expected 9 remaining after replay, got %d", result2.TokensRemaining)
	}
}

func TestRedeem_NonceConflict(t *testing.T) {
	env := setupBlindTestEnv(t)
	_, preimage := env.createEntitlement(t, 10)

	secret1, _ := credentials.GenerateTokenSecret()
	bm1, _ := credentials.BlindTokenSecret(env.denomKey.PublicKey, secret1)

	secret2, _ := credentials.GenerateTokenSecret()
	bm2, _ := credentials.BlindTokenSecret(env.denomKey.PublicKey, secret2)

	// First request.
	body1, _ := json.Marshal(RedeemRequest{
		PaymentPreimage: preimage,
		KeyID:           "key-100mb",
		BlindedMessages: []string{hex.EncodeToString(bm1.Blinded)},
		Nonce:           "nonce-conflict",
	})
	resp1 := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader(body1))
	resp1.Body.Close()

	// Second request with same nonce but different blinded messages.
	body2, _ := json.Marshal(RedeemRequest{
		PaymentPreimage: preimage,
		KeyID:           "key-100mb",
		BlindedMessages: []string{hex.EncodeToString(bm2.Blinded)},
		Nonce:           "nonce-conflict",
	})
	resp2 := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader(body2))
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 Conflict, got %d", resp2.StatusCode)
	}
}

func TestRedeem_InsufficientTokens(t *testing.T) {
	env := setupBlindTestEnv(t)
	_, preimage := env.createEntitlement(t, 2)

	// Try to redeem 3 tokens when only 2 exist.
	msgs := make([]string, 3)
	for i := 0; i < 3; i++ {
		secret, _ := credentials.GenerateTokenSecret()
		bm, _ := credentials.BlindTokenSecret(env.denomKey.PublicKey, secret)
		msgs[i] = hex.EncodeToString(bm.Blinded)
	}

	body, _ := json.Marshal(RedeemRequest{
		PaymentPreimage: preimage,
		KeyID:           "key-100mb",
		BlindedMessages: msgs,
		Nonce:           "nonce-overdraw",
	})

	resp := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader(body))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("expected 402, got %d", resp.StatusCode)
	}
}

func TestRedeem_InvalidPreimage(t *testing.T) {
	env := setupBlindTestEnv(t)

	body, _ := json.Marshal(RedeemRequest{
		PaymentPreimage: "not-valid-hex",
		KeyID:           "key-100mb",
		BlindedMessages: []string{"aabbccdd"},
		Nonce:           "nonce-1",
	})

	resp := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader(body))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestRedeem_NoEntitlement(t *testing.T) {
	env := setupBlindTestEnv(t)

	// Valid preimage but no matching entitlement.
	preimage := make([]byte, 32)
	preimage[0] = 0xFF

	secret, _ := credentials.GenerateTokenSecret()
	bm, _ := credentials.BlindTokenSecret(env.denomKey.PublicKey, secret)

	body, _ := json.Marshal(RedeemRequest{
		PaymentPreimage: hex.EncodeToString(preimage),
		KeyID:           "key-100mb",
		BlindedMessages: []string{hex.EncodeToString(bm.Blinded)},
		Nonce:           "nonce-noent",
	})

	resp := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader(body))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestRedeem_WrongKeyID(t *testing.T) {
	env := setupBlindTestEnv(t)
	_, preimage := env.createEntitlement(t, 10)

	secret, _ := credentials.GenerateTokenSecret()
	bm, _ := credentials.BlindTokenSecret(env.denomKey.PublicKey, secret)

	body, _ := json.Marshal(RedeemRequest{
		PaymentPreimage: preimage,
		KeyID:           "wrong-key-id",
		BlindedMessages: []string{hex.EncodeToString(bm.Blinded)},
		Nonce:           "nonce-wrong-key",
	})

	resp := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader(body))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

// --- /spend tests ---

func TestSpend_FullFlow(t *testing.T) {
	env := setupBlindTestEnv(t)
	_, preimage := env.createEntitlement(t, 10)

	// Step 1: Redeem to get a blind signature.
	secret, _ := credentials.GenerateTokenSecret()
	bm, _ := credentials.BlindTokenSecret(env.denomKey.PublicKey, secret)

	redeemBody, _ := json.Marshal(RedeemRequest{
		PaymentPreimage: preimage,
		KeyID:           "key-100mb",
		BlindedMessages: []string{hex.EncodeToString(bm.Blinded)},
		Nonce:           "nonce-spend",
	})

	redeemResp := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader(redeemBody))
	var redeemResult RedeemResponse
	json.NewDecoder(redeemResp.Body).Decode(&redeemResult)
	redeemResp.Body.Close()

	// Unblind the signature.
	blindSig, _ := hex.DecodeString(redeemResult.BlindSignatures[0])
	unblinded := credentials.UnblindSignature(env.denomKey.PublicKey, blindSig, bm.Unblinder)

	// Step 2: Spend the token.
	spendBody, _ := json.Marshal(SpendRequest{
		KeyID:       "key-100mb",
		TokenSecret: hex.EncodeToString(secret),
		Signature:   hex.EncodeToString(unblinded),
		NodePubkey:  "node-pubkey-1",
	})

	resp := httpPost(t, env.server.URL+"/spend", "application/json", bytes.NewReader(spendBody))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errResp map[string]string
		json.NewDecoder(resp.Body).Decode(&errResp)
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, errResp)
	}

	var result SpendResponse
	json.NewDecoder(resp.Body).Decode(&result)

	if !result.FirstSpend {
		t.Error("expected first_spend=true")
	}
	if result.BytesPerToken != 100_000_000 {
		t.Errorf("expected 100MB denomination, got %d", result.BytesPerToken)
	}
}

func TestSpend_DoubleSpend(t *testing.T) {
	env := setupBlindTestEnv(t)
	_, preimage := env.createEntitlement(t, 10)

	// Redeem.
	secret, _ := credentials.GenerateTokenSecret()
	bm, _ := credentials.BlindTokenSecret(env.denomKey.PublicKey, secret)

	redeemBody, _ := json.Marshal(RedeemRequest{
		PaymentPreimage: preimage,
		KeyID:           "key-100mb",
		BlindedMessages: []string{hex.EncodeToString(bm.Blinded)},
		Nonce:           "nonce-double",
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
		NodePubkey:  "node-pubkey-1",
	})

	// First spend.
	resp1 := httpPost(t, env.server.URL+"/spend", "application/json", bytes.NewReader(spendBody))
	var result1 SpendResponse
	json.NewDecoder(resp1.Body).Decode(&result1)
	resp1.Body.Close()

	if !result1.FirstSpend {
		t.Fatal("first spend should be true")
	}

	// Second spend — same token.
	resp2 := httpPost(t, env.server.URL+"/spend", "application/json", bytes.NewReader(spendBody))
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("double-spend should still return 200 with first_spend=false, got %d", resp2.StatusCode)
	}

	var result2 SpendResponse
	json.NewDecoder(resp2.Body).Decode(&result2)

	if result2.FirstSpend {
		t.Error("second spend should have first_spend=false")
	}
}

func TestSpend_InvalidSignature(t *testing.T) {
	env := setupBlindTestEnv(t)

	secret, _ := credentials.GenerateTokenSecret()

	body, _ := json.Marshal(SpendRequest{
		KeyID:       "key-100mb",
		TokenSecret: hex.EncodeToString(secret),
		Signature:   hex.EncodeToString(make([]byte, 256)), // garbage signature
		NodePubkey:  "node-pubkey-1",
	})

	resp := httpPost(t, env.server.URL+"/spend", "application/json", bytes.NewReader(body))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestSpend_MissingFields(t *testing.T) {
	env := setupBlindTestEnv(t)

	cases := []struct {
		name string
		req  SpendRequest
	}{
		{"missing key_id", SpendRequest{TokenSecret: "aabb", Signature: "ccdd", NodePubkey: "node"}},
		{"missing token_secret", SpendRequest{KeyID: "key-100mb", Signature: "ccdd", NodePubkey: "node"}},
		{"missing signature", SpendRequest{KeyID: "key-100mb", TokenSecret: "aabb", NodePubkey: "node"}},
		{"missing node_pubkey", SpendRequest{KeyID: "key-100mb", TokenSecret: "aabb", Signature: "ccdd"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(tc.req)
			resp := httpPost(t, env.server.URL+"/spend", "application/json", bytes.NewReader(body))
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", resp.StatusCode)
			}
		})
	}
}

func TestRedeem_MissingFields(t *testing.T) {
	env := setupBlindTestEnv(t)

	cases := []struct {
		name string
		req  RedeemRequest
		code int
	}{
		{"missing preimage", RedeemRequest{KeyID: "k", BlindedMessages: []string{"aa"}, Nonce: "n"}, http.StatusBadRequest},
		{"missing key_id", RedeemRequest{PaymentPreimage: hex.EncodeToString(make([]byte, 32)), BlindedMessages: []string{"aa"}, Nonce: "n"}, http.StatusBadRequest},
		{"missing messages", RedeemRequest{PaymentPreimage: hex.EncodeToString(make([]byte, 32)), KeyID: "k", Nonce: "n"}, http.StatusBadRequest},
		{"missing nonce", RedeemRequest{PaymentPreimage: hex.EncodeToString(make([]byte, 32)), KeyID: "k", BlindedMessages: []string{"aa"}}, http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(tc.req)
			resp := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader(body))
			defer resp.Body.Close()
			if resp.StatusCode != tc.code {
				t.Fatalf("expected %d, got %d", tc.code, resp.StatusCode)
			}
		})
	}
}

// --- End-to-end: Redeem → Spend ---

func TestEndToEnd_RedeemAndSpend(t *testing.T) {
	env := setupBlindTestEnv(t)
	_, preimage := env.createEntitlement(t, 5)

	// Redeem 3 tokens.
	secrets := make([][]byte, 3)
	blindedMsgs := make([]string, 3)
	unblinders := make([][]byte, 3)

	for i := 0; i < 3; i++ {
		s, _ := credentials.GenerateTokenSecret()
		bm, _ := credentials.BlindTokenSecret(env.denomKey.PublicKey, s)
		secrets[i] = s
		blindedMsgs[i] = hex.EncodeToString(bm.Blinded)
		unblinders[i] = bm.Unblinder
	}

	redeemBody, _ := json.Marshal(RedeemRequest{
		PaymentPreimage: preimage,
		KeyID:           "key-100mb",
		BlindedMessages: blindedMsgs,
		Nonce:           "nonce-e2e",
	})
	redeemResp := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader(redeemBody))
	var redeemResult RedeemResponse
	json.NewDecoder(redeemResp.Body).Decode(&redeemResult)
	redeemResp.Body.Close()

	if redeemResult.TokensRedeemed != 3 {
		t.Fatalf("expected 3 redeemed, got %d", redeemResult.TokensRedeemed)
	}
	if redeemResult.TokensRemaining != 2 {
		t.Fatalf("expected 2 remaining, got %d", redeemResult.TokensRemaining)
	}

	// Spend each token.
	for i := 0; i < 3; i++ {
		blindSig, _ := hex.DecodeString(redeemResult.BlindSignatures[i])
		unblinded := credentials.UnblindSignature(env.denomKey.PublicKey, blindSig, unblinders[i])

		spendBody, _ := json.Marshal(SpendRequest{
			KeyID:       "key-100mb",
			TokenSecret: hex.EncodeToString(secrets[i]),
			Signature:   hex.EncodeToString(unblinded),
			NodePubkey:  "node-pubkey-1",
		})

		resp := httpPost(t, env.server.URL+"/spend", "application/json", bytes.NewReader(spendBody))
		var result SpendResponse
		json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()

		if !result.FirstSpend {
			t.Errorf("token %d: expected first_spend=true", i)
		}
		if result.BytesPerToken != 100_000_000 {
			t.Errorf("token %d: expected 100MB, got %d", i, result.BytesPerToken)
		}
	}
}

func TestRedeem_MethodNotAllowed(t *testing.T) {
	env := setupBlindTestEnv(t)
	resp := httpGet(t, env.server.URL+"/redeem")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}
}

func TestSpend_MethodNotAllowed(t *testing.T) {
	env := setupBlindTestEnv(t)
	resp := httpGet(t, env.server.URL+"/spend")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}
}

// --- Settlement creates entitlement ---

func TestSettlement_CreatesEntitlement(t *testing.T) {
	env := setupBlindTestEnv(t)

	// Step 1: Purchase.
	purchaseBody, _ := json.Marshal(PurchaseRequest{TierID: "1gb"})
	resp := httpPost(t, env.server.URL+"/purchase", "application/json", bytes.NewReader(purchaseBody))
	var purchase PurchaseResponse
	json.NewDecoder(resp.Body).Decode(&purchase)
	resp.Body.Close()

	// Step 2: Settle the invoice via mock.
	if err := env.mock.SimulateSettlement(purchase.PaymentHash); err != nil {
		t.Fatalf("SimulateSettlement: %v", err)
	}
	time.Sleep(200 * time.Millisecond) // let settlement listener process

	// Step 3: Verify entitlement was created.
	ent, err := env.store.GetEntitlementByPaymentHash(purchase.PaymentHash)
	if err != nil {
		t.Fatalf("GetEntitlementByPaymentHash: %v", err)
	}

	if ent.TokensTotal != 10 { // 1gb tier = 10 tickets
		t.Errorf("expected 10 tokens total, got %d", ent.TokensTotal)
	}
	if ent.TokensRemaining != 10 {
		t.Errorf("expected 10 tokens remaining, got %d", ent.TokensRemaining)
	}
	if ent.BytesPerToken != 100_000_000 {
		t.Errorf("expected 100MB per token, got %d", ent.BytesPerToken)
	}
	if ent.KeyID != "key-100mb" {
		t.Errorf("expected key-100mb, got %s", ent.KeyID)
	}
}

// TestEndToEnd_PurchaseSettleRedeemSpend tests the complete Phase 4 flow:
// purchase → pay → settle → redeem → spend
func TestEndToEnd_PurchaseSettleRedeemSpend(t *testing.T) {
	env := setupBlindTestEnv(t)

	// Step 1: Purchase.
	purchaseBody, _ := json.Marshal(PurchaseRequest{TierID: "1gb"})
	resp := httpPost(t, env.server.URL+"/purchase", "application/json", bytes.NewReader(purchaseBody))
	var purchase PurchaseResponse
	json.NewDecoder(resp.Body).Decode(&purchase)
	resp.Body.Close()

	// Step 2: Settle invoice.
	env.mock.SimulateSettlement(purchase.PaymentHash)
	time.Sleep(200 * time.Millisecond)

	// Step 3: Get the preimage from the mock.
	preimage := env.mock.GetPreimage(purchase.PaymentHash)
	if preimage == "" {
		t.Fatal("no preimage from mock")
	}

	// Step 4: Redeem blind tokens.
	secret, _ := credentials.GenerateTokenSecret()
	bm, _ := credentials.BlindTokenSecret(env.denomKey.PublicKey, secret)

	redeemBody, _ := json.Marshal(RedeemRequest{
		PaymentPreimage: preimage,
		KeyID:           "key-100mb",
		BlindedMessages: []string{hex.EncodeToString(bm.Blinded)},
		Nonce:           "nonce-e2e-full",
	})
	redeemResp := httpPost(t, env.server.URL+"/redeem", "application/json", bytes.NewReader(redeemBody))
	var redeemResult RedeemResponse
	json.NewDecoder(redeemResp.Body).Decode(&redeemResult)
	redeemResp.Body.Close()

	if len(redeemResult.BlindSignatures) != 1 {
		t.Fatalf("expected 1 signature, got %d", len(redeemResult.BlindSignatures))
	}
	if redeemResult.TokensRemaining != 9 {
		t.Fatalf("expected 9 remaining, got %d", redeemResult.TokensRemaining)
	}

	// Step 5: Unblind and spend.
	blindSig, _ := hex.DecodeString(redeemResult.BlindSignatures[0])
	unblinded := credentials.UnblindSignature(env.denomKey.PublicKey, blindSig, bm.Unblinder)

	spendBody, _ := json.Marshal(SpendRequest{
		KeyID:       "key-100mb",
		TokenSecret: hex.EncodeToString(secret),
		Signature:   hex.EncodeToString(unblinded),
		NodePubkey:  "node-1",
	})
	spendResp := httpPost(t, env.server.URL+"/spend", "application/json", bytes.NewReader(spendBody))
	var spendResult SpendResponse
	json.NewDecoder(spendResp.Body).Decode(&spendResult)
	spendResp.Body.Close()

	if !spendResult.FirstSpend {
		t.Error("expected first_spend=true")
	}
	if spendResult.BytesPerToken != 100_000_000 {
		t.Error("expected 100MB denomination")
	}
}
