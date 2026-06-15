package control

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/Radi-Labs/ARFL/internal/client"
	"github.com/Radi-Labs/ARFL/internal/credentials"
	"github.com/Radi-Labs/ARFL/internal/quota"
	"github.com/Radi-Labs/ARFL/internal/wg"
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
	gate     *client.TokenGate
	ipPool   *tunnelIPPool
	wgPubkey string // This node's WireGuard public key (returned to clients).
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

	if err := s.wgMgr.RemovePeer(s.iface, pubkey); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("remove peer: %v", err))
		return
	}

	log.Printf("[admin] removed peer %s", pubkey[:16]+"...")
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
func (s *Server) EnableTokenGate(gate *client.TokenGate, wgPubkey, subnet string) {
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

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	if s.gate == nil {
		writeError(w, http.StatusServiceUnavailable, "token verification not configured")
		return
	}

	var req ConnectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.WGPubkey == "" {
		writeError(w, http.StatusBadRequest, "missing wg_pubkey")
		return
	}

	// Parse the token from the request.
	token, err := parseConnectToken(&req.Token)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid token: %v", err))
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
			writeError(w, http.StatusInternalServerError, "token verification failed")
			return
		}
	}

	if !spend.Valid {
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	}

	if !spend.FirstSpend {
		writeError(w, http.StatusConflict, "token already spent")
		return
	}

	// Token is valid and first-spend. Grant WireGuard access.
	tunnelIP := s.ipPool.Next()

	if err := s.wgMgr.AddPeer(s.iface, wg.PeerConfig{
		PublicKey:  req.WGPubkey,
		AllowedIPs: []string{tunnelIP + "/32"},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("add peer: %v", err))
		return
	}

	// Set quota to the token's bandwidth allowance.
	if err := s.quotaMgr.SetQuota(tunnelIP, spend.BytesPerToken); err != nil {
		log.Printf("[connect] warning: set quota for %s: %v", tunnelIP, err)
	}

	log.Printf("[connect] peer %s connected (ip=%s, bytes=%d)",
		req.WGPubkey[:16]+"...", tunnelIP, spend.BytesPerToken)

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

// tunnelIPPool assigns sequential IPs within a /24 subnet.
// Thread-safe for concurrent /connect requests.
type tunnelIPPool struct {
	subnet string // e.g. "10.100.0"
	next   int
	mu     sync.Mutex
}

func newTunnelIPPool(subnet string) *tunnelIPPool {
	return &tunnelIPPool{
		subnet: subnet,
		next:   2, // .1 is the node itself, start clients at .2
	}
}

func (p *tunnelIPPool) Next() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	ip := fmt.Sprintf("%s.%d", p.subnet, p.next)
	p.next++
	if p.next > 254 {
		p.next = 2 // wrap (in production, track and reclaim)
	}
	return ip
}
