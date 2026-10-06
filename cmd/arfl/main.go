package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Radi-Labs/ARFL/internal/config"
	"github.com/Radi-Labs/ARFL/internal/nostr"
	"github.com/Radi-Labs/ARFL/internal/wg"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

const defaultRelayCSV = "wss://relay.damus.io,wss://nos.lol"

var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	borderStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	passStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
	warnStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	failStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))
	mutedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	summaryStyle = lipgloss.NewStyle().Bold(true)
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stderr)
		return fmt.Errorf("missing command")
	}

	switch args[0] {
	case "init":
		return runInit(args[1:], stdin, stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		printUsage(stdout)
		return nil
	default:
		printUsage(stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "ARFL setup helper")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  arfl init hub [flags]     Generate hub.json with secure defaults")
	fmt.Fprintln(w, "  arfl init node [flags]    Generate node.json with generated keys")
	fmt.Fprintln(w, "  arfl doctor hub [flags]   Validate hub config and optional health")
	fmt.Fprintln(w, "  arfl doctor node [flags]  Validate node config and optional health")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  arfl init hub --output /opt/arfl/data/hub.json")
	fmt.Fprintln(w, "  arfl init node --role entry --endpoint 203.0.113.10:51820")
	fmt.Fprintln(w, "  arfl doctor hub --config hub.json --url http://127.0.0.1:8080")
	fmt.Fprintln(w, "  arfl doctor node --config node.json --hub-url http://127.0.0.1:8080")
}

func runInit(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: arfl init <hub|node> [flags]")
		return fmt.Errorf("missing init target")
	}
	switch args[0] {
	case "hub":
		return runInitHub(args[1:], stdin, stdout)
	case "node":
		return runInitNode(args[1:], stdin, stdout)
	default:
		return fmt.Errorf("unknown init target %q (expected hub or node)", args[0])
	}
}

func shouldUseCharm(nonInteractive bool, stdin io.Reader) bool {
	if nonInteractive {
		return false
	}
	f, ok := stdin.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

func shouldStyle(out io.Writer) bool {
	f, ok := out.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

func parseRequiredInt(label, value string, min, max int) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("%s is required", label)
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number", label)
	}
	if n < min || n > max {
		return 0, fmt.Errorf("%s must be between %d and %d", label, min, max)
	}
	return n, nil
}

func parseRequiredInt64(label, value string, min int64) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("%s is required", label)
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number", label)
	}
	if n < min {
		return 0, fmt.Errorf("%s must be >= %d", label, min)
	}
	return n, nil
}

func validateRequired(label string) func(string) error {
	return func(v string) error {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("%s is required", label)
		}
		return nil
	}
}

func validateHostPort(label string) func(string) error {
	return func(v string) error {
		v = strings.TrimSpace(v)
		if v == "" {
			return fmt.Errorf("%s is required", label)
		}
		if _, _, err := net.SplitHostPort(v); err != nil {
			return fmt.Errorf("%s must be host:port", label)
		}
		return nil
	}
}

func validateURL(label string) func(string) error {
	return func(v string) error {
		v = strings.TrimSpace(v)
		if v == "" {
			return fmt.Errorf("%s is required", label)
		}
		if _, err := url.ParseRequestURI(v); err != nil {
			return fmt.Errorf("%s must be a valid URL", label)
		}
		return nil
	}
}

func validateRole(v string) error {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "entry", "exit", "both":
		return nil
	default:
		return fmt.Errorf("role must be entry, exit, or both")
	}
}

func validateCIDR(label string) func(string) error {
	return func(v string) error {
		v = strings.TrimSpace(v)
		if v == "" {
			return fmt.Errorf("%s is required", label)
		}
		if _, _, err := net.ParseCIDR(v); err != nil {
			return fmt.Errorf("%s must be a valid CIDR", label)
		}
		return nil
	}
}

func validatePositiveInt(label string) func(string) error {
	return func(v string) error {
		n, err := parseRequiredInt(label, v, 1, 1_000_000_000)
		if err != nil {
			return err
		}
		if n <= 0 {
			return fmt.Errorf("%s must be > 0", label)
		}
		return nil
	}
}

func validateIntRange(label string, min, max int) func(string) error {
	return func(v string) error {
		_, err := parseRequiredInt(label, v, min, max)
		return err
	}
}

func validateMinInt64(label string, min int64) func(string) error {
	return func(v string) error {
		_, err := parseRequiredInt64(label, v, min)
		return err
	}
}

func validateRelayCSV(v string) error {
	if len(parseCSV(v)) == 0 {
		return fmt.Errorf("at least one relay is required")
	}
	return nil
}

func runCharmForm(form *huh.Form, stdin io.Reader, stdout io.Writer) error {
	return form.WithProgramOptions(
		tea.WithInput(stdin),
		tea.WithOutput(stdout),
	).Run()
}

func printInitHeader(out io.Writer, title string) {
	if shouldStyle(out) {
		fmt.Fprintln(out, titleStyle.Render(title))
		fmt.Fprintln(out, mutedStyle.Render("Use arrow/tab/enter keys to navigate fields and submit."))
		return
	}
	fmt.Fprintln(out, title)
	fmt.Fprintln(out, "Fill values and submit.")
}

