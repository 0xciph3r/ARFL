package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

// mockRelay simulates a Nostr relay for testing.
// It accepts connections, receives events, and echoes them to subscribers.
type mockRelay struct {
	server *httptest.Server
	events []*Event
}

func newMockRelay() *mockRelay {
	mr := &mockRelay{}
	mr.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")

		ctx := r.Context()
		var subID string

		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}

			var msg []json.RawMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			if len(msg) < 2 {
				continue
			}

			var msgType string
			json.Unmarshal(msg[0], &msgType)

			switch msgType {
			case "EVENT":
				// Relay receives an event and sends OK.
				var event Event
				json.Unmarshal(msg[1], &event)
				mr.events = append(mr.events, &event)

				ok := fmt.Sprintf(`["OK","%s",true,""]`, event.ID)
				conn.Write(ctx, websocket.MessageText, []byte(ok))

				// If there's an active subscription, forward the event.
				if subID != "" {
					resp, _ := json.Marshal([]interface{}{"EVENT", subID, &event})
					conn.Write(ctx, websocket.MessageText, resp)
				}

			case "REQ":
				// Relay receives a subscription request.
				json.Unmarshal(msg[1], &subID)
				// Send EOSE (end of stored events).
				eose := fmt.Sprintf(`["EOSE","%s"]`, subID)
				conn.Write(ctx, websocket.MessageText, []byte(eose))
			}
		}
	}))
	return mr
}

func (mr *mockRelay) wsURL() string {
	return "ws" + strings.TrimPrefix(mr.server.URL, "http")
}

func (mr *mockRelay) close() {
	mr.server.Close()
}

