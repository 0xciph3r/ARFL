package wallet

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/elnosh/gonuts/cashu"
	gcrypto "github.com/elnosh/gonuts/crypto"
	"golang.org/x/crypto/argon2"
)

const backupVersion = 1

// ErrBadBackupPassphrase is returned when a backup cannot be decrypted.
var ErrBadBackupPassphrase = errors.New("could not open the backup: wrong passphrase or damaged file")

// backupFile is the on-disk backup. Like the vault, only KDF inputs are clear.
type backupFile struct {
	Format     string `json:"format"`
	Version    int    `json:"version"`
	Salt       []byte `json:"salt"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

type backupPayload struct {
	DeviceKey []byte                  `json:"device_key"`
	Hubs      map[string]cashu.Proofs `json:"hubs"`
}

// SealBackup encrypts the device key and every held proof under passphrase.
// The file is a bearer instrument once opened, so it is never written without
// one.
func SealBackup(deviceKey []byte, hubs map[string]cashu.Proofs, passphrase string) ([]byte, error) {
	if len(passphrase) < 4 {
		return nil, fmt.Errorf("backup passphrase must be at least 4 characters")
	}
	if len(deviceKey) == 0 {
		return nil, fmt.Errorf("device key is required")
	}
	plaintext, err := json.Marshal(backupPayload{DeviceKey: deviceKey, Hubs: hubs})
	if err != nil {
		return nil, fmt.Errorf("encode backup: %w", err)
	}
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(passphrase), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	nonce, ciphertext, err := encrypt(key, plaintext)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(backupFile{
		Format: "arfl-backup", Version: backupVersion, Salt: salt, Nonce: nonce, Ciphertext: ciphertext,
	}, "", "  ")
}

// OpenBackup decrypts a backup made by SealBackup.
func OpenBackup(raw []byte, passphrase string) (deviceKey []byte, hubs map[string]cashu.Proofs, err error) {
	var file backupFile
	if err := json.Unmarshal(raw, &file); err != nil || file.Format != "arfl-backup" {
		return nil, nil, fmt.Errorf("this is not an ARFL backup file")
	}
	if file.Version != backupVersion {
		return nil, nil, fmt.Errorf("unsupported backup version %d", file.Version)
	}
	key := argon2.IDKey([]byte(passphrase), file.Salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	plaintext, err := decrypt(key, file.Nonce, file.Ciphertext)
	if err != nil {
		return nil, nil, ErrBadBackupPassphrase
	}
	var payload backupPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return nil, nil, fmt.Errorf("parse backup: %w", err)
	}
	if payload.Hubs == nil {
		payload.Hubs = map[string]cashu.Proofs{}
	}
	return payload.DeviceKey, payload.Hubs, nil
}

// Snapshot returns a copy of every held proof, keyed by hub URL.
func (s *EncryptedProofStore) Snapshot() map[string]cashu.Proofs {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]cashu.Proofs, len(s.data.Hubs))
	for hub, proofs := range s.data.Hubs {
		cp := make(cashu.Proofs, len(proofs))
		copy(cp, proofs)
		out[hub] = cp
	}
	return out
}

const maxCheckStateYs = 100

// UnspentProofs asks the hub which of proofs are still spendable (NUT-07) and
// returns only those. A restored backup can hold proofs the old device spent
// after the backup was taken; keeping them would show a balance that fails on
// first use.
func (c *MintClient) UnspentProofs(ctx context.Context, proofs cashu.Proofs) (cashu.Proofs, error) {
	var out cashu.Proofs
	for start := 0; start < len(proofs); start += maxCheckStateYs {
		end := min(start+maxCheckStateYs, len(proofs))
		batch := proofs[start:end]

		ys := make([]string, len(batch))
		for i, p := range batch {
			y, err := gcrypto.HashToCurve([]byte(p.Secret))
			if err != nil {
				return nil, fmt.Errorf("hash proof secret: %w", err)
			}
			ys[i] = hex.EncodeToString(y.SerializeCompressed())
		}

		var resp struct {
			States []struct {
				Y     string `json:"Y"`
				State string `json:"state"`
			} `json:"states"`
		}
		if err := c.do(ctx, "POST", "/v1/checkstate", map[string]any{"Ys": ys}, &resp); err != nil {
			return nil, err
		}
		spent := make(map[string]bool, len(resp.States))
		for _, st := range resp.States {
			spent[st.Y] = st.State != "UNSPENT"
		}
		for i, p := range batch {
			if s, ok := spent[ys[i]]; ok && !s {
				out = append(out, p)
			}
		}
	}
	return out, nil
}

// MoveHub re-files every proof held under from as belonging to to, for when a
// hub's address changes but its mint does not. It is a no-op when from holds
// nothing.
func (s *EncryptedProofStore) MoveHub(from, to string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	moving := s.data.Hubs[from]
	if len(moving) == 0 || from == to {
		return nil
	}
	existing := make(map[string]struct{}, len(s.data.Hubs[to]))
	for _, p := range s.data.Hubs[to] {
		existing[p.Secret] = struct{}{}
	}
	for _, p := range moving {
		if _, dup := existing[p.Secret]; !dup {
			s.data.Hubs[to] = append(s.data.Hubs[to], p)
		}
	}
	delete(s.data.Hubs, from)
	return s.persist()
}
