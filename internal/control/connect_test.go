package control

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Radi-Labs/ARFL/internal/client"
	"github.com/Radi-Labs/ARFL/internal/credentials"
	"github.com/Radi-Labs/ARFL/internal/lightning"
	"github.com/Radi-Labs/ARFL/internal/node"
	"github.com/Radi-Labs/ARFL/internal/payments"
	"github.com/Radi-Labs/ARFL/internal/quota"
	"github.com/Radi-Labs/ARFL/internal/store"
	"github.com/Radi-Labs/ARFL/internal/wg"
)

// connectEnv holds everything needed to test the /connect endpoint.
type connectEnv struct {
	srv      *Server
	mockWG   *wg.MockManager
	hub      *httptest.Server
	denomKey *credentials.DenominationKey
	mock     *lightning.MockClient
}

func setupConnectEnv(t *testing.T) *connectEnv {
	t.Helper()

	// --- Hub side ---
	dir := t.TempDir()
	db, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	lnMock := lightning.NewMockClient()
	issuer := credentials.NewHMACIssuer("key-1", []byte("test-secret-key-for-hmac-32bytes!"))
	api := payments.NewPurchaseAPI(db, lnMock, issuer)
	api.StartSettlementListener(context.Background())
	t.Cleanup(func() { api.Stop() })

	denomKey, _ := credentials.GenerateDenominationKey("key-100mb", 100_000_000)
	mint := credentials.NewRSABlindMint([]*credentials.DenominationKey{denomKey})
	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(denomKey),
	})
	api.EnableBlindSignatures(mint, verifier, "key-100mb")

	hubServer := httptest.NewServer(api.Handler())
	t.Cleanup(func() { hubServer.Close() })

	// --- Node side ---
	mockWG := wg.NewMockManager()
	mockQuota := quota.NewNoopEnforcer()
	mockWG.CreateInterface(wg.InterfaceConfig{
		Name:       "wg-test",
		PrivateKey: "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0NTY=",
		Address:    "10.100.0.1/24",
	})

	srv := NewServer(mockWG, mockQuota, "wg-test")

	nodeVerifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(denomKey),
	})
	gate := node.NewTokenGate(nodeVerifier, hubServer.URL, "test-node-pubkey")
	srv.EnableTokenGate(gate, "test-node-wg-pubkey", "10.100.0")

	return &connectEnv{
		srv:      srv,
		mockWG:   mockWG,
		hub:      hubServer,
		denomKey: denomKey,
		mock:     lnMock,
	}
}

// redeemToken creates a purchase, settles it, and redeems one token.
func (e *connectEnv) redeemToken(t *testing.T) *credentials.BlindToken {
	t.Helper()
	bwClient := client.NewBandwidthClient(e.hub.URL, e.denomKey.PublicKey, "key-100mb")

	purchase, err := bwClient.Purchase(context.Background(), "1gb")
	if err != nil {
		t.Fatalf("Purchase: %v", err)
	}

	e.mock.SimulateSettlement(purchase.PaymentHash)
	time.Sleep(200 * time.Millisecond)
	preimage := e.mock.GetPreimage(purchase.PaymentHash)

	nonce := "nonce-" + purchase.PaymentHash[:8]
	result, err := bwClient.RedeemTokens(context.Background(), preimage, 1, nonce)
	if err != nil {
		t.Fatalf("RedeemTokens: %v", err)
	}
	return result.Tokens[0]
}

func tokenToConnect(token *credentials.BlindToken) ConnectToken {
	return ConnectToken{
		Version:     token.Version,
		KeyID:       token.KeyID,
		TokenSecret: hex.EncodeToString(token.TokenSecret),
		Signature:   hex.EncodeToString(token.Signature),
	}
}

// --- Tests ---

