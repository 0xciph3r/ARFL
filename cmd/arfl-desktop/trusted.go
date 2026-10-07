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
func (t *trustedHubs) keepFresh(ctx context.Context) {
	cache := registryCache()
	if cache.Path != "" {
		if l, at := cache.Load(); !at.IsZero() {
			t.set(l)
		}
	}
	client := &http.Client{Timeout: 20 * time.Second}
	for {
		if l, err := registry.Fetch(ctx, client); err != nil {
			fmt.Printf("arfl-desktop: refresh trusted hubs: %v\n", err)
		} else {
			t.set(l)
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