type hubInitOptions struct {
	ListenAddr      string
	Relays          []string
	DBPath          string
	BlindKeyDir     string
	SettlementHours int
	MinPayoutSats   int64
	HubMarginPct    int
	LNDHost         string
	LNDPort         int
	LNDTLSCertPath  string
	LNDMacaroonPath string
	LNDFeeLimitSat  int64
}

func runInitHub(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("init hub", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	output := fs.String("output", "hub.json", "path to write hub config")
	force := fs.Bool("force", false, "overwrite output file if it exists (creates a timestamped backup)")
	nonInteractive := fs.Bool("non-interactive", false, "disable prompts and use provided/default values")
	listenAddr := fs.String("listen-addr", "0.0.0.0:8080", "hub API listen address")
	relayCSV := fs.String("relays", defaultRelayCSV, "comma-separated Nostr relays")
	dbPath := fs.String("db-path", "arfl.db", "SQLite database path")
	blindKeyDir := fs.String("blind-key-dir", "keys", "directory for mint keys")
	settlementHours := fs.Int("settlement-hours", 6, "settlement interval in hours")
	minPayoutSats := fs.Int64("min-payout-sats", 1000, "minimum payout threshold in sats")
	hubMargin := fs.Int("hub-margin-pct", 20, "hub margin percentage (0-50)")
	lndHost := fs.String("lnd-host", "localhost", "LND REST host")
	lndPort := fs.Int("lnd-port", 8080, "LND REST port")
	lndTLS := fs.String("lnd-tls-cert-path", "~/.lnd/tls.cert", "LND TLS cert path")
	lndMac := fs.String("lnd-macaroon-path", "~/.lnd/data/chain/bitcoin/mainnet/admin.macaroon", "LND admin macaroon path")
	lndFee := fs.Int64("lnd-fee-limit-sat", 100, "max Lightning routing fee in sats")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	if shouldUseCharm(*nonInteractive, stdin) {
		printInitHeader(stdout, "ARFL Hub Setup")

		settlementHoursInput := strconv.Itoa(*settlementHours)
		minPayoutInput := strconv.FormatInt(*minPayoutSats, 10)
		hubMarginInput := strconv.Itoa(*hubMargin)
		lndPortInput := strconv.Itoa(*lndPort)
		lndFeeInput := strconv.FormatInt(*lndFee, 10)

		form := huh.NewForm(
			huh.NewGroup(
				huh.NewInput().
					Title("Hub listen address").
					Description("Public API bind address for arfl-hub").
					Value(listenAddr).
					Validate(validateHostPort("listen address")),
				huh.NewInput().
					Title("Nostr relays").
					Description("Comma-separated relay URLs").
					Value(relayCSV).
					Validate(validateRelayCSV),
				huh.NewInput().
					Title("Hub database path").
					Value(dbPath).
					Validate(validateRequired("db path")),
				huh.NewInput().
					Title("Blind key directory").
					Value(blindKeyDir).
					Validate(validateRequired("blind key directory")),
			),
			huh.NewGroup(
				huh.NewInput().
					Title("Settlement interval (hours)").
					Value(&settlementHoursInput).
					Validate(validatePositiveInt("settlement interval")),
				huh.NewInput().
					Title("Minimum payout (sats)").
					Value(&minPayoutInput).
					Validate(validateMinInt64("minimum payout", 0)),
				huh.NewInput().
					Title("Hub margin (%)").
					Value(&hubMarginInput).
					Validate(validateIntRange("hub margin", 0, 50)),
			),
			huh.NewGroup(
				huh.NewInput().
					Title("LND host").
					Value(lndHost).
					Validate(validateRequired("LND host")),
				huh.NewInput().
					Title("LND REST port").
					Value(&lndPortInput).
					Validate(validatePositiveInt("LND port")),
				huh.NewInput().
					Title("LND TLS cert path").
					Value(lndTLS).
					Validate(validateRequired("LND TLS cert path")),
				huh.NewInput().
					Title("LND macaroon path").
					Value(lndMac).
					Validate(validateRequired("LND macaroon path")),
				huh.NewInput().
					Title("LND fee limit (sats)").
					Value(&lndFeeInput).
					Validate(validateMinInt64("LND fee limit", 0)),
			),
		)
		if err := runCharmForm(form, stdin, stdout); err != nil {
			return err
		}

		var err error
		*settlementHours, err = parseRequiredInt("settlement interval", settlementHoursInput, 1, 1_000_000_000)
		if err != nil {
			return err
		}
		*minPayoutSats, err = parseRequiredInt64("minimum payout", minPayoutInput, 0)
		if err != nil {
			return err
		}
		*hubMargin, err = parseRequiredInt("hub margin", hubMarginInput, 0, 50)
		if err != nil {
			return err
		}
		*lndPort, err = parseRequiredInt("LND port", lndPortInput, 1, 1_000_000_000)
		if err != nil {
			return err
		}
		*lndFee, err = parseRequiredInt64("LND fee limit", lndFeeInput, 0)
		if err != nil {
			return err
		}
	}

	cfg, hubPubkey, err := buildHubConfig(hubInitOptions{
		ListenAddr:      *listenAddr,
		Relays:          parseCSV(*relayCSV),
		DBPath:          *dbPath,
		BlindKeyDir:     *blindKeyDir,
		SettlementHours: *settlementHours,
		MinPayoutSats:   *minPayoutSats,
		HubMarginPct:    *hubMargin,
		LNDHost:         *lndHost,
		LNDPort:         *lndPort,
		LNDTLSCertPath:  *lndTLS,
		LNDMacaroonPath: *lndMac,
		LNDFeeLimitSat:  *lndFee,
	})
	if err != nil {
		return err
	}
	if err := guardOutputPath(*output, *force); err != nil {
		return err
	}
	if err := writeJSON(*output, cfg, 0o600); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "hub config written: %s\n", *output)
	fmt.Fprintf(stdout, "hub nostr pubkey: %s\n", hubPubkey)
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "next steps:")
	fmt.Fprintf(stdout, "  1. start hub: arfl-hub --config %s\n", *output)
	fmt.Fprintln(stdout, "  2. copy generated public key (*.pub.json) from blind_key_dir to nodes")
	fmt.Fprintln(stdout, "  3. run: arfl doctor hub --config "+*output)
	return nil
}

