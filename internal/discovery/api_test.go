package discovery

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Radi-Labs/ARFL/internal/nostr"
	"github.com/Radi-Labs/ARFL/pkg/types"
)

// --- Discovery API Tests ---

func TestDiscoveryAPI_ListNodes(t *testing.T) {
	hubKP, nodeKP, att, info := testFixture(t)
	idx := NewNodeIndex([]string{hubKP.PubkeyHex()})

	event, _ := BuildAnnouncementEvent(nodeKP, info, att)
	idx.ProcessEvent(event)

	api := NewDiscoveryAPI(idx)
	server := httptest.NewServer(api.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/nodes")
	if err != nil {
		t.Fatalf("GET /nodes: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var discovery DiscoveryResponse
	json.NewDecoder(resp.Body).Decode(&discovery)

	if discovery.Count != 1 {
		t.Errorf("expected 1 node, got %d", discovery.Count)
	}
	if discovery.Nodes[0].Event == nil {
		t.Error("response should include raw signed events")
	}
	if discovery.Nodes[0].Attestation == nil {
		t.Error("response should include attestations")
	}
}

func TestDiscoveryAPI_FilterByRole(t *testing.T) {
	hubKP, _, _, _ := testFixture(t)
	idx := NewNodeIndex([]string{hubKP.PubkeyHex()})

	// Add entry node.
	nodeA, _ := nostr.GenerateKeyPair()
	wgA := "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0NTY="
	attA, _ := nostr.CreateAttestation(hubKP, nodeA.PubkeyHex(), wgA, "op-1", []string{"entry"})
	infoA := types.NodeInfo{
		WGPubkey: wgA, Endpoint: "203.0.113.1:51820",
		UploadMbps: 100, DownloadMbps: 100, Load: 5, Capacity: 50, Role: types.RoleEntry,
	}
	evA, _ := BuildAnnouncementEvent(nodeA, infoA, attA)
	idx.ProcessEvent(evA)

	// Add exit node.
	nodeB, _ := nostr.GenerateKeyPair()
	wgB := "eHl6YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4MTIzNA=="
	attB, _ := nostr.CreateAttestation(hubKP, nodeB.PubkeyHex(), wgB, "op-2", []string{"exit"})
	infoB := types.NodeInfo{
		WGPubkey: wgB, Endpoint: "203.0.113.2:51820",
		UploadMbps: 100, DownloadMbps: 100, Load: 10, Capacity: 50, Role: types.RoleExit,
	}
	evB, _ := BuildAnnouncementEvent(nodeB, infoB, attB)
	idx.ProcessEvent(evB)

	api := NewDiscoveryAPI(idx)
	server := httptest.NewServer(api.Handler())
	defer server.Close()

	// Filter for entry only.
	resp, _ := http.Get(server.URL + "/nodes?role=entry")
	defer resp.Body.Close()
	var result DiscoveryResponse
	json.NewDecoder(resp.Body).Decode(&result)
	if result.Count != 1 {
		t.Errorf("expected 1 entry node, got %d", result.Count)
	}
}

func TestDiscoveryAPI_RateLimit(t *testing.T) {
	hubKP, _, _, _ := testFixture(t)
	idx := NewNodeIndex([]string{hubKP.PubkeyHex()})
	api := NewDiscoveryAPI(idx)
	api.maxRequests = 3 // Low limit for testing.

	server := httptest.NewServer(api.Handler())
	defer server.Close()

	// First 3 should succeed.
	for i := 0; i < 3; i++ {
		resp, _ := http.Get(server.URL + "/nodes")
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d should succeed", i+1)
		}
	}

	// 4th should be rate limited.
	resp, _ := http.Get(server.URL + "/nodes")
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", resp.StatusCode)
	}
}

func TestDiscoveryAPI_Health(t *testing.T) {
	hubKP, nodeKP, att, info := testFixture(t)
	idx := NewNodeIndex([]string{hubKP.PubkeyHex()})
	event, _ := BuildAnnouncementEvent(nodeKP, info, att)
	idx.ProcessEvent(event)

	api := NewDiscoveryAPI(idx)
	server := httptest.NewServer(api.Handler())
	defer server.Close()

	resp, _ := http.Get(server.URL + "/health")
	defer resp.Body.Close()

	var health map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&health)

	if health["status"] != "ok" {
		t.Errorf("expected ok, got %v", health["status"])
	}
}

func TestDiscoveryAPI_MethodNotAllowed(t *testing.T) {
	hubKP, _, _, _ := testFixture(t)
	idx := NewNodeIndex([]string{hubKP.PubkeyHex()})
	api := NewDiscoveryAPI(idx)
	server := httptest.NewServer(api.Handler())
	defer server.Close()

	resp, _ := http.Post(server.URL+"/nodes", "application/json", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", resp.StatusCode)
	}
}

// --- Node Selector Tests ---

