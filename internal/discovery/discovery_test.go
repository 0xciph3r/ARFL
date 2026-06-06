package discovery

import (
	"testing"
	"time"

	"github.com/Radi-Labs/ARFL/internal/nostr"
	"github.com/Radi-Labs/ARFL/pkg/types"
)

// testFixture creates hub keypair, node keypair, attestation, and valid NodeInfo.
func testFixture(t *testing.T) (*nostr.KeyPair, *nostr.KeyPair, *nostr.Attestation, types.NodeInfo) {
	t.Helper()
	hubKP, _ := nostr.GenerateKeyPair()
	nodeKP, _ := nostr.GenerateKeyPair()

	att, err := nostr.CreateAttestation(hubKP, nodeKP.PubkeyHex(),
		"YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0NTY=", // 44-char base64 WG key
		"operator-1", []string{"entry", "exit"})
	if err != nil {
		t.Fatalf("CreateAttestation: %v", err)
	}

	info := types.NodeInfo{
		NostrPubkey:  nodeKP.PubkeyHex(),
		WGPubkey:     "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0NTY=",
		Endpoint:     "203.0.113.1:51820",
		UploadMbps:   100,
		DownloadMbps: 100,
		Load:         5,
		Capacity:     50,
		Role:         types.RoleEntry,
		Version:      "0.1.0",
	}

	return hubKP, nodeKP, att, info
}

func TestNodeIndex_ProcessValidEvent(t *testing.T) {
	hubKP, nodeKP, att, info := testFixture(t)
	idx := NewNodeIndex([]string{hubKP.PubkeyHex()})

	event, err := BuildAnnouncementEvent(nodeKP, info, att)
	if err != nil {
		t.Fatalf("BuildAnnouncementEvent: %v", err)
	}

	if err := idx.ProcessEvent(event); err != nil {
		t.Fatalf("ProcessEvent should accept valid event: %v", err)
	}

	total, online := idx.NodeCount()
	if total != 1 || online != 1 {
		t.Errorf("expected 1 total, 1 online; got %d, %d", total, online)
	}
}

func TestNodeIndex_RejectsUnsignedEvent(t *testing.T) {
	hubKP, _, att, info := testFixture(t)
	idx := NewNodeIndex([]string{hubKP.PubkeyHex()})

	// Create event but corrupt the signature.
	nodeKP, _ := nostr.GenerateKeyPair()
	event, _ := BuildAnnouncementEvent(nodeKP, info, att)
	event.Sig = "0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"

	if err := idx.ProcessEvent(event); err == nil {
		t.Error("should reject event with invalid signature")
	}
}

func TestNodeIndex_RejectsUntrustedHub(t *testing.T) {
	// STRIDE: Spoofing — event has attestation from unknown hub.
	_, nodeKP, att, info := testFixture(t)
	trustedHubKP, _ := nostr.GenerateKeyPair()

	// Index trusts a DIFFERENT hub than the one that signed the attestation.
	idx := NewNodeIndex([]string{trustedHubKP.PubkeyHex()})

	event, _ := BuildAnnouncementEvent(nodeKP, info, att)
	if err := idx.ProcessEvent(event); err == nil {
		t.Error("should reject event attested by untrusted hub")
	}
}

func TestNodeIndex_RejectsExpiredAttestation(t *testing.T) {
	hubKP, nodeKP, att, info := testFixture(t)
	idx := NewNodeIndex([]string{hubKP.PubkeyHex()})

	// Expire the attestation.
	att.ExpiresAt = time.Now().Add(-1 * time.Hour).Unix()

	// Note: the attestation ID will mismatch because we changed ExpiresAt.
	// This is intentional — expired attestations can't be used even if re-signed.
	event, _ := BuildAnnouncementEvent(nodeKP, info, att)
	if err := idx.ProcessEvent(event); err == nil {
		t.Error("should reject expired attestation")
	}
}

func TestNodeIndex_RejectsWrongEventKind(t *testing.T) {
	hubKP, _, _, _ := testFixture(t)
	idx := NewNodeIndex([]string{hubKP.PubkeyHex()})

	kp, _ := nostr.GenerateKeyPair()
	event := &nostr.Event{
		CreatedAt: time.Now().Unix(),
		Kind:      1, // Wrong kind — this is a text note, not a node announcement.
		Tags:      nostr.Tags{},
		Content:   "hello world",
	}
	event.Sign(kp)

	if err := idx.ProcessEvent(event); err == nil {
		t.Error("should reject wrong event kind")
	}
}

