// Package client — NodeConnector handles presenting blind tokens to ARFL
// nodes in exchange for WireGuard access.
//
// Flow:
//  1. Client discovers nodes (via Nostr or static config)
//  2. Client calls Connect() on entry node with a token + WG pubkey
//  3. Client calls Connect() on exit node with a token + WG pubkey
//  4. Nodes verify tokens, add WG peers, return tunnel IPs
//  5. Client creates WG tunnels using the returned IPs
package client

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Radi-Labs/ARFL/internal/credentials"
)

// NodeConnector presents blind tokens to a node's /connect endpoint.
type NodeConnector struct {
	httpClient *http.Client
}

// NewNodeConnector creates a connector with sensible defaults.
func NewNodeConnector() *NodeConnector {
	return &NodeConnector{
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// ConnectResult holds the response from a successful node connection.
type ConnectResult struct {
	TunnelIP     string `json:"tunnel_ip"`      // Assigned IP (e.g. "10.100.0.2/32")
	NodeWGPubkey string `json:"node_wg_pubkey"` // Node's WG pubkey (base64)
	BytesAllowed int64  `json:"bytes_allowed"`  // Bandwidth quota from this token
}

// Connect presents a blind token to a node's /connect endpoint.
// connectURL is the node's connect API base (e.g. "http://1.2.3.4:9090").
// token is an unspent BlindToken from the client's token store.
// clientWGPubkey is the client's WireGuard public key (base64).
func (nc *NodeConnector) Connect(ctx context.Context, connectURL string, token *credentials.BlindToken, clientWGPubkey string) (*ConnectResult, error) {
	reqBody, err := json.Marshal(connectRequest{
		Token: connectToken{
			Version:     token.Version,
			KeyID:       token.KeyID,
			TokenSecret: hex.EncodeToString(token.TokenSecret),
			Signature:   hex.EncodeToString(token.Signature),
		},
		WGPubkey: clientWGPubkey,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal connect request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, connectURL+"/connect", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := nc.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connect request to %s: %w", connectURL, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return nil, fmt.Errorf("node rejected token (%d): %s", resp.StatusCode, errResp.Error)
		}
		return nil, fmt.Errorf("node connect error (%d): %s", resp.StatusCode, string(body))
	}

	var result ConnectResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode connect response: %w", err)
	}

	if result.TunnelIP == "" || result.NodeWGPubkey == "" {
		return nil, fmt.Errorf("node returned incomplete response (missing tunnel_ip or wg_pubkey)")
	}

	return &result, nil
}

// --- Request types (mirrors control.ConnectRequest) ---

type connectRequest struct {
	Token    connectToken `json:"token"`
	WGPubkey string       `json:"wg_pubkey"`
}

type connectToken struct {
	Version     uint8  `json:"version"`
	KeyID       string `json:"key_id"`
	TokenSecret string `json:"token_secret"`
	Signature   string `json:"signature"`
}
