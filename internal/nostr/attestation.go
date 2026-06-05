package nostr

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

// AttestationTTL is how long a hub attestation remains valid.
// 6 hours: aggressive but safe. If a node is compromised, max 6h of damage.
// If the hub goes down >6h, all nodes disappear — that's a feature, not a bug.
const AttestationTTL = 6 * time.Hour

// Attestation is a structured, signed voucher from the hub proving it has
// verified a node. Unlike a bare signature over a pubkey, this binds to
// SPECIFIC metadata: which WireGuard key, which roles, which operator.
// Change any of those? You need a new attestation.
//
// Think of it like an SSL certificate — it's not "this server exists,"
// it's "this server at this domain with this public key is verified until this date."
type Attestation struct {
	Protocol      string   `json:"protocol"`       // "arfl-node-attestation-v1"
	HubPubkey     string   `json:"hub_pubkey"`     // Hub's Nostr pubkey (who signed this)
	NodePubkey    string   `json:"node_pubkey"`    // Node's Nostr pubkey (who this is for)
	NodeWGPubkey  string   `json:"node_wg_pubkey"` // Node's WireGuard pubkey (bound to this key)
	OperatorID    string   `json:"operator_id"`    // Hub-assigned operator identity
	AllowedRoles  []string `json:"allowed_roles"`  // ["entry"], ["exit"], or ["entry","exit"]
	IssuedAt      int64    `json:"issued_at"`      // Unix timestamp
	ExpiresAt     int64    `json:"expires_at"`     // Unix timestamp (issued_at + 6h)
	AttestationID string   `json:"attestation_id"` // SHA-256 of the attestation content
	Signature     string   `json:"signature"`      // Hub's BIP-340 Schnorr sig over attestation_id
}

// CreateAttestation builds and signs a new attestation for a node.
// Only the hub calls this — it proves the hub has vetted this node.
func CreateAttestation(hubKP *KeyPair, nodePubkey, nodeWGPubkey, operatorID string, allowedRoles []string) (*Attestation, error) {
	now := time.Now()
	att := &Attestation{
		Protocol:     "arfl-node-attestation-v1",
		HubPubkey:    hubKP.PubkeyHex(),
		NodePubkey:   nodePubkey,
		NodeWGPubkey: nodeWGPubkey,
		OperatorID:   operatorID,
		AllowedRoles: allowedRoles,
		IssuedAt:     now.Unix(),
		ExpiresAt:    now.Add(AttestationTTL).Unix(),
	}

	// Compute attestation ID (hash of all fields except ID and signature).
	id, err := att.computeID()
	if err != nil {
		return nil, fmt.Errorf("compute attestation ID: %w", err)
	}
	att.AttestationID = id

	// Sign the attestation ID with the hub's key.
	idBytes, err := hex.DecodeString(id)
	if err != nil {
		return nil, fmt.Errorf("decode attestation ID: %w", err)
	}
	sig, err := schnorr.Sign(hubKP.PrivateKey, idBytes)
	if err != nil {
		return nil, fmt.Errorf("sign attestation: %w", err)
	}
	att.Signature = hex.EncodeToString(sig.Serialize())

	return att, nil
}

// computeID produces a deterministic hash of the attestation content.
// This is what gets signed — change any field and the signature breaks.
func (a *Attestation) computeID() (string, error) {
	// Serialize the signable fields in a canonical order.
	content := []interface{}{
		a.Protocol,
		a.HubPubkey,
		a.NodePubkey,
		a.NodeWGPubkey,
		a.OperatorID,
		a.AllowedRoles,
		a.IssuedAt,
		a.ExpiresAt,
	}
	data, err := json.Marshal(content)
	if err != nil {
		return "", fmt.Errorf("marshal attestation content: %w", err)
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

// Verify checks that an attestation is valid:
//  1. Protocol version is correct
//  2. Not expired
//  3. Attestation ID matches the content hash (tamper-proof)
//  4. Signature is valid and from the claimed hub pubkey
//
// The caller must ALSO verify that the node's announcement matches
// the attestation (same Nostr pubkey, same WG pubkey, role is allowed).
func (a *Attestation) Verify(trustedHubPubkeys []string) error {
	// Step 1: Check protocol version.
	if a.Protocol != "arfl-node-attestation-v1" {
		return fmt.Errorf("unknown attestation protocol: %s", a.Protocol)
	}

	// Step 2: Check expiry.
	if time.Now().Unix() > a.ExpiresAt {
		return fmt.Errorf("attestation expired at %d", a.ExpiresAt)
	}

	// Step 3: Check the hub pubkey is trusted.
	trusted := false
	for _, pk := range trustedHubPubkeys {
		if pk == a.HubPubkey {
			trusted = true
			break
		}
	}
	if !trusted {
		return fmt.Errorf("hub pubkey %s is not in trusted set", a.HubPubkey)
	}

	// Step 4: Recompute the attestation ID and check it matches.
	computedID, err := a.computeID()
	if err != nil {
		return fmt.Errorf("recompute attestation ID: %w", err)
	}
	if computedID != a.AttestationID {
		return fmt.Errorf("attestation ID mismatch: computed %s, got %s", computedID, a.AttestationID)
	}

	// Step 5: Verify the Schnorr signature over the attestation ID.
	pubBytes, err := hex.DecodeString(a.HubPubkey)
	if err != nil {
		return fmt.Errorf("decode hub pubkey: %w", err)
	}
	pubKey, err := schnorr.ParsePubKey(pubBytes)
	if err != nil {
		return fmt.Errorf("parse hub pubkey: %w", err)
	}

	sigBytes, err := hex.DecodeString(a.Signature)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	sig, err := schnorr.ParseSignature(sigBytes)
	if err != nil {
		return fmt.Errorf("parse signature: %w", err)
	}

	idBytes, err := hex.DecodeString(a.AttestationID)
	if err != nil {
		return fmt.Errorf("decode attestation ID: %w", err)
	}

	if !sig.Verify(idBytes, pubKey) {
		return fmt.Errorf("invalid attestation signature")
	}

	return nil
}

// VerifyNodeBinding checks that a node announcement matches this attestation.
// This prevents a node from using another node's attestation (replay attack).
func (a *Attestation) VerifyNodeBinding(eventPubkey, wgPubkey, role string) error {
	// The Nostr event pubkey must match the attestation's node pubkey.
	if eventPubkey != a.NodePubkey {
		return fmt.Errorf("node pubkey mismatch: event has %s, attestation has %s", eventPubkey, a.NodePubkey)
	}

	// The WireGuard pubkey must match.
	if wgPubkey != a.NodeWGPubkey {
		return fmt.Errorf("WG pubkey mismatch: announcement has %s, attestation has %s", wgPubkey, a.NodeWGPubkey)
	}

	// The announced role must be within allowed roles.
	for _, allowed := range a.AllowedRoles {
		if allowed == role || allowed == "both" {
			return nil
		}
	}
	return fmt.Errorf("role %q not in allowed roles %v", role, a.AllowedRoles)
}

// Encode serializes the attestation to JSON for embedding in Nostr event tags.
func (a *Attestation) Encode() (string, error) {
	data, err := json.Marshal(a)
	if err != nil {
		return "", fmt.Errorf("encode attestation: %w", err)
	}
	return string(data), nil
}

// DecodeAttestation parses an attestation from JSON.
func DecodeAttestation(data string) (*Attestation, error) {
	var att Attestation
	if err := json.Unmarshal([]byte(data), &att); err != nil {
		return nil, fmt.Errorf("decode attestation: %w", err)
	}
	return &att, nil
}
