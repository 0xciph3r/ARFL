package control

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Radi-Labs/ARFL/internal/credentials"
	"github.com/Radi-Labs/ARFL/internal/node"
	"github.com/Radi-Labs/ARFL/internal/quota"
	"github.com/Radi-Labs/ARFL/internal/wg"
	"github.com/elnosh/gonuts/cashu"
)

// Server provides an HTTP admin API for the node daemon.
// It allows the hub (or arflctl in Phase 1) to add/remove peers,
// set quotas, and query byte counter stats.
//
// When a TokenGate is configured (EnableTokenGate), the server also
// exposes POST /connect for clients to present blind tokens and get
// WireGuard access.
type Server struct {
	wgMgr    wg.Manager
	quotaMgr quota.Enforcer
	iface    string
	mux      *http.ServeMux

	// Token gate (optional — set via EnableTokenGate).
	gate     *node.TokenGate
	ipPool   *tunnelIPPool
	wgPubkey string // This node's WireGuard public key (returned to clients).

	// Cashu gate (optional — set via EnableCashuGate).
	redeemer *node.HubRedeemer

	// peerMu serialises granting and removing peers so the reaper cannot tear
	// down a peer that reconnected while it was being reaped.
	peerMu sync.Mutex
}

// NewServer creates a new admin API server.
func NewServer(wgMgr wg.Manager, quotaMgr quota.Enforcer, iface string) *Server {
	s := &Server{
		wgMgr:    wgMgr,
		quotaMgr: quotaMgr,
		iface:    iface,
		mux:      http.NewServeMux(),
	}
	s.mux.HandleFunc("POST /peers", s.handleAddPeer)
	s.mux.HandleFunc("DELETE /peers/", s.handleRemovePeer)
	s.mux.HandleFunc("GET /peers", s.handleListPeers)
	s.mux.HandleFunc("POST /quota", s.handleSetQuota)
	s.mux.HandleFunc("GET /health", s.handleHealth)
	return s
}

// ListenAndServe starts the admin API server.
func (s *Server) ListenAndServe(addr string) error {
	log.Printf("[admin] listening on %s", addr)
	return http.ListenAndServe(addr, s.mux)
}

// --- Request/Response types ---

type AddPeerRequest struct {
	PublicKey  string   `json:"public_key"`
	Endpoint   string   `json:"endpoint,omitempty"`
	AllowedIPs []string `json:"allowed_ips"`
	Keepalive  int      `json:"keepalive,omitempty"`
	TunnelIP   string   `json:"tunnel_ip,omitempty"`
	QuotaBytes int64    `json:"quota_bytes,omitempty"`
}

type SetQuotaRequest struct {
	TunnelIP string `json:"tunnel_ip"`
	Bytes    int64  `json:"bytes"`
}

// --- Handlers ---

func (s *Server) handleAddPeer(w http.ResponseWriter, r *http.Request) {
	var req AddPeerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := s.wgMgr.AddPeer(s.iface, wg.PeerConfig{
		PublicKey:  req.PublicKey,
		Endpoint:   req.Endpoint,
		AllowedIPs: req.AllowedIPs,
		Keepalive:  req.Keepalive,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("add peer: %v", err))
		return
	}

	// Set initial quota slab if specified
	if req.QuotaBytes > 0 && req.TunnelIP != "" {
		if err := s.quotaMgr.SetQuota(req.TunnelIP, req.QuotaBytes); err != nil {
			log.Printf("[admin] warning: set quota for %s: %v", req.TunnelIP, err)
		}
	}

	log.Printf("[admin] added peer %s", req.PublicKey[:16]+"...")
	writeJSON(w, http.StatusCreated, map[string]string{"status": "ok"})
}

func (s *Server) handleRemovePeer(w http.ResponseWriter, r *http.Request) {
	pubkey := strings.TrimPrefix(r.URL.Path, "/peers/")
	if pubkey == "" {
		writeError(w, http.StatusBadRequest, "missing public key")
		return
	}

	s.peerMu.Lock()
	defer s.peerMu.Unlock()

	if err := s.wgMgr.RemovePeer(s.iface, pubkey); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("remove peer: %v", err))
		return
	}
	s.releasePeerResources(pubkey)

	log.Printf("[admin] removed peer %s", shortKey(pubkey))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleListPeers(w http.ResponseWriter, r *http.Request) {
	stats, err := s.wgMgr.GetPeerStats(s.iface)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("get stats: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleSetQuota(w http.ResponseWriter, r *http.Request) {
	var req SetQuotaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := s.quotaMgr.RefreshQuota(req.TunnelIP, req.Bytes); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("set quota: %v", err))
		return
	}

	log.Printf("[admin] set quota for %s: %d bytes", req.TunnelIP, req.Bytes)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// --- Token-gated connect (Phase 5) ---

