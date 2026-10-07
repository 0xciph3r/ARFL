// Package helper runs the ARFL tunnel in a small privileged process, so the
// desktop app itself never needs root.
//
// The helper owns internal/tunnel: it creates the WireGuard interfaces,
// changes routes, DNS and IPv6, and holds the per-hop private keys, which
// never cross to the app. The app talks to it over a local socket that only
// the user who installed it may use. Each call is one JSON request and one
// JSON response on a fresh connection.
package helper

import (
	"encoding/json"
	"time"

	"github.com/Radi-Labs/ARFL/internal/app"
)

// Version is bumped when the request set or the helper's behaviour changes,
// so the app offers to reinstall an out-of-date helper rather than drive it.
// 2: Preflight checks for wireguard-go, found outside launchd's PATH.
// 3: Preflight and tunnel bring-up require an outbound IPv6 block.
const Version = 3

// Method names.
const (
	MethodVersion           = "version"
	MethodPublicKey         = "public_key"
	MethodPreflight         = "preflight"
	MethodValidateEndpoints = "validate_endpoints"
	MethodUp                = "up"
	MethodUpOuter           = "up_outer"
	MethodUpInner           = "up_inner"
	MethodDown              = "down"
	MethodPrepareHopKeys    = "prepare_hop_keys"
	MethodResetHopKeys      = "reset_hop_keys"
	MethodUsage             = "usage"
	MethodDisableIPv6       = "disable_ipv6"
)

type request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type response struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type endpointsParams struct {
	Entry string `json:"entry"`
	Exit  string `json:"exit"`
}

type configParams struct {
	Config app.TunnelConfig `json:"config"`
}

type hopKeysResult struct {
	Entry string `json:"entry"`
	Exit  string `json:"exit"`
}

// UsageResult mirrors tunnel.Usage plus whether a session is up.
type UsageResult struct {
	Active         bool      `json:"active"`
	RxBytes        int64     `json:"rx_bytes"`
	TxBytes        int64     `json:"tx_bytes"`
	EntryHandshake time.Time `json:"entry_handshake"`
	ExitHandshake  time.Time `json:"exit_handshake"`
}

// maxMessage bounds a single request or response. Tunnel configs are small.
const maxMessage = 64 * 1024

// callTimeout bounds calls that do not carry their own deadline.
const callTimeout = 30 * time.Second

// upTimeout covers interface creation and route changes.
const upTimeout = 2 * time.Minute
