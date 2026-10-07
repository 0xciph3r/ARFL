// Bridge is the boundary between the Wails frontend and internal/app.
//
// Every method here is callable from JavaScript, so each one converts errors
// and domain types into shapes that survive JSON and are safe to render. The
// bridge deliberately holds no protocol logic of its own.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Radi-Labs/ARFL/internal/app"
	"github.com/Radi-Labs/ARFL/internal/client"
	"github.com/Radi-Labs/ARFL/internal/config"
	"github.com/Radi-Labs/ARFL/internal/tunnel"
	"github.com/Radi-Labs/ARFL/internal/wallet"
	"github.com/Radi-Labs/ARFL/pkg/types"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// Bridge exposes the ARFL client to the UI.
type Bridge struct {
	mu  sync.Mutex
	ctx context.Context
	svc *app.Service

	// tun is held separately from the service so its wgctrl handle can be
	// released on shutdown; app.Service only owns the session, not the handle.
	tun tunnelHandle
	// tunErr records why privileged networking was unavailable, so the UI can
	// explain the disabled Connect button instead of failing silently.
	tunErr string
	// needsSetup is true when installing the helper would enable Connect.
	needsSetup bool

	// Window and tray handles, set once at startup.
	app        *application.App
	mainWin    *application.WebviewWindow
	trayWin    *application.WebviewWindow
	setTrayIcn func(connected bool)
}

// NewBridge returns a locked bridge. The wallet stays sealed until the user
// supplies a passphrase, so proofs are never decrypted just by launching the
// app.
func NewBridge() *Bridge {
	return &Bridge{}
}

// ServiceStartup captures the application context for lifecycle-aware calls.
func (b *Bridge) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	b.mu.Lock()
	b.ctx = ctx
	b.mu.Unlock()
	go trusted.keepFresh(ctx, b.registryUpdated)
	return nil
}

// Shutdown tears down any active session so the machine is not left with a
// half-configured tunnel after the window closes.
func (b *Bridge) ServiceShutdown() error {
	ctx := context.Background()
	svc := b.service()
	if svc != nil {
		if err := svc.Close(ctx); err != nil {
			fmt.Printf("arfl-desktop: shutdown: %v\n", err)
		}
	}

	// Release the wgctrl handle after teardown, never before: closing it first
	// would leave the routes and interfaces in place with no way to remove them.
	b.mu.Lock()
	tun := b.tun
	b.tun = nil
	b.mu.Unlock()

	if tun != nil {
		if err := tun.Close(); err != nil {
			fmt.Printf("arfl-desktop: close tunnel: %v\n", err)
		}
	}
	return nil
}

// StatusView is the snapshot the UI renders on every state change.
type StatusView struct {
	Unlocked bool   `json:"unlocked"`
	HubURL   string `json:"hub_url"`
	State    string `json:"state"`
	Balance  uint64 `json:"balance_sats"`
	// TunnelReady reports whether privileged networking is available. The UI
	// disables Connect when it is not, rather than letting the user pay for a
	// session that cannot be established.
	TunnelReady bool `json:"tunnel_ready"`
	// TunnelError explains an unavailable tunnel, typically missing root.
	TunnelError string `json:"tunnel_error,omitempty"`
	// HelperSetup is true when "Set up the tunnel" would enable Connect.
	HelperSetup bool `json:"helper_setup"`
	// Error carries a non-fatal problem (for example an unreadable balance)
	// without failing the whole call, so the UI can still render.
	Error string `json:"error,omitempty"`
}

// VaultStateView describes whether a local encrypted token vault already
// exists. The UI uses this to switch between first-run "create" and
// returning-user "unlock" flows.
type VaultStateView struct {
	Exists bool `json:"exists"`
}

// Unlock opens the encrypted proof vault. Calling it again is a no-op, since
// re-opening the store while a session is live would drop that session.
func (b *Bridge) Unlock(passphrase string) (*StatusView, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.svc != nil {
		return b.status(b.svc), nil
	}
	if passphrase == "" {
		return nil, fmt.Errorf("a passphrase is required to unlock the wallet")
	}
	return b.openLocked(passphrase)
}

