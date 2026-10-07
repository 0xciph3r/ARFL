// Package app provides the headless, UI-agnostic core of the ARFL client.
//
// It wraps the wallet, node selector and node connector behind a small API
// that both the CLI and the desktop app bind to. Nothing here imports a UI
// toolkit or touches a network interface — tunnel bring-up is delegated to a
// Tunnel supplied by the caller, so the service can run unprivileged while a
// separate helper does the privileged work.
package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Radi-Labs/ARFL/internal/client"
	"github.com/Radi-Labs/ARFL/internal/discovery"
	"github.com/Radi-Labs/ARFL/internal/nostr"
	"github.com/Radi-Labs/ARFL/internal/wallet"
	"github.com/Radi-Labs/ARFL/pkg/protocol"
	"github.com/Radi-Labs/ARFL/pkg/types"
	"github.com/elnosh/gonuts/cashu"
)

// Errors returned by the service.
var (
	ErrNoHub                = errors.New("no hub connected")
	ErrAlreadyOn            = errors.New("already connected")
	ErrNotConnected         = errors.New("not connected")
	ErrAmountTooSmall       = errors.New("amount must be greater than zero")
	ErrTransportUnsupported = errors.New("selected transport is not supported by this runtime")
	ErrPinnedNodeOffline    = errors.New("a node you picked is not online at this hub")
	ErrPinnedSameOperator   = errors.New("entry and exit must belong to different operators")
)

// State is the connection state machine exposed to the UI.
type State string

const (
	StateDisconnected  State = "disconnected"
	StateConnecting    State = "connecting"
	StateConnected     State = "connected"
	StateDisconnecting State = "disconnecting"

	DiscoverySourceHub   = "hub"
	DiscoverySourceNostr = "nostr"
)

// Tunnel brings a two-hop WireGuard tunnel up and down.
//
// Implementations are platform-specific and generally privileged. The service
// treats this as an opaque dependency so it can be stubbed in tests and left
// nil in builds that only need wallet and discovery features.
type Tunnel interface {
	// PublicKey returns the client's WireGuard public key (base64). Nodes need
	// this before the tunnel exists, so it must be available at any time.
	PublicKey() (string, error)
	// Preflight reports whether the tunnel could be brought up right now,
	// without changing anything.
	//
	// Connect pays both nodes before calling Up, and tokens a node has accepted
	// are burned at the hub. Checking first — for example that the process is
	// privileged enough to edit the routing table — turns a silent loss of
	// funds into a refusal to start.
	Preflight() error
	// Up establishes the nested tunnel from node-issued configuration.
	Up(ctx context.Context, cfg TunnelConfig) error
	// Down tears the tunnel down and restores the previous routing state.
	Down(ctx context.Context) error
}

// EndpointValidator is implemented by a Tunnel that can reject unusable node
// endpoints up front. Connect calls it before reserving proofs, because a node
// that has accepted its proofs has already burned them at the hub.
type EndpointValidator interface {
	ValidateEndpoints(entryEndpoint, exitEndpoint string) error
}

// StagedTunnel can bring up the outer and inner hops separately.
//
// Service uses this to connect the exit node only after the outer hop exists,
// so the exit /cashu-connect request is sourced from inside the tunnel.
type StagedTunnel interface {
	UpOuter(ctx context.Context, cfg TunnelConfig) error
	UpInner(ctx context.Context, cfg TunnelConfig) error
}

// HopKeyProvider supplies distinct client WireGuard keys for each hop.
//
// The service calls PrepareHopKeys once per connect attempt and ResetHopKeys
// on failed attempts so retries do not reuse identifiers.
type HopKeyProvider interface {
	PrepareHopKeys() (entryPub, exitPub string, err error)
	ResetHopKeys()
}

// TunnelConfig is everything a Tunnel needs to establish both hops.
type TunnelConfig struct {
	Entry          HopConfig `json:"entry"`
	Exit           HopConfig `json:"exit"`
	ClientKey      string    `json:"client_key"`
	EntryClientKey string    `json:"entry_client_key,omitempty"`
	ExitClientKey  string    `json:"exit_client_key,omitempty"`
	// OuterPinnedEndpoints are additional host:port endpoints that must be
	// pinned through the outer tunnel during staged setup.
	//
	// This keeps HTTP provisioning endpoints and the real exit WireGuard
	// endpoint both routed through the entry hop when they differ by host.
	OuterPinnedEndpoints []string        `json:"outer_pinned_endpoints,omitempty"`
	Transport            types.Transport `json:"transport,omitempty"`
}

// HopConfig describes one leg of the two-hop tunnel.
type HopConfig struct {
	NodeID       string `json:"node_id"`
	Endpoint     string `json:"endpoint"`
	NodeWGPubkey string `json:"node_wg_pubkey"`
	TunnelIP     string `json:"tunnel_ip"`
	BytesAllowed int64  `json:"bytes_allowed"`
}

