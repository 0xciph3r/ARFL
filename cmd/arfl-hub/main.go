package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Radi-Labs/ARFL/internal/config"
	"github.com/Radi-Labs/ARFL/internal/discovery"
	"github.com/Radi-Labs/ARFL/internal/nostr"
	"github.com/Radi-Labs/ARFL/pkg/protocol"
)

func main() {
	cfgPath := flag.String("config", "hub.json", "path to hub config file")
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

	// Start discovery API.
	api := discovery.NewDiscoveryAPI(idx)
	server := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: api.Handler(),
	}

	go func() {
		log.Printf("[hub] discovery API listening on %s", cfg.ListenAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("discovery API: %v", err)
		}
	}()

	total, online := idx.NodeCount()
	log.Printf("[hub] ready | nodes: %d total, %d online | relays: %d",
		total, online, len(cfg.Relays))

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
