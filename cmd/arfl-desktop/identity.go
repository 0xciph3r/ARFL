package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Radi-Labs/ARFL/internal/config"
	"github.com/Radi-Labs/ARFL/internal/wallet"
	"github.com/elnosh/gonuts/cashu"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/zalando/go-keyring"
)

// The device key is the user's only identity. It never leaves this machine
// except inside a passphrase-encrypted backup, and it seals the token vault so
// the app opens without a password: the OS keychain guards it instead.
const (
	keyringService = "ARFL"
	keyringUser    = "device-key"
	deviceKeyLen   = 32
	// ARFL Community Hub, the hub the old bare-IP address now maps to.
	defaultHubURL = "https://hub.arfl.us"
)

// SetupView tells the UI which first-launch path to show.
type SetupView struct {
	HasKey      bool   `json:"has_key"`
	Fingerprint string `json:"fingerprint,omitempty"`
	// LegacyVault is a wallet from before device keys, still sealed by a
	// passphrase. It must be unlocked once to move it onto a device key.
	LegacyVault bool `json:"legacy_vault"`
}

// HubPreview is what the hub chooser shows before the user commits to a hub.
type HubPreview struct {
	URL       string `json:"url"`
	Name      string `json:"name"`
	MarginPct int    `json:"margin_pct"`
	NodeCount int    `json:"node_count"`
	// Trusted is true for hubs on the ARFL trusted list.
	Trusted bool `json:"trusted"`
}

// RestoredHub is one hub's balance after a backup is restored.
type RestoredHub struct {
	HubURL  string `json:"hub_url"`
	Name    string `json:"name"`
	Sats    uint64 `json:"sats"`
	Dropped uint64 `json:"dropped_sats"`
	Error   string `json:"error,omitempty"`
}

func loadDeviceKey() ([]byte, error) {
	enc, err := keyring.Get(keyringService, keyringUser)
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(enc)
}

func saveDeviceKey(key []byte) error {
	return keyring.Set(keyringService, keyringUser, base64.StdEncoding.EncodeToString(key))
}

// vaultSecret is what seals the token vault. The vault still runs it through
// Argon2id, which is harmless for a random key and keeps one file format.
func vaultSecret(key []byte) string {
	return base64.StdEncoding.EncodeToString(key)
}

// fingerprint renders the device key as four groups the user can compare,
// for example "7K2M · Q9XA · 4DW8 · RH3P".
func fingerprint(key []byte) string {
	sum := sha256.Sum256(key)
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:])
	return strings.Join([]string{enc[0:4], enc[4:8], enc[8:12], enc[12:16]}, " · ")
}

func vaultExists() (bool, error) {
	path, err := wallet.DefaultStorePath()
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// Setup reports whether this device already has a key.
func (b *Bridge) Setup() (*SetupView, error) {
	key, err := loadDeviceKey()
	if err == nil {
		return &SetupView{HasKey: true, Fingerprint: fingerprint(key)}, nil
	}
	if !errors.Is(err, keyring.ErrNotFound) {
		// Still show first launch; Generate key reports the keychain problem
		// where the user can act on it, instead of a blank error page.
		fmt.Printf("arfl-desktop: read device key: %v\n", err)
	}
	exists, err := vaultExists()
	if err != nil {
		return nil, err
	}
	return &SetupView{LegacyVault: exists}, nil
}

// CreateKey makes the device key and an empty vault sealed by it.
func (b *Bridge) CreateKey() (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.svc != nil {
		return "", fmt.Errorf("a wallet is already open")
	}
	if key, err := loadDeviceKey(); err == nil {
		return fingerprint(key), nil
	}
	if exists, err := vaultExists(); err != nil {
		return "", err
	} else if exists {
		return "", fmt.Errorf("this device already has a wallet; unlock it to move it onto a device key")
	}

	key := make([]byte, deviceKeyLen)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("generate device key: %w", err)
	}
	if err := saveDeviceKey(key); err != nil {
		return "", fmt.Errorf("save the device key in the system keychain: %w", err)
	}
	if _, err := b.openLocked(vaultSecret(key)); err != nil {
		return "", err
	}
	return fingerprint(key), nil
}

// OpenWallet opens the vault with the device key on every launch after the
// first, so the user is never asked for a password.
func (b *Bridge) OpenWallet() (*StatusView, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.svc != nil {
		return b.status(b.svc), nil
	}
	key, err := loadDeviceKey()
	if err != nil {
		return nil, fmt.Errorf("read the device key from the system keychain: %w", err)
	}
	return b.openLocked(vaultSecret(key))
}

