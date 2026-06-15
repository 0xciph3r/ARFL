package client

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Radi-Labs/ARFL/internal/credentials"
	"github.com/Radi-Labs/ARFL/internal/lightning"
	"github.com/Radi-Labs/ARFL/internal/payments"
	"github.com/Radi-Labs/ARFL/internal/store"
)

// testHub spins up an in-process hub with blind sigs enabled.
type testHub struct {
	server   *httptest.Server
	mock     *lightning.MockClient
	denomKey *credentials.DenominationKey
}

func setupTestHub(t *testing.T) *testHub {
	t.Helper()

	// Database.
	dir := t.TempDir()
	db, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Credentials.
	secret := []byte("test-secret-key-for-hmac-32bytes!")
	issuer := credentials.NewHMACIssuer("key-1", secret)

	// Lightning mock.
	mock := lightning.NewMockClient()

	// Payment API.
	api := payments.NewPurchaseAPI(db, mock, issuer)
	api.StartSettlementListener(context.Background())
	t.Cleanup(func() { api.Stop() })

	// Blind signatures.
	denomKey, err := credentials.GenerateDenominationKey("key-100mb", 100_000_000)
	if err != nil {
		t.Fatalf("GenerateDenominationKey: %v", err)
	}

	mint := credentials.NewRSABlindMint([]*credentials.DenominationKey{denomKey})
	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(denomKey),
	})
	api.EnableBlindSignatures(mint, verifier, "key-100mb")

	server := httptest.NewServer(api.Handler())
	t.Cleanup(func() { server.Close() })

	return &testHub{
		server:   server,
		mock:     mock,
		denomKey: denomKey,
	}
}

func TestBandwidthClient_Purchase(t *testing.T) {
	hub := setupTestHub(t)
	client := NewBandwidthClient(hub.server.URL, hub.denomKey.PublicKey, "key-100mb")

	result, err := client.Purchase(context.Background(), "1gb")
	if err != nil {
		t.Fatalf("Purchase: %v", err)
	}

	if result.PaymentHash == "" {
		t.Error("expected non-empty payment_hash")
	}
	if result.PaymentRequest == "" {
		t.Error("expected non-empty payment_request")
	}
	if result.AmountSats != 500 {
		t.Errorf("expected 500 sats, got %d", result.AmountSats)
	}
	if result.Tier != "1gb" {
		t.Errorf("expected tier 1gb, got %s", result.Tier)
	}
}

func TestBandwidthClient_Purchase_UnknownTier(t *testing.T) {
	hub := setupTestHub(t)
	client := NewBandwidthClient(hub.server.URL, hub.denomKey.PublicKey, "key-100mb")

	_, err := client.Purchase(context.Background(), "999gb")
	if err == nil {
		t.Fatal("expected error for unknown tier")
	}
}

func TestBandwidthClient_GetPurchaseStatus(t *testing.T) {
	hub := setupTestHub(t)
	client := NewBandwidthClient(hub.server.URL, hub.denomKey.PublicKey, "key-100mb")

	purchase, _ := client.Purchase(context.Background(), "1gb")

	status, err := client.GetPurchaseStatus(context.Background(), purchase.PaymentHash)
	if err != nil {
		t.Fatalf("GetPurchaseStatus: %v", err)
	}
	if status.Status != "open" {
		t.Errorf("expected open, got %s", status.Status)
	}
}

