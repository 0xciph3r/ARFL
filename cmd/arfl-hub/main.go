package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
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
	// Handle subcommands before flag parsing.
	if len(os.Args) > 1 && os.Args[0] != "-" {
		switch os.Args[1] {
		case "attest":
			runAttest(os.Args[2:])
			return
		case "revoke":
			runRevoke(os.Args[2:])
			return
		case "renew":
			runRenew(os.Args[2:])
			return
		case "list-nodes":
			runListNodes(os.Args[2:])
			return
		}
	}

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
		rejectionCount := make(map[string]int)
		for {
			select {
			case <-ctx.Done():
				return
			case event := <-eventCh:
				if err := idx.ProcessEvent(event); err != nil {
					key := event.Pubkey + ":" + err.Error()
					rejectionCount[key]++
					if rejectionCount[key] <= 3 {
						log.Printf("[hub] rejected announcement from %s: %v", event.Pubkey[:16], err)
					}
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
	var lnc lightning.Client
	if cfg.LNDHost != "" && cfg.LNDPort > 0 {
		lndClient, err := lightning.NewLNDClient(lightning.LNDConfig{
			Host:         cfg.LNDHost,
			Port:         cfg.LNDPort,
			TLSCertPath:  cfg.LNDTLSCertPath,
			MacaroonPath: cfg.LNDMacaroonPath,
			FeeLimitSat:  cfg.LNDFeeLimitSat,
		})
		if err != nil {
			log.Fatalf("connect to LND: %v", err)
		}
		lnc = lndClient
		log.Printf("[hub] lightning: LND at %s:%d", cfg.LNDHost, cfg.LNDPort)
	} else {
		if !*devMode {
			log.Fatalf("LND config required (lnd_host, lnd_port, lnd_tls_cert_path, lnd_macaroon_path) — use --dev for mock")
		}
		lnc = lightning.NewMockClient()
		log.Printf("[hub] lightning: mock client (--dev mode)")
	}

	// Create payment API.
	purchaseAPI := payments.NewPurchaseAPI(db, lnc, issuer)
	if err := purchaseAPI.StartSettlementListener(ctx); err != nil {
		log.Fatalf("start settlement listener: %v", err)
	}
	defer purchaseAPI.Stop()

	// --- Phase 4: Blind Signatures ---

	// Load or generate denomination key.
	// On first run: generates a new RSA key and saves it.
	// On subsequent runs: loads from disk (keys are immutable once issued).
	blindKeyDir := cfg.BlindKeyDir
	if blindKeyDir == "" {
		blindKeyDir = "keys"
	}
	denomKey, err := loadOrGenerateDenomKey(blindKeyDir, "key-100mb", 100_000_000)
	if err != nil {
		log.Fatalf("denomination key: %v", err)
	}

	mint := credentials.NewRSABlindMint([]*credentials.DenominationKey{denomKey})
	verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{
		credentials.ExportPublicKey(denomKey),
	})
	purchaseAPI.EnableBlindSignatures(mint, verifier, denomKey.KeyID)
	log.Printf("[hub] blind signatures enabled (key=%s, denomination=%d bytes)",
		denomKey.KeyID, denomKey.BytesPerToken)

	// Export public key for nodes.
	pubKeyPath := filepath.Join(blindKeyDir, denomKey.KeyID+".pub.json")
	if err := credentials.SavePublicKey(pubKeyPath, denomKey); err != nil {
		log.Printf("[hub] warning: could not export public key: %v", err)
	} else {
		log.Printf("[hub] public key exported to %s (distribute to nodes)", pubKeyPath)
	}

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
	discoveryAPI.SetHubKeyPair(hubKP, db)
	mux.Handle("/nodes", discoveryAPI.Handler())
	mux.Handle("/health", discoveryAPI.Handler())
	mux.Handle("/announce", discoveryAPI.Handler())
	mux.Handle("/attest/", discoveryAPI.Handler())

	// Payment endpoints.
	mux.Handle("/purchase", purchaseAPI.Handler())
	mux.Handle("/purchase/", purchaseAPI.Handler())
	mux.Handle("/report", purchaseAPI.Handler())

	// Blind signature endpoints (Phase 4).
	mux.Handle("/redeem", purchaseAPI.Handler())
	mux.Handle("/spend", purchaseAPI.Handler())

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
			log.Printf("[settlement] done: %d sessions, %d entries, %d payouts (%d ok, %d failed, %d in-flight), %d sats paid",
				result.SessionsSettled, result.EntriesCreated,
				result.PayoutsSent, result.PayoutsSucceeded, result.PayoutsFailed,
				result.PayoutsInFlight, result.TotalPaidSats)

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

// loadOrGenerateDenomKey loads an RSA denomination key from disk, or generates
// one on first run. Keys are immutable — once tokens are issued with a key,
// the key MUST NOT be regenerated (it would invalidate all outstanding tokens).
func loadOrGenerateDenomKey(keyDir, keyID string, bytesPerToken int64) (*credentials.DenominationKey, error) {
	keyPath := filepath.Join(keyDir, keyID+".json")

	// Try to load existing key.
	key, err := credentials.LoadDenominationKey(keyPath)
	if err == nil {
		log.Printf("[hub] loaded denomination key %s from %s", keyID, keyPath)
		return key, nil
	}

	// Key doesn't exist — generate a new one.
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read key %s: %w", keyPath, err)
	}

	// Ensure key directory exists.
	if err := os.MkdirAll(keyDir, 0700); err != nil {
		return nil, fmt.Errorf("create key dir %s: %w", keyDir, err)
	}

	log.Printf("[hub] generating new denomination key %s (%d bytes/token)...", keyID, bytesPerToken)
	key, err = credentials.GenerateDenominationKey(keyID, bytesPerToken)
	if err != nil {
		return nil, fmt.Errorf("generate key %s: %w", keyID, err)
	}

	if err := credentials.SaveDenominationKey(keyPath, key); err != nil {
		return nil, fmt.Errorf("save key %s: %w", keyID, err)
	}

	log.Printf("[hub] denomination key %s saved to %s", keyID, keyPath)
	return key, nil
}

// runAttest generates a signed attestation for a node.
// Usage: arfl-hub attest --config hub.json --node-pubkey <hex> --node-wg-key <base64> --operator <id> --role <entry|exit> [--lease 90d]
func runAttest(args []string) {
	fs := flag.NewFlagSet("attest", flag.ExitOnError)
	cfgPath := fs.String("config", "hub.json", "path to hub config file")
	nodePubkey := fs.String("node-pubkey", "", "node's Nostr public key (64-char hex)")
	nodeWGKey := fs.String("node-wg-key", "", "node's WireGuard public key (base64)")
	operator := fs.String("operator", "", "operator identifier")
	role := fs.String("role", "", "allowed role: entry, exit, or both")
	outFile := fs.String("out", "", "write attestation to file (default: stdout)")
	leaseDur := fs.String("lease", "", "lease duration (e.g. 90d, 30d, 7d). Stores in DB and enforces on refresh")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: arfl-hub attest [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Generate a signed attestation for a node. The hub vouches that this\n")
		fmt.Fprintf(os.Stderr, "node is authorized to join the network.\n\n")
		fmt.Fprintf(os.Stderr, "Example:\n")
		fmt.Fprintf(os.Stderr, "  arfl-hub attest --config hub.json \\\n")
		fmt.Fprintf(os.Stderr, "    --node-pubkey abc123...def \\\n")
		fmt.Fprintf(os.Stderr, "    --node-wg-key YWJjZGVm... \\\n")
		fmt.Fprintf(os.Stderr, "    --operator my-org \\\n")
		fmt.Fprintf(os.Stderr, "    --role entry --lease 90d\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	if *nodePubkey == "" || *nodeWGKey == "" || *operator == "" || *role == "" {
		fs.Usage()
		fmt.Fprintf(os.Stderr, "\nError: all flags are required\n")
		os.Exit(1)
	}

	if *role != "entry" && *role != "exit" && *role != "both" {
		fmt.Fprintf(os.Stderr, "Error: --role must be entry, exit, or both\n")
		os.Exit(1)
	}

	// Load hub config for the Nostr private key.
	cfg, err := config.LoadHubConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: load config: %v\n", err)
		os.Exit(1)
	}

	hubKP, err := nostr.KeyPairFromPrivHex(cfg.NostrPrivkey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: parse hub key: %v\n", err)
		os.Exit(1)
	}

	// Determine allowed roles.
	var roles []string
	if *role == "both" {
		roles = []string{"entry", "exit"}
	} else {
		roles = []string{*role}
	}

	att, err := nostr.CreateAttestation(hubKP, *nodePubkey, *nodeWGKey, *operator, roles)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: create attestation: %v\n", err)
		os.Exit(1)
	}

	// If lease specified, store in database.
	if *leaseDur != "" {
		dur, parseErr := parseDuration(*leaseDur)
		if parseErr != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid --lease: %v\n", parseErr)
			os.Exit(1)
		}

		dbPath := resolveDBPath(*cfgPath)
		db, dbErr := store.Open(dbPath)
		if dbErr != nil {
			fmt.Fprintf(os.Stderr, "Error: open database: %v\n", dbErr)
			os.Exit(1)
		}
		defer db.Close()

		lease := store.NodeLease{
			NodePubkey:   *nodePubkey,
			NodeWGPubkey: *nodeWGKey,
			OperatorID:   *operator,
			AllowedRoles: roles,
			LeaseStart:   time.Now().UTC(),
			LeaseEnd:     time.Now().UTC().Add(dur),
		}
		if err := db.UpsertNodeLease(lease); err != nil {
			fmt.Fprintf(os.Stderr, "Error: store lease: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Lease stored: expires %s\n", lease.LeaseEnd.Format(time.RFC3339))
	}

	encoded, encErr := att.Encode()
	if encErr != nil {
		fmt.Fprintf(os.Stderr, "Error: encode attestation: %v\n", encErr)
		os.Exit(1)
	}

	if *outFile != "" {
		if err := os.WriteFile(*outFile, []byte(encoded+"\n"), 0600); err != nil {
			fmt.Fprintf(os.Stderr, "Error: write file: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Attestation written to %s\n", *outFile)
		fmt.Fprintf(os.Stderr, "Hub pubkey:  %s\n", hubKP.PubkeyHex())
		fmt.Fprintf(os.Stderr, "Node pubkey: %s\n", *nodePubkey)
		fmt.Fprintf(os.Stderr, "Role:        %s\n", *role)
		fmt.Fprintf(os.Stderr, "Expires:     %d (%s)\n", att.ExpiresAt, time.Unix(att.ExpiresAt, 0).Format(time.RFC3339))
	} else {
		fmt.Println(encoded)
	}
}

// runRevoke immediately revokes a node's lease.
func runRevoke(args []string) {
	fs := flag.NewFlagSet("revoke", flag.ExitOnError)
	cfgPath := fs.String("config", "hub.json", "path to hub config file")
	nodePubkey := fs.String("node-pubkey", "", "node's Nostr public key (64-char hex)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: arfl-hub revoke --node-pubkey <hex> [--config hub.json]\n\n")
		fmt.Fprintf(os.Stderr, "Immediately revoke a node's lease. The node will be unable to\n")
		fmt.Fprintf(os.Stderr, "refresh its attestation and will drop off the network.\n\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}
	if *nodePubkey == "" {
		fs.Usage()
		os.Exit(1)
	}

	dbPath := resolveDBPath(*cfgPath)
	db, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: open database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := db.RevokeNodeLease(*nodePubkey); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "Node %s revoked successfully\n", *nodePubkey)
}

// runRenew extends a node's lease.
func runRenew(args []string) {
	fs := flag.NewFlagSet("renew", flag.ExitOnError)
	cfgPath := fs.String("config", "hub.json", "path to hub config file")
	nodePubkey := fs.String("node-pubkey", "", "node's Nostr public key (64-char hex)")
	leaseDur := fs.String("lease", "", "new lease duration from now (e.g. 90d, 30d)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: arfl-hub renew --node-pubkey <hex> --lease 90d [--config hub.json]\n\n")
		fmt.Fprintf(os.Stderr, "Extend a node's lease. Clears any revocation.\n\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}
	if *nodePubkey == "" || *leaseDur == "" {
		fs.Usage()
		os.Exit(1)
	}

	dur, err := parseDuration(*leaseDur)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid --lease: %v\n", err)
		os.Exit(1)
	}

	dbPath := resolveDBPath(*cfgPath)
	db, dbErr := store.Open(dbPath)
	if dbErr != nil {
		fmt.Fprintf(os.Stderr, "Error: open database: %v\n", dbErr)
		os.Exit(1)
	}
	defer db.Close()

	newEnd := time.Now().UTC().Add(dur)
	if err := db.RenewNodeLease(*nodePubkey, newEnd); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "Node %s renewed until %s\n", *nodePubkey, newEnd.Format(time.RFC3339))
}

