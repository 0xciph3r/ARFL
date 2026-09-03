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
	reads   int
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
	s.reads++
	s.mu.Unlock()

	time.Sleep(s.readGap)
	return was, nil
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

// TestVerifyProofsStillAllowsConcurrentReads guards against fixing the race by
// serialising all verification: checking proofs without spending them (the
// /v1/checkstate path) must not queue behind other callers' spends.
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

	began := time.Now()
	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			if _, err := m.VerifyProofs(cashu.Proofs{proof}); err != nil {
				t.Errorf("verify: %v", err)
			}
		}()
	}
	wg.Wait()

	// Serialised, this would take readers × readGap. Allow generous slack for
	// slow CI while still failing if the reads were queued.
	if limit := time.Duration(readers) * store.readGap; time.Since(began) >= limit {
		t.Errorf("%d concurrent verifications took %v, at least the %v a fully serialised path would need: read-only checks are being blocked",
			readers, time.Since(began), limit)
	}
}