func TestSelector_SelectPairWithDiversity(t *testing.T) {
	hubKP, _, _, _ := testFixture(t)
	idx := NewNodeIndex([]string{hubKP.PubkeyHex()})

	// Add entry node (operator-1).
	nodeA, _ := nostr.GenerateKeyPair()
	wgA := "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0NTY="
	attA, _ := nostr.CreateAttestation(hubKP, nodeA.PubkeyHex(), wgA, "operator-1", []string{"entry"})
	infoA := types.NodeInfo{
		WGPubkey: wgA, Endpoint: "203.0.113.1:51820",
		UploadMbps: 100, DownloadMbps: 100, Load: 5, Capacity: 50, Role: types.RoleEntry,
	}
	evA, _ := BuildAnnouncementEvent(nodeA, infoA, attA)
	idx.ProcessEvent(evA)

	// Add exit node (operator-2 — DIFFERENT operator).
	nodeB, _ := nostr.GenerateKeyPair()
	wgB := "eHl6YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4MTIzNA=="
	attB, _ := nostr.CreateAttestation(hubKP, nodeB.PubkeyHex(), wgB, "operator-2", []string{"exit"})
	infoB := types.NodeInfo{
		WGPubkey: wgB, Endpoint: "203.0.113.2:51820",
		UploadMbps: 100, DownloadMbps: 100, Load: 10, Capacity: 50, Role: types.RoleExit,
	}
	evB, _ := BuildAnnouncementEvent(nodeB, infoB, attB)
	idx.ProcessEvent(evB)

	// Start a test HTTP server serving the discovery API.
	api := NewDiscoveryAPI(idx)
	server := httptest.NewServer(api.Handler())
	defer server.Close()

	// Create selector pointing to test server.
	selector := NewNodeSelector(server.URL, []string{hubKP.PubkeyHex()})
	pair, err := selector.SelectPair()
	if err != nil {
		t.Fatalf("SelectPair: %v", err)
	}

	if pair.Entry.Attestation.OperatorID == pair.Exit.Attestation.OperatorID {
		t.Error("entry and exit should have different operators")
	}
}

func TestSelector_RejectsSameOperator(t *testing.T) {
	hubKP, _, _, _ := testFixture(t)
	idx := NewNodeIndex([]string{hubKP.PubkeyHex()})

	// Both nodes from SAME operator — should fail selection.
	nodeA, _ := nostr.GenerateKeyPair()
	wgA := "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0NTY="
	attA, _ := nostr.CreateAttestation(hubKP, nodeA.PubkeyHex(), wgA, "same-operator", []string{"entry"})
	infoA := types.NodeInfo{
		WGPubkey: wgA, Endpoint: "203.0.113.1:51820",
		UploadMbps: 100, DownloadMbps: 100, Load: 5, Capacity: 50, Role: types.RoleEntry,
	}
	evA, _ := BuildAnnouncementEvent(nodeA, infoA, attA)
	idx.ProcessEvent(evA)

	nodeB, _ := nostr.GenerateKeyPair()
	wgB := "eHl6YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4MTIzNA=="
	attB, _ := nostr.CreateAttestation(hubKP, nodeB.PubkeyHex(), wgB, "same-operator", []string{"exit"})
	infoB := types.NodeInfo{
		WGPubkey: wgB, Endpoint: "203.0.113.2:51820",
		UploadMbps: 100, DownloadMbps: 100, Load: 10, Capacity: 50, Role: types.RoleExit,
	}
	evB, _ := BuildAnnouncementEvent(nodeB, infoB, attB)
	idx.ProcessEvent(evB)

	api := NewDiscoveryAPI(idx)
	server := httptest.NewServer(api.Handler())
	defer server.Close()

	selector := NewNodeSelector(server.URL, []string{hubKP.PubkeyHex()})
	_, err := selector.SelectPair()
	if err == nil {
		t.Error("should fail when all nodes have the same operator")
	}
}

func TestSelector_ClientVerifiesSignatures(t *testing.T) {
	// STRIDE: Hub manipulation — what if the hub serves nodes
	// attested by a hub the client doesn't trust?
	hubKP, _, _, _ := testFixture(t)
	rogueHubKP, _ := nostr.GenerateKeyPair()
	idx := NewNodeIndex([]string{rogueHubKP.PubkeyHex()}) // Index trusts rogue hub.

	// Add nodes attested by the rogue hub.
	nodeA, _ := nostr.GenerateKeyPair()
	wgA := "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0NTY="
	attA, _ := nostr.CreateAttestation(rogueHubKP, nodeA.PubkeyHex(), wgA, "rogue-op-1", []string{"entry"})
	infoA := types.NodeInfo{
		WGPubkey: wgA, Endpoint: "203.0.113.1:51820",
		UploadMbps: 100, DownloadMbps: 100, Load: 5, Capacity: 50, Role: types.RoleEntry,
	}
	evA, _ := BuildAnnouncementEvent(nodeA, infoA, attA)
	idx.ProcessEvent(evA)

	nodeB, _ := nostr.GenerateKeyPair()
	wgB := "eHl6YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4MTIzNA=="
	attB, _ := nostr.CreateAttestation(rogueHubKP, nodeB.PubkeyHex(), wgB, "rogue-op-2", []string{"exit"})
	infoB := types.NodeInfo{
		WGPubkey: wgB, Endpoint: "203.0.113.2:51820",
		UploadMbps: 100, DownloadMbps: 100, Load: 10, Capacity: 50, Role: types.RoleExit,
	}
	evB, _ := BuildAnnouncementEvent(nodeB, infoB, attB)
	idx.ProcessEvent(evB)

	api := NewDiscoveryAPI(idx)
	server := httptest.NewServer(api.Handler())
	defer server.Close()

	// Client only trusts the REAL hub, not the rogue hub.
	selector := NewNodeSelector(server.URL, []string{hubKP.PubkeyHex()})
	_, err := selector.SelectPair()
	if err == nil {
		t.Error("client should reject nodes attested by untrusted hub")
	}
}

