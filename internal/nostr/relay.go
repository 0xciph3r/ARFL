package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

// RelayPool manages connections to multiple Nostr relays.
// Why a pool? Redundancy. If one relay goes down, others still work.
// Nodes PUBLISH to all relays (broadcast). The hub SUBSCRIBES to all relays
// and deduplicates (same event from 3 relays = processed once).
type RelayPool struct {
	relays []*Relay
	mu     sync.RWMutex
}

// Relay is a single Nostr relay connection.
type Relay struct {
	URL       string
	conn      *websocket.Conn
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	connected bool

	// Event callbacks keyed by subscription ID.
	subs   map[string]chan *Event
	subsMu sync.RWMutex
}

// NewRelayPool creates a pool from a list of relay URLs.
func NewRelayPool(urls []string) *RelayPool {
	relays := make([]*Relay, len(urls))
	for i, url := range urls {
		relays[i] = &Relay{
			URL:  url,
			subs: make(map[string]chan *Event),
		}
	}
	return &RelayPool{relays: relays}
}

// Connect establishes WebSocket connections to all relays.
// Failures are logged but not fatal — we proceed with whatever connects.
// This is intentional: partial connectivity > total failure.
func (p *RelayPool) Connect(ctx context.Context) error {
	var connectedCount int

	for _, r := range p.relays {
		if err := r.connect(ctx); err != nil {
			log.Printf("[relay] failed to connect to %s: %v", r.URL, err)
			continue
		}
		connectedCount++
		log.Printf("[relay] connected to %s", r.URL)
	}

	if connectedCount == 0 {
		return fmt.Errorf("could not connect to any relay")
	}
	log.Printf("[relay] connected to %d/%d relays", connectedCount, len(p.relays))
	return nil
}

// Publish broadcasts an event to all connected relays.
// Returns the number of relays that accepted it.
func (p *RelayPool) Publish(ctx context.Context, event *Event) (int, error) {
	msg, err := json.Marshal([]interface{}{"EVENT", event})
	if err != nil {
		return 0, fmt.Errorf("marshal event message: %w", err)
	}

	var accepted int
	for _, r := range p.relays {
		if !r.isConnected() {
			continue
		}
		if err := r.send(ctx, msg); err != nil {
			log.Printf("[relay] publish to %s failed: %v", r.URL, err)
			continue
		}
		accepted++
	}

	if accepted == 0 {
		return 0, fmt.Errorf("event not accepted by any relay")
	}
	return accepted, nil
}

// Subscribe creates a subscription on all connected relays.
// Events matching the filter are sent to the returned channel.
// The caller must cancel the context to unsubscribe.
func (p *RelayPool) Subscribe(ctx context.Context, subID string, filters ...Filter) (<-chan *Event, error) {
	eventCh := make(chan *Event, 64)

	for _, r := range p.relays {
		if !r.isConnected() {
			continue
		}
		r.subsMu.Lock()
		r.subs[subID] = eventCh
		r.subsMu.Unlock()

		// Build the REQ message: ["REQ", sub_id, filter1, filter2, ...]
		msg := []interface{}{"REQ", subID}
		for _, f := range filters {
			msg = append(msg, f)
		}
		data, err := json.Marshal(msg)
		if err != nil {
			log.Printf("[relay] marshal subscribe: %v", err)
			continue
		}
		if err := r.send(ctx, data); err != nil {
			log.Printf("[relay] subscribe to %s failed: %v", r.URL, err)
		}
	}

	return eventCh, nil
}

// Close disconnects from all relays.
func (p *RelayPool) Close() {
	for _, r := range p.relays {
		r.close()
	}
}

// Filter is a NIP-01 subscription filter.
// Clients use this to say "give me only kind 30078 events" or
// "give me events from this specific pubkey."
type Filter struct {
	IDs     []string `json:"ids,omitempty"`
	Authors []string `json:"authors,omitempty"`
	Kinds   []int    `json:"kinds,omitempty"`
	Since   *int64   `json:"since,omitempty"`
	Until   *int64   `json:"until,omitempty"`
	Limit   int      `json:"limit,omitempty"`

	// Tag filters: #d, #p, etc. The key is the tag name (without #).
	Tags map[string][]string `json:"-"`
}

// MarshalJSON handles the special tag filter syntax.
// NIP-01 represents tag filters as "#d": ["value1", "value2"].
func (f Filter) MarshalJSON() ([]byte, error) {
	// Use an alias to avoid infinite recursion.
	type Alias Filter
	m := make(map[string]interface{})

	data, err := json.Marshal(Alias(f))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}

	// Add tag filters with # prefix.
	for k, v := range f.Tags {
		m["#"+k] = v
	}

	return json.Marshal(m)
}