func TestNodeIndex_RejectsMissingAttestation(t *testing.T) {
	hubKP, _, _, _ := testFixture(t)
	idx := NewNodeIndex([]string{hubKP.PubkeyHex()})

	kp, _ := nostr.GenerateKeyPair()
	event := &nostr.Event{
		CreatedAt: time.Now().Unix(),
		Kind:      30078,
		Tags:      nostr.Tags{{"d", "no-attestation"}}, // No attestation tag.
		Content:   `{}`,
	}
	event.Sign(kp)

	if err := idx.ProcessEvent(event); err == nil {
		t.Error("should reject event without attestation")
	}
}

func TestNodeIndex_RejectsReplayAttack(t *testing.T) {
	// STRIDE: Spoofing — node B uses node A's attestation.
	hubKP, _, att, info := testFixture(t)
	idx := NewNodeIndex([]string{hubKP.PubkeyHex()})

	// Node B tries to use node A's attestation.
	nodeB, _ := nostr.GenerateKeyPair()
	event, _ := BuildAnnouncementEvent(nodeB, info, att)

	// The event is signed by nodeB, but the attestation is for the original node.
	// VerifyNodeBinding should catch this.
	if err := idx.ProcessEvent(event); err == nil {
		t.Error("should reject replay attack (different node using another's attestation)")
	}
}

func TestNodeIndex_RejectsBadEndpoint(t *testing.T) {
	hubKP, nodeKP, att, info := testFixture(t)
	idx := NewNodeIndex([]string{hubKP.PubkeyHex()})

	// Bad endpoint — no port.
	info.Endpoint = "203.0.113.1"
	event, _ := BuildAnnouncementEvent(nodeKP, info, att)
	if err := idx.ProcessEvent(event); err == nil {
		t.Error("should reject invalid endpoint")
	}
}

func TestNodeIndex_RejectsLoopbackEndpoint(t *testing.T) {
	hubKP, nodeKP, att, info := testFixture(t)
	idx := NewNodeIndex([]string{hubKP.PubkeyHex()})

	info.Endpoint = "127.0.0.1:51820"
	event, _ := BuildAnnouncementEvent(nodeKP, info, att)
	if err := idx.ProcessEvent(event); err == nil {
		t.Error("should reject loopback endpoint")
	}
}

func TestNodeIndex_RejectsBelowMinBandwidth(t *testing.T) {
	hubKP, nodeKP, att, info := testFixture(t)
	idx := NewNodeIndex([]string{hubKP.PubkeyHex()})

	info.UploadMbps = 10 // Below MinBandwidthMbps (50).
	event, _ := BuildAnnouncementEvent(nodeKP, info, att)
	if err := idx.ProcessEvent(event); err == nil {
		t.Error("should reject node below minimum bandwidth")
	}
}

func TestNodeIndex_RejectsInvalidRole(t *testing.T) {
	hubKP, nodeKP, att, info := testFixture(t)
	idx := NewNodeIndex([]string{hubKP.PubkeyHex()})

	info.Role = "admin" // Not a valid role.
	event, _ := BuildAnnouncementEvent(nodeKP, info, att)
	if err := idx.ProcessEvent(event); err == nil {
		t.Error("should reject invalid role")
	}
}

func TestNodeIndex_RejectsNegativeCapacity(t *testing.T) {
	hubKP, nodeKP, att, info := testFixture(t)
	idx := NewNodeIndex([]string{hubKP.PubkeyHex()})

	info.Capacity = -1
	event, _ := BuildAnnouncementEvent(nodeKP, info, att)
	if err := idx.ProcessEvent(event); err == nil {
		t.Error("should reject negative capacity")
	}
}

func TestNodeIndex_RejectsLoadExceedingCapacity(t *testing.T) {
	hubKP, nodeKP, att, info := testFixture(t)
	idx := NewNodeIndex([]string{hubKP.PubkeyHex()})

	info.Load = 100
	info.Capacity = 50
	event, _ := BuildAnnouncementEvent(nodeKP, info, att)
	if err := idx.ProcessEvent(event); err == nil {
		t.Error("should reject load exceeding capacity")
	}
}

