package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/Radi-Labs/ARFL/internal/config"
	"github.com/Radi-Labs/ARFL/internal/wg"
	"github.com/Radi-Labs/ARFL/pkg/protocol"
)

func main() {
	sessionPath := flag.String("session", "session.json", "path to session config file")
	keyFile := flag.String("key", "client.key", "path to client WireGuard key file")
	genKey := flag.Bool("genkey", false, "generate a new WireGuard keypair and exit")
	flag.Parse()

	if *genKey {
		kp, err := wg.GenerateKeyPair()
		if err != nil {
			log.Fatalf("generate keypair: %v", err)
		}
		if err := os.WriteFile(*keyFile, mustMarshal(kp), 0600); err != nil {
			log.Fatalf("write key file: %v", err)
		}
		fmt.Printf("Keypair written to %s\n", *keyFile)
		fmt.Printf("Public key: %s\n", kp.PublicKey)
		return
	}

	// Load client key
	kp, err := loadKeyPair(*keyFile)
	if err != nil {
		log.Fatalf("load key: %v (run with --genkey first)", err)
	}
	log.Printf("[client] public key: %s", kp.PublicKey)

	// Load session config
	session, err := config.LoadSessionFile(*sessionPath)
	if err != nil {
		log.Fatalf("load session: %v", err)
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
	addRoute(exitIP+"/32", "", "wg-outer")            // exit goes through outer tunnel

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

func loadKeyPair(path string) (*wg.KeyPair, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var kp wg.KeyPair
	return &kp, json.Unmarshal(data, &kp)
}

func mustMarshal(v any) []byte {
	data, _ := json.MarshalIndent(v, "", "  ")
	return data
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
