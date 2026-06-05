package discovery

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/Radi-Labs/ARFL/internal/nostr"
	"github.com/Radi-Labs/ARFL/pkg/protocol"
	"github.com/Radi-Labs/ARFL/pkg/types"
)

// OnlineTTL is how long a node stays "online" after its last announcement.
// If no announcement arrives within this window, the node is marked offline.
// This is SEPARATE from the payment refund threshold (30 min) — a node can
// be "offline" for discovery but still within the refund grace period.
const OnlineTTL = 5 * time.Minute

// IndexedNode wraps a NodeInfo with discovery metadata.
type IndexedNode struct {
	Info        types.NodeInfo     `json:"info"`
	Event       *nostr.Event       `json:"event"`       // Raw signed event (for client verification)
	Attestation *nostr.Attestation `json:"attestation"` // Hub attestation (for client verification)
	LastSeen    time.Time          `json:"last_seen"`
	Online      bool               `json:"online"`
}

// NodeIndex is the hub's in-memory registry of verified nodes.
// It validates every incoming announcement against strict criteria
// before adding/updating a node in the index.
//
// Thread-safe: multiple relay subscriptions feed events concurrently.
type NodeIndex struct {
	nodes        map[string]*IndexedNode // keyed by node Nostr pubkey
	mu           sync.RWMutex
	trustedHubs  []string // List of trusted hub pubkeys
	minBandwidth int      // Minimum Mbps (from protocol constants)
}

// NewNodeIndex creates an empty index with the given trusted hub pubkeys.
func NewNodeIndex(trustedHubPubkeys []string) *NodeIndex {
	return &NodeIndex{
		nodes:        make(map[string]*IndexedNode),
		trustedHubs:  trustedHubPubkeys,
		minBandwidth: protocol.MinBandwidthMbps,
	}
}

// ProcessEvent validates and indexes a node announcement event.
// This is called for every EVENT received from the relay subscription.
//
// Validation pipeline (any failure = event rejected):
// 1. Event kind must be 30078 (node announcement)
// 2. Event signature must be valid (tamper-proof)
// 3. Attestation tag must be present and parseable
// 4. Attestation must be signed by a trusted hub
// 5. Attestation must not be expired
// 6. Node binding: event pubkey = attestation node pubkey
// 7. Content must parse as valid NodeInfo
// 8. NodeInfo fields must pass strict validation
// 9. Event must be fresh (created_at within OnlineTTL)
func (idx *NodeIndex) ProcessEvent(event *nostr.Event) error {
	// 1. Check event kind.
	if event.Kind != protocol.NostrKindNodeAnnouncement {
		return fmt.Errorf("wrong event kind: %d", event.Kind)
	}

	// 2. Verify event signature.
	if err := event.Verify(); err != nil {
		return fmt.Errorf("invalid event signature: %w", err)
	}

	// 3. Parse attestation from tag.
	attJSON := event.GetTagValue("attestation")
	if attJSON == "" {
		return fmt.Errorf("missing attestation tag")
	}
	att, err := nostr.DecodeAttestation(attJSON)
	if err != nil {
		return fmt.Errorf("decode attestation: %w", err)
	}

	// 4+5. Verify attestation (trusted hub + not expired).
	if err := att.Verify(idx.trustedHubs); err != nil {
		return fmt.Errorf("attestation verification failed: %w", err)
	}

	// 6. Parse NodeInfo from content.
	var info types.NodeInfo
	if err := json.Unmarshal([]byte(event.Content), &info); err != nil {
		return fmt.Errorf("invalid node info content: %w", err)
	}

	// 7. Verify node binding (event pubkey matches attestation, WG key matches, role allowed).
	if err := att.VerifyNodeBinding(event.Pubkey, info.WGPubkey, string(info.Role)); err != nil {
		return fmt.Errorf("node binding failed: %w", err)
	}

	// 8. Strict field validation.
	if err := validateNodeInfo(&info); err != nil {
		return fmt.Errorf("invalid node info: %w", err)
	}

	// 9. Check freshness.
	eventAge := time.Since(time.Unix(event.CreatedAt, 0))
	if eventAge > OnlineTTL {
		return fmt.Errorf("stale announcement: %s old", eventAge)
	}

	// All checks passed — add/update the index.
	idx.mu.Lock()
	idx.nodes[event.Pubkey] = &IndexedNode{
		Info:        info,
		Event:       event,
		Attestation: att,
		LastSeen:    time.Now(),
		Online:      true,
	}
	idx.mu.Unlock()

	log.Printf("[index] indexed node %s | role=%s | endpoint=%s | load=%d/%d",
		event.Pubkey[:8], info.Role, info.Endpoint, info.Load, info.Capacity)
	return nil
}

