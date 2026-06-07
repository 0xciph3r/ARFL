package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Radi-Labs/ARFL/internal/config"
	"github.com/Radi-Labs/ARFL/internal/credentials"
	"github.com/Radi-Labs/ARFL/internal/discovery"
	"github.com/Radi-Labs/ARFL/internal/lightning"
	"github.com/Radi-Labs/ARFL/internal/nostr"
	"github.com/Radi-Labs/ARFL/internal/payments"
	"github.com/Radi-Labs/ARFL/internal/store"
	"github.com/Radi-Labs/ARFL/pkg/protocol"
)

func main() {
	cfgPath := flag.String("config", "hub.json", "path to hub config file")
	devMode := flag.Bool("dev", false, "development mode (insecure credential key, NOT for production)")
	flag.Parse()

	cfg, err := config.LoadHubConfig(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "0.0.0.0:8080"
	}

	// Parse hub's Nostr keypair.
	hubKP, err := nostr.KeyPairFromPrivHex(cfg.NostrPrivkey)
	if err != nil {
		log.Fatalf("parse hub Nostr key: %v", err)
	}
	log.Printf("[hub] Nostr pubkey: %s", hubKP.PubkeyHex())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --- Phase 2: Discovery ---

	// Create node index — stores all verified, online nodes.
	idx := discovery.NewNodeIndex([]string{hubKP.PubkeyHex()})

	// Connect to Nostr relays and subscribe to node announcements.
	pool := nostr.NewRelayPool(cfg.Relays)
	if err := pool.Connect(ctx); err != nil {
		log.Fatalf("connect to relays: %v", err)
	}
	defer pool.Close()

	// Subscribe to kind 30078 events (node announcements).
	eventCh, err := pool.Subscribe(ctx, "node-announcements", nostr.Filter{
		Kinds: []int{protocol.NostrKindNodeAnnouncement},
	})
	if err != nil {
		log.Fatalf("subscribe to node announcements: %v", err)
	}

	// Process incoming announcements in a goroutine.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case event := <-eventCh:
				if err := idx.ProcessEvent(event); err != nil {
					log.Printf("[hub] rejected announcement: %v", err)
				}
			}
		}
	}()

	// Run offline pruner every 60 seconds.
	go func() {
		ticker := time.NewTicker(time.Duration(protocol.PingIntervalSeconds) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pruned := idx.PruneOffline()
				if pruned > 0 {
					log.Printf("[hub] pruned %d offline node(s)", pruned)
				}
			}
		}
	}()

	// --- Phase 3: Payments ---

	// Open settlement database.
	dbPath := cfg.DBPath
	if dbPath == "" {
		dbPath = store.DefaultPath()
	}
	db, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()
	log.Printf("[hub] database: %s", dbPath)

	// Initialize credential issuer.
	credKey, err := parseCredentialKey(cfg.CredentialKey, *devMode)
	if err != nil {
		log.Fatalf("credential key: %v", err)
	}
	issuer := credentials.NewHMACIssuer("key-1", credKey)

	// Initialize Lightning client.
	// Phase 3: mock client for development. Real LND adapter in production.
	lnc := lightning.NewMockClient()
	log.Printf("[hub] lightning: mock client (development mode)")

	// Create payment API.
	purchaseAPI := payments.NewPurchaseAPI(db, lnc, issuer)
	if err := purchaseAPI.StartSettlementListener(ctx); err != nil {
		log.Fatalf("start settlement listener: %v", err)
	}
	defer purchaseAPI.Stop()

	// Create settlement engine.
	engine := payments.NewSettlementEngine(db, lnc)
	if cfg.MinPayoutSats > 0 {
		engine.SetMinPayout(cfg.MinPayoutSats)
	}

	// Run periodic settlement.
	settlementInterval := 6 * time.Hour
	if cfg.SettlementHours > 0 {
		settlementInterval = time.Duration(cfg.SettlementHours) * time.Hour
	}
	go runPeriodicSettlement(ctx, engine, settlementInterval)

	// --- HTTP Server (combined: discovery + payments) ---

	mux := http.NewServeMux()

	// Discovery endpoints.
	discoveryAPI := discovery.NewDiscoveryAPI(idx)
	mux.Handle("/nodes", discoveryAPI.Handler())
	mux.Handle("/health", discoveryAPI.Handler())

	// Payment endpoints.
	mux.Handle("/purchase", purchaseAPI.Handler())
	mux.Handle("/purchase/", purchaseAPI.Handler())
	mux.Handle("/report", purchaseAPI.Handler())

	server := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: mux,
	}

	go func() {
		log.Printf("[hub] API listening on %s", cfg.ListenAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("API server: %v", err)
		}
	}()

	total, online := idx.NodeCount()
	log.Printf("[hub] ready | nodes: %d total, %d online | relays: %d | settlement: every %s",
		total, online, len(cfg.Relays), settlementInterval)

	// Wait for shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("[hub] shutting down...")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	server.Shutdown(shutdownCtx)

	log.Println("[hub] stopped")
}

// runPeriodicSettlement runs the settlement engine on a timer.
func runPeriodicSettlement(ctx context.Context, engine *payments.SettlementEngine, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			periodEnd := t.UTC().Format(time.RFC3339)
			periodStart := t.Add(-interval).UTC().Format(time.RFC3339)

			log.Printf("[settlement] running for period %s → %s", periodStart, periodEnd)
			result, err := engine.RunSettlement(ctx, periodStart, periodEnd)
			if err != nil {
				log.Printf("[settlement] error: %v", err)
				continue
			}
			log.Printf("[settlement] done: %d sessions, %d entries, %d payouts (%d ok, %d failed), %d sats paid",
				result.SessionsSettled, result.EntriesCreated,
				result.PayoutsSent, result.PayoutsSucceeded, result.PayoutsFailed,
				result.TotalPaidSats)

			// Retry any failed payouts from previous periods.
			if ok, fail, err := engine.RetryFailedPayouts(ctx); err != nil {
				log.Printf("[settlement] retry error: %v", err)
			} else if ok > 0 || fail > 0 {
				log.Printf("[settlement] retries: %d succeeded, %d failed", ok, fail)
			}
		}
	}
}

// parseCredentialKey decodes a hex credential key.
// In development mode (--dev flag), falls back to a deterministic key.
// Without --dev, a missing key is a fatal error — prevents accidental
// deployment with a forgeable credential secret.
func parseCredentialKey(hexKey string, devMode bool) ([]byte, error) {
	if hexKey == "" {
		if !devMode {
			return nil, fmt.Errorf("credential_key is required in config (use --dev for development mode)")
		}
		log.Printf("[hub] WARNING: --dev mode, using insecure credential key — NOT FOR PRODUCTION")
		key := make([]byte, 32)
		copy(key, []byte("arfl-dev-key-not-for-production!"))
		return key, nil
	}
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, err
	}
	if len(key) < 32 {
		return nil, fmt.Errorf("credential key must be at least 32 bytes, got %d", len(key))
	}
	return key, nil
}
