// TokenSender delivers Cashu proofs to ARFL nodes via NIP-44 encrypted
// Nostr events. This is the privacy-maximizing delivery channel — the hub
// and relay operators cannot read the token contents.
//
// Flow:
//  1. Client selects entry/exit nodes (NodeSelector)
//  2. Client calls SendTokens() for each node
//  3. Each node receives an encrypted Nostr event with its Cashu proofs
//  4. Node decrypts and calls hub /v1/redeem to verify
//
// The client generates an ephemeral Nostr keypair per session so that
// even the nodes cannot correlate sessions by sender pubkey.
package client

import (
	"context"
	"fmt"

	"github.com/Radi-Labs/ARFL/internal/nostr"
	"github.com/elnosh/gonuts/cashu"
)

// TokenSender encrypts and publishes Cashu tokens to nodes via Nostr.
type TokenSender struct {
	pool *nostr.RelayPool
}

// NewTokenSender creates a sender connected to the given relay pool.
func NewTokenSender(pool *nostr.RelayPool) *TokenSender {
	return &TokenSender{pool: pool}
}

// SendTokens encrypts Cashu proofs for a specific node and publishes
// the encrypted event to Nostr relays.
//
// senderKP should be an ephemeral keypair (generated per session).
// nodePubkeyHex is the recipient node's Nostr public key.
// role is "entry" or "exit".
func (ts *TokenSender) SendTokens(
	ctx context.Context,
	senderKP *nostr.KeyPair,
	nodePubkeyHex string,
	proofs cashu.Proofs,
	clientWGPubkey string,
	role string,
) error {
	if len(proofs) == 0 {
		return fmt.Errorf("no proofs to send")
	}
	if clientWGPubkey == "" {
		return fmt.Errorf("wg_pubkey required")
	}
	if role != "entry" && role != "exit" {
		return fmt.Errorf("role must be 'entry' or 'exit', got %q", role)
	}

	payload := &nostr.TokenPayload{
		Proofs:   proofs,
		WGPubkey: clientWGPubkey,
		Role:     role,
		Version:  1,
	}

	event, err := nostr.SealTokenEnvelope(senderKP, nodePubkeyHex, payload)
	if err != nil {
		return fmt.Errorf("seal token envelope: %w", err)
	}

	published, err := ts.pool.Publish(ctx, event)
	if err != nil {
		return fmt.Errorf("publish to relays: %w", err)
	}
	if published == 0 {
		return fmt.Errorf("no relays accepted the event")
	}

	return nil
}

// SendToNodePair sends entry and exit proofs to their respective nodes
// using an ephemeral sender keypair for unlinkability.
func (ts *TokenSender) SendToNodePair(
	ctx context.Context,
	pair *NodePair,
	entryProofs cashu.Proofs,
	exitProofs cashu.Proofs,
	clientWGPubkey string,
) error {
	// Generate ephemeral keypair — nodes can't correlate sessions.
	ephemeralKP, err := nostr.GenerateKeyPair()
	if err != nil {
		return fmt.Errorf("generate ephemeral keypair: %w", err)
	}

	if err := ts.SendTokens(ctx, ephemeralKP, pair.Entry.NostrPubkey, entryProofs, clientWGPubkey, "entry"); err != nil {
		return fmt.Errorf("send to entry node: %w", err)
	}

	if err := ts.SendTokens(ctx, ephemeralKP, pair.Exit.NostrPubkey, exitProofs, clientWGPubkey, "exit"); err != nil {
		return fmt.Errorf("send to exit node: %w", err)
	}

	return nil
}
