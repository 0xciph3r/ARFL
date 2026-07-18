// Package testutil provides shared test helpers for ARFL integration tests.
package testutil

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/Radi-Labs/ARFL/internal/credentials"
	"github.com/Radi-Labs/ARFL/internal/lightning"
	"github.com/Radi-Labs/ARFL/internal/payments"
	"github.com/Radi-Labs/ARFL/internal/store"
)

// TestHub is an in-process hub with blind signatures enabled.
type TestHub struct {
	Server   *httptest.Server
	Mock     *lightning.MockClient
	DenomKey *credentials.DenominationKey
}

// SetupTestHub creates a full hub environment for tests.
func SetupTestHub(t *testing.T) *TestHub {
	t.Helper()

	dir := t.TempDir()
	db, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	secret := []byte("test-secret-key-for-hmac-32bytes!")
	issuer := credentials.NewHMACIssuer("key-1", secret)

	mock := lightning.NewMockClient()

	api := payments.NewPurchaseAPI(db, mock, issuer)
	api.StartSettlementListener(context.Background())
	t.Cleanup(func() { api.Stop() })

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

	return &TestHub{
		Server:   server,
		Mock:     mock,
		DenomKey: denomKey,
	}
}
