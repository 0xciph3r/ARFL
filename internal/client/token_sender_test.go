package client

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Radi-Labs/ARFL/internal/node"
	"github.com/Radi-Labs/ARFL/internal/nostr"
	"github.com/elnosh/gonuts/cashu"
)

type memoryRelay struct {
	mu   sync.Mutex
	subs map[string]memorySub
}

type memorySub struct {
	ch      chan *nostr.Event
	filters []nostr.Filter
}

func newMemoryRelay() *memoryRelay {
	return &memoryRelay{subs: make(map[string]memorySub)}
}

func (m *memoryRelay) Publish(_ context.Context, event *nostr.Event) (int, error) {
	m.mu.Lock()
	snapshot := make([]memorySub, 0, len(m.subs))
	for _, sub := range m.subs {
		snapshot = append(snapshot, sub)
	}
	m.mu.Unlock()

	delivered := 0
	for _, sub := range snapshot {
		if !matchesAnyFilter(event, sub.filters) {
			continue
		}
		select {
		case sub.ch <- event:
			delivered++
		default:
		}
	}
	return delivered, nil
}

func (m *memoryRelay) Subscribe(ctx context.Context, subID string, filters ...nostr.Filter) (<-chan *nostr.Event, error) {
	ch := make(chan *nostr.Event, 32)
	m.mu.Lock()
	m.subs[subID] = memorySub{ch: ch, filters: append([]nostr.Filter(nil), filters...)}
	m.mu.Unlock()

	go func() {
		<-ctx.Done()
		m.mu.Lock()
		if sub, ok := m.subs[subID]; ok {
			delete(m.subs, subID)
			close(sub.ch)
		}
		m.mu.Unlock()
	}()
	return ch, nil
}

func (m *memoryRelay) subscriptionCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.subs)
}

func waitForSubscription(t *testing.T, relay *memoryRelay) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if relay.subscriptionCount() > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("receiver did not subscribe in time (subs=%d)", relay.subscriptionCount())
}

func matchesAnyFilter(event *nostr.Event, filters []nostr.Filter) bool {
	if len(filters) == 0 {
		return true
	}
	for _, f := range filters {
		if matchesFilter(event, f) {
			return true
		}
	}
	return false
}

func matchesFilter(event *nostr.Event, filter nostr.Filter) bool {
	if len(filter.Kinds) > 0 {
		match := false
		for _, kind := range filter.Kinds {
			if event.Kind == kind {
				match = true
				break
			}
		}
		if !match {
			return false
		}
	}
	for key, wantValues := range filter.Tags {
		gotValues := event.GetTagValues(key)
		if len(gotValues) == 0 {
			return false
		}
		valueMatch := false
		for _, got := range gotValues {
			for _, want := range wantValues {
				if got == want {
					valueMatch = true
					break
				}
			}
			if valueMatch {
				break
			}
		}
		if !valueMatch {
			return false
		}
	}
	return true
}

type fakeRedeemer struct {
	mu       sync.Mutex
	bytes    int64
	err      error
	calls    int
	lastSize int
}

func (f *fakeRedeemer) Redeem(_ context.Context, proofs cashu.Proofs) (*node.CashuRedeemResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastSize = len(proofs)
	if f.err != nil {
		return nil, f.err
	}
	return &node.CashuRedeemResult{BytesAllowed: f.bytes, SatsRedeemed: uint64(proofs.Amount())}, nil
}

func TestTokenSenderConnectWithProofs_RoundTrip(t *testing.T) {
	relay := newMemoryRelay()
	nodeKP, err := nostr.GenerateKeyPair()
	if err != nil {
		t.Fatalf("node keypair: %v", err)
	}

	redeemer := &fakeRedeemer{bytes: 64_000_000}
	callbackWGValid := make(chan bool, 1)
	receiver := node.NewTokenReceiver(nodeKP, redeemer, relay, func(wgPubkey string, bytesAllowed int64) (*node.CashuConnectResult, int, error) {
		callbackWGValid <- wgPubkey != ""
		return &node.CashuConnectResult{
			TunnelIP:     "10.100.0.5/32",
			NodeWGPubkey: "node-wg-pubkey==",
			BytesAllowed: bytesAllowed,
		}, 200, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		_ = receiver.Listen(ctx)
	}()
	waitForSubscription(t, relay)

	sender := NewTokenSender(relay)
	res, err := sender.ConnectWithProofs(ctx, nodeKP.PubkeyHex(), cashu.Proofs{
		{Amount: 32, Id: "k", Secret: "s1", C: "02abc"},
		{Amount: 32, Id: "k", Secret: "s2", C: "02def"},
	}, "client-wg-pubkey==", "entry")
	if err != nil {
		t.Fatalf("ConnectWithProofs: %v", err)
	}
	if res.TunnelIP != "10.100.0.5/32" {
		t.Fatalf("tunnel_ip=%q want %q", res.TunnelIP, "10.100.0.5/32")
	}
	if res.NodeWGPubkey != "node-wg-pubkey==" {
		t.Fatalf("node_wg_pubkey=%q want %q", res.NodeWGPubkey, "node-wg-pubkey==")
	}
	if res.BytesAllowed != 64_000_000 {
		t.Fatalf("bytes_allowed=%d want %d", res.BytesAllowed, int64(64_000_000))
	}
	if ok := <-callbackWGValid; !ok {
		t.Fatal("receiver callback got empty wg pubkey")
	}
}

func TestTokenSenderConnectWithProofs_MapsNodeRejection(t *testing.T) {
	relay := newMemoryRelay()
	nodeKP, err := nostr.GenerateKeyPair()
	if err != nil {
		t.Fatalf("node keypair: %v", err)
	}

	callbackCalled := make(chan struct{}, 1)
	receiver := node.NewTokenReceiver(nodeKP, &fakeRedeemer{err: node.ErrRedeemAlreadySpent}, relay, func(wgPubkey string, bytesAllowed int64) (*node.CashuConnectResult, int, error) {
		callbackCalled <- struct{}{}
		return nil, 0, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		_ = receiver.Listen(ctx)
	}()
	waitForSubscription(t, relay)

	sender := NewTokenSender(relay)
	_, err = sender.ConnectWithProofs(ctx, nodeKP.PubkeyHex(), cashu.Proofs{
		{Amount: 1, Id: "k", Secret: "spent", C: "02dead"},
	}, "client-wg-pubkey==", "exit")
	if err == nil {
		t.Fatal("expected rejection error")
	}
	rejected, ok := err.(*NodeRejectedError)
	if !ok {
		t.Fatalf("error type=%T, want *NodeRejectedError (%v)", err, err)
	}
	if rejected.StatusCode != 409 {
		t.Fatalf("status=%d want 409", rejected.StatusCode)
	}
	if !rejected.ProofsBurned() {
		t.Fatal("expected 409 to be treated as burned proofs")
	}
	select {
	case <-callbackCalled:
		t.Fatal("connect callback should not run when redeem fails")
	default:
	}
}
