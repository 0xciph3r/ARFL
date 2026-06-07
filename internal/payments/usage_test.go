package payments

import (
	"strings"
	"testing"
	"time"

	"github.com/Radi-Labs/ARFL/internal/nostr"
)

func makeSignedReport(t *testing.T) (*UsageReport, *nostr.KeyPair) {
	t.Helper()
	kp, err := nostr.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	r := &UsageReport{
		SessionID:     "session-abc",
		TicketID:      "ticket-001",
		NodeRole:      "entry",
		BytesReported: 50_000_000,
		ReportedAt:    time.Now().UTC().Format(time.RFC3339),
	}

	if err := r.Sign(kp); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	return r, kp
}

// --- Happy path ---

func TestUsageReport_SignAndVerify(t *testing.T) {
	r, _ := makeSignedReport(t)
	if err := r.Verify(); err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
}

func TestUsageReport_EntryRole(t *testing.T) {
	r, _ := makeSignedReport(t)
	r.NodeRole = "entry" // already set, but explicit
	if err := r.Verify(); err != nil {
		t.Fatalf("entry role should be valid: %v", err)
	}
}

func TestUsageReport_ExitRole(t *testing.T) {
	kp, _ := nostr.GenerateKeyPair()
	r := &UsageReport{
		SessionID:     "session-abc",
		TicketID:      "ticket-001",
		NodeRole:      "exit",
		BytesReported: 1_000_000,
		ReportedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	r.Sign(kp)
	if err := r.Verify(); err != nil {
		t.Fatalf("exit role should be valid: %v", err)
	}
}

func TestUsageReport_ZeroBytes(t *testing.T) {
	// Zero bytes is valid — a session can be established with no traffic.
	kp, _ := nostr.GenerateKeyPair()
	r := &UsageReport{
		SessionID:     "session-abc",
		TicketID:      "ticket-001",
		NodeRole:      "entry",
		BytesReported: 0,
		ReportedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	r.Sign(kp)
	if err := r.Verify(); err != nil {
		t.Fatalf("zero bytes should be valid: %v", err)
	}
}

// --- Structural validation ---

func TestUsageReport_EmptySessionID(t *testing.T) {
	r, _ := makeSignedReport(t)
	r.SessionID = ""
	if err := r.Verify(); err == nil {
		t.Fatal("should reject empty session_id")
	}
}

func TestUsageReport_EmptyTicketID(t *testing.T) {
	r, _ := makeSignedReport(t)
	r.TicketID = ""
	if err := r.Verify(); err == nil {
		t.Fatal("should reject empty ticket_id")
	}
}

func TestUsageReport_EmptyNodePubkey(t *testing.T) {
	r, _ := makeSignedReport(t)
	r.NodePubkey = ""
	if err := r.Verify(); err == nil {
		t.Fatal("should reject empty node_pubkey")
	}
}

func TestUsageReport_InvalidRole(t *testing.T) {
	r, _ := makeSignedReport(t)
	r.NodeRole = "relay" // not entry or exit
	if err := r.Verify(); err == nil {
		t.Fatal("should reject invalid role")
	}
}

func TestUsageReport_NegativeBytes(t *testing.T) {
	r, _ := makeSignedReport(t)
	r.BytesReported = -1
	if err := r.Verify(); err == nil {
		t.Fatal("should reject negative bytes")
	}
}

func TestUsageReport_EmptyReportedAt(t *testing.T) {
	r, _ := makeSignedReport(t)
	r.ReportedAt = ""
	if err := r.Verify(); err == nil {
		t.Fatal("should reject empty reported_at")
	}
}

func TestUsageReport_InvalidReportedAtFormat(t *testing.T) {
	r, _ := makeSignedReport(t)
	r.ReportedAt = "2024-01-01" // not RFC3339
	if err := r.Verify(); err == nil {
		t.Fatal("should reject non-RFC3339 timestamp")
	}
}

func TestUsageReport_EmptySignature(t *testing.T) {
	r, _ := makeSignedReport(t)
	r.Signature = ""
	if err := r.Verify(); err == nil {
		t.Fatal("should reject empty signature")
	}
}

func TestUsageReport_InvalidSignatureHex(t *testing.T) {
	r, _ := makeSignedReport(t)
	r.Signature = "zzzz-not-hex"
	if err := r.Verify(); err == nil {
		t.Fatal("should reject non-hex signature")
	}
}

// --- STRIDE: Spoofing ---

func TestUsageReport_TamperedBytes(t *testing.T) {
	// Attacker changes bytes_reported after signing.
	r, _ := makeSignedReport(t)
	r.BytesReported = 999_999_999
	if err := r.Verify(); err == nil {
		t.Fatal("should reject tampered bytes_reported")
	}
}

func TestUsageReport_TamperedSessionID(t *testing.T) {
	r, _ := makeSignedReport(t)
	r.SessionID = "session-EVIL"
	if err := r.Verify(); err == nil {
		t.Fatal("should reject tampered session_id")
	}
}

func TestUsageReport_TamperedTicketID(t *testing.T) {
	r, _ := makeSignedReport(t)
	r.TicketID = "ticket-FAKE"
	if err := r.Verify(); err == nil {
		t.Fatal("should reject tampered ticket_id")
	}
}

func TestUsageReport_TamperedRole(t *testing.T) {
	r, _ := makeSignedReport(t)
	r.NodeRole = "exit" // was "entry"
	if err := r.Verify(); err == nil {
		t.Fatal("should reject tampered role")
	}
}

func TestUsageReport_TamperedTimestamp(t *testing.T) {
	r, _ := makeSignedReport(t)
	r.ReportedAt = time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	if err := r.Verify(); err == nil {
		t.Fatal("should reject tampered timestamp")
	}
}

func TestUsageReport_WrongSigner(t *testing.T) {
	// Report signed by one node, pubkey swapped to another.
	r, _ := makeSignedReport(t)

	rogue, _ := nostr.GenerateKeyPair()
	r.NodePubkey = rogue.PubkeyHex()

	if err := r.Verify(); err == nil {
		t.Fatal("should reject mismatched signer/pubkey")
	}
}

// --- STRIDE: Replay / time attacks ---

func TestUsageReport_FutureTimestamp(t *testing.T) {
	kp, _ := nostr.GenerateKeyPair()
	r := &UsageReport{
		SessionID:     "session-abc",
		TicketID:      "ticket-001",
		NodeRole:      "entry",
		BytesReported: 1000,
		ReportedAt:    time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339),
	}
	r.Sign(kp)

	if err := r.Verify(); err == nil {
		t.Fatal("should reject report with future timestamp")
	}
}

func TestUsageReport_FutureWithinSkewAllowed(t *testing.T) {
	// Within the 30-second clock skew tolerance.
	kp, _ := nostr.GenerateKeyPair()
	r := &UsageReport{
		SessionID:     "session-abc",
		TicketID:      "ticket-001",
		NodeRole:      "entry",
		BytesReported: 1000,
		ReportedAt:    time.Now().Add(10 * time.Second).UTC().Format(time.RFC3339),
	}
	r.Sign(kp)

	if err := r.Verify(); err != nil {
		t.Fatalf("should allow within skew tolerance: %v", err)
	}
}

// --- Payload determinism ---

func TestUsageReport_PayloadDeterministic(t *testing.T) {
	r := &UsageReport{
		SessionID:     "s1",
		TicketID:      "t1",
		NodePubkey:    "abc",
		NodeRole:      "entry",
		BytesReported: 100,
		ReportedAt:    "2024-01-01T00:00:00Z",
	}

	p1 := r.Payload()
	p2 := r.Payload()
	if p1 != p2 {
		t.Fatal("payload must be deterministic")
	}
	// Should contain domain prefix.
	if !strings.Contains(p1, "ARFL_USAGE_REPORT_V1") {
		t.Errorf("payload missing domain separator: %s", p1)
	}
	// Should contain length-prefixed fields.
	if !strings.Contains(p1, "2:s1") {
		t.Errorf("payload missing length-prefixed session_id: %s", p1)
	}
}

// --- Adversarial: delimiter injection ---

func TestUsageReport_DelimiterInSessionID(t *testing.T) {
	// Field containing "|" must not create ambiguous payload.
	kp, _ := nostr.GenerateKeyPair()
	r1 := &UsageReport{
		SessionID:     "a|b",
		TicketID:      "t1",
		NodeRole:      "entry",
		BytesReported: 100,
		ReportedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	r1.Sign(kp)

	r2 := &UsageReport{
		SessionID:     "a",
		TicketID:      "b|3:t1", // attempt to fake the same payload
		NodeRole:      "entry",
		BytesReported: 100,
		ReportedAt:    r1.ReportedAt,
	}
	r2.Sign(kp)

	// Length-prefixed payloads MUST differ.
	if r1.Payload() == r2.Payload() {
		t.Fatal("delimiter injection produced identical payloads — format is ambiguous")
	}
}

// --- Adversarial: huge bytes ---

func TestUsageReport_HugeBytes(t *testing.T) {
	kp, _ := nostr.GenerateKeyPair()
	r := &UsageReport{
		SessionID:     "session-abc",
		TicketID:      "ticket-001",
		NodeRole:      "entry",
		BytesReported: 1<<62 - 1, // near MaxInt64
		ReportedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	r.Sign(kp)
	// Should still verify — huge but valid.
	if err := r.Verify(); err != nil {
		t.Fatalf("huge bytes should verify: %v", err)
	}
}

// --- Adversarial: invalid signature length ---

func TestUsageReport_ShortSignature(t *testing.T) {
	r, _ := makeSignedReport(t)
	r.Signature = "deadbeef" // too short for BIP-340
	if err := r.Verify(); err == nil {
		t.Fatal("should reject short signature")
	}
}

func TestUsageReport_OversizeSignature(t *testing.T) {
	r, _ := makeSignedReport(t)
	r.Signature = strings.Repeat("ab", 128) // 256 hex chars, 128 bytes
	if err := r.Verify(); err == nil {
		t.Fatal("should reject oversized signature")
	}
}

// --- Adversarial: invalid pubkey ---

func TestUsageReport_InvalidPubkeyHex(t *testing.T) {
	r, _ := makeSignedReport(t)
	r.NodePubkey = "zzzz-not-hex"
	if err := r.Verify(); err == nil {
		t.Fatal("should reject non-hex pubkey")
	}
}

func TestUsageReport_ShortPubkey(t *testing.T) {
	r, _ := makeSignedReport(t)
	r.NodePubkey = "deadbeef" // only 4 bytes, not 32
	if err := r.Verify(); err == nil {
		t.Fatal("should reject short pubkey")
	}
}
