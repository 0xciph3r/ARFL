// Package client provides both the bandwidth purchase SDK (BandwidthClient)
// and the node-side token verification SDK (TokenGate).
//
// TokenGate is used by arfl-node to verify and spend BlindTokens presented
// by clients. It verifies the RSA blind signature locally, then calls the
// Hub's /spend endpoint for double-spend prevention.
//
// The node never needs the Hub's private key — only the public key.
package client

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Radi-Labs/ARFL/internal/credentials"
)

// TokenGate verifies and spends BlindTokens on behalf of a node.
// It performs two checks:
//  1. Local RSA signature verification (fast, offline-capable)
//  2. Hub /spend call for double-spend prevention (requires Hub connectivity)
type TokenGate struct {
	verifier   credentials.BlindVerifier
	hubURL     string
	nodePubkey string // this node's public key (for attribution in /spend)
	httpClient *http.Client
}

// NewTokenGate creates a gate that verifies tokens and checks spend status.
// nodePubkey identifies this node in the Hub's settlement records.
func NewTokenGate(verifier credentials.BlindVerifier, hubURL, nodePubkey string) *TokenGate {
	return &TokenGate{
		verifier:   verifier,
		hubURL:     hubURL,
		nodePubkey: nodePubkey,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// SpendResult holds the outcome of a token spend attempt.
type SpendResult struct {
	Valid         bool  // signature is cryptographically valid
	FirstSpend    bool  // true if this is the first time this token was spent
	BytesPerToken int64 // how much bandwidth this token is worth
}

// VerifyAndSpend performs the full token validation:
//  1. Verify RSA blind signature locally
//  2. Call Hub /spend for double-spend check
//
// Returns an error only for infrastructure failures (network, Hub down).
// Invalid signatures or double-spends are indicated in the result, not errors.
func (g *TokenGate) VerifyAndSpend(ctx context.Context, token *credentials.BlindToken) (*SpendResult, error) {
	// Step 1: Local signature verification (fast, no network).
	if err := g.verifier.Verify(token); err != nil {
		return &SpendResult{Valid: false}, nil
	}

	// Step 2: Call Hub /spend for double-spend prevention.
	reqBody, _ := json.Marshal(map[string]string{
		"key_id":       token.KeyID,
		"token_secret": hex.EncodeToString(token.TokenSecret),
		"signature":    hex.EncodeToString(token.Signature),
		"node_pubkey":  g.nodePubkey,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.hubURL+"/spend", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create spend request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("spend request failed (hub unreachable?): %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		// Hub rejected the signature — this shouldn't happen if local
		// verification passed, but the Hub is the ultimate authority.
		return &SpendResult{Valid: false}, nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil, readAPIError(resp)
	}

	var spendResp struct {
		FirstSpend    bool  `json:"first_spend"`
		BytesPerToken int64 `json:"bytes_per_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&spendResp); err != nil {
		return nil, fmt.Errorf("decode spend response: %w", err)
	}

	return &SpendResult{
		Valid:         true,
		FirstSpend:    spendResp.FirstSpend,
		BytesPerToken: spendResp.BytesPerToken,
	}, nil
}

// VerifyOnly performs local signature verification without calling the Hub.
// Use this during the grace period when the Hub is unreachable.
// WARNING: Without /spend, double-spend detection is not possible.
func (g *TokenGate) VerifyOnly(token *credentials.BlindToken) (*SpendResult, error) {
	if err := g.verifier.Verify(token); err != nil {
		return &SpendResult{Valid: false}, nil
	}

	denom, err := g.verifier.Denomination(token.KeyID)
	if err != nil {
		return nil, fmt.Errorf("denomination lookup: %w", err)
	}

	return &SpendResult{
		Valid:         true,
		FirstSpend:    true, // assumed — no Hub check
		BytesPerToken: denom,
	}, nil
}
