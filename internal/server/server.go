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

// Run serves until cancellation, drains requests, then closes any remaining connections.
// Request handling never logs URLs, headers, bodies, or raw errors. Startup and serve
// failures keep their cause because they describe operator configuration, not recipients.
func Run(ctx context.Context, c Config, listener net.Listener, logger *slog.Logger) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.Role == "app" {
		if err := os.MkdirAll(c.StateDir, 0700); err != nil {
			return fmt.Errorf("state directory unavailable: %w", err)
		}
		info, err := os.Stat(c.StateDir)
		if err != nil {
			return fmt.Errorf("state directory unavailable: %w", err)
		}
		if info.Mode().Perm()&0077 != 0 {
			return errors.New("state directory must be private (0700)")
		}
		f, err := os.CreateTemp(c.StateDir, ".ready-")
		if err != nil {
			return fmt.Errorf("state directory is not writable: %w", err)
		}
		name := f.Name()
		if err := f.Close(); err != nil {
			return fmt.Errorf("state directory check failed: %w", err)
		}
		if err := os.Remove(name); err != nil {
			return fmt.Errorf("state directory check failed: %w", err)
		}
	}
	var tlsConfig *tls.Config
	if c.TLSCert != "" {
		certificate, err := tls.LoadX509KeyPair(c.TLSCert, c.TLSKey)
		if err != nil {
			return fmt.Errorf("TLS certificate/key unavailable: %w", err)
		}
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	}
	var ready atomic.Bool
	ready.Store(true)
	s := &http.Server{
		Handler:           Handler(c, &ready),
		ReadHeaderTimeout: c.ReadHeaderTimeout,
		ReadTimeout:       c.ReadTimeout,
		WriteTimeout:      c.WriteTimeout,
		IdleTimeout:       c.IdleTimeout,
		MaxHeaderBytes:    16384,
		ErrorLog:          log.New(io.Discard, "", 0),
		TLSConfig:         tlsConfig,
	}
	done := make(chan error, 1)
	go func() {
		if tlsConfig != nil {
			done <- s.ServeTLS(listener, "", "")
		} else {
			done <- s.Serve(listener)
		}
	}()
	logger.Info("server_started", "role", c.Role)
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("HTTP serve failed: %w", err)
	case <-ctx.Done():
		ready.Store(false)
		logger.Info("server_stopping")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), c.ShutdownTimeout)
		defer cancel()
		err := s.Shutdown(shutdownCtx)
		if err != nil {
			_ = s.Close()
		}
		<-done
		if err != nil {
			return fmt.Errorf("shutdown deadline exceeded: %w", err)
		}
		logger.Info("server_stopped")
		return nil
	}
}

// Handler applies the shared response controls and health routes, then the role's fixed routes.
func Handler(c Config, ready *atomic.Bool) http.Handler {
	routes := http.HandlerFunc(appRoutes)
	if c.Role == "frontend" {
		routes = frontendRoutes(c.Upstream)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		controlHeaders(w.Header())
		if r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		if r.Header.Get("Sec-Fetch-Dest") == "serviceworker" || r.Header.Get("Service-Worker") != "" {
			jsonError(w, 403, "service_workers_disabled")
			return
		}
		if r.Header.Get("Upgrade") != "" {
			jsonError(w, 400, "upgrades_disabled")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			jsonError(w, 405, "method_not_allowed")
			return
		}
		switch r.URL.Path {
		case "/healthz":
			_, _ = io.WriteString(w, "{\"status\":\"ok\"}\n")
		case "/readyz":
			if !ready.Load() {
				jsonError(w, 503, "not_ready")
				return
			}
			_, _ = io.WriteString(w, "{\"status\":\"ready\"}\n")
		default:
			routes.ServeHTTP(w, r)
		}
	})
}

// appRoutes belongs to B, which listens privately behind A.
func appRoutes(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/v1/status" {
		_, _ = io.WriteString(w, "{\"status\":\"scaffold\"}\n")
		return
	}
	jsonError(w, 404, "not_found")
}

func controlHeaders(h http.Header) {
	h.Set("Content-Type", "application/json")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; sandbox")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
}

func jsonError(w http.ResponseWriter, code int, message string) {
	controlHeaders(w.Header())
	w.WriteHeader(code)
	_, _ = fmt.Fprintf(w, "{\"error\":%q}\n", message)
}
