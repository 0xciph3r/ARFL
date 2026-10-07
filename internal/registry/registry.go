// Package registry is the list of hubs ARFL trusts by default.
//
// The list lives in the repository at hubs/registry.json. Hub operators add
// themselves through a pull request, and merging it is the approval: clients
// read the file from the main branch over HTTPS, so a merged hub appears in
// every app within a day without a release. Only people who can merge can
// publish, the same people who can change the app itself.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultURL is where clients fetch the published list.
const DefaultURL = "https://raw.githubusercontent.com/0xciph3r/ARFL/main/hubs/registry.json"

// fetchURL is DefaultURL except in tests.
var fetchURL = DefaultURL

// URLForTest points Fetch elsewhere and returns the previous address.
func URLForTest(u string) string {
	old := fetchURL
	fetchURL = u
	return old
}

// maxSize bounds the download; the list is a few kilobytes.
const maxSize = 256 * 1024

// maxHubs keeps a bad merge from flooding the hub chooser.
const maxHubs = 200

// Hub is one trusted hub.
type Hub struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Operator string `json:"operator"`
	// NostrPubkey is the hub's hex key, used to verify its node attestations.
	NostrPubkey string `json:"nostr_pubkey,omitempty"`
	Contact     string `json:"contact,omitempty"`
	Added       string `json:"added,omitempty"`
}

// List is the file's shape.
type List struct {
	Version int   `json:"version"`
	Hubs    []Hub `json:"hubs"`
}

// Fallback is used before the first successful fetch.
var Fallback = List{Version: 1, Hubs: []Hub{{
	Name:     "ARFL Community Hub",
	URL:      "https://hub.arfl.us",
	Operator: "ARFL",
}}}

// Parse decodes and validates a list, dropping nothing silently: one bad
// entry rejects the whole file, so a mistake is caught in review rather than
// half-shipped.
func Parse(raw []byte) (List, error) {
	var l List
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return List{}, fmt.Errorf("parse registry: %w", err)
	}
	return l, l.Validate()
}

// Validate checks every entry.
func (l List) Validate() error {
	if l.Version != 1 {
		return fmt.Errorf("unsupported registry version %d", l.Version)
	}
	if len(l.Hubs) == 0 {
		return errors.New("registry lists no hubs")
	}
	if len(l.Hubs) > maxHubs {
		return fmt.Errorf("registry lists %d hubs, more than %d", len(l.Hubs), maxHubs)
	}
	seen := map[string]bool{}
	for i, h := range l.Hubs {
		if err := h.validate(); err != nil {
			return fmt.Errorf("hub %d (%q): %w", i+1, h.Name, err)
		}
		if seen[h.URL] {
			return fmt.Errorf("hub %d: %s is listed twice", i+1, h.URL)
		}
		seen[h.URL] = true
	}
	return nil
}

func (h Hub) validate() error {
	if strings.TrimSpace(h.Name) == "" || len(h.Name) > 64 {
		return errors.New("name must be 1-64 characters")
	}
	if strings.TrimSpace(h.Operator) == "" {
		return errors.New("operator is required")
	}
	u, err := url.Parse(h.URL)
	if err != nil {
		return fmt.Errorf("bad url: %w", err)
	}
	// Plain HTTP would expose token purchases on the network, and a bare IP
	// cannot carry a certificate tied to the operator's name.
	if u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" {
		return errors.New("url must be https://host with no path, e.g. https://hub.example.com")
	}
	if host := u.Hostname(); strings.Trim(host, "0123456789.:[]") == "" {
		return errors.New("url must use a domain name, not an IP address")
	}
	if h.NostrPubkey != "" && (len(h.NostrPubkey) != 64 || strings.Trim(strings.ToLower(h.NostrPubkey), "0123456789abcdef") != "") {
		return errors.New("nostr_pubkey must be 64 hex characters")
	}
	return nil
}

// Contains reports whether url is a listed hub.
func (l List) Contains(url string) bool {
	for _, h := range l.Hubs {
		if h.URL == url {
			return true
		}
	}
	return false
}

// Fetch downloads and validates the published list.
func Fetch(ctx context.Context, client *http.Client) (List, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return List{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return List{}, fmt.Errorf("fetch registry: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return List{}, fmt.Errorf("fetch registry: %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return List{}, err
	}
	if len(raw) > maxSize {
		return List{}, errors.New("registry is too large")
	}
	return Parse(raw)
}

// Cache keeps the last good list on disk so the app works offline and a
// failed fetch never empties the hub chooser.
type Cache struct {
	Path string
}

// Load returns the cached list, or Fallback when there is none.
func (c Cache) Load() (List, time.Time) {
	raw, err := os.ReadFile(c.Path)
	if err != nil {
		return Fallback, time.Time{}
	}
	l, err := Parse(raw)
	if err != nil {
		return Fallback, time.Time{}
	}
	info, _ := os.Stat(c.Path)
	return l, info.ModTime()
}

// Save stores a validated list.
func (c Cache) Save(l List) error {
	raw, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o700); err != nil {
		return err
	}
	tmp := c.Path + ".new"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.Path)
}
