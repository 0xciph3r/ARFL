package ecash

import (
	"context"
	"fmt"
	"runtime"
	"sync"

	"github.com/elnosh/gonuts/cashu"
)

// WorkerPool bounds concurrent CPU-heavy crypto operations (blind signing,
// proof verification) to prevent resource exhaustion from parallel requests.
type WorkerPool struct {
	sem     chan struct{}
	mu      sync.RWMutex
	mint    *Mint
	pending int64
}

// NewWorkerPool creates a pool with the given concurrency limit.
// If maxWorkers <= 0, defaults to GOMAXPROCS (number of CPU cores).
func NewWorkerPool(mint *Mint, maxWorkers int) *WorkerPool {
	if maxWorkers <= 0 {
		maxWorkers = runtime.GOMAXPROCS(0)
	}
	return &WorkerPool{
		sem:  make(chan struct{}, maxWorkers),
		mint: mint,
	}
}

// Pending returns the number of in-flight crypto operations.
func (wp *WorkerPool) Pending() int64 {
	wp.mu.RLock()
	defer wp.mu.RUnlock()
	return wp.pending
}

// MaxWorkers returns the pool's concurrency limit.
func (wp *WorkerPool) MaxWorkers() int {
	return cap(wp.sem)
}

// SignBlindedMessages acquires a worker slot, then signs.
func (wp *WorkerPool) SignBlindedMessages(ctx context.Context, outputs cashu.BlindedMessages) (cashu.BlindedSignatures, error) {
	if err := wp.acquire(ctx); err != nil {
		return nil, err
	}
	defer wp.release()
	return wp.mint.SignBlindedMessages(outputs)
}

// VerifyProofs acquires a worker slot, then verifies.
func (wp *WorkerPool) VerifyProofs(ctx context.Context, proofs cashu.Proofs) ([]SpentProof, error) {
	if err := wp.acquire(ctx); err != nil {
		return nil, err
	}
	defer wp.release()
	return wp.mint.VerifyProofs(proofs)
}

// Swap acquires a worker slot, then performs the swap.
func (wp *WorkerPool) Swap(ctx context.Context, inputs cashu.Proofs, outputs cashu.BlindedMessages) (cashu.BlindedSignatures, error) {
	if err := wp.acquire(ctx); err != nil {
		return nil, err
	}
	defer wp.release()
	return wp.mint.Swap(inputs, outputs)
}

// MintTokens acquires a worker slot, then mints.
func (wp *WorkerPool) MintTokens(ctx context.Context, quoteID string, outputs cashu.BlindedMessages) (cashu.BlindedSignatures, error) {
	if err := wp.acquire(ctx); err != nil {
		return nil, err
	}
	defer wp.release()
	return wp.mint.MintTokens(quoteID, outputs)
}

// acquire blocks until a worker slot is available or context is cancelled.
func (wp *WorkerPool) acquire(ctx context.Context) error {
	select {
	case wp.sem <- struct{}{}:
		wp.mu.Lock()
		wp.pending++
		wp.mu.Unlock()
		return nil
	case <-ctx.Done():
		return fmt.Errorf("crypto worker pool full: %w", ctx.Err())
	}
}

// release returns a worker slot to the pool.
func (wp *WorkerPool) release() {
	wp.mu.Lock()
	wp.pending--
	wp.mu.Unlock()
	<-wp.sem
}
