package lightning

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// CircuitState represents the current state of the circuit breaker.
type CircuitState int32

const (
	CircuitClosed   CircuitState = iota // Normal — requests pass through
	CircuitOpen                         // Broken — requests fail fast
	CircuitHalfOpen                     // Probing — one test request allowed
)

func (s CircuitState) String() string {
	switch s {
	case CircuitClosed:
		return "closed"
	case CircuitOpen:
		return "open"
	case CircuitHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// ErrCircuitOpen is returned when the circuit breaker is open.
var ErrCircuitOpen = fmt.Errorf("circuit breaker open: lightning backend unavailable")

// CircuitBreakerConfig controls circuit breaker behavior.
type CircuitBreakerConfig struct {
	FailureThreshold int           // consecutive failures before opening (default: 3)
	OpenTimeout      time.Duration // how long to stay open before probing (default: 30s)
	ProbeInterval    time.Duration // how often to probe when open (default: 15s)
	HalfOpenMax      int           // max concurrent requests in half-open state (default: 1)
}

func defaultCBConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		FailureThreshold: 3,
		OpenTimeout:      30 * time.Second,
		ProbeInterval:    15 * time.Second,
		HalfOpenMax:      1,
	}
}

// CircuitBreaker wraps a lightning.Client with circuit breaker protection.
// When the underlying LND node becomes unreachable, it fails fast instead
// of letting every request hang until timeout.
type CircuitBreaker struct {
	inner  Client
	config CircuitBreakerConfig

	state           atomic.Int32
	mu              sync.Mutex
	failCount       int
	lastFailure     time.Time
	lastStateChange time.Time
	halfOpenCount   int

	// Self-remediation
	stopProbe chan struct{}
	probeOnce sync.Once
}

// NewCircuitBreaker wraps a lightning client with circuit breaker protection.
func NewCircuitBreaker(client Client, cfgs ...CircuitBreakerConfig) *CircuitBreaker {
	cfg := defaultCBConfig()
	if len(cfgs) > 0 {
		cfg = cfgs[0]
		if cfg.FailureThreshold <= 0 {
			cfg.FailureThreshold = 3
		}
		if cfg.OpenTimeout <= 0 {
			cfg.OpenTimeout = 30 * time.Second
		}
		if cfg.ProbeInterval <= 0 {
			cfg.ProbeInterval = 15 * time.Second
		}
		if cfg.HalfOpenMax <= 0 {
			cfg.HalfOpenMax = 1
		}
	}

	cb := &CircuitBreaker{
		inner:     client,
		config:    cfg,
		stopProbe: make(chan struct{}),
	}
	cb.state.Store(int32(CircuitClosed))
	cb.lastStateChange = time.Now()
	return cb
}

// State returns the current circuit breaker state.
func (cb *CircuitBreaker) State() CircuitState {
	return CircuitState(cb.state.Load())
}

// CreateInvoice wraps the underlying client with circuit breaker logic.
func (cb *CircuitBreaker) CreateInvoice(ctx context.Context, amountSats int64, memo string, expiry time.Duration) (*Invoice, error) {
	if err := cb.allowRequest(); err != nil {
		return nil, err
	}
	inv, err := cb.inner.CreateInvoice(ctx, amountSats, memo, expiry)
	cb.recordResult(err)
	return inv, err
}

// LookupInvoice wraps the underlying client with circuit breaker logic.
func (cb *CircuitBreaker) LookupInvoice(ctx context.Context, paymentHash string) (*Invoice, error) {
	if err := cb.allowRequest(); err != nil {
		return nil, err
	}
	inv, err := cb.inner.LookupInvoice(ctx, paymentHash)
	cb.recordResult(err)
	return inv, err
}

// SubscribeInvoices passes through without circuit breaker (long-lived stream).
func (cb *CircuitBreaker) SubscribeInvoices(ctx context.Context) (<-chan *Invoice, error) {
	return cb.inner.SubscribeInvoices(ctx)
}

// SendPayment wraps the underlying client with circuit breaker logic.
func (cb *CircuitBreaker) SendPayment(ctx context.Context, paymentRequest string, amountSats int64) (*PaymentResult, error) {
	if err := cb.allowRequest(); err != nil {
		return nil, err
	}
	res, err := cb.inner.SendPayment(ctx, paymentRequest, amountSats)
	cb.recordResult(err)
	return res, err
}

// Keysend wraps the underlying client with circuit breaker logic.
func (cb *CircuitBreaker) Keysend(ctx context.Context, destPubkey string, amountSats int64) (*PaymentResult, error) {
	if err := cb.allowRequest(); err != nil {
		return nil, err
	}
	res, err := cb.inner.Keysend(ctx, destPubkey, amountSats)
	cb.recordResult(err)
	return res, err
}

