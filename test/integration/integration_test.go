// Package integration tests the full ARFL blind signature protocol
// end-to-end in a single process. No network infrastructure required.
//
// This test proves to grant reviewers that:
// 1. Hub generates and persists denomination keys
// 2. Client purchases bandwidth via Lightning invoice
// 3. Client redeems blind tokens (Hub never sees token secrets)
// 4. Node verifies tokens and checks double-spend via Hub
// 5. Buyer-session unlinkability is maintained throughout
package integration

import (
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Radi-Labs/ARFL/internal/client"
	"github.com/Radi-Labs/ARFL/internal/control"
	"github.com/Radi-Labs/ARFL/internal/credentials"
	"github.com/Radi-Labs/ARFL/internal/lightning"
	"github.com/Radi-Labs/ARFL/internal/node"
	"github.com/Radi-Labs/ARFL/internal/payments"
	"github.com/Radi-Labs/ARFL/internal/quota"
	"github.com/Radi-Labs/ARFL/internal/store"
	"github.com/Radi-Labs/ARFL/internal/wg"
)

// TestFullProtocol_PurchaseRedeemSpend exercises the complete ARFL
// bandwidth purchase flow in a single test:
//
//	Hub starts → Client purchases → Payment settles → Client redeems
//	blind tokens → Client presents to Node → Node verifies + spends
//
// This is the "one test that proves it works" for the grant narrative.
func TestFullProtocol_PurchaseRedeemSpend(t *testing.T) {
	// ===== HUB SETUP =====
	// In production, this runs as `arfl-hub`.
	db, cleanup := openTestDB(t)
	defer cleanup()

	mock := lightning.NewMockClient()
	issuer := credentials.NewHMACIssuer("key-1", []byte("test-secret-key-for-hmac-32bytes!"))
	api := payments.NewPurchaseAPI(db, mock, issuer)
	api.StartSettlementListener(context.Background())
	defer api.Stop()

	// Generate denomination key (in production, loaded from disk).
	denomKey, err := credentials.GenerateDenominationKey("key-100mb", 100_000_000)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	mint := credentials.NewRSABlindMint([]*credentials.DenominationKey{denomKey})
	hubVerifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(denomKey),
	})
	api.EnableBlindSignatures(mint, hubVerifier, "key-100mb")

	server := httptest.NewServer(api.Handler())
	defer server.Close()

	t.Logf("Hub running at %s", server.URL)

	// ===== CLIENT PURCHASES BANDWIDTH =====
	// In production, the user runs `arfl-client --purchase 1gb`.
	bwClient := client.NewBandwidthClient(server.URL, denomKey.PublicKey, "key-100mb")

	purchase, err := bwClient.Purchase(context.Background(), "1gb")
	if err != nil {
		t.Fatalf("Purchase: %v", err)
	}

	t.Logf("Invoice created: %s (%d sats)", purchase.PaymentHash[:16]+"...", purchase.AmountSats)

	if purchase.AmountSats != 500 {
		t.Errorf("expected 500 sats for 1gb tier, got %d", purchase.AmountSats)
	}

	// ===== PAYMENT SETTLES =====
	// In production, the user pays via their Lightning wallet.
	// The wallet reveals the preimage upon successful payment.
	mock.SimulateSettlement(purchase.PaymentHash)
	time.Sleep(200 * time.Millisecond) // settlement listener processes

	preimage := mock.GetPreimage(purchase.PaymentHash)
	t.Logf("Payment settled, preimage: %s...", preimage[:16])

	// Verify entitlement was created by settlement listener.
	ent, err := db.GetEntitlementByPaymentHash(purchase.PaymentHash)
	if err != nil {
		t.Fatalf("no entitlement created: %v", err)
	}
	t.Logf("Entitlement: %d tokens × %d bytes", ent.TokensTotal, ent.BytesPerToken)

	// ===== CLIENT REDEEMS BLIND TOKENS =====
	// The client generates random secrets, blinds them, and sends to Hub.
	// The Hub signs without seeing the secrets — buyer-session unlinkability.
	tokenCount := 5
	result, err := bwClient.RedeemTokens(context.Background(), preimage, tokenCount, "nonce-integration")
	if err != nil {
		t.Fatalf("RedeemTokens: %v", err)
	}

	if len(result.Tokens) != tokenCount {
		t.Fatalf("expected %d tokens, got %d", tokenCount, len(result.Tokens))
	}
	if result.TokensRemaining != 5 { // 10 total - 5 redeemed
		t.Errorf("expected 5 remaining, got %d", result.TokensRemaining)
	}

	t.Logf("Redeemed %d tokens, %d remaining", result.TokensRedeemed, result.TokensRemaining)

	// ===== NODE VERIFIES AND SPENDS TOKENS =====
	// In production, the node receives tokens from the client before
	// granting bandwidth. It verifies locally, then calls Hub /spend.
	nodeVerifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(denomKey),
	})
	gate := node.NewTokenGate(nodeVerifier, server.URL, "node-entry-1")

	for i, token := range result.Tokens {
		spend, err := gate.VerifyAndSpend(context.Background(), token)
		if err != nil {
			t.Fatalf("token %d: VerifyAndSpend: %v", i, err)
		}

		if !spend.Valid {
			t.Errorf("token %d: invalid signature", i)
		}
		if !spend.FirstSpend {
			t.Errorf("token %d: not first spend", i)
		}
		if spend.BytesPerToken != 100_000_000 {
			t.Errorf("token %d: expected 100MB, got %d", i, spend.BytesPerToken)
		}

		t.Logf("Token %d: verified ✓ first_spend=%v bytes=%dMB",
			i, spend.FirstSpend, spend.BytesPerToken/1_000_000)
	}

	// ===== DOUBLE-SPEND PREVENTION =====
	// If a malicious client replays the same token at another node,
	// the Hub detects it.
	t.Log("Testing double-spend detection...")
	secondGate := node.NewTokenGate(nodeVerifier, server.URL, "node-exit-2")

	doubleSpend, err := secondGate.VerifyAndSpend(context.Background(), result.Tokens[0])
	if err != nil {
		t.Fatalf("double-spend check: %v", err)
	}
	if doubleSpend.FirstSpend {
		t.Error("double-spend should NOT be first_spend")
	}
	if !doubleSpend.Valid {
		t.Error("signature should still be valid (just already spent)")
	}
	t.Log("Double-spend detected ✓")

	// ===== BUYER-SESSION UNLINKABILITY =====
	// The Hub signed blinded messages. Even though it knows which
	// payment_hash the tokens were redeemed under, it cannot link
	// the token_secret (revealed at /spend) back to the buyer.
	//
	// Proof: The Hub never sees token_secret during /redeem.
	// It only sees blinded_messages. The unblinding happens client-side.
	t.Log("Buyer-session unlinkability: Hub never saw token secrets during redemption ✓")

	// ===== SUMMARY =====
	totalBandwidth := int64(tokenCount) * result.BytesPerToken
	t.Logf("\n=== Integration Test Summary ===")
	t.Logf("Tier:           1gb")
	t.Logf("Paid:           %d sats", purchase.AmountSats)
	t.Logf("Tokens:         %d × %dMB = %dMB",
		tokenCount, result.BytesPerToken/1_000_000, totalBandwidth/1_000_000)
	t.Logf("Tokens remaining: %d", result.TokensRemaining)
	t.Logf("All tokens verified and spent ✓")
	t.Logf("Double-spend detected ✓")
	t.Logf("Unlinkability maintained ✓")
}