// HubStatus summarises a connected hub for display.
type HubStatus struct {
	URL       string `json:"url"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	KeysetID  string `json:"keyset_id"`
	Balance   uint64 `json:"balance_sats"`
	NodeCount int    `json:"node_count"`
}

// Invoice is a pending bandwidth purchase awaiting Lightning payment.
type Invoice struct {
	QuoteID string `json:"quote_id"`
	Bolt11  string `json:"bolt11"`
	// PaymentHash lets the user reconcile the payment against their Lightning
	// wallet without having to decode the BOLT11 themselves.
	PaymentHash string    `json:"payment_hash"`
	AmountSat   uint64    `json:"amount_sats"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// Session describes an active two-hop connection.
type Session struct {
	State     State        `json:"state"`
	Config    TunnelConfig `json:"config"`
	SpentSats uint64       `json:"spent_sats"`
	StartedAt time.Time    `json:"started_at"`
}

// Config configures a Service.
type Config struct {
	// StorePath is where encrypted proofs live. Empty uses the OS default.
	StorePath string
	// Passphrase encrypts the proof store. Required.
	Passphrase string
	// Tunnel performs privileged network setup. Optional — a service without
	// one can still mint, hold balance and browse nodes, but cannot connect.
	Tunnel Tunnel
	// PollInterval controls how often a pending invoice is re-checked.
	PollInterval time.Duration
	// PreferredTransports controls node-pair transport negotiation order.
	// Empty defaults to WireGuard-only.
	PreferredTransports []types.Transport
	// AllowedTransports restricts transports eligible for pair selection.
	// Empty defaults to all known transports.
	AllowedTransports []types.Transport
	// NostrRelays is used when TokenDelivery is "nip44".
	NostrRelays []string
	// TokenDelivery selects how proofs are delivered to nodes.
	// Empty defaults to "http".
	TokenDelivery client.TokenDeliveryMode
	// DiscoverySource controls where the client builds its node index from.
	// "hub" uses GET /nodes. "nostr" subscribes to relay announcements.
	// Empty defaults to "hub".
	DiscoverySource string
	// TrustedHubPubkeys is required for discovery_source=nostr so attestations
	// can be verified client-side.
	TrustedHubPubkeys []string
	// DiscoveryWindow bounds relay sampling time for each node-list fetch.
	// Zero defaults to 2 seconds.
	DiscoveryWindow time.Duration
}

// Service is the headless ARFL client.
//
// All exported methods are safe for concurrent use; the desktop UI calls them
// from arbitrary goroutines.
type Service struct {
	mu sync.Mutex

	store     *wallet.EncryptedProofStore
	tunnel    Tunnel
	connector *client.CashuConnector

	pollInterval time.Duration
	preferred    []types.Transport
	allowed      map[types.Transport]struct{}
	nostrRelays  []string
	delivery     client.TokenDeliveryMode
	discoverySrc string
	trustedHubs  []string
	discoveryTTL time.Duration

	// Hub-scoped state, replaced wholesale by ConnectHub.
	wallet   *wallet.Wallet
	selector *client.NodeSelector
	hubInfo  *wallet.HubInfo

	state   State
	session *Session

	// pinned replaces the random pair with one the user chose. It is cleared
	// on every hub switch, since node IDs only mean something at one hub.
	pinned *PinnedPair

	// route is what Extend needs to pay the live session's nodes again.
	route *liveRoute
}

// liveRoute is the connected pair and the client keys used with it.
type liveRoute struct {
	pair          client.NodePair
	exitConnectIP string
	entryKey      string
	exitKey       string
}

// PinnedPair names the entry and exit nodes a user chose by ID.
type PinnedPair struct {
	EntryID string `json:"entry_id"`
	ExitID  string `json:"exit_id"`
}

// New opens the proof store and returns a service with no hub connected.
func New(cfg Config) (*Service, error) {
	if cfg.Passphrase == "" {
		return nil, fmt.Errorf("passphrase is required to encrypt the proof store")
	}

	path := cfg.StorePath
	if path == "" {
		var err error
		path, err = wallet.DefaultStorePath()
		if err != nil {
			return nil, fmt.Errorf("resolve store path: %w", err)
		}
	}

	store, err := wallet.OpenProofStore(path, cfg.Passphrase)
	if err != nil {
		return nil, fmt.Errorf("open proof store: %w", err)
	}

	interval := cfg.PollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}

	delivery := cfg.TokenDelivery
	if delivery == "" {
		delivery = client.TokenDeliveryHTTP
	}
	if delivery != client.TokenDeliveryHTTP && delivery != client.TokenDeliveryNIP44 {
		return nil, fmt.Errorf("unsupported token delivery mode %q", delivery)
	}
	nostrRelays := sanitizeRelays(cfg.NostrRelays)
	if delivery == client.TokenDeliveryNIP44 && len(nostrRelays) == 0 {
		return nil, fmt.Errorf("token delivery mode %q requires at least one relay URL", delivery)
	}
	discoverySrc := strings.ToLower(strings.TrimSpace(cfg.DiscoverySource))
	if discoverySrc == "" {
		discoverySrc = DiscoverySourceHub
	}
	if discoverySrc != DiscoverySourceHub && discoverySrc != DiscoverySourceNostr {
		return nil, fmt.Errorf("unsupported discovery source %q", cfg.DiscoverySource)
	}
	if delivery == client.TokenDeliveryNIP44 && discoverySrc != DiscoverySourceNostr {
		return nil, fmt.Errorf("token delivery mode %q requires discovery_source=%q to verify node identities", delivery, DiscoverySourceNostr)
	}
	trustedHubs := sanitizeHubPubkeys(cfg.TrustedHubPubkeys)
	if discoverySrc == DiscoverySourceNostr {
		if len(nostrRelays) == 0 {
			return nil, fmt.Errorf("discovery source %q requires at least one relay URL", discoverySrc)
		}
		if len(trustedHubs) == 0 {
			return nil, fmt.Errorf("discovery source %q requires at least one trusted hub pubkey", discoverySrc)
		}
	}
	discoveryTTL := cfg.DiscoveryWindow
	if discoveryTTL <= 0 {
		discoveryTTL = 2 * time.Second
	}

	return &Service{
		store:        store,
		tunnel:       cfg.Tunnel,
		connector:    client.NewCashuConnector(),
		pollInterval: interval,
		preferred:    sanitizePreferredTransports(cfg.PreferredTransports),
		allowed:      sanitizeAllowedTransports(cfg.AllowedTransports),
		nostrRelays:  nostrRelays,
		delivery:     delivery,
		discoverySrc: discoverySrc,
		trustedHubs:  trustedHubs,
		discoveryTTL: discoveryTTL,
		state:        StateDisconnected,
	}, nil
}