func TestNodeIndex_UpdatesExistingNode(t *testing.T) {
	hubKP, nodeKP, att, info := testFixture(t)
	idx := NewNodeIndex([]string{hubKP.PubkeyHex()})

	// First announcement.
	event1, _ := BuildAnnouncementEvent(nodeKP, info, att)
	idx.ProcessEvent(event1)

	// Second announcement with updated load.
	info.Load = 25
	event2, _ := BuildAnnouncementEvent(nodeKP, info, att)
	idx.ProcessEvent(event2)

	// Should still be 1 node, not 2.
	total, _ := idx.NodeCount()
	if total != 1 {
		t.Errorf("expected 1 node after update, got %d", total)
	}

	// Load should be updated.
	node, ok := idx.GetNode(nodeKP.PubkeyHex())
	if !ok {
		t.Fatal("node not found")
	}
	if node.Info.Load != 25 {
		t.Errorf("load not updated: got %d, want 25", node.Info.Load)
	}
}

func TestNodeIndex_PruneOffline(t *testing.T) {
	hubKP, nodeKP, att, info := testFixture(t)
	idx := NewNodeIndex([]string{hubKP.PubkeyHex()})

	event, _ := BuildAnnouncementEvent(nodeKP, info, att)
	idx.ProcessEvent(event)

	// Manually backdate the last seen time.
	idx.mu.Lock()
	idx.nodes[nodeKP.PubkeyHex()].LastSeen = time.Now().Add(-10 * time.Minute)
	idx.mu.Unlock()

	pruned := idx.PruneOffline()
	if pruned != 1 {
		t.Errorf("expected 1 pruned, got %d", pruned)
	}

	_, online := idx.NodeCount()
	if online != 0 {
		t.Errorf("expected 0 online after prune, got %d", online)
	}
}

func TestNodeIndex_ListByRole(t *testing.T) {
	hubKP, _, _, _ := testFixture(t)
	idx := NewNodeIndex([]string{hubKP.PubkeyHex()})

	// Add an entry node.
	nodeA, _ := nostr.GenerateKeyPair()
	wgKeyA := "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0NTY="
	attA, _ := nostr.CreateAttestation(hubKP, nodeA.PubkeyHex(), wgKeyA, "op-1", []string{"entry"})
	infoA := types.NodeInfo{
		WGPubkey: wgKeyA, Endpoint: "203.0.113.1:51820",
		UploadMbps: 100, DownloadMbps: 100, Load: 5, Capacity: 50, Role: types.RoleEntry,
	}
	evA, _ := BuildAnnouncementEvent(nodeA, infoA, attA)
	idx.ProcessEvent(evA)

	// Add an exit node.
	nodeB, _ := nostr.GenerateKeyPair()
	wgKeyB := "eHl6YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4MTIzNA=="
	attB, _ := nostr.CreateAttestation(hubKP, nodeB.PubkeyHex(), wgKeyB, "op-2", []string{"exit"})
	infoB := types.NodeInfo{
		WGPubkey: wgKeyB, Endpoint: "203.0.113.2:51820",
		UploadMbps: 100, DownloadMbps: 100, Load: 10, Capacity: 50, Role: types.RoleExit,
	}
	evB, _ := BuildAnnouncementEvent(nodeB, infoB, attB)
	idx.ProcessEvent(evB)

	entries := idx.ListByRole(types.RoleEntry)
	exits := idx.ListByRole(types.RoleExit)

	if len(entries) != 1 {
		t.Errorf("expected 1 entry node, got %d", len(entries))
	}
	if len(exits) != 1 {
		t.Errorf("expected 1 exit node, got %d", len(exits))
	}
}

func TestNodeIndex_RejectsStaleAnnouncement(t *testing.T) {
	hubKP, nodeKP, att, info := testFixture(t)
	idx := NewNodeIndex([]string{hubKP.PubkeyHex()})

	// Create event with old timestamp.
	event, _ := BuildAnnouncementEvent(nodeKP, info, att)
	event.CreatedAt = time.Now().Add(-10 * time.Minute).Unix()
	// Re-sign because we changed CreatedAt.
	event.Sign(nodeKP)

	if err := idx.ProcessEvent(event); err == nil {
		t.Error("should reject stale announcement older than OnlineTTL")
	}
}
