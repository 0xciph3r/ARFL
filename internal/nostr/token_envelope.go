// Token envelope wraps Cashu proofs in NIP-44 encrypted Nostr events
// for private delivery from VPN clients to nodes.
//
// The client encrypts the token payload with the node's Nostr pubkey.
// Neither the hub nor relay operators can read the contents.
//
// Event kind 21000 is used (ephemeral, custom ARFL kind). Relays
// are not expected to store these long-term.
package nostr

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/elnosh/gonuts/cashu"
)

// TokenEnvelopeKind is the Nostr event kind for ARFL token delivery.
// 20000-29999 range = ephemeral events (NIP-16), not persisted by relays.
const TokenEnvelopeKind = 21000

// TokenReplyEnvelopeKind is the Nostr event kind for encrypted connect replies.
// Nodes use it to return tunnel parameters or an explicit rejection reason.
const TokenReplyEnvelopeKind = 21001

// TokenPayload is the plaintext content encrypted inside the envelope.
type TokenPayload struct {
	Proofs    cashu.Proofs `json:"proofs"`
	WGPubkey  string       `json:"wg_pubkey"`  // Client's WireGuard public key
	Role      string       `json:"role"`       // "entry" or "exit"
	RequestID string       `json:"request_id"` // Client-generated correlation ID
	Version   int          `json:"version"`    // Protocol version (1)
}

// TokenReplyPayload is the encrypted reply a node sends to the request sender.
// It mirrors the /cashu-connect response shape for parity between transports.
type TokenReplyPayload struct {
	RequestID    string `json:"request_id"`
	OK           bool   `json:"ok"`
	Error        string `json:"error,omitempty"`
	StatusCode   int    `json:"status_code,omitempty"`
	TunnelIP     string `json:"tunnel_ip,omitempty"`
	NodeWGPubkey string `json:"node_wg_pubkey,omitempty"`
	BytesAllowed int64  `json:"bytes_allowed,omitempty"`
	Version      int    `json:"version"` // Protocol version (1)
}

// SealTokenEnvelope encrypts a TokenPayload for a specific node and
// wraps it in a signed Nostr event.
//
// The event is addressed to the node via a "p" tag (standard Nostr DM tag).
// Content is NIP-44 encrypted — only the node can decrypt it.
func SealTokenEnvelope(
	senderKP *KeyPair,
	recipientPubkeyHex string,
	payload *TokenPayload,
) (*Event, error) {
	return sealEnvelope(senderKP, recipientPubkeyHex, TokenEnvelopeKind, payload)
}

// SealTokenReplyEnvelope encrypts a TokenReplyPayload for the original sender
// and wraps it in a signed Nostr event.
func SealTokenReplyEnvelope(
	senderKP *KeyPair,
	recipientPubkeyHex string,
	payload *TokenReplyPayload,
) (*Event, error) {
	return sealEnvelope(senderKP, recipientPubkeyHex, TokenReplyEnvelopeKind, payload)
}

func sealEnvelope(
	senderKP *KeyPair,
	recipientPubkeyHex string,
	kind int,
	payload any,
) (*Event, error) {
	// Serialize payload.
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal token envelope payload: %w", err)
	}

	// Parse recipient pubkey for ECDH.
	recipientPub, err := PubkeyFromHex(recipientPubkeyHex)
	if err != nil {
		return nil, fmt.Errorf("parse recipient pubkey: %w", err)
	}

	// Compute conversation key and encrypt.
	convKey, err := GetConversationKey(senderKP.PrivateKey, recipientPub)
	if err != nil {
		return nil, fmt.Errorf("conversation key: %w", err)
	}

	encrypted, err := Encrypt(string(payloadJSON), convKey)
	if err != nil {
		return nil, fmt.Errorf("nip44 encrypt: %w", err)
	}

	// Build the Nostr event.
	event := &Event{
		Pubkey:    senderKP.PubkeyHex(),
		CreatedAt: time.Now().Unix(),
		Kind:      kind,
		Tags: Tags{
			Tag{"p", recipientPubkeyHex}, // Recipient node
		},
		Content: encrypted,
	}

	if err := event.Sign(senderKP); err != nil {
		return nil, fmt.Errorf("sign event: %w", err)
	}

	return event, nil
}

// OpenTokenEnvelope decrypts and parses a token envelope event.
// recipientKP is the node's keypair (has the private key for decryption).
func OpenTokenEnvelope(event *Event, recipientKP *KeyPair) (*TokenPayload, error) {
	var payload TokenPayload
	if err := openEnvelope(event, recipientKP, TokenEnvelopeKind, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

// OpenTokenReplyEnvelope decrypts and parses a token-reply envelope event.
func OpenTokenReplyEnvelope(event *Event, recipientKP *KeyPair) (*TokenReplyPayload, error) {
	var payload TokenReplyPayload
	if err := openEnvelope(event, recipientKP, TokenReplyEnvelopeKind, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func openEnvelope(event *Event, recipientKP *KeyPair, expectedKind int, target any) error {
	if event.Kind != expectedKind {
		return fmt.Errorf("unexpected event kind %d (want %d)", event.Kind, expectedKind)
	}

	// Parse sender pubkey for ECDH.
	senderPub, err := PubkeyFromHex(event.Pubkey)
	if err != nil {
		return fmt.Errorf("parse sender pubkey: %w", err)
	}

	// Compute conversation key and decrypt.
	convKey, err := GetConversationKey(recipientKP.PrivateKey, senderPub)
	if err != nil {
		return fmt.Errorf("conversation key: %w", err)
	}

	decrypted, err := Decrypt(event.Content, convKey)
	if err != nil {
		return fmt.Errorf("nip44 decrypt: %w", err)
	}

	if err := json.Unmarshal([]byte(decrypted), target); err != nil {
		return fmt.Errorf("unmarshal token payload: %w", err)
	}
	return nil
}

// PubkeyFromHex parses a 32-byte hex-encoded x-only public key (BIP-340).
func PubkeyFromHex(hexStr string) (*btcec.PublicKey, error) {
	if len(hexStr) != 64 {
		return nil, fmt.Errorf("pubkey hex must be 64 chars, got %d", len(hexStr))
	}
	pubBytes := make([]byte, 33)
	pubBytes[0] = 0x02 // Even y-coordinate prefix for x-only keys.

	n, err := hexDecode(hexStr, pubBytes[1:])
	if err != nil || n != 32 {
		return nil, fmt.Errorf("invalid pubkey hex: %v", err)
	}

	pubKey, err := btcec.ParsePubKey(pubBytes)
	if err != nil {
		return nil, fmt.Errorf("parse pubkey: %w", err)
	}
	return pubKey, nil
}

// hexDecode decodes hex string into dst, returning bytes written.
func hexDecode(src string, dst []byte) (int, error) {
	if len(src)%2 != 0 {
		return 0, fmt.Errorf("odd length hex string")
	}
	n := len(src) / 2
	if n > len(dst) {
		return 0, fmt.Errorf("dst too small")
	}
	for i := 0; i < n; i++ {
		hi := unhex(src[2*i])
		lo := unhex(src[2*i+1])
		if hi == 255 || lo == 255 {
			return 0, fmt.Errorf("invalid hex char at position %d", 2*i)
		}
		dst[i] = hi<<4 | lo
	}
	return n, nil
}

func unhex(c byte) byte {
	switch {
	case '0' <= c && c <= '9':
		return c - '0'
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10
	default:
		return 255
	}
}
