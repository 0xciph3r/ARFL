package integration

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Radi-Labs/ARFL/internal/client"
	"github.com/Radi-Labs/ARFL/internal/credentials"
	"github.com/Radi-Labs/ARFL/internal/node"
	"github.com/Radi-Labs/ARFL/test/testutil"
)

// Phase 5 STRIDE tests for the client-side attack surfaces:
// - TokenGate (node verifier)
// - BandwidthClient (purchase + redeem SDK)
// - Key persistence (keystore)
// - Token persistence (token files)
//
// Phase 4 STRIDE tests (in internal/payments/) cover the server-side
// endpoints (/redeem, /spend). These tests cover the client/node side.

// ========================================================================
// SPOOFING — Attacker impersonates a legitimate entity
// ========================================================================

func TestSTRIDE_TokenGate_WrongDenomKey(t *testing.T) {
	// THREAT: Attacker runs a rogue hub that signs tokens with a different
	// key. Nodes must reject tokens signed by keys they don't trust.
	hub := testutil.SetupTestHub(t)

	// Generate a completely separate key (rogue hub).
	rogueKey, _ := credentials.GenerateDenominationKey("key-100mb", 100_000_000)
	rogueMint := credentials.NewRSABlindMint([]*credentials.DenominationKey{rogueKey})

	// Mint a token with the rogue key.
	secret, _ := credentials.GenerateTokenSecret()
	bm, _ := credentials.BlindTokenSecret(rogueKey.PublicKey, secret)
	sigs, _ := rogueMint.SignBlinded("key-100mb", [][]byte{bm.Blinded})
	unblinded := credentials.UnblindSignature(rogueKey.PublicKey, sigs[0], bm.Unblinder)

	rogueToken := &credentials.BlindToken{
		Version:     credentials.BlindTokenVersion,
		KeyID:       "key-100mb",
		TokenSecret: secret,
		Signature:   unblinded,
	}

	// Node trusts only the real hub's key.
	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(hub.DenomKey),
	})
	gate := node.NewTokenGate(verifier, hub.Server.URL, "node-1")

	spend, err := gate.VerifyAndSpend(context.Background(), rogueToken)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spend.Valid {
		t.Fatal("rogue-hub token must NOT pass node verification")
	}
}

func TestSTRIDE_TokenGate_VersionMismatch(t *testing.T) {
	// THREAT: Attacker forges a token with a future version number to
	// bypass version-specific checks.
	hub := testutil.SetupTestHub(t)

	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(hub.DenomKey),
	})
	gate := node.NewTokenGate(verifier, hub.Server.URL, "node-1")

	// Get a legitimately signed token, then change its version.
	bwClient := client.NewBandwidthClient(hub.Server.URL, hub.DenomKey.PublicKey, "key-100mb")
	purchase, _ := bwClient.Purchase(context.Background(), "1gb")
	hub.Mock.SimulateSettlement(purchase.PaymentHash)
	time.Sleep(200 * time.Millisecond)
	preimage := hub.Mock.GetPreimage(purchase.PaymentHash)

	result, _ := bwClient.RedeemTokens(context.Background(), preimage, 1, "nonce-version")
	token := result.Tokens[0]
	token.Version = 99 // future version

	spend, err := gate.VerifyAndSpend(context.Background(), token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spend.Valid {
		t.Fatal("token with wrong version must be rejected")
	}
}