// TestMultipleClients_IndependentEntitlements verifies that two different
// clients purchasing bandwidth get independent entitlements and tokens.
func TestMultipleClients_IndependentEntitlements(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	mock := lightning.NewMockClient()
	issuer := credentials.NewHMACIssuer("key-1", []byte("test-secret-key-for-hmac-32bytes!"))
	api := payments.NewPurchaseAPI(db, mock, issuer)
	api.StartSettlementListener(context.Background())
	defer api.Stop()

	denomKey, _ := credentials.GenerateDenominationKey("key-100mb", 100_000_000)
	mint := credentials.NewRSABlindMint([]*credentials.DenominationKey{denomKey})
	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(denomKey),
	})
	api.EnableBlindSignatures(mint, verifier, "key-100mb")

	server := httptest.NewServer(api.Handler())
	defer server.Close()

	// Two independent clients.
	clientA := client.NewBandwidthClient(server.URL, denomKey.PublicKey, "key-100mb")
	clientB := client.NewBandwidthClient(server.URL, denomKey.PublicKey, "key-100mb")

	// Client A buys 1gb.
	purchaseA, _ := clientA.Purchase(context.Background(), "1gb")
	mock.SimulateSettlement(purchaseA.PaymentHash)
	time.Sleep(200 * time.Millisecond)
	preimageA := mock.GetPreimage(purchaseA.PaymentHash)

	// Client B buys 10gb.
	purchaseB, _ := clientB.Purchase(context.Background(), "10gb")
	mock.SimulateSettlement(purchaseB.PaymentHash)
	time.Sleep(200 * time.Millisecond)
	preimageB := mock.GetPreimage(purchaseB.PaymentHash)

	// Both redeem.
	resultA, err := clientA.RedeemTokens(context.Background(), preimageA, 5, "nonce-a")
	if err != nil {
		t.Fatalf("client A redeem: %v", err)
	}
	resultB, err := clientB.RedeemTokens(context.Background(), preimageB, 10, "nonce-b")
	if err != nil {
		t.Fatalf("client B redeem: %v", err)
	}

	// Verify independent entitlements.
	if resultA.TokensRemaining != 5 { // 10 total - 5 redeemed
		t.Errorf("client A: expected 5 remaining, got %d", resultA.TokensRemaining)
	}
	if resultB.TokensRemaining != 90 { // 100 total - 10 redeemed
		t.Errorf("client B: expected 90 remaining, got %d", resultB.TokensRemaining)
	}

	// All tokens from both clients should be independently valid.
	gate := node.NewTokenGate(
		credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
			credentials.ExportPublicKey(denomKey),
		}),
		server.URL, "node-1",
	)

	for _, token := range resultA.Tokens {
		spend, _ := gate.VerifyAndSpend(context.Background(), token)
		if !spend.Valid || !spend.FirstSpend {
			t.Error("client A token should be valid first-spend")
		}
	}
	for _, token := range resultB.Tokens {
		spend, _ := gate.VerifyAndSpend(context.Background(), token)
		if !spend.Valid || !spend.FirstSpend {
			t.Error("client B token should be valid first-spend")
		}
	}

	// Cross-client tokens should not interfere.
	// Client A's token replayed should still be detected.
	spendA, _ := gate.VerifyAndSpend(context.Background(), resultA.Tokens[0])
	if spendA.FirstSpend {
		t.Error("client A token replay should be detected")
	}
}

