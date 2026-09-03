package wallet

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"

	"github.com/elnosh/gonuts/cashu"
	"golang.org/x/crypto/argon2"
)

// Argon2id parameters, matching internal/wg/keystore.go so the project has one
// key-derivation profile rather than several subtly different ones.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
	saltLen      = 16

	proofFileVersion = 1
)

// ErrInsufficientBalance is returned when the store holds fewer sats than requested.
var ErrInsufficientBalance = errors.New("insufficient token balance")

// ProofStore persists unspent Cashu proofs, keyed by the hub that issued them.
//
// Proofs are bearer instruments: whoever holds one can spend it. Implementations
// must therefore protect them at rest and must not return the same proof twice
// from Take.
type ProofStore interface {
	// List returns all unspent proofs held for a hub.
	List(hubURL string) (cashu.Proofs, error)

	// Add records newly minted or returned proofs.
	Add(hubURL string, proofs cashu.Proofs) error

	// Take atomically removes and returns proofs worth at least amountSats.
	// It returns ErrInsufficientBalance if the hub's balance is too low.
	Take(hubURL string, amountSats uint64) (cashu.Proofs, error)
}

// proofFile is the on-disk format. Proof data is encrypted as a single blob;
// only the KDF parameters are stored in the clear.
type proofFile struct {
	Version    int    `json:"version"`
	Salt       []byte `json:"salt"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

// vault is the decrypted payload: hub URL → unspent proofs.
type vault struct {
	Hubs map[string]cashu.Proofs `json:"hubs"`
}

// EncryptedProofStore is a passphrase-protected, file-backed ProofStore.
//
// The whole vault is held in memory and rewritten on every mutation. Wallets
// hold tens to hundreds of proofs, so this stays cheap while making atomicity
// easy to reason about.
type EncryptedProofStore struct {
	mu   sync.Mutex
	path string
	key  []byte // derived once; the passphrase itself is not retained
	salt []byte
	data vault
}

// OpenProofStore opens an existing vault or creates a new one at path.
//
// The passphrase is stretched with Argon2id. Supplying the wrong passphrase for
// an existing vault returns an error rather than silently starting empty, which
// would look to the user like their tokens had vanished.
func OpenProofStore(path, passphrase string) (*EncryptedProofStore, error) {
	if path == "" {
		return nil, fmt.Errorf("proof store path is required")
	}
	if passphrase == "" {
		return nil, fmt.Errorf("passphrase is required to protect stored tokens")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create store directory: %w", err)
	}

	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return newProofStore(path, passphrase)
	}
	if err != nil {
		return nil, fmt.Errorf("read proof store: %w", err)
	}

	var file proofFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse proof store: %w", err)
	}
	if file.Version != proofFileVersion {
		return nil, fmt.Errorf("unsupported proof store version %d", file.Version)
	}

	key := argon2.IDKey([]byte(passphrase), file.Salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	plaintext, err := decrypt(key, file.Nonce, file.Ciphertext)
	if err != nil {
		return nil, errors.New("could not open token store: wrong passphrase or corrupted file")
	}

	var data vault
	if err := json.Unmarshal(plaintext, &data); err != nil {
		return nil, fmt.Errorf("parse token vault: %w", err)
	}
	if data.Hubs == nil {
		data.Hubs = make(map[string]cashu.Proofs)
	}

	return &EncryptedProofStore{path: path, key: key, salt: file.Salt, data: data}, nil
}

// newProofStore initialises an empty vault and writes it to disk immediately,
// so a failure to persist surfaces before the user pays for any tokens.
func newProofStore(path, passphrase string) (*EncryptedProofStore, error) {
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}

	key := argon2.IDKey([]byte(passphrase), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	store := &EncryptedProofStore{
		path: path,
		key:  key,
		salt: salt,
		data: vault{Hubs: make(map[string]cashu.Proofs)},
	}

	if err := store.persist(); err != nil {
		return nil, err
	}
	return store, nil
}

// List returns a copy of the unspent proofs for a hub.
func (s *EncryptedProofStore) List(hubURL string) (cashu.Proofs, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored := s.data.Hubs[hubURL]
	out := make(cashu.Proofs, len(stored))
	copy(out, stored)
	return out, nil
}

// Add appends proofs for a hub and persists the vault.
func (s *EncryptedProofStore) Add(hubURL string, proofs cashu.Proofs) error {
	if hubURL == "" {
		return fmt.Errorf("hub URL is required")
	}
	if len(proofs) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Guard against re-adding a proof that is already held. Duplicates would
	// inflate the displayed balance and then fail as double-spends at a node.
	existing := make(map[string]struct{}, len(s.data.Hubs[hubURL]))
	for _, proof := range s.data.Hubs[hubURL] {
		existing[proof.Secret] = struct{}{}
	}

	for _, proof := range proofs {
		if _, duplicate := existing[proof.Secret]; duplicate {
			continue
		}
		existing[proof.Secret] = struct{}{}
		s.data.Hubs[hubURL] = append(s.data.Hubs[hubURL], proof)
	}

	return s.persist()
}

// Take removes and returns proofs worth at least amountSats.
//
// Selection prefers the largest denominations that still fit, then falls back
// to the smallest proof that covers the remainder. This keeps the proof count
// low while avoiding overpayment where an exact fit exists.
func (s *EncryptedProofStore) Take(hubURL string, amountSats uint64) (cashu.Proofs, error) {
	if amountSats == 0 {
		return nil, fmt.Errorf("amount must be greater than zero")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	available := s.data.Hubs[hubURL]
	var total uint64
	for _, proof := range available {
		total += proof.Amount
	}
	if total < amountSats {
		return nil, fmt.Errorf("%w: have %d sats, need %d", ErrInsufficientBalance, total, amountSats)
	}

	// Work on index order sorted by descending denomination.
	order := make([]int, len(available))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return available[order[a]].Amount > available[order[b]].Amount
	})

	selected := make(map[int]struct{})
	var collected uint64

	for _, idx := range order {
		if collected >= amountSats {
			break
		}
		if available[idx].Amount <= amountSats-collected {
			selected[idx] = struct{}{}
			collected += available[idx].Amount
		}
	}

	// Large denominations alone may not reach the target (e.g. needing 3 sats
	// from proofs of 4 and 1). Cover any shortfall with the smallest proof that
	// closes it, which overpays by the least possible amount.
	if collected < amountSats {
		best := -1
		for _, idx := range order {
			if _, taken := selected[idx]; taken {
				continue
			}
			if available[idx].Amount < amountSats-collected {
				continue
			}
			if best == -1 || available[idx].Amount < available[best].Amount {
				best = idx
			}
		}
		if best == -1 {
			return nil, fmt.Errorf("%w: cannot assemble %d sats from available denominations",
				ErrInsufficientBalance, amountSats)
		}
		selected[best] = struct{}{}
		collected += available[best].Amount
	}

	taken := make(cashu.Proofs, 0, len(selected))
	remaining := make(cashu.Proofs, 0, len(available)-len(selected))
	for i, proof := range available {
		if _, ok := selected[i]; ok {
			taken = append(taken, proof)
			continue
		}
		remaining = append(remaining, proof)
	}

	s.data.Hubs[hubURL] = remaining

	// Persist before returning. If the write fails the caller must not receive
	// proofs that are still recorded on disk, or a crash would let them be spent twice.
	if err := s.persist(); err != nil {
		s.data.Hubs[hubURL] = available
		return nil, err
	}

	return taken, nil
}

// persist encrypts and atomically writes the vault. Callers must hold s.mu.
func (s *EncryptedProofStore) persist() error {
	plaintext, err := json.Marshal(s.data)
	if err != nil {
		return fmt.Errorf("encode token vault: %w", err)
	}

	nonce, ciphertext, err := encrypt(s.key, plaintext)
	if err != nil {
		return err
	}

	encoded, err := json.MarshalIndent(proofFile{
		Version:    proofFileVersion,
		Salt:       s.salt,
		Nonce:      nonce,
		Ciphertext: ciphertext,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode proof store: %w", err)
	}

	// Write to a temporary file and rename over the original. A partial write
	// would otherwise destroy every token the user has paid for.
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".arfl-tokens-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()

	defer func() {
		// No-op once the rename has succeeded and the file no longer exists.
		_ = os.Remove(tmpName)
	}()

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("set permissions: %w", err)
	}
	if _, err := tmp.Write(encoded); err != nil {
		tmp.Close()
		return fmt.Errorf("write proof store: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("flush proof store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close proof store: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replace proof store: %w", err)
	}

	return nil
}

// encrypt seals plaintext with AES-256-GCM under a fresh nonce.
func encrypt(key, plaintext []byte) (nonce, ciphertext []byte, err error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, nil, err
	}

	nonce = make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("generate nonce: %w", err)
	}

	return nonce, gcm.Seal(nil, nonce, plaintext, nil), nil
}

// decrypt opens an AES-256-GCM ciphertext, verifying authenticity.
func decrypt(key, nonce, ciphertext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, ciphertext, nil)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	return gcm, nil
}

// DefaultStorePath returns the per-user location of the token vault.
//
//	Linux:   ~/.local/share/arfl/tokens.json
//	macOS:   ~/Library/Application Support/arfl/tokens.json
//	Windows: %APPDATA%\arfl\tokens.json
func DefaultStorePath() (string, error) {
	var base string

	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, "Library", "Application Support", "arfl")
	case "windows":
		appdata := os.Getenv("APPDATA")
		if appdata == "" {
			return "", fmt.Errorf("APPDATA is not set")
		}
		base = filepath.Join(appdata, "arfl")
	default:
		dataDir := os.Getenv("XDG_DATA_HOME")
		if dataDir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			dataDir = filepath.Join(home, ".local", "share")
		}
		base = filepath.Join(dataDir, "arfl")
	}

	return filepath.Join(base, "tokens.json"), nil
}
