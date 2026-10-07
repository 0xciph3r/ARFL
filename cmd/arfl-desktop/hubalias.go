package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Radi-Labs/ARFL/internal/app"
	"github.com/Radi-Labs/ARFL/internal/wallet"
)

// hubAliases maps old addresses of a hub to its current one. Tokens are filed
// under the hub URL they were minted at, so without this a hub that moves
// behind a domain would look empty to everyone who bought from it before.
var hubAliases = map[string]string{
	"http://209.250.238.223:8080": defaultHubURL,
}

// migrateHubAliases moves tokens onto current hub addresses, then reconnects
// to the hub the user last chose so they are not asked again on every launch.
func migrateHubAliases(svc *app.Service) {
	for from, to := range hubAliases {
		if err := svc.MoveHub(from, to); err != nil {
			fmt.Printf("arfl-desktop: move tokens from %s to %s: %v\n", from, to, err)
		}
	}
	if last := loadLastHub(); last != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := svc.ConnectHub(ctx, last); err != nil {
			fmt.Printf("arfl-desktop: reopen hub %s: %v\n", last, err)
		}
	}
}

// currentHubURL returns the address to use for a hub the user selected.
func currentHubURL(url string) string {
	if to, ok := hubAliases[url]; ok {
		return to
	}
	return url
}

// lastHubFile records the hub the user chose, so the app reopens on it. It
// holds only the hub's address and sits beside the token vault.
func lastHubFile() (string, error) {
	vault, err := wallet.DefaultStorePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(vault), "last-hub"), nil
}

func saveLastHub(url string) {
	path, err := lastHubFile()
	if err != nil {
		return
	}
	if err := os.WriteFile(path, []byte(url), 0o600); err != nil {
		fmt.Printf("arfl-desktop: remember hub: %v\n", err)
	}
}

func loadLastHub() string {
	path, err := lastHubFile()
	if err != nil {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return currentHubURL(strings.TrimSpace(string(raw)))
}
