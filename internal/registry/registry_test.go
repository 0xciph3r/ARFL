package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

const good = `{"version":1,"hubs":[{"name":"ARFL Community Hub","url":"https://hub.arfl.us","operator":"ARFL"}]}`

func TestParseAcceptsAWellFormedList(t *testing.T) {
	l, err := Parse([]byte(good))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !l.Contains("https://hub.arfl.us") {
		t.Fatal("listed hub not found")
	}
}

func TestParseRejectsUnsafeEntries(t *testing.T) {
	cases := map[string]string{
		"plain http":    `{"version":1,"hubs":[{"name":"A","url":"http://hub.example.com","operator":"x"}]}`,
		"bare ip":       `{"version":1,"hubs":[{"name":"A","url":"https://203.0.113.5:8080","operator":"x"}]}`,
		"path":          `{"version":1,"hubs":[{"name":"A","url":"https://hub.example.com/x","operator":"x"}]}`,
		"no operator":   `{"version":1,"hubs":[{"name":"A","url":"https://hub.example.com","operator":""}]}`,
		"bad key":       `{"version":1,"hubs":[{"name":"A","url":"https://hub.example.com","operator":"x","nostr_pubkey":"zz"}]}`,
		"duplicate":     `{"version":1,"hubs":[{"name":"A","url":"https://h.example.com","operator":"x"},{"name":"B","url":"https://h.example.com","operator":"y"}]}`,
		"unknown field": `{"version":1,"hubs":[{"name":"A","url":"https://h.example.com","operator":"x","fee":1}]}`,
		"empty":         `{"version":1,"hubs":[]}`,
		"wrong version": `{"version":2,"hubs":[{"name":"A","url":"https://h.example.com","operator":"x"}]}`,
	}
	for name, raw := range cases {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: accepted, want rejected", name)
		}
	}
}

func TestCacheFallsBackAndRoundTrips(t *testing.T) {
	c := Cache{Path: filepath.Join(t.TempDir(), "hubs.json")}
	if l, at := c.Load(); !at.IsZero() || !l.Contains(Fallback.Hubs[0].URL) {
		t.Fatal("empty cache should return the fallback list")
	}
	l, _ := Parse([]byte(good))
	if err := c.Save(l); err != nil {
		t.Fatal(err)
	}
	if got, at := c.Load(); at.IsZero() || len(got.Hubs) != 1 {
		t.Fatalf("cache did not round trip: %+v", got)
	}
}

func TestFetchRejectsAnInvalidPublishedList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Replace(good, "https://", "http://", 1)))
	}))
	defer srv.Close()
	old := URLForTest(srv.URL)
	defer URLForTest(old)
	if _, err := Fetch(context.Background(), srv.Client()); err == nil {
		t.Fatal("a list with an http hub was accepted")
	}
}