func TestConnect_ValidToken(t *testing.T) {
	env := setupConnectEnv(t)
	token := env.redeemToken(t)

	resp := doRequest(t, env.srv, "POST", "/connect", ConnectRequest{
		Token:    tokenToConnect(token),
		WGPubkey: "dGVzdGNsaWVudHB1YmtleTEyMzQ1Njc4OTAxMjM0NTY=",
	})

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", resp.Code, resp.Body.String())
	}

	var result ConnectResponse
	json.Unmarshal(resp.Body.Bytes(), &result)

	if result.Status != "connected" {
		t.Errorf("status = %q, want connected", result.Status)
	}
	if result.TunnelIP != "10.100.0.2/32" {
		t.Errorf("tunnel_ip = %q, want 10.100.0.2/32", result.TunnelIP)
	}
	if result.BytesAllowed != 100_000_000 {
		t.Errorf("bytes_allowed = %d, want 100000000", result.BytesAllowed)
	}
	if !result.FirstSpend {
		t.Error("expected first_spend=true")
	}
	if result.NodeWGPubkey != "test-node-wg-pubkey" {
		t.Errorf("node_wg_pubkey = %q, want test-node-wg-pubkey", result.NodeWGPubkey)
	}

	// Verify WireGuard peer was actually added.
	if env.mockWG.PeerCount("wg-test") != 1 {
		t.Errorf("peer count = %d, want 1", env.mockWG.PeerCount("wg-test"))
	}
}

func TestConnect_DoubleSpend(t *testing.T) {
	env := setupConnectEnv(t)
	token := env.redeemToken(t)

	// First connect: should succeed.
	resp1 := doRequest(t, env.srv, "POST", "/connect", ConnectRequest{
		Token:    tokenToConnect(token),
		WGPubkey: "Y2xpZW50MTIzNDU2Nzg5MDEyMzQ1Njc4OTAxMjM0NTY=",
	})
	if resp1.Code != http.StatusOK {
		t.Fatalf("first connect: status = %d", resp1.Code)
	}

	// Second connect with same token: should be rejected.
	resp2 := doRequest(t, env.srv, "POST", "/connect", ConnectRequest{
		Token:    tokenToConnect(token),
		WGPubkey: "Y2xpZW50Mjc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=",
	})
	if resp2.Code != http.StatusConflict {
		t.Fatalf("double-spend: status = %d, want 409. Body: %s", resp2.Code, resp2.Body.String())
	}
}

func TestConnect_InvalidSignature(t *testing.T) {
	env := setupConnectEnv(t)

	resp := doRequest(t, env.srv, "POST", "/connect", ConnectRequest{
		Token: ConnectToken{
			Version:     credentials.BlindTokenVersion,
			KeyID:       "key-100mb",
			TokenSecret: hex.EncodeToString(make([]byte, 32)),
			Signature:   hex.EncodeToString(make([]byte, 256)),
		},
		WGPubkey: "dGVzdGNsaWVudHB1YmtleTEyMzQ1Njc4OTAxMjM0NTY=",
	})

	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("invalid sig: status = %d, want 401. Body: %s", resp.Code, resp.Body.String())
	}

	// No peer should be added.
	if env.mockWG.PeerCount("wg-test") != 0 {
		t.Error("no peer should be added for invalid token")
	}
}

func TestConnect_MissingWGPubkey(t *testing.T) {
	env := setupConnectEnv(t)
	token := env.redeemToken(t)

	resp := doRequest(t, env.srv, "POST", "/connect", ConnectRequest{
		Token:    tokenToConnect(token),
		WGPubkey: "", // missing
	})

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("missing pubkey: status = %d, want 400", resp.Code)
	}
}

func TestConnect_MissingTokenFields(t *testing.T) {
	env := setupConnectEnv(t)

	resp := doRequest(t, env.srv, "POST", "/connect", ConnectRequest{
		Token: ConnectToken{
			Version: 1,
			KeyID:   "key-100mb",
			// missing token_secret and signature
		},
		WGPubkey: "dGVzdGNsaWVudHB1YmtleTEyMzQ1Njc4OTAxMjM0NTY=",
	})

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("missing fields: status = %d, want 400", resp.Code)
	}
}

