package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Radi-Labs/ARFL/pkg/types"
	"github.com/elnosh/gonuts/cashu"
)

// --- NodeSelector tests ---

func TestPairNodes_HappyPath(t *testing.T) {
	nodes := []types.NodeInfo{
		{NostrPubkey: "entry1", Role: types.RoleEntry, Endpoint: "203.0.113.10:51820", ConnectURL: "http://e1:9091"},
		{NostrPubkey: "exit1", Role: types.RoleExit, Endpoint: "203.0.113.11:51821", ConnectURL: "http://x1:9091"},
		{NostrPubkey: "exit2", Role: types.RoleExit, Endpoint: "203.0.113.12:51821", ConnectURL: "http://x2:9091"},
	}

	pair, err := PairNodes(nodes)
	if err != nil {
		t.Fatalf("PairNodes: %v", err)
	}
	if pair.Entry.NostrPubkey != "entry1" {
		t.Errorf("expected entry1, got %s", pair.Entry.NostrPubkey)
	}
	if pair.Exit.Role != types.RoleExit {
		t.Errorf("exit should have exit role, got %s", pair.Exit.Role)
	}
	if pair.Transport != types.TransportWireGuard {
		t.Errorf("expected wireguard transport, got %s", pair.Transport)
	}
	// Entry and exit should differ when possible.
	if pair.Entry.NostrPubkey == pair.Exit.NostrPubkey {
		t.Error("entry and exit should differ when multiple nodes available")
	}
}

func TestPairNodes_BothRole(t *testing.T) {
	nodes := []types.NodeInfo{
		{NostrPubkey: "both1", Role: types.RoleBoth, Endpoint: "203.0.113.20:51820", ConnectURL: "http://b1:9091"},
		{NostrPubkey: "both2", Role: types.RoleBoth, Endpoint: "203.0.113.21:51820", ConnectURL: "http://b2:9091"},
	}

	pair, err := PairNodes(nodes)
	if err != nil {
		t.Fatalf("PairNodes: %v", err)
	}
	// Both nodes serve both roles; entry != exit when possible.
	if pair.Entry.NostrPubkey == pair.Exit.NostrPubkey {
		t.Error("should select different nodes when two 'both' nodes available")
	}
}

func TestPairNodes_SingleBothNode(t *testing.T) {
	nodes := []types.NodeInfo{
		{NostrPubkey: "only", Role: types.RoleBoth, Endpoint: "203.0.113.22:51820", ConnectURL: "http://only:9091"},
	}

	pair, err := PairNodes(nodes)
	if err != nil {
		t.Fatalf("PairNodes: %v", err)
	}
	// Only one node — must use same for both.
	if pair.Entry.NostrPubkey != "only" || pair.Exit.NostrPubkey != "only" {
		t.Error("single both-role node should serve as both entry and exit")
	}
}

func TestPairNodes_NoEntryNodes(t *testing.T) {
	nodes := []types.NodeInfo{
		{NostrPubkey: "exit1", Role: types.RoleExit},
	}
	_, err := PairNodes(nodes)
	if err != ErrNoEntryNodes {
		t.Errorf("expected ErrNoEntryNodes, got %v", err)
	}
}

func TestPairNodes_NoExitNodes(t *testing.T) {
	nodes := []types.NodeInfo{
		{NostrPubkey: "entry1", Role: types.RoleEntry},
	}
	_, err := PairNodes(nodes)
	if err != ErrNoExitNodes {
		t.Errorf("expected ErrNoExitNodes, got %v", err)
	}
}

func TestPairNodes_EmptyList(t *testing.T) {
	_, err := PairNodes(nil)
	if err != ErrNoEntryNodes {
		t.Errorf("expected ErrNoEntryNodes for empty list, got %v", err)
	}
}

func TestFetchNodes_FromHub(t *testing.T) {
	// Mock hub server returning a node list.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/nodes" {
			http.NotFound(w, r)
			return
		}
		resp := map[string]interface{}{
			"nodes": []map[string]interface{}{
				{
					"info":      types.NodeInfo{NostrPubkey: "n1", Role: types.RoleEntry, Endpoint: "203.0.113.30:51820", ConnectURL: "http://n1:9091"},
					"online":    true,
					"last_seen": "2025-01-01T00:00:00Z",
				},
				{
					"info":      types.NodeInfo{NostrPubkey: "n2", Role: types.RoleExit, Endpoint: "203.0.113.31:51821", ConnectURL: "http://n2:9091"},
					"online":    true,
					"last_seen": "2025-01-01T00:00:00Z",
				},
				{
					"info":      types.NodeInfo{NostrPubkey: "offline", Role: types.RoleBoth, Endpoint: "203.0.113.32:51820", ConnectURL: "http://off:9091"},
					"online":    false,
					"last_seen": "2024-12-01T00:00:00Z",
				},
			},
			"count": 3,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	sel := NewNodeSelector(srv.URL)
	nodes, err := sel.FetchNodes(context.Background())
	if err != nil {
		t.Fatalf("FetchNodes: %v", err)
	}
	// Should get 2 online nodes (offline filtered out).
	if len(nodes) != 2 {
		t.Fatalf("expected 2 online nodes, got %d", len(nodes))
	}
}

