// TokenReceiver listens for NIP-44 encrypted Cashu token deliveries
// on Nostr relays and processes them into WireGuard connections.
//
// When a VPN client sends an encrypted token event, the receiver:
//  1. Decrypts the NIP-44 payload using the node's private key
//  2. Extracts the Cashu proofs and WireGuard pubkey
//  3. Calls the hub /v1/redeem to verify and burn the proofs
//  4. Triggers WireGuard peer setup via a callback
//
// This replaces the direct HTTP /cashu-connect path with a fully
// private Nostr-based delivery channel.
package node

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/Radi-Labs/ARFL/internal/nostr"
	"github.com/elnosh/gonuts/cashu"
)

// ConnectCallback is called when valid tokens are received and verified.
// The implementation should add a WireGuard peer and return the tunnel config.
type ConnectCallback func(wgPubkey string, bytesAllowed int64) (*CashuConnectResult, int, error)

type proofRedeemer interface {
	Redeem(ctx context.Context, proofs cashu.Proofs) (*CashuRedeemResult, error)
}

// TokenReceiver listens on Nostr relays for encrypted token events.
type TokenReceiver struct {
	nodeKP    *nostr.KeyPair
	redeemer  proofRedeemer
	pool      relayClient
	onConnect ConnectCallback
}

type relayClient interface {
	Publish(ctx context.Context, event *nostr.Event) (int, error)
	Subscribe(ctx context.Context, subID string, filters ...nostr.Filter) (<-chan *nostr.Event, error)
}

// NewTokenReceiver creates a receiver for the given node identity.
func NewTokenReceiver(
	nodeKP *nostr.KeyPair,
	redeemer proofRedeemer,
	pool relayClient,
	onConnect ConnectCallback,
) *TokenReceiver {
	return &TokenReceiver{
		nodeKP:    nodeKP,
		redeemer:  redeemer,
		pool:      pool,
		onConnect: onConnect,
	}
}

// Listen subscribes to Nostr relays for token envelope events addressed
// to this node and processes them. Blocks until ctx is cancelled.
func (tr *TokenReceiver) Listen(ctx context.Context) error {
	// Subscribe to kind 21000 events tagged with our pubkey.
	filter := nostr.Filter{
		Kinds: []int{nostr.TokenEnvelopeKind},
		Tags: map[string][]string{
			"p": {tr.nodeKP.PubkeyHex()},
		},
	}

	eventCh, err := tr.pool.Subscribe(ctx, "arfl-tokens", filter)
	if err != nil {
		return fmt.Errorf("subscribe to relays: %w", err)
	}

	log.Printf("[token-receiver] listening for encrypted token events (pubkey=%s)",
		tr.nodeKP.PubkeyHex()[:16]+"...")

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-eventCh:
			if !ok {
				return fmt.Errorf("event channel closed")
			}
			tr.handleEvent(ctx, event)
		}
	}
}

// handleEvent processes a single token envelope event.
func (tr *TokenReceiver) handleEvent(ctx context.Context, event *nostr.Event) {
	if err := event.Verify(); err != nil {
		log.Printf("[token-receiver] dropped event with invalid signature (sender=%s): %v",
			truncate(event.Pubkey), err)
		return
	}

	// Decrypt the NIP-44 envelope.
	payload, err := nostr.OpenTokenEnvelope(event, tr.nodeKP)
	if err != nil {
		log.Printf("[token-receiver] decrypt failed (sender=%s): %v",
			truncate(event.Pubkey), err)
		return
	}

	if payload.Version != 1 {
		log.Printf("[token-receiver] unknown payload version %d", payload.Version)
		return
	}
	if payload.RequestID == "" {
		log.Printf("[token-receiver] payload missing request_id (sender=%s)", truncate(event.Pubkey))
		return
	}
	if len(payload.Proofs) == 0 || payload.WGPubkey == "" || (payload.Role != "entry" && payload.Role != "exit") {
		log.Printf("[token-receiver] incomplete payload (proofs=%d, wg=%q, role=%q)",
			len(payload.Proofs), payload.WGPubkey, payload.Role)
		tr.sendReply(ctx, event.Pubkey, &nostr.TokenReplyPayload{
			RequestID:  payload.RequestID,
			OK:         false,
			Error:      "invalid token payload",
			StatusCode: http.StatusBadRequest,
			Version:    1,
		})
		return
	}

	// Verify and burn proofs with hub.
	result, err := tr.redeemer.Redeem(ctx, payload.Proofs)
	if err != nil {
		status, msg, known := RedeemErrorResponse(err)
		if !known {
			status = http.StatusBadGateway
			msg = "hub verification failed"
		}
		log.Printf("[token-receiver] redeem failed: %v", err)
		tr.sendReply(ctx, event.Pubkey, &nostr.TokenReplyPayload{
			RequestID:  payload.RequestID,
			OK:         false,
			Error:      msg,
			StatusCode: status,
			Version:    1,
		})
		return
	}

	// Trigger WireGuard peer setup.
	connectResult, status, err := tr.onConnect(payload.WGPubkey, result.BytesAllowed)
	if err != nil {
		if status == 0 {
			status = http.StatusInternalServerError
		}
		log.Printf("[token-receiver] connect callback failed (status=%d): %v", status, err)
		tr.sendReply(ctx, event.Pubkey, &nostr.TokenReplyPayload{
			RequestID:  payload.RequestID,
			OK:         false,
			Error:      err.Error(),
			StatusCode: status,
			Version:    1,
		})
		return
	}

	tr.sendReply(ctx, event.Pubkey, &nostr.TokenReplyPayload{
		RequestID:    payload.RequestID,
		OK:           true,
		TunnelIP:     connectResult.TunnelIP,
		NodeWGPubkey: connectResult.NodeWGPubkey,
		BytesAllowed: connectResult.BytesAllowed,
		Version:      1,
	})
	log.Printf("[token-receiver] peer connected via Nostr (wg=%s, bytes=%d, role=%s, request=%s)",
		truncate(payload.WGPubkey), result.BytesAllowed, payload.Role, payload.RequestID)
}

func (tr *TokenReceiver) sendReply(ctx context.Context, recipientPubkey string, reply *nostr.TokenReplyPayload) {
	event, err := nostr.SealTokenReplyEnvelope(tr.nodeKP, recipientPubkey, reply)
	if err != nil {
		log.Printf("[token-receiver] seal reply failed: %v", err)
		return
	}
	accepted, err := tr.pool.Publish(ctx, event)
	if err != nil {
		log.Printf("[token-receiver] publish reply failed: %v", err)
		return
	}
	if accepted == 0 {
		log.Printf("[token-receiver] reply not accepted by any relay")
	}
}

func truncate(s string) string {
	if len(s) > 16 {
		return s[:16] + "..."
	}
	return s
}