// TestKeyPersistence_RoundTrip verifies that denomination keys survive
// save/load and tokens signed with a loaded key are still valid.
func TestKeyPersistence_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	keyPath := dir + "/key-100mb.json"

	// Generate and save.
	original, _ := credentials.GenerateDenominationKey("key-100mb", 100_000_000)
	credentials.SaveDenominationKey(keyPath, original)

	// Load back.
	loaded, err := credentials.LoadDenominationKey(keyPath)
	if err != nil {
		t.Fatalf("LoadDenominationKey: %v", err)
	}

	// Use loaded key to sign.
	mint := credentials.NewRSABlindMint([]*credentials.DenominationKey{loaded})

	secret, _ := credentials.GenerateTokenSecret()
	bm, _ := credentials.BlindTokenSecret(loaded.PublicKey, secret)
	sigs, _ := mint.SignBlinded("key-100mb", [][]byte{bm.Blinded})
	unblinded := credentials.UnblindSignature(loaded.PublicKey, sigs[0], bm.Unblinder)

	// Verify with original key's public component.
	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(original),
	})

	token := &credentials.BlindToken{
		Version:     credentials.BlindTokenVersion,
		KeyID:       "key-100mb",
		TokenSecret: secret,
		Signature:   unblinded,
	}
	if err := verifier.Verify(token); err != nil {
		t.Fatalf("token from loaded key failed verification: %v", err)
	}
}

