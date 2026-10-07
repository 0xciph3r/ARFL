package helper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/Radi-Labs/ARFL/internal/app"
	"github.com/Radi-Labs/ARFL/internal/tunnel"
)

// Backend is what the helper drives; *tunnel.Tunnel satisfies it.
type Backend interface {
	PublicKey() (string, error)
	Preflight() error
	ValidateEndpoints(entry, exit string) error
	Up(ctx context.Context, cfg app.TunnelConfig) error
	UpOuter(ctx context.Context, cfg app.TunnelConfig) error
	UpInner(ctx context.Context, cfg app.TunnelConfig) error
	Down(ctx context.Context) error
	PrepareHopKeys() (string, string, error)
	ResetHopKeys()
	Usage() (tunnel.Usage, bool, error)
	DisableIPv6() error
}

// Server answers requests from the allowed user.
type Server struct {
	backend Backend
	// allowedUID is the user who installed the helper. Root may also call.
	allowedUID int
	// peerUID reports who is on the other end of a connection; it is a
	// field so tests can stand in for the kernel.
	peerUID func(net.Conn) (int, error)
	// mu serialises calls: the tunnel is one piece of shared system state.
	mu sync.Mutex
}

// NewServer returns a server for backend that accepts only allowedUID and root.
func NewServer(backend Backend, allowedUID int) *Server {
	return &Server{backend: backend, allowedUID: allowedUID, peerUID: peerUID}
}

// Serve handles connections until the listener is closed.
func (s *Server) Serve(l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()

	// Read the request before answering, even from a caller that will be
	// refused: closing on a client that is still writing makes it see a
	// broken pipe instead of the refusal. The read is bounded in size and time.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var req request
	decodeErr := json.NewDecoder(io.LimitReader(conn, maxMessage)).Decode(&req)

	uid, err := s.peerUID(conn)
	if err != nil || (uid != s.allowedUID && uid != 0) {
		log.Printf("[helper] refused connection (uid=%d, err=%v)", uid, err)
		_ = json.NewEncoder(conn).Encode(response{Error: "not allowed to use the ARFL helper"})
		return
	}
	if decodeErr != nil {
		_ = json.NewEncoder(conn).Encode(response{Error: "bad request"})
		return
	}

	result, err := s.dispatch(req)
	resp := response{}
	if err != nil {
		resp.Error = err.Error()
	} else if result != nil {
		raw, merr := json.Marshal(result)
		if merr != nil {
			resp.Error = merr.Error()
		} else {
			resp.Result = raw
		}
	}
	_ = json.NewEncoder(conn).Encode(resp)
}

func (s *Server) dispatch(req request) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	b := s.backend
	switch req.Method {
	case MethodVersion:
		return Version, nil
	case MethodPublicKey:
		return b.PublicKey()
	case MethodPreflight:
		return nil, b.Preflight()
	case MethodValidateEndpoints:
		var p endpointsParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("bad params: %w", err)
		}
		return nil, b.ValidateEndpoints(p.Entry, p.Exit)
	case MethodUp, MethodUpOuter, MethodUpInner:
		var p configParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("bad params: %w", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), upTimeout)
		defer cancel()
		switch req.Method {
		case MethodUpOuter:
			return nil, b.UpOuter(ctx, p.Config)
		case MethodUpInner:
			return nil, b.UpInner(ctx, p.Config)
		default:
			return nil, b.Up(ctx, p.Config)
		}
	case MethodDown:
		ctx, cancel := context.WithTimeout(context.Background(), upTimeout)
		defer cancel()
		return nil, b.Down(ctx)
	case MethodPrepareHopKeys:
		entry, exit, err := b.PrepareHopKeys()
		if err != nil {
			return nil, err
		}
		return hopKeysResult{Entry: entry, Exit: exit}, nil
	case MethodResetHopKeys:
		b.ResetHopKeys()
		return nil, nil
	case MethodUsage:
		u, active, err := b.Usage()
		if err != nil {
			return nil, err
		}
		return UsageResult{Active: active, RxBytes: u.RxBytes, TxBytes: u.TxBytes, EntryHandshake: u.EntryHandshake, ExitHandshake: u.ExitHandshake}, nil
	case MethodDisableIPv6:
		return nil, b.DisableIPv6()
	}
	return nil, fmt.Errorf("unknown method %q", req.Method)
}