func buildHubConfig(opts hubInitOptions) (*config.HubConfig, string, error) {
	if opts.ListenAddr == "" {
		return nil, "", fmt.Errorf("listen address is required")
	}
	if opts.LNDHost == "" {
		return nil, "", fmt.Errorf("LND host is required")
	}
	if opts.LNDPort <= 0 {
		return nil, "", fmt.Errorf("LND port must be > 0")
	}
	if opts.HubMarginPct < 0 || opts.HubMarginPct > 50 {
		return nil, "", fmt.Errorf("hub margin must be between 0 and 50")
	}
	if len(opts.Relays) == 0 {
		opts.Relays = parseCSV(defaultRelayCSV)
	}

	kp, err := nostr.GenerateKeyPair()
	if err != nil {
		return nil, "", fmt.Errorf("generate nostr key: %w", err)
	}
	credKey, err := randomHex(32)
	if err != nil {
		return nil, "", fmt.Errorf("generate credential key: %w", err)
	}

	cfg := &config.HubConfig{
		NostrPrivkey:    kp.PrivkeyHex(),
		ListenAddr:      opts.ListenAddr,
		Relays:          opts.Relays,
		DBPath:          opts.DBPath,
		CredentialKey:   credKey,
		BlindKeyDir:     opts.BlindKeyDir,
		SettlementHours: opts.SettlementHours,
		MinPayoutSats:   opts.MinPayoutSats,
		HubMarginPct:    opts.HubMarginPct,
		LNDHost:         opts.LNDHost,
		LNDPort:         opts.LNDPort,
		LNDTLSCertPath:  opts.LNDTLSCertPath,
		LNDMacaroonPath: opts.LNDMacaroonPath,
		LNDFeeLimitSat:  opts.LNDFeeLimitSat,
	}
	return cfg, kp.PubkeyHex(), nil
}

type nodePreset struct {
	ListenPort int
	TunnelIP   string
	Interface  string
}

func presetForRole(role string) (nodePreset, error) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "entry":
		return nodePreset{
			ListenPort: 51820,
			TunnelIP:   "10.100.0.1/24",
			Interface:  "wg-entry",
		}, nil
	case "exit":
		return nodePreset{
			ListenPort: 51821,
			TunnelIP:   "10.200.0.1/24",
			Interface:  "wg-exit",
		}, nil
	case "both":
		return nodePreset{
			ListenPort: 51820,
			TunnelIP:   "10.150.0.1/24",
			Interface:  "wg-both",
		}, nil
	default:
		return nodePreset{}, fmt.Errorf("invalid role %q (expected entry, exit, or both)", role)
	}
}

type nodeInitOptions struct {
	Role          string
	ListenPort    int
	TunnelIP      string
	Interface     string
	OutInterface  string
	AdminAddr     string
	MTU           int
	Endpoint      string
	ConnectAddr   string
	UploadMbps    int
	DownloadMbps  int
	Capacity      int
	NostrRelays   []string
	HubURL        string
	HubPubkeyFile string
}

