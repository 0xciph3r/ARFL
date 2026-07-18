package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Radi-Labs/ARFL/internal/config"
	"github.com/Radi-Labs/ARFL/internal/control"
	"github.com/Radi-Labs/ARFL/internal/credentials"
	"github.com/Radi-Labs/ARFL/internal/discovery"
	"github.com/Radi-Labs/ARFL/internal/node"
	"github.com/Radi-Labs/ARFL/internal/nostr"
	"github.com/Radi-Labs/ARFL/internal/quota"
	"github.com/Radi-Labs/ARFL/internal/routing"
	"github.com/Radi-Labs/ARFL/internal/wg"
	"github.com/Radi-Labs/ARFL/pkg/protocol"
	"github.com/Radi-Labs/ARFL/pkg/types"
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

	// Parse WireGuard private key to derive public key for announcements.
	wgPrivKey, err := wg.ParseKey(cfg.PrivateKey)
	if err != nil {
		log.Fatalf("parse WireGuard key: %v", err)
	}
	wgPubKey := wgPrivKey.PublicKey()
	wgPubKeyB64 := base64.StdEncoding.EncodeToString(wgPubKey[:])

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

	// Set up IP forwarding and NAT.
	// IP forwarding: tells the kernel "it's OK to route packets between interfaces."
	// NAT: rewrites source IPs from private tunnel addresses to the node's public IP.
	// Without these, packets arrive at the WireGuard interface and die.
	outIface := cfg.OutInterface
	if outIface == "" {
		outIface = "eth0"
	}
	if runtime.GOOS == "linux" {
		if err := routing.EnableForwarding(); err != nil {
			log.Printf("[node] warning: enable forwarding: %v", err)
		}
		if err := routing.SetupNAT(cfg.Interface, outIface); err != nil {
			log.Printf("[node] warning: setup NAT: %v", err)
		}
		log.Printf("[node] IP forwarding enabled, NAT configured (%s → %s)", cfg.Interface, outIface)
	}

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
	var currentLoad int32 // Atomic counter: how many peers have active traffic.
	go pollByteCounters(ctx, wgMgr, cfg.Interface, &currentLoad)

	// Start admin API
	adminServer := control.NewServer(wgMgr, quotaMgr, cfg.Interface)

	// Wire token-gated /connect if hub_url and hub_pubkey_file are configured.
	connectAddr := cfg.ConnectAddr
	if cfg.HubURL != "" && cfg.HubPubkeyFile != "" {
		pubKey, err := credentials.LoadPublicKey(cfg.HubPubkeyFile)
		if err != nil {
			log.Fatalf("load hub public key: %v", err)
		}
		log.Printf("[node] loaded hub public key: %s (%d bytes/token)", pubKey.KeyID, pubKey.BytesPerToken)

		verifier := credentials.NewRSABlindVerifier([]*credentials.DenominationKey{pubKey})
		gate := node.NewTokenGate(verifier, cfg.HubURL, wgPubKeyB64)

		subnet := deriveTunnelSubnet(cfg.TunnelIP)
		adminServer.EnableTokenGate(gate, wgPubKeyB64, subnet)
	} else {
		log.Println("[node] RSA token gate disabled (no hub_url or hub_pubkey_file)")
	}

	// Wire Cashu-gated /cashu-connect if hub_url and nostr_privkey are configured.
	if cfg.HubURL != "" && cfg.NostrPrivkey != "" {
		nodeKPForCashu, err := nostr.KeyPairFromPrivHex(cfg.NostrPrivkey)
		if err != nil {
			log.Fatalf("parse nostr key for cashu gate: %v", err)
		}
		redeemer := node.NewHubRedeemer(cfg.HubURL, nodeKPForCashu.PubkeyHex())
		subnet := deriveTunnelSubnet(cfg.TunnelIP)
		adminServer.EnableCashuGate(redeemer, wgPubKeyB64, subnet)
	} else {
		log.Println("[node] Cashu gate disabled (no hub_url or nostr_privkey)")
	}

	// Start public-facing connect API on a separate port if configured.
	// Serves both /connect (RSA) and /cashu-connect (Cashu) on the same port.
	if connectAddr != "" {
		connectMux := http.NewServeMux()
		connectMux.HandleFunc("POST /connect", adminServer.HandleConnect)
		connectMux.HandleFunc("POST /cashu-connect", adminServer.HandleCashuConnect)
		connectMux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"status":"healthy"}`))
		})
		go func() {
			log.Printf("[node] public connect API on %s (/connect + /cashu-connect)", connectAddr)
			if err := http.ListenAndServe(connectAddr, connectMux); err != nil {
				log.Fatalf("connect API: %v", err)
			}
		}()
	}

	go func() {
		if err := adminServer.ListenAndServe(cfg.AdminAddr); err != nil {
			log.Fatalf("admin API: %v", err)
		}
	}()

	log.Printf("[node] ready. admin API on %s", cfg.AdminAddr)

	// Start Nostr announcer (Phase 2).
	// The announcer publishes "I'm alive" events to Nostr relays every 60s.
	// The hub subscribes to these events and builds a live node index.
	if cfg.NostrPrivkey != "" && len(cfg.Relays) > 0 {
		nodeKP, err := nostr.KeyPairFromPrivHex(cfg.NostrPrivkey)
		if err != nil {
			log.Fatalf("parse nostr private key: %v", err)
		}
		log.Printf("[node] Nostr pubkey: %s", nodeKP.PubkeyHex())

		// Parse the hub attestation (optional for testnet).
		var att *nostr.Attestation
		if cfg.AttestationJSON != "" {
			att, err = nostr.DecodeAttestation(cfg.AttestationJSON)
			if err != nil {
				log.Fatalf("parse attestation: %v", err)
			}
		}

		// Connect to Nostr relays.
		pool := nostr.NewRelayPool(cfg.Relays)
		if err := pool.Connect(ctx); err != nil {
			log.Printf("[node] warning: could not connect to relays: %v", err)
		} else {
			// nodeInfoFn is called every 60s to get fresh load/capacity data.
			nodeInfoFn := func() types.NodeInfo {
				info := types.NodeInfo{
					NostrPubkey:  nodeKP.PubkeyHex(),
					WGPubkey:     wgPubKeyB64,
					Endpoint:     cfg.Endpoint,
					UploadMbps:   cfg.UploadMbps,
					DownloadMbps: cfg.DownloadMbps,
					Load:         int(atomic.LoadInt32(&currentLoad)),
					Capacity:     cfg.Capacity,
					Role:         types.NodeRole(cfg.Role),
					Version:      "0.1.0",
				}
				if connectAddr != "" {
					// Derive public connect URL from the WG endpoint host + connect port.
					host := strings.Split(cfg.Endpoint, ":")[0]
					_, port, _ := strings.Cut(connectAddr, ":")
					info.ConnectURL = "http://" + host + ":" + port
				}
				return info
			}

			announcer := discovery.NewAnnouncer(nodeKP, nodeInfoFn, att, pool)
			if cfg.HubURL != "" {
				announcer.SetHubURL(cfg.HubURL)
			}
			go announcer.Run(ctx)
			defer pool.Close()
			log.Printf("[node] announcing to %d relay(s)", len(cfg.Relays))
		}
	} else {
		log.Println("[node] Nostr discovery disabled (no nostr_privkey or relays configured)")
	}

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("[node] shutting down...")
	cancel()

	// Cleanup WireGuard interface and NAT rules
	if runtime.GOOS == "linux" {
		if err := routing.CleanupNAT(cfg.Interface, outIface); err != nil {
			log.Printf("[node] warning: cleanup NAT: %v", err)
		}
	}
	if err := wgMgr.DeleteInterface(cfg.Interface); err != nil {
		log.Printf("[node] warning: delete interface: %v", err)
	}
	log.Println("[node] stopped")
}

// pollByteCounters polls WireGuard byte counters for all peers every 5 seconds
// and logs them. It also updates the atomic load counter for the announcer.
func pollByteCounters(ctx context.Context, mgr *wg.WgctrlManager, iface string, load *int32) {
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
			var activePeers int32
			for _, s := range stats {
				if s.TotalBytes > 0 {
					activePeers++
					log.Printf("[poll] peer=%s rx=%d tx=%d total=%d",
						s.PublicKey[:16]+"...", s.ReceiveBytes, s.TransmitBytes, s.TotalBytes)
				}
			}
			atomic.StoreInt32(load, activePeers)
		}
	}
}

// deriveTunnelSubnet extracts the first 3 octets from a tunnel IP.
// "10.100.0.1/24" → "10.100.0"
func deriveTunnelSubnet(tunnelIP string) string {
	// Strip CIDR prefix if present.
	ip := tunnelIP
	if idx := strings.Index(ip, "/"); idx >= 0 {
		ip = ip[:idx]
	}
	// Take first 3 octets.
	parts := strings.Split(ip, ".")
	if len(parts) >= 3 {
		return parts[0] + "." + parts[1] + "." + parts[2]
	}
	return "10.100.0" // fallback
}