// EnableTokenGate wires blind token verification into the admin server.
// Once enabled, clients can POST /connect with a blind token to get
// WireGuard access. wgPubkey is this node's WireGuard public key
// (base64), returned to clients so they can configure their tunnel.
// subnet is the tunnel subnet (e.g. "10.100.0") from which IPs are assigned.
func (s *Server) EnableTokenGate(gate *node.TokenGate, wgPubkey, subnet string) {
	s.gate = gate
	s.wgPubkey = wgPubkey
	s.ipPool = newTunnelIPPool(subnet)
	s.mux.HandleFunc("POST /connect", s.handleConnect)
	log.Printf("[admin] token-gated /connect enabled (subnet=%s.0/24)", subnet)
}

// ConnectRequest is sent by clients to request WireGuard access.
type ConnectRequest struct {
	Token    ConnectToken `json:"token"`
	WGPubkey string       `json:"wg_pubkey"` // Client's WireGuard public key (base64)
}

// ConnectToken is the blind token presented inline in the connect request.
type ConnectToken struct {
	Version     uint8  `json:"version"`
	KeyID       string `json:"key_id"`
	TokenSecret string `json:"token_secret"` // hex
	Signature   string `json:"signature"`    // hex
}

// ConnectResponse is returned on successful token verification.
type ConnectResponse struct {
	Status       string `json:"status"`         // "connected"
	TunnelIP     string `json:"tunnel_ip"`      // Assigned IP (e.g. "10.100.0.5/32")
	NodeWGPubkey string `json:"node_wg_pubkey"` // Node's WG pubkey for client config
	BytesAllowed int64  `json:"bytes_allowed"`  // Bandwidth granted by this token
	FirstSpend   bool   `json:"first_spend"`    // Whether this was a fresh token
}

// HandleConnect is the exported handler for POST /connect, used when the
// connect API is served on a separate public-facing port.
func (s *Server) HandleConnect(w http.ResponseWriter, r *http.Request) {
	s.handleConnect(w, r)
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	if s.gate == nil {
		s.rejectConnect(w, r, "connect", http.StatusServiceUnavailable, "token verification not configured", "")
		return
	}

	var req ConnectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.rejectConnect(w, r, "connect", http.StatusBadRequest, "invalid request body", req.WGPubkey)
		return
	}

	if req.WGPubkey == "" {
		s.rejectConnect(w, r, "connect", http.StatusBadRequest, "missing wg_pubkey", req.WGPubkey)
		return
	}

	// Parse the token from the request.
	token, err := parseConnectToken(&req.Token)
	if err != nil {
		s.rejectConnect(w, r, "connect", http.StatusBadRequest, fmt.Sprintf("invalid token: %v", err), req.WGPubkey)
		return
	}

	// Verify and spend the token.
	ctx := r.Context()
	spend, err := s.gate.VerifyAndSpend(ctx, token)
	if err != nil {
		// Hub unreachable — try offline verification with bounded risk.
		log.Printf("[connect] hub unreachable, falling back to VerifyOnly: %v", err)
		spend, err = s.gate.VerifyOnly(token)
		if err != nil {
			s.rejectConnect(w, r, "connect", http.StatusInternalServerError, "token verification failed", req.WGPubkey)
			return
		}
	}

	if !spend.Valid {
		s.rejectConnect(w, r, "connect", http.StatusUnauthorized, "invalid token", req.WGPubkey)
		return
	}

	if !spend.FirstSpend {
		s.rejectConnect(w, r, "connect", http.StatusConflict, "token already spent", req.WGPubkey)
		return
	}

	// Token is valid and first-spend. Grant WireGuard access.
	tunnelIP, status, err := s.grantPeer(req.WGPubkey, spend.BytesPerToken)
	if err != nil {
		s.rejectConnect(w, r, "connect", status, err.Error(), req.WGPubkey)
		return
	}

	log.Printf("[connect] peer %s connected (ip=%s, bytes=%d)",
		shortKey(req.WGPubkey), tunnelIP, spend.BytesPerToken)

	writeJSON(w, http.StatusOK, ConnectResponse{
		Status:       "connected",
		TunnelIP:     tunnelIP + "/32",
		NodeWGPubkey: s.wgPubkey,
		BytesAllowed: spend.BytesPerToken,
		FirstSpend:   spend.FirstSpend,
	})
}

