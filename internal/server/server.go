package server

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync/atomic"

	"runlink/internal/frontend"
)

// Run serves until cancellation, drains requests, then closes any remaining connections.
// Logs contain lifecycle events only: no request URLs, headers, bodies, or raw errors.
func Run(ctx context.Context, c Config, listener net.Listener, logger *slog.Logger) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.Role == "app" {
		if err := os.MkdirAll(c.StateDir, 0700); err != nil {
			return errors.New("state directory unavailable")
		}
		info, err := os.Stat(c.StateDir)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			return errors.New("state directory must be private (0700)")
		}
		f, err := os.CreateTemp(c.StateDir, ".ready-")
		if err != nil {
			return errors.New("state directory is not writable")
		}
		name := f.Name()
		if err := f.Close(); err != nil {
			return errors.New("state directory check failed")
		}
		if err := os.Remove(name); err != nil {
			return errors.New("state directory check failed")
		}
	}
	var tlsConfig *tls.Config
	if c.TLSCert != "" {
		certificate, err := tls.LoadX509KeyPair(c.TLSCert, c.TLSKey)
		if err != nil {
			return errors.New("TLS certificate/key unavailable")
		}
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	}
	var ready atomic.Bool
	ready.Store(true)
	handler := Handler(c, &ready)
	s := &http.Server{Handler: handler, ReadHeaderTimeout: c.ReadHeaderTimeout, ReadTimeout: c.ReadTimeout, WriteTimeout: c.WriteTimeout, IdleTimeout: c.IdleTimeout, MaxHeaderBytes: 16384, ErrorLog: log.New(io.Discard, "", 0), TLSConfig: tlsConfig}
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
		return errors.New("HTTP serve failed")
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
			return errors.New("shutdown deadline exceeded")
		}
		logger.Info("server_stopped")
		return nil
	}
}

func Handler(c Config, ready *atomic.Bool) http.Handler {
	var front http.Handler
	if c.Role == "frontend" {
		front = frontend.Handler(c.Upstream)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		frontend.ControlHeaders(w.Header())
		if r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		if r.Header.Get("Sec-Fetch-Dest") == "serviceworker" || r.Header.Get("Service-Worker") != "" {
			frontend.JSONError(w, 403, "service_workers_disabled")
			return
		}
		if r.Header.Get("Upgrade") != "" {
			frontend.JSONError(w, 400, "upgrades_disabled")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			frontend.JSONError(w, 405, "method_not_allowed")
			return
		}
		switch r.URL.Path {
		case "/healthz":
			_, _ = io.WriteString(w, "{\"status\":\"ok\"}\n")
		case "/readyz":
			if !ready.Load() {
				frontend.JSONError(w, 503, "not_ready")
				return
			}
			_, _ = io.WriteString(w, "{\"status\":\"ready\"}\n")
		default:
			if front != nil {
				front.ServeHTTP(w, r)
				return
			}
			if r.URL.Path == "/api/v1/status" {
				_, _ = io.WriteString(w, "{\"status\":\"scaffold\"}\n")
				return
			}
			frontend.JSONError(w, 404, "not_found")
		}
	})
}
