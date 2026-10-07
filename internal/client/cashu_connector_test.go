package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestCashuConnectorPinnedDestinationAndRedirect(t *testing.T) {
	var hits int
	var gotHost string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		gotHost = r.Host
		http.Redirect(w, r, "http://127.0.0.1:1/cashu-connect", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")
	connectURL := "http://unresolvable.invalid:" + u.Port()
	proofs := cashu.Proofs{{Amount: 32, Id: "ks", Secret: "s", C: "02ab"}}
	_, err = NewCashuConnector().ConnectWithProofsPinned(context.Background(), connectURL, "127.0.0.1", proofs, "wgkey")
	if err == nil || !strings.Contains(err.Error(), "307") {
		t.Fatalf("redirect must be rejected without following it: %v", err)
	}
	if hits != 1 || gotHost != "unresolvable.invalid:"+u.Port() {
		t.Fatalf("pinned hits=%d, Host=%q", hits, gotHost)
	}
	_, err = NewCashuConnector().ConnectWithProofsPinned(context.Background(), connectURL, "::1", proofs, "wgkey")
	if err == nil || !strings.Contains(err.Error(), "IPv4") {
		t.Fatalf("IPv6 pin should fail: %v", err)
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

func TestNodeRejectedError_ProofsBurned(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		err    NodeRejectedError
		burned bool
	}{
		{
			name:   "already spent 409",
			err:    NodeRejectedError{StatusCode: http.StatusConflict, Message: "proofs already spent"},
			burned: true,
		},
		{
			name:   "grant peer failure 500",
			err:    NodeRejectedError{StatusCode: http.StatusInternalServerError, Message: "add peer: interface down"},
			burned: true,
		},
		{
			name:   "pool exhausted 503",
			err:    NodeRejectedError{StatusCode: http.StatusServiceUnavailable, Message: "no IPs available: exhausted"},
			burned: true,
		},
		{
			name:   "hub unavailable 503",
			err:    NodeRejectedError{StatusCode: http.StatusServiceUnavailable, Message: "hub payment system temporarily down"},
			burned: false,
		},
		{
			name:   "hub verify failed 502",
			err:    NodeRejectedError{StatusCode: http.StatusBadGateway, Message: "hub verification failed"},
			burned: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.err.ProofsBurned(); got != tc.burned {
				t.Fatalf("ProofsBurned()=%v, want %v", got, tc.burned)
			}
		})
	}
}
