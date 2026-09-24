package control

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/elnosh/gonuts/cashu"
)

func okRedeem(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "bytes_allowed": 1_000_000, "sats_redeemed": 1})
}

func connectAs(t *testing.T, srv *Server, pubkey string) int {
	t.Helper()
	body, _ := json.Marshal(CashuConnectRequest{
		Proofs:   cashu.Proofs{{Amount: 1, Id: "ks1", Secret: "s-" + pubkey, C: "02abc"}},
		WGPubkey: pubkey,
	})
	rr := httptest.NewRecorder()
	srv.handleCashuConnect(rr, httptest.NewRequest("POST", "/cashu-connect", bytes.NewReader(body)))
	return rr.Code
}

func TestIPPool_ReconnectKeepsSameIP(t *testing.T) {
	pool := newTunnelIPPool("10.30.0")
	first, _ := pool.Allocate("peer-a")
	again, _ := pool.Allocate("peer-a")
	if first != again {
		t.Errorf("reconnect got %s, want the original %s", again, first)
	}
	if pool.Count() != 1 {
		t.Errorf("count = %d, want 1", pool.Count())
	}
}

func TestIPPool_ReleasePubkey(t *testing.T) {
	pool := newTunnelIPPool("10.30.0")
	ip, _ := pool.Allocate("peer-a")

	got, ok := pool.ReleasePubkey("peer-a")
	if !ok || got != ip {
		t.Fatalf("ReleasePubkey = %q, %v; want %q, true", got, ok, ip)
	}
	if _, ok := pool.ReleasePubkey("peer-a"); ok {
		t.Error("second release should report nothing to release")
	}
	if pool.Count() != 0 {
		t.Errorf("count = %d, want 0", pool.Count())
	}
}

func TestCashuConnect_ReconnectDoesNotLeakIPs(t *testing.T) {
	srv, _ := setupCashuEnv(t, okRedeem)
	for i := 0; i < 3; i++ {
		if code := connectAs(t, srv, "same-client=="); code != http.StatusOK {
			t.Fatalf("connect %d: status %d", i, code)
		}
	}
	if srv.ipPool.Count() != 1 {
		t.Errorf("pool holds %d IPs after 3 reconnects, want 1", srv.ipPool.Count())
	}
}

func TestReapIdle_RemovesIdlePeerAndFreesIP(t *testing.T) {
	srv, mockWG := setupCashuEnv(t, okRedeem)
	connectAs(t, srv, "idle-client==")

	// The mock reports a handshake at the moment of the call, so an idle window
	// of zero treats every peer as idle.
	if n := srv.ReapIdle(0); n != 1 {
		t.Fatalf("reaped %d peers, want 1", n)
	}
	if mockWG.PeerCount("wg-test") != 0 {
		t.Errorf("peer still present after reap")
	}
	if srv.ipPool.Count() != 0 {
		t.Errorf("IP not released after reap")
	}
}

func TestReapIdle_KeepsRecentPeer(t *testing.T) {
	srv, mockWG := setupCashuEnv(t, okRedeem)
	connectAs(t, srv, "active-client==")

	if n := srv.ReapIdle(time.Hour); n != 0 {
		t.Fatalf("reaped %d peers, want 0", n)
	}
	if mockWG.PeerCount("wg-test") != 1 || srv.ipPool.Count() != 1 {
		t.Error("recent peer was removed")
	}
}

func TestRemovePeer_ReleasesIP(t *testing.T) {
	srv, mockWG := setupCashuEnv(t, okRedeem)
	connectAs(t, srv, "leaving-client==")

	rr := httptest.NewRecorder()
	srv.handleRemovePeer(rr, httptest.NewRequest("DELETE", "/peers/leaving-client==", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("remove peer: status %d: %s", rr.Code, rr.Body.String())
	}
	if mockWG.PeerCount("wg-test") != 0 || srv.ipPool.Count() != 0 {
		t.Error("peer or IP survived removal")
	}
}

func TestCashuConnect_RejectionIsLogged(t *testing.T) {
	srv, _ := setupCashuEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"detail": "proof already spent"})
	})

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	if code := connectAs(t, srv, "rejected-client=="); code != http.StatusConflict {
		t.Fatalf("status %d, want 409", code)
	}

	out := buf.String()
	if !strings.Contains(out, "[cashu-connect] rejected connect") || !strings.Contains(out, "409 proofs already spent") {
		t.Errorf("rejection not logged, got: %q", out)
	}
	if strings.Contains(out, "s-rejected-client") {
		t.Error("proof secret leaked into the log")
	}
}
