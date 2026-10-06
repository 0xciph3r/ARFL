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
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/Radi-Labs/ARFL/internal/nostr"
	"github.com/elnosh/gonuts/cashu"
)

// TokenSender encrypts and publishes Cashu tokens to nodes via Nostr.
type TokenSender struct {
	pool relayClient
}

// ProofSpendUncertainError reports a post-publish failure where proofs may
// already have been redeemed by the node.
//
// Callers must treat these proofs as unsafe to re-use.
type ProofSpendUncertainError struct {
	Reason string
	Cause  error
}

func (e *ProofSpendUncertainError) Error() string {
	if e == nil {
		return "proof spend uncertainty"
	}
	if e.Cause != nil && e.Reason != "" {
		return fmt.Sprintf("%s: %v", e.Reason, e.Cause)
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	if e.Reason != "" {
		return e.Reason
	}
	return "proof spend uncertainty"
}

func (e *ProofSpendUncertainError) Unwrap() error { return e.Cause }

type relayClient interface {
	Publish(ctx context.Context, event *nostr.Event) (int, error)
	Subscribe(ctx context.Context, subID string, filters ...nostr.Filter) (<-chan *nostr.Event, error)
}

// NewTokenSender creates a sender connected to the given relay pool.
func NewTokenSender(pool relayClient) *TokenSender {
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
	requestID, err := randomRequestID()
	if err != nil {
		return fmt.Errorf("generate request ID: %w", err)
	}

	payload := &nostr.TokenPayload{
		Proofs:    proofs,
		WGPubkey:  clientWGPubkey,
		Role:      role,
		RequestID: requestID,
		Version:   1,
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
	entryClientWGPubkey string,
	exitClientWGPubkey string,
) error {
	if entryClientWGPubkey == "" || exitClientWGPubkey == "" {
		return fmt.Errorf("both entry and exit wg_pubkeys are required")
	}
	// Use a distinct sender keypair per hop so colluding nodes cannot link hops
	// by sender identity.
	entryKP, err := nostr.GenerateKeyPair()
	if err != nil {
		return fmt.Errorf("generate entry sender keypair: %w", err)
	}
	if err := ts.SendTokens(ctx, entryKP, pair.Entry.NostrPubkey, entryProofs, entryClientWGPubkey, "entry"); err != nil {
		return fmt.Errorf("send to entry node: %w", err)
	}

	exitKP, err := nostr.GenerateKeyPair()
	if err != nil {
		return fmt.Errorf("generate exit sender keypair: %w", err)
	}
	if err := ts.SendTokens(ctx, exitKP, pair.Exit.NostrPubkey, exitProofs, exitClientWGPubkey, "exit"); err != nil {
		return fmt.Errorf("send to exit node: %w", err)
	}

	return nil
}

// ConnectWithProofs sends encrypted proofs to one node and waits for the
// encrypted connect reply event.
func (ts *TokenSender) ConnectWithProofs(
	ctx context.Context,
	nodePubkeyHex string,
	proofs cashu.Proofs,
	clientWGPubkey string,
	role string,
) (*ConnectResult, error) {
	if nodePubkeyHex == "" {
		return nil, fmt.Errorf("node pubkey required")
	}
	if len(proofs) == 0 {
		return nil, fmt.Errorf("no proofs to send")
	}
	if clientWGPubkey == "" {
		return nil, fmt.Errorf("wg_pubkey required")
	}
	if role != "entry" && role != "exit" {
		return nil, fmt.Errorf("role must be 'entry' or 'exit', got %q", role)
	}

	senderKP, err := nostr.GenerateKeyPair()
	if err != nil {
		return nil, fmt.Errorf("generate sender keypair: %w", err)
	}
	requestID, err := randomRequestID()
	if err != nil {
		return nil, fmt.Errorf("generate request ID: %w", err)
	}

	replyCh, err := ts.pool.Subscribe(ctx, "arfl-token-reply-"+requestID, nostr.Filter{
		Kinds: []int{nostr.TokenReplyEnvelopeKind},
		Tags: map[string][]string{
			"p": {senderKP.PubkeyHex()},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("subscribe for token reply: %w", err)
	}

	event, err := nostr.SealTokenEnvelope(senderKP, nodePubkeyHex, &nostr.TokenPayload{
		Proofs:    proofs,
		WGPubkey:  clientWGPubkey,
		Role:      role,
		RequestID: requestID,
		Version:   1,
	})
	if err != nil {
		return nil, fmt.Errorf("seal token envelope: %w", err)
	}
	published := false
	accepted, err := ts.pool.Publish(ctx, event)
	if err != nil {
		return nil, fmt.Errorf("publish token envelope: %w", err)
	}
	if accepted == 0 {
		return nil, fmt.Errorf("no relays accepted token envelope")
	}
	published = true

	for {
		select {
		case <-ctx.Done():
			if published {
				return nil, &ProofSpendUncertainError{
					Reason: "token envelope published; connect outcome unknown",
					Cause:  ctx.Err(),
				}
			}
			return nil, fmt.Errorf("wait for token reply: %w", ctx.Err())
		case event, ok := <-replyCh:
			if !ok {
				if published {
					return nil, &ProofSpendUncertainError{
						Reason: "token envelope published; reply subscription closed before outcome",
					}
				}
				return nil, fmt.Errorf("reply subscription closed")
			}
			if event == nil {
				continue
			}
			if event.Kind != nostr.TokenReplyEnvelopeKind || event.Pubkey != nodePubkeyHex {
				continue
			}
			if err := event.Verify(); err != nil {
				continue
			}

			reply, err := nostr.OpenTokenReplyEnvelope(event, senderKP)
			if err != nil {
				continue
			}
			if reply.RequestID != requestID {
				continue
			}
			if reply.Version != 1 {
				return nil, &ProofSpendUncertainError{
					Reason: fmt.Sprintf("token envelope published; unsupported token reply version %d", reply.Version),
				}
			}
			if !reply.OK {
				if reply.StatusCode > 0 {
					return nil, &NodeRejectedError{
						StatusCode: reply.StatusCode,
						Message:    reply.Error,
					}
				}
				return nil, &ProofSpendUncertainError{
					Reason: fmt.Sprintf("token envelope published; node rejected proofs without status: %s", reply.Error),
				}
			}
			if reply.TunnelIP == "" || reply.NodeWGPubkey == "" {
				return nil, &ProofSpendUncertainError{
					Reason: "token envelope published; node reply missing tunnel_ip or node_wg_pubkey",
				}
			}
			return &ConnectResult{
				TunnelIP:     reply.TunnelIP,
				NodeWGPubkey: reply.NodeWGPubkey,
				BytesAllowed: reply.BytesAllowed,
			}, nil
		}
	}
}

// ConnectPair connects to both entry and exit nodes via NIP-44 delivery.
func (ts *TokenSender) ConnectPair(
	ctx context.Context,
	pair *NodePair,
	entryProofs cashu.Proofs,
	exitProofs cashu.Proofs,
	entryClientWGPubkey string,
	exitClientWGPubkey string,
) (entry *ConnectResult, exit *ConnectResult, err error) {
	entry, err = ts.ConnectWithProofs(ctx, pair.Entry.NostrPubkey, entryProofs, entryClientWGPubkey, "entry")
	if err != nil {
		return nil, nil, fmt.Errorf("entry node connect via Nostr: %w", err)
	}

	exit, err = ts.ConnectWithProofs(ctx, pair.Exit.NostrPubkey, exitProofs, exitClientWGPubkey, "exit")
	if err != nil {
		return entry, nil, fmt.Errorf("exit node connect via Nostr (entry succeeded): %w", err)
	}
	return entry, exit, nil
}

func randomRequestID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
