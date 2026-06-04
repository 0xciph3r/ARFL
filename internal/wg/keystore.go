package wg

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters — these control how expensive it is to guess a passphrase.
// Higher values = slower brute-force, but also slower legitimate decryption.
const (
	argonTime    = 3         // iterations — how many passes over memory
	argonMemory  = 64 * 1024 // 64 MB — memory required per attempt
	argonThreads = 4         // parallelism
	argonKeyLen  = 32        // output key length (256 bits for AES-256)
	saltLen      = 16        // random salt length
)

// encryptedKeyFile is the on-disk format for an encrypted WireGuard keypair.
// The private key is encrypted. The public key is stored in cleartext because
// it's your network identity — shared openly, like a Bitcoin address.
type encryptedKeyFile struct {
	PublicKey      string `json:"public_key"`
	EncryptedKey   []byte `json:"encrypted_key"`   // AES-256-GCM ciphertext + tag
	Nonce          []byte `json:"nonce"`            // 12-byte GCM nonce
	Salt           []byte `json:"salt"`             // 16-byte Argon2 salt
	Version        int    `json:"version"`          // format version for future changes
}

// SaveKeyPairEncrypted encrypts the private key with a passphrase and saves to disk.
//
// The process:
//  1. Generate random salt (16 bytes) — unique per file, prevents rainbow tables
//  2. Derive AES key from passphrase via Argon2id — makes brute-force slow
//  3. Generate random nonce (12 bytes) — required by AES-GCM, must never repeat
//  4. Encrypt private key with AES-256-GCM — authenticated encryption
//  5. Save salt + nonce + ciphertext + public key to disk
func SaveKeyPairEncrypted(path string, kp *KeyPair, passphrase string) error {
	// Step 1: random salt
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return fmt.Errorf("generate salt: %w", err)
	}

	// Step 2: derive encryption key from passphrase
	// Argon2id: resistant to both GPU attacks (memory-hard) and side-channel attacks
	key := argon2.IDKey([]byte(passphrase), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	// Step 3: create AES-256-GCM cipher
	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("create GCM: %w", err)
	}

	// Step 4: random nonce + encrypt
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(kp.PrivateKey), nil)

	// Step 5: save to disk
	ekf := encryptedKeyFile{
		PublicKey:    kp.PublicKey,
		EncryptedKey: ciphertext,
		Nonce:        nonce,
		Salt:         salt,
		Version:      1,
	}

	data, err := json.MarshalIndent(ekf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal key file: %w", err)
	}

	// 0600 = owner read/write only. Belt and suspenders — encrypted AND restricted.
	return os.WriteFile(path, data, 0600)
}

// LoadKeyPairEncrypted decrypts a saved keypair using the passphrase.
// Returns an error if the passphrase is wrong (GCM authentication fails).
func LoadKeyPairEncrypted(path string, passphrase string) (*KeyPair, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key file: %w", err)
	}

	var ekf encryptedKeyFile
	if err := json.Unmarshal(data, &ekf); err != nil {
		return nil, fmt.Errorf("parse key file: %w", err)
	}

	if ekf.Version != 1 {
		return nil, fmt.Errorf("unsupported key file version: %d", ekf.Version)
	}

	// Derive the same key from passphrase + stored salt
	key := argon2.IDKey([]byte(passphrase), ekf.Salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	// Decrypt
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	// GCM decryption verifies authenticity — wrong passphrase = error, not garbage
	plaintext, err := gcm.Open(nil, ekf.Nonce, ekf.EncryptedKey, nil)
	if err != nil {
		return nil, errors.New("decryption failed: wrong passphrase or corrupted key file")
	}

	return &KeyPair{
		PrivateKey: string(plaintext),
		PublicKey:  ekf.PublicKey,
	}, nil
}
