package wg

import (
	"encoding/base64"
	"fmt"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// GenerateKeyPair generates a new WireGuard Curve25519 key pair.
func GenerateKeyPair() (*KeyPair, error) {
	privKey, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return nil, fmt.Errorf("generate private key: %w", err)
	}

	pubKey := privKey.PublicKey()

	return &KeyPair{
		PrivateKey: base64.StdEncoding.EncodeToString(privKey[:]),
		PublicKey:  base64.StdEncoding.EncodeToString(pubKey[:]),
	}, nil
}

// ParseKey decodes a base64-encoded WireGuard key into a wgtypes.Key.
func ParseKey(b64 string) (wgtypes.Key, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return wgtypes.Key{}, fmt.Errorf("decode base64 key: %w", err)
	}
	if len(raw) != wgtypes.KeyLen {
		return wgtypes.Key{}, fmt.Errorf("invalid key length: got %d, want %d", len(raw), wgtypes.KeyLen)
	}

	var key wgtypes.Key
	copy(key[:], raw)
	return key, nil
}