func TestConnect_NotEnabled(t *testing.T) {
	// Server without token gate should return 503.
	srv, _ := newTestServer(t)

	resp := doRequest(t, srv, "POST", "/connect", ConnectRequest{
		Token: ConnectToken{
			Version:     1,
			KeyID:       "key-100mb",
			TokenSecret: "abcd",
			Signature:   "1234",
		},
		WGPubkey: "dGVzdGNsaWVudHB1YmtleTEyMzQ1Njc4OTAxMjM0NTY=",
	})

	// Without EnableTokenGate, /connect is not registered → 405 or 404.
	if resp.Code == http.StatusOK {
		t.Fatal("should not succeed without token gate")
	}
}

func TestConnect_SequentialIPAssignment(t *testing.T) {
	env := setupConnectEnv(t)

	// Connect 3 clients with different tokens.
	expectedIPs := []string{"10.100.0.2/32", "10.100.0.3/32", "10.100.0.4/32"}

	for i := 0; i < 3; i++ {
		token := env.redeemToken(t)
		// Use unique WG pubkeys by embedding the index.
		pubkey := hex.EncodeToString(append(make([]byte, 31), byte(i+1)))

		resp := doRequest(t, env.srv, "POST", "/connect", ConnectRequest{
			Token:    tokenToConnect(token),
			WGPubkey: pubkey,
		})
		if resp.Code != http.StatusOK {
			t.Fatalf("client %d: status = %d. Body: %s", i, resp.Code, resp.Body.String())
		}

		var result ConnectResponse
		json.Unmarshal(resp.Body.Bytes(), &result)

		if result.TunnelIP != expectedIPs[i] {
			t.Errorf("client %d: tunnel_ip = %q, want %q", i, result.TunnelIP, expectedIPs[i])
		}
	}

	// All 3 peers should be added.
	if env.mockWG.PeerCount("wg-test") != 3 {
		t.Errorf("peer count = %d, want 3", env.mockWG.PeerCount("wg-test"))
	}
}

func TestConnect_IPPoolExhaustion(t *testing.T) {
	// Fill a small pool and verify exhaustion returns 503.
	pool := newTunnelIPPool("10.50.0")

	// Allocate all 253 IPs (.2 through .254).
	for i := 2; i <= 254; i++ {
		ip, err := pool.Allocate("peer-" + hex.EncodeToString([]byte{byte(i)}))
		if err != nil {
			t.Fatalf("allocate %d: %v", i, err)
		}
		expected := "10.50.0." + hex.EncodeToString([]byte{byte(i)})
		_ = expected
		if ip == "" {
			t.Fatalf("allocate %d returned empty IP", i)
		}
	}

	// 254th allocation should fail.
	_, err := pool.Allocate("overflow-peer")
	if err == nil {
		t.Fatal("expected error when pool is exhausted")
	}

	// Release one IP and verify reallocation works.
	pool.Release("10.50.0.100")
	ip, err := pool.Allocate("new-peer")
	if err != nil {
		t.Fatalf("allocate after release: %v", err)
	}
	if ip != "10.50.0.100" {
		t.Errorf("expected reuse of 10.50.0.100, got %s", ip)
	}

	if pool.Count() != 253 {
		t.Errorf("count = %d, want 253", pool.Count())
	}
}

func TestConnect_IPPoolRelease(t *testing.T) {
	pool := newTunnelIPPool("10.20.0")

	ip1, _ := pool.Allocate("peer-a")
	ip2, _ := pool.Allocate("peer-b")

	if ip1 != "10.20.0.2" || ip2 != "10.20.0.3" {
		t.Fatalf("unexpected IPs: %s, %s", ip1, ip2)
	}

	pool.Release(ip1)
	if pool.Count() != 1 {
		t.Errorf("count after release = %d, want 1", pool.Count())
	}

	// Next allocation reuses released IP.
	ip3, _ := pool.Allocate("peer-c")
	if ip3 != "10.20.0.2" {
		t.Errorf("expected reuse of 10.20.0.2, got %s", ip3)
	}
}
