// NIP-44 v2 encrypted payloads for Nostr.
//
// Implements the versioned encryption scheme from
// https://github.com/nostr-protocol/nips/blob/master/44.md
//
// Algorithm: secp256k1 ECDH → HKDF-extract → conversation key →
// HKDF-expand (per-message) → ChaCha20 + HMAC-SHA256 + padding.
//
// Used by ARFL to deliver Cashu tokens from clients to nodes via
// encrypted Nostr events. The hub cannot read these messages.
package nostr

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/btcsuite/btcd/btcec/v2"
	"golang.org/x/crypto/chacha20"
	"golang.org/x/crypto/hkdf"
)

// NIP-44 errors.
var (
	ErrNIP44UnknownVersion = errors.New("nip44: unknown version")
	ErrNIP44InvalidPayload = errors.New("nip44: invalid payload")
	ErrNIP44DecryptFailed  = errors.New("nip44: decryption failed (bad MAC)")
	ErrNIP44InvalidPadding = errors.New("nip44: invalid padding")
)

const (
	nip44Version          byte = 2
	nip44Salt                  = "nip44-v2"
	nip44NonceSize             = 32
	nip44MACSize               = 32
	nip44MinPlaintextSize      = 1
	nip44MaxPlaintextSize      = 65535 // practical limit for our use case
	nip44MinPayloadLen         = 132   // base64 chars
	nip44MinDecodedLen         = 99    // version(1) + nonce(32) + min_padded(34) + mac(32)
)

// GetConversationKey computes the NIP-44 conversation key between two parties.
// conv(a, B) == conv(b, A) — the key is symmetric.
func GetConversationKey(privKey *btcec.PrivateKey, pubKey *btcec.PublicKey) ([]byte, error) {
	// ECDH: scalar multiplication of pubKey by privKey.
	// btcec gives us the full point; we need just the 32-byte x coordinate.
	var pubKeyJacobian btcec.JacobianPoint
	pubKey.AsJacobian(&pubKeyJacobian)

	var sharedPoint btcec.JacobianPoint
	btcec.ScalarMultNonConst(&privKey.Key, &pubKeyJacobian, &sharedPoint)
	sharedPoint.ToAffine()

	sharedX := sharedPoint.X.Bytes()

	// HKDF-extract with salt="nip44-v2".
	extractor := hkdf.Extract(sha256.New, sharedX[:], []byte(nip44Salt))
	return extractor, nil
}

// Encrypt encrypts plaintext using NIP-44 v2.
// conversationKey is from GetConversationKey().
func Encrypt(plaintext string, conversationKey []byte) (string, error) {
	if len(conversationKey) != 32 {
		return "", fmt.Errorf("nip44: conversation key must be 32 bytes")
	}

	ptBytes := []byte(plaintext)
	if len(ptBytes) < nip44MinPlaintextSize || len(ptBytes) > nip44MaxPlaintextSize {
		return "", fmt.Errorf("nip44: plaintext length %d out of range [%d, %d]",
			len(ptBytes), nip44MinPlaintextSize, nip44MaxPlaintextSize)
	}

	// Generate random 32-byte nonce.
	nonce := make([]byte, nip44NonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("nip44: generate nonce: %w", err)
	}

	return encryptWithNonce(ptBytes, conversationKey, nonce)
}

// encryptWithNonce is the internal encrypt that accepts an explicit nonce (for testing).
func encryptWithNonce(plaintext, conversationKey, nonce []byte) (string, error) {
	// Pad plaintext.
	padded := pad(plaintext)

	// Derive per-message keys: chacha_key(32) + chacha_nonce(12) + hmac_key(32) = 76 bytes.
	messageKeys, err := getMessageKeys(conversationKey, nonce)
	if err != nil {
		return "", err
	}
	chachaKey := messageKeys[:32]
	chaChaNonce := messageKeys[32:44]
	hmacKey := messageKeys[44:76]

	// ChaCha20 encrypt.
	cipher, err := chacha20.NewUnauthenticatedCipher(chachaKey, chaChaNonce)
	if err != nil {
		return "", fmt.Errorf("nip44: create chacha20: %w", err)
	}
	ciphertext := make([]byte, len(padded))
	cipher.XORKeyStream(ciphertext, padded)

	// HMAC-SHA256 over (nonce || ciphertext).
	mac := hmacAAD(hmacKey, ciphertext, nonce)

	// Encode: version(1) || nonce(32) || ciphertext || mac(32).
	payload := make([]byte, 0, 1+len(nonce)+len(ciphertext)+len(mac))
	payload = append(payload, nip44Version)
	payload = append(payload, nonce...)
	payload = append(payload, ciphertext...)
	payload = append(payload, mac...)

	return base64.StdEncoding.EncodeToString(payload), nil
}