// ConnectHub points the service at a hub. Any previously connected hub is
// replaced, but its proofs stay in the store — proofs are only spendable at
// the mint that issued them, so they are kept until that hub is used again.
func (s *Service) ConnectHub(ctx context.Context, hubURL string) (*HubStatus, error) {
	mintClient, err := wallet.NewMintClient(hubURL)
	if err != nil {
		return nil, err
	}

	info, err := mintClient.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("hub unreachable: %w", err)
	}

	keyset, err := mintClient.ActiveKeyset(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch hub keyset: %w", err)
	}

	w, err := wallet.NewWallet(mintClient, s.store)
	if err != nil {
		return nil, err
	}

	selector := client.NewNodeSelectorWithPolicy(mintClient.HubURL(), s.preferred, mapKeys(s.allowed))

	s.mu.Lock()
	if s.state != StateDisconnected {
		s.mu.Unlock()
		return nil, fmt.Errorf("disconnect the active session before switching hubs")
	}
	s.wallet = w
	s.selector = selector
	s.pinned = nil
	s.hubInfo = info
	s.mu.Unlock()

	balance, err := w.Balance()
	if err != nil {
		return nil, fmt.Errorf("read balance: %w", err)
	}

	status := &HubStatus{
		URL:      mintClient.HubURL(),
		Name:     info.Name,
		Version:  info.Version,
		KeysetID: keyset.ID,
		Balance:  balance,
	}

	// Node count is informational — minting still works if discovery is down.
	if nodes, err := s.fetchNodes(ctx); err == nil {
		status.NodeCount = len(nodes)
	}

	return status, nil
}

// HubURL returns the currently connected hub, or "" if none.
func (s *Service) HubURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wallet == nil {
		return ""
	}
	return s.wallet.HubURL()
}

// Balance returns unspent sats held for the connected hub.
func (s *Service) Balance() (uint64, error) {
	w, err := s.currentWallet()
	if err != nil {
		return 0, err
	}
	return w.Balance()
}

// Purchase requests a Lightning invoice for amountSats of bandwidth credit.
// The caller pays the returned BOLT11, then calls AwaitPurchase.
func (s *Service) Purchase(ctx context.Context, amountSats uint64) (*Invoice, error) {
	w, err := s.currentWallet()
	if err != nil {
		return nil, err
	}

	quote, err := w.RequestQuote(ctx, amountSats)
	if err != nil {
		return nil, err
	}

	return &Invoice{
		QuoteID:     quote.ID,
		Bolt11:      quote.PaymentRequest,
		PaymentHash: quote.PaymentHash,
		AmountSat:   quote.Amount,
		ExpiresAt:   time.Unix(quote.Expiry, 0),
	}, nil
}

// AwaitPurchase blocks until the invoice is paid, then mints and stores the
// proofs, returning the new balance.
//
// Callers should pass a cancellable context so the user can abandon a
// purchase; an unpaid quote simply expires at the hub.
func (s *Service) AwaitPurchase(ctx context.Context, quoteID string) (uint64, error) {
	w, err := s.currentWallet()
	if err != nil {
		return 0, err
	}

	quote, err := w.AwaitPayment(ctx, quoteID, s.pollInterval)
	if err != nil {
		return 0, err
	}

	if _, err := w.Mint(ctx, quote); err != nil {
		return 0, err
	}

	return w.Balance()
}

// ListNodes returns the online nodes the hub knows about.
func (s *Service) ListNodes(ctx context.Context) ([]types.NodeInfo, error) {
	return s.fetchNodes(ctx)
}

// SelectPair picks an entry/exit pair client-side. The hub never learns the
// choice, which is what keeps payment unlinkable from routing.
func (s *Service) SelectPair(ctx context.Context) (*client.NodePair, error) {
	// An allowlist that was supplied but names no supported transport means
	// "nothing is allowed". The selector reads an empty set as "unrestricted",
	// so this must be refused here rather than passed down.
	if len(s.allowed) == 0 {
		return nil, fmt.Errorf("%w (transport policy: allowed transports contain no supported transport)", client.ErrNoCompatiblePair)
	}
	nodes, err := s.fetchNodes(ctx)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	pinned := s.pinned
	s.mu.Unlock()
	if pinned != nil {
		if nodes, err = restrictToPinned(nodes, *pinned); err != nil {
			return nil, err
		}
	}
	return client.PairNodesWithPolicyForDelivery(nodes, s.preferred, s.allowed, s.delivery)
}

