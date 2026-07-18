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
		{NostrPubkey: "entry1", Role: types.RoleEntry, ConnectURL: "http://e1:9091"},
		{NostrPubkey: "exit1", Role: types.RoleExit, ConnectURL: "http://x1:9091"},
		{NostrPubkey: "exit2", Role: types.RoleExit, ConnectURL: "http://x2:9091"},
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
	// Entry and exit should differ when possible.
	if pair.Entry.NostrPubkey == pair.Exit.NostrPubkey {
		t.Error("entry and exit should differ when multiple nodes available")
	}
}

func TestPairNodes_BothRole(t *testing.T) {
	nodes := []types.NodeInfo{
		{NostrPubkey: "both1", Role: types.RoleBoth, ConnectURL: "http://b1:9091"},
		{NostrPubkey: "both2", Role: types.RoleBoth, ConnectURL: "http://b2:9091"},
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
		{NostrPubkey: "only", Role: types.RoleBoth, ConnectURL: "http://only:9091"},
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
					"info":      types.NodeInfo{NostrPubkey: "n1", Role: types.RoleEntry, ConnectURL: "http://n1:9091"},
					"online":    true,
					"last_seen": "2025-01-01T00:00:00Z",
				},
				{
					"info":      types.NodeInfo{NostrPubkey: "n2", Role: types.RoleExit, ConnectURL: "http://n2:9091"},
					"online":    true,
					"last_seen": "2025-01-01T00:00:00Z",
				},
				{
					"info":      types.NodeInfo{NostrPubkey: "offline", Role: types.RoleBoth, ConnectURL: "http://off:9091"},
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
					"info":      types.NodeInfo{NostrPubkey: "e1", Role: types.RoleEntry, ConnectURL: "http://e1:9091"},
					"online":    true,
					"last_seen": "2025-01-01T00:00:00Z",
				},
				{
					"info":      types.NodeInfo{NostrPubkey: "x1", Role: types.RoleExit, ConnectURL: "http://x1:9091"},
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

// --- CashuConnector tests ---

func TestCashuConnector_HappyPath(t *testing.T) {
	// Mock node /connect endpoint.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/connect" || r.Method != http.MethodPost {
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