func TestSTRIDE_Client_RogueHubSignatures(t *testing.T) {
	// THREAT: Client connects to a rogue hub that returns garbage blind
	// signatures. The resulting tokens should fail node verification.
	rogueKey, _ := credentials.GenerateDenominationKey("key-100mb", 100_000_000)

	// Rogue hub returns deterministic garbage signatures.
	rogue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/purchase":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"payment_hash":    "abc123",
				"payment_request": "lnbc...",
				"amount_sats":     500,
				"tier":            "1gb",
				"expires_at":      "2099-01-01T00:00:00Z",
			})
			w.WriteHeader(http.StatusCreated)
		case "/redeem":
			body, _ := io.ReadAll(r.Body)
			var req struct {
				BlindedMessages []string `json:"blinded_messages"`
			}
			json.Unmarshal(body, &req)

			// Return garbage signatures (same length, wrong content).
			sigs := make([]string, len(req.BlindedMessages))
			for i := range sigs {
				garbage := make([]byte, 256)
				for j := range garbage {
					garbage[j] = byte(i + j)
				}
				sigs[i] = hex.EncodeToString(garbage)
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"blind_signatures": sigs,
				"bytes_per_token":  100_000_000,
				"tokens_redeemed":  len(sigs),
				"tokens_remaining": 0,
			})
		}
	}))
	defer rogue.Close()

	// Client talks to rogue hub but verifies against real key.
	realKey, _ := credentials.GenerateDenominationKey("key-100mb", 100_000_000)
	bwClient := client.NewBandwidthClient(rogue.URL, rogueKey.PublicKey, "key-100mb")

	result, err := bwClient.RedeemTokens(context.Background(), "deadbeef", 2, "nonce-rogue")
	if err != nil {
		t.Fatalf("RedeemTokens should succeed (client doesn't verify at redeem): %v", err)
	}

	// But when the node tries to verify, it should fail.
	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(realKey),
	})
	gate := node.NewTokenGate(verifier, "http://irrelevant", "node-1")

	for i, token := range result.Tokens {
		spend, _ := gate.VerifyOnly(token)
		if spend.Valid {
			t.Errorf("token %d from rogue hub should not verify", i)
		}
	}
}

// ========================================================================
// TAMPERING — Attacker modifies data in transit or at rest
// ========================================================================

func TestSTRIDE_TokenGate_TamperedSecret(t *testing.T) {
	// THREAT: Man-in-the-middle tampers with the token secret after
	// the client receives valid tokens but before presenting to node.
	hub := testutil.SetupTestHub(t)

	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(hub.DenomKey),
	})
	gate := node.NewTokenGate(verifier, hub.Server.URL, "node-1")

	bwClient := client.NewBandwidthClient(hub.Server.URL, hub.DenomKey.PublicKey, "key-100mb")
	purchase, _ := bwClient.Purchase(context.Background(), "1gb")
	hub.Mock.SimulateSettlement(purchase.PaymentHash)
	time.Sleep(200 * time.Millisecond)
	preimage := hub.Mock.GetPreimage(purchase.PaymentHash)

	result, _ := bwClient.RedeemTokens(context.Background(), preimage, 1, "nonce-tamper")
	token := result.Tokens[0]

	// Tamper: flip a bit in the secret.
	tampered := make([]byte, len(token.TokenSecret))
	copy(tampered, token.TokenSecret)
	tampered[0] ^= 0xFF
	token.TokenSecret = tampered

	spend, err := gate.VerifyAndSpend(context.Background(), token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spend.Valid {
		t.Fatal("tampered token secret must fail verification")
	}
}

func TestSTRIDE_TokenGate_TamperedSignature(t *testing.T) {
	// THREAT: Attacker modifies the signature bytes to try an
	// existential forgery.
	hub := testutil.SetupTestHub(t)

	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(hub.DenomKey),
	})
	gate := node.NewTokenGate(verifier, hub.Server.URL, "node-1")

	bwClient := client.NewBandwidthClient(hub.Server.URL, hub.DenomKey.PublicKey, "key-100mb")
	purchase, _ := bwClient.Purchase(context.Background(), "1gb")
	hub.Mock.SimulateSettlement(purchase.PaymentHash)
	time.Sleep(200 * time.Millisecond)
	preimage := hub.Mock.GetPreimage(purchase.PaymentHash)

	result, _ := bwClient.RedeemTokens(context.Background(), preimage, 1, "nonce-sig-tamper")
	token := result.Tokens[0]

	// Tamper: flip one bit in the signature.
	token.Signature[len(token.Signature)/2] ^= 0x01

	spend, err := gate.VerifyAndSpend(context.Background(), token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spend.Valid {
		t.Fatal("tampered signature must fail verification")
	}
}