// UpgradeLegacy moves a passphrase wallet onto a new device key. The old file
// is kept beside the new one until the move has fully succeeded.
func (b *Bridge) UpgradeLegacy(passphrase string) (*StatusView, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.svc != nil {
		return b.status(b.svc), nil
	}
	path, err := wallet.DefaultStorePath()
	if err != nil {
		return nil, err
	}
	old, err := wallet.OpenProofStore(path, passphrase)
	if err != nil {
		return nil, errors.New("wrong passphrase for this wallet")
	}
	held := old.Snapshot()

	aside := fmt.Sprintf("%s.pre-device-key-%s", path, time.Now().Format("20060102-150405"))
	if err := os.Rename(path, aside); err != nil {
		return nil, fmt.Errorf("set the old wallet aside: %w", err)
	}
	key := make([]byte, deviceKeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate device key: %w", err)
	}
	if err := saveDeviceKey(key); err != nil {
		_ = os.Rename(aside, path)
		return nil, fmt.Errorf("save the device key in the system keychain: %w", err)
	}
	if _, err := b.openLocked(vaultSecret(key)); err != nil {
		return nil, err
	}
	for hub, proofs := range held {
		if err := b.svc.ImportProofs(hub, proofs); err != nil {
			return nil, fmt.Errorf("move tokens for %s: %w", hub, err)
		}
	}
	_ = os.Remove(aside)
	return b.status(b.svc), nil
}

// Fingerprint returns the device key's fingerprint, or "" before one exists.
func (b *Bridge) Fingerprint() string {
	key, err := loadDeviceKey()
	if err != nil {
		return ""
	}
	return fingerprint(key)
}

// RecommendedHubs lists the hubs the chooser offers before the user adds one.
func (b *Bridge) RecommendedHubs() []string {
	var urls []string
	cfgPath := strings.TrimSpace(os.Getenv("ARFL_CLIENT_CONFIG"))
	if cfgPath == "" {
		cfgPath = "client.json"
	}
	// A hub named in client.json comes first: it is the operator's own choice.
	if cfg, err := config.LoadClientConfig(cfgPath); err == nil && strings.TrimSpace(cfg.HubURL) != "" {
		urls = append(urls, currentHubURL(strings.TrimSpace(cfg.HubURL)))
	}
	for _, h := range trusted.get().Hubs {
		if !containsString(urls, h.URL) {
			urls = append(urls, h.URL)
		}
	}
	return urls
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// PreviewHub reads a hub's public info without connecting to it.
func (b *Bridge) PreviewHub(hubURL string) (*HubPreview, error) {
	mc, err := wallet.NewMintClient(hubURL)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(b.context(), 10*time.Second)
	defer cancel()
	info, err := mc.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not reach that hub: %w", err)
	}
	name := info.Name
	if name == "" {
		name = mc.HubURL()
	}
	return &HubPreview{URL: mc.HubURL(), Name: name, MarginPct: info.HubMarginPct, NodeCount: info.NodeCount, Trusted: trusted.get().Contains(mc.HubURL())}, nil
}

// ExportBackup writes the key and every token to a file the user picks,
// encrypted with passphrase. It returns the path, or "" if they cancelled.
func (b *Bridge) ExportBackup(passphrase string) (string, error) {
	svc, _, err := b.ready()
	if err != nil {
		return "", err
	}
	key, err := loadDeviceKey()
	if err != nil {
		return "", fmt.Errorf("read the device key from the system keychain: %w", err)
	}
	sealed, err := wallet.SealBackup(key, svc.Snapshot(), passphrase)
	if err != nil {
		return "", err
	}
	path, err := application.Get().Dialog.SaveFile().
		SetMessage("Save your ARFL backup").
		SetFilename("arfl-backup-"+time.Now().Format("2006-01-02")+".arflbak").
		AddFilter("ARFL backup", "*.arflbak").
		PromptForSingleSelection()
	if err != nil || path == "" {
		return "", err
	}
	if err := os.WriteFile(path, sealed, 0o600); err != nil {
		return "", fmt.Errorf("write backup: %w", err)
	}
	return path, nil
}

// ChooseBackupFile asks the user for a backup to restore. "" means cancelled.
func (b *Bridge) ChooseBackupFile() (string, error) {
	return application.Get().Dialog.OpenFile().
		SetTitle("Choose your ARFL backup").
		CanChooseFiles(true).
		AddFilter("ARFL backup", "*.arflbak").
		PromptForSingleSelection()
}

