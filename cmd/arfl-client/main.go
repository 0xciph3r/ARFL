package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Radi-Labs/ARFL/internal/app"
	"github.com/Radi-Labs/ARFL/internal/client"
	"github.com/Radi-Labs/ARFL/internal/config"
	"github.com/Radi-Labs/ARFL/internal/credentials"
	"github.com/Radi-Labs/ARFL/internal/discovery"
	"github.com/Radi-Labs/ARFL/internal/tunnel"
	"github.com/Radi-Labs/ARFL/internal/wg"
	"golang.org/x/term"
)

func main() {
	sessionPath := flag.String("session", "", "path to session config file (Phase 1 static mode)")
	keyFile := flag.String("key", "client.key", "path to client WireGuard key file")
	genKey := flag.Bool("genkey", false, "generate a new WireGuard keypair and exit")
	discoverFlag := flag.String("discover", "", "hub URL for dynamic node discovery (Phase 2)")
	hubPubkeys := flag.String("hub-pubkeys", "", "comma-separated trusted hub pubkeys for verification")

	// Phase 5: Bandwidth purchase flags.
	purchaseTier := flag.String("purchase", "", "purchase bandwidth tier (1gb, 10gb, 50gb)")
	hubURL := flag.String("hub-url", "", "hub API URL for purchasing/redeeming tokens")
	hubKeyFile := flag.String("hub-key", "", "path to hub's blind signature public key file")
	tokenFile := flag.String("tokens", "tokens.json", "path to save/load bandwidth tokens")
	tokenCount := flag.Int("token-count", 0, "how many tokens to redeem (default: all)")
	flag.Parse()

	if *genKey {
		kp, err := wg.GenerateKeyPair()
		if err != nil {
			log.Fatalf("generate keypair: %v", err)
		}

		// Prompt for passphrase to encrypt the private key
		passphrase := promptPassphrase("Set a passphrase to protect your key: ")
		confirm := promptPassphrase("Confirm passphrase: ")
		if passphrase != confirm {
			log.Fatalf("passphrases do not match")
		}

		if err := wg.SaveKeyPairEncrypted(*keyFile, kp, passphrase); err != nil {
			log.Fatalf("save key file: %v", err)
		}
		fmt.Printf("Encrypted keypair written to %s\n", *keyFile)
		fmt.Printf("Public key: %s\n", kp.PublicKey)
		fmt.Println("⚠ If you lose this passphrase, your bandwidth balance is irrecoverable.")
		return
	}

	// --- Phase 5: Purchase bandwidth tokens ---
	if *purchaseTier != "" {
		if *hubURL == "" || *hubKeyFile == "" {
			log.Fatalf("--hub-url and --hub-key are required for --purchase")
		}
		runPurchaseFlow(*hubURL, *hubKeyFile, *purchaseTier, *tokenFile, *tokenCount)
		return
	}

	// Load client key — requires passphrase to decrypt
	passphrase := promptPassphrase("Passphrase: ")
	kp, err := wg.LoadKeyPairEncrypted(*keyFile, passphrase)
	if err != nil {
		log.Fatalf("load key: %v", err)
	}
	log.Printf("[client] public key: %s", kp.PublicKey)

	// Resolve session: either static file (Phase 1) or dynamic discovery (Phase 2).
	var session *config.SessionFile

	if *discoverFlag != "" {
		// Phase 2: Dynamic discovery via hub API.
		if *hubPubkeys == "" {
			log.Fatalf("--hub-pubkeys required when using --discover")
		}
		trustedPubkeys := strings.Split(*hubPubkeys, ",")
		log.Printf("[client] discovering nodes from %s...", *discoverFlag)

		selector := discovery.NewNodeSelector(*discoverFlag, trustedPubkeys)
		pair, err := selector.SelectPair()
		if err != nil {
			log.Fatalf("node discovery failed: %v", err)
		}

		log.Printf("[client] selected entry: %s (operator=%s)",
			pair.Entry.Info.Endpoint, pair.Entry.Attestation.OperatorID)
		log.Printf("[client] selected exit:  %s (operator=%s)",
			pair.Exit.Info.Endpoint, pair.Exit.Attestation.OperatorID)

		session = &config.SessionFile{
			EntryEndpoint:   pair.Entry.Info.Endpoint,
			EntryWGPubkey:   pair.Entry.Info.WGPubkey,
			EntryConnectURL: pair.Entry.Info.ConnectURL,
			ExitEndpoint:    pair.Exit.Info.Endpoint,
			ExitWGPubkey:    pair.Exit.Info.WGPubkey,
			ExitConnectURL:  pair.Exit.Info.ConnectURL,
			OuterTunnelIP:   "10.100.0.2/24",
			InnerTunnelIP:   "10.200.0.2/24",
		}
	} else if *sessionPath != "" {
		// Phase 1: Static session file.
		session, err = config.LoadSessionFile(*sessionPath)
		if err != nil {
			log.Fatalf("load session: %v", err)
		}
	} else {
		log.Fatalf("provide either --session <file> or --discover <hub-url>")
	}

	// --- Phase 6: Present tokens to nodes before creating tunnels ---
	// If we have tokens and both nodes have connect URLs, present tokens
	// to get authorized WireGuard access. This is the full privacy flow:
	// the nodes never see who purchased the tokens.

	var spentTokenCount int // Track how many tokens were spent (for deferred store update).
	if session.EntryConnectURL != "" && session.ExitConnectURL != "" {
		tokens, err := loadTokens(*tokenFile)
		if err != nil {
			log.Fatalf("load tokens from %s: %v (run --purchase first)", *tokenFile, err)
		}
		if len(tokens) < 2 {
			log.Fatalf("need at least 2 tokens (have %d) — one for entry, one for exit", len(tokens))
		}

		connector := client.NewNodeConnector()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

		// Connect to entry node with first token.
		log.Printf("[client] presenting token to entry node %s...", session.EntryConnectURL)
		entryResult, err := connector.Connect(ctx, session.EntryConnectURL, tokens[0], kp.PublicKey)
		if err != nil {
			cancel()
			log.Fatalf("entry node connect: %v", err)
		}
		log.Printf("[client] entry node: assigned IP %s, quota %d MB",
			entryResult.TunnelIP, entryResult.BytesAllowed/1_000_000)

		// Connect to exit node with second token.
		log.Printf("[client] presenting token to exit node %s...", session.ExitConnectURL)
		exitResult, err := connector.Connect(ctx, session.ExitConnectURL, tokens[1], kp.PublicKey)
		if err != nil {
			cancel()
			log.Fatalf("exit node connect: %v", err)
		}
		log.Printf("[client] exit node: assigned IP %s, quota %d MB",
			exitResult.TunnelIP, exitResult.BytesAllowed/1_000_000)
		cancel()

		// Use node-assigned IPs and pubkeys instead of static config.
		session.OuterTunnelIP = entryResult.TunnelIP
		session.InnerTunnelIP = exitResult.TunnelIP
		session.EntryWGPubkey = entryResult.NodeWGPubkey
		session.ExitWGPubkey = exitResult.NodeWGPubkey
		spentTokenCount = 2

		// NOTE: Token store update is deferred until AFTER tunnel creation
		// succeeds. If WG setup fails, tokens stay in the store so the user
		// doesn't lose paid bandwidth.
	} else if session.EntryConnectURL == "" && session.ExitConnectURL == "" {
		// No connect URLs — legacy static session mode (Phase 1).
		// Tunnel IPs must be in the session file.
		if session.OuterTunnelIP == "" || session.InnerTunnelIP == "" {
			log.Fatalf("session file missing tunnel IPs and no connect URLs — cannot proceed")
		}
	} else {
		log.Fatalf("both entry and exit nodes must have connect URLs (got entry=%q, exit=%q)",
			session.EntryConnectURL, session.ExitConnectURL)
	}

	tun, err := tunnel.New()
	if err != nil {
		log.Fatalf("create tunnel manager: %v", err)
	}
	defer tun.Close()

	// cmd/arfl-client uses a persisted client key. Keep both hops on that key
	// so legacy static-session flows continue to work while this command still
	// uses the old connector stack.
	if err := tun.SetHopKeys(kp, kp); err != nil {
		log.Fatalf("set tunnel keys: %v", err)
	}
	if err := tun.Preflight(); err != nil {
		log.Fatalf("tunnel preflight: %v", err)
	}
	if err := tun.ValidateEndpoints(session.EntryEndpoint, session.ExitEndpoint); err != nil {
		log.Fatalf("validate tunnel endpoints: %v", err)
	}

	cfg := app.TunnelConfig{
		ClientKey:      kp.PublicKey,
		EntryClientKey: kp.PublicKey,
		ExitClientKey:  kp.PublicKey,
		Entry: app.HopConfig{
			NodeID:       "entry",
			Endpoint:     session.EntryEndpoint,
			NodeWGPubkey: session.EntryWGPubkey,
			TunnelIP:     session.OuterTunnelIP,
		},
		Exit: app.HopConfig{
			NodeID:       "exit",
			Endpoint:     session.ExitEndpoint,
			NodeWGPubkey: session.ExitWGPubkey,
			TunnelIP:     session.InnerTunnelIP,
		},
	}
	if err := tun.Up(context.Background(), cfg); err != nil {
		log.Fatalf("bring tunnel up: %v", err)
	}

	log.Println("[client] ✓ connected")
	log.Println("[client]   outer tunnel: you <-> entry node (encrypted)")
	log.Println("[client]   inner tunnel: you <-> exit node (double encrypted)")
	log.Println("[client]   all traffic routed through two-hop tunnel")
	log.Println("[client]   DNS routed through tunnel resolver")

	// Now that tunnels are up, update the token store.
	// Deferred from the connect phase so tokens aren't lost if WG setup fails.
	if spentTokenCount > 0 {
		tokens, _ := loadTokens(*tokenFile)
		if len(tokens) >= spentTokenCount {
			remaining := tokens[spentTokenCount:]
			if err := saveTokens(*tokenFile, remaining); err != nil {
				log.Printf("[client] warning: could not update token store: %v", err)
			} else {
				log.Printf("[client] %d tokens remaining", len(remaining))
			}
		}
	}

	log.Println("[client] press Ctrl-C to disconnect")

	// Wait for shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("[client] disconnecting...")
	if err := tun.Down(context.Background()); err != nil {
		log.Printf("[client] warning: tunnel teardown reported errors: %v", err)
	}
	log.Println("[client] disconnected")
}