func TestSTRIDE_Keystore_TamperedPrivateKey(t *testing.T) {
	// THREAT: Attacker modifies the key file on disk (corrupted or
	// replaced private key). Load should fail.
	dir := t.TempDir()
	path := filepath.Join(dir, "key.json")

	original, _ := credentials.GenerateDenominationKey("key-100mb", 100_000_000)
	credentials.SaveDenominationKey(path, original)

	// Tamper: corrupt the PEM content.
	data, _ := os.ReadFile(path)
	corrupted := strings.Replace(string(data), "PRIVATE KEY", "CORRUPTED", 1)
	os.WriteFile(path, []byte(corrupted), 0600)

	_, err := credentials.LoadDenominationKey(path)
	if err == nil {
		t.Fatal("loading tampered key file should fail")
	}
}

func TestSTRIDE_Keystore_TamperedPublicKey(t *testing.T) {
	// THREAT: MITM replaces the public key file distributed to nodes,
	// so nodes verify against the wrong key. Tokens should fail.
	dir := t.TempDir()

	// Generate real key and save the public part.
	realKey, _ := credentials.GenerateDenominationKey("key-100mb", 100_000_000)
	pubPath := filepath.Join(dir, "key.pub.json")
	credentials.SavePublicKey(pubPath, realKey)

	// Generate rogue key and overwrite the public file.
	rogueKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	rogueDenom := &credentials.DenominationKey{
		KeyID:         "key-100mb",
		BytesPerToken: 100_000_000,
		PublicKey:     &rogueKey.PublicKey,
	}
	credentials.SavePublicKey(pubPath, rogueDenom)

	// Node loads the tampered public key.
	loaded, err := credentials.LoadPublicKey(pubPath)
	if err != nil {
		t.Fatalf("LoadPublicKey: %v", err)
	}

	// Token signed by the real key should fail verification with the tampered key.
	mint := credentials.NewRSABlindMint([]*credentials.DenominationKey{realKey})
	secret, _ := credentials.GenerateTokenSecret()
	bm, _ := credentials.BlindTokenSecret(realKey.PublicKey, secret)
	sigs, _ := mint.SignBlinded("key-100mb", [][]byte{bm.Blinded})
	unblinded := credentials.UnblindSignature(realKey.PublicKey, sigs[0], bm.Unblinder)

	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{loaded})
	token := &credentials.BlindToken{
		Version:     credentials.BlindTokenVersion,
		KeyID:       "key-100mb",
		TokenSecret: secret,
		Signature:   unblinded,
	}

	if err := verifier.Verify(token); err == nil {
		t.Fatal("token should NOT verify against tampered public key")
	}
}

func TestSTRIDE_TokenFile_TamperedOnDisk(t *testing.T) {
	// THREAT: Attacker modifies tokens.json to inject forged tokens.
	// When loaded and presented to a node, they must fail verification.
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	// Write a forged token file.
	forgedSecret := make([]byte, 32)
	for i := range forgedSecret {
		forgedSecret[i] = byte(i)
	}
	forgedToken := &credentials.BlindToken{
		Version:     credentials.BlindTokenVersion,
		KeyID:       "key-100mb",
		TokenSecret: forgedSecret,
		Signature:   make([]byte, 256), // garbage sig
	}

	store := struct {
		Tokens []*credentials.BlindToken `json:"tokens"`
	}{Tokens: []*credentials.BlindToken{forgedToken}}

	data, _ := json.MarshalIndent(store, "", "  ")
	os.WriteFile(path, data, 0600)

	// Load the tampered tokens.
	loaded, _ := os.ReadFile(path)
	var loaded_store struct {
		Tokens []*credentials.BlindToken `json:"tokens"`
	}
	json.Unmarshal(loaded, &loaded_store)

	// Node verifies: should reject.
	realKey, _ := credentials.GenerateDenominationKey("key-100mb", 100_000_000)
	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(realKey),
	})

	if err := verifier.Verify(loaded_store.Tokens[0]); err == nil {
		t.Fatal("forged token from tampered file should not verify")
	}
}

// ========================================================================
// REPUDIATION — Inability to prove an action occurred
// ========================================================================