func runInitNode(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("init node", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	output := fs.String("output", "node.json", "path to write node config")
	force := fs.Bool("force", false, "overwrite output file if it exists (creates a timestamped backup)")
	nonInteractive := fs.Bool("non-interactive", false, "disable prompts and use provided/default values")
	role := fs.String("role", "entry", "node role: entry, exit, or both")
	listenPort := fs.Int("listen-port", -1, "WireGuard UDP listen port")
	tunnelIP := fs.String("tunnel-ip", "", "node tunnel interface CIDR")
	iface := fs.String("interface", "", "WireGuard interface name")
	outIface := fs.String("out-interface", "eth0", "internet-facing interface")
	adminAddr := fs.String("admin-addr", "127.0.0.1:9090", "admin API listen address")
	connectAddr := fs.String("connect-addr", "0.0.0.0:9091", "public connect API listen address")
	mtu := fs.Int("mtu", 1280, "WireGuard MTU")
	endpoint := fs.String("endpoint", "", "public endpoint in host:port form")
	upload := fs.Int("upload-mbps", 100, "advertised upload bandwidth")
	download := fs.Int("download-mbps", 100, "advertised download bandwidth")
	capacity := fs.Int("capacity", 50, "max concurrent peers")
	relayCSV := fs.String("relays", defaultRelayCSV, "comma-separated Nostr relays")
	hubURL := fs.String("hub-url", "http://127.0.0.1:8080", "hub API URL")
	hubPubFile := fs.String("hub-pubkey-file", "keys/key-100mb.pub.json", "hub blind signature public key file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	preset, err := presetForRole(*role)
	if err != nil {
		return err
	}
	if *listenPort <= 0 {
		*listenPort = preset.ListenPort
	}
	if strings.TrimSpace(*tunnelIP) == "" {
		*tunnelIP = preset.TunnelIP
	}
	if strings.TrimSpace(*iface) == "" {
		*iface = preset.Interface
	}
	endpointWasDefault := false
	if strings.TrimSpace(*endpoint) == "" {
		*endpoint = fmt.Sprintf("127.0.0.1:%d", *listenPort)
		endpointWasDefault = true
	}

	if shouldUseCharm(*nonInteractive, stdin) {
		printInitHeader(stdout, "ARFL Node Setup")

		initialPreset := preset
		roleSelection := strings.ToLower(strings.TrimSpace(*role))

		roleForm := huh.NewForm(
			huh.NewGroup(
				huh.NewSelect[string]().
					Title("Node role").
					Description("Choose which hop this node serves.").
					Value(&roleSelection).
					Options(
						huh.NewOption("entry", "entry"),
						huh.NewOption("exit", "exit"),
						huh.NewOption("both", "both"),
					),
			),
		)
		if err := runCharmForm(roleForm, stdin, stdout); err != nil {
			return err
		}
		roleSelection = strings.ToLower(strings.TrimSpace(roleSelection))
		if roleSelection != strings.ToLower(strings.TrimSpace(*role)) {
			newPreset, err := presetForRole(roleSelection)
			if err != nil {
				return err
			}
			if *listenPort == initialPreset.ListenPort {
				*listenPort = newPreset.ListenPort
			}
			if strings.TrimSpace(*tunnelIP) == initialPreset.TunnelIP {
				*tunnelIP = newPreset.TunnelIP
			}
			if strings.TrimSpace(*iface) == initialPreset.Interface {
				*iface = newPreset.Interface
			}
			if endpointWasDefault {
				*endpoint = fmt.Sprintf("127.0.0.1:%d", *listenPort)
			}
		}
		*role = roleSelection

		listenPortInput := strconv.Itoa(*listenPort)
		mtuInput := strconv.Itoa(*mtu)
		uploadInput := strconv.Itoa(*upload)
		downloadInput := strconv.Itoa(*download)
		capacityInput := strconv.Itoa(*capacity)

		form := huh.NewForm(
			huh.NewGroup(
				huh.NewInput().
					Title("WireGuard listen port").
					Value(&listenPortInput).
					Validate(validatePositiveInt("listen port")),
				huh.NewInput().
					Title("Tunnel CIDR").
					Value(tunnelIP).
					Validate(validateCIDR("tunnel CIDR")),
				huh.NewInput().
					Title("WireGuard interface").
					Value(iface).
					Validate(validateRequired("interface")),
				huh.NewInput().
					Title("Internet-facing interface").
					Value(outIface).
					Validate(validateRequired("out interface")),
				huh.NewInput().
					Title("WireGuard MTU").
					Value(&mtuInput).
					Validate(validatePositiveInt("MTU")),
			),
			huh.NewGroup(
				huh.NewInput().
					Title("Admin API listen address").
					Value(adminAddr).
					Validate(validateHostPort("admin address")),
				huh.NewInput().
					Title("Connect API listen address").
					Value(connectAddr).
					Validate(validateHostPort("connect address")),
				huh.NewInput().
					Title("Public endpoint host:port").
					Value(endpoint).
					Validate(validateHostPort("endpoint")),
				huh.NewInput().
					Title("Hub API URL").
					Value(hubURL).
					Validate(validateURL("hub URL")),
				huh.NewInput().
					Title("Hub public key file").
					Value(hubPubFile).
					Validate(validateRequired("hub public key file")),
				huh.NewInput().
					Title("Nostr relays").
					Description("Comma-separated relay URLs").
					Value(relayCSV).
					Validate(validateRelayCSV),
			),
			huh.NewGroup(
				huh.NewInput().
					Title("Advertised upload (Mbps)").
					Value(&uploadInput).
					Validate(validatePositiveInt("upload Mbps")),
				huh.NewInput().
					Title("Advertised download (Mbps)").
					Value(&downloadInput).
					Validate(validatePositiveInt("download Mbps")),
				huh.NewInput().
					Title("Capacity (concurrent peers)").
					Value(&capacityInput).
					Validate(validatePositiveInt("capacity")),
			),
		)
		if err := runCharmForm(form, stdin, stdout); err != nil {
			return err
		}

		*role = strings.ToLower(strings.TrimSpace(*role))
		var err error
		*listenPort, err = parseRequiredInt("listen port", listenPortInput, 1, 1_000_000_000)
		if err != nil {
			return err
		}
		*mtu, err = parseRequiredInt("MTU", mtuInput, 1, 1_000_000_000)
		if err != nil {
			return err
		}
		*upload, err = parseRequiredInt("upload Mbps", uploadInput, 1, 1_000_000_000)
		if err != nil {
			return err
		}
		*download, err = parseRequiredInt("download Mbps", downloadInput, 1, 1_000_000_000)
		if err != nil {
			return err
		}
		*capacity, err = parseRequiredInt("capacity", capacityInput, 1, 1_000_000_000)
		if err != nil {
			return err
		}
	}

	cfg, nodePubkey, nodeWGPubkey, err := buildNodeConfig(nodeInitOptions{
		Role:          *role,
		ListenPort:    *listenPort,
		TunnelIP:      *tunnelIP,
		Interface:     *iface,
		OutInterface:  *outIface,
		AdminAddr:     *adminAddr,
		MTU:           *mtu,
		Endpoint:      *endpoint,
		ConnectAddr:   *connectAddr,
		UploadMbps:    *upload,
		DownloadMbps:  *download,
		Capacity:      *capacity,
		NostrRelays:   parseCSV(*relayCSV),
		HubURL:        *hubURL,
		HubPubkeyFile: *hubPubFile,
	})
	if err != nil {
		return err
	}
	if err := guardOutputPath(*output, *force); err != nil {
		return err
	}
	if err := writeJSON(*output, cfg, 0o600); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "node config written: %s\n", *output)
	fmt.Fprintf(stdout, "node nostr pubkey: %s\n", nodePubkey)
	fmt.Fprintf(stdout, "node wireguard pubkey: %s\n", nodeWGPubkey)
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "next steps:")
	fmt.Fprintln(stdout, "  1. request attestation from your hub operator:")
	fmt.Fprintf(stdout, "     arfl-hub attest --config hub.json --node-pubkey %s --node-wg-key %s --operator <id> --role %s --out attestation.json\n", nodePubkey, nodeWGPubkey, strings.ToLower(strings.TrimSpace(*role)))
	fmt.Fprintln(stdout, "  2. add the attestation JSON string to node config field \"attestation\"")
	fmt.Fprintf(stdout, "  3. start node: arfl-node --config %s\n", *output)
	fmt.Fprintln(stdout, "  4. run: arfl doctor node --config "+*output)
	return nil
}

