package ecash

import (
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/elnosh/gonuts/cashu"
	gcrypto "github.com/elnosh/gonuts/crypto"
)

// racyStore models the weakest store the Store interface permits: it records
// spent proofs but does not reject duplicates itself, and it is slow enough to
// read that two concurrent callers will overlap.
//
// Real deployments happen to be protected by the primary key on
// cashu_spent_proofs, which hides a check-then-act race in the mint. This store
// removes that accident so the tests exercise the mint's own guarantee.
type racyStore struct {
	memStore

	mu      sync.Mutex
	spent   map[string]bool
	readGap time.Duration

	// active/peak track how many reads overlap, so concurrency can be
	// asserted directly instead of inferred from elapsed time.
	active int
	peak   int
}

func newRacyStore() *racyStore {
	return &racyStore{
		memStore: *newMemStore(),
		spent:    make(map[string]bool),
		readGap:  50 * time.Millisecond,
	}
}

// IsProofSpent reads the spent set and then stalls, widening the window between
// the check and the write. If verification and marking are not serialised, a
// second caller reads the same stale answer during the stall.
func (s *racyStore) IsProofSpent(y string) (bool, error) {
	s.mu.Lock()
	was := s.spent[y]
	s.active++
	if s.active > s.peak {
		s.peak = s.active
	}
	s.mu.Unlock()

	time.Sleep(s.readGap)

	s.mu.Lock()
	s.active--
	s.mu.Unlock()
	return was, nil
}

// peakReaders reports the greatest number of reads that were ever in flight
// at the same time.
func (s *racyStore) peakReaders() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peak
}

// SaveSpentProofs deliberately overwrites without complaint, so acceptance is
// decided solely by whatever the mint did before calling it.
func (s *racyStore) SaveSpentProofs(proofs []SpentProof) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range proofs {
		s.spent[p.Y] = true
	}
	return nil
}

func (s *racyStore) GetSpentProofsCount() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(len(s.spent)), nil
}

// GetMintQuote stalls after reading, so concurrent callers all observe the same
// state and any check-then-act on it is exposed.
func (s *racyStore) GetMintQuote(id string) (*MintQuote, error) {
	s.mu.Lock()
	q, ok := s.quotes[id]
	if !ok {
		s.mu.Unlock()
		return nil, ErrQuoteNotFound
	}
	// Copy under the lock. Reading the fields after releasing it would race
	// with a concurrent transition writing them.
	copied := *q
	s.mu.Unlock()

	time.Sleep(s.readGap)
	return &copied, nil
}

// TransitionMintQuoteState compares and sets atomically, as the contract on
// ecash.Store requires. The race must be settled by the mint using this, not by
// the store being lucky.
func (s *racyStore) TransitionMintQuoteState(id string, from, to QuoteState) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	q, ok := s.quotes[id]
	if !ok {
		return false, ErrQuoteNotFound
	}
	if q.State != from {
		return false, nil
	}
	q.State = to
	return true, nil
}

// spendRace fires n concurrent attempts to spend the same proof, released
// together, and reports how many the mint accepted.
func spendRace(t *testing.T, n int, attempt func() error) (accepted int, errs []error) {
	t.Helper()

	var (
		start sync.WaitGroup
		done  sync.WaitGroup
		mu    sync.Mutex
	)
	start.Add(1)
	done.Add(n)

	for i := 0; i < n; i++ {
		go func() {
			defer done.Done()
			start.Wait()
			err := attempt()

			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				accepted++
			} else {
				errs = append(errs, err)
			}
		}()
	}

	start.Done()
	done.Wait()
	return accepted, errs
}

// blindedOutputs builds a fresh blinded message for the given amount. Each
// concurrent swap needs its own outputs so the only thing they contend over is
// the input proof.
func blindedOutputs(m *Mint, amount uint64) (cashu.BlindedMessages, error) {
	r, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}
	secret := hex.EncodeToString(r.Serialize()[:16])

	B_, _, err := gcrypto.BlindMessage(secret, r)
	if err != nil {
		return nil, err
	}
	return cashu.BlindedMessages{cashu.NewBlindedMessage(m.ActiveKeysetID(), amount, B_)}, nil
}

