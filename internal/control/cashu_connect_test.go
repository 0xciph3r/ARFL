package control

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Radi-Labs/ARFL/internal/node"
	"github.com/Radi-Labs/ARFL/internal/quota"
	"github.com/Radi-Labs/ARFL/internal/wg"
	"github.com/elnosh/gonuts/cashu"
)

// mockHubRedeem creates a mock hub /v1/redeem endpoint.
func mockHubRedeem(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/redeem", handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func setupCashuEnv(t *testing.T, hubHandler http.HandlerFunc) (*Server, *wg.MockManager) {
	t.Helper()
	mockWG := wg.NewMockManager()
	mockQuota := quota.NewNoopEnforcer()
	mockWG.CreateInterface(wg.InterfaceConfig{
		Name:       "wg-test",
		PrivateKey: "YPrKbTKgZ1HE/9bVKMYXEEaGFzk7Rp1G1dCfOBqvYW0=",
		ListenPort: 51820,
		Address:    "10.100.0.1/24",
	})

	srv := NewServer(mockWG, mockQuota, "wg-test")

	hub := mockHubRedeem(t, hubHandler)
	redeemer := node.NewHubRedeemer(hub.URL, "test-node-pubkey")
	srv.EnableCashuGate(redeemer, "fakeNodePubkey==", "10.100.0")

	return srv, mockWG
}

func TestCashuConnect_HappyPath(t *testing.T) {
	srv, mockWG := setupCashuEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":            true,
			"bytes_allowed": 10_000_000,
			"sats_redeemed": 10,
		})
	})

	reqBody, _ := json.Marshal(CashuConnectRequest{
		Proofs:   cashu.Proofs{{Amount: 10, Id: "ks1", Secret: "s1", C: "02abc"}},
		WGPubkey: "clientPubKey123==",
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/cashu-connect", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	srv.handleCashuConnect(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp ConnectResponse
	json.NewDecoder(rr.Body).Decode(&resp)

	if resp.Status != "connected" {
		t.Errorf("expected status 'connected', got %q", resp.Status)
	}
	if resp.TunnelIP == "" {
		t.Error("expected non-empty tunnel IP")
	}
	if resp.NodeWGPubkey != "fakeNodePubkey==" {
		t.Errorf("expected node pubkey, got %q", resp.NodeWGPubkey)
	}
	if resp.BytesAllowed != 10_000_000 {
		t.Errorf("expected 10M bytes, got %d", resp.BytesAllowed)
	}

	// Verify WG peer was added.
	if mockWG.PeerCount("wg-test") != 1 {
		t.Fatalf("expected 1 peer, got %d", mockWG.PeerCount("wg-test"))
	}
}

func TestCashuConnect_AlreadySpent(t *testing.T) {
	srv, _ := setupCashuEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"detail": "proof already spent",
			"code":   409,
		})
	})

	reqBody, _ := json.Marshal(CashuConnectRequest{
		Proofs:   cashu.Proofs{{Amount: 5, Id: "ks1", Secret: "spent", C: "02abc"}},
		WGPubkey: "pk==",
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/cashu-connect", bytes.NewReader(reqBody))
	srv.handleCashuConnect(rr, req)

	if rr.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestCashuConnect_InvalidProofs(t *testing.T) {
	srv, _ := setupCashuEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"detail": "invalid proof",
			"code":   400,
		})
	})

	reqBody, _ := json.Marshal(CashuConnectRequest{
		Proofs:   cashu.Proofs{{Amount: 1, Id: "bad", Secret: "s", C: "02c"}},
		WGPubkey: "pk==",
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/cashu-connect", bytes.NewReader(reqBody))
	srv.handleCashuConnect(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestCashuConnect_MissingPubkey(t *testing.T) {
	srv, _ := setupCashuEnv(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("hub should not be called")
	})

	reqBody, _ := json.Marshal(CashuConnectRequest{
		Proofs:   cashu.Proofs{{Amount: 1, Id: "ks", Secret: "s", C: "02c"}},
		WGPubkey: "", // empty
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/cashu-connect", bytes.NewReader(reqBody))
	srv.handleCashuConnect(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestCashuConnect_NoProofs(t *testing.T) {
	srv, _ := setupCashuEnv(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("hub should not be called")
	})

	reqBody, _ := json.Marshal(CashuConnectRequest{
		Proofs:   nil,
		WGPubkey: "pk==",
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/cashu-connect", bytes.NewReader(reqBody))
	srv.handleCashuConnect(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestCashuConnect_HubDown(t *testing.T) {
	mockWG := wg.NewMockManager()
	mockQuota := quota.NewNoopEnforcer()
	mockWG.CreateInterface(wg.InterfaceConfig{
		Name: "wg-test", PrivateKey: "YPrKbTKgZ1HE/9bVKMYXEEaGFzk7Rp1G1dCfOBqvYW0=",
		ListenPort: 51820, Address: "10.100.0.1/24",
	})
	srv := NewServer(mockWG, mockQuota, "wg-test")

	// Point redeemer at a non-existent URL.
	redeemer := node.NewHubRedeemer("http://127.0.0.1:1", "node-pk")
	srv.EnableCashuGate(redeemer, "nodePub==", "10.100.0")

	reqBody, _ := json.Marshal(CashuConnectRequest{
		Proofs:   cashu.Proofs{{Amount: 5, Id: "ks", Secret: "s", C: "02c"}},
		WGPubkey: "pk==",
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/cashu-connect", bytes.NewReader(reqBody))
	srv.handleCashuConnect(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestCashuConnect_NotConfigured(t *testing.T) {
	mockWG := wg.NewMockManager()
	mockQuota := quota.NewNoopEnforcer()
	srv := NewServer(mockWG, mockQuota, "wg-test")
	// Do NOT call EnableCashuGate.

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/cashu-connect", nil)
	srv.handleCashuConnect(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rr.Code)
	}
}