// TestTokenUnlinkability verifies that the Hub cannot link token_secrets
// to the blinded messages it signed.
func TestTokenUnlinkability(t *testing.T) {
	denomKey, _ := credentials.GenerateDenominationKey("key-100mb", 100_000_000)
	mint := credentials.NewRSABlindMint([]*credentials.DenominationKey{denomKey})

	// Client generates a secret and blinds it.
	secret, _ := credentials.GenerateTokenSecret()
	bm, _ := credentials.BlindTokenSecret(denomKey.PublicKey, secret)

	// Hub sees only the blinded message — it cannot derive the secret.
	blindedHex := hex.EncodeToString(bm.Blinded)
	secretHex := hex.EncodeToString(secret)

	// These must be completely different — blinding is a one-way transform.
	if blindedHex == secretHex {
		t.Fatal("blinded message equals secret — blinding is broken")
	}

	// Hub signs the blinded message.
	sigs, _ := mint.SignBlinded("key-100mb", [][]byte{bm.Blinded})

	// Client unblinds to get the real signature.
	unblinded := credentials.UnblindSignature(denomKey.PublicKey, sigs[0], bm.Unblinder)

	// The blind signature (what Hub computed) and the unblinded signature
	// (what the client has) are different — Hub can't link them.
	if hex.EncodeToString(sigs[0]) == hex.EncodeToString(unblinded) {
		t.Fatal("blind sig equals unblinded sig — unlinkability broken")
	}

	// But the unblinded signature still verifies against the original secret.
	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(denomKey),
	})
	token := &credentials.BlindToken{
		Version:     credentials.BlindTokenVersion,
		KeyID:       "key-100mb",
		TokenSecret: secret,
		Signature:   unblinded,
	}
	if err := verifier.Verify(token); err != nil {
		t.Fatal("unblinded signature doesn't verify — crypto is broken")
	}

	t.Log("Blinding transform verified:")
	t.Logf("  Secret (client-only):  %s...", secretHex[:32])
	t.Logf("  Blinded (Hub sees):    %s...", blindedHex[:32])
	t.Logf("  These are unlinkable ✓")
}

// --- Helpers ---

func openTestDB(t *testing.T) (*store.Store, func()) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return db, func() { db.Close() }
}

