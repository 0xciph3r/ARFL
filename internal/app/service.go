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
	"sync"
	"time"

	"github.com/Radi-Labs/ARFL/internal/client"
	"github.com/Radi-Labs/ARFL/internal/wallet"
	"github.com/Radi-Labs/ARFL/pkg/types"
	"github.com/elnosh/gonuts/cashu"
)

// Errors returned by the service.
var (
	ErrNoHub          = errors.New("no hub connected")
	ErrAlreadyOn      = errors.New("already connected")
	ErrNotConnected   = errors.New("not connected")
	ErrAmountTooSmall = errors.New("amount must be greater than zero")
)

// State is the connection state machine exposed to the UI.
type State string

const (
	StateDisconnected  State = "disconnected"
	StateConnecting    State = "connecting"
	StateConnected     State = "connected"
	StateDisconnecting State = "disconnecting"
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
	// Up establishes the nested tunnel from node-issued configuration.
	Up(ctx context.Context, cfg TunnelConfig) error
	// Down tears the tunnel down and restores the previous routing state.
	Down(ctx context.Context) error
}

// TunnelConfig is everything a Tunnel needs to establish both hops.
type TunnelConfig struct {
	Entry     HopConfig `json:"entry"`
	Exit      HopConfig `json:"exit"`
	ClientKey string    `json:"client_key"`
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

	// Hub-scoped state, replaced wholesale by ConnectHub.
	wallet   *wallet.Wallet
	selector *client.NodeSelector
	hubInfo  *wallet.HubInfo

	state   State
	session *Session
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

	return &Service{
		store:        store,
		tunnel:       cfg.Tunnel,
		connector:    client.NewCashuConnector(),
		pollInterval: interval,
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

	selector := client.NewNodeSelector(mintClient.HubURL())

	s.mu.Lock()
	if s.state != StateDisconnected {
		s.mu.Unlock()
		return nil, fmt.Errorf("disconnect the active session before switching hubs")
	}
	s.wallet = w
	s.selector = selector
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

	// Node count is informational — a hub that cannot serve the list is still
	// usable for minting, so a failure here must not block connecting.
	if nodes, err := selector.FetchNodes(ctx); err == nil {
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
	sel, err := s.currentSelector()
	if err != nil {
		return nil, err
	}
	return sel.FetchNodes(ctx)
}

// SelectPair picks an entry/exit pair client-side. The hub never learns the
// choice, which is what keeps payment unlinkable from routing.
func (s *Service) SelectPair(ctx context.Context) (*client.NodePair, error) {
	sel, err := s.currentSelector()
	if err != nil {
		return nil, err
	}
	return sel.SelectPair(ctx)
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
		return nil, err
	}

	clientKey, err := s.clientPublicKey()
	if err != nil {
		return nil, err
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

	entryRes, exitRes, err := s.connector.ConnectPair(ctx, pair, entryProofs, exitProofs, clientKey)
	if err != nil {
		// Only refund what was never handed over. If a node accepted its
		// proofs they are already burned at the hub, and returning them to the
		// store would show a balance the user cannot actually spend.
		var unspent cashu.Proofs
		if entryRes == nil {
			unspent = append(unspent, entryProofs...)
		}
		if exitRes == nil {
			unspent = append(unspent, exitProofs...)
		}
		if rerr := w.Release(unspent); rerr != nil {
			return nil, fmt.Errorf("%w (unspent proofs could not be returned to the store: %v)", err, rerr)
		}
		return nil, err
	}

	cfg := TunnelConfig{
		ClientKey: clientKey,
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
			NodeWGPubkey: exitRes.NodeWGPubkey,
			TunnelIP:     exitRes.TunnelIP,
			BytesAllowed: exitRes.BytesAllowed,
		},
	}

	if err := s.tunnel.Up(ctx, cfg); err != nil {
		return nil, fmt.Errorf("bring tunnel up: %w", err)
	}

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
	s.mu.Unlock()

	if err != nil {
		return fmt.Errorf("tear tunnel down: %w", err)
	}
	return nil
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

func (s *Service) clientPublicKey() (string, error) {
	if s.tunnel == nil {
		return "", fmt.Errorf("no tunnel configured: cannot supply a WireGuard public key")
	}
	key, err := s.tunnel.PublicKey()
	if err != nil {
		return "", fmt.Errorf("read WireGuard public key: %w", err)
	}
	if key == "" {
		return "", fmt.Errorf("tunnel returned an empty WireGuard public key")
	}
	return key, nil
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