func TestSelectPair_IntegrationWithMockHub(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"nodes": []map[string]interface{}{
				{
					"info":      types.NodeInfo{NostrPubkey: "e1", Role: types.RoleEntry, Endpoint: "203.0.113.40:51820", ConnectURL: "http://e1:9091"},
					"online":    true,
					"last_seen": "2025-01-01T00:00:00Z",
				},
				{
					"info":      types.NodeInfo{NostrPubkey: "x1", Role: types.RoleExit, Endpoint: "203.0.113.41:51821", ConnectURL: "http://x1:9091"},
					"online":    true,
					"last_seen": "2025-01-01T00:00:00Z",
				},
			},
			"count": 2,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	sel := NewNodeSelector(srv.URL)
	pair, err := sel.SelectPair(context.Background())
	if err != nil {
		t.Fatalf("SelectPair: %v", err)
	}
	if pair.Entry.NostrPubkey != "e1" {
		t.Errorf("expected entry e1, got %s", pair.Entry.NostrPubkey)
	}
	if pair.Exit.NostrPubkey != "x1" {
		t.Errorf("expected exit x1, got %s", pair.Exit.NostrPubkey)
	}
}

func TestPairNodesWithPreferred_SelectsCommonPreferredTransport(t *testing.T) {
	nodes := []types.NodeInfo{
		{
			NostrPubkey: "entry",
			Role:        types.RoleEntry,
			Endpoint:    "203.0.113.50:51820",
			Transports: []types.TransportCapability{
				{Transport: types.TransportWireGuard, Endpoint: "203.0.113.50:51820", ConnectURL: "http://203.0.113.50:9091"},
				{Transport: types.TransportHysteria2, Endpoint: "203.0.113.50:443", ConnectURL: "http://203.0.113.50:9091"},
			},
		},
		{
			NostrPubkey: "exit",
			Role:        types.RoleExit,
			Endpoint:    "203.0.113.51:51821",
			Transports: []types.TransportCapability{
				{Transport: types.TransportWireGuard, Endpoint: "203.0.113.51:51821", ConnectURL: "http://203.0.113.51:9091"},
				{Transport: types.TransportHysteria2, Endpoint: "203.0.113.51:443", ConnectURL: "http://203.0.113.51:9091"},
			},
		},
	}

	pair, err := PairNodesWithPreferred(nodes, []types.Transport{types.TransportHysteria2, types.TransportWireGuard})
	if err != nil {
		t.Fatalf("PairNodesWithPreferred: %v", err)
	}
	if pair.Transport != types.TransportHysteria2 {
		t.Fatalf("expected hysteria2 transport, got %s", pair.Transport)
	}
}

func TestPairNodesWithPreferred_NoCompatiblePair(t *testing.T) {
	nodes := []types.NodeInfo{
		{
			NostrPubkey: "entry",
			Role:        types.RoleEntry,
			Endpoint:    "203.0.113.52:51820",
			Transports: []types.TransportCapability{
				{Transport: types.TransportHysteria2, Endpoint: "203.0.113.52:443", ConnectURL: "http://203.0.113.52:9091"},
			},
		},
		{
			NostrPubkey: "exit",
			Role:        types.RoleExit,
			Endpoint:    "203.0.113.53:51821",
			Transports: []types.TransportCapability{
				{Transport: types.TransportAmneziaWG, Endpoint: "203.0.113.53:51831", ConnectURL: "http://203.0.113.53:9091"},
			},
		},
	}

	_, err := PairNodesWithPreferred(nodes, []types.Transport{types.TransportWireGuard})
	if err != ErrNoCompatiblePair {
		t.Fatalf("expected ErrNoCompatiblePair, got %v", err)
	}
}

