package nostr

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

// Event is a NIP-01 Nostr event. Every node announcement, hub attestation,
// and federation receipt is one of these. The struct mirrors the Nostr wire
// format exactly — any Nostr client can read our events.
type Event struct {
	ID        string `json:"id"`
	Pubkey    string `json:"pubkey"`
	CreatedAt int64  `json:"created_at"`
	Kind      int    `json:"kind"`
	Tags      Tags   `json:"tags"`
	Content   string `json:"content"`
	Sig       string `json:"sig"`
}

// Tags is a list of tag arrays. Each tag is ["key", "value1", "value2", ...].
// Example: ["d", "node-abc123"] is a NIP-33 identifier tag.
type Tags []Tag

// Tag is a single tag: a string array where index 0 is the tag name.
type Tag []string

// KeyPair holds a Nostr identity. The private key is 32 bytes (scalar),
// the public key is 32 bytes (x-only, BIP-340). This is the same curve
// Bitcoin uses (secp256k1), so a Nostr identity IS a Bitcoin identity.
type KeyPair struct {
	PrivateKey *btcec.PrivateKey
	PublicKey  *btcec.PublicKey
}

// GenerateKeyPair creates a new random Nostr keypair.
// Under the hood: pick 32 random bytes as a scalar on secp256k1,
// derive the public key by multiplying the generator point.
func GenerateKeyPair() (*KeyPair, error) {
	privKey, err := btcec.NewPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("generate private key: %w", err)
	}
	return &KeyPair{
		PrivateKey: privKey,
		PublicKey:  privKey.PubKey(),
	}, nil
}

// PubkeyHex returns the 32-byte x-only public key as a 64-char hex string.
// "x-only" means we drop the Y coordinate (it can be derived from X).
// This is how Nostr identifies users — your pubkey IS your username.
func (kp *KeyPair) PubkeyHex() string {
	// schnorr.SerializePubKey gives the 32-byte x-only key (BIP-340 format).
	return hex.EncodeToString(schnorr.SerializePubKey(kp.PublicKey))
}

// PrivkeyHex returns the 32-byte private key as hex. Handle with care.
func (kp *KeyPair) PrivkeyHex() string {
	return hex.EncodeToString(kp.PrivateKey.Serialize())
}

// KeyPairFromPrivHex reconstructs a keypair from a hex-encoded private key.
func KeyPairFromPrivHex(privHex string) (*KeyPair, error) {
	privBytes, err := hex.DecodeString(privHex)
	if err != nil {
		return nil, fmt.Errorf("decode private key hex: %w", err)
	}
	if len(privBytes) != 32 {
		return nil, fmt.Errorf("private key must be 32 bytes, got %d", len(privBytes))
	}
	privKey, _ := btcec.PrivKeyFromBytes(privBytes)
	return &KeyPair{
		PrivateKey: privKey,
		PublicKey:  privKey.PubKey(),
	}, nil
}

// Serialize produces the NIP-01 canonical byte sequence for ID computation.
// Format: JSON array [0, pubkey, created_at, kind, tags, content]
// The 0 is a version marker — always 0 in the current Nostr spec.
// This is what gets hashed to produce the event ID.
func (e *Event) Serialize() ([]byte, error) {
	// Build the array manually. The order is fixed by NIP-01 and must be exact.
	serialized := []interface{}{
		0,
		e.Pubkey,
		e.CreatedAt,
		e.Kind,
		e.Tags,
		e.Content,
	}
	return json.Marshal(serialized)
}

// ComputeID hashes the serialized event to produce the event ID.
// ID = SHA-256(serialize([0, pubkey, created_at, kind, tags, content]))
// This is like a Bitcoin transaction's txid — change any field and the ID changes.
func (e *Event) ComputeID() (string, error) {
	serialized, err := e.Serialize()
	if err != nil {
		return "", fmt.Errorf("serialize event: %w", err)
	}
	hash := sha256.Sum256(serialized)
	return hex.EncodeToString(hash[:]), nil
}

// Sign signs the event with the given keypair, setting ID and Sig fields.
// Uses BIP-340 Schnorr signatures — same as Bitcoin Taproot.
// After signing, the event is tamper-proof: changing any field invalidates
// both the ID (hash won't match) and the signature.
func (e *Event) Sign(kp *KeyPair) error {
	e.Pubkey = kp.PubkeyHex()
	if e.CreatedAt == 0 {
		e.CreatedAt = time.Now().Unix()
	}

	id, err := e.ComputeID()
	if err != nil {
		return fmt.Errorf("compute event ID: %w", err)
	}
	e.ID = id

	// Hash the ID to get the 32-byte message for Schnorr signing.
	idBytes, err := hex.DecodeString(id)
	if err != nil {
		return fmt.Errorf("decode event ID: %w", err)
	}

	// btcec uses RFC 6979 deterministic nonces internally — this prevents
	// the class of bugs where bad randomness leaks private keys (the PS3 hack).
	sig, err := schnorr.Sign(kp.PrivateKey, idBytes)
	if err != nil {
		return fmt.Errorf("sign event: %w", err)
	}
	e.Sig = hex.EncodeToString(sig.Serialize())
	return nil
}

// Verify checks the event's ID and signature.
// Returns nil if valid, error describing what's wrong otherwise.
// Clients MUST call this on every event received from relays or the hub API.
func (e *Event) Verify() error {
	// Step 1: Recompute the ID and check it matches.
	computedID, err := e.ComputeID()
	if err != nil {
		return fmt.Errorf("compute ID for verification: %w", err)
	}
	if computedID != e.ID {
		return fmt.Errorf("event ID mismatch: computed %s, got %s", computedID, e.ID)
	}

	// Step 2: Parse the x-only public key (32 bytes → secp256k1 point).
	pubBytes, err := hex.DecodeString(e.Pubkey)
	if err != nil {
		return fmt.Errorf("decode pubkey: %w", err)
	}
	if len(pubBytes) != 32 {
		return fmt.Errorf("pubkey must be 32 bytes, got %d", len(pubBytes))
	}
	pubKey, err := schnorr.ParsePubKey(pubBytes)
	if err != nil {
		return fmt.Errorf("parse pubkey: %w", err)
	}

	// Step 3: Parse and verify the BIP-340 Schnorr signature.
	sigBytes, err := hex.DecodeString(e.Sig)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	sig, err := schnorr.ParseSignature(sigBytes)
	if err != nil {
		return fmt.Errorf("parse signature: %w", err)
	}

	idBytes, err := hex.DecodeString(e.ID)
	if err != nil {
		return fmt.Errorf("decode event ID: %w", err)
	}

	if !sig.Verify(idBytes, pubKey) {
		return fmt.Errorf("invalid signature")
	}
	return nil
}

// GetTagValue returns the first value for a given tag key, or empty string.
// Example: event.GetTagValue("d") returns the NIP-33 identifier.
func (e *Event) GetTagValue(key string) string {
	for _, tag := range e.Tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}

// GetTagValues returns all values for a given tag key.
func (e *Event) GetTagValues(key string) []string {
	var values []string
	for _, tag := range e.Tags {
		if len(tag) >= 2 && tag[0] == key {
			values = append(values, tag[1:]...)
		}
	}
	return values
}
