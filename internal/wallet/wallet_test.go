package wallet_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Radi-Labs/ARFL/internal/discovery"
	"github.com/Radi-Labs/ARFL/internal/ecash"
	"github.com/Radi-Labs/ARFL/internal/lightning"
	"github.com/Radi-Labs/ARFL/internal/store"
	"github.com/Radi-Labs/ARFL/internal/wallet"
	"github.com/elnosh/gonuts/cashu"
)

// testHub spins up a real Cashu mint behind the hub's HTTP API, backed by a
// mock Lightning node. Testing against the actual mint is the point: it proves
// the client's blinding is compatible with the hub's BDHKE implementation
// rather than merely self-consistent.
type testHub struct {
	server *httptest.Server
	mint   *ecash.Mint
	ln     *lightning.MockClient
}

func newTestHub(t *testing.T) *testHub {
	t.Helper()

	db, err := store.Open(t.TempDir() + "/hub.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	seed := []byte("arfl-wallet-test-seed-000000000000000000")
	mint, err := ecash.NewMint(db, seed)
	if err != nil {
		t.Fatalf("create mint: %v", err)
	}

	ln := lightning.NewMockClient()

	api := discovery.NewDiscoveryAPI(discovery.NewNodeIndex(nil))
	api.SetLightningClient(ln)
	api.SetMint(mint, db)
	// The wallet polls quote status, which would otherwise trip the default
	// 30-requests-per-minute limit during tests.
	api.SetRateLimit(0, 0)

	server := httptest.NewServer(api.Handler())
	t.Cleanup(server.Close)

	return &testHub{server: server, mint: mint, ln: ln}
}

// newTestWallet returns a wallet pointed at hub, backed by a temporary vault.
func newTestWallet(t *testing.T, hub *testHub) *wallet.Wallet {
	t.Helper()

	client, err := wallet.NewMintClient(hub.server.URL)
	if err != nil {
		t.Fatalf("create mint client: %v", err)
	}

	store, err := wallet.OpenProofStore(t.TempDir()+"/tokens.json", "correct horse battery staple")
	if err != nil {
		t.Fatalf("open proof store: %v", err)
	}

	w, err := wallet.NewWallet(client, store)
	if err != nil {
		t.Fatalf("create wallet: %v", err)
	}
	return w
}

// mintPaid runs the full purchase path and returns the resulting proofs.
func mintPaid(t *testing.T, hub *testHub, w *wallet.Wallet, amount uint64) cashu.Proofs {
	t.Helper()
	ctx := context.Background()

	quote, err := w.RequestQuote(ctx, amount)
	if err != nil {
		t.Fatalf("request quote: %v", err)
	}
	if err := hub.ln.SimulateSettlement(quote.PaymentHash); err != nil {
		t.Fatalf("simulate settlement: %v", err)
	}

	paid, err := w.AwaitPayment(ctx, quote.ID, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("await payment: %v", err)
	}

	proofs, err := w.Mint(ctx, paid)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return proofs
}

// TestMintRoundTripProducesRedeemableProofs is the central guarantee of this
// package: proofs minted client-side must verify against the hub's mint.
func TestMintRoundTripProducesRedeemableProofs(t *testing.T) {
	hub := newTestHub(t)
	w := newTestWallet(t, hub)

	const amount = 100
	proofs := mintPaid(t, hub, w, amount)

	if got := proofs.Amount(); got != amount {
		t.Fatalf("minted %d sats, want %d", got, amount)
	}

	// The mint is the authority on validity. If this passes, the client's
	// blinding, the hub's signing, and the client's unblinding all agree.
	if _, err := hub.mint.VerifyProofs(proofs); err != nil {
		t.Fatalf("hub rejected client-minted proofs: %v", err)
	}
}

// TestMintedSecretsAreUnpredictable guards the unlinkability property. If a
// secret were derived from its blinding factor, anyone learning the secret
// could unblind the signature and link it to the payment.
func TestMintedSecretsAreUnpredictable(t *testing.T) {
	hub := newTestHub(t)
	w := newTestWallet(t, hub)

	proofs := mintPaid(t, hub, w, 128)

	seen := make(map[string]struct{}, len(proofs))
	for _, proof := range proofs {
		if proof.Secret == "" {
			t.Fatal("proof has an empty secret")
		}
		// 32 random bytes, hex encoded.
		if len(proof.Secret) != 64 {
			t.Fatalf("secret length %d, want 64 hex chars", len(proof.Secret))
		}
		if _, dup := seen[proof.Secret]; dup {
			t.Fatalf("secret %s reused across proofs", proof.Secret)
		}
		seen[proof.Secret] = struct{}{}
	}
}

// TestMintRejectedBeforePayment confirms the hub will not issue tokens for an
// unpaid quote, so a client cannot mint bandwidth it has not bought.
func TestMintRejectedBeforePayment(t *testing.T) {
	hub := newTestHub(t)
	w := newTestWallet(t, hub)
	ctx := context.Background()

	quote, err := w.RequestQuote(ctx, 64)
	if err != nil {
		t.Fatalf("request quote: %v", err)
	}

	if _, err := w.Mint(ctx, quote); err == nil {
		t.Fatal("expected mint to fail for an unpaid quote")
	}

	balance, err := w.Balance()
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != 0 {
		t.Fatalf("balance is %d after failed mint, want 0", balance)
	}
}

// TestBalanceReflectsMintedProofs checks that minting accumulates spendable value.
func TestBalanceReflectsMintedProofs(t *testing.T) {
	hub := newTestHub(t)
	w := newTestWallet(t, hub)

	mintPaid(t, hub, w, 64)
	mintPaid(t, hub, w, 32)

	balance, err := w.Balance()
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != 96 {
		t.Fatalf("balance is %d, want 96", balance)
	}
}

// TestReserveRemovesProofsFromBalance ensures reserved proofs cannot be handed
// out twice, which a node would reject as a double-spend.
func TestReserveRemovesProofsFromBalance(t *testing.T) {
	hub := newTestHub(t)
	w := newTestWallet(t, hub)

	mintPaid(t, hub, w, 100)

	reserved, err := w.Reserve(context.Background(), 64)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if reserved.Amount() < 64 {
		t.Fatalf("reserved %d sats, want at least 64", reserved.Amount())
	}

	balance, err := w.Balance()
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != 100-reserved.Amount() {
		t.Fatalf("balance is %d, want %d", balance, 100-reserved.Amount())
	}

	// Reserved proofs must still be valid at the mint — reserving is a local
	// bookkeeping step, not a spend.
	if _, err := hub.mint.VerifyProofs(reserved); err != nil {
		t.Fatalf("reserved proofs rejected by mint: %v", err)
	}
}

// TestReleaseRestoresReservedProofs covers the failed-connection path: the user
// must not lose bandwidth they paid for when a node is unreachable.
func TestReleaseRestoresReservedProofs(t *testing.T) {
	hub := newTestHub(t)
	w := newTestWallet(t, hub)

	mintPaid(t, hub, w, 100)

	reserved, err := w.Reserve(context.Background(), 64)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := w.Release(reserved); err != nil {
		t.Fatalf("release: %v", err)
	}

	balance, err := w.Balance()
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != 100 {
		t.Fatalf("balance is %d after release, want 100", balance)
	}
}

// TestReserveSwapsForExactChange guards against silently burning the surplus.
// Minting 128 sats yields a single 128-denomination proof, so reserving 32
// without a swap would hand the whole 128 to a node and lose 96 sats.
func TestReserveSwapsForExactChange(t *testing.T) {
	hub := newTestHub(t)
	w := newTestWallet(t, hub)

	mintPaid(t, hub, w, 128)

	reserved, err := w.Reserve(context.Background(), 32)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if reserved.Amount() != 32 {
		t.Fatalf("reserved %d sats, want exactly 32", reserved.Amount())
	}

	balance, err := w.Balance()
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != 96 {
		t.Fatalf("balance is %d, want 96 in change", balance)
	}

	// Both the payment and the change must be freshly signed, spendable proofs.
	if _, err := hub.mint.VerifyProofs(reserved); err != nil {
		t.Fatalf("payment proofs rejected by mint: %v", err)
	}
	change, err := w.Proofs()
	if err != nil {
		t.Fatalf("list change: %v", err)
	}
	if _, err := hub.mint.VerifyProofs(change); err != nil {
		t.Fatalf("change proofs rejected by mint: %v", err)
	}
}

// TestReserveBeyondBalanceFails verifies the wallet refuses to over-commit.
func TestReserveBeyondBalanceFails(t *testing.T) {
	hub := newTestHub(t)
	w := newTestWallet(t, hub)

	mintPaid(t, hub, w, 32)

	_, err := w.Reserve(context.Background(), 1000)
	if !errors.Is(err, wallet.ErrInsufficientBalance) {
		t.Fatalf("got %v, want ErrInsufficientBalance", err)
	}
}

// TestProofsSurviveReopen confirms the vault round-trips through disk, so a
// user's tokens are still there after restarting the app.
func TestProofsSurviveReopen(t *testing.T) {
	hub := newTestHub(t)

	dir := t.TempDir()
	path := dir + "/tokens.json"
	const passphrase = "correct horse battery staple"

	client, err := wallet.NewMintClient(hub.server.URL)
	if err != nil {
		t.Fatalf("create mint client: %v", err)
	}

	first, err := wallet.OpenProofStore(path, passphrase)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	w, err := wallet.NewWallet(client, first)
	if err != nil {
		t.Fatalf("create wallet: %v", err)
	}

	minted := mintPaid(t, hub, w, 100)

	reopened, err := wallet.OpenProofStore(path, passphrase)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	restored, err := wallet.NewWallet(client, reopened)
	if err != nil {
		t.Fatalf("recreate wallet: %v", err)
	}

	balance, err := restored.Balance()
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != minted.Amount() {
		t.Fatalf("balance after reopen is %d, want %d", balance, minted.Amount())
	}
}
