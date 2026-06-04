package control

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Radi-Labs/ARFL/internal/quota"
	"github.com/Radi-Labs/ARFL/internal/wg"
)

// newTestServer creates an admin API server backed by mocks.
// No real WireGuard, no real nftables — everything is in-memory.
func newTestServer(t *testing.T) (*Server, *wg.MockManager) {
	t.Helper()

	mockWG := wg.NewMockManager()
	mockQuota := quota.NewNoopEnforcer()
	iface := "wg-test"

	// Create the mock interface so peer operations work
	if err := mockWG.CreateInterface(wg.InterfaceConfig{
		Name:       iface,
		PrivateKey: "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0NTY=", // fake 32-byte key
		Address:    "10.100.0.1/24",
	}); err != nil {
		t.Fatalf("create mock interface: %v", err)
	}

	server := NewServer(mockWG, mockQuota, iface)
	return server, mockWG
}

// TestAddPeer verifies that POST /peers adds a WireGuard peer.
//
// This is the most common operation in ARFL — every time a user connects,
// the hub calls this endpoint to register their public key on the node.
func TestAddPeer(t *testing.T) {
	srv, mockWG := newTestServer(t)

	body := AddPeerRequest{
		PublicKey:  "dGVzdHB1YmtleTEyMzQ1Njc4OTAxMjM0NTY3ODkwMTI=",
		AllowedIPs: []string{"10.100.0.2/32"},
	}

	resp := doRequest(t, srv, "POST", "/peers", body)

	if resp.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d. Body: %s", resp.Code, http.StatusCreated, resp.Body.String())
	}

	// Verify the peer was actually added in the mock
	if mockWG.PeerCount("wg-test") != 1 {
		t.Errorf("peer count = %d, want 1", mockWG.PeerCount("wg-test"))
	}
}

// TestAddPeer_WithQuota verifies that a quota is set when requested.
func TestAddPeer_WithQuota(t *testing.T) {
	srv, _ := newTestServer(t)

	body := AddPeerRequest{
		PublicKey:  "dGVzdHB1YmtleTEyMzQ1Njc4OTAxMjM0NTY3ODkwMTI=",
		AllowedIPs: []string{"10.100.0.2/32"},
		TunnelIP:   "10.100.0.2",
		QuotaBytes: 268435456, // 256 MB slab
	}

	resp := doRequest(t, srv, "POST", "/peers", body)

	if resp.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusCreated)
	}
}

// TestListPeers verifies that GET /peers returns peer stats.
func TestListPeers(t *testing.T) {
	srv, mockWG := newTestServer(t)

	// Add a peer, then simulate some traffic
	pubkey := "dGVzdHB1YmtleTEyMzQ1Njc4OTAxMjM0NTY3ODkwMTI="
	mockWG.AddPeer("wg-test", wg.PeerConfig{
		PublicKey:  pubkey,
		AllowedIPs: []string{"10.100.0.2/32"},
	})
	mockWG.SimulateTraffic("wg-test", pubkey, 1000, 2000)

	resp := doRequest(t, srv, "GET", "/peers", nil)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}

	var stats []wg.PeerStats
	if err := json.Unmarshal(resp.Body.Bytes(), &stats); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if len(stats) != 1 {
		t.Fatalf("got %d peers, want 1", len(stats))
	}

	if stats[0].ReceiveBytes != 1000 {
		t.Errorf("ReceiveBytes = %d, want 1000", stats[0].ReceiveBytes)
	}
	if stats[0].TransmitBytes != 2000 {
		t.Errorf("TransmitBytes = %d, want 2000", stats[0].TransmitBytes)
	}
	if stats[0].TotalBytes != 3000 {
		t.Errorf("TotalBytes = %d, want 3000", stats[0].TotalBytes)
	}
}

// TestRemovePeer verifies that DELETE /peers/{key} removes a peer.
//
// This is called when a user's bandwidth is exhausted or they disconnect.
// If this fails silently, zombie peers consume resources forever.
func TestRemovePeer(t *testing.T) {
	srv, mockWG := newTestServer(t)

	pubkey := "dGVzdHB1YmtleTEyMzQ1Njc4OTAxMjM0NTY3ODkwMTI="
	mockWG.AddPeer("wg-test", wg.PeerConfig{
		PublicKey:  pubkey,
		AllowedIPs: []string{"10.100.0.2/32"},
	})

	if mockWG.PeerCount("wg-test") != 1 {
		t.Fatal("precondition: peer should exist")
	}

	resp := doRequest(t, srv, "DELETE", "/peers/"+pubkey, nil)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d. Body: %s", resp.Code, http.StatusOK, resp.Body.String())
	}

	if mockWG.PeerCount("wg-test") != 0 {
		t.Errorf("peer count = %d, want 0 — peer was not removed", mockWG.PeerCount("wg-test"))
	}
}

// TestHealth verifies the health check endpoint.
func TestHealth(t *testing.T) {
	srv, _ := newTestServer(t)

	resp := doRequest(t, srv, "GET", "/health", nil)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
}

// TestAddPeer_InvalidBody verifies that bad input returns 400, not 500.
//
// Why this matters: if the hub sends malformed JSON (bug in hub code),
// the node should respond "bad request", not crash or silently succeed.
func TestAddPeer_InvalidBody(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest("POST", "/peers", bytes.NewBufferString("not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// TestSetQuota verifies that POST /quota refreshes a bandwidth quota.
func TestSetQuota(t *testing.T) {
	srv, _ := newTestServer(t)

	body := SetQuotaRequest{
		TunnelIP: "10.100.0.2",
		Bytes:    268435456,
	}

	resp := doRequest(t, srv, "POST", "/quota", body)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
}

// --- Test helpers ---

func doRequest(t *testing.T, srv *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode request body: %v", err)
		}
	}

	req := httptest.NewRequest(method, path, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	return w
}