// openLocked builds the service over the vault sealed with secret. Callers
// must hold b.mu and have checked that no service is open.
func (b *Bridge) openLocked(secret string) (*StatusView, error) {

	// Privileged networking is optional. Without it the wallet still mints,
	// holds balance and browses nodes, so the app opens with Connect disabled
	// and offers to set up the helper instead of refusing to start.
	tun, reason, needsSetup := chooseTunnel()
	b.tunErr, b.needsSetup = reason, needsSetup

	preferred, allowed, relays, trustedHubPubkeys, delivery, discoverySource, err := loadTransportPolicyFromClientConfig()
	if err != nil {
		if tun != nil {
			tun.Close()
		}
		return nil, err
	}

	svc, err := app.New(app.Config{
		Passphrase:          secret,
		PreferredTransports: preferred,
		AllowedTransports:   allowed,
		NostrRelays:         relays,
		TrustedHubPubkeys:   withRegistryKeys(trustedHubPubkeys),
		TokenDelivery:       delivery,
		DiscoverySource:     discoverySource,
		Tunnel:              tunnelOrNil(tun),
		PollInterval:        2 * time.Second,
	})
	if err != nil {
		if tun != nil {
			tun.Close()
		}
		return nil, err
	}

	b.svc = svc
	b.tun = tun
	migrateHubAliases(svc)
	return b.status(svc), nil
}

// VaultState reports whether the local encrypted vault file exists.
func (b *Bridge) VaultState() (*VaultStateView, error) {
	path, err := wallet.DefaultStorePath()
	if err != nil {
		return nil, fmt.Errorf("resolve vault path: %w", err)
	}
	_, err = os.Stat(path)
	if err == nil {
		return &VaultStateView{Exists: true}, nil
	}
	if os.IsNotExist(err) {
		return &VaultStateView{Exists: false}, nil
	}
	return nil, fmt.Errorf("read vault state: %w", err)
}

// ResetVault deletes the local encrypted token vault.
//
// This is only allowed while locked. Deleting while unlocked would orphan the
// in-memory service state from the file on disk.
func (b *Bridge) ResetVault() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	loaded := b.svc != nil
	if loaded {
		return fmt.Errorf("lock the wallet before resetting it")
	}

	path, err := wallet.DefaultStorePath()
	if err != nil {
		return fmt.Errorf("resolve vault path: %w", err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete vault: %w", err)
	}
	return nil
}

// tunnelOrNil avoids the typed-nil trap: a nil handle stored in app.Tunnel
// would be a non-nil interface, and the service would call methods on it
// instead of reporting that no tunnel is configured.
func tunnelOrNil(t tunnelHandle) app.Tunnel {
	if t == nil {
		return nil
	}
	return t
}

func loadTransportPolicyFromClientConfig() ([]types.Transport, []types.Transport, []string, []string, client.TokenDeliveryMode, string, error) {
	cfgPath := strings.TrimSpace(os.Getenv("ARFL_CLIENT_CONFIG"))
	if cfgPath == "" {
		cfgPath = "client.json"
	}

	if _, err := os.Stat(cfgPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil, nil, client.TokenDeliveryHTTP, app.DiscoverySourceHub, nil
		}
		return nil, nil, nil, nil, "", "", fmt.Errorf("read client config path %q: %w", cfgPath, err)
	}

	cfg, err := config.LoadClientConfig(cfgPath)
	if err != nil {
		return nil, nil, nil, nil, "", "", fmt.Errorf("load client config %q: %w", cfgPath, err)
	}
	if cfg.RequireCommonHops != nil && !*cfg.RequireCommonHops {
		return nil, nil, nil, nil, "", "", fmt.Errorf(
			"client config %q: require_common_hop_transport=false is not yet supported by arfl-desktop (mixed-hop adapters are not implemented); set require_common_hop_transport=true",
			cfgPath,
		)
	}

	preferred, err := parseTransportNames("preferred_transports", cfg.PreferredTransports)
	if err != nil {
		return nil, nil, nil, nil, "", "", fmt.Errorf("client config %q: %w", cfgPath, err)
	}
	allowed, err := parseTransportNames("allowed_transports", cfg.AllowedTransports)
	if err != nil {
		return nil, nil, nil, nil, "", "", fmt.Errorf("client config %q: %w", cfgPath, err)
	}

	mode := client.TokenDeliveryHTTP
	switch strings.ToLower(strings.TrimSpace(cfg.TokenDelivery)) {
	case "", string(client.TokenDeliveryHTTP):
		mode = client.TokenDeliveryHTTP
	case string(client.TokenDeliveryNIP44):
		mode = client.TokenDeliveryNIP44
	default:
		return nil, nil, nil, nil, "", "", fmt.Errorf("client config %q: token_delivery has unsupported value %q", cfgPath, cfg.TokenDelivery)
	}

	discoverySource := strings.ToLower(strings.TrimSpace(cfg.DiscoverySource))
	switch discoverySource {
	case "", app.DiscoverySourceHub:
		discoverySource = app.DiscoverySourceHub
	case app.DiscoverySourceNostr:
		// accepted
	default:
		return nil, nil, nil, nil, "", "", fmt.Errorf("client config %q: discovery_source has unsupported value %q", cfgPath, cfg.DiscoverySource)
	}

	relays := make([]string, 0, len(cfg.Relays))
	seen := make(map[string]struct{}, len(cfg.Relays))
	for _, relay := range cfg.Relays {
		relay = strings.TrimSpace(relay)
		if relay == "" {
			continue
		}
		if _, ok := seen[relay]; ok {
			continue
		}
		seen[relay] = struct{}{}
		relays = append(relays, relay)
	}

	hubPubkeys := make([]string, 0, len(cfg.HubPubkeys))
	seenPubkeys := make(map[string]struct{}, len(cfg.HubPubkeys))
	for _, pubkey := range cfg.HubPubkeys {
		pubkey = strings.TrimSpace(pubkey)
		if pubkey == "" {
			continue
		}
		if _, ok := seenPubkeys[pubkey]; ok {
			continue
		}
		seenPubkeys[pubkey] = struct{}{}
		hubPubkeys = append(hubPubkeys, pubkey)
	}

	return preferred, allowed, relays, hubPubkeys, mode, discoverySource, nil
}

