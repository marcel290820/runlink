// Package server runs both runlink-server roles behind shared HTTP controls.
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync/atomic"
)

// Run serves c's role on listener until ctx is cancelled, then drains in-flight
// requests for up to c.ShutdownTimeout and force-closes the rest.
//
// Request handling never logs URLs, headers, bodies, or errors. Startup and serve
// failures keep their cause because they describe operator configuration, not
// recipients.
func Run(ctx context.Context, c Config, listener net.Listener, logger *slog.Logger) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.Role == RoleApp {
		if err := prepareStateDir(c.StateDir); err != nil {
			return err
		}
	}
	var ready atomic.Bool
	s := &http.Server{
		Handler:           Handler(c, &ready),
		ReadHeaderTimeout: c.ReadHeaderTimeout,
		ReadTimeout:       c.ReadTimeout,
		WriteTimeout:      c.WriteTimeout,
		IdleTimeout:       c.IdleTimeout,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(io.Discard, "", 0), // its messages can carry client data
	}
	serve := func() error { return s.Serve(listener) }
	if c.TLSCert != "" {
		certificate, err := tls.LoadX509KeyPair(c.TLSCert, c.TLSKey)
		if err != nil {
			return fmt.Errorf("TLS certificate/key unavailable: %w", err)
		}
		s.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
		serve = func() error { return s.ServeTLS(listener, "", "") }
	}

	served := make(chan error, 1)
	go func() { served <- serve() }()
	ready.Store(true)
	// The address lets supervisors and tests that listen on port 0 find the server.
	logger.Info("server_started", "role", c.Role, "address", listener.Addr().String())
	select {
	case err := <-served:
		return fmt.Errorf("HTTP serve failed: %w", err)
	case <-ctx.Done():
	}

	ready.Store(false)
	logger.Info("server_stopping")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), c.ShutdownTimeout)
	defer cancel()
	if err := s.Shutdown(shutdownCtx); err != nil {
		_ = s.Close() // the deadline error below is the cause worth reporting
		<-served
		return fmt.Errorf("shutdown deadline exceeded: %w", err)
	}
	<-served
	logger.Info("server_stopped")
	return nil
}

// prepareStateDir creates the app's state directory and proves it is private
// and writable.
func prepareStateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("state directory unavailable: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("state directory unavailable: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("state directory must be private (0700)")
	}
	probe, err := os.CreateTemp(dir, ".ready-")
	if err != nil {
		return fmt.Errorf("state directory is not writable: %w", err)
	}
	if err := errors.Join(probe.Close(), os.Remove(probe.Name())); err != nil {
		return fmt.Errorf("state directory check failed: %w", err)
	}
	return nil
}

// Handler applies the shared response controls and health routes, then the
// role's fixed routes. ready decides whether /readyz succeeds.
func Handler(c Config, ready *atomic.Bool) http.Handler {
	routes := http.HandlerFunc(appRoutes)
	if c.Role == RoleFrontend {
		routes = frontendRoutes(c.Upstream)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setInertHeaders(w.Header())
		if r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		switch {
		case r.Header.Get("Sec-Fetch-Dest") == "serviceworker" || r.Header.Get("Service-Worker") != "":
			writeError(w, http.StatusForbidden, "service_workers_disabled")
		case r.Header.Get("Upgrade") != "":
			writeError(w, http.StatusBadRequest, "upgrades_disabled")
		case r.Method != http.MethodGet && r.Method != http.MethodHead:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		case r.URL.Path == "/healthz":
			writeJSON(w, http.StatusOK, `{"status":"ok"}`)
		case r.URL.Path == "/readyz" && !ready.Load():
			writeError(w, http.StatusServiceUnavailable, "not_ready")
		case r.URL.Path == "/readyz":
			writeJSON(w, http.StatusOK, `{"status":"ready"}`)
		default:
			routes.ServeHTTP(w, r)
		}
	})
}

// appRoutes are B's routes. B listens privately behind A.
func appRoutes(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == statusPath {
		writeJSON(w, http.StatusOK, `{"status":"scaffold"}`)
		return
	}
	writeError(w, http.StatusNotFound, "not_found")
}

// setInertHeaders makes every response non-executable JSON by default. Only A's
// asset routes replace the content type and CSP.
func setInertHeaders(h http.Header) {
	h.Set("Content-Type", "application/json")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; sandbox")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body+"\n") // a failed write means the client left
}

// writeError reports a fixed error code; codes are identifiers, never input.
func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, `{"error":"`+code+`"}`)
}