// --- Relay (single connection) methods ---

func (r *Relay) connect(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(dialCtx, r.URL, nil)
	if err != nil {
		return fmt.Errorf("dial %s: %w", r.URL, err)
	}

	// STRIDE/DoS: Limit incoming message size to 64KB.
	// A valid Nostr event is ~2KB max. A relay sending 100MB would exhaust memory.
	conn.SetReadLimit(65536)

	r.conn = conn
	r.ctx, r.cancel = context.WithCancel(ctx)
	r.connected = true

	// Start the read loop in a goroutine.
	// Pass conn and ctx as parameters — capturing from local scope while lock
	// is held eliminates the race where close() nils r.conn before readLoop
	// can acquire the lock to capture it.
	go r.readLoop(conn, r.ctx)

	return nil
}

func (r *Relay) isConnected() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.connected
}

func (r *Relay) send(ctx context.Context, msg []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn == nil {
		return fmt.Errorf("not connected")
	}
	return r.conn.Write(ctx, websocket.MessageText, msg)
}

// readLoop processes incoming messages from the relay.
// Nostr relays send: ["EVENT", sub_id, event], ["EOSE", sub_id],
// ["OK", event_id, success, message], ["NOTICE", message].
//
// conn and ctx are passed as parameters (not captured from r.conn) to prevent
// a race condition: close() can nil r.conn between connect()'s Unlock and
// readLoop's Lock, causing a nil pointer panic. By accepting them as args
// from connect()'s local scope (while the lock is still held), we guarantee
// they are never nil.
func (r *Relay) readLoop(conn *websocket.Conn, ctx context.Context) {
	defer func() {
		r.mu.Lock()
		// Only update state if this goroutine still owns the active connection.
		// Without this check, a stale readLoop from a previous connection could
		// mark a freshly-reconnected relay as disconnected.
		if r.conn == conn {
			r.connected = false
			r.conn = nil
		}
		r.mu.Unlock()
	}()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return // Context cancelled, clean shutdown.
			}
			log.Printf("[relay] read from %s failed: %v", r.URL, err)
			return
		}

		r.handleMessage(data)
	}
}

func (r *Relay) handleMessage(data []byte) {
	// Parse the message as a JSON array.
	var msg []json.RawMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		log.Printf("[relay] invalid message from %s: %v", r.URL, err)
		return
	}
	if len(msg) < 2 {
		return
	}

	var msgType string
	if err := json.Unmarshal(msg[0], &msgType); err != nil {
		return
	}

	switch msgType {
	case "EVENT":
		if len(msg) < 3 {
			return
		}
		var subID string
		if err := json.Unmarshal(msg[1], &subID); err != nil {
			return
		}
		var event Event
		if err := json.Unmarshal(msg[2], &event); err != nil {
			log.Printf("[relay] invalid event from %s: %v", r.URL, err)
			return
		}

		r.subsMu.RLock()
		ch, ok := r.subs[subID]
		r.subsMu.RUnlock()
		if ok {
			select {
			case ch <- &event:
			default:
				log.Printf("[relay] event channel full for sub %s, dropping", subID)
			}
		}

	case "EOSE":
		// End of stored events — relay has sent all matching events.
		// For our use case, this means "initial index is loaded."
		log.Printf("[relay] EOSE for subscription from %s", r.URL)

	case "OK":
		// Publish acknowledgement. We could track these for retry logic.
		if len(msg) >= 4 {
			var eventID string
			var success bool
			json.Unmarshal(msg[1], &eventID)
			json.Unmarshal(msg[2], &success)
			if !success {
				var reason string
				json.Unmarshal(msg[3], &reason)
				log.Printf("[relay] event %s rejected by %s: %s", truncateID(eventID), r.URL, reason)
			}
		}

	case "NOTICE":
		if len(msg) >= 2 {
			var notice string
			json.Unmarshal(msg[1], &notice)
			log.Printf("[relay] NOTICE from %s: %s", r.URL, notice)
		}
	}
}

// truncateID safely shortens an event ID for logging.
// Prevents panics from malicious relays sending short IDs.
func truncateID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func (r *Relay) close() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cancel != nil {
		r.cancel()
	}
	if r.conn != nil {
		r.conn.Close(websocket.StatusNormalClosure, "shutdown")
		r.conn = nil
	}
	r.connected = false
}
