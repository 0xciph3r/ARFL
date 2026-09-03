package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/elnosh/gonuts/cashu"
)

// TestCashuConnectorTargetsTheCashuGate pins the endpoint the connector calls.
//
// The node exposes two gates on the same port: /connect for the legacy RSA
// blind tokens and /cashu-connect for Cashu proofs. The connector posted to
// /connect, so proofs were decoded as an empty RSA token and every real
// connection attempt failed. Nothing caught it, because the mocks in the unit
// tests answered whatever path the connector asked for and the E2E test built
// the /cashu-connect URL by hand instead of going through the connector.
//
// This registers the two paths the way the node does and asserts which one is
// reached, so the routing is verified rather than assumed.
func TestCashuConnectorTargetsTheCashuGate(t *testing.T) {
	var gotPath string

	mux := http.NewServeMux()
	mux.HandleFunc("POST /connect", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		http.Error(w, "RSA gate: token required", http.StatusBadRequest)
	})
	mux.HandleFunc("POST /cashu-connect", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path

		var req CashuConnectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if len(req.Proofs) == 0 {
			t.Error("cashu gate received no proofs")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":         "connected",
			"tunnel_ip":      "10.100.0.5/32",
			"node_wg_pubkey": "bm9kZXB1YmtleWJhc2U2NGVuY29kZWQxMjM0NTY3OD0=",
			"bytes_allowed":  1048576,
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	cc := NewCashuConnector()
	proofs := cashu.Proofs{{
		Amount: 32,
		Id:     "00ad268c4d1f5826",
		Secret: "deadbeef",
		C:      "02abcdef",
	}}

	resp, err := cc.ConnectWithProofs(context.Background(), srv.URL, proofs, "Y2xpZW50cHVia2V5YmFzZTY0ZW5jb2RlZDEyMzQ1Njc4PQ==")
	if err != nil {
		t.Fatalf("ConnectWithProofs: %v", err)
	}

	if gotPath != "/cashu-connect" {
		t.Errorf("connector posted proofs to %q, want /cashu-connect; the RSA gate cannot read Cashu proofs", gotPath)
	}
	if resp.TunnelIP != "10.100.0.5/32" {
		t.Errorf("tunnel IP = %q, want 10.100.0.5/32", resp.TunnelIP)
	}
}

// TestCashuConnectorReportsGateErrors checks a rejection from the node is
// surfaced with its status and body, so a misrouted or refused connection is
// diagnosable rather than a bare failure.
func TestCashuConnectorReportsGateErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "proof already spent", http.StatusConflict)
	}))
	defer srv.Close()

	cc := NewCashuConnector()
	proofs := cashu.Proofs{{Amount: 32, Id: "00ad268c4d1f5826", Secret: "s", C: "02ab"}}

	_, err := cc.ConnectWithProofs(context.Background(), srv.URL, proofs, "cHVia2V5")
	if err == nil {
		t.Fatal("expected an error when the node rejects the proofs")
	}
	if !strings.Contains(err.Error(), "409") || !strings.Contains(err.Error(), "already spent") {
		t.Errorf("error %q should carry the status and the node's reason", err)
	}
}
