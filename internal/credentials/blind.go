// Package credentials — blind signature support for Phase 4.
//
// This file implements Chaumian ecash-style RSA blind signatures for
// bandwidth tickets. The Hub signs blinded messages without seeing the
// token secret, making it impossible to link ticket usage back to the buyer.
//
// System model: online bounded-risk authorization, NOT offline ecash.
// Nodes verify signatures locally, then check spent status with the Hub.
//
// Denomination model: each signing key maps to a fixed denomination
// (bytes_per_token) via immutable config. The key_id → denomination
// mapping MUST NOT change after tokens have been issued with that key.
//
// Cryptographic primitive: Full-Domain-Hash RSA Blind Signatures
// (via cryptoballot/rsablind). Not production-audited — suitable for
// PoC and grant applications. Production deployment requires audit.
package credentials

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/cryptoballot/fdh"
	"github.com/cryptoballot/rsablind"
)

// Blind signature constants.
const (
	// RSAKeySize is the RSA key size in bits. 2048 is the minimum for
	// security against Index Calculation Attacks with FDH.
	RSAKeySize = 2048

	// FDHHashSize is the Full-Domain-Hash output size in bits.
	// Must be 3/4 of key size per rsablind recommendations.
	FDHHashSize = 1536

	// MaxBlindMessagesPerRequest limits batch size to prevent DoS.
	MaxBlindMessagesPerRequest = 500

	// BlindTokenVersion is the protocol version for token ID derivation.
	BlindTokenVersion = 1
)

// DenominationKey is an immutable mapping from key_id to signing key
// and denomination. Once tokens are issued with a key, the denomination
// MUST NOT change (append-only config, never mutate).
type DenominationKey struct {
	KeyID         string
	BytesPerToken int64
	PrivateKey    *rsa.PrivateKey // nil on nodes (verifier-only)
	PublicKey     *rsa.PublicKey
}

// BlindToken is the credential a client presents to a node.
// The node verifies the RSA signature on TokenSecret using the Hub's
// public key, then calls /spend for double-spend prevention.
type BlindToken struct {
	Version     uint8  `json:"version"`      // protocol version (1)
	KeyID       string `json:"key_id"`       // which denomination key signed this
	TokenSecret []byte `json:"token_secret"` // client-generated random secret
	Signature   []byte `json:"signature"`    // RSA blind signature (unblinded)
}

// TokenID derives the canonical token identifier using domain-separated SHA256.
// This ID is used for double-spend tracking in the spent_tokens table.
// Format: SHA256("ARFL|v1|{key_id}|{token_secret_hex}")
func (t *BlindToken) TokenID() string {
	h := sha256.New()
	h.Write([]byte(fmt.Sprintf("ARFL|v%d|%s|%s", t.Version, t.KeyID, hex.EncodeToString(t.TokenSecret))))
	return hex.EncodeToString(h.Sum(nil))
}

// --- Mint (Hub-side: signs blinded messages) ---

// BlindMint signs blinded messages from clients.
// The Hub holds the RSA private key; it never sees the token secrets.
type BlindMint interface {
	// SignBlinded signs a batch of blinded messages.
	// Returns one signature per message, or an error.
	SignBlinded(keyID string, blindedMessages [][]byte) ([][]byte, error)

	// PublicKeyBytes returns the DER-encoded public key for a given key_id.
	// Nodes need this to verify token signatures.
	PublicKeyBytes(keyID string) ([]byte, error)

	// Denomination returns the bytes_per_token for a given key_id.
	Denomination(keyID string) (int64, error)
}

// RSABlindMint implements BlindMint using Full-Domain-Hash RSA blind signatures.
type RSABlindMint struct {
	keys map[string]*DenominationKey
	mu   sync.RWMutex
}

// NewRSABlindMint creates a mint with one or more denomination keys.
func NewRSABlindMint(keys []*DenominationKey) *RSABlindMint {
	m := &RSABlindMint{keys: make(map[string]*DenominationKey, len(keys))}
	for _, k := range keys {
		m.keys[k.KeyID] = k
	}
	return m
}

// SignBlinded signs a batch of blinded messages with the specified key.
func (m *RSABlindMint) SignBlinded(keyID string, blindedMessages [][]byte) ([][]byte, error) {
	if len(blindedMessages) == 0 {
		return nil, fmt.Errorf("no blinded messages provided")
	}
	if len(blindedMessages) > MaxBlindMessagesPerRequest {
		return nil, fmt.Errorf("too many messages: %d exceeds max %d", len(blindedMessages), MaxBlindMessagesPerRequest)
	}

	m.mu.RLock()
	key, ok := m.keys[keyID]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("unknown key_id: %s", keyID)
	}
	if key.PrivateKey == nil {
		return nil, fmt.Errorf("key %s has no private key (verifier-only)", keyID)
	}

	sigs := make([][]byte, len(blindedMessages))
	for i, bm := range blindedMessages {
		sig, err := rsablind.BlindSign(key.PrivateKey, bm)
		if err != nil {
			return nil, fmt.Errorf("blind sign message %d: %w", i, err)
		}
		sigs[i] = sig
	}

	return sigs, nil
}

// PublicKeyBytes returns the DER-encoded public key for distribution to nodes.
func (m *RSABlindMint) PublicKeyBytes(keyID string) ([]byte, error) {
	m.mu.RLock()
	key, ok := m.keys[keyID]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("unknown key_id: %s", keyID)
	}
	return x509.MarshalPKIXPublicKey(key.PublicKey)
}

