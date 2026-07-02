package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/Radi-Labs/ARFL/internal/client"
	"github.com/Radi-Labs/ARFL/internal/config"
	"github.com/Radi-Labs/ARFL/internal/credentials"
	"github.com/Radi-Labs/ARFL/internal/discovery"
	"github.com/Radi-Labs/ARFL/internal/wg"
	"github.com/Radi-Labs/ARFL/pkg/protocol"
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

		// Mark tokens as spent — remove from store so they can't be reused.
		remaining := tokens[2:]
		if err := saveTokens(*tokenFile, remaining); err != nil {
			log.Printf("[client] warning: could not update token store: %v", err)
		} else {
			log.Printf("[client] %d tokens remaining", len(remaining))
		}
	}

	// Create WireGuard manager
	wgMgr, err := wg.NewManager()
	if err != nil {
		log.Fatalf("create WireGuard manager: %v", err)
	}
	defer wgMgr.Close()

	// Store default gateway before we change routing
	defaultGW, defaultIface := getDefaultGateway()
	entryIP, _, _ := net.SplitHostPort(session.EntryEndpoint)
	exitIP, _, _ := net.SplitHostPort(session.ExitEndpoint)

	// 1. Create outer tunnel (client <-> entry node)
	log.Println("[client] creating outer tunnel to entry node...")
	if err := wgMgr.CreateInterface(wg.InterfaceConfig{
		Name:       "wg-outer",
		PrivateKey: kp.PrivateKey,
		ListenPort: 0, // Client mode, no listening
		Address:    session.OuterTunnelIP,
		MTU:        protocol.TunnelMTU,
	}); err != nil {
		log.Fatalf("create wg-outer: %v", err)
	}

	// Add entry node as peer on outer tunnel
	// AllowedIPs: entry tunnel subnet + exit node's public IP (so inner tunnel packets route through outer)
	if err := wgMgr.AddPeer("wg-outer", wg.PeerConfig{
		PublicKey:  session.EntryWGPubkey,
		Endpoint:   session.EntryEndpoint,
		AllowedIPs: []string{"10.100.0.0/24", exitIP + "/32"},
		Keepalive:  25,
	}); err != nil {
		log.Fatalf("add entry peer: %v", err)
	}
	log.Println("[client] outer tunnel configured")

	// 2. Set up routing so exit node's IP goes through outer tunnel
	log.Println("[client] configuring routes...")
	addRoute(entryIP+"/32", defaultGW, defaultIface) // entry reachable via real gateway
	addRoute(exitIP+"/32", "", "wg-outer")           // exit goes through outer tunnel

	// 3. Create inner tunnel (client <-> exit node, carried inside outer)
	log.Println("[client] creating inner tunnel to exit node...")
	if err := wgMgr.CreateInterface(wg.InterfaceConfig{
		Name:       "wg-inner",
		PrivateKey: kp.PrivateKey,
		ListenPort: 0,
		Address:    session.InnerTunnelIP,
		MTU:        protocol.TunnelMTU - 80, // Account for double encapsulation
	}); err != nil {
		log.Fatalf("create wg-inner: %v", err)
	}

	// Add exit node as peer on inner tunnel — endpoint goes through outer tunnel
	if err := wgMgr.AddPeer("wg-inner", wg.PeerConfig{
		PublicKey:  session.ExitWGPubkey,
		Endpoint:   session.ExitEndpoint,
		AllowedIPs: []string{"0.0.0.0/0"},
		Keepalive:  25,
	}); err != nil {
		log.Fatalf("add exit peer: %v", err)
	}
	log.Println("[client] inner tunnel configured")

	// 4. Route all traffic through inner tunnel
	addRoute("0.0.0.0/1", "", "wg-inner")
	addRoute("128.0.0.0/1", "", "wg-inner")

	// 5. Set DNS to Quad9 within tunnel
	setDNS(protocol.DNSResolver)

	log.Println("[client] ✓ connected")
	log.Println("[client]   outer tunnel: you <-> entry node (encrypted)")
	log.Println("[client]   inner tunnel: you <-> exit node (double encrypted)")
	log.Println("[client]   all traffic routed through two-hop tunnel")
	log.Printf("[client]   DNS: %s (Quad9, no leak)", protocol.DNSResolver)
	log.Println("[client] press Ctrl-C to disconnect")

	// Wait for shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("[client] disconnecting...")
	cleanup(wgMgr, entryIP, exitIP, defaultGW, defaultIface)
	log.Println("[client] disconnected")
}

