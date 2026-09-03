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
	"sync"
	"time"

	"github.com/Radi-Labs/ARFL/internal/app"
	"github.com/Radi-Labs/ARFL/internal/tunnel"
	"github.com/Radi-Labs/ARFL/internal/wallet"
	"github.com/Radi-Labs/ARFL/pkg/types"
)

// Bridge exposes the ARFL client to the UI.
type Bridge struct {
	mu  sync.Mutex
	ctx context.Context
	svc *app.Service

	// tun is held separately from the service so its wgctrl handle can be
	// released on shutdown; app.Service only owns the session, not the handle.
	tun *tunnel.Tunnel
	// tunErr records why privileged networking was unavailable, so the UI can
	// explain the disabled Connect button instead of failing silently.
	tunErr string
}

// NewBridge returns a locked bridge. The wallet stays sealed until the user
// supplies a passphrase, so proofs are never decrypted just by launching the
// app.
func NewBridge() *Bridge {
	return &Bridge{}
}

// Startup captures the Wails context used for lifecycle-aware calls.
func (b *Bridge) Startup(ctx context.Context) {
	b.mu.Lock()
	b.ctx = ctx
	b.mu.Unlock()
}

// Shutdown tears down any active session so the machine is not left with a
// half-configured tunnel after the window closes.
func (b *Bridge) Shutdown(ctx context.Context) {
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

	// Privileged networking is optional. Without it the wallet still mints,
	// holds balance and browses nodes, so a user without root gets a working
	// app with Connect disabled rather than one that refuses to open.
	//
	// tunnel.New succeeds unprivileged, so Preflight is what actually decides:
	// without it the UI would offer Connect, the service would pay both nodes,
	// and bring-up would then fail on the first route command with the sats
	// already burned.
	tun, tunErr := tunnel.New()
	if tunErr == nil {
		tunErr = tun.Preflight()
		if tunErr != nil {
			tun.Close()
			tun = nil
		}
	}
	if tunErr != nil {
		b.tunErr = tunErr.Error()
	}

	svc, err := app.New(app.Config{
		Passphrase: passphrase,
		// A nil *tunnel.Tunnel in an interface is not nil, so pass the
		// interface explicitly as nil when setup failed.
		Tunnel:       tunnelOrNil(tun),
		PollInterval: 2 * time.Second,
	})
	if err != nil {
		if tun != nil {
			tun.Close()
		}
		return nil, err
	}

	b.svc = svc
	b.tun = tun
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

// tunnelOrNil avoids the typed-nil trap: assigning a nil *tunnel.Tunnel to an
// app.Tunnel interface yields a non-nil interface, and the service would then
// call methods on it instead of reporting that no tunnel is configured.
func tunnelOrNil(t *tunnel.Tunnel) app.Tunnel {
	if t == nil {
		return nil
	}
	return t
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
	return svc.ConnectHub(ctx, hubURL)
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

	return svc.Connect(ctx, perHopSats)
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
	return svc.Disconnect(ctx)
}

// status builds a snapshot. Callers must hold b.mu.
func (b *Bridge) status(svc *app.Service) *StatusView {
	view := &StatusView{
		Unlocked:    true,
		HubURL:      svc.HubURL(),
		State:       string(svc.State()),
		TunnelReady: b.tun != nil,
		TunnelError: b.tunErr,
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