func TestPairNodesWithPolicy_RespectsAllowedTransports(t *testing.T) {
	nodes := []types.NodeInfo{
		{
			NostrPubkey: "entry",
			Role:        types.RoleEntry,
			Endpoint:    "203.0.113.60:51820",
			Transports: []types.TransportCapability{
				{Transport: types.TransportWireGuard, Endpoint: "203.0.113.60:51820", ConnectURL: "http://203.0.113.60:9091"},
				{Transport: types.TransportHysteria2, Endpoint: "203.0.113.60:443", ConnectURL: "http://203.0.113.60:9091"},
			},
		},
		{
			NostrPubkey: "exit",
			Role:        types.RoleExit,
			Endpoint:    "203.0.113.61:51821",
			Transports: []types.TransportCapability{
				{Transport: types.TransportWireGuard, Endpoint: "203.0.113.61:51821", ConnectURL: "http://203.0.113.61:9091"},
				{Transport: types.TransportHysteria2, Endpoint: "203.0.113.61:443", ConnectURL: "http://203.0.113.61:9091"},
			},
		},
	}

	pair, err := PairNodesWithPolicy(
		nodes,
		[]types.Transport{types.TransportHysteria2, types.TransportWireGuard},
		map[types.Transport]struct{}{types.TransportWireGuard: {}},
	)
	if err != nil {
		t.Fatalf("PairNodesWithPolicy: %v", err)
	}
	if pair.Transport != types.TransportWireGuard {
		t.Fatalf("expected allowed wireguard transport, got %s", pair.Transport)
	}
}

func TestPairNodesWithPreferred_ProjectsNodeEndpointsForSelectedTransport(t *testing.T) {
	nodes := []types.NodeInfo{
		{
			NostrPubkey: "entry",
			Role:        types.RoleEntry,
			Endpoint:    "203.0.113.70:51820",
			ConnectURL:  "http://203.0.113.70:9091",
			Transports: []types.TransportCapability{
				{Transport: types.TransportWireGuard, Endpoint: "203.0.113.70:51820", ConnectURL: "http://203.0.113.70:9091"},
				{Transport: types.TransportHysteria2, Endpoint: "203.0.113.70:443", ConnectURL: "http://203.0.113.70:9092"},
			},
		},
		{
			NostrPubkey: "exit",
			Role:        types.RoleExit,
			Endpoint:    "203.0.113.71:51821",
			ConnectURL:  "http://203.0.113.71:9091",
			Transports: []types.TransportCapability{
				{Transport: types.TransportWireGuard, Endpoint: "203.0.113.71:51821", ConnectURL: "http://203.0.113.71:9091"},
				{Transport: types.TransportHysteria2, Endpoint: "203.0.113.71:443", ConnectURL: "http://203.0.113.71:9092"},
			},
		},
	}
	pair, err := PairNodesWithPreferred(nodes, []types.Transport{types.TransportHysteria2, types.TransportWireGuard})
	if err != nil {
		t.Fatalf("PairNodesWithPreferred: %v", err)
	}
	if pair.Transport != types.TransportHysteria2 {
		t.Fatalf("expected hysteria2 transport, got %s", pair.Transport)
	}
	if pair.Entry.Endpoint != "203.0.113.70:443" || pair.Entry.ConnectURL != "http://203.0.113.70:9092" {
		t.Fatalf("entry not projected to hysteria2 endpoint/connect: %+v", pair.Entry)
	}
	if pair.Exit.Endpoint != "203.0.113.71:443" || pair.Exit.ConnectURL != "http://203.0.113.71:9092" {
		t.Fatalf("exit not projected to hysteria2 endpoint/connect: %+v", pair.Exit)
	}
}

func TestPairNodesWithPolicy_PrioritizesHighestPreferredTransportBucket(t *testing.T) {
	nodes := []types.NodeInfo{
		{
			NostrPubkey: "entry-h2",
			Role:        types.RoleEntry,
			Transports: []types.TransportCapability{
				{Transport: types.TransportWireGuard, Endpoint: "203.0.113.81:51820", ConnectURL: "http://203.0.113.81:9091"},
				{Transport: types.TransportHysteria2, Endpoint: "203.0.113.81:443", ConnectURL: "http://203.0.113.81:9091"},
			},
		},
		{
			NostrPubkey: "entry-wg-only",
			Role:        types.RoleEntry,
			Transports: []types.TransportCapability{
				{Transport: types.TransportWireGuard, Endpoint: "203.0.113.82:51820", ConnectURL: "http://203.0.113.82:9091"},
			},
		},
		{
			NostrPubkey: "exit-h2",
			Role:        types.RoleExit,
			Transports: []types.TransportCapability{
				{Transport: types.TransportWireGuard, Endpoint: "203.0.113.83:51820", ConnectURL: "http://203.0.113.83:9091"},
				{Transport: types.TransportHysteria2, Endpoint: "203.0.113.83:443", ConnectURL: "http://203.0.113.83:9091"},
			},
		},
		{
			NostrPubkey: "exit-wg-only",
			Role:        types.RoleExit,
			Transports: []types.TransportCapability{
				{Transport: types.TransportWireGuard, Endpoint: "203.0.113.84:51820", ConnectURL: "http://203.0.113.84:9091"},
			},
		},
	}

	pair, err := PairNodesWithPolicy(
		nodes,
		[]types.Transport{types.TransportHysteria2, types.TransportWireGuard},
		map[types.Transport]struct{}{
			types.TransportWireGuard: {},
			types.TransportHysteria2: {},
		},
	)
	if err != nil {
		t.Fatalf("PairNodesWithPolicy: %v", err)
	}
	if pair.Transport != types.TransportHysteria2 {
		t.Fatalf("expected hysteria2 to be prioritized, got %s", pair.Transport)
	}
}

