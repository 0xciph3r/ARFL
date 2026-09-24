// Package client — NodeSelector fetches the node list from the hub and
// pairs entry/exit nodes entirely on the client side.
//
// Privacy property: the hub publishes the full node list but never learns
// which pair the client chose. Combined with Cashu blind tokens, the hub
// cannot link payment → node pair → traffic.
package client

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"time"

	"github.com/Radi-Labs/ARFL/pkg/types"
)

// Errors returned by the selector.
var (
	ErrNoEntryNodes     = errors.New("no online entry nodes available")
	ErrNoExitNodes      = errors.New("no online exit nodes available")
	ErrNoCompatiblePair = errors.New("no compatible entry/exit node pair for selected transports")
	ErrFetchFailed      = errors.New("failed to fetch node list from hub")
)

// NodePair is a client-selected entry/exit node combination.
type NodePair struct {
	Entry     types.NodeInfo  `json:"entry"`
	Exit      types.NodeInfo  `json:"exit"`
	Transport types.Transport `json:"transport,omitempty"`
}

// discoveryNode matches the IndexedNode JSON from GET /nodes.
type discoveryNode struct {
	Info     types.NodeInfo `json:"info"`
	Online   bool           `json:"online"`
	LastSeen time.Time      `json:"last_seen"`
}

// discoveryResponse matches the DiscoveryResponse JSON from GET /nodes.
type discoveryResponse struct {
	Nodes []*discoveryNode `json:"nodes"`
	Count int              `json:"count"`
}

type pairCandidate struct {
	entry     types.NodeInfo
	exit      types.NodeInfo
	transport types.Transport
}

// NodeSelector fetches the node list from a hub and performs client-side pairing.
type NodeSelector struct {
	hubURL     string
	httpClient *http.Client
	preferred  []types.Transport
	allowed    map[types.Transport]struct{}
}

// NewNodeSelector creates a selector that fetches from the given hub URL.
func NewNodeSelector(hubURL string) *NodeSelector {
	return NewNodeSelectorWithPolicy(
		hubURL,
		[]types.Transport{types.TransportWireGuard},
		[]types.Transport{types.TransportWireGuard, types.TransportHysteria2, types.TransportAmneziaWG},
	)
}

// NewNodeSelectorWithPreferred creates a selector with transport preference order.
func NewNodeSelectorWithPreferred(hubURL string, preferred []types.Transport) *NodeSelector {
	return NewNodeSelectorWithPolicy(
		hubURL,
		preferred,
		[]types.Transport{types.TransportWireGuard, types.TransportHysteria2, types.TransportAmneziaWG},
	)
}

// NewNodeSelectorWithPolicy creates a selector with explicit preference and allowlist.
func NewNodeSelectorWithPolicy(hubURL string, preferred []types.Transport, allowed []types.Transport) *NodeSelector {
	if len(preferred) == 0 {
		preferred = []types.Transport{types.TransportWireGuard}
	}
	allowedSet := make(map[types.Transport]struct{}, len(allowed))
	for _, t := range allowed {
		allowedSet[t] = struct{}{}
	}
	return &NodeSelector{
		hubURL: hubURL,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		preferred: preferred,
		allowed:   allowedSet,
	}
}

// FetchNodes retrieves all online nodes from the hub.
func (s *NodeSelector) FetchNodes(ctx context.Context) ([]types.NodeInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.hubURL+"/nodes", nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFetchFailed, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("%w: status %d: %s", ErrFetchFailed, resp.StatusCode, body)
	}

	var dr discoveryResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 512*1024)).Decode(&dr); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	// Filter to online nodes only.
	var online []types.NodeInfo
	for _, n := range dr.Nodes {
		if n.Online {
			online = append(online, n.Info)
		}
	}
	return online, nil
}

// SelectPair fetches the node list and randomly selects an entry/exit pair.
// The selection is cryptographically random and happens entirely client-side.
func (s *NodeSelector) SelectPair(ctx context.Context) (*NodePair, error) {
	nodes, err := s.FetchNodes(ctx)
	if err != nil {
		return nil, err
	}
	return PairNodesWithPolicy(nodes, s.preferred, s.allowed)
}

// PairNodes selects a random entry/exit pair from a list of nodes.
// Exported for testing with pre-fetched node lists.
func PairNodes(nodes []types.NodeInfo) (*NodePair, error) {
	return PairNodesWithPolicy(nodes, []types.Transport{types.TransportWireGuard}, map[types.Transport]struct{}{
		types.TransportWireGuard: {},
	})
}

// PairNodesWithPreferred selects a random entry/exit pair from nodes with a
// common transport, honoring preferred transport order.
func PairNodesWithPreferred(nodes []types.NodeInfo, preferred []types.Transport) (*NodePair, error) {
	allowed := map[types.Transport]struct{}{
		types.TransportWireGuard: {},
		types.TransportHysteria2: {},
		types.TransportAmneziaWG: {},
	}
	return PairNodesWithPolicy(nodes, preferred, allowed)
}

