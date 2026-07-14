package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/Radi-Labs/ARFL/internal/nostr"
	"github.com/Radi-Labs/ARFL/pkg/protocol"
	"github.com/Radi-Labs/ARFL/pkg/types"
)

// Announcer publishes node presence to Nostr relays on a fixed interval.
// It's a goroutine that runs for the lifetime of the node daemon.
//
// Why announce on a timer instead of on-demand? Because:
// 1. Relays and the hub use "last seen" to determine liveness
// 2. NIP-33 replaceable events update the load/capacity in real-time
// 3. If the node crashes, the announcement goes stale and gets pruned
type Announcer struct {
	nodeKP      *nostr.KeyPair
	nodeInfo    func() types.NodeInfo // Dynamic — load/capacity change
	attestation *nostr.Attestation
	pool        *nostr.RelayPool
	interval    time.Duration
	hubURL      string // Hub API URL for attestation refresh
}

// NewAnnouncer creates an announcer with the given identity and relay pool.
//
// nodeInfoFn is a function (not a static struct) because the node's load
// and capacity change over time. Every tick, we call it to get fresh data.
func NewAnnouncer(nodeKP *nostr.KeyPair, nodeInfoFn func() types.NodeInfo, att *nostr.Attestation, pool *nostr.RelayPool) *Announcer {
	return &Announcer{
		nodeKP:      nodeKP,
		nodeInfo:    nodeInfoFn,
		attestation: att,
		pool:        pool,
		interval:    time.Duration(protocol.PingIntervalSeconds) * time.Second,
	}
}

// SetHubURL enables automatic attestation refresh from the hub.
// When set, the announcer will request a fresh attestation before the
// current one expires.
func (a *Announcer) SetHubURL(hubURL string) {
	a.hubURL = hubURL
}

// Run starts the announcement loop. It blocks until ctx is cancelled.
// Call this in a goroutine: go announcer.Run(ctx)
func (a *Announcer) Run(ctx context.Context) {
	log.Println("[announcer] starting announcement loop")

	// Announce immediately on startup, then on ticker.
	a.refreshIfNeeded(ctx)
	if err := a.announce(ctx); err != nil {
		log.Printf("[announcer] initial announcement failed: %v", err)
	}

	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[announcer] shutting down")
			return
		case <-ticker.C:
			a.refreshIfNeeded(ctx)
			if err := a.announce(ctx); err != nil {
				log.Printf("[announcer] announcement failed: %v", err)
			}
		}
	}
}

// refreshIfNeeded checks if the attestation is within 1 hour of expiry
// and requests a fresh one from the hub.
func (a *Announcer) refreshIfNeeded(ctx context.Context) {
	if a.attestation == nil || a.hubURL == "" {
		return
	}

	remaining := time.Until(time.Unix(a.attestation.ExpiresAt, 0))
	if remaining > time.Hour {
		return
	}

	log.Printf("[announcer] attestation expires in %s, refreshing...", remaining.Round(time.Second))

	newAtt, err := a.refreshAttestation(ctx)
	if err != nil {
		log.Printf("[announcer] refresh failed: %v (will retry next tick)", err)
		return
	}

	a.attestation = newAtt
	log.Printf("[announcer] attestation refreshed, expires %s",
		time.Unix(newAtt.ExpiresAt, 0).Format(time.RFC3339))
}