// allowRequest checks if a request should proceed or be rejected.
func (cb *CircuitBreaker) allowRequest() error {
	state := CircuitState(cb.state.Load())

	switch state {
	case CircuitClosed:
		return nil

	case CircuitOpen:
		cb.mu.Lock()
		elapsed := time.Since(cb.lastFailure)
		cb.mu.Unlock()

		// Check if enough time has passed to try half-open
		if elapsed >= cb.config.OpenTimeout {
			if cb.state.CompareAndSwap(int32(CircuitOpen), int32(CircuitHalfOpen)) {
				cb.mu.Lock()
				cb.halfOpenCount = 0
				cb.lastStateChange = time.Now()
				cb.mu.Unlock()
				log.Printf("[circuit-breaker] transitioning to half-open (probing LND)")
			}
			return nil // allow the probe request
		}
		return ErrCircuitOpen

	case CircuitHalfOpen:
		cb.mu.Lock()
		defer cb.mu.Unlock()
		if cb.halfOpenCount >= cb.config.HalfOpenMax {
			return ErrCircuitOpen // only allow limited concurrent probes
		}
		cb.halfOpenCount++
		return nil
	}

	return nil
}

// recordResult updates circuit breaker state based on call outcome.
func (cb *CircuitBreaker) recordResult(err error) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	state := CircuitState(cb.state.Load())

	if err != nil {
		cb.failCount++
		cb.lastFailure = time.Now()

		switch state {
		case CircuitClosed:
			if cb.failCount >= cb.config.FailureThreshold {
				cb.state.Store(int32(CircuitOpen))
				cb.lastStateChange = time.Now()
				log.Printf("[circuit-breaker] OPEN after %d consecutive failures — failing fast",
					cb.failCount)
				cb.startProbe()
			}
		case CircuitHalfOpen:
			// Probe failed — back to open
			cb.state.Store(int32(CircuitOpen))
			cb.lastStateChange = time.Now()
			log.Printf("[circuit-breaker] probe failed, back to OPEN")
		}
	} else {
		// Success
		switch state {
		case CircuitHalfOpen:
			// Probe succeeded — close the circuit
			cb.state.Store(int32(CircuitClosed))
			cb.failCount = 0
			cb.halfOpenCount = 0
			cb.lastStateChange = time.Now()
			log.Printf("[circuit-breaker] CLOSED — LND recovered")
			cb.stopProbing()
		case CircuitClosed:
			cb.failCount = 0
		}
	}
}

// startProbe launches the background self-remediation goroutine.
// Must be called with cb.mu held.
func (cb *CircuitBreaker) startProbe() {
	cb.probeOnce.Do(func() {
		stopCh := cb.stopProbe
		go cb.probeLoop(stopCh)
	})
}

// probeLoop periodically tests LND connectivity when circuit is open.
func (cb *CircuitBreaker) probeLoop(stopCh <-chan struct{}) {
	ticker := time.NewTicker(cb.config.ProbeInterval)
	defer ticker.Stop()

	log.Printf("[circuit-breaker] self-remediation probe started (every %s)", cb.config.ProbeInterval)

	for {
		select {
		case <-stopCh:
			log.Printf("[circuit-breaker] probe stopped — circuit recovered")
			return
		case <-ticker.C:
			state := CircuitState(cb.state.Load())
			if state == CircuitClosed {
				// Already recovered via a regular request
				return
			}

			// Try a lightweight probe: create a tiny invoice
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, err := cb.inner.CreateInvoice(ctx, 1, "circuit-breaker-probe", 60*time.Second)
			cancel()

			cb.mu.Lock()
			if err == nil {
				cb.state.Store(int32(CircuitClosed))
				cb.failCount = 0
				cb.lastStateChange = time.Now()
				cb.mu.Unlock()
				log.Printf("[circuit-breaker] CLOSED — LND recovered via probe")
				return
			}
			cb.lastFailure = time.Now()
			cb.mu.Unlock()
			log.Printf("[circuit-breaker] probe failed: %v", err)
		}
	}
}

// stopProbing signals the probe goroutine to stop.
// Must be called with cb.mu held.
func (cb *CircuitBreaker) stopProbing() {
	ch := cb.stopProbe

	select {
	case ch <- struct{}{}:
	default:
	}

	// Reset probeOnce so future opens can start a new probe.
	cb.probeOnce = sync.Once{}
	cb.stopProbe = make(chan struct{})
}

// Stop gracefully shuts down the circuit breaker.
func (cb *CircuitBreaker) Stop() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.stopProbing()
}

// Compile-time interface check.
var _ Client = (*CircuitBreaker)(nil)