func TestRelayPool_PublishAndReceive(t *testing.T) {
	// Set up a mock relay.
	mr := newMockRelay()
	defer mr.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Connect to the mock relay.
	pool := NewRelayPool([]string{mr.wsURL()})
	if err := pool.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer pool.Close()

	// Subscribe to events.
	eventCh, err := pool.Subscribe(ctx, "test-sub", Filter{Kinds: []int{30078}})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Give subscription time to register.
	time.Sleep(100 * time.Millisecond)

	// Create and sign an event.
	kp, _ := GenerateKeyPair()
	event := &Event{
		CreatedAt: time.Now().Unix(),
		Kind:      30078,
		Tags:      Tags{{"d", "test-node"}},
		Content:   `{"endpoint":"1.2.3.4:51820"}`,
	}
	event.Sign(kp)

	// Publish the event.
	accepted, err := pool.Publish(ctx, event)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if accepted != 1 {
		t.Errorf("expected 1 relay to accept, got %d", accepted)
	}

	// Wait for the event to arrive on the subscription.
	select {
	case received := <-eventCh:
		if received.ID != event.ID {
			t.Errorf("received event ID mismatch: got %s, want %s", received.ID, event.ID)
		}
		if err := received.Verify(); err != nil {
			t.Errorf("received event failed verification: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("timeout waiting for event on subscription")
	}
}

func TestRelayPool_ConnectFailsGracefully(t *testing.T) {
	// Pool should fail when NO relays are reachable.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pool := NewRelayPool([]string{"ws://localhost:19999"})
	err := pool.Connect(ctx)
	if err == nil {
		t.Error("should fail when no relays are reachable")
		pool.Close()
	}
}

func TestRelayPool_PartialConnectivity(t *testing.T) {
	// Pool should succeed if at least one relay connects.
	mr := newMockRelay()
	defer mr.close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	pool := NewRelayPool([]string{"ws://localhost:19999", mr.wsURL()})
	if err := pool.Connect(ctx); err != nil {
		t.Fatalf("should succeed with partial connectivity: %v", err)
	}
	defer pool.Close()
}

func TestFilter_MarshalJSON_WithTags(t *testing.T) {
	f := Filter{
		Kinds: []int{30078},
		Tags:  map[string][]string{"d": {"node-123"}},
	}

	data, err := f.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	// The JSON should contain "#d" key.
	var m map[string]interface{}
	json.Unmarshal(data, &m)

	if _, ok := m["#d"]; !ok {
		t.Error("filter JSON should contain '#d' tag filter")
	}
	if _, ok := m["kinds"]; !ok {
		t.Error("filter JSON should contain 'kinds'")
	}
}

// --- Adversarial / edge case tests ---

func TestRelayPool_MalformedRelayMessages(t *testing.T) {
	// STRIDE: Tampering — a rogue relay sends garbage to crash our client.
	// Our readLoop must handle this without panicking.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		ctx := r.Context()

		// Send a barrage of malformed messages.
		garbage := []string{
			`not json at all`,
			`{"object": "instead of array"}`,
			`[]`,
			`[1, 2, 3]`,
			`["EVENT"]`,
			`["EVENT", "sub-1"]`,
			`["EVENT", "sub-1", "not an event object"]`,
			`["EVENT", "sub-1", {"id": "bad", "sig": "also bad"}]`,
			`["UNKNOWN_TYPE", "data"]`,
			`["OK"]`,
			`["NOTICE"]`,
		}
		for _, g := range garbage {
			conn.Write(ctx, websocket.MessageText, []byte(g))
		}

		// Now send a valid REQ response to prove the connection still works.
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var msg []json.RawMessage
		json.Unmarshal(data, &msg)
		var msgType string
		json.Unmarshal(msg[0], &msgType)
		if msgType == "REQ" {
			var subID string
			json.Unmarshal(msg[1], &subID)
			eose := fmt.Sprintf(`["EOSE","%s"]`, subID)
			conn.Write(ctx, websocket.MessageText, []byte(eose))
		}

		// Keep connection alive briefly.
		time.Sleep(500 * time.Millisecond)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	pool := NewRelayPool([]string{wsURL})
	if err := pool.Connect(ctx); err != nil {
		t.Fatalf("Connect should succeed even with garbage messages: %v", err)
	}
	defer pool.Close()

	// Give time for garbage messages to be processed.
	time.Sleep(200 * time.Millisecond)

	// Connection should still be alive — subscribe should work.
	_, err := pool.Subscribe(ctx, "test-sub", Filter{Kinds: []int{30078}})
	if err != nil {
		t.Fatalf("Subscribe should work after garbage messages: %v", err)
	}
}

func TestRelayPool_RelayDropsConnection(t *testing.T) {
	// STRIDE: DoS — relay drops connection mid-session.
	// Our client should mark it as disconnected, not crash.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		// Immediately close the connection (simulating a crash/ban).
		conn.Close(websocket.StatusGoingAway, "go away")
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	pool := NewRelayPool([]string{wsURL})
	if err := pool.Connect(ctx); err != nil {
		t.Fatalf("initial connect should succeed: %v", err)
	}
	defer pool.Close()

	// Wait for the connection drop to be detected.
	time.Sleep(300 * time.Millisecond)

	// Publish should fail gracefully (no relays connected), not panic.
	kp, _ := GenerateKeyPair()
	event := &Event{
		CreatedAt: time.Now().Unix(),
		Kind:      1,
		Tags:      Tags{},
		Content:   "test",
	}
	event.Sign(kp)

	_, err := pool.Publish(ctx, event)
	if err == nil {
		t.Error("publish should fail when all relays disconnected")
	}
}

func TestRelayPool_EventVerificationAfterReceive(t *testing.T) {
	// STRIDE: Spoofing — relay injects a forged event with invalid signature.
	// Our code delivers raw events, but the CONSUMER must verify.
	// This test proves that Verify() catches forged events.
	kp, _ := GenerateKeyPair()
	forged := &Event{
		Pubkey:    kp.PubkeyHex(),
		CreatedAt: time.Now().Unix(),
		Kind:      30078,
		Tags:      Tags{{"d", "fake-node"}},
		Content:   `{"endpoint":"evil.com:51820"}`,
		ID:        "0000000000000000000000000000000000000000000000000000000000000000",
		Sig:       "0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
	}

	// A consumer that doesn't call Verify() would accept this.
	// Verify MUST catch it.
	if err := forged.Verify(); err == nil {
		t.Error("Verify should reject forged event with fake ID/sig")
	}
}

func TestRelayPool_PublishToDisconnectedRelay(t *testing.T) {
	// Edge case: what happens when we try to send to a relay that was never reachable?
	mr := newMockRelay()
	defer mr.close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// One good relay + one bad relay.
	pool := NewRelayPool([]string{"ws://localhost:19999", mr.wsURL()})
	if err := pool.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer pool.Close()

	kp, _ := GenerateKeyPair()
	event := &Event{
		CreatedAt: time.Now().Unix(),
		Kind:      30078,
		Tags:      Tags{},
		Content:   "test",
	}
	event.Sign(kp)

	// Should succeed with 1 relay (the good one).
	accepted, err := pool.Publish(ctx, event)
	if err != nil {
		t.Fatalf("Publish should succeed with partial connectivity: %v", err)
	}
	if accepted != 1 {
		t.Errorf("expected 1 accepting relay, got %d", accepted)
	}
}

func TestRelayPool_ImmediateCloseNoPanic(t *testing.T) {
	// Regression: close() racing with readLoop startup must not panic.
	// Reproduces the CI nil-pointer crash by connecting and immediately closing
	// many times under the race detector.
	for i := 0; i < 50; i++ {
		mr := newMockRelay()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		pool := NewRelayPool([]string{mr.wsURL()})

		if err := pool.Connect(ctx); err != nil {
			cancel()
			mr.close()
			t.Fatalf("iteration %d: Connect: %v", i, err)
		}

		// Immediately close — exercises the race window.
		pool.Close()
		cancel()
		mr.close()
	}
}

func TestRelayPool_ShortOKEventID(t *testing.T) {
	// STRIDE: Tampering — relay sends an OK with a short event ID.
	// Must not panic on eventID[:8].
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		ctx := r.Context()

		// Wait for an EVENT, then send OK with a short ID.
		_, _, err = conn.Read(ctx)
		if err != nil {
			return
		}
		conn.Write(ctx, websocket.MessageText, []byte(`["OK","bad",false,"rejected"]`))

		time.Sleep(200 * time.Millisecond)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	pool := NewRelayPool([]string{wsURL})
	if err := pool.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer pool.Close()

	kp, _ := GenerateKeyPair()
	event := &Event{
		CreatedAt: time.Now().Unix(),
		Kind:      1,
		Tags:      Tags{},
		Content:   "test",
	}
	event.Sign(kp)

	pool.Publish(ctx, event)

	// Give time for the short-ID OK to be processed without panicking.
	time.Sleep(300 * time.Millisecond)
}