// RestoreBackup brings a backed-up key and its tokens to this device. Each
// hub is asked which tokens are still unspent, and spent ones are dropped.
func (b *Bridge) RestoreBackup(path, passphrase string) ([]RestoredHub, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read backup: %w", err)
	}
	key, held, err := wallet.OpenBackup(raw, passphrase)
	if err != nil {
		return nil, err
	}

	b.mu.Lock()
	if b.svc != nil {
		b.mu.Unlock()
		return nil, fmt.Errorf("a wallet is already open on this device")
	}
	if storePath, err := wallet.DefaultStorePath(); err == nil {
		if _, statErr := os.Stat(storePath); statErr == nil {
			aside := fmt.Sprintf("%s.replaced-%s", storePath, time.Now().Format("20060102-150405"))
			if err := os.Rename(storePath, aside); err != nil {
				b.mu.Unlock()
				return nil, fmt.Errorf("set the existing wallet aside: %w", err)
			}
		}
	}
	if err := saveDeviceKey(key); err != nil {
		b.mu.Unlock()
		return nil, fmt.Errorf("save the device key in the system keychain: %w", err)
	}
	if _, err := b.openLocked(vaultSecret(key)); err != nil {
		b.mu.Unlock()
		return nil, err
	}
	svc := b.svc
	b.mu.Unlock()

	ctx, cancel := context.WithTimeout(b.context(), 60*time.Second)
	defer cancel()

	var out []RestoredHub
	for hub, proofs := range held {
		row := RestoredHub{HubURL: hub, Name: hub}
		mc, err := wallet.NewMintClient(hub)
		if err == nil {
			if info, ierr := mc.Info(ctx); ierr == nil && info.Name != "" {
				row.Name = info.Name
			}
			var live cashu.Proofs
			if live, err = mc.UnspentProofs(ctx, proofs); err == nil {
				row.Sats = live.Amount()
				row.Dropped = proofs.Amount() - live.Amount()
				err = svc.ImportProofs(hub, live)
			}
		}
		if err != nil {
			// Keep every token when the hub can't be asked: dropping them would
			// lose real money, and a spent one is refused safely on first use.
			row.Error = err.Error()
			row.Sats = proofs.Amount()
			_ = svc.ImportProofs(hub, proofs)
		}
		out = append(out, row)
	}
	return out, nil
}

func (b *Bridge) context() context.Context {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ctx == nil {
		return context.Background()
	}
	return b.ctx
}

// KnownHub is a hub the user can switch to, with what they hold there.
type KnownHub struct {
	HubPreview
	Sats      uint64 `json:"sats"`
	Reachable bool   `json:"reachable"`
	Custom    bool   `json:"custom"`
}

// KnownHubs lists the recommended hubs plus every hub this wallet holds
// tokens at, each with its public info and the local balance there.
func (b *Bridge) KnownHubs() ([]KnownHub, error) {
	svc, _, err := b.ready()
	if err != nil {
		return nil, err
	}
	held := svc.Snapshot()
	recommended := b.RecommendedHubs()

	seen := map[string]bool{}
	var urls []string
	for _, u := range recommended {
		if mc, err := wallet.NewMintClient(u); err == nil && !seen[mc.HubURL()] {
			seen[mc.HubURL()] = true
			urls = append(urls, mc.HubURL())
		}
	}
	if cur := svc.HubURL(); cur != "" && !seen[cur] {
		seen[cur] = true
		urls = append(urls, cur)
	}
	for u := range held {
		if !seen[u] {
			seen[u] = true
			urls = append(urls, u)
		}
	}

	out := make([]KnownHub, 0, len(urls))
	trustedList := trusted.get()
	for _, u := range urls {
		row := KnownHub{HubPreview: HubPreview{URL: u, Name: strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")}, Custom: !trustedList.Contains(u)}
		if p, err := b.PreviewHub(u); err == nil {
			row.HubPreview = *p
			row.Reachable = true
		}
		row.Sats = held[u].Amount()
		out = append(out, row)
	}
	return out, nil
}

// HeldSats is the value of every token on this device, across all hubs. It is
// what a lost device without a backup would take with it.
func (b *Bridge) HeldSats() (uint64, error) {
	svc, _, err := b.ready()
	if err != nil {
		return 0, err
	}
	var total uint64
	for _, proofs := range svc.Snapshot() {
		total += proofs.Amount()
	}
	return total, nil
}

// KeyTransfer returns the device key in the form the "Move to a new device"
// QR carries. Tokens are deliberately not included; they travel only in the
// encrypted backup file.
func (b *Bridge) KeyTransfer() (string, error) {
	key, err := loadDeviceKey()
	if err != nil {
		return "", fmt.Errorf("read the device key from the system keychain: %w", err)
	}
	return "arfl-key:" + base64.RawURLEncoding.EncodeToString(key), nil
}