// restrictToPinned narrows the node list to the user's chosen entry and exit,
// each reduced to that one role, so the pair still passes the same transport,
// delivery and endpoint checks as a random one.
func restrictToPinned(nodes []types.NodeInfo, pin PinnedPair) ([]types.NodeInfo, error) {
	var entry, exit *types.NodeInfo
	for i := range nodes {
		n := &nodes[i]
		if n.ID == pin.EntryID && (n.Role == types.RoleEntry || n.Role == types.RoleBoth) {
			entry = n
		}
		if n.ID == pin.ExitID && (n.Role == types.RoleExit || n.Role == types.RoleBoth) {
			exit = &nodes[i]
		}
	}
	if entry == nil || exit == nil {
		return nil, ErrPinnedNodeOffline
	}
	if sameOperator(*entry, *exit) {
		return nil, ErrPinnedSameOperator
	}
	e, x := *entry, *exit
	e.Role = types.RoleEntry
	x.Role = types.RoleExit
	return []types.NodeInfo{e, x}, nil
}

// sameOperator compares the operator the hub assigned in each node's
// attestation, since one operator can run many nodes with different keys.
// Without attestation data it falls back to the node keys.
func sameOperator(a, b types.NodeInfo) bool {
	if a.OperatorID != "" && b.OperatorID != "" {
		return a.OperatorID == b.OperatorID
	}
	return a.NostrPubkey == b.NostrPubkey
}

// SetPinnedPair makes every later connect use these two nodes. Pass nil to go
// back to a random pair.
func (s *Service) SetPinnedPair(pin *PinnedPair) error {
	if pin != nil && (pin.EntryID == "" || pin.ExitID == "") {
		return fmt.Errorf("both an entry and an exit node are required")
	}
	if pin != nil && pin.EntryID == pin.ExitID {
		return ErrPinnedSameOperator
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if pin == nil {
		s.pinned = nil
		return nil
	}
	cp := *pin
	s.pinned = &cp
	return nil
}

// SetTunnel swaps the tunnel, for example once the privileged helper has
// been installed. It is refused during a session.
func (s *Service) SetTunnel(t Tunnel) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateDisconnected {
		return fmt.Errorf("disconnect before changing how the tunnel runs")
	}
	s.tunnel = t
	return nil
}

// Snapshot returns every held proof keyed by hub URL, for backups.
func (s *Service) Snapshot() map[string]cashu.Proofs {
	return s.store.Snapshot()
}

// MoveHub re-files proofs held under an old hub address to its new one.
func (s *Service) MoveHub(from, to string) error {
	return s.store.MoveHub(from, to)
}

// ImportProofs adds restored proofs for a hub to the local vault.
func (s *Service) ImportProofs(hubURL string, proofs cashu.Proofs) error {
	return s.store.Add(hubURL, proofs)
}

// PinnedPair returns the user's chosen pair, or nil when pairing is random.
func (s *Service) PinnedPair() *PinnedPair {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pinned == nil {
		return nil
	}
	cp := *s.pinned
	return &cp
}

// State reports the current connection state.
func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Session returns a copy of the active session, or nil when disconnected.
func (s *Service) Session() *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session == nil {
		return nil
	}
	cp := *s.session
	return &cp
}

// Connect spends perHopSats at each of two client-selected nodes and brings
// the tunnel up.
//
// Each hop is paid with its own disjoint set of proofs. Reserving once and
// splitting the list would risk handing overlapping proofs to both nodes, and
// the second node would reject them as a double-spend.
func (s *Service) Connect(ctx context.Context, perHopSats uint64) (*Session, error) {
	if perHopSats == 0 {
		return nil, ErrAmountTooSmall
	}

	w, err := s.currentWallet()
	if err != nil {
		return nil, err
	}

	if err := s.beginConnect(); err != nil {
		return nil, err
	}

	session, err := s.connect(ctx, w, perHopSats)
	if err != nil {
		s.setState(StateDisconnected)
		return nil, err
	}

	s.mu.Lock()
	s.state = StateConnected
	s.session = session
	s.mu.Unlock()

	return session, nil
}

