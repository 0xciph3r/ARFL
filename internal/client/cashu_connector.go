// CashuConnector presents Cashu ecash proofs to ARFL nodes in exchange for
// WireGuard tunnel access. This replaces the RSA blind token flow from v0
// with the NUT-compliant Cashu tokens minted by the hub.
//
// Flow:
//  1. Client mints Cashu tokens via hub (POST /v1/mint/bolt11)
//  2. Client selects entry/exit nodes client-side (NodeSelector)
//  3. Client calls ConnectWithProofs() on each node
//  4. Node calls hub POST /v1/redeem to verify + burn the proofs
//  5. Node grants WireGuard access and returns tunnel config
//
// Privacy: The hub issued the tokens via blind signatures. It cannot link
// the redeemed proofs back to the original buyer.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/elnosh/gonuts/cashu"
)

// CashuConnector presents Cashu proofs to a node for WireGuard access.
type CashuConnector struct {
	httpClient *http.Client
}

// NewCashuConnector creates a connector with sensible defaults.
func NewCashuConnector() *CashuConnector {
	return &CashuConnector{
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

// CashuConnectRequest is sent to the node's /cashu-connect endpoint.
type CashuConnectRequest struct {
	Proofs   cashu.Proofs `json:"proofs"`
	WGPubkey string       `json:"wg_pubkey"`
}

// ConnectWithProofs presents Cashu proofs to a node and receives WireGuard
// tunnel configuration in return.
//
// connectURL: node's connect API base (e.g. "https://1.2.3.4:9091")
// proofs: unspent Cashu proofs from the client's token wallet
// clientWGPubkey: the client's WireGuard public key (base64)
func (cc *CashuConnector) ConnectWithProofs(
	ctx context.Context,
	connectURL string,
	proofs cashu.Proofs,
	clientWGPubkey string,
) (*ConnectResult, error) {
	if len(proofs) == 0 {
		return nil, fmt.Errorf("no proofs provided")
	}
	if clientWGPubkey == "" {
		return nil, fmt.Errorf("wg_pubkey required")
	}

	reqBody, err := json.Marshal(CashuConnectRequest{
		Proofs:   proofs,
		WGPubkey: clientWGPubkey,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal connect request: %w", err)
	}

	// The node serves two gates on this port: /connect takes the legacy RSA
	// blind tokens, /cashu-connect takes Cashu proofs. Posting proofs to
	// /connect decodes into an empty token and is rejected, so the path must
	// match the credential being presented.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, connectURL+"/cashu-connect", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := cc.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connect request to %s: %w", connectURL, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error  string `json:"error"`
			Detail string `json:"detail"`
		}
		if json.Unmarshal(body, &errResp) == nil {
			msg := errResp.Error
			if msg == "" {
				msg = errResp.Detail
			}
			if msg != "" {
				return nil, fmt.Errorf("node rejected proofs (%d): %s", resp.StatusCode, msg)
			}
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

// ConnectPair connects to both entry and exit nodes using Cashu proofs.
// Proofs are split between the two nodes — each gets half the bandwidth.
func (cc *CashuConnector) ConnectPair(
	ctx context.Context,
	pair *NodePair,
	entryProofs cashu.Proofs,
	exitProofs cashu.Proofs,
	clientWGPubkey string,
) (entry *ConnectResult, exit *ConnectResult, err error) {
	// Connect to entry node first.
	entry, err = cc.ConnectWithProofs(ctx, pair.Entry.ConnectURL, entryProofs, clientWGPubkey)
	if err != nil {
		return nil, nil, fmt.Errorf("entry node connect: %w", err)
	}

	// Then exit node.
	exit, err = cc.ConnectWithProofs(ctx, pair.Exit.ConnectURL, exitProofs, clientWGPubkey)
	if err != nil {
		return entry, nil, fmt.Errorf("exit node connect (entry succeeded): %w", err)
	}

	return entry, exit, nil
}