func TestBandwidthClient_WaitForSettlement(t *testing.T) {
	hub := setupTestHub(t)
	client := NewBandwidthClient(hub.server.URL, hub.denomKey.PublicKey, "key-100mb")

	purchase, _ := client.Purchase(context.Background(), "1gb")

	// Settle after a short delay.
	go func() {
		time.Sleep(100 * time.Millisecond)
		hub.mock.SimulateSettlement(purchase.PaymentHash)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	status, err := client.WaitForSettlement(ctx, purchase.PaymentHash, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("WaitForSettlement: %v", err)
	}
	if status.Status != "settled" {
		t.Errorf("expected settled, got %s", status.Status)
	}
}

func TestBandwidthClient_FullFlow(t *testing.T) {
	hub := setupTestHub(t)
	client := NewBandwidthClient(hub.server.URL, hub.denomKey.PublicKey, "key-100mb")

	// 1. Purchase.
	purchase, err := client.Purchase(context.Background(), "1gb")
	if err != nil {
		t.Fatalf("Purchase: %v", err)
	}

	// 2. Simulate payment + settlement.
	hub.mock.SimulateSettlement(purchase.PaymentHash)
	time.Sleep(200 * time.Millisecond) // let settlement listener process

	// 3. Get preimage (in real life, the wallet returns this after payment).
	preimage := hub.mock.GetPreimage(purchase.PaymentHash)
	if preimage == "" {
		t.Fatal("no preimage from mock")
	}

	// 4. Redeem 3 tokens.
	result, err := client.RedeemTokens(context.Background(), preimage, 3, "nonce-full-flow")
	if err != nil {
		t.Fatalf("RedeemTokens: %v", err)
	}

	if len(result.Tokens) != 3 {
		t.Fatalf("expected 3 tokens, got %d", len(result.Tokens))
	}
	if result.BytesPerToken != 100_000_000 {
		t.Errorf("expected 100MB per token, got %d", result.BytesPerToken)
	}
	if result.TokensRedeemed != 3 {
		t.Errorf("expected 3 redeemed, got %d", result.TokensRedeemed)
	}
	if result.TokensRemaining != 7 { // 10 total - 3 redeemed
		t.Errorf("expected 7 remaining, got %d", result.TokensRemaining)
	}

	// 5. Verify each token is cryptographically valid.
	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(hub.denomKey),
	})
	for i, token := range result.Tokens {
		if err := verifier.Verify(token); err != nil {
			t.Errorf("token %d verification failed: %v", i, err)
		}
		if token.KeyID != "key-100mb" {
			t.Errorf("token %d: expected key-100mb, got %s", i, token.KeyID)
		}
		if token.TokenID() == "" {
			t.Errorf("token %d: empty TokenID", i)
		}
	}
}

func TestBandwidthClient_RedeemTokens_InsufficientEntitlement(t *testing.T) {
	hub := setupTestHub(t)
	client := NewBandwidthClient(hub.server.URL, hub.denomKey.PublicKey, "key-100mb")

	// Purchase + settle.
	purchase, _ := client.Purchase(context.Background(), "1gb")
	hub.mock.SimulateSettlement(purchase.PaymentHash)
	time.Sleep(200 * time.Millisecond)

	preimage := hub.mock.GetPreimage(purchase.PaymentHash)

	// Try to redeem more than the entitlement (1gb tier = 10 tokens).
	_, err := client.RedeemTokens(context.Background(), preimage, 11, "nonce-overdraw")
	if err == nil {
		t.Fatal("expected error for overdraw")
	}
}

func TestBandwidthClient_RedeemTokens_DifferentNonces(t *testing.T) {
	hub := setupTestHub(t)
	client := NewBandwidthClient(hub.server.URL, hub.denomKey.PublicKey, "key-100mb")

	purchase, _ := client.Purchase(context.Background(), "1gb")
	hub.mock.SimulateSettlement(purchase.PaymentHash)
	time.Sleep(200 * time.Millisecond)

	preimage := hub.mock.GetPreimage(purchase.PaymentHash)

	// First redemption: 2 tokens.
	result1, err := client.RedeemTokens(context.Background(), preimage, 2, "nonce-1")
	if err != nil {
		t.Fatalf("first RedeemTokens: %v", err)
	}
	if result1.TokensRemaining != 8 {
		t.Errorf("expected 8 remaining after first, got %d", result1.TokensRemaining)
	}

	// Second redemption: 3 more tokens with different nonce.
	result2, err := client.RedeemTokens(context.Background(), preimage, 3, "nonce-2")
	if err != nil {
		t.Fatalf("second RedeemTokens: %v", err)
	}
	if result2.TokensRemaining != 5 {
		t.Errorf("expected 5 remaining after second, got %d", result2.TokensRemaining)
	}
	if len(result2.Tokens) != 3 {
		t.Errorf("expected 3 tokens, got %d", len(result2.Tokens))
	}
}
