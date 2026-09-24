package control

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/Radi-Labs/ARFL/internal/wg"
)

const (
	// DefaultPeerIdleTimeout is how long a peer may go without a WireGuard
	// handshake before it is reaped. It matches the lifetime of the quota it
	// was granted.
	DefaultPeerIdleTimeout = 30 * time.Minute
	defaultReapInterval    = time.Minute
)

func shortKey(pubkey string) string {
	if len(pubkey) > 16 {
		return pubkey[:16] + "..."
	}
	return pubkey
}

// rejectConnect logs why a connect was refused and writes the error response.
// The node otherwise says nothing about rejected peers, which leaves no trail
// when a client reports a failure. Proofs and tokens are never logged.
func (s *Server) rejectConnect(w http.ResponseWriter, r *http.Request, gate string, status int, msg, pubkey string) {
	log.Printf("[%s] rejected connect from %s (peer=%s): %d %s", gate, r.RemoteAddr, shortKey(pubkey), status, msg)
	writeError(w, status, msg)
}

// grantPeer allocates a tunnel IP, adds the WireGuard peer and sets its quota.
// On failure it returns the HTTP status to report.
func (s *Server) grantPeer(wgPubkey string, quotaBytes int64) (string, int, error) {
	s.peerMu.Lock()
	defer s.peerMu.Unlock()

	tunnelIP, err := s.ipPool.Allocate(wgPubkey)
	if err != nil {
		return "", http.StatusServiceUnavailable, fmt.Errorf("no IPs available: %v", err)
	}

	if err := s.wgMgr.AddPeer(s.iface, wg.PeerConfig{
		PublicKey:  wgPubkey,
		AllowedIPs: []string{tunnelIP + "/32"},
	}); err != nil {
		s.ipPool.Release(tunnelIP)
		return "", http.StatusInternalServerError, fmt.Errorf("add peer: %v", err)
	}

	if err := s.quotaMgr.SetQuota(tunnelIP, quotaBytes); err != nil {
		log.Printf("[admin] warning: set quota for %s: %v", tunnelIP, err)
	}
	return tunnelIP, http.StatusOK, nil
}

// releasePeerResources frees the tunnel IP and quota held by a peer. The
// caller must hold peerMu.
func (s *Server) releasePeerResources(pubkey string) {
	if s.ipPool == nil {
		return
	}
	ip, ok := s.ipPool.ReleasePubkey(pubkey)
	if !ok {
		return
	}
	if err := s.quotaMgr.RemoveQuota(ip); err != nil {
		log.Printf("[admin] warning: remove quota for %s: %v", ip, err)
	}
}

// ReapIdle removes peers that have not completed a WireGuard handshake within
// idle, freeing their tunnel IP and quota. A peer that never handshook is
// measured from when it was granted. It returns how many peers were removed.
func (s *Server) ReapIdle(idle time.Duration) int {
	if s.ipPool == nil {
		return 0
	}

	s.peerMu.Lock()
	defer s.peerMu.Unlock()

	stats, err := s.wgMgr.GetPeerStats(s.iface)
	if err != nil {
		log.Printf("[admin] reaper: get peer stats: %v", err)
		return 0
	}
	lastHandshake := make(map[string]time.Time, len(stats))
	for _, st := range stats {
		lastHandshake[st.PublicKey] = st.LastHandshake
	}

	now := time.Now()
	reaped := 0
	for _, e := range s.ipPool.Entries() {
		lastActive := e.Since
		if hs := lastHandshake[e.Pubkey]; hs.After(lastActive) {
			lastActive = hs
		}
		if now.Sub(lastActive) < idle {
			continue
		}

		if err := s.wgMgr.RemovePeer(s.iface, e.Pubkey); err != nil {
			log.Printf("[admin] reaper: remove peer %s: %v", shortKey(e.Pubkey), err)
			continue
		}
		s.releasePeerResources(e.Pubkey)
		log.Printf("[admin] reaped idle peer %s (ip=%s)", shortKey(e.Pubkey), e.IP)
		reaped++
	}
	return reaped
}

// StartReaper periodically removes idle peers until ctx is cancelled.
func (s *Server) StartReaper(ctx context.Context, idle time.Duration) {
	if idle <= 0 {
		idle = DefaultPeerIdleTimeout
	}
	go func() {
		ticker := time.NewTicker(defaultReapInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.ReapIdle(idle)
			}
		}
	}()
}
