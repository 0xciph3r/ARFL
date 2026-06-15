package credentials

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
)

// DenominationKeyFile is the on-disk format for a denomination key.
// The private key is PEM-encoded RSA. The metadata (key_id, denomination)
// is stored alongside so the mapping is self-describing and immutable.
type DenominationKeyFile struct {
	KeyID         string `json:"key_id"`
	BytesPerToken int64  `json:"bytes_per_token"`
	PrivateKeyPEM string `json:"private_key_pem"`
}

// SaveDenominationKey writes a denomination key to a JSON file.
// The file contains the RSA private key (PEM) and denomination metadata.
// File permissions: 0600 (owner read/write only — this is key material).
func SaveDenominationKey(path string, key *DenominationKey) error {
	privDER := x509.MarshalPKCS1PrivateKey(key.PrivateKey)
	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privDER,
	})

	f := DenominationKeyFile{
		KeyID:         key.KeyID,
		BytesPerToken: key.BytesPerToken,
		PrivateKeyPEM: string(privPEM),
	}

	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal key file: %w", err)
	}

	// Ensure parent directory exists.
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("create key directory: %w", err)
		}
	}

	return os.WriteFile(path, data, 0600)
}

// LoadDenominationKey reads a denomination key from a JSON file.
// Returns the full key with both private and public keys populated.
func LoadDenominationKey(path string) (*DenominationKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key file: %w", err)
	}

	var f DenominationKeyFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse key file: %w", err)
	}

	block, _ := pem.Decode([]byte(f.PrivateKeyPEM))
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in key file")
	}

	privKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse RSA private key: %w", err)
	}

	return &DenominationKey{
		KeyID:         f.KeyID,
		BytesPerToken: f.BytesPerToken,
		PrivateKey:    privKey,
		PublicKey:     &privKey.PublicKey,
	}, nil
}

// ExportPublicKey extracts the public key + metadata from a denomination key.
// This is what nodes need — they never see the private key.
func ExportPublicKey(key *DenominationKey) *DenominationKey {
	return &DenominationKey{
		KeyID:         key.KeyID,
		BytesPerToken: key.BytesPerToken,
		PublicKey:     key.PublicKey,
	}
}

// SavePublicKey writes the public portion of a denomination key for nodes.
// Contains DER-encoded public key + metadata, no private key material.
func SavePublicKey(path string, key *DenominationKey) error {
	pubDER, err := x509.MarshalPKIXPublicKey(key.PublicKey)
	if err != nil {
		return fmt.Errorf("marshal public key: %w", err)
	}

	pubPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubDER,
	})

	f := struct {
		KeyID         string `json:"key_id"`
		BytesPerToken int64  `json:"bytes_per_token"`
		PublicKeyPEM  string `json:"public_key_pem"`
	}{
		KeyID:         key.KeyID,
		BytesPerToken: key.BytesPerToken,
		PublicKeyPEM:  string(pubPEM),
	}

	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal public key file: %w", err)
	}

	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("create key directory: %w", err)
		}
	}

	return os.WriteFile(path, data, 0644) // public keys can be world-readable
}

// LoadPublicKey reads a public denomination key file (for nodes).
func LoadPublicKey(path string) (*DenominationKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read public key file: %w", err)
	}

	var f struct {
		KeyID         string `json:"key_id"`
		BytesPerToken int64  `json:"bytes_per_token"`
		PublicKeyPEM  string `json:"public_key_pem"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse public key file: %w", err)
	}

	block, _ := pem.Decode([]byte(f.PublicKeyPEM))
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in public key file")
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}

	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("not an RSA public key")
	}

	return &DenominationKey{
		KeyID:         f.KeyID,
		BytesPerToken: f.BytesPerToken,
		PublicKey:     rsaPub,
	}, nil
}
