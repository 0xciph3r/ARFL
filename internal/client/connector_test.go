package client

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Radi-Labs/ARFL/internal/credentials"
)

// --- Connector tests ---

func TestNodeConnector_Connect_Success(t *testing.T) {
	token := makeTestToken(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/connect" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "not found", 404)
			return
		}

		var req connectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode body: %v", err)
			http.Error(w, "bad request", 400)
			return
		}

		if req.WGPubkey != "testClientPubkey" {
			t.Errorf("expected wg_pubkey=testClientPubkey, got %s", req.WGPubkey)
		}
		if req.Token.KeyID != token.KeyID {
			t.Errorf("key_id mismatch")
		}
		if req.Token.TokenSecret != hex.EncodeToString(token.TokenSecret) {
			t.Errorf("token_secret mismatch")
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ConnectResult{
			TunnelIP:     "10.100.0.5/32",
			NodeWGPubkey: "nodePublicKey123",
			BytesAllowed: 100_000_000,
		})
	}))
	defer srv.Close()

	nc := NewNodeConnector()
	result, err := nc.Connect(context.Background(), srv.URL, token, "testClientPubkey")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if result.TunnelIP != "10.100.0.5/32" {
		t.Errorf("expected tunnel_ip=10.100.0.5/32, got %s", result.TunnelIP)
	}
	if result.NodeWGPubkey != "nodePublicKey123" {
		t.Errorf("expected node_wg_pubkey=nodePublicKey123, got %s", result.NodeWGPubkey)
	}
	if result.BytesAllowed != 100_000_000 {
		t.Errorf("expected bytes_allowed=100000000, got %d", result.BytesAllowed)
	}
}

func TestNodeConnector_Connect_TokenRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid token"})
	}))
	defer srv.Close()

	nc := NewNodeConnector()
	_, err := nc.Connect(context.Background(), srv.URL, makeTestToken(t), "pubkey")
	if err == nil {
		t.Fatal("expected error for rejected token")
	}
	if want := "node rejected token (401): invalid token"; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestNodeConnector_Connect_DoubleSpend(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"error": "token already spent"})
	}))
	defer srv.Close()

	nc := NewNodeConnector()
	_, err := nc.Connect(context.Background(), srv.URL, makeTestToken(t), "pubkey")
	if err == nil {
		t.Fatal("expected error for double-spend")
	}
}

func TestNodeConnector_Connect_ServerDown(t *testing.T) {
	nc := NewNodeConnector()
	_, err := nc.Connect(context.Background(), "http://127.0.0.1:1", makeTestToken(t), "pubkey")
	if err == nil {
		t.Fatal("expected error for unreachable server")
	}
}

func TestNodeConnector_Connect_IncompleteResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ConnectResult{
			TunnelIP:     "", // missing
			NodeWGPubkey: "abc",
		})
	}))
	defer srv.Close()

	nc := NewNodeConnector()
	_, err := nc.Connect(context.Background(), srv.URL, makeTestToken(t), "pubkey")
	if err == nil {
		t.Fatal("expected error for incomplete response")
	}
}

func TestNodeConnector_Connect_CancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Never responds
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Already cancelled

	nc := NewNodeConnector()
	_, err := nc.Connect(ctx, srv.URL, makeTestToken(t), "pubkey")
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestNodeConnector_Connect_TwoNodes_IndependentTokens(t *testing.T) {
	// Simulates connecting to entry + exit nodes with different tokens.
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ConnectResult{
			TunnelIP:     "10.100.0." + string(rune('0'+callCount)) + "/32",
			NodeWGPubkey: "nodePub" + string(rune('0'+callCount)),
			BytesAllowed: 100_000_000,
		})
	}))
	defer srv.Close()

	nc := NewNodeConnector()
	ctx := context.Background()

	token1 := makeTestToken(t)
	token2 := makeTestToken(t)

	r1, err := nc.Connect(ctx, srv.URL, token1, "clientPub")
	if err != nil {
		t.Fatalf("entry connect: %v", err)
	}
	r2, err := nc.Connect(ctx, srv.URL, token2, "clientPub")
	if err != nil {
		t.Fatalf("exit connect: %v", err)
	}

	if r1.TunnelIP == r2.TunnelIP {
		t.Error("entry and exit should get different tunnel IPs")
	}
}

// --- Helpers ---

func makeTestToken(t *testing.T) *credentials.BlindToken {
	t.Helper()
	secret, err := credentials.GenerateTokenSecret()
	if err != nil {
		t.Fatalf("generate token secret: %v", err)
	}
	return &credentials.BlindToken{
		Version:     1,
		KeyID:       "key-100mb",
		TokenSecret: secret,
		Signature:   []byte("test-sig-not-real"),
	}
}