// validateNodeInfo performs strict validation on node metadata.
// This prevents malicious nodes from advertising garbage data.
func validateNodeInfo(info *types.NodeInfo) error {
	// WireGuard pubkey must be exactly 44 chars (32 bytes base64).
	if len(info.WGPubkey) != 44 {
		return fmt.Errorf("invalid WG pubkey length: %d (want 44)", len(info.WGPubkey))
	}

	// Endpoint must be valid host:port.
	host, _, err := net.SplitHostPort(info.Endpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint: %w", err)
	}

	// Reject private/link-local IPs (unless testing).
	ip := net.ParseIP(host)
	if ip != nil {
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return fmt.Errorf("endpoint uses non-routable IP: %s", host)
		}
		// Allow private IPs (10.x, 192.168.x) for testing/dev.
		// In production, the hub config would set a flag to reject these.
	}

	// Bandwidth must meet minimum.
	if info.UploadMbps < protocol.MinBandwidthMbps {
		return fmt.Errorf("upload %d Mbps below minimum %d", info.UploadMbps, protocol.MinBandwidthMbps)
	}
	if info.DownloadMbps < protocol.MinBandwidthMbps {
		return fmt.Errorf("download %d Mbps below minimum %d", info.DownloadMbps, protocol.MinBandwidthMbps)
	}

	// Capacity must be positive.
	if info.Capacity <= 0 {
		return fmt.Errorf("capacity must be positive, got %d", info.Capacity)
	}

	// Load must not exceed capacity.
	if info.Load < 0 || info.Load > info.Capacity {
		return fmt.Errorf("load %d out of bounds [0, %d]", info.Load, info.Capacity)
	}

	// Role must be valid.
	switch info.Role {
	case types.RoleEntry, types.RoleExit, types.RoleBoth:
		// OK
	default:
		return fmt.Errorf("invalid role: %q", info.Role)
	}

	return nil
}

// PruneOffline marks nodes as offline if their last announcement
// is older than OnlineTTL. Run this on a timer in the hub daemon.
func (idx *NodeIndex) PruneOffline() int {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	pruned := 0
	for pubkey, node := range idx.nodes {
		if node.Online && time.Since(node.LastSeen) > OnlineTTL {
			node.Online = false
			pruned++
			log.Printf("[index] node %s marked offline (last seen %s ago)",
				pubkey[:8], time.Since(node.LastSeen).Round(time.Second))
		}
	}
	return pruned
}

// ListOnline returns all currently online nodes.
func (idx *NodeIndex) ListOnline() []*IndexedNode {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var nodes []*IndexedNode
	for _, node := range idx.nodes {
		if node.Online {
			nodes = append(nodes, node)
		}
	}
	return nodes
}

// ListByRole returns online nodes filtered by role.
func (idx *NodeIndex) ListByRole(role types.NodeRole) []*IndexedNode {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var nodes []*IndexedNode
	for _, node := range idx.nodes {
		if !node.Online {
			continue
		}
		if node.Info.Role == role || node.Info.Role == types.RoleBoth {
			nodes = append(nodes, node)
		}
	}
	return nodes
}

// GetNode returns a specific node by pubkey.
func (idx *NodeIndex) GetNode(pubkey string) (*IndexedNode, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	node, ok := idx.nodes[pubkey]
	return node, ok
}

// NodeCount returns total and online node counts.
func (idx *NodeIndex) NodeCount() (total int, online int) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	total = len(idx.nodes)
	for _, node := range idx.nodes {
		if node.Online {
			online++
		}
	}
	return
}
