package discovery

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Radi-Labs/ARFL/internal/credentials"
	"github.com/Radi-Labs/ARFL/internal/lightning"
	"github.com/Radi-Labs/ARFL/internal/nostr"
	"github.com/Radi-Labs/ARFL/internal/store"
	"github.com/Radi-Labs/ARFL/pkg/types"
)

// DiscoveryAPI is the hub's HTTP endpoint for clients to discover nodes.
// Instead of subscribing to Nostr relays directly (which leaks client IP),
// clients call this API to get the list of verified, online nodes.
//
// CRITICAL DESIGN DECISION: We return raw signed Nostr events + attestations,
// not just NodeInfo. This lets the client verify signatures independently.
// The hub CANNOT manipulate the list without being detected.
type DiscoveryAPI struct {
	index    *NodeIndex
	hubKP    *nostr.KeyPair
	store    LeaseChecker
	earnings EarningsStore
	hubInfo  *HubInfo
	lnc      lightning.Client
	mux      *http.ServeMux

	// Rate limiting: map[IP][]timestamp of recent requests.
	rateLimit   map[string][]time.Time
	rateMu      sync.Mutex
	maxRequests int
	rateWindow  time.Duration
}

// LeaseChecker is the interface needed to verify node leases.
type LeaseChecker interface {
	IsLeaseActive(nodePubkey string) (bool, error)
}

// EarningsStore is the interface for querying node earnings and withdrawals.
type EarningsStore interface {
	GetNodeEarnings(nodePubkey string) (*store.NodeEarnings, error)
	SetNodeWallet(nodePubkey, lnAddress string) error
	GetNodeWallet(nodePubkey string) (string, error)
	InsertWithdrawal(nodePubkey string, amountSats int64) (int64, error)
	MarkWithdrawalPaid(id int64, paymentHash string) error
	MarkWithdrawalFailed(id int64, lastError string) error
	TotalWithdrawnSats(nodePubkey string) (int64, error)
}

// HubInfo is metadata about this hub, exposed via GET /info for extensions.
type HubInfo struct {
	Name         string                      `json:"name"`
	Version      string                      `json:"version"`
	NodeCount    int                         `json:"node_count"`
	HubMarginPct int                         `json:"hub_margin_pct"`
	Tiers        map[string]credentials.Tier `json:"tiers"`
}

// DiscoveryResponse is what the client receives.
// It contains the raw signed events and attestations — the client
// verifies each one before trusting the node list.
type DiscoveryResponse struct {
	Nodes     []*IndexedNode `json:"nodes"`
	Timestamp int64          `json:"timestamp"`
	Count     int            `json:"count"`
}

// NewDiscoveryAPI creates a discovery API backed by the given node index.
func NewDiscoveryAPI(index *NodeIndex) *DiscoveryAPI {
	api := &DiscoveryAPI{
		index:       index,
		mux:         http.NewServeMux(),
		rateLimit:   make(map[string][]time.Time),
		maxRequests: 30,              // 30 requests per window.
		rateWindow:  1 * time.Minute, // Per minute.
	}

	api.mux.HandleFunc("/nodes", api.handleNodes)
	api.mux.HandleFunc("/health", api.handleHealth)
	api.mux.HandleFunc("/announce", api.handleAnnounce)
	api.mux.HandleFunc("/info", api.handleInfo)
	api.mux.HandleFunc("/node/", api.handleNodeEarnings)

	return api
}

// SetHubKeyPair enables the /attest/refresh endpoint.
func (api *DiscoveryAPI) SetHubKeyPair(kp *nostr.KeyPair, leaseStore LeaseChecker) {
	api.hubKP = kp
	api.store = leaseStore
	api.mux.HandleFunc("/attest/refresh", api.handleAttestRefresh)
}

// Handler returns the HTTP handler for use with http.Server.
func (api *DiscoveryAPI) Handler() http.Handler {
	return api.mux
}

// SetHubInfo sets the hub metadata returned by GET /info.
func (api *DiscoveryAPI) SetHubInfo(info *HubInfo) {
	api.hubInfo = info
}

// SetEarningsStore enables the /node/{pubkey}/earnings endpoint.
func (api *DiscoveryAPI) SetEarningsStore(es EarningsStore) {
	api.earnings = es
}

// SetLightningClient enables withdrawal payments.
func (api *DiscoveryAPI) SetLightningClient(lnc lightning.Client) {
	api.lnc = lnc
	api.mux.HandleFunc("/node/wallet", api.handleNodeWallet)
	api.mux.HandleFunc("/node/withdraw", api.handleNodeWithdraw)
}

