package store

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Radi-Labs/ARFL/internal/ecash"
)

func spentProof(y string) ecash.SpentProof {
	return ecash.SpentProof{
		Y:        y,
		KeysetID: "00ad268c4d1f5826",
		Amount:   8,
		Secret:   "deadbeef",
		C:        "02" + y,
		SpentAt:  time.Now(),
	}
}

// TestSaveSpentProofsRejectsDuplicatesAsAlreadySpent pins the last line of
// defence against double spends. The mint's in-process lock cannot serialise
// two hub instances sharing one database, so the primary key on
// cashu_spent_proofs decides the winner. The loser must be reported as an
// already-spent proof rather than a generic failure, otherwise the caller
// answers 500 and the client retries a spend that can never succeed.
func TestSaveSpentProofsRejectsDuplicatesAsAlreadySpent(t *testing.T) {
	s := testStore(t)
	p := spentProof("aa11")

	if err := s.SaveSpentProofs([]ecash.SpentProof{p}); err != nil {
		t.Fatalf("first save: %v", err)
	}

	err := s.SaveSpentProofs([]ecash.SpentProof{p})
	if !errors.Is(err, ecash.ErrProofAlreadySpent) {
		t.Fatalf("re-saving a spent proof returned %v, want ErrProofAlreadySpent so the API answers 409", err)
	}
}

// TestSaveSpentProofsIsAllOrNothing checks a batch containing one already-spent
// proof leaves no partial record behind: the caller is rejected, so none of the
// proofs may be consumed.
func TestSaveSpentProofsIsAllOrNothing(t *testing.T) {
	s := testStore(t)
	dup := spentProof("bb22")

	if err := s.SaveSpentProofs([]ecash.SpentProof{dup}); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	fresh := spentProof("cc33")
	if err := s.SaveSpentProofs([]ecash.SpentProof{fresh, dup}); !errors.Is(err, ecash.ErrProofAlreadySpent) {
		t.Fatalf("batch with a spent proof returned %v, want ErrProofAlreadySpent", err)
	}

	spent, err := s.IsProofSpent(fresh.Y)
	if err != nil {
		t.Fatalf("is spent: %v", err)
	}
	if spent {
		t.Error("a proof from a rejected batch was recorded as spent: the batch is not atomic")
	}
}

// TestSaveSpentProofsConcurrent races many writers for the same proof directly
// against the database, bypassing the mint, to confirm the schema alone admits
// exactly one winner.
func TestSaveSpentProofsConcurrent(t *testing.T) {
	s := testStore(t)
	p := spentProof("dd44")

	const writers = 16
	var (
		start    sync.WaitGroup
		done     sync.WaitGroup
		mu       sync.Mutex
		accepted int
		other    []error
	)
	start.Add(1)
	done.Add(writers)

	for i := 0; i < writers; i++ {
		go func() {
			defer done.Done()
			start.Wait()
			err := s.SaveSpentProofs([]ecash.SpentProof{p})

			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				accepted++
			case errors.Is(err, ecash.ErrProofAlreadySpent):
			default:
				other = append(other, err)
			}
		}()
	}

	start.Done()
	done.Wait()

	if accepted != 1 {
		t.Errorf("%d of %d concurrent writers recorded the same proof, want exactly 1", accepted, writers)
	}
	for _, err := range other {
		t.Errorf("concurrent writer failed with %v, want nil or ErrProofAlreadySpent", err)
	}
}