// Denomination returns the bytes_per_token for a key_id.
func (m *RSABlindMint) Denomination(keyID string) (int64, error) {
	m.mu.RLock()
	key, ok := m.keys[keyID]
	m.mu.RUnlock()

	if !ok {
		return 0, fmt.Errorf("unknown key_id: %s", keyID)
	}
	return key.BytesPerToken, nil
}

// --- Verifier (Node-side: verifies token signatures) ---

// BlindVerifier verifies RSA blind signatures on tokens.
// Nodes hold the Hub's public key(s) and verify locally.
type BlindVerifier interface {
	// Verify checks that the token's signature is valid for its TokenSecret
	// under the specified key_id.
	Verify(token *BlindToken) error

	// Denomination returns the bytes_per_token for a key_id.
	Denomination(keyID string) (int64, error)
}

// RSABlindVerifier implements BlindVerifier.
type RSABlindVerifier struct {
	keys map[string]*DenominationKey // key_id → public key + denomination
	mu   sync.RWMutex
}

// NewRSABlindVerifier creates a verifier with the given denomination keys.
// Only public keys are needed (PrivateKey should be nil).
func NewRSABlindVerifier(keys []*DenominationKey) *RSABlindVerifier {
	v := &RSABlindVerifier{keys: make(map[string]*DenominationKey, len(keys))}
	for _, k := range keys {
		v.keys[k.KeyID] = k
	}
	return v
}

// Verify checks the blind signature on a token.
func (v *RSABlindVerifier) Verify(token *BlindToken) error {
	if token == nil {
		return ErrInvalidTicket
	}
	if token.Version != BlindTokenVersion {
		return fmt.Errorf("%w: unsupported version %d", ErrInvalidTicket, token.Version)
	}
	if token.KeyID == "" {
		return fmt.Errorf("%w: empty key_id", ErrInvalidTicket)
	}
	if len(token.TokenSecret) == 0 {
		return fmt.Errorf("%w: empty token secret", ErrInvalidTicket)
	}
	if len(token.Signature) == 0 {
		return fmt.Errorf("%w: empty signature", ErrInvalidTicket)
	}

	v.mu.RLock()
	key, ok := v.keys[token.KeyID]
	v.mu.RUnlock()

	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownKeyID, token.KeyID)
	}

	// Hash the token secret using Full-Domain-Hash (same as client-side blinding).
	hashed := fdh.Sum(crypto.SHA256, FDHHashSize, token.TokenSecret)

	// Verify the unblinded signature against the hashed token secret.
	if err := rsablind.VerifyBlindSignature(key.PublicKey, hashed, token.Signature); err != nil {
		return ErrInvalidMAC // reuse existing error — "MAC" covers both HMAC and blind sigs
	}

	return nil
}

// Denomination returns the bytes_per_token for a key_id.
func (v *RSABlindVerifier) Denomination(keyID string) (int64, error) {
	v.mu.RLock()
	key, ok := v.keys[keyID]
	v.mu.RUnlock()

	if !ok {
		return 0, fmt.Errorf("unknown key_id: %s", keyID)
	}
	return key.BytesPerToken, nil
}

// AddKey registers a new denomination key (rotation support).
func (v *RSABlindVerifier) AddKey(key *DenominationKey) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.keys[key.KeyID] = key
}

// --- Client-side helpers (for arfl-client) ---

// BlindedMessage holds the blinded message and the unblinding factor.
// The client keeps the unblinder secret; the Hub never sees it.
type BlindedMessage struct {
	Blinded   []byte // sent to Hub for blind signing
	Unblinder []byte // kept by client for unblinding
}

// BlindTokenSecret blinds a token secret for signing by the Hub.
func BlindTokenSecret(pubKey *rsa.PublicKey, tokenSecret []byte) (*BlindedMessage, error) {
	hashed := fdh.Sum(crypto.SHA256, FDHHashSize, tokenSecret)

	blinded, unblinder, err := rsablind.Blind(pubKey, hashed)
	if err != nil {
		return nil, fmt.Errorf("blind token: %w", err)
	}

	return &BlindedMessage{
		Blinded:   blinded,
		Unblinder: unblinder,
	}, nil
}

// UnblindSignature removes the blinding factor from a blind signature.
func UnblindSignature(pubKey *rsa.PublicKey, blindSig []byte, unblinder []byte) []byte {
	return rsablind.Unblind(pubKey, blindSig, unblinder)
}

// GenerateTokenSecret creates a cryptographically random 32-byte token secret.
func GenerateTokenSecret() ([]byte, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate token secret: %w", err)
	}
	return secret, nil
}

// GenerateDenominationKey creates a new RSA key pair for a denomination.
func GenerateDenominationKey(keyID string, bytesPerToken int64) (*DenominationKey, error) {
	if keyID == "" {
		return nil, fmt.Errorf("key_id must not be empty")
	}
	if bytesPerToken <= 0 {
		return nil, fmt.Errorf("bytes_per_token must be positive")
	}

	privKey, err := rsa.GenerateKey(rand.Reader, RSAKeySize)
	if err != nil {
		return nil, fmt.Errorf("generate RSA key: %w", err)
	}

	return &DenominationKey{
		KeyID:         keyID,
		BytesPerToken: bytesPerToken,
		PrivateKey:    privKey,
		PublicKey:     &privKey.PublicKey,
	}, nil
}

// PublicKeyFromDER parses a DER-encoded public key.
// Used by nodes to load the Hub's public key from config or Nostr.
func PublicKeyFromDER(der []byte) (*rsa.PublicKey, error) {
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("not an RSA public key")
	}
	return rsaPub, nil
}
