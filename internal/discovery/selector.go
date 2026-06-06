package discovery

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"time"

	"github.com/Radi-Labs/ARFL/internal/nostr"
	"github.com/Radi-Labs/ARFL/pkg/types"
)

// NodeSelector picks entry + exit nodes from the hub's discovery API.
// It runs on the CLIENT — the user's device.
//
// Design: the hub returns raw signed events + attestations. The client
// verifies every signature before trusting ANY node. The hub cannot
// manipulate the list without being detected.
type NodeSelector struct {
	hubURL      string
	trustedHubs []string
	httpClient  *http.Client
}

// NewNodeSelector creates a selector that fetches from the given hub API.
func NewNodeSelector(hubURL string, trustedHubPubkeys []string) *NodeSelector {
	return &NodeSelector{
		hubURL:      hubURL,
		trustedHubs: trustedHubPubkeys,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
	}
}

// NodePair is the result of node selection: an entry and exit node
// verified to be from different operators.
type NodePair struct {
	Entry *IndexedNode
	Exit  *IndexedNode
}

// SelectPair fetches the node list from the hub, verifies all signatures,
// and selects an entry + exit pair with operator diversity.
func (s *NodeSelector) SelectPair() (*NodePair, error) {
	// Step 1: Fetch node list from hub.
	nodes, err := s.fetchAndVerify()
	if err != nil {
		return nil, fmt.Errorf("fetch nodes: %w", err)
	}

	// Step 2: Split into entry-capable and exit-capable nodes.
	var entries, exits []*IndexedNode
	for _, node := range nodes {
		switch node.Info.Role {
		case types.RoleEntry:
			entries = append(entries, node)
		case types.RoleExit:
			exits = append(exits, node)
		case types.RoleBoth:
			entries = append(entries, node)
			exits = append(exits, node)
		}
	}

	if len(entries) == 0 {
		return nil, fmt.Errorf("no entry nodes available")
	}
	if len(exits) == 0 {
		return nil, fmt.Errorf("no exit nodes available")
	}

	// Step 3: Weighted random selection with operator diversity.
	return selectDiversePair(entries, exits)
}

// fetchAndVerify gets the node list from the hub and verifies every signature.
func (s *NodeSelector) fetchAndVerify() ([]*IndexedNode, error) {
	resp, err := s.httpClient.Get(s.hubURL + "/nodes")
	if err != nil {
		return nil, fmt.Errorf("GET /nodes: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hub returned status %d", resp.StatusCode)
	}

	var discovery DiscoveryResponse
	if err := json.NewDecoder(resp.Body).Decode(&discovery); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	// Verify EVERY node's signatures client-side.
	var verified []*IndexedNode
	for _, node := range discovery.Nodes {
		if node.Event == nil || node.Attestation == nil {
			continue // Skip nodes without proofs.
		}

		// Verify event signature (proves the node actually published this).
		if err := node.Event.Verify(); err != nil {
			continue
		}

		// Verify attestation (proves a trusted hub vouched for this node).
		if err := node.Attestation.Verify(s.trustedHubs); err != nil {
			continue
		}

		// Verify binding (proves the event, attestation, and node info match).
		if err := node.Attestation.VerifyNodeBinding(
			node.Event.Pubkey, node.Info.WGPubkey, string(node.Info.Role)); err != nil {
			continue
		}

		verified = append(verified, node)
	}

	if len(verified) == 0 {
		return nil, fmt.Errorf("no nodes passed signature verification")
	}

	return verified, nil
}

// selectDiversePair picks an entry and exit node from different operators.
// Uses weighted random selection based on available capacity.
func selectDiversePair(entries, exits []*IndexedNode) (*NodePair, error) {
	// Shuffle to avoid deterministic selection patterns.
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	rng.Shuffle(len(entries), func(i, j int) { entries[i], entries[j] = entries[j], entries[i] })
	rng.Shuffle(len(exits), func(i, j int) { exits[i], exits[j] = exits[j], exits[i] })

	// Pick entry by weighted capacity.
	entry := weightedSelect(entries, rng)
	if entry == nil {
		return nil, fmt.Errorf("could not select entry node")
	}

	// Pick exit from different operator.
	var exitCandidates []*IndexedNode
	for _, e := range exits {
		if e.Attestation.OperatorID != entry.Attestation.OperatorID {
			exitCandidates = append(exitCandidates, e)
		}
	}

	if len(exitCandidates) == 0 {
		return nil, fmt.Errorf("no exit nodes from different operator than entry (operator=%s)",
			entry.Attestation.OperatorID)
	}

	exit := weightedSelect(exitCandidates, rng)
	if exit == nil {
		return nil, fmt.Errorf("could not select exit node")
	}

	return &NodePair{Entry: entry, Exit: exit}, nil
}

// weightedSelect picks a node with probability proportional to available capacity.
// Nodes with more free capacity get picked more often — this naturally load-balances.
func weightedSelect(nodes []*IndexedNode, rng *rand.Rand) *IndexedNode {
	var totalWeight int
	for _, n := range nodes {
		weight := n.Info.Capacity - n.Info.Load
		if weight <= 0 {
			weight = 1 // Even full nodes get a small chance.
		}
		totalWeight += weight
	}

	if totalWeight == 0 {
		return nil
	}

	pick := rng.Intn(totalWeight)
	for _, n := range nodes {
		weight := n.Info.Capacity - n.Info.Load
		if weight <= 0 {
			weight = 1
		}
		pick -= weight
		if pick < 0 {
			return n
		}
	}

	return nodes[0] // Fallback — shouldn't reach here.
}

// FetchNodeList is a standalone function for fetching and verifying nodes
// without the full selector. Useful for CLI tools and debugging.
func FetchNodeList(hubURL string, trustedHubs []string) ([]*IndexedNode, error) {
	selector := NewNodeSelector(hubURL, trustedHubs)
	return selector.fetchAndVerify()
}

// ParseEventNodeInfo extracts NodeInfo from a verified Nostr event.
func ParseEventNodeInfo(event *nostr.Event) (*types.NodeInfo, error) {
	var info types.NodeInfo
	if err := json.Unmarshal([]byte(event.Content), &info); err != nil {
		return nil, fmt.Errorf("parse node info: %w", err)
	}
	return &info, nil
}
