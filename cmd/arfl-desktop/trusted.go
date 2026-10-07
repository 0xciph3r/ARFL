package main

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/Radi-Labs/ARFL/internal/registry"
	"github.com/Radi-Labs/ARFL/internal/wallet"
)

// registryRefresh is how often the trusted hub list is re-read.
const registryRefresh = 24 * time.Hour

// trustedHubs holds the latest trusted hub list, kept fresh in the
// background and cached on disk for offline starts.
type trustedHubs struct {
	mu   sync.Mutex
	list registry.List
}

var trusted = &trustedHubs{list: registry.Fallback}

func registryCache() registry.Cache {
	vault, err := wallet.DefaultStorePath()
	if err != nil {
		return registry.Cache{}
	}
	return registry.Cache{Path: filepath.Join(filepath.Dir(vault), "trusted-hubs.json")}
}

func (t *trustedHubs) get() registry.List {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.list
}

func (t *trustedHubs) set(l registry.List) {
	t.mu.Lock()
	t.list = l
	t.mu.Unlock()
}

// keepFresh loads the cached list, then fetches the published one now and
// every day until ctx ends. A failed fetch keeps the last good list.
// updated is called after each change so the open wallet picks it up.
func (t *trustedHubs) keepFresh(ctx context.Context, updated func()) {
	cache := registryCache()
	if cache.Path != "" {
		if l, at := cache.Load(); !at.IsZero() {
			t.set(l)
			updated()
		}
	}
	client := &http.Client{Timeout: 20 * time.Second}
	for {
		if l, err := registry.Fetch(ctx, client); err != nil {
			fmt.Printf("arfl-desktop: refresh trusted hubs: %v\n", err)
		} else {
			t.set(l)
			updated()
			if cache.Path != "" {
				if err := cache.Save(l); err != nil {
					fmt.Printf("arfl-desktop: cache trusted hubs: %v\n", err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(registryRefresh):
		}
	}
}

// withRegistryKeys adds every trusted hub's key to the configured ones, so a
// hub marked Trusted by ARFL can also sign the node attestations the app
// verifies when it discovers nodes over Nostr.
func withRegistryKeys(configured []string) []string {
	out := append([]string(nil), configured...)
	for _, h := range trusted.get().Hubs {
		if h.NostrPubkey != "" && !containsString(out, h.NostrPubkey) {
			out = append(out, h.NostrPubkey)
		}
	}
	return out
}

// registryUpdated hands a refreshed trusted list's keys to the open wallet.
func (b *Bridge) registryUpdated() {
	b.mu.Lock()
	svc := b.svc
	b.mu.Unlock()
	if svc == nil {
		return
	}
	_, _, _, configured, _, _, err := loadTransportPolicyFromClientConfig()
	if err != nil {
		return
	}
	svc.SetTrustedHubPubkeys(withRegistryKeys(configured))
}