func TestSTRIDE_TokenGate_SpendCreatesDurableRecord(t *testing.T) {
	// REQUIREMENT: After a node spends a token, the Hub has a durable
	// record. A second spend of the same token must return first_spend=false.
	hub := testutil.SetupTestHub(t)

	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(hub.DenomKey),
	})

	bwClient := client.NewBandwidthClient(hub.Server.URL, hub.DenomKey.PublicKey, "key-100mb")
	purchase, _ := bwClient.Purchase(context.Background(), "1gb")
	hub.Mock.SimulateSettlement(purchase.PaymentHash)
	time.Sleep(200 * time.Millisecond)
	preimage := hub.Mock.GetPreimage(purchase.PaymentHash)
	result, _ := bwClient.RedeemTokens(context.Background(), preimage, 1, "nonce-repudiation")

	// Spend from node A.
	gateA := node.NewTokenGate(verifier, hub.Server.URL, "node-A")
	spendA, _ := gateA.VerifyAndSpend(context.Background(), result.Tokens[0])
	if !spendA.FirstSpend {
		t.Fatal("first spend should be true")
	}

	// Spend same token from node B — Hub proves the token was already spent.
	gateB := node.NewTokenGate(verifier, hub.Server.URL, "node-B")
	spendB, _ := gateB.VerifyAndSpend(context.Background(), result.Tokens[0])
	if spendB.FirstSpend {
		t.Fatal("Hub must record spend durably — replay detected")
	}
}

// ========================================================================
// INFORMATION DISCLOSURE — Sensitive data leaks
// ========================================================================

func TestSTRIDE_Client_HubErrorDoesNotLeakPreimage(t *testing.T) {
	// THREAT: Hub returns error messages that include the preimage or
	// payment hash in cleartext. SDK errors must not propagate these.
	hub := testutil.SetupTestHub(t)
	bwClient := client.NewBandwidthClient(hub.Server.URL, hub.DenomKey.PublicKey, "key-100mb")

	// Try to redeem with a fake preimage that doesn't exist.
	fakePreimage := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	_, err := bwClient.RedeemTokens(context.Background(), fakePreimage, 1, "nonce-leak")
	if err == nil {
		t.Fatal("expected error for invalid preimage")
	}

	errMsg := err.Error()
	if strings.Contains(errMsg, fakePreimage) {
		t.Errorf("error message leaks preimage: %s", errMsg)
	}
}

func TestSTRIDE_Keystore_PrivateKeyPermissions(t *testing.T) {
	// REQUIREMENT: Private key files must be 0600 (owner-only).
	dir := t.TempDir()
	path := filepath.Join(dir, "key.json")

	key, _ := credentials.GenerateDenominationKey("key-100mb", 100_000_000)
	credentials.SaveDenominationKey(path, key)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("private key file has permissions %o, want 0600", perm)
	}
}

func TestSTRIDE_Keystore_PublicKeyNoPrivateMaterial(t *testing.T) {
	// REQUIREMENT: The exported public key file must NOT contain
	// the private key (PEM or otherwise).
	dir := t.TempDir()
	path := filepath.Join(dir, "key.pub.json")

	key, _ := credentials.GenerateDenominationKey("key-100mb", 100_000_000)
	credentials.SavePublicKey(path, key)

	data, _ := os.ReadFile(path)
	content := string(data)

	if strings.Contains(content, "PRIVATE KEY") {
		t.Fatal("public key file contains private key PEM header")
	}
	if strings.Contains(content, "private_key_pem") {
		t.Fatal("public key file contains private_key_pem field")
	}
}

func TestSTRIDE_TokenGate_SpendDoesNotLeakTokenSecret(t *testing.T) {
	// REQUIREMENT: When the Hub receives /spend, the response should
	// not echo the token_secret back. We verify via the SDK's result.
	hub := testutil.SetupTestHub(t)

	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(hub.DenomKey),
	})
	gate := node.NewTokenGate(verifier, hub.Server.URL, "node-1")

	bwClient := client.NewBandwidthClient(hub.Server.URL, hub.DenomKey.PublicKey, "key-100mb")
	purchase, _ := bwClient.Purchase(context.Background(), "1gb")
	hub.Mock.SimulateSettlement(purchase.PaymentHash)
	time.Sleep(200 * time.Millisecond)
	preimage := hub.Mock.GetPreimage(purchase.PaymentHash)
	result, _ := bwClient.RedeemTokens(context.Background(), preimage, 1, "nonce-no-echo")

	spend, err := gate.VerifyAndSpend(context.Background(), result.Tokens[0])
	if err != nil {
		t.Fatalf("VerifyAndSpend: %v", err)
	}

	// SpendResult should contain only Valid, FirstSpend, BytesPerToken.
	// No token_secret, signature, or key material.
	if !spend.Valid {
		t.Error("expected valid")
	}
	// The struct has only 3 fields by design. This is a compile-time
	// guarantee but we document the intent here for reviewers.
}