// runListNodes shows all node leases.
func runListNodes(args []string) {
	fs := flag.NewFlagSet("list-nodes", flag.ExitOnError)
	cfgPath := fs.String("config", "hub.json", "path to hub config file")
	fs.Parse(args)

	dbPath := resolveDBPath(*cfgPath)
	db, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: open database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	leases, err := db.ListNodeLeases()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if len(leases) == 0 {
		fmt.Println("No node leases found.")
		return
	}

	now := time.Now()
	fmt.Printf("%-18s %-10s %-12s %-8s %-25s %s\n",
		"NODE (first 16)", "OPERATOR", "ROLES", "STATUS", "EXPIRES", "WG KEY")
	fmt.Println("-------------------------------------------------------------------------------------------------------------------")
	for _, l := range leases {
		status := "ACTIVE"
		if l.Revoked {
			status = "REVOKED"
		} else if now.After(l.LeaseEnd) {
			status = "EXPIRED"
		}
		rolesStr := fmt.Sprintf("%v", l.AllowedRoles)
		pubShort := l.NodePubkey
		if len(pubShort) > 16 {
			pubShort = pubShort[:16] + ".."
		}
		wgShort := l.NodeWGPubkey
		if len(wgShort) > 20 {
			wgShort = wgShort[:20] + ".."
		}
		fmt.Printf("%-18s %-10s %-12s %-8s %-25s %s\n",
			pubShort, l.OperatorID, rolesStr, status,
			l.LeaseEnd.Format("2006-01-02 15:04 UTC"), wgShort)
	}
}

// parseDuration parses human-friendly durations like "90d", "30d", "7d", "24h".
func parseDuration(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("too short: %q", s)
	}
	unit := s[len(s)-1]
	numStr := s[:len(s)-1]
	var n int
	if _, err := fmt.Sscanf(numStr, "%d", &n); err != nil {
		return 0, fmt.Errorf("invalid number in %q: %w", s, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("duration must be positive: %q", s)
	}
	switch unit {
	case 'd':
		return time.Duration(n) * 24 * time.Hour, nil
	case 'h':
		return time.Duration(n) * time.Hour, nil
	default:
		return 0, fmt.Errorf("unknown unit %q in %q (use d or h)", string(unit), s)
	}
}

// resolveDBPath reads the hub config to find the database path.
// This ensures CLI subcommands use the same DB as the running hub.
func resolveDBPath(cfgPath string) string {
	cfg, err := config.LoadHubConfig(cfgPath)
	if err == nil && cfg.DBPath != "" {
		return cfg.DBPath
	}
	return filepath.Join(filepath.Dir(cfgPath), "arfl.db")
}
