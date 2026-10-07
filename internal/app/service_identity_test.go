package app

import (
	"context"
	"strings"
	"testing"

	"github.com/Radi-Labs/ARFL/internal/discovery"
	"github.com/Radi-Labs/ARFL/internal/nostr"
	"github.com/Radi-Labs/ARFL/pkg/types"
)

func TestIndexedToNodeInfosUsesSignedEventIdentity(t *testing.T) {
	nodes := indexedToNodeInfos([]*discovery.IndexedNode{
		{Info: types.NodeInfo{NostrPubkey: "substituted"}, Event: &nostr.Event{Pubkey: "verified"}},
		{Info: types.NodeInfo{NostrPubkey: "unverified"}},
	})
	if len(nodes) != 1 || nodes[0].NostrPubkey != "verified" {
		t.Fatalf("node identities = %+v, want only the signed event identity", nodes)
	}
}

func TestResolveConnectIPv4RejectsUnusableURLs(t *testing.T) {
	for _, raw := range []string{"http://", "ftp://node.example", "http://user@node.example", "http://127.0.0.1:", "http://node.example?x=1"} {
		t.Run(raw, func(t *testing.T) {
			_, _, err := resolveConnectIPv4(context.Background(), raw)
			if err == nil || !strings.Contains(err.Error(), "connect URL") {
				t.Fatalf("URL %q should fail before resolving: %v", raw, err)
			}
		})
	}
}