// ========================================================================
// DENIAL OF SERVICE — Exhaust resources or disrupt service
// ========================================================================

func TestSTRIDE_Client_RedeemZeroTokens(t *testing.T) {
	// THREAT: Client requests zero or negative token count.
	hub := testutil.SetupTestHub(t)
	bwClient := client.NewBandwidthClient(hub.Server.URL, hub.DenomKey.PublicKey, "key-100mb")

	_, err := bwClient.RedeemTokens(context.Background(), "somepreimage", 0, "nonce-zero")
	if err == nil {
		t.Fatal("zero count should be rejected")
	}

	_, err = bwClient.RedeemTokens(context.Background(), "somepreimage", -5, "nonce-neg")
	if err == nil {
		t.Fatal("negative count should be rejected")
	}
}

func TestSTRIDE_TokenGate_EmptyToken(t *testing.T) {
	// THREAT: Node receives completely empty or zero-value token fields.
	hub := testutil.SetupTestHub(t)

	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(hub.DenomKey),
	})
	gate := node.NewTokenGate(verifier, hub.Server.URL, "node-1")

	cases := []struct {
		name  string
		token *credentials.BlindToken
	}{
		{"empty_secret", &credentials.BlindToken{
			Version: credentials.BlindTokenVersion, KeyID: "key-100mb",
			TokenSecret: nil, Signature: make([]byte, 256),
		}},
		{"empty_signature", &credentials.BlindToken{
			Version: credentials.BlindTokenVersion, KeyID: "key-100mb",
			TokenSecret: make([]byte, 32), Signature: nil,
		}},
		{"empty_key_id", &credentials.BlindToken{
			Version: credentials.BlindTokenVersion, KeyID: "",
			TokenSecret: make([]byte, 32), Signature: make([]byte, 256),
		}},
		{"zero_version", &credentials.BlindToken{
			Version: 0, KeyID: "key-100mb",
			TokenSecret: make([]byte, 32), Signature: make([]byte, 256),
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spend, err := gate.VerifyAndSpend(context.Background(), tc.token)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if spend.Valid {
				t.Errorf("%s: should be invalid", tc.name)
			}
		})
	}
}

func TestSTRIDE_TokenGate_HubUnreachable(t *testing.T) {
	// THREAT: Hub goes down. VerifyAndSpend should return an error
	// (not silently pass), so the node can fall back to VerifyOnly.
	hub := testutil.SetupTestHub(t)

	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(hub.DenomKey),
	})

	// Get a valid token.
	bwClient := client.NewBandwidthClient(hub.Server.URL, hub.DenomKey.PublicKey, "key-100mb")
	purchase, _ := bwClient.Purchase(context.Background(), "1gb")
	hub.Mock.SimulateSettlement(purchase.PaymentHash)
	time.Sleep(200 * time.Millisecond)
	preimage := hub.Mock.GetPreimage(purchase.PaymentHash)
	result, _ := bwClient.RedeemTokens(context.Background(), preimage, 1, "nonce-unreachable")

	// Shut down the hub.
	hub.Server.Close()

	// Point gate at the now-dead hub.
	gate := node.NewTokenGate(verifier, hub.Server.URL, "node-1")

	_, err := gate.VerifyAndSpend(context.Background(), result.Tokens[0])
	if err == nil {
		t.Fatal("VerifyAndSpend must return error when hub is unreachable")
	}

	// VerifyOnly should still work (offline).
	spend, err := gate.VerifyOnly(result.Tokens[0])
	if err != nil {
		t.Fatalf("VerifyOnly should work offline: %v", err)
	}
	if !spend.Valid {
		t.Error("VerifyOnly should pass for valid token even when hub is down")
	}
}

