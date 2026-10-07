package helper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/Radi-Labs/ARFL/internal/app"
)

// ErrNotRunning means no helper answered: it is not installed, or stopped.
var ErrNotRunning = errors.New("the ARFL helper is not running")

// Client talks to the helper. It implements app.Tunnel and the optional
// staged, endpoint-validating and per-hop key interfaces, so app.Service
// drives a tunnel in another process exactly as it would one in its own.
type Client struct {
	socket string
}

// NewClient returns a client for the helper listening at socket.
func NewClient(socket string) *Client {
	return &Client{socket: socket}
}

var (
	_ app.Tunnel            = (*Client)(nil)
	_ app.StagedTunnel      = (*Client)(nil)
	_ app.EndpointValidator = (*Client)(nil)
	_ app.HopKeyProvider    = (*Client)(nil)
)

func (c *Client) call(ctx context.Context, method string, params, out any) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, callTimeout)
		defer cancel()
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	req := request{Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		req.Params = raw
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return fmt.Errorf("send to helper: %w", err)
	}

	var resp response
	if err := json.NewDecoder(io.LimitReader(conn, maxMessage)).Decode(&resp); err != nil {
		return fmt.Errorf("read from helper: %w", err)
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	if out != nil && len(resp.Result) > 0 {
		return json.Unmarshal(resp.Result, out)
	}
	return nil
}

// Ping returns the helper's protocol version, or ErrNotRunning.
func (c *Client) Ping() (int, error) {
	var v int
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := c.call(ctx, MethodVersion, nil, &v)
	return v, err
}

func (c *Client) PublicKey() (string, error) {
	var key string
	err := c.call(context.Background(), MethodPublicKey, nil, &key)
	return key, err
}

func (c *Client) Preflight() error {
	return c.call(context.Background(), MethodPreflight, nil, nil)
}

func (c *Client) ValidateEndpoints(entry, exit string) error {
	return c.call(context.Background(), MethodValidateEndpoints, endpointsParams{Entry: entry, Exit: exit}, nil)
}

func (c *Client) Up(ctx context.Context, cfg app.TunnelConfig) error {
	ctx, cancel := withUpTimeout(ctx)
	defer cancel()
	return c.call(ctx, MethodUp, configParams{Config: cfg}, nil)
}

func (c *Client) UpOuter(ctx context.Context, cfg app.TunnelConfig) error {
	ctx, cancel := withUpTimeout(ctx)
	defer cancel()
	return c.call(ctx, MethodUpOuter, configParams{Config: cfg}, nil)
}

func (c *Client) UpInner(ctx context.Context, cfg app.TunnelConfig) error {
	ctx, cancel := withUpTimeout(ctx)
	defer cancel()
	return c.call(ctx, MethodUpInner, configParams{Config: cfg}, nil)
}

func (c *Client) Down(ctx context.Context) error {
	ctx, cancel := withUpTimeout(ctx)
	defer cancel()
	return c.call(ctx, MethodDown, nil, nil)
}

func (c *Client) PrepareHopKeys() (string, string, error) {
	var keys hopKeysResult
	err := c.call(context.Background(), MethodPrepareHopKeys, nil, &keys)
	return keys.Entry, keys.Exit, err
}

func (c *Client) ResetHopKeys() {
	_ = c.call(context.Background(), MethodResetHopKeys, nil, nil)
}

// Usage reports live traffic; active is false when no session is up.
func (c *Client) Usage() (UsageResult, error) {
	var u UsageResult
	err := c.call(context.Background(), MethodUsage, nil, &u)
	return u, err
}

func (c *Client) DisableIPv6() error {
	return c.call(context.Background(), MethodDisableIPv6, nil, nil)
}

// Close exists so the client can stand in for an in-process tunnel. The
// helper keeps running; it tears a session down on Down or on its own exit.
func (c *Client) Close() error { return nil }

// withUpTimeout gives bring-up and teardown room, since the caller's context
// is often a short UI request.
func withUpTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, upTimeout)
}
