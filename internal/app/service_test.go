package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Radi-Labs/ARFL/internal/app"
	"github.com/Radi-Labs/ARFL/internal/discovery"
	"github.com/Radi-Labs/ARFL/internal/ecash"
	"github.com/Radi-Labs/ARFL/internal/lightning"
	"github.com/Radi-Labs/ARFL/internal/store"
	"github.com/Radi-Labs/ARFL/pkg/types"
	"github.com/elnosh/gonuts/cashu"
)

// testHub runs the real hub API (real Cashu mint, mock Lightning) but serves a
// fixed node list. Real node announcements require signed Nostr events with
// hub attestations, which would test the discovery pipeline rather than the
// service; the mint stays real because proof compatibility is what matters.
type testHub struct {
	server *httptest.Server
	ln     *lightning.MockClient

	mu    sync.Mutex
	nodes []nodeEntry
}

type nodeEntry struct {
	Info   types.NodeInfo `json:"info"`
	Online bool           `json:"online"`
}

func newTestHub(t *testing.T) *testHub {
	t.Helper()

	db, err := store.Open(t.TempDir() + "/hub.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	mint, err := ecash.NewMint(db, []byte("arfl-app-test-seed-0000000000000000000000"))
	if err != nil {
		t.Fatalf("create mint: %v", err)
	}

	ln := lightning.NewMockClient()

	api := discovery.NewDiscoveryAPI(discovery.NewNodeIndex(nil))
	api.SetLightningClient(ln)
	api.SetMint(mint, db)
	// The wallet polls quote status, which would trip the default rate limit.
	api.SetRateLimit(0, 0)

	hub := &testHub{ln: ln}

	mux := http.NewServeMux()
	mux.HandleFunc("/nodes", func(w http.ResponseWriter, r *http.Request) {
		hub.mu.Lock()
		nodes := append([]nodeEntry(nil), hub.nodes...)
		hub.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"nodes": nodes,
			"count": len(nodes),
		})
	})
	mux.Handle("/", api.Handler())

	hub.server = httptest.NewServer(mux)
	t.Cleanup(hub.server.Close)

	return hub
}

// addNode starts a node that redeems presented proofs at the hub, mirroring
// what a real arfl-node does, and publishes it in the hub's node list.
func (h *testHub) addNode(t *testing.T, id string, role types.NodeRole) *testNode {
	t.Helper()

	node := &testNode{id: id, hubURL: h.server.URL, wgPubkey: id + "-wg-pubkey"}
	node.server = httptest.NewServer(http.HandlerFunc(node.handleConnect))
	t.Cleanup(node.server.Close)

	h.mu.Lock()
	h.nodes = append(h.nodes, nodeEntry{
		Info: types.NodeInfo{
			ID:          id,
			NostrPubkey: id + "-nostr-pubkey",
			WGPubkey:    node.wgPubkey,
			Endpoint:    id + ".example:51820",
			ConnectURL:  node.server.URL,
			Role:        role,
		},
		Online: true,
	})
	h.mu.Unlock()

	return node
}

// testNode is a stand-in arfl-node: it verifies proofs by redeeming them at
// the hub, so double-spends are rejected by the real mint.
type testNode struct {
	id       string
	wgPubkey string
	hubURL   string
	server   *httptest.Server

	mu        sync.Mutex
	rejectAll bool
	connects  int
}

func (n *testNode) setReject(reject bool) {
	n.mu.Lock()
	n.rejectAll = reject
	n.mu.Unlock()
}

func (n *testNode) connectCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.connects
}

