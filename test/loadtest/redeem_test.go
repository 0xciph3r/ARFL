// Load test for the /v1/redeem endpoint.
//
// This test spins up a full hub HTTP server with an in-memory store,
// pre-mints a large batch of valid Cashu proofs, then hammers the
// /v1/redeem endpoint with concurrent requests to measure:
//
//   - Throughput (redeems/sec)
//   - Latency percentiles (p50, p95, p99)
//   - Error rate under pressure
//   - Worker pool saturation
//
// HOW TO RUN:
//
//	# Quick (100 requests, 10 concurrent):
//	go test -run TestLoadRedeem -v ./test/loadtest/
//
//	# Heavy (1000 requests, 50 concurrent):
//	go test -run TestLoadRedeem -v ./test/loadtest/ -count=1 \
//	  -requests=1000 -concurrency=50
//
//	# With CPU profile (combine with pprof):
//	go test -run TestLoadRedeem -v ./test/loadtest/ -cpuprofile=cpu.prof
//
// WHAT TO LOOK FOR:
//
//  1. p99 latency >> p50 → contention (worker pool or SQLite lock)
//  2. Error rate > 0% → the pool is rejecting (cryptoTimeout hit)
//  3. Throughput plateaus as concurrency grows → you found the ceiling
package loadtest

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Radi-Labs/ARFL/internal/discovery"
	"github.com/Radi-Labs/ARFL/internal/ecash"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/elnosh/gonuts/cashu"
	gcrypto "github.com/elnosh/gonuts/crypto"
)

var (
	flagRequests    = flag.Int("requests", 200, "total number of redeem requests")
	flagConcurrency = flag.Int("concurrency", 20, "number of concurrent workers")
)

// testSeed for deterministic mint keyset.
var loadTestSeed = []byte{
	0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
	0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
	0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
	0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20,
}

// TestLoadRedeem fires concurrent /v1/redeem requests at the hub.
func TestLoadRedeem(t *testing.T) {
	totalRequests := *flagRequests
	concurrency := *flagConcurrency

	t.Logf("=== Load Test: /v1/redeem ===")
	t.Logf("Requests: %d | Concurrency: %d", totalRequests, concurrency)

	// --- Setup: mint + server ---
	store := newLoadMemStore()
	mint, err := ecash.NewMint(store, loadTestSeed)
	if err != nil {
		t.Fatalf("NewMint: %v", err)
	}

	// Wire up a minimal DiscoveryAPI with the mint.
	idx := discovery.NewNodeIndex(nil)
	api := discovery.NewDiscoveryAPI(idx)
	api.SetRateLimit(0, 0) // disable rate limiting for load test
	api.SetMint(mint, store)

	server := httptest.NewServer(api.Handler())
	defer server.Close()

	redeemURL := server.URL + "/v1/redeem"

	// --- Pre-mint proofs (one unique proof per request) ---
	t.Logf("Pre-minting %d proofs...", totalRequests)
	startMint := time.Now()
	proofs := preMintProofs(t, mint, totalRequests, 64) // 64 sats each
	t.Logf("Minted %d proofs in %s", totalRequests, time.Since(startMint))

	// --- Run load test ---
	var (
		successes int64
		failures  int64
		latencies = make([]time.Duration, totalRequests)
	)

	// Semaphore for concurrency control.
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	t.Logf("Firing %d requests (%d concurrent)...", totalRequests, concurrency)
	loadStart := time.Now()

	for i := 0; i < totalRequests; i++ {
		wg.Add(1)
		sem <- struct{}{} // acquire

		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }() // release

			body := redeemBody(proofs[idx], "loadtest-node-pubkey")
			start := time.Now()

			resp, err := client.Post(redeemURL, "application/json", bytes.NewReader(body))
			elapsed := time.Since(start)
			latencies[idx] = elapsed

			if err != nil {
				atomic.AddInt64(&failures, 1)
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				atomic.AddInt64(&successes, 1)
			} else {
				atomic.AddInt64(&failures, 1)
			}
		}(i)
	}

	wg.Wait()
	totalDuration := time.Since(loadStart)

	// --- Results ---
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })

	p50 := latencies[len(latencies)*50/100]
	p95 := latencies[len(latencies)*95/100]
	p99 := latencies[len(latencies)*99/100]
	throughput := float64(totalRequests) / totalDuration.Seconds()

	t.Logf("")
	t.Logf("=== Results ===")
	t.Logf("Duration:    %s", totalDuration)
	t.Logf("Throughput:  %.1f redeems/sec", throughput)
	t.Logf("Successes:   %d (%.1f%%)", successes, float64(successes)/float64(totalRequests)*100)
	t.Logf("Failures:    %d (%.1f%%)", failures, float64(failures)/float64(totalRequests)*100)
	t.Logf("")
	t.Logf("Latency:")
	t.Logf("  p50:  %s", p50)
	t.Logf("  p95:  %s", p95)
	t.Logf("  p99:  %s", p99)
	t.Logf("  min:  %s", latencies[0])
	t.Logf("  max:  %s", latencies[len(latencies)-1])

	// Sanity checks.
	if successes == 0 {
		t.Fatal("zero successful redeems — something is broken")
	}
	errorRate := float64(failures) / float64(totalRequests)
	if errorRate > 0.05 {
		t.Errorf("error rate %.1f%% exceeds 5%% threshold", errorRate*100)
	}
}

