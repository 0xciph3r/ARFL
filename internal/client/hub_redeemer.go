// HubRedeemer is an HTTP client that nodes use to call the hub's
// POST /v1/redeem endpoint to verify and burn Cashu proofs presented
// by connecting clients.
//
// The node receives Cashu proofs from a client, forwards them to the
// hub for verification + spend-marking, and gets back the bandwidth
// allowance if the proofs are valid.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/elnosh/gonuts/cashu"
)

// HubRedeemer errors.
var (
	ErrRedeemInvalidProof = errors.New("hub rejected proofs: invalid")
	ErrRedeemAlreadySpent = errors.New("hub rejected proofs: already spent")
	ErrRedeemHubDown      = errors.New("hub unreachable or returned server error")
	ErrRedeemRateLimited  = errors.New("hub rate-limited this node")
	ErrRedeemCircuitOpen  = errors.New("hub circuit breaker is open (LN down)")
)

// CashuRedeemResult is returned on successful Cashu proof redemption.
type CashuRedeemResult struct {
	BytesAllowed int64  `json:"bytes_allowed"`
	SatsRedeemed uint64 `json:"sats_redeemed"`
}

// HubRedeemer calls the hub's /v1/redeem endpoint.
type HubRedeemer struct {
	hubURL     string
	nodePubkey string // This node's Nostr pubkey (identifies the caller).
	httpClient *http.Client
}

// NewHubRedeemer creates a redeemer pointed at the given hub.
// nodePubkey is this node's Nostr public key (hex).
func NewHubRedeemer(hubURL, nodePubkey string) *HubRedeemer {
	return &HubRedeemer{
		hubURL:     hubURL,
		nodePubkey: nodePubkey,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// redeemRequest matches the hub's expected JSON body.
type redeemRequest struct {
	Proofs     cashu.Proofs `json:"proofs"`
	NodePubkey string       `json:"node_pubkey"`
}

// redeemResponse matches the hub's success JSON.
type redeemResponse struct {
	OK           bool   `json:"ok"`
	BytesAllowed int64  `json:"bytes_allowed"`
	SatsRedeemed uint64 `json:"sats_redeemed"`
}

// Redeem sends proofs to the hub for verification and burn.
// Returns the bandwidth allowance on success.
func (hr *HubRedeemer) Redeem(ctx context.Context, proofs cashu.Proofs) (*CashuRedeemResult, error) {
	body, err := json.Marshal(redeemRequest{
		Proofs:     proofs,
		NodePubkey: hr.nodePubkey,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal redeem request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hr.hubURL+"/v1/redeem", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := hr.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRedeemHubDown, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	switch resp.StatusCode {
	case http.StatusOK:
		var result redeemResponse
		if err := json.Unmarshal(respBody, &result); err != nil {
			return nil, fmt.Errorf("decode redeem response: %w", err)
		}
		if !result.OK {
			return nil, fmt.Errorf("hub returned ok=false")
		}
		return &CashuRedeemResult{
			BytesAllowed: result.BytesAllowed,
			SatsRedeemed: result.SatsRedeemed,
		}, nil

	case http.StatusBadRequest:
		return nil, ErrRedeemInvalidProof

	case http.StatusConflict:
		return nil, ErrRedeemAlreadySpent

	case http.StatusTooManyRequests:
		return nil, ErrRedeemRateLimited

	case http.StatusServiceUnavailable:
		return nil, ErrRedeemCircuitOpen

	default:
		return nil, fmt.Errorf("%w: status %d: %s", ErrRedeemHubDown, resp.StatusCode, respBody)
	}
}
