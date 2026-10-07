package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Radi-Labs/ARFL/internal/app"
	"github.com/Radi-Labs/ARFL/internal/client"
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
	rejectAs  int
	connects  int
	wgKeys    []string
	onConnect func()
}

func (n *testNode) setReject(reject bool) {
	n.mu.Lock()
	n.rejectAll = reject
	n.mu.Unlock()
}

// setBurnedReject makes the node answer 409, as a real node does when the hub
// reports the presented proofs as already spent.
func (n *testNode) setBurnedReject() {
	n.mu.Lock()
	n.rejectAll = true
	n.rejectAs = http.StatusConflict
	n.mu.Unlock()
}

func (n *testNode) connectCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.connects
}

func (n *testNode) setOnConnect(fn func()) {
	n.mu.Lock()
	n.onConnect = fn
	n.mu.Unlock()
}

func (n *testNode) seenClientWGKeys() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.wgKeys...)
}

func (n *testNode) handleConnect(w http.ResponseWriter, r *http.Request) {
	// Matched exactly. This used to be HasSuffix(path, "/connect"), which also
	// matches "/cashu-connect" — so the fake accepted the client posting Cashu
	// proofs to the RSA endpoint, and the mismatch survived until a real node
	// rejected it. The fake must be as strict as the node it stands in for.
	if r.URL.Path != "/cashu-connect" {
		http.NotFound(w, r)
		return
	}

	n.mu.Lock()
	n.connects++
	reject := n.rejectAll
	status := n.rejectAs
	onConnect := n.onConnect
	n.mu.Unlock()
	if onConnect != nil {
		onConnect()
	}

	if reject {
		if status == 0 {
			status = http.StatusPaymentRequired
		}
		w.WriteHeader(status)
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

	n.mu.Lock()
	n.wgKeys = append(n.wgKeys, req.WGPubkey)
	n.mu.Unlock()

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

func (f *fakeTunnel) UpOuter(_ context.Context, cfg app.TunnelConfig) error {
	// Default staged behavior for tests: record only final full config in Up().
	// This keeps legacy assertions stable while allowing service-level checks
	// that require staged-tunnel support in HTTP mode.
	return nil
}

func (f *fakeTunnel) UpInner(ctx context.Context, cfg app.TunnelConfig) error {
	return f.Up(ctx, cfg)
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

// unstagedFakeTunnel intentionally does not implement app.StagedTunnel.
type unstagedFakeTunnel struct {
	base *fakeTunnel
}

func newUnstagedFakeTunnel() *unstagedFakeTunnel {
	return &unstagedFakeTunnel{base: newFakeTunnel()}
}

func (u *unstagedFakeTunnel) Preflight() error {
	return u.base.Preflight()
}

func (u *unstagedFakeTunnel) PublicKey() (string, error) {
	return u.base.PublicKey()
}

func (u *unstagedFakeTunnel) Up(ctx context.Context, cfg app.TunnelConfig) error {
	return u.base.Up(ctx, cfg)
}

func (u *unstagedFakeTunnel) Down(ctx context.Context) error {
	return u.base.Down(ctx)
}

type stagedFakeTunnel struct {
	*fakeTunnel
	outerErr  error
	innerErr  error
	outerUp   []app.TunnelConfig
	innerUp   []app.TunnelConfig
	outerUpMu sync.Mutex
}

func newStagedFakeTunnel() *stagedFakeTunnel {
	return &stagedFakeTunnel{fakeTunnel: newFakeTunnel()}
}

func (s *stagedFakeTunnel) UpOuter(_ context.Context, cfg app.TunnelConfig) error {
	s.outerUpMu.Lock()
	defer s.outerUpMu.Unlock()
	if s.outerErr != nil {
		return s.outerErr
	}
	s.outerUp = append(s.outerUp, cfg)
	return nil
}

func (s *stagedFakeTunnel) UpInner(_ context.Context, cfg app.TunnelConfig) error {
	s.outerUpMu.Lock()
	defer s.outerUpMu.Unlock()
	if s.innerErr != nil {
		return s.innerErr
	}
	s.innerUp = append(s.innerUp, cfg)
	return nil
}

func (s *stagedFakeTunnel) outerUps() []app.TunnelConfig {
	s.outerUpMu.Lock()
	defer s.outerUpMu.Unlock()
	return append([]app.TunnelConfig(nil), s.outerUp...)
}

func (s *stagedFakeTunnel) innerUps() []app.TunnelConfig {
	s.outerUpMu.Lock()
	defer s.outerUpMu.Unlock()
	return append([]app.TunnelConfig(nil), s.innerUp...)
}

type hopKeyFakeTunnel struct {
	*fakeTunnel
	mu           sync.Mutex
	seq          int
	prepareCalls int
	resetCalls   int
	entryPub     string
	exitPub      string
	prepareErr   error
}

func newHopKeyFakeTunnel() *hopKeyFakeTunnel {
	return &hopKeyFakeTunnel{
		fakeTunnel: newFakeTunnel(),
		entryPub:   "client-entry-0",
		exitPub:    "client-exit-0",
	}
}

func (h *hopKeyFakeTunnel) PrepareHopKeys() (string, string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prepareCalls++
	if h.prepareErr != nil {
		return "", "", h.prepareErr
	}
	h.entryPub = fmt.Sprintf("client-entry-%d", h.seq)
	h.exitPub = fmt.Sprintf("client-exit-%d", h.seq)
	return h.entryPub, h.exitPub, nil
}

func (h *hopKeyFakeTunnel) ResetHopKeys() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.resetCalls++
	h.seq++
}

func (h *hopKeyFakeTunnel) stats() (prepareCalls, resetCalls int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.prepareCalls, h.resetCalls
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

func newServiceWithTransports(t *testing.T, tunnel app.Tunnel, preferred []types.Transport, allowed []types.Transport) *app.Service {
	t.Helper()
	svc, err := app.New(app.Config{
		StorePath:           t.TempDir() + "/tokens.json",
		Passphrase:          "correct horse battery staple",
		Tunnel:              tunnel,
		PollInterval:        10 * time.Millisecond,
		PreferredTransports: preferred,
		AllowedTransports:   allowed,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

func (h *testHub) setNodeTransports(id string, preferred types.Transport, caps []types.TransportCapability) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.nodes {
		if h.nodes[i].Info.ID != id {
			continue
		}
		h.nodes[i].Info.PreferredTransport = preferred
		h.nodes[i].Info.Transports = append([]types.TransportCapability(nil), caps...)
		return
	}
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

func TestNewRejectsNIP44DeliveryWithoutRelays(t *testing.T) {
	_, err := app.New(app.Config{
		StorePath:         t.TempDir() + "/tokens.json",
		Passphrase:        "correct horse battery staple",
		TokenDelivery:     client.TokenDeliveryNIP44,
		NostrRelays:       nil,
		PollInterval:      10 * time.Millisecond,
		AllowedTransports: []types.Transport{types.TransportWireGuard},
	})
	if err == nil {
		t.Fatal("expected New to reject nip44 delivery without relays")
	}
	if !strings.Contains(err.Error(), "requires at least one relay URL") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewRejectsNIP44DeliveryWithHubDiscovery(t *testing.T) {
	_, err := app.New(app.Config{
		StorePath:       t.TempDir() + "/tokens.json",
		Passphrase:      "correct horse battery staple",
		TokenDelivery:   client.TokenDeliveryNIP44,
		NostrRelays:     []string{"wss://relay.example"},
		DiscoverySource: app.DiscoverySourceHub,
	})
	if err == nil || !strings.Contains(err.Error(), "requires discovery_source=\"nostr\"") {
		t.Fatalf("unverified hub identities must be rejected: %v", err)
	}
}

func TestNewRejectsUnknownTokenDeliveryMode(t *testing.T) {
	_, err := app.New(app.Config{
		StorePath:         t.TempDir() + "/tokens.json",
		Passphrase:        "correct horse battery staple",
		TokenDelivery:     client.TokenDeliveryMode("carrier-pigeon"),
		PollInterval:      10 * time.Millisecond,
		AllowedTransports: []types.Transport{types.TransportWireGuard},
	})
	if err == nil {
		t.Fatal("expected New to reject unknown token delivery mode")
	}
	if !strings.Contains(err.Error(), "unsupported token delivery mode") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewRejectsNostrDiscoveryWithoutTrustedHubPubkeys(t *testing.T) {
	_, err := app.New(app.Config{
		StorePath:         t.TempDir() + "/tokens.json",
		Passphrase:        "correct horse battery staple",
		TokenDelivery:     client.TokenDeliveryNIP44,
		NostrRelays:       []string{"wss://relay.example"},
		DiscoverySource:   app.DiscoverySourceNostr,
		TrustedHubPubkeys: nil,
		PollInterval:      10 * time.Millisecond,
		AllowedTransports: []types.Transport{types.TransportWireGuard},
	})
	if err == nil {
		t.Fatal("expected New to reject nostr discovery without trusted hub pubkeys")
	}
	if !strings.Contains(err.Error(), "requires at least one trusted hub pubkey") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewAcceptsNostrDiscoveryWithTrustedHubPubkeys(t *testing.T) {
	svc, err := app.New(app.Config{
		StorePath:         t.TempDir() + "/tokens.json",
		Passphrase:        "correct horse battery staple",
		TokenDelivery:     client.TokenDeliveryNIP44,
		NostrRelays:       []string{"wss://relay.example"},
		DiscoverySource:   app.DiscoverySourceNostr,
		TrustedHubPubkeys: []string{"hub-pubkey"},
		PollInterval:      10 * time.Millisecond,
		AllowedTransports: []types.Transport{types.TransportWireGuard},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = svc.Close(context.Background())
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
	if ups[0].Transport != types.TransportWireGuard {
		t.Errorf("transport = %q, want %q", ups[0].Transport, types.TransportWireGuard)
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

func TestConnectUsesDistinctPerHopClientKeys(t *testing.T) {
	hub := newTestHub(t)
	entry := hub.addNode(t, "entry-1", types.RoleEntry)
	exit := hub.addNode(t, "exit-1", types.RoleExit)

	tunnel := newHopKeyFakeTunnel()
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

	entryKeys := entry.seenClientWGKeys()
	exitKeys := exit.seenClientWGKeys()
	if len(entryKeys) != 1 || len(exitKeys) != 1 {
		t.Fatalf("unexpected node key observations: entry=%v exit=%v", entryKeys, exitKeys)
	}
	if entryKeys[0] == exitKeys[0] {
		t.Fatalf("entry and exit received the same client key %q", entryKeys[0])
	}

	if session.Config.EntryClientKey != entryKeys[0] || session.Config.ExitClientKey != exitKeys[0] {
		t.Fatalf("session keys do not match node-observed keys: session=%q/%q nodes=%q/%q", session.Config.EntryClientKey, session.Config.ExitClientKey, entryKeys[0], exitKeys[0])
	}
	if session.Config.ClientKey != session.Config.EntryClientKey {
		t.Fatalf("session client_key = %q, want entry key %q", session.Config.ClientKey, session.Config.EntryClientKey)
	}

	prepareCalls, resetCalls := tunnel.stats()
	if prepareCalls != 1 || resetCalls != 0 {
		t.Fatalf("hop key lifecycle calls = prepare:%d reset:%d, want prepare:1 reset:0", prepareCalls, resetCalls)
	}
}

func TestConnectFailureResetsHopKeysBeforeRetry(t *testing.T) {
	hub := newTestHub(t)
	entry := hub.addNode(t, "entry-1", types.RoleEntry)
	exit := hub.addNode(t, "exit-1", types.RoleExit)
	exit.setReject(true)

	tunnel := newHopKeyFakeTunnel()
	svc := newService(t, tunnel)
	ctx := context.Background()

	if _, err := svc.ConnectHub(ctx, hub.server.URL); err != nil {
		t.Fatalf("connect hub: %v", err)
	}
	fundService(t, hub, svc, 128)

	if _, err := svc.Connect(ctx, 32); err == nil {
		t.Fatal("expected first connect attempt to fail")
	}

	prepareCalls, resetCalls := tunnel.stats()
	if prepareCalls != 1 || resetCalls != 1 {
		t.Fatalf("after failure hop key lifecycle calls = prepare:%d reset:%d, want prepare:1 reset:1", prepareCalls, resetCalls)
	}

	exit.setReject(false)
	if _, err := svc.Connect(ctx, 32); err != nil {
		t.Fatalf("second connect attempt failed: %v", err)
	}

	prepareCalls, resetCalls = tunnel.stats()
	if prepareCalls != 2 || resetCalls != 1 {
		t.Fatalf("after retry hop key lifecycle calls = prepare:%d reset:%d, want prepare:2 reset:1", prepareCalls, resetCalls)
	}

	entryKeys := entry.seenClientWGKeys()
	if len(entryKeys) != 2 {
		t.Fatalf("entry key observations = %v, want two attempts", entryKeys)
	}
	if entryKeys[0] == entryKeys[1] {
		t.Fatalf("entry client key was reused across retries: %q", entryKeys[0])
	}
}

func TestConnectPreflightFailureResetsHopKeys(t *testing.T) {
	hub := newTestHub(t)
	hub.addNode(t, "entry-1", types.RoleEntry)
	hub.addNode(t, "exit-1", types.RoleExit)

	tunnel := newHopKeyFakeTunnel()
	tunnel.preflightErr = errors.New("needs privilege")
	svc := newService(t, tunnel)
	ctx := context.Background()

	if _, err := svc.ConnectHub(ctx, hub.server.URL); err != nil {
		t.Fatalf("connect hub: %v", err)
	}
	fundService(t, hub, svc, 128)

	if _, err := svc.Connect(ctx, 32); err == nil {
		t.Fatal("expected connect to fail on preflight")
	}
	prepareCalls, resetCalls := tunnel.stats()
	if prepareCalls != 1 || resetCalls != 1 {
		t.Fatalf("hop key lifecycle calls = prepare:%d reset:%d, want prepare:1 reset:1", prepareCalls, resetCalls)
	}
}

func TestConnectStagesOuterBeforeExitProvisioning(t *testing.T) {
	hub := newTestHub(t)
	entry := hub.addNode(t, "entry-1", types.RoleEntry)
	exit := hub.addNode(t, "exit-1", types.RoleExit)

	tunnel := newStagedFakeTunnel()
	exitConnectObservation := make(chan bool, 1)
	exit.setOnConnect(func() {
		exitConnectObservation <- len(tunnel.outerUps()) > 0
	})
	svc := newService(t, tunnel)
	ctx := context.Background()

	if _, err := svc.ConnectHub(ctx, hub.server.URL); err != nil {
		t.Fatalf("connect hub: %v", err)
	}
	fundService(t, hub, svc, 128)

	if _, err := svc.Connect(ctx, 32); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if entry.connectCount() != 1 || exit.connectCount() != 1 {
		t.Fatalf("connect counts: entry=%d exit=%d, want 1 each", entry.connectCount(), exit.connectCount())
	}
	if got := <-exitConnectObservation; !got {
		t.Fatal("exit node was contacted before outer tunnel came up")
	}
	outerUps := tunnel.outerUps()
	if len(outerUps) != 1 || len(tunnel.innerUps()) != 1 {
		t.Fatalf("staged calls outer=%d inner=%d, want 1 each", len(outerUps), len(tunnel.innerUps()))
	}
	exitURL, err := url.Parse(exit.server.URL)
	if err != nil {
		t.Fatalf("parse exit test URL: %v", err)
	}
	if outerUps[0].Exit.Endpoint != "exit-1.example:51820" {
		t.Fatalf("outer exit endpoint = %q, want exit-1.example:51820", outerUps[0].Exit.Endpoint)
	}
	if len(outerUps[0].OuterPinnedEndpoints) != 1 || outerUps[0].OuterPinnedEndpoints[0] != exitURL.Host {
		t.Fatalf("outer pinned endpoints = %v, want [%s]", outerUps[0].OuterPinnedEndpoints, exitURL.Host)
	}
	if len(tunnel.ups()) != 0 {
		t.Fatalf("full Up should not be used when staged methods exist; got %d calls", len(tunnel.ups()))
	}
}

func TestConnectOuterStageFailureRefundsExitProofs(t *testing.T) {
	hub := newTestHub(t)
	entry := hub.addNode(t, "entry-1", types.RoleEntry)
	exit := hub.addNode(t, "exit-1", types.RoleExit)

	tunnel := newStagedFakeTunnel()
	tunnel.outerErr = errors.New("outer tunnel failed")
	svc := newService(t, tunnel)
	ctx := context.Background()

	if _, err := svc.ConnectHub(ctx, hub.server.URL); err != nil {
		t.Fatalf("connect hub: %v", err)
	}
	fundService(t, hub, svc, 128)

	if _, err := svc.Connect(ctx, 32); err == nil {
		t.Fatal("expected connect to fail when UpOuter fails")
	}
	balance, err := svc.Balance()
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != 96 {
		t.Fatalf("balance=%d, want 96 (entry spent, exit refunded)", balance)
	}
	if entry.connectCount() != 1 {
		t.Fatalf("entry connects=%d, want 1", entry.connectCount())
	}
	if exit.connectCount() != 0 {
		t.Fatalf("exit connects=%d, want 0 when outer stage fails", exit.connectCount())
	}
}

func TestConnectInnerStageFailureCleansUpAndKeepsSpentBalance(t *testing.T) {
	hub := newTestHub(t)
	hub.addNode(t, "entry-1", types.RoleEntry)
	hub.addNode(t, "exit-1", types.RoleExit)

	tunnel := newStagedFakeTunnel()
	tunnel.innerErr = errors.New("inner tunnel failed")
	svc := newService(t, tunnel)
	ctx := context.Background()

	if _, err := svc.ConnectHub(ctx, hub.server.URL); err != nil {
		t.Fatalf("connect hub: %v", err)
	}
	fundService(t, hub, svc, 128)

	if _, err := svc.Connect(ctx, 32); err == nil {
		t.Fatal("expected connect to fail when UpInner fails")
	}
	if tunnel.downs() != 1 {
		t.Fatalf("Down called %d times, want 1 cleanup after inner failure", tunnel.downs())
	}
	balance, err := svc.Balance()
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != 64 {
		t.Fatalf("balance=%d, want 64 (both hops were already spent)", balance)
	}
}

func TestConnectHTTPRequiresStagedTunnelSupport(t *testing.T) {
	hub := newTestHub(t)
	entry := hub.addNode(t, "entry-1", types.RoleEntry)
	exit := hub.addNode(t, "exit-1", types.RoleExit)

	tunnel := newUnstagedFakeTunnel()
	svc := newService(t, tunnel)
	ctx := context.Background()

	if _, err := svc.ConnectHub(ctx, hub.server.URL); err != nil {
		t.Fatalf("connect hub: %v", err)
	}
	fundService(t, hub, svc, 128)

	before, err := svc.Balance()
	if err != nil {
		t.Fatalf("balance before connect: %v", err)
	}

	_, err = svc.Connect(ctx, 32)
	if err == nil {
		t.Fatal("expected connect to fail without staged tunnel support in http mode")
	}
	if !strings.Contains(err.Error(), "requires staged tunnel support") {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry.connectCount() != 0 || exit.connectCount() != 0 {
		t.Fatalf("node connect should not run when staged support is missing: entry=%d exit=%d", entry.connectCount(), exit.connectCount())
	}
	after, err := svc.Balance()
	if err != nil {
		t.Fatalf("balance after connect: %v", err)
	}
	if before != after {
		t.Fatalf("balance changed despite fail-closed staged requirement: before=%d after=%d", before, after)
	}
}

func TestConnectFailsFastForUnsupportedTransport(t *testing.T) {
	hub := newTestHub(t)
	entry := hub.addNode(t, "entry-1", types.RoleEntry)
	exit := hub.addNode(t, "exit-1", types.RoleExit)

	hub.setNodeTransports("entry-1", types.TransportHysteria2, []types.TransportCapability{
		{Transport: types.TransportWireGuard, Endpoint: "entry-1.example:51820", ConnectURL: entry.server.URL},
		{Transport: types.TransportHysteria2, Endpoint: "entry-1.example:443", ConnectURL: entry.server.URL},
	})
	hub.setNodeTransports("exit-1", types.TransportHysteria2, []types.TransportCapability{
		{Transport: types.TransportWireGuard, Endpoint: "exit-1.example:51820", ConnectURL: exit.server.URL},
		{Transport: types.TransportHysteria2, Endpoint: "exit-1.example:443", ConnectURL: exit.server.URL},
	})

	tunnel := newFakeTunnel()
	svc := newServiceWithTransports(
		t,
		tunnel,
		[]types.Transport{types.TransportHysteria2, types.TransportWireGuard},
		[]types.Transport{types.TransportWireGuard, types.TransportHysteria2},
	)
	ctx := context.Background()

	if _, err := svc.ConnectHub(ctx, hub.server.URL); err != nil {
		t.Fatalf("connect hub: %v", err)
	}
	fundService(t, hub, svc, 128)

	before, err := svc.Balance()
	if err != nil {
		t.Fatalf("balance before connect: %v", err)
	}

	_, err = svc.Connect(ctx, 32)
	if !errors.Is(err, app.ErrTransportUnsupported) {
		t.Fatalf("error = %v, want ErrTransportUnsupported", err)
	}
	if !strings.Contains(err.Error(), "selected=hysteria2") {
		t.Fatalf("error should include selected transport, got %q", err)
	}
	if !strings.Contains(err.Error(), "preferred=hysteria2,wireguard") {
		t.Fatalf("error should include preferred policy, got %q", err)
	}
	if !strings.Contains(err.Error(), "allowed=hysteria2,wireguard") {
		t.Fatalf("error should include allowed policy, got %q", err)
	}

	if entry.connectCount() != 0 || exit.connectCount() != 0 {
		t.Fatalf("node connect should not run for unsupported transport: entry=%d exit=%d", entry.connectCount(), exit.connectCount())
	}
	if len(tunnel.ups()) != 0 {
		t.Fatal("tunnel should not be brought up for unsupported transport")
	}
	after, err := svc.Balance()
	if err != nil {
		t.Fatalf("balance after connect: %v", err)
	}
	if before != after {
		t.Fatalf("balance changed on unsupported transport: before=%d after=%d", before, after)
	}
}

func TestConnectNoCompatiblePairIncludesTransportPolicyAndPreservesBalance(t *testing.T) {
	hub := newTestHub(t)
	entry := hub.addNode(t, "entry-1", types.RoleEntry)
	exit := hub.addNode(t, "exit-1", types.RoleExit)

	hub.setNodeTransports("entry-1", types.TransportHysteria2, []types.TransportCapability{
		{Transport: types.TransportHysteria2, Endpoint: "entry-1.example:443", ConnectURL: entry.server.URL},
	})
	hub.setNodeTransports("exit-1", types.TransportHysteria2, []types.TransportCapability{
		{Transport: types.TransportHysteria2, Endpoint: "exit-1.example:443", ConnectURL: exit.server.URL},
	})

	svc := newServiceWithTransports(
		t,
		newFakeTunnel(),
		[]types.Transport{types.TransportWireGuard},
		[]types.Transport{types.TransportWireGuard},
	)
	ctx := context.Background()

	if _, err := svc.ConnectHub(ctx, hub.server.URL); err != nil {
		t.Fatalf("connect hub: %v", err)
	}
	fundService(t, hub, svc, 128)

	before, err := svc.Balance()
	if err != nil {
		t.Fatalf("balance before connect: %v", err)
	}

	_, err = svc.Connect(ctx, 32)
	if !errors.Is(err, client.ErrNoCompatiblePair) {
		t.Fatalf("error = %v, want ErrNoCompatiblePair", err)
	}
	if !strings.Contains(err.Error(), "preferred=wireguard") {
		t.Fatalf("error should include preferred policy, got %q", err)
	}
	if !strings.Contains(err.Error(), "allowed=wireguard") {
		t.Fatalf("error should include allowed policy, got %q", err)
	}
	if entry.connectCount() != 0 || exit.connectCount() != 0 {
		t.Fatalf("node connect should not run when no compatible pair exists: entry=%d exit=%d", entry.connectCount(), exit.connectCount())
	}

	after, err := svc.Balance()
	if err != nil {
		t.Fatalf("balance after connect: %v", err)
	}
	if before != after {
		t.Fatalf("balance changed on incompatible policy: before=%d after=%d", before, after)
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

// A 409 means the hub already burned the failing hop's proofs even though the
// node gave no tunnel. Returning them to the wallet would make every retry pick
// the same dead proof and fail again.
func TestExitAlreadySpentDropsBurnedProofsInsteadOfRefunding(t *testing.T) {
	hub := newTestHub(t)
	hub.addNode(t, "entry-1", types.RoleEntry)
	exit := hub.addNode(t, "exit-1", types.RoleExit)
	exit.setBurnedReject()

	svc := newService(t, newFakeTunnel())
	ctx := context.Background()

	if _, err := svc.ConnectHub(ctx, hub.server.URL); err != nil {
		t.Fatalf("connect hub: %v", err)
	}
	fundService(t, hub, svc, 128)

	if _, err := svc.Connect(ctx, 32); err == nil {
		t.Fatal("expected connect to fail when the exit node reports proofs spent")
	}

	balance, err := svc.Balance()
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != 64 {
		t.Errorf("balance = %d, want 64 — entry (32) was burned and the exit's rejected proofs (32) must be dropped", balance)
	}
}

func TestEntryAlreadySpentDropsItsProofsButRefundsTheExitHop(t *testing.T) {
	hub := newTestHub(t)
	entry := hub.addNode(t, "entry-1", types.RoleEntry)
	hub.addNode(t, "exit-1", types.RoleExit)
	entry.setBurnedReject()

	svc := newService(t, newFakeTunnel())
	ctx := context.Background()

	if _, err := svc.ConnectHub(ctx, hub.server.URL); err != nil {
		t.Fatalf("connect hub: %v", err)
	}
	fundService(t, hub, svc, 128)

	if _, err := svc.Connect(ctx, 32); err == nil {
		t.Fatal("expected connect to fail when the entry node reports proofs spent")
	}

	balance, err := svc.Balance()
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != 96 {
		t.Errorf("balance = %d, want 96 — only the entry hop's proofs are burned; the exit hop never reached a node", balance)
	}
}

// validatingTunnel is a fakeTunnel that can also refuse node endpoints, like the
// real tunnel does.
type validatingTunnel struct {
	*fakeTunnel
	endpointErr error
}

func (v *validatingTunnel) ValidateEndpoints(_, _ string) error { return v.endpointErr }

// Endpoints come from node announcements. One the tunnel will refuse must be
// caught before either node is paid, because a node that accepted its proofs
// has already burned them.
func TestConnectRejectsUnusableEndpointBeforeSpending(t *testing.T) {
	hub := newTestHub(t)
	entry := hub.addNode(t, "entry-1", types.RoleEntry)
	exit := hub.addNode(t, "exit-1", types.RoleExit)

	tunnel := &validatingTunnel{fakeTunnel: newFakeTunnel(), endpointErr: errors.New("address 10.0.0.5 is a private address")}
	svc := newService(t, tunnel)
	ctx := context.Background()

	if _, err := svc.ConnectHub(ctx, hub.server.URL); err != nil {
		t.Fatalf("connect hub: %v", err)
	}
	fundService(t, hub, svc, 128)

	_, err := svc.Connect(ctx, 32)
	if err == nil || !strings.Contains(err.Error(), "unusable endpoint") {
		t.Fatalf("expected an unusable endpoint error, got %v", err)
	}

	balance, berr := svc.Balance()
	if berr != nil {
		t.Fatalf("balance: %v", berr)
	}
	if balance != 128 {
		t.Errorf("balance = %d, want 128 — nothing should be spent on an endpoint the tunnel will refuse", balance)
	}
	if entry.connectCount() != 0 || exit.connectCount() != 0 {
		t.Errorf("a node was contacted (entry=%d exit=%d) despite the bad endpoint", entry.connectCount(), exit.connectCount())
	}
}

// A supplied allowlist that names nothing supported (say, a typo) means nothing
// is allowed. It must not fall back to allowing every transport.
func TestInvalidAllowlistDoesNotWidenPolicy(t *testing.T) {
	hub := newTestHub(t)
	entry := hub.addNode(t, "entry-1", types.RoleEntry)
	hub.addNode(t, "exit-1", types.RoleExit)

	svc := newServiceWithTransports(t, newFakeTunnel(), nil, []types.Transport{"hysteria-typo"})
	ctx := context.Background()

	if _, err := svc.ConnectHub(ctx, hub.server.URL); err != nil {
		t.Fatalf("connect hub: %v", err)
	}
	fundService(t, hub, svc, 128)

	_, err := svc.Connect(ctx, 32)
	if !errors.Is(err, client.ErrNoCompatiblePair) {
		t.Fatalf("got %v, want ErrNoCompatiblePair", err)
	}

	balance, berr := svc.Balance()
	if berr != nil {
		t.Fatalf("balance: %v", berr)
	}
	if balance != 128 || entry.connectCount() != 0 {
		t.Errorf("balance=%d entryConnects=%d, want 128 and 0", balance, entry.connectCount())
	}
}

// Topping up pays both nodes of the live session without touching the
// tunnel, so traffic never leaves it while more bandwidth is bought.
func TestExtendPaysBothNodesWithoutTearingTheTunnelDown(t *testing.T) {
	hub := newTestHub(t)
	entry := hub.addNode(t, "entry-1", types.RoleEntry)
	exit := hub.addNode(t, "exit-1", types.RoleExit)

	tunnel := newFakeTunnel()
	svc := newService(t, tunnel)
	ctx := context.Background()
	if _, err := svc.ConnectHub(ctx, hub.server.URL); err != nil {
		t.Fatalf("connect hub: %v", err)
	}
	fundService(t, hub, svc, 256)
	if _, err := svc.Connect(ctx, 32); err != nil {
		t.Fatalf("connect: %v", err)
	}

	session, err := svc.Extend(ctx, 32)
	if err != nil {
		t.Fatalf("extend: %v", err)
	}
	if session.SpentSats != 128 {
		t.Errorf("spent = %d, want 128 after one top-up", session.SpentSats)
	}
	if entry.connectCount() != 2 || exit.connectCount() != 2 {
		t.Errorf("connect calls: entry=%d exit=%d, want 2 each", entry.connectCount(), exit.connectCount())
	}
	if len(tunnel.ups()) != 1 || tunnel.downs() != 0 {
		t.Errorf("tunnel ups=%d downs=%d, want 1 and 0", len(tunnel.ups()), tunnel.downs())
	}
	if svc.State() != app.StateConnected {
		t.Errorf("state = %q, want connected", svc.State())
	}
	if balance, _ := svc.Balance(); balance != 128 {
		t.Errorf("balance = %d, want 128", balance)
	}
}

func TestExtendRequiresALiveSession(t *testing.T) {
	hub := newTestHub(t)
	svc := newService(t, newFakeTunnel())
	if _, err := svc.ConnectHub(context.Background(), hub.server.URL); err != nil {
		t.Fatalf("connect hub: %v", err)
	}
	if _, err := svc.Extend(context.Background(), 32); !errors.Is(err, app.ErrNotConnected) {
		t.Fatalf("got %v, want ErrNotConnected", err)
	}
}