// TestLoadRedeem_ScalingCurve runs increasing concurrency levels to find
// where throughput saturates — this shows the concurrency ceiling.
func TestLoadRedeem_ScalingCurve(t *testing.T) {
	store := newLoadMemStore()
	mint, err := ecash.NewMint(store, loadTestSeed)
	if err != nil {
		t.Fatalf("NewMint: %v", err)
	}

	idx := discovery.NewNodeIndex(nil)
	api := discovery.NewDiscoveryAPI(idx)
	api.SetRateLimit(0, 0)
	api.SetMint(mint, store)

	server := httptest.NewServer(api.Handler())
	defer server.Close()

	redeemURL := server.URL + "/v1/redeem"

	// Test at different concurrency levels.
	levels := []int{1, 5, 10, 20, 50, 100}
	requestsPerLevel := 100

	t.Logf("=== Scaling Curve: /v1/redeem ===")
	t.Logf("%-12s %-12s %-10s %-10s %-10s", "Concurrency", "Throughput", "p50", "p95", "Errors")

	for _, conc := range levels {
		// Pre-mint fresh proofs for this level.
		proofs := preMintProofs(t, mint, requestsPerLevel, 64)

		sem := make(chan struct{}, conc)
		var wg sync.WaitGroup
		var successes, failures int64
		latencies := make([]time.Duration, requestsPerLevel)

		client := &http.Client{Timeout: 30 * time.Second}
		start := time.Now()

		for i := 0; i < requestsPerLevel; i++ {
			wg.Add(1)
			sem <- struct{}{}

			go func(idx int) {
				defer wg.Done()
				defer func() { <-sem }()

				body := redeemBody(proofs[idx], "node-pubkey")
				reqStart := time.Now()

				resp, err := client.Post(redeemURL, "application/json", bytes.NewReader(body))
				latencies[idx] = time.Since(reqStart)

				if err != nil {
					atomic.AddInt64(&failures, 1)
					return
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()

				if resp.StatusCode == http.StatusOK {
					atomic.AddInt64(&successes, 1)
				} else {
					atomic.AddInt64(&failures, 1)
				}
			}(i)
		}

		wg.Wait()
		elapsed := time.Since(start)

		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		p50 := latencies[len(latencies)*50/100]
		p95 := latencies[len(latencies)*95/100]
		throughput := float64(requestsPerLevel) / elapsed.Seconds()

		t.Logf("%-12d %-12.1f %-10s %-10s %-10d",
			conc, throughput, p50, p95, failures)
	}
}

// TestLoadRedeem_DoubleSpendUnderLoad ensures the double-spend protection
// holds even when the same proof is sent concurrently by multiple goroutines.
func TestLoadRedeem_DoubleSpendUnderLoad(t *testing.T) {
	store := newLoadMemStore()
	mint, err := ecash.NewMint(store, loadTestSeed)
	if err != nil {
		t.Fatalf("NewMint: %v", err)
	}

	idx := discovery.NewNodeIndex(nil)
	api := discovery.NewDiscoveryAPI(idx)
	api.SetRateLimit(0, 0)
	api.SetMint(mint, store)

	server := httptest.NewServer(api.Handler())
	defer server.Close()

	redeemURL := server.URL + "/v1/redeem"

	// Mint ONE proof, then try to redeem it 50 times concurrently.
	proof := preMintProofs(t, mint, 1, 64)[0]
	body := redeemBody(proof, "attacker-node")

	concurrent := 50
	var wg sync.WaitGroup
	var accepted int64
	var rejected int64

	for i := 0; i < concurrent; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Post(redeemURL, "application/json", bytes.NewReader(body))
			if err != nil {
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				atomic.AddInt64(&accepted, 1)
			} else {
				atomic.AddInt64(&rejected, 1)
			}
		}()
	}

	wg.Wait()

	t.Logf("Double-spend test: %d accepted, %d rejected (of %d attempts)", accepted, rejected, concurrent)

	// Exactly 1 should succeed — the rest must be rejected.
	if accepted != 1 {
		t.Errorf("expected exactly 1 accepted, got %d (double-spend vulnerability!)", accepted)
	}
}