func buildNodeConfig(opts nodeInitOptions) (*config.NodeConfig, string, string, error) {
	role := strings.ToLower(strings.TrimSpace(opts.Role))
	if _, err := presetForRole(role); err != nil {
		return nil, "", "", err
	}
	if opts.ListenPort <= 0 {
		return nil, "", "", fmt.Errorf("listen_port must be > 0")
	}
	if opts.Endpoint == "" {
		return nil, "", "", fmt.Errorf("endpoint is required")
	}
	if _, _, err := net.SplitHostPort(opts.Endpoint); err != nil {
		return nil, "", "", fmt.Errorf("endpoint must be host:port: %w", err)
	}
	if opts.HubURL == "" {
		return nil, "", "", fmt.Errorf("hub_url is required")
	}
	if _, err := url.ParseRequestURI(opts.HubURL); err != nil {
		return nil, "", "", fmt.Errorf("invalid hub_url: %w", err)
	}
	if opts.ConnectAddr == "" {
		return nil, "", "", fmt.Errorf("connect_addr is required")
	}
	if _, _, err := net.SplitHostPort(opts.ConnectAddr); err != nil {
		return nil, "", "", fmt.Errorf("connect_addr must be host:port: %w", err)
	}
	if opts.AdminAddr == "" {
		return nil, "", "", fmt.Errorf("admin_addr is required")
	}
	if _, _, err := net.SplitHostPort(opts.AdminAddr); err != nil {
		return nil, "", "", fmt.Errorf("admin_addr must be host:port: %w", err)
	}
	if len(opts.NostrRelays) == 0 {
		opts.NostrRelays = parseCSV(defaultRelayCSV)
	}

	wgKP, err := wg.GenerateKeyPair()
	if err != nil {
		return nil, "", "", fmt.Errorf("generate WireGuard keypair: %w", err)
	}
	nostrKP, err := nostr.GenerateKeyPair()
	if err != nil {
		return nil, "", "", fmt.Errorf("generate Nostr keypair: %w", err)
	}

	cfg := &config.NodeConfig{
		Role:                 role,
		ListenPort:           opts.ListenPort,
		PrivateKey:           wgKP.PrivateKey,
		TunnelIP:             opts.TunnelIP,
		Interface:            opts.Interface,
		OutInterface:         opts.OutInterface,
		AdminAddr:            opts.AdminAddr,
		MTU:                  opts.MTU,
		NostrPrivkey:         nostrKP.PrivkeyHex(),
		Relays:               opts.NostrRelays,
		Endpoint:             opts.Endpoint,
		UploadMbps:           opts.UploadMbps,
		DownloadMbps:         opts.DownloadMbps,
		Capacity:             opts.Capacity,
		HubURL:               opts.HubURL,
		HubPubkeyFile:        opts.HubPubkeyFile,
		ConnectAddr:          opts.ConnectAddr,
		EnabledTransports:    []string{"wireguard"},
		TransportEndpoints:   map[string]string{"wireguard": opts.Endpoint},
		TransportConnectAddr: map[string]string{"wireguard": opts.ConnectAddr},
	}
	return cfg, nostrKP.PubkeyHex(), wgKP.PublicKey, nil
}