func parseTransportNames(field string, in []string) ([]types.Transport, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]types.Transport, 0, len(in))
	for _, raw := range in {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case string(types.TransportWireGuard):
			out = append(out, types.TransportWireGuard)
		case string(types.TransportHysteria2):
			out = append(out, types.TransportHysteria2)
		case string(types.TransportAmneziaWG):
			out = append(out, types.TransportAmneziaWG)
		default:
			return nil, fmt.Errorf("%s has unsupported transport %q", field, raw)
		}
	}
	return out, nil
}

// Locked reports whether the vault still needs a passphrase.
func (b *Bridge) Locked() bool {
	return b.service() == nil
}

// Status returns the current snapshot for the UI.
func (b *Bridge) Status() *StatusView {
	svc := b.service()
	if svc == nil {
		return &StatusView{State: string(app.StateDisconnected)}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.status(svc)
}

// ConnectHub points the client at a hub URL the user typed in.
func (b *Bridge) ConnectHub(hubURL string) (*app.HubStatus, error) {
	svc, ctx, err := b.ready()
	if err != nil {
		return nil, err
	}
	status, err := svc.ConnectHub(ctx, currentHubURL(hubURL))
	if err == nil {
		saveLastHub(status.URL)
	}
	return status, err
}

// Balance returns unspent sats for the connected hub.
func (b *Bridge) Balance() (uint64, error) {
	svc, _, err := b.ready()
	if err != nil {
		return 0, err
	}
	return svc.Balance()
}

// Purchase requests a Lightning invoice for bandwidth credit.
func (b *Bridge) Purchase(amountSats uint64) (*app.Invoice, error) {
	svc, ctx, err := b.ready()
	if err != nil {
		return nil, err
	}
	return svc.Purchase(ctx, amountSats)
}

// AwaitPurchase blocks until the invoice settles, then mints the tokens.
//
// The call is bounded so a never-paid invoice cannot pin a goroutine for the
// lifetime of the app; the UI reports a timeout and the user can retry.
func (b *Bridge) AwaitPurchase(quoteID string) (uint64, error) {
	svc, ctx, err := b.ready()
	if err != nil {
		return 0, err
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	return svc.AwaitPurchase(ctx, quoteID)
}

// ListNodes returns the hub's online nodes.
func (b *Bridge) ListNodes() ([]types.NodeInfo, error) {
	svc, ctx, err := b.ready()
	if err != nil {
		return nil, err
	}
	nodes, err := svc.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	if nodes == nil {
		// A nil slice marshals to null; the UI expects an array it can iterate.
		nodes = []types.NodeInfo{}
	}
	return nodes, nil
}

// Connect establishes the two-hop tunnel, paying perHopSats to each node.
//
// The call is bounded: node handshakes and route changes can hang on a
// misbehaving node, and an unbounded call would leave the UI spinning with no
// way back to a disconnected state.
func (b *Bridge) Connect(perHopSats uint64) (*app.Session, error) {
	svc, ctx, err := b.ready()
	if err != nil {
		return nil, err
	}

	b.mu.Lock()
	ready, reason := b.tun != nil, b.tunErr
	b.mu.Unlock()

	if !ready {
		if reason == "" {
			reason = "privileged networking is unavailable"
		}
		return nil, fmt.Errorf("cannot connect: %s", reason)
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	session, err := svc.Connect(ctx, perHopSats)
	b.stateChanged(err == nil)
	return session, err
}

// PinPair makes later connects use the chosen entry and exit nodes.
func (b *Bridge) PinPair(entryID, exitID string) error {
	svc, _, err := b.ready()
	if err != nil {
		return err
	}
	return svc.SetPinnedPair(&app.PinnedPair{EntryID: entryID, ExitID: exitID})
}

// UnpinPair goes back to a random pair chosen at connect time.
func (b *Bridge) UnpinPair() error {
	svc, _, err := b.ready()
	if err != nil {
		return err
	}
	return svc.SetPinnedPair(nil)
}

// PinnedPair returns the user's chosen pair, or nil when pairing is random.
func (b *Bridge) PinnedPair() *app.PinnedPair {
	svc := b.service()
	if svc == nil {
		return nil
	}
	return svc.PinnedPair()
}

// Extend buys another allowance from the live session's nodes without
// taking the tunnel down.
func (b *Bridge) Extend(perHopSats uint64) (*app.Session, error) {
	svc, ctx, err := b.ready()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return svc.Extend(ctx, perHopSats)
}

// Session returns the active session, or nil when disconnected.
func (b *Bridge) Session() *app.Session {
	svc := b.service()
	if svc == nil {
		return nil
	}
	return svc.Session()
}

// Disconnect tears down the active session.
func (b *Bridge) Disconnect() error {
	svc, ctx, err := b.ready()
	if err != nil {
		return err
	}
	err = svc.Disconnect(ctx)
	b.stateChanged(false)
	return err
}

// status builds a snapshot. Callers must hold b.mu.
func (b *Bridge) status(svc *app.Service) *StatusView {
	view := &StatusView{
		Unlocked:    true,
		HubURL:      svc.HubURL(),
		State:       string(svc.State()),
		TunnelReady: b.tun != nil,
		TunnelError: b.tunErr,
		HelperSetup: b.needsSetup,
	}
	if view.HubURL == "" {
		return view
	}
	balance, err := svc.Balance()
	if err != nil {
		view.Error = err.Error()
		return view
	}
	view.Balance = balance
	return view
}

func (b *Bridge) service() *app.Service {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.svc
}

// ready returns the unlocked service and a usable context.
func (b *Bridge) ready() (*app.Service, context.Context, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.svc == nil {
		return nil, nil, fmt.Errorf("wallet is locked: unlock it before using the hub")
	}
	ctx := b.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return b.svc, ctx, nil
}

// UsageView is live traffic through the tunnel.
type UsageView struct {
	Connected bool  `json:"connected"`
	RxBytes   int64 `json:"rx_bytes"`
	TxBytes   int64 `json:"tx_bytes"`
	// Seconds since the last handshake with each node; -1 before the first.
	EntryIdleSecs int64 `json:"entry_idle_secs"`
	ExitIdleSecs  int64 `json:"exit_idle_secs"`
}

func idleSecs(t time.Time) int64 {
	if t.IsZero() {
		return -1
	}
	return int64(time.Since(t).Seconds())
}

// Usage reports bytes carried by the tunnel this session.
func (b *Bridge) Usage() (*UsageView, error) {
	b.mu.Lock()
	tun := b.tun
	b.mu.Unlock()
	if tun == nil {
		return &UsageView{}, nil
	}
	u, err := tun.usage()
	if err != nil {
		return nil, err
	}
	return &UsageView{
		Connected:     u.Active,
		RxBytes:       u.RxBytes,
		TxBytes:       u.TxBytes,
		EntryIdleSecs: idleSecs(u.EntryHandshake),
		ExitIdleSecs:  idleSecs(u.ExitHandshake),
	}, nil
}

// IPv6Exposed reports whether this machine has IPv6 the tunnel does not cover.
func (b *Bridge) IPv6Exposed() (bool, error) {
	return tunnel.IPv6Exposed()
}

// DisableIPv6 turns IPv6 off until the session ends.
func (b *Bridge) DisableIPv6() error {
	b.mu.Lock()
	tun := b.tun
	b.mu.Unlock()
	if tun == nil {
		return fmt.Errorf("the tunnel is unavailable on this device")
	}
	return tun.DisableIPv6()
}
