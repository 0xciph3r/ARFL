// Bridge is the boundary between the Wails frontend and internal/app.
//
// Every method here is callable from JavaScript, so each one converts errors
// and domain types into shapes that survive JSON and are safe to render. The
// bridge deliberately holds no protocol logic of its own.
package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Radi-Labs/ARFL/internal/app"
	"github.com/Radi-Labs/ARFL/pkg/types"
)

// Bridge exposes the ARFL client to the UI.
type Bridge struct {
	mu  sync.Mutex
	ctx context.Context
	svc *app.Service
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
	if svc == nil {
		return
	}
	if err := svc.Close(ctx); err != nil {
		fmt.Printf("arfl-desktop: shutdown: %v\n", err)
	}
}

// StatusView is the snapshot the UI renders on every state change.
type StatusView struct {
	Unlocked bool   `json:"unlocked"`
	HubURL   string `json:"hub_url"`
	State    string `json:"state"`
	Balance  uint64 `json:"balance_sats"`
	// Error carries a non-fatal problem (for example an unreadable balance)
	// without failing the whole call, so the UI can still render.
	Error string `json:"error,omitempty"`
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

	svc, err := app.New(app.Config{
		Passphrase:   passphrase,
		Tunnel:       nil, // Milestone 3 is wallet + discovery only.
		PollInterval: 2 * time.Second,
	})
	if err != nil {
		return nil, err
	}

	b.svc = svc
	return b.status(svc), nil
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
		Unlocked: true,
		HubURL:   svc.HubURL(),
		State:    string(svc.State()),
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