// TestVerifyAndMarkSpentIsAtomic is the regression test for the double-spend
// hole: the mint used to verify a proof and mark it spent as two separate
// steps, so concurrent redemptions both passed the spent check.
//
// The store stalls for 50ms inside the spent check, far longer than the
// goroutines take to start, so without serialisation every caller is
// guaranteed to observe the proof as unspent. Exactly one must be accepted.
func TestVerifyAndMarkSpentIsAtomic(t *testing.T) {
	m, err := NewMint(newRacyStore(), testSeed)
	if err != nil {
		t.Fatalf("new mint: %v", err)
	}

	proof := blindAndSign(t, m, 8)

	const attempts = 8
	accepted, errs := spendRace(t, attempts, func() error {
		_, err := m.VerifyAndMarkSpent(cashu.Proofs{proof})
		return err
	})

	if accepted != 1 {
		t.Fatalf("the same proof was spent %d times out of %d concurrent attempts, want exactly 1: double spend", accepted, attempts)
	}
	for _, err := range errs {
		if !errors.Is(err, ErrProofAlreadySpent) {
			t.Errorf("losing attempt failed with %v, want ErrProofAlreadySpent so the caller answers 409 rather than 500", err)
		}
	}
}

// TestSwapIsAtomic covers the same race on the swap path, where a winning
// double spend would also mint a second set of signatures out of nothing.
func TestSwapIsAtomic(t *testing.T) {
	m, err := NewMint(newRacyStore(), testSeed)
	if err != nil {
		t.Fatalf("new mint: %v", err)
	}

	proof := blindAndSign(t, m, 8)

	const attempts = 8
	accepted, errs := spendRace(t, attempts, func() error {
		outputs, err := blindedOutputs(m, 8)
		if err != nil {
			return err
		}
		_, err = m.Swap(cashu.Proofs{proof}, outputs)
		return err
	})

	if accepted != 1 {
		t.Fatalf("swap accepted the same input proof %d times out of %d, want exactly 1: the mint signed outputs it was not paid for", accepted, attempts)
	}
	for _, err := range errs {
		if !errors.Is(err, ErrProofAlreadySpent) {
			t.Errorf("losing swap failed with %v, want ErrProofAlreadySpent", err)
		}
	}
}

// TestMintTokensIssuesOncePerQuote is the regression test for quote
// double-issuance: MintTokens used to read the quote state, sign, and only
// then mark it issued, so two concurrent requests for one paid invoice both
// saw QuotePaid and both received signatures — one Lightning payment, two sets
// of tokens.
//
// The store stalls inside the quote read, so without an atomic claim every
// caller is guaranteed to observe QuotePaid. Exactly one may be served.
func TestMintTokensIssuesOncePerQuote(t *testing.T) {
	store := newRacyStore()
	m, err := NewMint(store, testSeed)
	if err != nil {
		t.Fatalf("new mint: %v", err)
	}

	const quoteID = "quote-race"
	store.quotes[quoteID] = &MintQuote{
		ID:     quoteID,
		Amount: 8,
		State:  QuotePaid,
	}

	const attempts = 8
	accepted, errs := spendRace(t, attempts, func() error {
		outputs, err := blindedOutputs(m, 8)
		if err != nil {
			return err
		}
		_, err = m.MintTokens(quoteID, outputs)
		return err
	})

	if accepted != 1 {
		t.Fatalf("one paid quote issued tokens %d times out of %d concurrent attempts, want exactly 1: the mint gave away %d free token sets", accepted, attempts, accepted-1)
	}
	for _, err := range errs {
		if !errors.Is(err, ErrQuoteAlreadyUsed) {
			t.Errorf("losing attempt failed with %v, want ErrQuoteAlreadyUsed", err)
		}
	}

	q, err := store.GetMintQuote(quoteID)
	if err != nil {
		t.Fatalf("get quote: %v", err)
	}
	if q.State != QuoteIssued {
		t.Errorf("quote state = %v, want %v", q.State, QuoteIssued)
	}
}

// TestVerifyProofsStillAllowsConcurrentReads guards against fixing the race by
// serialising all verification: checking proofs without spending them (the
// /v1/checkstate path) must not queue behind other callers' spends.
//
// Concurrency is measured directly rather than by elapsed time. A wall-clock
// bound would flake on a loaded CI runner under -race, and would only show
// overlap indirectly.
func TestVerifyProofsStillAllowsConcurrentReads(t *testing.T) {
	store := newRacyStore()
	m, err := NewMint(store, testSeed)
	if err != nil {
		t.Fatalf("new mint: %v", err)
	}

	proof := blindAndSign(t, m, 8)

	const readers = 4
	var wg sync.WaitGroup
	wg.Add(readers)

	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			if _, err := m.VerifyProofs(cashu.Proofs{proof}); err != nil {
				t.Errorf("verify: %v", err)
			}
		}()
	}
	wg.Wait()

	if peak := store.peakReaders(); peak < 2 {
		t.Errorf("peak concurrent verifications was %d of %d readers: read-only checks are being serialised", peak, readers)
	}
}