func runDoctor(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: arfl doctor <hub|node> [flags]")
		return fmt.Errorf("missing doctor target")
	}
	switch args[0] {
	case "hub":
		return runDoctorHub(args[1:], stdout)
	case "node":
		return runDoctorNode(args[1:], stdout)
	default:
		return fmt.Errorf("unknown doctor target %q (expected hub or node)", args[0])
	}
}

type doctorStatus string

const (
	doctorPass doctorStatus = "PASS"
	doctorWarn doctorStatus = "WARN"
	doctorFail doctorStatus = "FAIL"
)

type doctorCheck struct {
	Name   string
	Status doctorStatus
	Detail string
	Hint   string
}

func runDoctorHub(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("doctor hub", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	cfgPath := fs.String("config", "hub.json", "path to hub config file")
	baseURL := fs.String("url", "", "optional running hub URL to check (e.g. http://127.0.0.1:8080)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	cfg, err := config.LoadHubConfig(*cfgPath)
	if err != nil {
		return fmt.Errorf("load hub config %q: %w", *cfgPath, err)
	}

	checks := validateHubConfig(cfg)
	if *baseURL != "" {
		if err := checkHTTPHealth(*baseURL); err != nil {
			checks = append(checks, doctorCheck{
				Name:   "Hub /health reachable",
				Status: doctorFail,
				Detail: err.Error(),
				Hint:   "start the hub and retry: arfl-hub --config " + *cfgPath,
			})
		} else {
			checks = append(checks, doctorCheck{
				Name:   "Hub /health reachable",
				Status: doctorPass,
				Detail: "responding at " + *baseURL + "/health",
			})
		}
	} else {
		checks = append(checks, doctorCheck{
			Name:   "Live health check",
			Status: doctorWarn,
			Detail: "skipped (no --url provided)",
			Hint:   "run with --url http://127.0.0.1:8080 after starting the hub",
		})
	}

	return printDoctorReport(stdout, "hub", checks)
}

func validateHubConfig(cfg *config.HubConfig) []doctorCheck {
	var checks []doctorCheck

	if _, err := nostr.KeyPairFromPrivHex(cfg.NostrPrivkey); err != nil {
		checks = append(checks, doctorCheck{
			Name:   "nostr_privkey",
			Status: doctorFail,
			Detail: err.Error(),
			Hint:   "regenerate config with: arfl init hub --output hub.json",
		})
	} else {
		checks = append(checks, doctorCheck{
			Name:   "nostr_privkey",
			Status: doctorPass,
			Detail: "valid 32-byte hex key",
		})
	}

	cred, err := hex.DecodeString(strings.TrimSpace(cfg.CredentialKey))
	if err != nil || len(cred) != 32 {
		checks = append(checks, doctorCheck{
			Name:   "credential_key",
			Status: doctorFail,
			Detail: "must be 32-byte hex value",
			Hint:   "regenerate with: openssl rand -hex 32",
		})
	} else {
		checks = append(checks, doctorCheck{
			Name:   "credential_key",
			Status: doctorPass,
			Detail: "valid 32-byte hex key",
		})
	}

	if _, _, err := net.SplitHostPort(cfg.ListenAddr); err != nil {
		checks = append(checks, doctorCheck{
			Name:   "listen_addr",
			Status: doctorFail,
			Detail: err.Error(),
			Hint:   "use host:port format, e.g. 0.0.0.0:8080",
		})
	} else {
		checks = append(checks, doctorCheck{
			Name:   "listen_addr",
			Status: doctorPass,
			Detail: cfg.ListenAddr,
		})
	}

	if len(cfg.Relays) == 0 {
		checks = append(checks, doctorCheck{
			Name:   "relays",
			Status: doctorFail,
			Detail: "no Nostr relays configured",
			Hint:   "set at least one relay URL in hub.json",
		})
	} else {
		checks = append(checks, doctorCheck{
			Name:   "relays",
			Status: doctorPass,
			Detail: fmt.Sprintf("%d configured", len(cfg.Relays)),
		})
	}

	if cfg.LNDHost == "" || cfg.LNDPort <= 0 {
		checks = append(checks, doctorCheck{
			Name:   "LND endpoint",
			Status: doctorFail,
			Detail: "missing lnd_host or lnd_port",
			Hint:   "set lnd_host/lnd_port in config or env overrides",
		})
	} else {
		checks = append(checks, doctorCheck{
			Name:   "LND endpoint",
			Status: doctorPass,
			Detail: fmt.Sprintf("%s:%d", cfg.LNDHost, cfg.LNDPort),
		})
	}

	checks = append(checks, fileCheck("lnd_tls_cert_path", cfg.LNDTLSCertPath)...)
	checks = append(checks, fileCheck("lnd_macaroon_path", cfg.LNDMacaroonPath)...)

	return checks
}

func runDoctorNode(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("doctor node", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	cfgPath := fs.String("config", "node.json", "path to node config file")
	hubURL := fs.String("hub-url", "", "optional hub URL for /health check override")
	nodeURL := fs.String("node-url", "", "optional node connect URL for /health check")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	cfg, err := config.LoadNodeConfig(*cfgPath)
	if err != nil {
		return fmt.Errorf("load node config %q: %w", *cfgPath, err)
	}

	checks := validateNodeConfig(cfg)

	hubHealthURL := strings.TrimSpace(*hubURL)
	if hubHealthURL == "" {
		hubHealthURL = cfg.HubURL
	}
	if hubHealthURL != "" {
		if err := checkHTTPHealth(hubHealthURL); err != nil {
			checks = append(checks, doctorCheck{
				Name:   "Hub /health reachable",
				Status: doctorWarn,
				Detail: err.Error(),
				Hint:   "ensure hub is running and reachable from this node",
			})
		} else {
			checks = append(checks, doctorCheck{
				Name:   "Hub /health reachable",
				Status: doctorPass,
				Detail: "responding at " + strings.TrimRight(hubHealthURL, "/") + "/health",
			})
		}
	} else {
		checks = append(checks, doctorCheck{
			Name:   "Hub /health reachable",
			Status: doctorWarn,
			Detail: "skipped (hub_url empty)",
			Hint:   "set hub_url in node config",
		})
	}

	nodeHealthURL := strings.TrimSpace(*nodeURL)
	if nodeHealthURL != "" {
		if err := checkHTTPHealth(nodeHealthURL); err != nil {
			checks = append(checks, doctorCheck{
				Name:   "Node /health reachable",
				Status: doctorWarn,
				Detail: err.Error(),
				Hint:   "start the node and verify firewall rules",
			})
		} else {
			checks = append(checks, doctorCheck{
				Name:   "Node /health reachable",
				Status: doctorPass,
				Detail: "responding at " + strings.TrimRight(nodeHealthURL, "/") + "/health",
			})
		}
	}

	return printDoctorReport(stdout, "node", checks)
}

func validateNodeConfig(cfg *config.NodeConfig) []doctorCheck {
	var checks []doctorCheck

	switch strings.ToLower(strings.TrimSpace(cfg.Role)) {
	case "entry", "exit", "both":
		checks = append(checks, doctorCheck{
			Name:   "role",
			Status: doctorPass,
			Detail: cfg.Role,
		})
	default:
		checks = append(checks, doctorCheck{
			Name:   "role",
			Status: doctorFail,
			Detail: fmt.Sprintf("invalid role %q", cfg.Role),
			Hint:   "use entry, exit, or both",
		})
	}

	if cfg.ListenPort <= 0 {
		checks = append(checks, doctorCheck{
			Name:   "listen_port",
			Status: doctorFail,
			Detail: "must be > 0",
		})
	} else {
		checks = append(checks, doctorCheck{
			Name:   "listen_port",
			Status: doctorPass,
			Detail: strconv.Itoa(cfg.ListenPort),
		})
	}

	if _, err := wg.ParseKey(cfg.PrivateKey); err != nil {
		checks = append(checks, doctorCheck{
			Name:   "private_key",
			Status: doctorFail,
			Detail: err.Error(),
			Hint:   "regenerate with: arfl init node",
		})
	} else {
		checks = append(checks, doctorCheck{
			Name:   "private_key",
			Status: doctorPass,
			Detail: "valid WireGuard private key",
		})
	}

	if _, _, err := net.SplitHostPort(cfg.Endpoint); err != nil {
		checks = append(checks, doctorCheck{
			Name:   "endpoint",
			Status: doctorFail,
			Detail: err.Error(),
			Hint:   "use host:port format, e.g. 203.0.113.10:51820",
		})
	} else {
		checks = append(checks, doctorCheck{
			Name:   "endpoint",
			Status: doctorPass,
			Detail: cfg.Endpoint,
		})
	}

	if _, _, err := net.SplitHostPort(cfg.ConnectAddr); err != nil {
		checks = append(checks, doctorCheck{
			Name:   "connect_addr",
			Status: doctorFail,
			Detail: err.Error(),
			Hint:   "use host:port format, e.g. 0.0.0.0:9091",
		})
	} else {
		checks = append(checks, doctorCheck{
			Name:   "connect_addr",
			Status: doctorPass,
			Detail: cfg.ConnectAddr,
		})
	}

	if cfg.HubURL == "" {
		checks = append(checks, doctorCheck{
			Name:   "hub_url",
			Status: doctorFail,
			Detail: "is empty",
			Hint:   "set hub_url to your hub base URL",
		})
	} else if _, err := url.ParseRequestURI(cfg.HubURL); err != nil {
		checks = append(checks, doctorCheck{
			Name:   "hub_url",
			Status: doctorFail,
			Detail: err.Error(),
			Hint:   "use full URL, e.g. http://203.0.113.20:8080",
		})
	} else {
		checks = append(checks, doctorCheck{
			Name:   "hub_url",
			Status: doctorPass,
			Detail: cfg.HubURL,
		})
	}

	checks = append(checks, fileCheck("hub_pubkey_file", cfg.HubPubkeyFile)...)

	if len(cfg.Relays) == 0 {
		checks = append(checks, doctorCheck{
			Name:   "relays",
			Status: doctorWarn,
			Detail: "no relays configured",
			Hint:   "set at least one Nostr relay for discovery",
		})
	} else {
		checks = append(checks, doctorCheck{
			Name:   "relays",
			Status: doctorPass,
			Detail: fmt.Sprintf("%d configured", len(cfg.Relays)),
		})
	}

	if len(cfg.EnabledTransports) == 0 {
		checks = append(checks, doctorCheck{
			Name:   "enabled_transports",
			Status: doctorWarn,
			Detail: "empty (legacy WireGuard fallback applies)",
			Hint:   "set enabled_transports to include wireguard",
		})
	} else {
		checks = append(checks, doctorCheck{
			Name:   "enabled_transports",
			Status: doctorPass,
			Detail: strings.Join(cfg.EnabledTransports, ", "),
		})
	}

	return checks
}

func fileCheck(name, p string) []doctorCheck {
	p = strings.TrimSpace(p)
	if p == "" {
		return []doctorCheck{{
			Name:   name,
			Status: doctorFail,
			Detail: "path is empty",
		}}
	}
	expanded := expandHome(p)
	if _, err := os.Stat(expanded); err != nil {
		return []doctorCheck{{
			Name:   name,
			Status: doctorFail,
			Detail: err.Error(),
			Hint:   "ensure the file exists and permissions allow reading",
		}}
	}
	return []doctorCheck{{
		Name:   name,
		Status: doctorPass,
		Detail: expanded,
	}}
}

func printDoctorReport(out io.Writer, target string, checks []doctorCheck) error {
	styled := shouldStyle(out)

	title := fmt.Sprintf("ARFL doctor (%s)", target)
	if styled {
		title = titleStyle.Render(title)
	}
	fmt.Fprintln(out, title)

	divider := strings.Repeat("-", 64)
	if styled {
		divider = borderStyle.Render(strings.Repeat("─", 64))
	}
	fmt.Fprintln(out, divider)

	failCount := 0
	warnCount := 0

	for _, c := range checks {
		label := fmt.Sprintf("[%s]", c.Status)
		if styled {
			switch c.Status {
			case doctorPass:
				label = passStyle.Render(label)
			case doctorWarn:
				label = warnStyle.Render(label)
			case doctorFail:
				label = failStyle.Render(label)
			}
		}
		fmt.Fprintf(out, "%s %s: %s\n", label, c.Name, c.Detail)
		if c.Hint != "" && (c.Status == doctorFail || c.Status == doctorWarn) {
			hintLabel := "hint"
			if styled {
				hintLabel = mutedStyle.Render("hint")
			}
			fmt.Fprintf(out, "       %s: %s\n", hintLabel, c.Hint)
		}
		if c.Status == doctorFail {
			failCount++
		}
		if c.Status == doctorWarn {
			warnCount++
		}
	}
	fmt.Fprintln(out, divider)
	summary := fmt.Sprintf("summary: %d fail, %d warn, %d pass", failCount, warnCount, len(checks)-failCount-warnCount)
	if styled {
		switch {
		case failCount > 0:
			summary = failStyle.Render(summary)
		case warnCount > 0:
			summary = warnStyle.Render(summary)
		default:
			summary = summaryStyle.Foreground(lipgloss.Color("42")).Render(summary)
		}
	}
	fmt.Fprintln(out, summary)
	if failCount > 0 {
		return fmt.Errorf("doctor found %d blocking issue(s)", failCount)
	}
	return nil
}

func checkHTTPHealth(baseURL string) error {
	baseURL = strings.TrimSpace(strings.TrimRight(baseURL, "/"))
	if baseURL == "" {
		return fmt.Errorf("empty URL")
	}
	healthURL := baseURL + "/health"
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(healthURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %s", resp.Status)
	}
	return nil
}

func parseCSV(in string) []string {
	parts := strings.Split(in, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

func writeJSON(path string, v any, perm os.FileMode) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("output path is required")
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, append(data, '\n'), perm); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Chmod(path, perm); err != nil {
		return fmt.Errorf("set permissions on %s: %w", path, err)
	}
	return nil
}

func guardOutputPath(path string, force bool) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("output path is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("check output path %s: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("output path %s is a directory", path)
	}
	if !force {
		return fmt.Errorf("refusing to overwrite existing file %s (rerun with --force)", path)
	}
	backup := fmt.Sprintf("%s.bak.%d", path, time.Now().Unix())
	if err := copyFile(path, backup, info.Mode()); err != nil {
		return fmt.Errorf("backup existing file before overwrite: %w", err)
	}
	return nil
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	return nil
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func expandHome(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