// announce creates and publishes a single node announcement event.
func (a *Announcer) announce(ctx context.Context) error {
	info := a.nodeInfo()

	// Serialize NodeInfo as the event content.
	content, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("marshal node info: %w", err)
	}

	// Encode the attestation for the tag (optional for testnet).
	var attJSON string
	if a.attestation != nil {
		attJSON, err = a.attestation.Encode()
		if err != nil {
			return fmt.Errorf("encode attestation: %w", err)
		}
	}

	// Build the NIP-33 replaceable event.
	// The "d" tag is the node's Nostr pubkey — one announcement per node.
	tags := nostr.Tags{
		{"d", a.nodeKP.PubkeyHex()},
		{"wg_pubkey", info.WGPubkey},
		{"role", string(info.Role)},
	}
	if a.attestation != nil {
		tags = append(tags,
			nostr.Tag{"hub", a.attestation.HubPubkey},
			nostr.Tag{"attestation", attJSON},
			nostr.Tag{"operator", a.attestation.OperatorID},
		)
	}

	event := &nostr.Event{
		CreatedAt: time.Now().Unix(),
		Kind:      protocol.NostrKindNodeAnnouncement,
		Tags:      tags,
		Content:   string(content),
	}

	if err := event.Sign(a.nodeKP); err != nil {
		return fmt.Errorf("sign announcement: %w", err)
	}

	accepted, err := a.pool.Publish(ctx, event)
	if err != nil {
		return fmt.Errorf("publish announcement: %w", err)
	}

	log.Printf("[announcer] published to %d relay(s) | load=%d/%d | role=%s",
		accepted, info.Load, info.Capacity, info.Role)
	return nil
}

// BuildAnnouncementEvent creates a signed announcement event without publishing.
// Useful for testing and for the hub to verify event structure.
func BuildAnnouncementEvent(nodeKP *nostr.KeyPair, info types.NodeInfo, att *nostr.Attestation) (*nostr.Event, error) {
	content, err := json.Marshal(info)
	if err != nil {
		return nil, fmt.Errorf("marshal node info: %w", err)
	}

	tags := nostr.Tags{
		{"d", nodeKP.PubkeyHex()},
		{"wg_pubkey", info.WGPubkey},
		{"role", string(info.Role)},
	}
	if att != nil {
		attJSON, err := att.Encode()
		if err != nil {
			return nil, fmt.Errorf("encode attestation: %w", err)
		}
		tags = append(tags,
			nostr.Tag{"hub", att.HubPubkey},
			nostr.Tag{"attestation", attJSON},
			nostr.Tag{"operator", att.OperatorID},
		)
	}

	event := &nostr.Event{
		CreatedAt: time.Now().Unix(),
		Kind:      protocol.NostrKindNodeAnnouncement,
		Tags:      tags,
		Content:   string(content),
	}

	if err := event.Sign(nodeKP); err != nil {
		return nil, fmt.Errorf("sign event: %w", err)
	}
	return event, nil
}

// AttestRefreshRequest is sent by a node to the hub to request a fresh attestation.
type AttestRefreshRequest struct {
	Attestation string `json:"attestation"` // Current (possibly near-expiry) attestation JSON
	Timestamp   int64  `json:"timestamp"`   // Current unix timestamp
	Signature   string `json:"signature"`   // Node's Schnorr signature over SHA256(attestation + timestamp)
}

// AttestRefreshResponse is returned by the hub with a fresh attestation.
type AttestRefreshResponse struct {
	Attestation string `json:"attestation"` // Fresh attestation JSON
}

// refreshAttestation calls the hub's /attest/refresh endpoint.
// The node proves it owns the Nostr key by signing the request.
func (a *Announcer) refreshAttestation(ctx context.Context) (*nostr.Attestation, error) {
	attJSON, err := a.attestation.Encode()
	if err != nil {
		return nil, fmt.Errorf("encode current attestation: %w", err)
	}

	now := time.Now().Unix()

	// Sign the refresh request with the node's Nostr key.
	sig, err := nostr.SignRefreshRequest(a.nodeKP, attJSON, now)
	if err != nil {
		return nil, fmt.Errorf("sign refresh request: %w", err)
	}

	req := AttestRefreshRequest{
		Attestation: attJSON,
		Timestamp:   now,
		Signature:   sig,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", a.hubURL+"/attest/refresh", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("hub request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("hub returned %d: %s", resp.StatusCode, string(errBody))
	}

	var refreshResp AttestRefreshResponse
	if err := json.NewDecoder(resp.Body).Decode(&refreshResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	newAtt, err := nostr.DecodeAttestation(refreshResp.Attestation)
	if err != nil {
		return nil, fmt.Errorf("decode new attestation: %w", err)
	}

	return newAtt, nil
}