func cleanup(wgMgr *wg.WgctrlManager, entryIP, exitIP, defaultGW, defaultIface string) {
	// Remove routes
	delRoute("0.0.0.0/1", "", "wg-inner")
	delRoute("128.0.0.0/1", "", "wg-inner")
	delRoute(exitIP+"/32", "", "wg-outer")
	delRoute(entryIP+"/32", defaultGW, defaultIface)

	// Remove interfaces
	wgMgr.DeleteInterface("wg-inner")
	wgMgr.DeleteInterface("wg-outer")

	// Restore DNS
	restoreDNS()
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

// --- Routing helpers (platform-specific) ---

func getDefaultGateway() (gw string, iface string) {
	switch runtime.GOOS {
	case "linux":
		out, err := exec.Command("ip", "route", "show", "default").Output()
		if err != nil {
			log.Printf("[client] warning: could not determine default gateway: %v", err)
			return "", ""
		}
		// Parse: "default via 192.168.1.1 dev eth0"
		fields := splitFields(string(out))
		for i, f := range fields {
			if f == "via" && i+1 < len(fields) {
				gw = fields[i+1]
			}
			if f == "dev" && i+1 < len(fields) {
				iface = fields[i+1]
			}
		}
	case "darwin":
		out, err := exec.Command("route", "-n", "get", "default").Output()
		if err != nil {
			log.Printf("[client] warning: could not determine default gateway: %v", err)
			return "", ""
		}
		for _, line := range splitLines(string(out)) {
			fields := splitFields(line)
			if len(fields) >= 2 {
				if fields[0] == "gateway:" {
					gw = fields[1]
				}
				if fields[0] == "interface:" {
					iface = fields[1]
				}
			}
		}
	}
	return gw, iface
}

func addRoute(cidr, gateway, iface string) {
	var err error
	switch runtime.GOOS {
	case "linux":
		if gateway != "" {
			err = runCmd("ip", "route", "add", cidr, "via", gateway, "dev", iface)
		} else {
			err = runCmd("ip", "route", "add", cidr, "dev", iface)
		}
	case "darwin":
		ip, _, _ := net.ParseCIDR(cidr)
		if ip == nil {
			ip = net.ParseIP(cidr)
		}
		dest := cidr
		if ip != nil {
			dest = ip.String()
		}
		if gateway != "" {
			err = runCmd("route", "-n", "add", "-net", dest, gateway)
		} else {
			err = runCmd("route", "-n", "add", "-net", dest, "-interface", iface)
		}
	}
	if err != nil {
		log.Printf("[route] add %s: %v", cidr, err)
	}
}

func delRoute(cidr, gateway, iface string) {
	var err error
	switch runtime.GOOS {
	case "linux":
		err = runCmd("ip", "route", "del", cidr)
	case "darwin":
		err = runCmd("route", "-n", "delete", "-net", cidr)
	}
	if err != nil {
		log.Printf("[route] del %s: %v", cidr, err)
	}
}

func setDNS(resolver string) {
	switch runtime.GOOS {
	case "linux":
		content := fmt.Sprintf("nameserver %s\n", resolver)
		if err := os.WriteFile("/etc/resolv.conf.arfl.bak", readFileOr("/etc/resolv.conf"), 0644); err != nil {
			log.Printf("[dns] warning: backup resolv.conf: %v", err)
		}
		if err := os.WriteFile("/etc/resolv.conf", []byte(content), 0644); err != nil {
			log.Printf("[dns] warning: set resolver: %v", err)
		}
	case "darwin":
		log.Printf("[dns] on macOS, set DNS manually to %s in System Preferences", resolver)
	}
}

func restoreDNS() {
	switch runtime.GOOS {
	case "linux":
		backup := readFileOr("/etc/resolv.conf.arfl.bak")
		if len(backup) > 0 {
			os.WriteFile("/etc/resolv.conf", backup, 0644)
			os.Remove("/etc/resolv.conf.arfl.bak")
		}
	case "darwin":
		log.Println("[dns] restore DNS manually in System Preferences")
	}
}

// --- Utility ---

func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, string(out))
	}
	return nil
}

func readFileOr(path string) []byte {
	data, _ := os.ReadFile(path)
	return data
}

func splitFields(s string) []string {
	var fields []string
	for _, f := range splitByWhitespace(s) {
		if f != "" {
			fields = append(fields, f)
		}
	}
	return fields
}

func splitByWhitespace(s string) []string {
	var result []string
	current := ""
	for _, c := range s {
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			if current != "" {
				result = append(result, current)
				current = ""
			}
		} else {
			current += string(c)
		}
	}
	if current != "" {
		result = append(result, current)
	}
	return result
}

func splitLines(s string) []string {
	var lines []string
	current := ""
	for _, c := range s {
		if c == '\n' {
			lines = append(lines, current)
			current = ""
		} else {
			current += string(c)
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
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
