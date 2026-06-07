// Package payments contains the Hub's payment processing logic:
// purchase flow, usage reporting, and settlement engine.
package payments

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/Radi-Labs/ARFL/internal/nostr"
)

// usageReportDomain is the domain separator for usage report signatures.
// Prevents cross-protocol replay: a signed Nostr event can never be
// mistaken for a usage report and vice versa (STRIDE/Spoofing).
const usageReportDomain = "ARFL_USAGE_REPORT_V1"

// UsageReport is a signed bandwidth attestation from a node.
// Nodes submit these to the Hub to prove they served traffic.
// The BIP-340 signature prevents spoofing (STRIDE/Spoofing).
type UsageReport struct {
	SessionID     string `json:"session_id"`
	TicketID      string `json:"ticket_id"`
	NodePubkey    string `json:"node_pubkey"`
	NodeRole      string `json:"node_role"` // "entry" or "exit"
	BytesReported int64  `json:"bytes_reported"`
	ReportedAt    string `json:"reported_at"` // RFC3339
	Signature     string `json:"signature"`   // BIP-340 Schnorr over the payload
}

// Payload returns the canonical byte string that the signature covers.
// Uses length-prefixed fields to prevent delimiter injection attacks.
// Includes domain separator to prevent cross-protocol replay.
func (r *UsageReport) Payload() string {
	fields := []string{
		r.SessionID,
		r.TicketID,
		r.NodePubkey,
		r.NodeRole,
		fmt.Sprintf("%d", r.BytesReported),
		r.ReportedAt,
	}

	// Length-prefix each field: "7:sess-01" — unambiguous even if
	// a field contains the delimiter character.
	var b strings.Builder
	b.WriteString(usageReportDomain)
	for _, f := range fields {
		fmt.Fprintf(&b, "|%d:%s", len(f), f)
	}
	return b.String()
}

// Sign signs the usage report with the node's Nostr keypair.
func (r *UsageReport) Sign(kp *nostr.KeyPair) error {
	r.NodePubkey = kp.PubkeyHex()
	payload := r.Payload()
	sig, err := nostr.SignRaw(kp, []byte(payload))
	if err != nil {
		return fmt.Errorf("sign usage report: %w", err)
	}
	r.Signature = hex.EncodeToString(sig)
	return nil
}

// Verify checks the BIP-340 signature on the usage report.
// Returns nil if valid, or an error describing the failure.
func (r *UsageReport) Verify() error {
	if r.SessionID == "" {
		return fmt.Errorf("empty session_id")
	}
	if r.TicketID == "" {
		return fmt.Errorf("empty ticket_id")
	}
	if r.NodePubkey == "" {
		return fmt.Errorf("empty node_pubkey")
	}
	if r.NodeRole != "entry" && r.NodeRole != "exit" {
		return fmt.Errorf("invalid role: %s", r.NodeRole)
	}
	if r.BytesReported < 0 {
		return fmt.Errorf("negative bytes_reported: %d", r.BytesReported)
	}
	if r.ReportedAt == "" {
		return fmt.Errorf("empty reported_at")
	}
	if r.Signature == "" {
		return fmt.Errorf("empty signature")
	}

	// Parse the reported_at timestamp.
	reportedAt, err := time.Parse(time.RFC3339, r.ReportedAt)
	if err != nil {
		return fmt.Errorf("invalid reported_at: %w", err)
	}

	// Reject reports from the future (clock skew tolerance: 30 seconds).
	if reportedAt.After(time.Now().Add(30 * time.Second)) {
		return fmt.Errorf("report is from the future: %s", r.ReportedAt)
	}

	// Verify BIP-340 signature.
	sigBytes, err := hex.DecodeString(r.Signature)
	if err != nil {
		return fmt.Errorf("invalid signature hex: %w", err)
	}

	payload := r.Payload()
	if err := nostr.VerifyRaw(r.NodePubkey, []byte(payload), sigBytes); err != nil {
		return fmt.Errorf("signature verification failed: %w", err)
	}

	return nil
}