// --- Key management ---

// promptPassphrase reads a passphrase from the terminal without echoing it.
// This prevents the passphrase from appearing in screen recordings, shoulder
// surfing, or terminal scrollback history.
func promptPassphrase(prompt string) string {
	fmt.Print(prompt)
	// term.ReadPassword reads from stdin with echo disabled — characters
	// are NOT shown as you type, just like sudo or ssh-keygen.
	pass, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println() // newline after hidden input
	if err != nil {
		log.Fatalf("read passphrase: %v", err)
	}
	if len(pass) == 0 {
		log.Fatalf("passphrase cannot be empty")
	}
	return string(pass)
}

// --- Phase 5: Bandwidth purchase flow ---

// runPurchaseFlow handles the complete bandwidth purchase:
// 1. Load hub's public key
// 2. Purchase a tier (get Lightning invoice)
// 3. Wait for the user to pay
// 4. Redeem blind tokens
// 5. Save tokens to disk
func runPurchaseFlow(hubURL, hubKeyFile, tierID, tokenFile string, tokenCount int) {
	// Load hub's blind signature public key.
	pubKey, err := credentials.LoadPublicKey(hubKeyFile)
	if err != nil {
		log.Fatalf("load hub public key: %v", err)
	}
	log.Printf("[purchase] hub key: %s (%d bytes/token)", pubKey.KeyID, pubKey.BytesPerToken)

	bwClient := client.NewBandwidthClient(hubURL, pubKey.PublicKey, pubKey.KeyID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle Ctrl-C gracefully.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		cancel()
	}()

	// Step 1: Purchase.
	log.Printf("[purchase] requesting %s tier...", tierID)
	purchase, err := bwClient.Purchase(ctx, tierID)
	if err != nil {
		log.Fatalf("purchase failed: %v", err)
	}

	fmt.Println()
	fmt.Println("╔══════════════════════════════════════╗")
	fmt.Println("║      ARFL Bandwidth Purchase         ║")
	fmt.Println("╠══════════════════════════════════════╣")
	fmt.Printf("║ Tier:    %-28s ║\n", purchase.Tier)
	fmt.Printf("║ Amount:  %-28s ║\n", fmt.Sprintf("%d sats", purchase.AmountSats))
	fmt.Printf("║ Expires: %-28s ║\n", purchase.ExpiresAt)
	fmt.Println("╠══════════════════════════════════════╣")
	fmt.Println("║ Pay this invoice:                    ║")
	fmt.Println("╚══════════════════════════════════════╝")
	fmt.Println()
	fmt.Println(purchase.PaymentRequest)
	fmt.Println()
	fmt.Println("Waiting for payment...")

	// Step 2: Wait for settlement.
	status, err := bwClient.WaitForSettlement(ctx, purchase.PaymentHash, 2*time.Second)
	if err != nil {
		log.Fatalf("settlement failed: %v", err)
	}
	_ = status

	fmt.Println("✓ Payment received!")
	fmt.Println()

	// Step 3: Get preimage.
	// In production, the wallet returns the preimage after payment.
	// For the PoC, we prompt the user.
	fmt.Print("Enter payment preimage (hex): ")
	var preimage string
	fmt.Scanln(&preimage)
	preimage = strings.TrimSpace(preimage)
	if preimage == "" {
		log.Fatalf("preimage is required to redeem tokens")
	}

	// Step 4: Redeem tokens.
	count := tokenCount
	if count <= 0 {
		// Default: redeem all available tokens for the tier.
		tier, err := credentials.LookupTier(tierID)
		if err != nil {
			log.Fatalf("unknown tier: %v", err)
		}
		count = tier.TicketCount
	}

	nonce := fmt.Sprintf("purchase-%s-%d", purchase.PaymentHash[:16], time.Now().UnixNano())
	log.Printf("[purchase] redeeming %d tokens...", count)

	result, err := bwClient.RedeemTokens(ctx, preimage, count, nonce)
	if err != nil {
		log.Fatalf("redeem failed: %v", err)
	}

	fmt.Printf("✓ Redeemed %d tokens (%d remaining)\n", result.TokensRedeemed, result.TokensRemaining)
	fmt.Printf("  Each token: %d MB\n", result.BytesPerToken/1_000_000)

	// Step 5: Save tokens to disk.
	if err := saveTokens(tokenFile, result.Tokens); err != nil {
		log.Fatalf("save tokens: %v", err)
	}
	fmt.Printf("✓ Tokens saved to %s\n", tokenFile)
	fmt.Printf("\nUse --session or --discover to connect with these tokens.\n")
}

// --- Token persistence ---

// TokenStore is the on-disk format for saved tokens.
type TokenStore struct {
	Tokens []*credentials.BlindToken `json:"tokens"`
}

func saveTokens(path string, tokens []*credentials.BlindToken) error {
	store := TokenStore{Tokens: tokens}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func loadTokens(path string) ([]*credentials.BlindToken, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var store TokenStore
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, err
	}
	return store.Tokens, nil
}