func parseConnectToken(ct *ConnectToken) (*credentials.BlindToken, error) {
	if ct.TokenSecret == "" || ct.Signature == "" || ct.KeyID == "" {
		return nil, fmt.Errorf("missing required token fields")
	}

	secret, err := hex.DecodeString(ct.TokenSecret)
	if err != nil {
		return nil, fmt.Errorf("invalid token_secret hex: %w", err)
	}

	sig, err := hex.DecodeString(ct.Signature)
	if err != nil {
		return nil, fmt.Errorf("invalid signature hex: %w", err)
	}

	return &credentials.BlindToken{
		Version:     ct.Version,
		KeyID:       ct.KeyID,
		TokenSecret: secret,
		Signature:   sig,
	}, nil
}

// tunnelIPPool assigns IPs within a /24 subnet, tracking allocations
// to prevent collisions when IPs wrap or peers disconnect.
// Thread-safe for concurrent /connect requests.
type tunnelIPPool struct {
	subnet    string            // e.g. "10.100.0"
	allocated map[int]string    // IP suffix → peer pubkey
	since     map[int]time.Time // IP suffix → when it was last granted
	mu        sync.Mutex
}

// poolEntry is a snapshot of one allocation.
type poolEntry struct {
	IP     string
	Pubkey string
	Since  time.Time
}

func newTunnelIPPool(subnet string) *tunnelIPPool {
	return &tunnelIPPool{
		subnet:    subnet,
		allocated: make(map[int]string),
		since:     make(map[int]time.Time),
	}
}

// Allocate assigns an IP to a peer, returning "subnet.N". A peer that already
// holds an IP keeps it, so reconnecting does not consume a new slot each time.
// Returns an error if the pool is exhausted (253 peers).
func (p *tunnelIPPool) Allocate(peerPubkey string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i, holder := range p.allocated {
		if holder == peerPubkey {
			p.since[i] = time.Now()
			return fmt.Sprintf("%s.%d", p.subnet, i), nil
		}
	}

	// Scan .2 through .254 for an unallocated slot.
	for i := 2; i <= 254; i++ {
		if _, taken := p.allocated[i]; !taken {
			p.allocated[i] = peerPubkey
			p.since[i] = time.Now()
			return fmt.Sprintf("%s.%d", p.subnet, i), nil
		}
	}
	return "", fmt.Errorf("IP pool exhausted (253/253 allocated)")
}

// Release frees an IP allocation when a peer disconnects.
func (p *tunnelIPPool) Release(ip string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Extract the last octet.
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return
	}
	var n int
	fmt.Sscanf(parts[3], "%d", &n)
	delete(p.allocated, n)
	delete(p.since, n)
}

// ReleasePubkey frees the IP held by a peer and returns it.
func (p *tunnelIPPool) ReleasePubkey(peerPubkey string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i, holder := range p.allocated {
		if holder == peerPubkey {
			delete(p.allocated, i)
			delete(p.since, i)
			return fmt.Sprintf("%s.%d", p.subnet, i), true
		}
	}
	return "", false
}

// Entries returns a snapshot of all current allocations.
func (p *tunnelIPPool) Entries() []poolEntry {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := make([]poolEntry, 0, len(p.allocated))
	for i, holder := range p.allocated {
		out = append(out, poolEntry{
			IP:     fmt.Sprintf("%s.%d", p.subnet, i),
			Pubkey: holder,
			Since:  p.since[i],
		})
	}
	return out
}

// Count returns how many IPs are currently allocated.
func (p *tunnelIPPool) Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.allocated)
}

// --- Cashu-gated connect (Phase 10) ---

const maxCashuConnectProofs = 64

