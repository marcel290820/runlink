// Package devstack runs Runlink's production services and a TURN stand-in on
// loopback, so the whole product runs on one machine (ADR 0008). The frontend
// (A) and app (B) are the production server roles; only infrastructure is
// replaced. Development tools and tests import it; releases never contain it.
package devstack

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"runlink/internal/server"
)

// Config places the stack. Every address is a loopback IP:port; port zero
// picks a free port.
type Config struct {
	Frontend string // A: trusted loader and ingress
	App      string // B: registration and signaling, private behind A
	TURN     string // C stand-in: STUN and TURN on UDP and TCP
	StateDir string // B's state in StateDir/app, the TURN secret in StateDir/turn-secret
}

// DefaultConfig is the stack scripts/dev.sh runs. Development builds of the
// owner CLI target its frontend.
func DefaultConfig() Config {
	return Config{Frontend: "127.0.0.1:8080", App: "127.0.0.1:8081", TURN: "127.0.0.1:3478", StateDir: "var/dev"}
}

// Validate keeps every stand-in on this machine: none has the production
// controls that would make it safe to expose. TURN stays on IPv4: ADR 0008
// leaves IPv6 TURN to the real server.
func (c Config) Validate() error {
	for _, listener := range []struct {
		name, address string
		ipv4          bool
	}{
		{"frontend", c.Frontend, false}, {"app", c.App, false}, {"turn", c.TURN, true},
	} {
		addr, err := netip.ParseAddrPort(listener.address)
		if err != nil || !addr.Addr().IsLoopback() || addr.Addr().Zone() != "" || (listener.ipv4 && !addr.Addr().Is4()) {
			if listener.ipv4 {
				return fmt.Errorf("%s must be an IPv4 loopback IP:port", listener.name)
			}
			return fmt.Errorf("%s must be a loopback IP:port", listener.name)
		}
	}
	if c.StateDir == "" {
		return errors.New("a state directory is required")
	}
	return nil
}

// Stack is a running local stack.
type Stack struct {
	Frontend   string   // A's origin, standing in for https://runlink.dev
	App        string   // B's private origin
	TURN       string   // STUN/TURN IP:port, standing in for C
	TURNURIs   []string // ICE server URIs for the stand-in
	TURNSecret string   // shared secret that signs TURN REST credentials

	turn    *turnServer
	cancel  context.CancelFunc
	stopped chan struct{} // closed once every service has stopped
	err     error         // why the stack stopped, set before stopped closes
}

// Start binds every listener, starts the services, and returns once A and B
// report ready. Cancelling ctx or calling Stop shuts the stack down; if any
// service stops on its own, the others stop too.
func Start(ctx context.Context, c Config, logger *slog.Logger) (*Stack, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	// Other users must not be able to replace the secret or B's state.
	if err := os.MkdirAll(c.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("state directory unavailable: %w", err)
	}
	if info, err := os.Stat(c.StateDir); err != nil {
		return nil, fmt.Errorf("state directory unavailable: %w", err)
	} else if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("state directory must be private (0700)")
	}
	secret, err := loadOrCreateSecret(filepath.Join(c.StateDir, "turn-secret"))
	if err != nil {
		return nil, err
	}
	appListener, err := net.Listen("tcp", c.App)
	if err != nil {
		return nil, fmt.Errorf("app: %w", err)
	}
	frontendListener, err := net.Listen("tcp", c.Frontend)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("frontend: %w", err), appListener.Close())
	}
	turn, turnAddr, err := startTURN(netip.MustParseAddrPort(c.TURN), secret, logger.With("service", "turn"))
	if err != nil {
		return nil, errors.Join(fmt.Errorf("turn: %w", err), appListener.Close(), frontendListener.Close())
	}

	app := server.DefaultConfig()
	app.Listen, app.StateDir = appListener.Addr().String(), filepath.Join(c.StateDir, "app")
	app.TURNURIs = []string{"stun:" + turnAddr.String(), "turn:" + turnAddr.String() + "?transport=udp", "turn:" + turnAddr.String() + "?transport=tcp"}
	app.TURNSecretFile = filepath.Join(c.StateDir, "turn-secret")
	frontend := server.DefaultConfig()
	frontend.Role, frontend.Listen, frontend.StateDir = server.RoleFrontend, frontendListener.Addr().String(), ""
	frontend.Upstream = "http://" + app.Listen

	ctx, cancel := context.WithCancel(ctx)
	s := &Stack{
		Frontend:   "http://" + frontend.Listen,
		App:        frontend.Upstream,
		TURN:       turnAddr.String(),
		TURNURIs:   app.TURNURIs,
		TURNSecret: secret,
		turn:       turn,
		cancel:     cancel,
		stopped:    make(chan struct{}),
	}
	exited := make(chan error, 3)
	serve := func(name string, config server.Config, listener net.Listener) {
		err := server.Run(ctx, config, listener, logger.With("service", name))
		_ = listener.Close() // Run leaves it open when it fails before serving
		if err != nil {
			err = fmt.Errorf("%s: %w", name, err)
		}
		exited <- err
	}
	go serve("app", app, appListener)
	go serve("frontend", frontend, frontendListener)
	go func() {
		var err error
		select {
		case <-ctx.Done():
		case err = <-turn.failed:
			err = fmt.Errorf("turn: %w", err)
		}
		exited <- errors.Join(err, turn.Close())
	}()
	go func() {
		first := <-exited
		cancel()
		s.err = errors.Join(first, <-exited, <-exited)
		close(s.stopped)
	}()

	for _, origin := range []string{s.App, s.Frontend} {
		if err := waitReady(origin, s.stopped); err != nil {
			if stopErr := s.Stop(); stopErr != nil {
				err = stopErr // the service's own failure explains more
			}
			return nil, err
		}
	}
	return s, nil
}

// Wait blocks until the stack stops and returns why, or nil after a requested
// shutdown.
func (s *Stack) Wait() error {
	<-s.stopped
	return s.err
}

// Stop shuts the stack down and waits for every service to drain.
func (s *Stack) Stop() error {
	s.cancel()
	return s.Wait()
}

// waitReady polls origin's /readyz until it succeeds, the stack stops, or ten
// seconds pass.
func waitReady(origin string, stopped <-chan struct{}) error {
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if res, err := client.Get(origin + "/readyz"); err == nil {
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not become ready", origin)
		}
		select {
		case <-stopped:
			return errors.New("the stack stopped during startup")
		case <-time.After(20 * time.Millisecond):
		}
	}
}
