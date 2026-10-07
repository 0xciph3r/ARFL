package helper

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/Radi-Labs/ARFL/internal/tunnel"
)

// Run is the helper's main loop: it owns the tunnel and serves the allowed
// user until stopped, tearing any session down on the way out so the machine
// is never left routed into a dead tunnel.
func Run(configPath string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("the helper must run as root")
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	tun, err := tunnel.New()
	if err != nil {
		return fmt.Errorf("start tunnel: %w", err)
	}
	defer tun.Close()

	_ = os.Remove(SocketPath)
	l, err := net.Listen("unix", SocketPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", SocketPath, err)
	}
	// Anyone may connect; the server checks the caller's uid with the kernel.
	if err := os.Chmod(SocketPath, 0o666); err != nil {
		return err
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		log.Printf("[helper] stopping")
		_ = tun.Down(context.Background())
		_ = l.Close()
	}()

	log.Printf("[helper] serving uid %d on %s", cfg.AllowedUID, SocketPath)
	return NewServer(tun, cfg.AllowedUID).Serve(l)
}