// PairNodesWithPolicy selects a random entry/exit pair from nodes with a common transport,
// honoring preferred order and allowlist.
func PairNodesWithPolicy(nodes []types.NodeInfo, preferred []types.Transport, allowed map[types.Transport]struct{}) (*NodePair, error) {
	var entryNodes, exitNodes []types.NodeInfo

	for _, n := range nodes {
		switch n.Role {
		case types.RoleEntry:
			entryNodes = append(entryNodes, n)
		case types.RoleExit:
			exitNodes = append(exitNodes, n)
		case types.RoleBoth:
			entryNodes = append(entryNodes, n)
			exitNodes = append(exitNodes, n)
		}
	}

	if len(entryNodes) == 0 {
		return nil, ErrNoEntryNodes
	}
	if len(exitNodes) == 0 {
		return nil, ErrNoExitNodes
	}

	for _, transport := range preferred {
		if len(allowed) > 0 {
			if _, ok := allowed[transport]; !ok {
				continue
			}
		}
		candidates := collectCandidatesForTransport(entryNodes, exitNodes, transport)
		if len(candidates) == 0 {
			continue
		}
		selected, err := randomPickCandidate(candidates)
		if err != nil {
			return nil, fmt.Errorf("selecting node pair: %w", err)
		}
		return &NodePair{
			Entry:     selected.entry,
			Exit:      selected.exit,
			Transport: selected.transport,
		}, nil
	}
	return nil, ErrNoCompatiblePair
}

// randomPick selects a random element from a slice using crypto/rand.
func randomPick(nodes []types.NodeInfo) (types.NodeInfo, error) {
	if len(nodes) == 1 {
		return nodes[0], nil
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(nodes))))
	if err != nil {
		return types.NodeInfo{}, fmt.Errorf("crypto random: %w", err)
	}
	return nodes[n.Int64()], nil
}

func randomPickCandidate(candidates []pairCandidate) (pairCandidate, error) {
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(candidates))))
	if err != nil {
		return pairCandidate{}, fmt.Errorf("crypto random: %w", err)
	}
	return candidates[n.Int64()], nil
}

func capabilities(node types.NodeInfo) []types.TransportCapability {
	if len(node.Transports) > 0 {
		caps := make([]types.TransportCapability, 0, len(node.Transports))
		seen := map[types.Transport]struct{}{}
		for _, c := range node.Transports {
			if c.Transport == "" || c.Endpoint == "" {
				continue
			}
			if _, ok := seen[c.Transport]; ok {
				continue
			}
			seen[c.Transport] = struct{}{}
			caps = append(caps, c)
		}
		return caps
	}
	if node.Endpoint == "" {
		return nil
	}
	return []types.TransportCapability{{
		Transport:  types.TransportWireGuard,
		Endpoint:   node.Endpoint,
		ConnectURL: node.ConnectURL,
	}}
}

func projectNodeForTransport(node types.NodeInfo, transport types.Transport) (types.NodeInfo, bool) {
	if transport == "" {
		return types.NodeInfo{}, false
	}
	if len(node.Transports) == 0 {
		if transport != types.TransportWireGuard || node.Endpoint == "" {
			return types.NodeInfo{}, false
		}
		return node, true
	}

	for _, cap := range capabilities(node) {
		if cap.Transport != transport {
			continue
		}
		if cap.Endpoint == "" || cap.ConnectURL == "" {
			return types.NodeInfo{}, false
		}
		projected := node
		projected.Endpoint = cap.Endpoint
		projected.ConnectURL = cap.ConnectURL
		return projected, true
	}
	return types.NodeInfo{}, false
}

func collectCandidatesForTransport(entryNodes, exitNodes []types.NodeInfo, transport types.Transport) []pairCandidate {
	candidates := make([]pairCandidate, 0)
	for _, entry := range entryNodes {
		entryProjected, ok := projectNodeForTransport(entry, transport)
		if !ok {
			continue
		}

		compatibleDistinct := false
		for _, exit := range exitNodes {
			if exit.NostrPubkey == entry.NostrPubkey {
				continue
			}
			if _, ok := projectNodeForTransport(exit, transport); ok {
				compatibleDistinct = true
				break
			}
		}

		for _, exit := range exitNodes {
			if compatibleDistinct && exit.NostrPubkey == entry.NostrPubkey {
				continue
			}
			exitProjected, ok := projectNodeForTransport(exit, transport)
			if !ok {
				continue
			}
			candidates = append(candidates, pairCandidate{
				entry:     entryProjected,
				exit:      exitProjected,
				transport: transport,
			})
		}
	}
	return candidates
}