func (n *testNode) handleConnect(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/connect") {
		http.NotFound(w, r)
		return
	}

	n.mu.Lock()
	n.connects++
	reject := n.rejectAll
	n.mu.Unlock()

	if reject {
		w.WriteHeader(http.StatusPaymentRequired)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "node offline"})
		return
	}

	var req struct {
		Proofs   cashu.Proofs `json:"proofs"`
		WGPubkey string       `json:"wg_pubkey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	bytesAllowed, err := n.redeem(r.Context(), req.Proofs)
	if err != nil {
		w.WriteHeader(http.StatusPaymentRequired)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"tunnel_ip":      "10.100.0.2/32",
		"node_wg_pubkey": n.wgPubkey,
		"bytes_allowed":  bytesAllowed,
	})
}

func (n *testNode) redeem(ctx context.Context, proofs cashu.Proofs) (int64, error) {
	body, err := json.Marshal(map[string]any{
		"proofs":      proofs,
		"node_pubkey": n.id + "-nostr-pubkey",
	})
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.hubURL+"/v1/redeem", strings.NewReader(string(body)))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error  string `json:"error"`
			Detail string `json:"detail"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errResp)
		msg := errResp.Error
		if msg == "" {
			msg = errResp.Detail
		}
		return 0, fmt.Errorf("hub rejected proofs (%d): %s", resp.StatusCode, msg)
	}

	var ok struct {
		BytesAllowed int64 `json:"bytes_allowed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ok); err != nil {
		return 0, err
	}
	return ok.BytesAllowed, nil
}

// fakeTunnel records bring-up without touching the network.
type fakeTunnel struct {
	mu           sync.Mutex
	pubkey       string
	keyErr       error
	preflightErr error
	upErr        error
	downErr      error
	upCalls      []app.TunnelConfig
	downCall     int
}

func newFakeTunnel() *fakeTunnel {
	return &fakeTunnel{pubkey: "client-wg-pubkey"}
}

func (f *fakeTunnel) Preflight() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.preflightErr
}

func (f *fakeTunnel) PublicKey() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pubkey, f.keyErr
}

func (f *fakeTunnel) Up(_ context.Context, cfg app.TunnelConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.upErr != nil {
		return f.upErr
	}
	f.upCalls = append(f.upCalls, cfg)
	return nil
}

func (f *fakeTunnel) Down(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downCall++
	return f.downErr
}

func (f *fakeTunnel) ups() []app.TunnelConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]app.TunnelConfig(nil), f.upCalls...)
}

func (f *fakeTunnel) downs() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.downCall
}

func newService(t *testing.T, tunnel app.Tunnel) *app.Service {
	t.Helper()
	svc, err := app.New(app.Config{
		StorePath:    t.TempDir() + "/tokens.json",
		Passphrase:   "correct horse battery staple",
		Tunnel:       tunnel,
		PollInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// fundService runs the full purchase path so the service holds spendable sats.
func fundService(t *testing.T, hub *testHub, svc *app.Service, amount uint64) {
	t.Helper()
	ctx := context.Background()

	invoice, err := svc.Purchase(ctx, amount)
	if err != nil {
		t.Fatalf("purchase: %v", err)
	}
	if invoice.Bolt11 == "" {
		t.Fatal("purchase returned an empty invoice")
	}

	if err := hub.ln.SimulateSettlement(invoice.PaymentHash); err != nil {
		t.Fatalf("simulate settlement: %v", err)
	}

	balance, err := svc.AwaitPurchase(ctx, invoice.QuoteID)
	if err != nil {
		t.Fatalf("await purchase: %v", err)
	}
	if balance < amount {
		t.Fatalf("balance = %d after minting %d sats", balance, amount)
	}
}

func TestConnectHubReportsBalanceAndNodes(t *testing.T) {
	hub := newTestHub(t)
	hub.addNode(t, "entry-1", types.RoleEntry)
	hub.addNode(t, "exit-1", types.RoleExit)

	svc := newService(t, newFakeTunnel())

	status, err := svc.ConnectHub(context.Background(), hub.server.URL)
	if err != nil {
		t.Fatalf("connect hub: %v", err)
	}
	if status.KeysetID == "" {
		t.Error("expected a keyset ID from the hub")
	}
	if status.Balance != 0 {
		t.Errorf("balance = %d, want 0 for a fresh wallet", status.Balance)
	}
	if status.NodeCount != 2 {
		t.Errorf("node count = %d, want 2", status.NodeCount)
	}
	if svc.HubURL() == "" {
		t.Error("hub URL not recorded")
	}
}

func TestOperationsRequireAHub(t *testing.T) {
	svc := newService(t, newFakeTunnel())
	ctx := context.Background()

	if _, err := svc.Balance(); !errors.Is(err, app.ErrNoHub) {
		t.Errorf("Balance error = %v, want ErrNoHub", err)
	}
	if _, err := svc.Purchase(ctx, 100); !errors.Is(err, app.ErrNoHub) {
		t.Errorf("Purchase error = %v, want ErrNoHub", err)
	}
	if _, err := svc.ListNodes(ctx); !errors.Is(err, app.ErrNoHub) {
		t.Errorf("ListNodes error = %v, want ErrNoHub", err)
	}
	if _, err := svc.Connect(ctx, 100); !errors.Is(err, app.ErrNoHub) {
		t.Errorf("Connect error = %v, want ErrNoHub", err)
	}
}

func TestPurchaseMintsSpendableBalance(t *testing.T) {
	hub := newTestHub(t)
	svc := newService(t, newFakeTunnel())

	if _, err := svc.ConnectHub(context.Background(), hub.server.URL); err != nil {
		t.Fatalf("connect hub: %v", err)
	}

	fundService(t, hub, svc, 128)

	balance, err := svc.Balance()
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != 128 {
		t.Errorf("balance = %d, want 128", balance)
	}
}

func TestConnectSpendsBothHopsAndBringsTunnelUp(t *testing.T) {
	hub := newTestHub(t)
	entry := hub.addNode(t, "entry-1", types.RoleEntry)
	exit := hub.addNode(t, "exit-1", types.RoleExit)

	tunnel := newFakeTunnel()
	svc := newService(t, tunnel)
	ctx := context.Background()

	if _, err := svc.ConnectHub(ctx, hub.server.URL); err != nil {
		t.Fatalf("connect hub: %v", err)
	}
	fundService(t, hub, svc, 128)

	session, err := svc.Connect(ctx, 32)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	if svc.State() != app.StateConnected {
		t.Errorf("state = %q, want connected", svc.State())
	}
	if session.SpentSats != 64 {
		t.Errorf("spent = %d, want 64 (32 per hop)", session.SpentSats)
	}
	if entry.connectCount() != 1 || exit.connectCount() != 1 {
		t.Errorf("connect calls: entry=%d exit=%d, want 1 each", entry.connectCount(), exit.connectCount())
	}

	ups := tunnel.ups()
	if len(ups) != 1 {
		t.Fatalf("tunnel brought up %d times, want 1", len(ups))
	}
	if ups[0].ClientKey != "client-wg-pubkey" {
		t.Errorf("client key = %q", ups[0].ClientKey)
	}
	if ups[0].Entry.NodeID != "entry-1" || ups[0].Exit.NodeID != "exit-1" {
		t.Errorf("hops = %q → %q, want entry-1 → exit-1", ups[0].Entry.NodeID, ups[0].Exit.NodeID)
	}
	if ups[0].Entry.NodeWGPubkey != entry.wgPubkey {
		t.Errorf("entry wg pubkey = %q, want %q", ups[0].Entry.NodeWGPubkey, entry.wgPubkey)
	}

	// The two hops must not have been paid with the same proofs: the hub burns
	// proofs on redemption, so a shared set would have failed at the exit node.
	balance, err := svc.Balance()
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != 64 {
		t.Errorf("balance = %d, want 64 remaining after spending 64", balance)
	}
}

func TestConnectWithoutBalanceLeavesServiceDisconnected(t *testing.T) {
	hub := newTestHub(t)
	hub.addNode(t, "entry-1", types.RoleEntry)
	hub.addNode(t, "exit-1", types.RoleExit)

	tunnel := newFakeTunnel()
	svc := newService(t, tunnel)
	ctx := context.Background()

	if _, err := svc.ConnectHub(ctx, hub.server.URL); err != nil {
		t.Fatalf("connect hub: %v", err)
	}

	if _, err := svc.Connect(ctx, 32); err == nil {
		t.Fatal("expected connect to fail with an empty wallet")
	}
	if svc.State() != app.StateDisconnected {
		t.Errorf("state = %q, want disconnected after a failed connect", svc.State())
	}
	if len(tunnel.ups()) != 0 {
		t.Error("tunnel was brought up despite payment failing")
	}
}

// An environment that cannot bring the tunnel up must be detected before any
// payment. The service pays both nodes before calling Up, and proofs a node
// has accepted are burned at the hub, so discovering the problem during
// bring-up would cost the user sats for a session they never get.
func TestConnectChecksTunnelBeforeSpending(t *testing.T) {
	hub := newTestHub(t)
	hub.addNode(t, "entry-1", types.RoleEntry)
	hub.addNode(t, "exit-1", types.RoleExit)

	tunnel := newFakeTunnel()
	tunnel.preflightErr = errors.New("root privileges are required")
	svc := newService(t, tunnel)
	ctx := context.Background()

	if _, err := svc.ConnectHub(ctx, hub.server.URL); err != nil {
		t.Fatalf("connect hub: %v", err)
	}
	fundService(t, hub, svc, 128)

	before, err := svc.Balance()
	if err != nil {
		t.Fatalf("balance: %v", err)
	}

	_, err = svc.Connect(ctx, 32)
	if err == nil {
		t.Fatal("connect should fail when the tunnel cannot be established")
	}
	if !strings.Contains(err.Error(), "root privileges") {
		t.Fatalf("error should explain the cause, got %q", err)
	}

	after, err := svc.Balance()
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if after != before {
		t.Fatalf("balance changed from %d to %d: no sats may be spent when the tunnel cannot come up", before, after)
	}
	if len(tunnel.ups()) != 0 {
		t.Error("tunnel must not be brought up after a failed preflight")
	}
	if svc.State() != app.StateDisconnected {
		t.Errorf("state = %q, want disconnected", svc.State())
	}
}

// A node failure before proofs change hands must not cost the user sats.
func TestFailedConnectRefundsUnspentProofs(t *testing.T) {
	hub := newTestHub(t)
	entry := hub.addNode(t, "entry-1", types.RoleEntry)
	hub.addNode(t, "exit-1", types.RoleExit)
	entry.setReject(true)

	svc := newService(t, newFakeTunnel())
	ctx := context.Background()

	if _, err := svc.ConnectHub(ctx, hub.server.URL); err != nil {
		t.Fatalf("connect hub: %v", err)
	}
	fundService(t, hub, svc, 128)

	if _, err := svc.Connect(ctx, 32); err == nil {
		t.Fatal("expected connect to fail when the entry node rejects")
	}

	balance, err := svc.Balance()
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != 128 {
		t.Errorf("balance = %d, want 128 — no proofs reached a node, so none should be lost", balance)
	}
}

// If the entry node accepted its proofs they are burned at the hub. Refunding
// them would show a balance the user cannot actually spend.
func TestPartialConnectDoesNotRefundBurnedProofs(t *testing.T) {
	hub := newTestHub(t)
	hub.addNode(t, "entry-1", types.RoleEntry)
	exit := hub.addNode(t, "exit-1", types.RoleExit)
	exit.setReject(true)

	svc := newService(t, newFakeTunnel())
	ctx := context.Background()

	if _, err := svc.ConnectHub(ctx, hub.server.URL); err != nil {
		t.Fatalf("connect hub: %v", err)
	}
	fundService(t, hub, svc, 128)

	if _, err := svc.Connect(ctx, 32); err == nil {
		t.Fatal("expected connect to fail when the exit node rejects")
	}

	balance, err := svc.Balance()
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != 96 {
		t.Errorf("balance = %d, want 96 — the entry hop's 32 sats were burned at the hub", balance)
	}
}

func TestDisconnectTearsTunnelDownAndClearsSession(t *testing.T) {
	hub := newTestHub(t)
	hub.addNode(t, "entry-1", types.RoleEntry)
	hub.addNode(t, "exit-1", types.RoleExit)

	tunnel := newFakeTunnel()
	svc := newService(t, tunnel)
	ctx := context.Background()

	if _, err := svc.ConnectHub(ctx, hub.server.URL); err != nil {
		t.Fatalf("connect hub: %v", err)
	}
	fundService(t, hub, svc, 128)

	if _, err := svc.Connect(ctx, 32); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if svc.Session() == nil {
		t.Fatal("expected an active session")
	}

	if err := svc.Disconnect(ctx); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if svc.State() != app.StateDisconnected {
		t.Errorf("state = %q, want disconnected", svc.State())
	}
	if svc.Session() != nil {
		t.Error("session should be cleared after disconnect")
	}
	if tunnel.downs() != 1 {
		t.Errorf("tunnel Down called %d times, want 1", tunnel.downs())
	}

	if err := svc.Disconnect(ctx); !errors.Is(err, app.ErrNotConnected) {
		t.Errorf("second disconnect error = %v, want ErrNotConnected", err)
	}
}

// A failed teardown must still clear the session, otherwise the UI is stuck
// showing "connected" with no way to retry.
func TestDisconnectClearsSessionEvenWhenTeardownFails(t *testing.T) {
	hub := newTestHub(t)
	hub.addNode(t, "entry-1", types.RoleEntry)
	hub.addNode(t, "exit-1", types.RoleExit)

	tunnel := newFakeTunnel()
	tunnel.downErr = errors.New("interface busy")
	svc := newService(t, tunnel)
	ctx := context.Background()

	if _, err := svc.ConnectHub(ctx, hub.server.URL); err != nil {
		t.Fatalf("connect hub: %v", err)
	}
	fundService(t, hub, svc, 128)
	if _, err := svc.Connect(ctx, 32); err != nil {
		t.Fatalf("connect: %v", err)
	}

	if err := svc.Disconnect(ctx); err == nil {
		t.Fatal("expected the teardown error to be reported")
	}
	if svc.State() != app.StateDisconnected {
		t.Errorf("state = %q, want disconnected", svc.State())
	}
	if svc.Session() != nil {
		t.Error("session should be cleared even when teardown fails")
	}
}

func TestConnectTwiceIsRejected(t *testing.T) {
	hub := newTestHub(t)
	hub.addNode(t, "entry-1", types.RoleEntry)
	hub.addNode(t, "exit-1", types.RoleExit)

	svc := newService(t, newFakeTunnel())
	ctx := context.Background()

	if _, err := svc.ConnectHub(ctx, hub.server.URL); err != nil {
		t.Fatalf("connect hub: %v", err)
	}
	fundService(t, hub, svc, 256)

	if _, err := svc.Connect(ctx, 32); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := svc.Connect(ctx, 32); !errors.Is(err, app.ErrAlreadyOn) {
		t.Errorf("second connect error = %v, want ErrAlreadyOn", err)
	}
}

func TestSwitchingHubsRequiresDisconnect(t *testing.T) {
	hub := newTestHub(t)
	hub.addNode(t, "entry-1", types.RoleEntry)
	hub.addNode(t, "exit-1", types.RoleExit)
	other := newTestHub(t)

	svc := newService(t, newFakeTunnel())
	ctx := context.Background()

	if _, err := svc.ConnectHub(ctx, hub.server.URL); err != nil {
		t.Fatalf("connect hub: %v", err)
	}
	fundService(t, hub, svc, 128)
	if _, err := svc.Connect(ctx, 32); err != nil {
		t.Fatalf("connect: %v", err)
	}

	if _, err := svc.ConnectHub(ctx, other.server.URL); err == nil {
		t.Fatal("expected switching hubs during an active session to fail")
	}

	if err := svc.Disconnect(ctx); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if _, err := svc.ConnectHub(ctx, other.server.URL); err != nil {
		t.Fatalf("connect to second hub after disconnect: %v", err)
	}
}

// Proofs are only spendable at the mint that issued them, so switching hubs
// must not surface another hub's balance.
func TestBalanceIsScopedToTheConnectedHub(t *testing.T) {
	hubA := newTestHub(t)
	hubB := newTestHub(t)

	svc := newService(t, newFakeTunnel())
	ctx := context.Background()

	if _, err := svc.ConnectHub(ctx, hubA.server.URL); err != nil {
		t.Fatalf("connect hub A: %v", err)
	}
	fundService(t, hubA, svc, 128)

	if _, err := svc.ConnectHub(ctx, hubB.server.URL); err != nil {
		t.Fatalf("connect hub B: %v", err)
	}
	balance, err := svc.Balance()
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != 0 {
		t.Errorf("hub B balance = %d, want 0 — hub A's proofs are not spendable here", balance)
	}

	// Hub A's balance must still be intact when the user switches back.
	if _, err := svc.ConnectHub(ctx, hubA.server.URL); err != nil {
		t.Fatalf("reconnect hub A: %v", err)
	}
	balance, err = svc.Balance()
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != 128 {
		t.Errorf("hub A balance = %d, want 128 after switching away and back", balance)
	}
}

func TestConnectRequiresATunnel(t *testing.T) {
	hub := newTestHub(t)
	hub.addNode(t, "entry-1", types.RoleEntry)
	hub.addNode(t, "exit-1", types.RoleExit)

	svc := newService(t, nil)
	ctx := context.Background()

	if _, err := svc.ConnectHub(ctx, hub.server.URL); err != nil {
		t.Fatalf("connect hub: %v", err)
	}
	fundService(t, hub, svc, 128)

	if _, err := svc.Connect(ctx, 32); err == nil {
		t.Fatal("expected connect to fail without a tunnel implementation")
	}

	balance, err := svc.Balance()
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != 128 {
		t.Errorf("balance = %d, want 128 — nothing was spent", balance)
	}
}

func TestZeroAmountConnectIsRejected(t *testing.T) {
	hub := newTestHub(t)
	svc := newService(t, newFakeTunnel())

	if _, err := svc.ConnectHub(context.Background(), hub.server.URL); err != nil {
		t.Fatalf("connect hub: %v", err)
	}
	if _, err := svc.Connect(context.Background(), 0); !errors.Is(err, app.ErrAmountTooSmall) {
		t.Errorf("error = %v, want ErrAmountTooSmall", err)
	}
}

func TestConnectHubRejectsUnreachableHub(t *testing.T) {
	svc := newService(t, newFakeTunnel())
	if _, err := svc.ConnectHub(context.Background(), "http://127.0.0.1:1"); err == nil {
		t.Fatal("expected an unreachable hub to fail")
	}
	if svc.HubURL() != "" {
		t.Error("a failed ConnectHub must not record the hub")
	}
}