// =============================================================
// Helpers
// =============================================================

// preMintProofs generates N valid proofs by doing the full blind-sign-unblind cycle.
func preMintProofs(t *testing.T, mint *ecash.Mint, count int, amount uint64) []cashu.Proof {
	t.Helper()
	proofs := make([]cashu.Proof, count)
	pks := mint.PublicKeys()

	K, err := parseHexPubkey(pks[amount])
	if err != nil {
		t.Fatalf("parse K for amount %d: %v", amount, err)
	}

	for i := 0; i < count; i++ {
		r, _ := secp256k1.GeneratePrivateKey()
		secret := hex.EncodeToString(r.Serialize()[:16])

		B_, _, err := gcrypto.BlindMessage(secret, r)
		if err != nil {
			t.Fatalf("blind: %v", err)
		}

		msg := cashu.NewBlindedMessage(mint.ActiveKeysetID(), amount, B_)
		sigs, err := mint.SignBlindedMessages(cashu.BlindedMessages{msg})
		if err != nil {
			t.Fatalf("sign: %v", err)
		}

		C_, err := parseHexPubkey(sigs[0].C_)
		if err != nil {
			t.Fatalf("parse C_: %v", err)
		}

		C := gcrypto.UnblindSignature(C_, r, K)

		proofs[i] = cashu.Proof{
			Amount: amount,
			Id:     mint.ActiveKeysetID(),
			Secret: secret,
			C:      hex.EncodeToString(C.SerializeCompressed()),
		}
	}
	return proofs
}

func redeemBody(proof cashu.Proof, nodePubkey string) []byte {
	body, _ := json.Marshal(struct {
		Proofs     cashu.Proofs `json:"proofs"`
		NodePubkey string       `json:"node_pubkey"`
	}{
		Proofs:     cashu.Proofs{proof},
		NodePubkey: nodePubkey,
	})
	return body
}

func parseHexPubkey(hexStr string) (*secp256k1.PublicKey, error) {
	b, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, fmt.Errorf("hex decode: %w", err)
	}
	return secp256k1.ParsePubKey(b)
}

// loadMemStore is a thread-safe in-memory store for load testing.
type loadMemStore struct {
	mu          sync.Mutex
	keysets     map[string]*ecash.KeysetRecord
	quotes      map[string]*ecash.MintQuote
	spentProofs map[string]bool // Y-hex → spent
}

func newLoadMemStore() *loadMemStore {
	return &loadMemStore{
		keysets:     make(map[string]*ecash.KeysetRecord),
		quotes:      make(map[string]*ecash.MintQuote),
		spentProofs: make(map[string]bool),
	}
}

func (s *loadMemStore) SaveKeyset(ks *ecash.KeysetRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keysets[ks.ID] = ks
	return nil
}

func (s *loadMemStore) GetActiveKeyset() (*ecash.KeysetRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ks := range s.keysets {
		if ks.Active {
			return ks, nil
		}
	}
	return nil, nil
}

func (s *loadMemStore) GetKeyset(id string) (*ecash.KeysetRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ks, ok := s.keysets[id]
	if !ok {
		return nil, nil
	}
	return ks, nil
}

func (s *loadMemStore) GetAllKeysets() ([]*ecash.KeysetRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []*ecash.KeysetRecord
	for _, ks := range s.keysets {
		result = append(result, ks)
	}
	return result, nil
}

func (s *loadMemStore) SaveMintQuote(q *ecash.MintQuote) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quotes[q.ID] = q
	return nil
}

func (s *loadMemStore) GetMintQuote(id string) (*ecash.MintQuote, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.quotes[id]
	if !ok {
		return nil, nil
	}
	return q, nil
}

func (s *loadMemStore) UpdateMintQuoteState(id string, state ecash.QuoteState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q, ok := s.quotes[id]; ok {
		q.State = state
	}
	return nil
}

func (s *loadMemStore) TransitionMintQuoteState(id string, from, to ecash.QuoteState) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.quotes[id]
	if !ok {
		return false, ecash.ErrQuoteNotFound
	}
	if q.State != from {
		return false, nil
	}
	q.State = to
	return true, nil
}

func (s *loadMemStore) IsProofSpent(y string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spentProofs[y], nil
}

func (s *loadMemStore) SaveSpentProofs(proofs []ecash.SpentProof) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range proofs {
		s.spentProofs[p.Y] = true
	}
	return nil
}

func (s *loadMemStore) GetSpentProofsCount() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(len(s.spentProofs)), nil
}