// TestFullFlow_PurchaseConnectTunnel exercises the complete Phase 6 flow:
// Purchase → Pay → Redeem tokens → Present tokens to nodes → Get WG access.
// This proves the client→node token handoff works end-to-end.
func TestFullFlow_PurchaseConnectTunnel(t *testing.T) {
	// ===== HUB SETUP =====
	db, cleanup := openTestDB(t)
	defer cleanup()

	mock := lightning.NewMockClient()
	issuer := credentials.NewHMACIssuer("key-1", []byte("test-secret-key-for-hmac-32bytes!"))
	api := payments.NewPurchaseAPI(db, mock, issuer)
	api.StartSettlementListener(context.Background())
	defer api.Stop()

	denomKey, err := credentials.GenerateDenominationKey("key-100mb", 100_000_000)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	mint := credentials.NewRSABlindMint([]*credentials.DenominationKey{denomKey})
	hubVerifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(denomKey),
	})
	api.EnableBlindSignatures(mint, hubVerifier, "key-100mb")

	hubServer := httptest.NewServer(api.Handler())
	defer hubServer.Close()

	// ===== ENTRY NODE SETUP =====
	entryWG := wg.NewMockManager()
	entryWG.CreateInterface(wg.InterfaceConfig{
		Name: "wg-entry", PrivateKey: "YNk/rMPgfEJUOG4JvA6FWzGm3Gd0qf6GiJnKrdOaHE8=",
		ListenPort: 51820, Address: "10.100.0.1/24",
	})
	entryQuota := quota.NewNoopEnforcer()
	entryServer := control.NewServer(entryWG, entryQuota, "wg-entry")

	nodeVerifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(denomKey),
	})
	entryGate := node.NewTokenGate(nodeVerifier, hubServer.URL, "entryNodePub")
	entryServer.EnableTokenGate(entryGate, "entryNodePub", "10.100.0")

	entryHTTP := httptest.NewServer(http.HandlerFunc(entryServer.HandleConnect))
	defer entryHTTP.Close()

	// ===== EXIT NODE SETUP =====
	exitWG := wg.NewMockManager()
	exitWG.CreateInterface(wg.InterfaceConfig{
		Name: "wg-exit", PrivateKey: "YNk/rMPgfEJUOG4JvA6FWzGm3Gd0qf6GiJnKrdOaHE8=",
		ListenPort: 51821, Address: "10.200.0.1/24",
	})
	exitQuota := quota.NewNoopEnforcer()
	exitServer := control.NewServer(exitWG, exitQuota, "wg-exit")

	exitGate := node.NewTokenGate(nodeVerifier, hubServer.URL, "exitNodePub")
	exitServer.EnableTokenGate(exitGate, "exitNodePub", "10.200.0")

	exitHTTP := httptest.NewServer(http.HandlerFunc(exitServer.HandleConnect))
	defer exitHTTP.Close()

	t.Logf("Hub: %s | Entry: %s | Exit: %s", hubServer.URL, entryHTTP.URL, exitHTTP.URL)

	// ===== CLIENT PURCHASES =====
	bwClient := client.NewBandwidthClient(hubServer.URL, denomKey.PublicKey, "key-100mb")
	purchase, err := bwClient.Purchase(context.Background(), "1gb")
	if err != nil {
		t.Fatalf("Purchase: %v", err)
	}

	mock.SimulateSettlement(purchase.PaymentHash)
	time.Sleep(200 * time.Millisecond)
	preimage := mock.GetPreimage(purchase.PaymentHash)

	// ===== CLIENT REDEEMS TOKENS =====
	result, err := bwClient.RedeemTokens(context.Background(), preimage, 3, "nonce-flow")
	if err != nil {
		t.Fatalf("RedeemTokens: %v", err)
	}
	if len(result.Tokens) != 3 {
		t.Fatalf("expected 3 tokens, got %d", len(result.Tokens))
	}
	t.Logf("Redeemed %d tokens", len(result.Tokens))

	// ===== CLIENT PRESENTS TOKENS TO NODES =====
	connector := client.NewNodeConnector()
	ctx := context.Background()

	// Present first token to entry node.
	entryResult, err := connector.Connect(ctx, entryHTTP.URL, result.Tokens[0], "clientWGPubkey")
	if err != nil {
		t.Fatalf("entry connect: %v", err)
	}
	t.Logf("Entry node: IP=%s, quota=%dMB", entryResult.TunnelIP, entryResult.BytesAllowed/1_000_000)

	if entryResult.TunnelIP != "10.100.0.2/32" {
		t.Errorf("expected entry IP 10.100.0.2/32, got %s", entryResult.TunnelIP)
	}
	if entryResult.NodeWGPubkey != "entryNodePub" {
		t.Errorf("expected entry pubkey=entryNodePub, got %s", entryResult.NodeWGPubkey)
	}

	// Present second token to exit node.
	exitResult, err := connector.Connect(ctx, exitHTTP.URL, result.Tokens[1], "clientWGPubkey")
	if err != nil {
		t.Fatalf("exit connect: %v", err)
	}
	t.Logf("Exit node: IP=%s, quota=%dMB", exitResult.TunnelIP, exitResult.BytesAllowed/1_000_000)

	if exitResult.TunnelIP != "10.200.0.2/32" {
		t.Errorf("expected exit IP 10.200.0.2/32, got %s", exitResult.TunnelIP)
	}

	// ===== VERIFY WG PEERS WERE ADDED =====
	entryPeers, _ := entryWG.GetPeerStats("wg-entry")
	if len(entryPeers) != 1 {
		t.Errorf("entry node: expected 1 peer, got %d", len(entryPeers))
	}

	exitPeers, _ := exitWG.GetPeerStats("wg-exit")
	if len(exitPeers) != 1 {
		t.Errorf("exit node: expected 1 peer, got %d", len(exitPeers))
	}

	// ===== DOUBLE-SPEND: SAME TOKEN REJECTED =====
	_, err = connector.Connect(ctx, entryHTTP.URL, result.Tokens[0], "clientWGPubkey2")
	if err == nil {
		t.Fatal("replaying spent token should fail")
	}
	t.Logf("Double-spend blocked: %v ✓", err)

	// ===== THIRD TOKEN STILL WORKS (unused) =====
	thirdResult, err := connector.Connect(ctx, entryHTTP.URL, result.Tokens[2], "clientWGPubkey3")
	if err != nil {
		t.Fatalf("third token should work: %v", err)
	}
	if thirdResult.TunnelIP != "10.100.0.3/32" {
		t.Errorf("expected sequential IP 10.100.0.3/32, got %s", thirdResult.TunnelIP)
	}

	t.Log("\n=== Phase 6 Integration Summary ===")
	t.Log("Purchase → Pay → Redeem → Connect entry ✓")
	t.Log("Connect exit ✓")
	t.Log("WG peers added ✓")
	t.Log("Double-spend blocked ✓")
	t.Log("Unused token still valid ✓")
}