// handleInfo returns hub metadata for extension builders (LNbits, Layerz).
func (api *DiscoveryAPI) handleInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	info := api.hubInfo
	if info == nil {
		info = &HubInfo{
			Name:         "ARFL Hub",
			Version:      "0.1.0",
			HubMarginPct: 20,
			Tiers:        credentials.DefaultTiers,
		}
	}
	// Always reflect live node count.
	_, online := api.index.NodeCount()
	info.NodeCount = online

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// handleNodeEarnings returns earnings for a specific node.
// GET /node/{pubkey}/earnings
func (api *DiscoveryAPI) handleNodeEarnings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse: /node/{pubkey}/earnings
	path := strings.TrimPrefix(r.URL.Path, "/node/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[1] != "earnings" || len(parts[0]) != 64 {
		http.Error(w, "expected /node/{64-char-pubkey}/earnings", http.StatusBadRequest)
		return
	}
	pubkey := parts[0]

	if api.earnings == nil {
		http.Error(w, "earnings not available", http.StatusServiceUnavailable)
		return
	}

	earnings, err := api.earnings.GetNodeEarnings(pubkey)
	if err != nil {
		log.Printf("[api] earnings query for %s: %v", pubkey[:16], err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(earnings)
}

// handleNodes returns the list of online, verified nodes.
// Supports optional query parameters:
//   - role: "entry", "exit", or "both" — filter by node role
//   - min_capacity: minimum available capacity (Capacity - Load)
func (api *DiscoveryAPI) handleNodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Rate limiting — use just the IP, not the port.
	clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if clientIP == "" {
		clientIP = r.RemoteAddr
	}
	if !api.checkRateLimit(clientIP) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	// Get query filters.
	roleFilter := r.URL.Query().Get("role")

	// Fetch nodes from the index.
	var nodes []*IndexedNode
	switch roleFilter {
	case "entry":
		nodes = api.index.ListByRole(types.RoleEntry)
	case "exit":
		nodes = api.index.ListByRole(types.RoleExit)
	case "both":
		nodes = api.index.ListByRole(types.RoleBoth)
	default:
		nodes = api.index.ListOnline()
	}

	resp := DiscoveryResponse{
		Nodes:     nodes,
		Timestamp: time.Now().Unix(),
		Count:     len(nodes),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleHealth returns the hub's health status.
func (api *DiscoveryAPI) handleHealth(w http.ResponseWriter, r *http.Request) {
	total, online := api.index.NodeCount()

	resp := map[string]interface{}{
		"status": "ok",
		"nodes":  map[string]int{"total": total, "online": online},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleAnnounce accepts direct node announcements over HTTP.
// This is the reliable fallback when Nostr relays are unavailable or rate-limiting.
// The node posts its signed Nostr event directly; the hub verifies and indexes it.
func (api *DiscoveryAPI) handleAnnounce(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 8192)

	var event nostr.Event
	if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
		http.Error(w, "invalid event JSON", http.StatusBadRequest)
		return
	}

	if err := api.index.ProcessEvent(&event); err != nil {
		http.Error(w, "rejected: "+err.Error(), http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
}

// checkRateLimit implements a sliding window rate limiter.
// Returns true if the request is allowed.
func (api *DiscoveryAPI) checkRateLimit(clientIP string) bool {
	api.rateMu.Lock()
	defer api.rateMu.Unlock()

	now := time.Now()
	cutoff := now.Add(-api.rateWindow)

	// Clean old entries.
	timestamps := api.rateLimit[clientIP]
	var fresh []time.Time
	for _, ts := range timestamps {
		if ts.After(cutoff) {
			fresh = append(fresh, ts)
		}
	}

	if len(fresh) >= api.maxRequests {
		log.Printf("[discovery-api] rate limit hit for %s (%d requests in %s)",
			clientIP, len(fresh), api.rateWindow)
		return false
	}

	api.rateLimit[clientIP] = append(fresh, now)
	return true
}

// handleAttestRefresh handles POST /attest/refresh.
// A node presents its current attestation + a Schnorr signature proving
// it owns the Nostr key. The hub verifies the signature, checks the
// attestation was originally issued by itself, and returns a fresh one.
func (api *DiscoveryAPI) handleAttestRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if api.hubKP == nil {
		http.Error(w, "attestation refresh not enabled", http.StatusServiceUnavailable)
		return
	}

	var req AttestRefreshRequest
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Decode the current attestation.
	att, err := nostr.DecodeAttestation(req.Attestation)
	if err != nil {
		http.Error(w, "invalid attestation: "+err.Error(), http.StatusBadRequest)
		return
	}

	// The attestation must have been issued by THIS hub.
	// Verify the hub's Schnorr signature (proves it wasn't forged).
	// We skip expiry check — the whole point is to refresh expired attestations.
	if err := att.VerifySignature(api.hubKP.PubkeyHex()); err != nil {
		http.Error(w, "attestation verification failed: "+err.Error(), http.StatusForbidden)
		return
	}

	// Verify the node's signature on the refresh request.
	if err := nostr.VerifyRefreshRequest(att.NodePubkey, req.Attestation, req.Timestamp, req.Signature); err != nil {
		http.Error(w, "invalid signature: "+err.Error(), http.StatusForbidden)
		return
	}

	// Check if the node has an active lease.
	if api.store != nil {
		active, err := api.store.IsLeaseActive(att.NodePubkey)
		if err != nil {
			log.Printf("[attest-refresh] lease check error for %s: %v", att.NodePubkey[:16], err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !active {
			log.Printf("[attest-refresh] denied: node %s has no active lease", att.NodePubkey[:16]+"...")
			http.Error(w, "lease expired or revoked", http.StatusForbidden)
			return
		}
	}

	// Issue a fresh attestation with the same parameters.
	newAtt, err := nostr.CreateAttestation(api.hubKP, att.NodePubkey, att.NodeWGPubkey, att.OperatorID, att.AllowedRoles)
	if err != nil {
		log.Printf("[attest-refresh] create attestation error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	encoded, err := newAtt.Encode()
	if err != nil {
		log.Printf("[attest-refresh] encode attestation error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	log.Printf("[attest-refresh] refreshed attestation for node %s (role=%v, expires=%s)",
		att.NodePubkey[:16]+"...", att.AllowedRoles, time.Unix(newAtt.ExpiresAt, 0).Format(time.RFC3339))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(AttestRefreshResponse{Attestation: encoded})
}

// handleNodeWallet registers or retrieves a node's payout Lightning address.
// POST /node/wallet {"pubkey": "...", "ln_address": "user@wallet.com"}
// GET  /node/wallet?pubkey=...
func (api *DiscoveryAPI) handleNodeWallet(w http.ResponseWriter, r *http.Request) {
	if api.earnings == nil {
		http.Error(w, "not available", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		var req struct {
			Pubkey    string `json:"pubkey"`
			LNAddress string `json:"ln_address"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if len(req.Pubkey) != 64 || req.LNAddress == "" {
			http.Error(w, "pubkey (64 hex chars) and ln_address required", http.StatusBadRequest)
			return
		}
		if err := api.earnings.SetNodeWallet(req.Pubkey, req.LNAddress); err != nil {
			log.Printf("[api] set wallet for %s: %v", req.Pubkey[:16], err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	case http.MethodGet:
		pubkey := r.URL.Query().Get("pubkey")
		if len(pubkey) != 64 {
			http.Error(w, "pubkey query param required (64 hex chars)", http.StatusBadRequest)
			return
		}
		addr, err := api.earnings.GetNodeWallet(pubkey)
		if err != nil {
			http.Error(w, "wallet not registered", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"ln_address": addr})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleNodeWithdraw processes a withdrawal request from a node operator.
// POST /node/withdraw {"pubkey": "...", "amount_sats": 1000}
// Pays the node's registered Lightning address.
func (api *DiscoveryAPI) handleNodeWithdraw(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if api.earnings == nil || api.lnc == nil {
		http.Error(w, "withdrawals not available", http.StatusServiceUnavailable)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	var req struct {
		Pubkey     string `json:"pubkey"`
		AmountSats int64  `json:"amount_sats"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if len(req.Pubkey) != 64 || req.AmountSats <= 0 {
		http.Error(w, "pubkey (64 hex chars) and positive amount_sats required", http.StatusBadRequest)
		return
	}

	// Check available balance.
	earnings, err := api.earnings.GetNodeEarnings(req.Pubkey)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Also subtract any pending (unpaid) withdrawals.
	withdrawn, err := api.earnings.TotalWithdrawnSats(req.Pubkey)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	available := earnings.TotalEarnedSats - earnings.PaidSats - withdrawn
	if available < 0 {
		available = 0
	}

	if req.AmountSats > available {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":          "insufficient balance",
			"available_sats": available,
			"requested_sats": req.AmountSats,
		})
		return
	}

	// Get Lightning address.
	lnAddr, err := api.earnings.GetNodeWallet(req.Pubkey)
	if err != nil {
		http.Error(w, "no Lightning address registered — POST /node/wallet first", http.StatusBadRequest)
		return
	}

	// Create withdrawal record.
	wdID, err := api.earnings.InsertWithdrawal(req.Pubkey, req.AmountSats)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Pay via keysend to the node's pubkey (for now; Lightning address invoice fetch is v2).
	result, lnErr := api.lnc.Keysend(r.Context(), req.Pubkey, req.AmountSats)
	if lnErr != nil {
		api.earnings.MarkWithdrawalFailed(wdID, lnErr.Error())
		log.Printf("[withdraw] keysend to %s failed: %v (ln_address=%s)", req.Pubkey[:16], lnErr, lnAddr)
		http.Error(w, "payment failed: "+lnErr.Error(), http.StatusBadGateway)
		return
	}

	if result.Status == lightning.PaymentSucceeded {
		api.earnings.MarkWithdrawalPaid(wdID, result.PaymentHash)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":       "paid",
			"amount_sats":  req.AmountSats,
			"payment_hash": result.PaymentHash,
		})
	} else {
		errMsg := result.Error
		if errMsg == "" {
			errMsg = "payment did not succeed"
		}
		api.earnings.MarkWithdrawalFailed(wdID, errMsg)
		http.Error(w, "payment failed: "+errMsg, http.StatusBadGateway)
	}
}
