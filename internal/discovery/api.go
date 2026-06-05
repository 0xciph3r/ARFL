package discovery

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

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
	index *NodeIndex
	mux   *http.ServeMux

	// Rate limiting: map[IP][]timestamp of recent requests.
	rateLimit   map[string][]time.Time
	rateMu      sync.Mutex
	maxRequests int
	rateWindow  time.Duration
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

	return api
}

// Handler returns the HTTP handler for use with http.Server.
func (api *DiscoveryAPI) Handler() http.Handler {
	return api.mux
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