func (s *Service) connect(ctx context.Context, w *wallet.Wallet, perHopSats uint64) (*Session, error) {
	pair, err := s.SelectPair(ctx)
	if err != nil {
		if errors.Is(err, client.ErrNoCompatiblePair) {
			return nil, fmt.Errorf("%w (transport policy: preferred=%s allowed=%s)", err, formatTransportList(s.preferred), formatTransportSet(s.allowed))
		}
		return nil, err
	}
	if pair.Transport == "" {
		pair.Transport = types.TransportWireGuard
	}
	if pair.Transport != types.TransportWireGuard {
		return nil, fmt.Errorf("%w: selected=%s (transport policy: preferred=%s allowed=%s)", ErrTransportUnsupported, pair.Transport, formatTransportList(s.preferred), formatTransportSet(s.allowed))
	}
	if pair.Entry.Endpoint == "" || pair.Exit.Endpoint == "" {
		return nil, fmt.Errorf("selected pair missing endpoint for transport %s", pair.Transport)
	}
	if s.delivery == client.TokenDeliveryHTTP && (pair.Entry.ConnectURL == "" || pair.Exit.ConnectURL == "") {
		return nil, fmt.Errorf("selected pair missing connect URL for transport %s", pair.Transport)
	}
	if s.delivery == client.TokenDeliveryNIP44 && (pair.Entry.NostrPubkey == "" || pair.Exit.NostrPubkey == "") {
		return nil, fmt.Errorf("selected pair missing nostr pubkey for NIP-44 delivery")
	}
	staged, stagedOK := s.tunnel.(StagedTunnel)
	if s.delivery == client.TokenDeliveryHTTP && !stagedOK {
		return nil, fmt.Errorf("token delivery mode %q requires staged tunnel support to avoid exit-side IP exposure", s.delivery)
	}
	var exitConnectIP, exitConnectEndpoint string
	if s.delivery == client.TokenDeliveryHTTP {
		exitConnectIP, exitConnectEndpoint, err = resolveConnectIPv4(ctx, pair.Exit.ConnectURL)
		if err != nil {
			return nil, fmt.Errorf("resolve exit connect URL before payment: %w", err)
		}
	}

	entryClientKey, exitClientKey, resetHopKeys, err := s.prepareConnectKeys()
	if err != nil {
		return nil, err
	}
	connectSucceeded := false
	defer func() {
		if connectSucceeded || resetHopKeys == nil {
			return
		}
		resetHopKeys()
	}()

	// Check the tunnel can actually be established before spending anything.
	// Everything below this line costs the user money that cannot be recovered
	// once a node has accepted its proofs.
	if err := s.tunnel.Preflight(); err != nil {
		return nil, fmt.Errorf("cannot establish tunnel: %w", err)
	}

	if v, ok := s.tunnel.(EndpointValidator); ok {
		if err := v.ValidateEndpoints(pair.Entry.Endpoint, pair.Exit.Endpoint); err != nil {
			return nil, fmt.Errorf("selected pair has an unusable endpoint: %w", err)
		}
	}

	entryProofs, err := w.Reserve(ctx, perHopSats)
	if err != nil {
		return nil, fmt.Errorf("reserve entry payment: %w", err)
	}

	exitProofs, err := w.Reserve(ctx, perHopSats)
	if err != nil {
		// Entry proofs were never presented, so they are still spendable.
		if rerr := w.Release(entryProofs); rerr != nil {
			return nil, fmt.Errorf("reserve exit payment: %w (entry proofs could not be returned to the store: %v)", err, rerr)
		}
		return nil, fmt.Errorf("reserve exit payment: %w", err)
	}

	var tokenSender *client.TokenSender
	var relayPool *nostr.RelayPool
	if s.delivery == client.TokenDeliveryNIP44 {
		relayPool = nostr.NewRelayPool(s.nostrRelays)
		if err := relayPool.Connect(ctx); err != nil {
			if rerr := w.Release(append(append(cashu.Proofs{}, entryProofs...), exitProofs...)); rerr != nil {
				return nil, fmt.Errorf("connect to relays for token delivery: %w (reserved proofs could not be returned to the store: %v)", err, rerr)
			}
			return nil, fmt.Errorf("connect to relays for token delivery: %w", err)
		}
		defer relayPool.Close()
		tokenSender = client.NewTokenSender(relayPool)
	}

	entryRes, err := s.connectNode(ctx, tokenSender, pair.Entry.ConnectURL, pair.Entry.NostrPubkey, "", entryProofs, entryClientKey, "entry")
	if err != nil {
		entrySpent := shouldTreatProofsAsSpent(err)
		if rerr := s.refundUnspentProofs(w, entryProofs, exitProofs, entrySpent, false); rerr != nil {
			return nil, fmt.Errorf("%w (unspent proofs could not be returned to the store: %v)", err, rerr)
		}
		return nil, err
	}

	cfg := TunnelConfig{
		ClientKey:      entryClientKey,
		EntryClientKey: entryClientKey,
		ExitClientKey:  exitClientKey,
		Transport:      pair.Transport,
		Entry: HopConfig{
			NodeID:       pair.Entry.ID,
			Endpoint:     pair.Entry.Endpoint,
			NodeWGPubkey: entryRes.NodeWGPubkey,
			TunnelIP:     entryRes.TunnelIP,
			BytesAllowed: entryRes.BytesAllowed,
		},
		Exit: HopConfig{
			NodeID:       pair.Exit.ID,
			Endpoint:     pair.Exit.Endpoint,
			NodeWGPubkey: pair.Exit.WGPubkey,
		},
	}

	// Route exit provisioning through the established outer hop when supported
	// and when proofs are delivered via direct HTTP.
	stageExitProvision := stagedOK && s.delivery == client.TokenDeliveryHTTP
	outerUp := false
	if stageExitProvision {
		outerCfg := cfg
		outerCfg.OuterPinnedEndpoints = []string{exitConnectEndpoint}
		if err := staged.UpOuter(ctx, outerCfg); err != nil {
			if rerr := s.refundUnspentProofs(w, entryProofs, exitProofs, true, false); rerr != nil {
				return nil, fmt.Errorf("bring outer tunnel up: %w (exit proofs could not be returned to the store: %v)", err, rerr)
			}
			return nil, fmt.Errorf("bring outer tunnel up: %w", err)
		}
		outerUp = true
	}

	exitRes, err := s.connectNode(ctx, tokenSender, pair.Exit.ConnectURL, pair.Exit.NostrPubkey, exitConnectIP, exitProofs, exitClientKey, "exit")
	if err != nil {
		if outerUp {
			if derr := s.tunnel.Down(ctx); derr != nil {
				err = fmt.Errorf("%w (cleanup after failed exit connect: %v)", err, derr)
			}
		}
		exitSpent := shouldTreatProofsAsSpent(err)
		if rerr := s.refundUnspentProofs(w, entryProofs, exitProofs, true, exitSpent); rerr != nil {
			return nil, fmt.Errorf("%w (unspent proofs could not be returned to the store: %v)", err, rerr)
		}
		return nil, err
	}

	cfg.Exit.NodeWGPubkey = exitRes.NodeWGPubkey
	cfg.Exit.TunnelIP = exitRes.TunnelIP
	cfg.Exit.BytesAllowed = exitRes.BytesAllowed

	if stageExitProvision {
		if err := staged.UpInner(ctx, cfg); err != nil {
			downErr := s.tunnel.Down(ctx)
			if downErr != nil {
				return nil, fmt.Errorf("bring inner tunnel up: %w (cleanup failed: %v)", err, downErr)
			}
			return nil, fmt.Errorf("bring inner tunnel up: %w", err)
		}
	} else if err := s.tunnel.Up(ctx, cfg); err != nil {
		return nil, fmt.Errorf("bring tunnel up: %w", err)
	}

	connectSucceeded = true

	s.mu.Lock()
	s.route = &liveRoute{pair: *pair, exitConnectIP: exitConnectIP, entryKey: entryClientKey, exitKey: exitClientKey}
	s.mu.Unlock()

	return &Session{
		State:     StateConnected,
		Config:    cfg,
		SpentSats: perHopSats * 2,
		StartedAt: time.Now(),
	}, nil
}

