package control

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/Radi-Labs/ARFL/internal/quota"
	"github.com/Radi-Labs/ARFL/internal/wg"
)

// Server provides an HTTP admin API for the node daemon.
// It allows the hub (or arflctl in Phase 1) to add/remove peers,
// set quotas, and query byte counter stats.
type Server struct {
	wgMgr    wg.Manager
	quotaMgr quota.Enforcer
	iface    string
	mux      *http.ServeMux
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