func TestSelector_WeightedSelection_PrefersCapacity(t *testing.T) {
	// Node with more free capacity should be selected more often.
	hubKP, _, _, _ := testFixture(t)
	idx := NewNodeIndex([]string{hubKP.PubkeyHex()})

	// Entry node: 45 free capacity.
	nodeA, _ := nostr.GenerateKeyPair()
	wgA := "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0NTY="
	attA, _ := nostr.CreateAttestation(hubKP, nodeA.PubkeyHex(), wgA, "op-1", []string{"entry"})
	infoA := types.NodeInfo{
		WGPubkey: wgA, Endpoint: "203.0.113.1:51820",
		UploadMbps: 100, DownloadMbps: 100, Load: 5, Capacity: 50, Role: types.RoleEntry,
	}
	evA, _ := BuildAnnouncementEvent(nodeA, infoA, attA)
	idx.ProcessEvent(evA)

	// Entry node: 1 free capacity (almost full).
	nodeC, _ := nostr.GenerateKeyPair()
	wgC := "cXdlcnR5dWlvcGFzZGZnaGprbHp4Y3Zibm0xMjM0NQ=="
	attC, _ := nostr.CreateAttestation(hubKP, nodeC.PubkeyHex(), wgC, "op-3", []string{"entry"})
	infoC := types.NodeInfo{
		WGPubkey: wgC, Endpoint: "203.0.113.3:51820",
		UploadMbps: 100, DownloadMbps: 100, Load: 49, Capacity: 50, Role: types.RoleEntry,
	}
	evC, _ := BuildAnnouncementEvent(nodeC, infoC, attC)
	idx.ProcessEvent(evC)

	// Exit node.
	nodeB, _ := nostr.GenerateKeyPair()
	wgB := "eHl6YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4MTIzNA=="
	attB, _ := nostr.CreateAttestation(hubKP, nodeB.PubkeyHex(), wgB, "op-2", []string{"exit"})
	infoB := types.NodeInfo{
		WGPubkey: wgB, Endpoint: "203.0.113.2:51820",
		UploadMbps: 100, DownloadMbps: 100, Load: 10, Capacity: 50, Role: types.RoleExit,
	}
	evB, _ := BuildAnnouncementEvent(nodeB, infoB, attB)
	idx.ProcessEvent(evB)

	api := NewDiscoveryAPI(idx)
	api.maxRequests = 200 // High limit — this test makes 100 requests.
	server := httptest.NewServer(api.Handler())
	defer server.Close()

	selector := NewNodeSelector(server.URL, []string{hubKP.PubkeyHex()})

	// Run selection 100 times and count how often each entry is picked.
	counts := map[string]int{}
	for i := 0; i < 100; i++ {
		pair, err := selector.SelectPair()
		if err != nil {
			t.Fatalf("SelectPair: %v", err)
		}
		counts[pair.Entry.Info.Endpoint]++
	}

	// Node A (45 free) should be picked significantly more than node C (1 free).
	if counts["203.0.113.1:51820"] < counts["203.0.113.3:51820"] {
		t.Errorf("node with more capacity should be picked more often: A=%d, C=%d",
			counts["203.0.113.1:51820"], counts["203.0.113.3:51820"])
	}
}

func TestSelector_HubDown(t *testing.T) {
	// What happens when the hub is unreachable?
	selector := NewNodeSelector("http://localhost:19999", []string{"some-hub-pubkey"})
	_, err := selector.SelectPair()
	if err == nil {
		t.Error("should fail when hub is unreachable")
	}
}

func TestSelector_HubReturnsEmptyList(t *testing.T) {
	hubKP, _, _, _ := testFixture(t)
	idx := NewNodeIndex([]string{hubKP.PubkeyHex()})
	// Empty index — no nodes.

	api := NewDiscoveryAPI(idx)
	server := httptest.NewServer(api.Handler())
	defer server.Close()

	selector := NewNodeSelector(server.URL, []string{hubKP.PubkeyHex()})
	_, err := selector.SelectPair()
	if err == nil {
		t.Error("should fail when no nodes are available")
	}
}

// Suppress log noise in tests.
func init() {
	_ = time.Now() // Avoid unused import.
}