// Disconnect tears the tunnel down and clears the session.
func (s *Service) Disconnect(ctx context.Context) error {
	s.mu.Lock()
	if s.state != StateConnected {
		s.mu.Unlock()
		return ErrNotConnected
	}
	s.state = StateDisconnecting
	s.mu.Unlock()

	var err error
	if s.tunnel != nil {
		err = s.tunnel.Down(ctx)
	}

	// Clear the session either way: leaving it marked connected after a failed
	// teardown would strand the UI with no way to retry.
	s.mu.Lock()
	s.state = StateDisconnected
	s.session = nil
	s.route = nil
	s.mu.Unlock()

	if err != nil {
		return fmt.Errorf("tear tunnel down: %w", err)
	}
	return nil
}

// ErrExtendMovedTunnel means a node answered a top-up with a different tunnel
// address, so the session must be rebuilt rather than extended.
var ErrExtendMovedTunnel = errors.New("a node moved the session to a new tunnel address")

// Extend pays both nodes of the live session for another allowance while the
// tunnel stays up. Tearing the tunnel down to buy more would send traffic out
// unprotected in the meantime.
//
// Nodes treat a payment from a peer they already serve as a top-up: the peer
// keeps its tunnel address and its quota is replaced with the new allowance.
// The exit is paid through the outer tunnel, as during connect.
func (s *Service) Extend(ctx context.Context, perHopSats uint64) (*Session, error) {
	if perHopSats == 0 {
		return nil, ErrAmountTooSmall
	}
	w, err := s.currentWallet()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	route, session := s.route, s.session
	if s.state != StateConnected || route == nil || session == nil {
		s.mu.Unlock()
		return nil, ErrNotConnected
	}
	s.mu.Unlock()

	entryProofs, err := w.Reserve(ctx, perHopSats)
	if err != nil {
		return nil, fmt.Errorf("reserve entry payment: %w", err)
	}
	exitProofs, err := w.Reserve(ctx, perHopSats)
	if err != nil {
		if rerr := w.Release(entryProofs); rerr != nil {
			return nil, fmt.Errorf("reserve exit payment: %w (entry proofs could not be returned to the store: %v)", err, rerr)
		}
		return nil, fmt.Errorf("reserve exit payment: %w", err)
	}

	var tokenSender *client.TokenSender
	if s.delivery == client.TokenDeliveryNIP44 {
		pool := nostr.NewRelayPool(s.nostrRelays)
		if err := pool.Connect(ctx); err != nil {
			if rerr := w.Release(append(append(cashu.Proofs{}, entryProofs...), exitProofs...)); rerr != nil {
				return nil, fmt.Errorf("connect to relays for token delivery: %w (reserved proofs could not be returned to the store: %v)", err, rerr)
			}
			return nil, fmt.Errorf("connect to relays for token delivery: %w", err)
		}
		defer pool.Close()
		tokenSender = client.NewTokenSender(pool)
	}

	pair := route.pair
	entryRes, err := s.connectNode(ctx, tokenSender, pair.Entry.ConnectURL, pair.Entry.NostrPubkey, "", entryProofs, route.entryKey, "entry")
	if err != nil {
		if rerr := s.refundUnspentProofs(w, entryProofs, exitProofs, shouldTreatProofsAsSpent(err), false); rerr != nil {
			return nil, fmt.Errorf("%w (unspent proofs could not be returned to the store: %v)", err, rerr)
		}
		return nil, err
	}
	exitRes, err := s.connectNode(ctx, tokenSender, pair.Exit.ConnectURL, pair.Exit.NostrPubkey, route.exitConnectIP, exitProofs, route.exitKey, "exit")
	if err != nil {
		if rerr := s.refundUnspentProofs(w, entryProofs, exitProofs, true, shouldTreatProofsAsSpent(err)); rerr != nil {
			return nil, fmt.Errorf("%w (unspent proofs could not be returned to the store: %v)", err, rerr)
		}
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session == nil {
		return nil, ErrNotConnected
	}
	if entryRes.TunnelIP != s.session.Config.Entry.TunnelIP || exitRes.TunnelIP != s.session.Config.Exit.TunnelIP {
		return nil, ErrExtendMovedTunnel
	}
	s.session.Config.Entry.BytesAllowed = entryRes.BytesAllowed
	s.session.Config.Exit.BytesAllowed = exitRes.BytesAllowed
	s.session.SpentSats += perHopSats * 2
	cp := *s.session
	return &cp, nil
}

// SetTrustedHubPubkeys replaces the hub keys trusted to sign node
// attestations, for example when the trusted hub list is refreshed.
func (s *Service) SetTrustedHubPubkeys(keys []string) {
	clean := sanitizeHubPubkeys(keys)
	s.mu.Lock()
	s.trustedHubs = clean
	s.mu.Unlock()
}

// Close releases resources. Proofs are already durable on disk.
func (s *Service) Close(ctx context.Context) error {
	if s.State() == StateConnected {
		return s.Disconnect(ctx)
	}
	return nil
}

func (s *Service) beginConnect() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.state {
	case StateConnected:
		return ErrAlreadyOn
	case StateDisconnected:
		s.state = StateConnecting
		return nil
	default:
		return fmt.Errorf("connection already in progress")
	}
}

