// Command arfl-registry checks hubs/registry.json before a pull request is
// merged: every entry must be well formed and every hub must be live.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/Radi-Labs/ARFL/internal/registry"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "check" {
		fmt.Fprintln(os.Stderr, "usage: arfl-registry check [path/to/registry.json]")
		os.Exit(2)
	}
	path := "hubs/registry.json"
	if len(os.Args) > 2 {
		path = os.Args[2]
	}
	if err := check(path); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}

func check(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	list, err := registry.Parse(raw)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	failed := 0
	for _, h := range list.Hubs {
		if err := checkHub(client, h); err != nil {
			fmt.Printf("✗ %s (%s): %v\n", h.Name, h.URL, err)
			failed++
			continue
		}
		fmt.Printf("✓ %s (%s)\n", h.Name, h.URL)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d hubs failed", failed, len(list.Hubs))
	}
	return nil
}

func checkHub(client *http.Client, h registry.Hub) error {
	var info struct {
		Name string `json:"name"`
	}
	if err := getJSON(client, h.URL+"/info", &info); err != nil {
		return fmt.Errorf("info: %w", err)
	}
	if info.Name != h.Name {
		return fmt.Errorf("hub calls itself %q, registry says %q", info.Name, h.Name)
	}
	var health struct {
		Nodes struct {
			Online int `json:"online"`
		} `json:"nodes"`
	}
	if err := getJSON(client, h.URL+"/health", &health); err != nil {
		return fmt.Errorf("health: %w", err)
	}
	if health.Nodes.Online < 1 {
		return fmt.Errorf("no nodes online")
	}
	return nil
}

func getJSON(client *http.Client, url string, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
