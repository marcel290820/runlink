package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestConfigRejectsUnsafeValues(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Config)
	}{
		{"role", func(c *Config) { c.Role = "secret-value" }},
		{"public listener", func(c *Config) { c.Listen = "0.0.0.0:8081" }},
		{"hostname", func(c *Config) { c.Listen = "localhost:8081" }},
		{"port", func(c *Config) { c.Listen = "127.0.0.1:65536" }},
		{"state", func(c *Config) { c.StateDir = "" }},
		{"timeout", func(c *Config) { c.ReadTimeout = 0 }},
		{"credential URL", func(c *Config) { c.Role = "frontend"; c.Upstream = "http://secret@127.0.0.1:8081" }},
		{"public upstream", func(c *Config) { c.Role = "frontend"; c.Upstream = "http://8.8.8.8:8081" }},
		{"query", func(c *Config) { c.Role = "frontend"; c.Upstream = "http://127.0.0.1:8081?secret" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := DefaultConfig()
			tc.change(&c)
			if err := c.Validate(); err == nil || strings.Contains(err.Error(), "secret-value") {
				t.Fatal("unsafe config accepted or leaked")
			}
		})
	}
	file := filepath.Join(t.TempDir(), "config.json")
	for _, data := range []string{`{"unknown":"secret-value"}`, `{"listen":"0.0.0.0:80"}`, `{} {}`, `null`, `[]`, "{}" + strings.Repeat(" ", 65536)} {
		if err := os.WriteFile(file, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(file); err == nil || strings.Contains(err.Error(), "secret-value") {
			t.Fatal("invalid config accepted or leaked")
		}
	}
}

func TestHealthReadinessAndNoRequestLogging(t *testing.T) {
	var ready atomic.Bool
	c := DefaultConfig()
	h := Handler(c, &ready)
	for _, tc := range []struct {
		path string
		code int
	}{{"/healthz", 200}, {"/readyz", 503}, {"/missing", 404}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
		if w.Code != tc.code {
			t.Fatalf("%s: %d", tc.path, w.Code)
		}
	}
	ready.Store(true)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}

func TestRunShutsDownAndKeepsLogsPrivate(t *testing.T) {
	c := DefaultConfig()
	c.StateDir = filepath.Join(t.TempDir(), "state")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- Run(ctx, c, ln, slog.New(slog.NewJSONHandler(&logs, nil))) }()
	client := &http.Client{Timeout: time.Second}
	res, err := client.Get("http://" + ln.Addr().String() + "/healthz?recipient-secret=do-not-log")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown hung")
	}
	if strings.Contains(logs.String(), "do-not-log") || !strings.Contains(logs.String(), "server_stopped") {
		t.Fatal(logs.String())
	}
	if _, err := client.Get("http://" + ln.Addr().String() + "/healthz"); err == nil {
		t.Fatal("listener survived shutdown")
	}
}

func TestHeaderTimeoutClosesIncompleteRequest(t *testing.T) {
	c := DefaultConfig()
	c.StateDir = filepath.Join(t.TempDir(), "state")
	c.ReadHeaderTimeout = 40 * time.Millisecond
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, c, ln, slog.New(slog.NewJSONHandler(io.Discard, nil))) }()
	connection, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_, err = io.WriteString(connection, "GET /healthz HTTP/1.1\r\nHost: localhost\r\nX-Incomplete: ")
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	_, err = connection.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("incomplete request was not closed")
	}
	if networkErr, ok := err.(net.Error); ok && networkErr.Timeout() {
		t.Fatal("server header timeout was not enforced")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestGracefulShutdownDrainsRequestAndBoundsWait(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprint("force=", force), func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				select {
				case <-release:
					_, _ = io.WriteString(w, `{"status":"scaffold"}`)
				case <-r.Context().Done():
				}
			}))
			defer upstream.Close()
			c := DefaultConfig()
			c.Role = "frontend"
			c.Upstream = upstream.URL
			c.ShutdownTimeout = 100 * time.Millisecond
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- Run(ctx, c, ln, slog.New(slog.NewJSONHandler(io.Discard, nil))) }()
			requestDone := make(chan error, 1)
			go func() {
				client := http.Client{Timeout: time.Second}
				res, err := client.Get("http://" + ln.Addr().String() + "/api/v1/status")
				if err == nil {
					_, err = io.ReadAll(res.Body)
					res.Body.Close()
					if res.StatusCode != 200 {
						err = fmt.Errorf("unexpected status")
					}
				}
				requestDone <- err
			}()
			<-entered
			cancel()
			if !force {
				time.Sleep(10 * time.Millisecond)
				close(release)
			}
			select {
			case err := <-done:
				if force && err == nil || !force && err != nil {
					t.Fatalf("force=%v: %v", force, err)
				}
			case <-time.After(time.Second):
				t.Fatal("shutdown was not bounded")
			}
			err = <-requestDone
			if !force && err != nil {
				t.Fatal("inflight request failed during graceful drain", err)
			}
			if force {
				close(release)
			}
		})
	}
}