// Decrypt decrypts a NIP-44 v2 payload.
func Decrypt(payload string, conversationKey []byte) (string, error) {
	if len(conversationKey) != 32 {
		return "", fmt.Errorf("nip44: conversation key must be 32 bytes")
	}

	if len(payload) == 0 {
		return "", ErrNIP44InvalidPayload
	}

	// Check for future non-base64 flag before length check.
	if payload[0] == '#' {
		return "", ErrNIP44UnknownVersion
	}

	if len(payload) < nip44MinPayloadLen {
		return "", ErrNIP44InvalidPayload
	}

	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", fmt.Errorf("nip44: base64 decode: %w", err)
	}

	if len(data) < nip44MinDecodedLen {
		return "", ErrNIP44InvalidPayload
	}

	version := data[0]
	if version != nip44Version {
		return "", fmt.Errorf("%w: got %d", ErrNIP44UnknownVersion, version)
	}

	nonce := data[1:33]
	ciphertext := data[33 : len(data)-32]
	mac := data[len(data)-32:]

	// Derive message keys.
	messageKeys, err := getMessageKeys(conversationKey, nonce)
	if err != nil {
		return "", err
	}
	chachaKey := messageKeys[:32]
	chaChaNonce := messageKeys[32:44]
	hmacKey := messageKeys[44:76]

	// Verify MAC before decrypting.
	expectedMAC := hmacAAD(hmacKey, ciphertext, nonce)
	if !hmac.Equal(mac, expectedMAC) {
		return "", ErrNIP44DecryptFailed
	}

	// ChaCha20 decrypt.
	cipher, err := chacha20.NewUnauthenticatedCipher(chachaKey, chaChaNonce)
	if err != nil {
		return "", fmt.Errorf("nip44: create chacha20: %w", err)
	}
	padded := make([]byte, len(ciphertext))
	cipher.XORKeyStream(padded, ciphertext)

	// Unpad.
	plaintext, err := unpad(padded)
	if err != nil {
		return "", err
	}

	return string(plaintext), nil
}

// getMessageKeys derives per-message ChaCha20 key + nonce + HMAC key via HKDF-expand.
func getMessageKeys(conversationKey, nonce []byte) ([]byte, error) {
	expander := hkdf.Expand(sha256.New, conversationKey, nonce)
	keys := make([]byte, 76) // 32 + 12 + 32
	if _, err := io.ReadFull(expander, keys); err != nil {
		return nil, fmt.Errorf("nip44: hkdf expand: %w", err)
	}
	return keys, nil
}

// hmacAAD computes HMAC-SHA256(key, aad || message).
func hmacAAD(key, message, aad []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(aad)
	h.Write(message)
	return h.Sum(nil)
}

// pad applies NIP-44 padding: u16 length prefix + plaintext + zero padding.
func pad(plaintext []byte) []byte {
	unpaddedLen := len(plaintext)
	paddedLen := calcPaddedLen(unpaddedLen)

	// For our use case, plaintext is always < 65536 bytes (2-byte prefix).
	prefix := make([]byte, 2)
	binary.BigEndian.PutUint16(prefix, uint16(unpaddedLen))

	result := make([]byte, 2+paddedLen)
	copy(result[:2], prefix)
	copy(result[2:], plaintext)
	// Remaining bytes are already zero.
	return result
}

// unpad removes NIP-44 padding and returns the original plaintext.
func unpad(padded []byte) ([]byte, error) {
	if len(padded) < 2 {
		return nil, ErrNIP44InvalidPadding
	}

	firstTwo := binary.BigEndian.Uint16(padded[0:2])
	var unpaddedLen int
	var prefixLen int

	if firstTwo == 0 {
		// Extended format: 6-byte prefix.
		if len(padded) < 6 {
			return nil, ErrNIP44InvalidPadding
		}
		unpaddedLen = int(binary.BigEndian.Uint32(padded[2:6]))
		if unpaddedLen < 65536 {
			return nil, ErrNIP44InvalidPadding
		}
		prefixLen = 6
	} else {
		unpaddedLen = int(firstTwo)
		prefixLen = 2
	}

	if unpaddedLen == 0 {
		return nil, ErrNIP44InvalidPadding
	}
	if prefixLen+unpaddedLen > len(padded) {
		return nil, ErrNIP44InvalidPadding
	}

	// Verify padding length matches expected.
	expectedPaddedLen := calcPaddedLen(unpaddedLen)
	if len(padded) != prefixLen+expectedPaddedLen {
		return nil, ErrNIP44InvalidPadding
	}

	return padded[prefixLen : prefixLen+unpaddedLen], nil
}

// calcPaddedLen computes the NIP-44 padded length for a given plaintext length.
func calcPaddedLen(unpaddedLen int) int {
	if unpaddedLen <= 32 {
		return 32
	}
	nextPower := 1 << (int(math.Floor(math.Log2(float64(unpaddedLen-1)))) + 1)
	var chunk int
	if nextPower <= 256 {
		chunk = 32
	} else {
		chunk = nextPower / 8
	}
	return chunk * (((unpaddedLen - 1) / chunk) + 1)
}
