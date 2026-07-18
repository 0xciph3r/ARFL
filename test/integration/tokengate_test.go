package integration

import (
	"context"
	"testing"
	"time"

	"github.com/Radi-Labs/ARFL/internal/client"
	"github.com/Radi-Labs/ARFL/internal/credentials"
	"github.com/Radi-Labs/ARFL/internal/node"
	"github.com/Radi-Labs/ARFL/test/testutil"
)

func TestTokenGate_VerifyAndSpend_ValidToken(t *testing.T) {
	hub := testutil.SetupTestHub(t)

	// Create a node-side verifier with the hub's public key.
	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(hub.DenomKey),
	})
	gate := node.NewTokenGate(verifier, hub.Server.URL, "test-node-1")

	// Get a valid token through the full flow.
	bwClient := client.NewBandwidthClient(hub.Server.URL, hub.DenomKey.PublicKey, "key-100mb")
	purchase, _ := bwClient.Purchase(context.Background(), "1gb")
	hub.Mock.SimulateSettlement(purchase.PaymentHash)
	time.Sleep(200 * time.Millisecond)
	preimage := hub.Mock.GetPreimage(purchase.PaymentHash)

	result, err := bwClient.RedeemTokens(context.Background(), preimage, 1, "nonce-gate-test")
	if err != nil {
		t.Fatalf("RedeemTokens: %v", err)
	}

	// Node verifies and spends the token.
	spend, err := gate.VerifyAndSpend(context.Background(), result.Tokens[0])
	if err != nil {
		t.Fatalf("VerifyAndSpend: %v", err)
	}

	if !spend.Valid {
		t.Error("expected valid token")
	}
	if !spend.FirstSpend {
		t.Error("expected first_spend=true")
	}
	if spend.BytesPerToken != 100_000_000 {
		t.Errorf("expected 100MB, got %d", spend.BytesPerToken)
	}
}

func TestTokenGate_VerifyAndSpend_DoubleSpend(t *testing.T) {
	hub := testutil.SetupTestHub(t)

	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(hub.DenomKey),
	})
	gate := node.NewTokenGate(verifier, hub.Server.URL, "test-node-1")

	bwClient := client.NewBandwidthClient(hub.Server.URL, hub.DenomKey.PublicKey, "key-100mb")
	purchase, _ := bwClient.Purchase(context.Background(), "1gb")
	hub.Mock.SimulateSettlement(purchase.PaymentHash)
	time.Sleep(200 * time.Millisecond)
	preimage := hub.Mock.GetPreimage(purchase.PaymentHash)

	result, _ := bwClient.RedeemTokens(context.Background(), preimage, 1, "nonce-double")

	// First spend.
	spend1, _ := gate.VerifyAndSpend(context.Background(), result.Tokens[0])
	if !spend1.FirstSpend {
		t.Fatal("first spend should be true")
	}

	// Second spend — same token.
	spend2, err := gate.VerifyAndSpend(context.Background(), result.Tokens[0])
	if err != nil {
		t.Fatalf("VerifyAndSpend: %v", err)
	}

	if spend2.FirstSpend {
		t.Error("second spend should have first_spend=false")
	}
	if !spend2.Valid {
		t.Error("token should still be valid (just already spent)")
	}
}

func TestTokenGate_VerifyAndSpend_InvalidSignature(t *testing.T) {
	hub := testutil.SetupTestHub(t)

	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(hub.DenomKey),
	})
	gate := node.NewTokenGate(verifier, hub.Server.URL, "test-node-1")

	// Craft a token with a bogus signature.
	secret, _ := credentials.GenerateTokenSecret()
	fakeToken := &credentials.BlindToken{
		Version:     credentials.BlindTokenVersion,
		KeyID:       "key-100mb",
		TokenSecret: secret,
		Signature:   make([]byte, 256), // garbage
	}

	spend, err := gate.VerifyAndSpend(context.Background(), fakeToken)
	if err != nil {
		t.Fatalf("VerifyAndSpend should not error on invalid sig: %v", err)
	}

	if spend.Valid {
		t.Error("expected valid=false for bogus signature")
	}
}

func TestTokenGate_VerifyOnly_ValidToken(t *testing.T) {
	hub := testutil.SetupTestHub(t)

	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(hub.DenomKey),
	})
	gate := node.NewTokenGate(verifier, hub.Server.URL, "test-node-1")

	// Get a valid token.
	bwClient := client.NewBandwidthClient(hub.Server.URL, hub.DenomKey.PublicKey, "key-100mb")
	purchase, _ := bwClient.Purchase(context.Background(), "1gb")
	hub.Mock.SimulateSettlement(purchase.PaymentHash)
	time.Sleep(200 * time.Millisecond)
	preimage := hub.Mock.GetPreimage(purchase.PaymentHash)

	result, _ := bwClient.RedeemTokens(context.Background(), preimage, 1, "nonce-verify-only")

	// VerifyOnly — no Hub call, assumes first_spend.
	spend, err := gate.VerifyOnly(result.Tokens[0])
	if err != nil {
		t.Fatalf("VerifyOnly: %v", err)
	}

	if !spend.Valid {
		t.Error("expected valid")
	}
	if !spend.FirstSpend {
		t.Error("expected first_spend=true (assumed in offline mode)")
	}
	if spend.BytesPerToken != 100_000_000 {
		t.Errorf("expected 100MB, got %d", spend.BytesPerToken)
	}
}

func TestTokenGate_VerifyOnly_InvalidSignature(t *testing.T) {
	hub := testutil.SetupTestHub(t)

	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(hub.DenomKey),
	})
	gate := node.NewTokenGate(verifier, hub.Server.URL, "test-node-1")

	fakeToken := &credentials.BlindToken{
		Version:     credentials.BlindTokenVersion,
		KeyID:       "key-100mb",
		TokenSecret: make([]byte, 32),
		Signature:   make([]byte, 256),
	}

	spend, err := gate.VerifyOnly(fakeToken)
	if err != nil {
		t.Fatalf("VerifyOnly should not error: %v", err)
	}
	if spend.Valid {
		t.Error("expected valid=false")
	}
}

func TestTokenGate_FullFlow_PurchaseRedeemSpend(t *testing.T) {
	hub := testutil.SetupTestHub(t)

	// Client side: purchase and redeem.
	bwClient := client.NewBandwidthClient(hub.Server.URL, hub.DenomKey.PublicKey, "key-100mb")
	purchase, _ := bwClient.Purchase(context.Background(), "1gb")
	hub.Mock.SimulateSettlement(purchase.PaymentHash)
	time.Sleep(200 * time.Millisecond)
	preimage := hub.Mock.GetPreimage(purchase.PaymentHash)

	redeemResult, err := bwClient.RedeemTokens(context.Background(), preimage, 3, "nonce-full-gate")
	if err != nil {
		t.Fatalf("RedeemTokens: %v", err)
	}

	// Node side: verify and spend each token.
	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(hub.DenomKey),
	})
	gate := node.NewTokenGate(verifier, hub.Server.URL, "node-1")

	for i, token := range redeemResult.Tokens {
		spend, err := gate.VerifyAndSpend(context.Background(), token)
		if err != nil {
			t.Fatalf("token %d: VerifyAndSpend: %v", i, err)
		}
		if !spend.Valid {
			t.Errorf("token %d: expected valid", i)
		}
		if !spend.FirstSpend {
			t.Errorf("token %d: expected first_spend", i)
		}
		if spend.BytesPerToken != 100_000_000 {
			t.Errorf("token %d: expected 100MB, got %d", i, spend.BytesPerToken)
		}
	}
}