func (s *Service) setState(st State) {
	s.mu.Lock()
	s.state = st
	s.mu.Unlock()
}

func (s *Service) prepareConnectKeys() (entryKey string, exitKey string, reset func(), err error) {
	if s.tunnel == nil {
		return "", "", nil, fmt.Errorf("no tunnel configured: cannot supply a WireGuard public key")
	}

	if provider, ok := s.tunnel.(HopKeyProvider); ok {
		entryKey, exitKey, err = provider.PrepareHopKeys()
		if err != nil {
			return "", "", nil, fmt.Errorf("prepare per-hop WireGuard keys: %w", err)
		}
		if entryKey == "" || exitKey == "" {
			return "", "", nil, fmt.Errorf("prepare per-hop WireGuard keys: tunnel returned an empty key")
		}
		return entryKey, exitKey, provider.ResetHopKeys, nil
	}

	key, err := s.tunnel.PublicKey()
	if err != nil {
		return "", "", nil, fmt.Errorf("read WireGuard public key: %w", err)
	}
	if key == "" {
		return "", "", nil, fmt.Errorf("tunnel returned an empty WireGuard public key")
	}
	return key, key, nil, nil
}

func (s *Service) currentWallet() (*wallet.Wallet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wallet == nil {
		return nil, ErrNoHub
	}
	return s.wallet, nil
}

func (s *Service) currentSelector() (*client.NodeSelector, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.selector == nil {
		return nil, ErrNoHub
	}
	return s.selector, nil
}

func (s *Service) fetchNodes(ctx context.Context) ([]types.NodeInfo, error) {
	switch s.discoverySource() {
	case DiscoverySourceNostr:
		return s.fetchNodesFromRelays(ctx)
	default:
		sel, err := s.currentSelector()
		if err != nil {
			return nil, err
		}
		return sel.FetchNodes(ctx)
	}
}

func (s *Service) fetchNodesFromRelays(ctx context.Context) ([]types.NodeInfo, error) {
	relays, hubs, window, err := s.relayDiscoveryConfig()
	if err != nil {
		return nil, err
	}

	pool := nostr.NewRelayPool(relays)
	if err := pool.Connect(ctx); err != nil {
		return nil, fmt.Errorf("%w: connect to relays: %v", client.ErrFetchFailed, err)
	}
	defer pool.Close()

	index := discovery.NewNodeIndex(hubs)
	since := time.Now().Add(-discovery.OnlineTTL).Unix()
	subID := fmt.Sprintf("arfl-client-index-%d", time.Now().UnixNano())
	events, err := pool.Subscribe(ctx, subID, nostr.Filter{
		Kinds: []int{protocol.NostrKindNodeAnnouncement},
		Since: &since,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: subscribe to relays: %v", client.ErrFetchFailed, err)
	}

	timer := time.NewTimer(window)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			nodes := indexedToNodeInfos(index.ListOnline())
			if len(nodes) > 0 {
				return nodes, nil
			}
			return nil, fmt.Errorf("%w: %v", client.ErrFetchFailed, ctx.Err())
		case ev, ok := <-events:
			if !ok {
				nodes := indexedToNodeInfos(index.ListOnline())
				if len(nodes) == 0 {
					return nil, fmt.Errorf("%w: relay subscription closed before any node announcements", client.ErrFetchFailed)
				}
				return nodes, nil
			}
			if ev == nil {
				continue
			}
			_ = index.ProcessEvent(ev)
		case <-timer.C:
			nodes := indexedToNodeInfos(index.ListOnline())
			if len(nodes) == 0 {
				return nil, fmt.Errorf("%w: no online nodes received from relays within %s", client.ErrFetchFailed, window)
			}
			return nodes, nil
		}
	}
}

func indexedToNodeInfos(in []*discovery.IndexedNode) []types.NodeInfo {
	if len(in) == 0 {
		return nil
	}
	out := make([]types.NodeInfo, 0, len(in))
	for _, node := range in {
		if node == nil {
			continue
		}
		if node.Event == nil {
			continue
		}
		info := node.Info
		info.NostrPubkey = node.Event.Pubkey
		info.OperatorID = ""
		if node.Attestation != nil {
			info.OperatorID = node.Attestation.OperatorID
		}
		out = append(out, info)
	}
	return out
}

func (s *Service) discoverySource() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.discoverySrc
}

func (s *Service) relayDiscoveryConfig() ([]string, []string, time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wallet == nil {
		return nil, nil, 0, ErrNoHub
	}
	if len(s.nostrRelays) == 0 {
		return nil, nil, 0, fmt.Errorf("no relays configured for nostr discovery")
	}
	if len(s.trustedHubs) == 0 {
		return nil, nil, 0, fmt.Errorf("no trusted hub pubkeys configured for nostr discovery")
	}
	return append([]string(nil), s.nostrRelays...), append([]string(nil), s.trustedHubs...), s.discoveryTTL, nil
}

