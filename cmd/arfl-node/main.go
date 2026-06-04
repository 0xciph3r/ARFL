package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Radi-Labs/ARFL/internal/config"
	"github.com/Radi-Labs/ARFL/internal/control"
	"github.com/Radi-Labs/ARFL/internal/quota"
	"github.com/Radi-Labs/ARFL/internal/wg"
	"github.com/Radi-Labs/ARFL/pkg/protocol"
)

func main() {
	cfgPath := flag.String("config", "node.json", "path to node config file")
	genKey := flag.Bool("genkey", false, "generate a new WireGuard keypair and exit")
	flag.Parse()

	if *genKey {
		kp, err := wg.GenerateKeyPair()
		if err != nil {
			log.Fatalf("generate keypair: %v", err)
		}
		out, _ := json.MarshalIndent(kp, "", "  ")
		fmt.Println(string(out))
		return
	}

	cfg, err := config.LoadNodeConfig(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	if cfg.MTU == 0 {
		cfg.MTU = protocol.TunnelMTU
	}
	if cfg.AdminAddr == "" {
		cfg.AdminAddr = "127.0.0.1:9090"
	}

	log.Printf("[node] starting ARFL node (role=%s, port=%d, iface=%s)",
		cfg.Role, cfg.ListenPort, cfg.Interface)

	// Create WireGuard manager
	wgMgr, err := wg.NewManager()
	if err != nil {
		log.Fatalf("create WireGuard manager: %v", err)
	}
	defer wgMgr.Close()

	// Create WireGuard interface
	if err := wgMgr.CreateInterface(wg.InterfaceConfig{
		Name:       cfg.Interface,
		PrivateKey: cfg.PrivateKey,
		ListenPort: cfg.ListenPort,
		Address:    cfg.TunnelIP,
		MTU:        cfg.MTU,
	}); err != nil {
		log.Fatalf("create WireGuard interface: %v", err)
	}
	log.Printf("[node] WireGuard interface %s created", cfg.Interface)

	// Create quota enforcer.
	// On Linux: real nftables enforcement at the kernel level.
	// On macOS: no-op (logs only) since nftables doesn't exist.
	quotaMgr := quota.NewEnforcer(cfg.Interface)
	if err := quotaMgr.Init(); err != nil {
		log.Printf("[node] warning: quota enforcer init: %v", err)
	}
	defer quotaMgr.Close()

	// Start byte counter polling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pollByteCounters(ctx, wgMgr, cfg.Interface)

	// Start admin API
	adminServer := control.NewServer(wgMgr, quotaMgr, cfg.Interface)
	go func() {
		if err := adminServer.ListenAndServe(cfg.AdminAddr); err != nil {
			log.Fatalf("admin API: %v", err)
		}
	}()

	log.Printf("[node] ready. admin API on %s", cfg.AdminAddr)

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("[node] shutting down...")
	cancel()

	// Cleanup WireGuard interface
	if err := wgMgr.DeleteInterface(cfg.Interface); err != nil {
		log.Printf("[node] warning: delete interface: %v", err)
	}
	log.Println("[node] stopped")
}

// pollByteCounters polls WireGuard byte counters for all peers every 5 seconds
// and logs them. In Phase 2+ this reports to the hub for billing.
func pollByteCounters(ctx context.Context, mgr *wg.WgctrlManager, iface string) {
	ticker := time.NewTicker(time.Duration(protocol.ByteCounterPollSeconds) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stats, err := mgr.GetPeerStats(iface)
			if err != nil {
				log.Printf("[poll] error: %v", err)
				continue
			}
			for _, s := range stats {
				if s.TotalBytes > 0 {
					log.Printf("[poll] peer=%s rx=%d tx=%d total=%d",
						s.PublicKey[:16]+"...", s.ReceiveBytes, s.TransmitBytes, s.TotalBytes)
				}
			}
		}
	}
}