// CashuConnectRequest is sent by clients presenting Cashu proofs.
type CashuConnectRequest struct {
	Proofs   cashu.Proofs `json:"proofs"`
	WGPubkey string       `json:"wg_pubkey"`
}

// EnableCashuGate wires Cashu proof verification into the admin server.
// When enabled, clients can POST /cashu-connect with Cashu proofs to get
// WireGuard access. The node forwards proofs to the hub for verification.
//
// Both gates (RSA and Cashu) can coexist — they register on different paths.
func (s *Server) EnableCashuGate(redeemer *node.HubRedeemer, wgPubkey, subnet string) {
	s.redeemer = redeemer
	s.wgPubkey = wgPubkey
	if s.ipPool == nil {
		s.ipPool = newTunnelIPPool(subnet)
	}
	s.mux.HandleFunc("POST /cashu-connect", s.handleCashuConnect)
	log.Printf("[admin] Cashu-gated /cashu-connect enabled (subnet=%s.0/24)", subnet)
}

// HandleCashuConnect is the exported handler, used when the connect API
// is served on a separate public-facing port.
func (s *Server) HandleCashuConnect(w http.ResponseWriter, r *http.Request) {
	s.handleCashuConnect(w, r)
}

func (s *Server) handleCashuConnect(w http.ResponseWriter, r *http.Request) {
	if s.redeemer == nil {
		s.rejectConnect(w, r, "cashu-connect", http.StatusServiceUnavailable, "cashu verification not configured", "")
		return
	}

	var req CashuConnectRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&req); err != nil {
		s.rejectConnect(w, r, "cashu-connect", http.StatusBadRequest, "invalid request body", req.WGPubkey)
		return
	}

	if req.WGPubkey == "" {
		s.rejectConnect(w, r, "cashu-connect", http.StatusBadRequest, "missing wg_pubkey", req.WGPubkey)
		return
	}
	if len(req.Proofs) == 0 {
		s.rejectConnect(w, r, "cashu-connect", http.StatusBadRequest, "no proofs provided", req.WGPubkey)
		return
	}
	if len(req.Proofs) > maxCashuConnectProofs {
		s.rejectConnect(w, r, "cashu-connect", http.StatusBadRequest, fmt.Sprintf("too many proofs (max %d)", maxCashuConnectProofs), req.WGPubkey)
		return
	}

	// Forward proofs to hub for verification + spend-marking.
	result, err := s.redeemer.Redeem(r.Context(), req.Proofs)
	if err != nil {
		switch {
		case errors.Is(err, node.ErrRedeemAlreadySpent):
			s.rejectConnect(w, r, "cashu-connect", http.StatusConflict, "proofs already spent", req.WGPubkey)
		case errors.Is(err, node.ErrRedeemInvalidProof):
			s.rejectConnect(w, r, "cashu-connect", http.StatusUnauthorized, "invalid proofs", req.WGPubkey)
		case errors.Is(err, node.ErrRedeemRateLimited):
			s.rejectConnect(w, r, "cashu-connect", http.StatusTooManyRequests, "hub rate-limited — try later", req.WGPubkey)
		case errors.Is(err, node.ErrRedeemCircuitOpen):
			s.rejectConnect(w, r, "cashu-connect", http.StatusServiceUnavailable, "hub payment system temporarily down", req.WGPubkey)
		default:
			log.Printf("[cashu-connect] hub redeem error: %v", err)
			s.rejectConnect(w, r, "cashu-connect", http.StatusBadGateway, "hub verification failed", req.WGPubkey)
		}
		return
	}

	// Proofs verified and burned. Grant WireGuard access.
	tunnelIP, status, err := s.grantPeer(req.WGPubkey, result.BytesAllowed)
	if err != nil {
		s.rejectConnect(w, r, "cashu-connect", status, err.Error(), req.WGPubkey)
		return
	}

	log.Printf("[cashu-connect] peer %s connected (ip=%s, bytes=%d, sats=%d)",
		shortKey(req.WGPubkey), tunnelIP, result.BytesAllowed, result.SatsRedeemed)

	writeJSON(w, http.StatusOK, ConnectResponse{
		Status:       "connected",
		TunnelIP:     tunnelIP + "/32",
		NodeWGPubkey: s.wgPubkey,
		BytesAllowed: result.BytesAllowed,
		FirstSpend:   true,
	})
}