func (s *Service) connectNode(
	ctx context.Context,
	tokenSender *client.TokenSender,
	connectURL string,
	nodePubkey string,
	pinnedIP string,
	proofs cashu.Proofs,
	clientWGPubkey string,
	role string,
) (*client.ConnectResult, error) {
	switch s.delivery {
	case client.TokenDeliveryHTTP:
		if connectURL == "" {
			return nil, fmt.Errorf("%s node missing connect URL for HTTP token delivery", role)
		}
		var res *client.ConnectResult
		var err error
		if pinnedIP != "" {
			res, err = s.connector.ConnectWithProofsPinned(ctx, connectURL, pinnedIP, proofs, clientWGPubkey)
		} else {
			res, err = s.connector.ConnectWithProofs(ctx, connectURL, proofs, clientWGPubkey)
		}
		if err != nil {
			return nil, fmt.Errorf("%s node connect: %w", role, err)
		}
		return res, nil
	case client.TokenDeliveryNIP44:
		if tokenSender == nil {
			return nil, fmt.Errorf("%s node connect: NIP-44 sender is not configured", role)
		}
		if nodePubkey == "" {
			return nil, fmt.Errorf("%s node missing nostr pubkey for NIP-44 delivery", role)
		}
		res, err := tokenSender.ConnectWithProofs(ctx, nodePubkey, proofs, clientWGPubkey, role)
		if err != nil {
			return nil, fmt.Errorf("%s node connect via NIP-44: %w", role, err)
		}
		return res, nil
	default:
		return nil, fmt.Errorf("unsupported token delivery mode %q", s.delivery)
	}
}

func (s *Service) refundUnspentProofs(
	w *wallet.Wallet,
	entryProofs cashu.Proofs,
	exitProofs cashu.Proofs,
	entrySpent bool,
	exitSpent bool,
) error {
	var unspent cashu.Proofs
	if !entrySpent {
		unspent = append(unspent, entryProofs...)
	}
	if !exitSpent {
		unspent = append(unspent, exitProofs...)
	}
	if len(unspent) == 0 {
		return nil
	}
	return w.Release(unspent)
}

func shouldTreatProofsAsSpent(err error) bool {
	var rejected *client.NodeRejectedError
	if errors.As(err, &rejected) && rejected.ProofsBurned() {
		return true
	}
	var uncertain *client.ProofSpendUncertainError
	return errors.As(err, &uncertain)
}

func resolveConnectIPv4(ctx context.Context, connectURL string) (string, string, error) {
	u, err := url.Parse(connectURL)
	if err != nil {
		return "", "", fmt.Errorf("parse connect URL: %w", err)
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", fmt.Errorf("invalid connect URL %q", connectURL)
	}
	if u.Port() == "" && strings.HasSuffix(u.Host, ":") {
		return "", "", fmt.Errorf("connect URL %q has an empty port", connectURL)
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", u.Hostname())
	if err != nil {
		return "", "", fmt.Errorf("resolve connect host %q: %w", u.Hostname(), err)
	}
	if len(ips) == 0 {
		return "", "", fmt.Errorf("connect host %q has no IPv4 address", u.Hostname())
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return ips[0].String(), net.JoinHostPort(ips[0].String(), port), nil
}

func sanitizeRelays(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, relay := range in {
		trimmed := strings.TrimSpace(relay)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func sanitizeHubPubkeys(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, key := range in {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func sanitizePreferredTransports(in []types.Transport) []types.Transport {
	if len(in) == 0 {
		return []types.Transport{types.TransportWireGuard}
	}
	seen := make(map[types.Transport]struct{}, len(in))
	out := make([]types.Transport, 0, len(in))
	for _, t := range in {
		switch t {
		case types.TransportWireGuard, types.TransportHysteria2, types.TransportAmneziaWG:
		default:
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	if len(out) == 0 {
		return []types.Transport{types.TransportWireGuard}
	}
	return out
}

func sanitizeAllowedTransports(in []types.Transport) map[types.Transport]struct{} {
	all := []types.Transport{
		types.TransportWireGuard,
		types.TransportHysteria2,
		types.TransportAmneziaWG,
	}
	if len(in) == 0 {
		out := make(map[types.Transport]struct{}, len(all))
		for _, t := range all {
			out[t] = struct{}{}
		}
		return out
	}
	out := make(map[types.Transport]struct{}, len(in))
	for _, t := range in {
		switch t {
		case types.TransportWireGuard, types.TransportHysteria2, types.TransportAmneziaWG:
			out[t] = struct{}{}
		}
	}
	return out
}

func mapKeys(in map[types.Transport]struct{}) []types.Transport {
	out := make([]types.Transport, 0, len(in))
	for t := range in {
		out = append(out, t)
	}
	if len(out) == 0 {
		return []types.Transport{types.TransportWireGuard}
	}
	return out
}

func formatTransportList(in []types.Transport) string {
	if len(in) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(in))
	for _, t := range in {
		if t == "" {
			continue
		}
		parts = append(parts, string(t))
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, ",")
}

func formatTransportSet(in map[types.Transport]struct{}) string {
	if len(in) == 0 {
		return "(none)"
	}
	out := make([]types.Transport, 0, len(in))
	for t := range in {
		if t == "" {
			continue
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return "(none)"
	}
	slices.Sort(out)
	parts := make([]string, 0, len(out))
	for _, t := range out {
		parts = append(parts, string(t))
	}
	return strings.Join(parts, ",")
}
