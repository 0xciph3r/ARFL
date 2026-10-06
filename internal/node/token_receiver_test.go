package node

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Radi-Labs/ARFL/internal/nostr"
	"github.com/elnosh/gonuts/cashu"
)

type relayStub struct {
	mu         sync.Mutex
	subCh      chan *nostr.Event
	subscribed chan struct{}
	published  []*nostr.Event
}

func newRelayStub() *relayStub {
	return &relayStub{
		subCh:      make(chan *nostr.Event, 16),
		subscribed: make(chan struct{}),
	}
}

func (r *relayStub) Publish(_ context.Context, event *nostr.Event) (int, error) {
	r.mu.Lock()
	r.published = append(r.published, event)
	r.mu.Unlock()
	return 1, nil
}

func (r *relayStub) Subscribe(_ context.Context, _ string, _ ...nostr.Filter) (<-chan *nostr.Event, error) {
	select {
	case <-r.subscribed:
	default:
		close(r.subscribed)
	}
	return r.subCh, nil
}

func (r *relayStub) emit(event *nostr.Event) {
	r.subCh <- event
}

func (r *relayStub) publishedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.published)
}

type countingRedeemer struct {
	mu    sync.Mutex
	calls int
}

func (r *countingRedeemer) Redeem(_ context.Context, _ cashu.Proofs) (*CashuRedeemResult, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return &CashuRedeemResult{BytesAllowed: 64_000_000, SatsRedeemed: 64}, nil
}

func (r *countingRedeemer) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func TestTokenReceiver_DeduplicatesDuplicateRequestEvents(t *testing.T) {
	nodeKP, err := nostr.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate node keypair: %v", err)
	}
	senderKP, err := nostr.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate sender keypair: %v", err)
	}

	relay := newRelayStub()
	redeemer := &countingRedeemer{}
	var callbackCalls int
	var callbackMu sync.Mutex

	receiver := NewTokenReceiver(nodeKP, redeemer, relay, func(_ string, bytesAllowed int64) (*CashuConnectResult, int, error) {
		callbackMu.Lock()
		callbackCalls++
		callbackMu.Unlock()
		return &CashuConnectResult{
			TunnelIP:     "10.100.0.8/32",
			NodeWGPubkey: "node-wg-pubkey==",
			BytesAllowed: bytesAllowed,
		}, 200, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- receiver.Listen(ctx)
	}()

	select {
	case <-relay.subscribed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("receiver did not subscribe in time")
	}

	request := &nostr.TokenPayload{
		Proofs: cashu.Proofs{
			{Amount: 32, Id: "ks", Secret: "s1", C: "02abc"},
			{Amount: 32, Id: "ks", Secret: "s2", C: "02def"},
		},
		WGPubkey:  "client-wg-pubkey==",
		Role:      "entry",
		RequestID: "dup-request-1",
		Version:   1,
	}
	event, err := nostr.SealTokenEnvelope(senderKP, nodeKP.PubkeyHex(), request)
	if err != nil {
		t.Fatalf("seal token envelope: %v", err)
	}

	relay.emit(event)
	relay.emit(event)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if relay.publishedCount() >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if relay.publishedCount() < 2 {
		t.Fatalf("expected duplicate request to trigger reply replay; published replies=%d", relay.publishedCount())
	}
	if redeemer.callCount() != 1 {
		t.Fatalf("redeem calls=%d, want 1 for duplicate request", redeemer.callCount())
	}
	callbackMu.Lock()
	gotCallbackCalls := callbackCalls
	callbackMu.Unlock()
	if gotCallbackCalls != 1 {
		t.Fatalf("connect callback calls=%d, want 1 for duplicate request", gotCallbackCalls)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("receiver did not exit after cancel")
	}
}