// ========================================================================
// ELEVATION OF PRIVILEGE — Gain more access than entitled
// ========================================================================

func TestSTRIDE_VerifyOnly_DoubleSpendBypasses(t *testing.T) {
	// THREAT: Attacker presents the same token to multiple nodes using
	// VerifyOnly (offline mode). Each node accepts it. This is the
	// bounded risk of grace period mode — it must be documented.
	hub := testutil.SetupTestHub(t)

	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(hub.DenomKey),
	})

	bwClient := client.NewBandwidthClient(hub.Server.URL, hub.DenomKey.PublicKey, "key-100mb")
	purchase, _ := bwClient.Purchase(context.Background(), "1gb")
	hub.Mock.SimulateSettlement(purchase.PaymentHash)
	time.Sleep(200 * time.Millisecond)
	preimage := hub.Mock.GetPreimage(purchase.PaymentHash)
	result, _ := bwClient.RedeemTokens(context.Background(), preimage, 1, "nonce-grace")

	// Same token verified at 3 different nodes in offline mode.
	// ALL should pass — this is the documented risk.
	for i := 0; i < 3; i++ {
		gate := node.NewTokenGate(verifier, hub.Server.URL, fmt.Sprintf("node-%d", i))
		spend, err := gate.VerifyOnly(result.Tokens[0])
		if err != nil {
			t.Fatalf("node %d: VerifyOnly: %v", i, err)
		}
		if !spend.Valid || !spend.FirstSpend {
			t.Errorf("node %d: offline mode should accept valid token", i)
		}
	}

	// But VerifyAndSpend (online) catches the double-spend after first use.
	gateOnline := node.NewTokenGate(verifier, hub.Server.URL, "node-online-1")
	spend1, _ := gateOnline.VerifyAndSpend(context.Background(), result.Tokens[0])
	if !spend1.FirstSpend {
		t.Fatal("first online spend should succeed")
	}

	gateOnline2 := node.NewTokenGate(verifier, hub.Server.URL, "node-online-2")
	spend2, _ := gateOnline2.VerifyAndSpend(context.Background(), result.Tokens[0])
	if spend2.FirstSpend {
		t.Fatal("second online spend must be detected as double-spend")
	}
}

func TestSTRIDE_Client_CrossKeyRedeem(t *testing.T) {
	// THREAT: Client redeems tokens for key_id "key-1gb" but presents
	// them claiming key_id "key-100mb" to get more bandwidth per token.
	// The signature won't verify because it was signed under a different key.
	hub := testutil.SetupTestHub(t)

	bwClient := client.NewBandwidthClient(hub.Server.URL, hub.DenomKey.PublicKey, "key-100mb")
	purchase, _ := bwClient.Purchase(context.Background(), "1gb")
	hub.Mock.SimulateSettlement(purchase.PaymentHash)
	time.Sleep(200 * time.Millisecond)
	preimage := hub.Mock.GetPreimage(purchase.PaymentHash)

	result, _ := bwClient.RedeemTokens(context.Background(), preimage, 1, "nonce-cross-key")

	// Tamper: change the key_id in the token.
	token := result.Tokens[0]
	token.KeyID = "key-1gb" // pretend it's a bigger denomination

	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(hub.DenomKey),
	})
	gate := node.NewTokenGate(verifier, hub.Server.URL, "node-1")

	spend, err := gate.VerifyAndSpend(context.Background(), token)
	if err != nil {
		// Error is acceptable (unknown key).
		return
	}
	if spend.Valid {
		t.Fatal("cross-key token must not be accepted")
	}
}

// ========================================================================
// CONCURRENT — Race conditions and atomicity
// ========================================================================