// --- CashuConnector tests ---

func TestCashuConnector_HappyPath(t *testing.T) {
	// Mock the node's /cashu-connect endpoint. The path is asserted exactly:
	// posting proofs to /connect reaches the RSA gate, which decodes them into
	// an empty token and rejects the request.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cashu-connect" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}

		var req CashuConnectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", 400)
			return
		}

		if len(req.Proofs) == 0 || req.WGPubkey == "" {
			http.Error(w, "missing fields", 400)
			return
		}

		// Calculate bytes from proofs.
		var totalSats uint64
		for _, p := range req.Proofs {
			totalSats += p.Amount
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ConnectResult{
			TunnelIP:     "10.100.0.2/32",
			NodeWGPubkey: "fakeNodePubkey==",
			BytesAllowed: int64(totalSats) * 1_000_000,
		})
	}))
	defer srv.Close()

	cc := NewCashuConnector()
	proofs := cashu.Proofs{
		{Amount: 10, Id: "test-keyset", Secret: "sec1", C: "02abc"},
	}

	result, err := cc.ConnectWithProofs(context.Background(), srv.URL, proofs, "clientPubKey==")
	if err != nil {
		t.Fatalf("ConnectWithProofs: %v", err)
	}
	if result.TunnelIP != "10.100.0.2/32" {
		t.Errorf("expected tunnel IP 10.100.0.2/32, got %s", result.TunnelIP)
	}
	if result.BytesAllowed != 10_000_000 {
		t.Errorf("expected 10M bytes, got %d", result.BytesAllowed)
	}
}

func TestCashuConnector_NodeRejectsProofs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"detail": "proof already spent"})
	}))
	defer srv.Close()

	cc := NewCashuConnector()
	proofs := cashu.Proofs{{Amount: 1, Id: "ks", Secret: "s", C: "02c"}}

	_, err := cc.ConnectWithProofs(context.Background(), srv.URL, proofs, "pk==")
	if err == nil {
		t.Fatal("expected error for rejected proofs")
	}
	t.Logf("got expected error: %v", err)
}

func TestCashuConnector_EmptyProofs(t *testing.T) {
	cc := NewCashuConnector()
	_, err := cc.ConnectWithProofs(context.Background(), "http://localhost", nil, "pk==")
	if err == nil {
		t.Fatal("expected error for empty proofs")
	}
}

func TestCashuConnector_EmptyPubkey(t *testing.T) {
	cc := NewCashuConnector()
	proofs := cashu.Proofs{{Amount: 1, Id: "ks", Secret: "s", C: "02c"}}
	_, err := cc.ConnectWithProofs(context.Background(), "http://localhost", proofs, "")
	if err == nil {
		t.Fatal("expected error for empty pubkey")
	}
}

func TestConnectPair_BothSucceed(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		ip := "10.100.0.2/32"
		if callCount == 2 {
			ip = "10.100.0.3/32"
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ConnectResult{
			TunnelIP:     ip,
			NodeWGPubkey: "nodePub==",
			BytesAllowed: 5_000_000,
		})
	}))
	defer srv.Close()

	cc := NewCashuConnector()
	pair := &NodePair{
		Entry: types.NodeInfo{ConnectURL: srv.URL, Role: types.RoleEntry},
		Exit:  types.NodeInfo{ConnectURL: srv.URL, Role: types.RoleExit},
	}

	entry, exit, err := cc.ConnectPair(
		context.Background(),
		pair,
		cashu.Proofs{{Amount: 5, Id: "ks", Secret: "s1", C: "02a"}},
		cashu.Proofs{{Amount: 5, Id: "ks", Secret: "s2", C: "02b"}},
		"clientPub==",
	)
	if err != nil {
		t.Fatalf("ConnectPair: %v", err)
	}
	if entry.TunnelIP != "10.100.0.2/32" {
		t.Errorf("entry tunnel IP: got %s", entry.TunnelIP)
	}
	if exit.TunnelIP != "10.100.0.3/32" {
		t.Errorf("exit tunnel IP: got %s", exit.TunnelIP)
	}
}
