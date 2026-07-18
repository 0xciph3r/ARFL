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
	ErrNoEntryNodes = errors.New("no online entry nodes available")
	ErrNoExitNodes  = errors.New("no online exit nodes available")
	ErrFetchFailed  = errors.New("failed to fetch node list from hub")
)

// NodePair is a client-selected entry/exit node combination.
type NodePair struct {
	Entry types.NodeInfo `json:"entry"`
	Exit  types.NodeInfo `json:"exit"`
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

// NodeSelector fetches the node list from a hub and performs client-side pairing.
type NodeSelector struct {
	hubURL     string
	httpClient *http.Client
}

// NewNodeSelector creates a selector that fetches from the given hub URL.
func NewNodeSelector(hubURL string) *NodeSelector {
	return &NodeSelector{
		hubURL: hubURL,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
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
	return PairNodes(nodes)
}

// PairNodes selects a random entry/exit pair from a list of nodes.
// Exported for testing with pre-fetched node lists.
func PairNodes(nodes []types.NodeInfo) (*NodePair, error) {
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

	// Cryptographically random selection.
	entry, err := randomPick(entryNodes)
	if err != nil {
		return nil, fmt.Errorf("selecting entry: %w", err)
	}

	// Ensure exit != entry (if possible).
	var candidateExits []types.NodeInfo
	for _, n := range exitNodes {
		if n.NostrPubkey != entry.NostrPubkey {
			candidateExits = append(candidateExits, n)
		}
	}
	if len(candidateExits) == 0 {
		// Only one node serves both roles — allow same node.
		candidateExits = exitNodes
	}

	exit, err := randomPick(candidateExits)
	if err != nil {
		return nil, fmt.Errorf("selecting exit: %w", err)
	}

	return &NodePair{Entry: entry, Exit: exit}, nil
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