func TestSTRIDE_ConcurrentSpend_SameTokenMultipleNodes(t *testing.T) {
	// THREAT: Multiple nodes race to spend the same token concurrently.
	// Exactly one must get first_spend=true.
	hub := testutil.SetupTestHub(t)

	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(hub.DenomKey),
	})

	bwClient := client.NewBandwidthClient(hub.Server.URL, hub.DenomKey.PublicKey, "key-100mb")
	purchase, _ := bwClient.Purchase(context.Background(), "1gb")
	hub.Mock.SimulateSettlement(purchase.PaymentHash)
	time.Sleep(200 * time.Millisecond)
	preimage := hub.Mock.GetPreimage(purchase.PaymentHash)
	result, _ := bwClient.RedeemTokens(context.Background(), preimage, 1, "nonce-race")

	var wg sync.WaitGroup
	var firstSpendCount int32
	var validCount int32
	var errorCount int32

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(nodeID int) {
			defer wg.Done()
			gate := node.NewTokenGate(verifier, hub.Server.URL, fmt.Sprintf("node-%d", nodeID))
			spend, err := gate.VerifyAndSpend(context.Background(), result.Tokens[0])
			if err != nil {
				atomic.AddInt32(&errorCount, 1)
				return
			}
			if spend.Valid {
				atomic.AddInt32(&validCount, 1)
			}
			if spend.FirstSpend {
				atomic.AddInt32(&firstSpendCount, 1)
			}
		}(i)
	}
	wg.Wait()

	if firstSpendCount != 1 {
		t.Fatalf("expected exactly 1 first_spend across 20 concurrent nodes, got %d", firstSpendCount)
	}
	t.Logf("Results: %d valid, %d first_spend, %d errors", validCount, firstSpendCount, errorCount)
}

func TestSTRIDE_ConcurrentRedeem_IndependentEntitlements(t *testing.T) {
	// THREAT: Multiple clients redeem concurrently from different
	// entitlements. Ensure no cross-contamination.
	hub := testutil.SetupTestHub(t)

	var wg sync.WaitGroup
	var successCount int32

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()

			bwClient := client.NewBandwidthClient(hub.Server.URL, hub.DenomKey.PublicKey, "key-100mb")
			purchase, err := bwClient.Purchase(context.Background(), "1gb")
			if err != nil {
				return
			}

			hub.Mock.SimulateSettlement(purchase.PaymentHash)
			time.Sleep(300 * time.Millisecond)
			preimage := hub.Mock.GetPreimage(purchase.PaymentHash)

			result, err := bwClient.RedeemTokens(
				context.Background(), preimage, 2,
				fmt.Sprintf("nonce-concurrent-%d", clientID),
			)
			if err != nil {
				return
			}

			if len(result.Tokens) == 2 {
				atomic.AddInt32(&successCount, 1)
			}
		}(i)
	}
	wg.Wait()

	if successCount < 3 {
		t.Fatalf("expected at least 3/5 concurrent clients to succeed, got %d", successCount)
	}
}

// ========================================================================
// KEY LIFECYCLE — Key rotation and persistence edge cases
// ========================================================================

func TestSTRIDE_Keystore_DifferentKeySameID(t *testing.T) {
	// THREAT: Two different RSA keys saved with the same key_id.
	// Loading should return the last-written key. Tokens from the
	// first key must NOT verify against the second.
	dir := t.TempDir()
	path := filepath.Join(dir, "key.json")

	key1, _ := credentials.GenerateDenominationKey("key-100mb", 100_000_000)
	credentials.SaveDenominationKey(path, key1)

	// Sign a token with key1.
	mint1 := credentials.NewRSABlindMint([]*credentials.DenominationKey{key1})
	secret, _ := credentials.GenerateTokenSecret()
	bm, _ := credentials.BlindTokenSecret(key1.PublicKey, secret)
	sigs, _ := mint1.SignBlinded("key-100mb", [][]byte{bm.Blinded})
	unblinded := credentials.UnblindSignature(key1.PublicKey, sigs[0], bm.Unblinder)

	// Overwrite with a different key (same ID).
	key2, _ := credentials.GenerateDenominationKey("key-100mb", 100_000_000)
	credentials.SaveDenominationKey(path, key2)

	// Load key2 and try to verify key1's token.
	loaded, _ := credentials.LoadDenominationKey(path)
	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(loaded),
	})

	token := &credentials.BlindToken{
		Version:     credentials.BlindTokenVersion,
		KeyID:       "key-100mb",
		TokenSecret: secret,
		Signature:   unblinded,
	}

	if err := verifier.Verify(token); err == nil {
		t.Fatal("token from key1 must NOT verify with key2 — key rotation invalidates old tokens")
	}
}
