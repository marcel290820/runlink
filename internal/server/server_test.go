package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// closeSignal closes closed when the server closes its listener, which is the
// first thing Shutdown does. Watching it adds no connections Shutdown must wait for.
type closeSignal struct {
	net.Listener
	once   sync.Once
	closed chan struct{}
}

func (l *closeSignal) Close() error {
	l.once.Do(func() { close(l.closed) })
	return l.Listener.Close()
}

// start runs Run on a loopback port and returns its address, an idempotent stop
// that cancels Run and returns its result, and a channel closed once shutdown begins.
func start(t *testing.T, c Config, logs io.Writer) (addr string, stop func() error, closed <-chan struct{}) {
	t.Helper()
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := &closeSignal{Listener: inner, closed: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, c, listener, slog.New(slog.NewJSONHandler(logs, nil))) }()
	stop = sync.OnceValue(func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(2 * time.Second):
			return errors.New("shutdown hung")
		}
	})
	t.Cleanup(func() { _ = stop() })
	return listener.Addr().String(), stop, listener.closed
}

func appConfig(t *testing.T) Config {
	c := DefaultConfig()
	c.StateDir = filepath.Join(t.TempDir(), "state")
	return c
}

func get(t *testing.T, client *http.Client, url string) (int, string) {
	t.Helper()
	res, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(body)
}

func TestHandlerRoutesAndControls(t *testing.T) {
	var ready atomic.Bool
	handler := Handler(DefaultConfig(), &ready)
	tests := []struct {
		name, method, path, header, value string
		want                              int
	}{
		{"health", "GET", "/healthz", "", "", 200},
		{"not ready", "GET", "/readyz", "", "", 503},
		{"status", "GET", "/api/v1/status", "", "", 200},
		{"head", "HEAD", "/healthz", "", "", 200},
		{"unknown route", "GET", "/missing", "", "", 404},
		{"post", "POST", "/healthz", "", "", 405},
		{"websocket", "GET", "/healthz", "Upgrade", "websocket", 400},
		{"worker script", "GET", "/healthz", "Service-Worker", "script", 403},
		{"worker fetch", "GET", "/healthz", "Sec-Fetch-Dest", "serviceworker", 403},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.header != "" {
				r.Header.Set(tc.header, tc.value)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			h := w.Header()
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d", w.Code, tc.want)
			}
			if h.Get("Content-Type") != "application/json" || h.Get("X-Content-Type-Options") != "nosniff" ||
				!strings.HasSuffix(h.Get("Content-Security-Policy"), "; sandbox") ||
				h.Get("Referrer-Policy") != "no-referrer" || h.Get("Cache-Control") != "no-store" {
				t.Fatalf("missing inert response headers: %v", h)
			}
			if h.Get("Strict-Transport-Security") != "" {
				t.Fatal("HSTS sent over plain HTTP")
			}
		})
	}
	ready.Store(true)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 200 || w.Body.String() != "{\"status\":\"ready\"}\n" {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestRunServesThenStopsWithPrivateLogs(t *testing.T) {
	c := appConfig(t)
	var logs bytes.Buffer
	addr, stop, _ := start(t, c, &logs)
	client := &http.Client{Timeout: time.Second}
	if code, body := get(t, client, "http://"+addr+"/healthz?recipient-secret=do-not-log"); code != 200 || body != "{\"status\":\"ok\"}\n" {
		t.Fatal(code, body)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{`"msg":"server_started"`, `"address":"` + addr + `"`, `"msg":"server_stopped"`} {
		if !strings.Contains(logs.String(), event) {
			t.Fatalf("logs lack %s: %s", event, &logs)
		}
	}
	if strings.Contains(logs.String(), "do-not-log") {
		t.Fatalf("request data logged: %s", &logs)
	}
	if _, err := client.Get("http://" + addr + "/healthz"); err == nil {
		t.Fatal("listener survived shutdown")
	}
	info, err := os.Stat(c.StateDir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("state directory not created privately", err)
	}
}

func TestRunRejectsSharedStateDirectory(t *testing.T) {
	c := appConfig(t)
	if err := os.Mkdir(c.StateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(c.StateDir, 0o755); err != nil { // independent of the test umask
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := Run(context.Background(), c, listener, slog.New(slog.DiscardHandler)); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("shared state directory accepted: %v", err)
	}
}

func TestHeaderTimeoutClosesIncompleteRequest(t *testing.T) {
	c := appConfig(t)
	c.ReadHeaderTimeout = 40 * time.Millisecond
	addr, stop, _ := start(t, c, io.Discard)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "GET /healthz HTTP/1.1\r\nHost: localhost\r\nX-Incomplete: "); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = conn.Read(make([]byte, 1))
	var netErr net.Error
	if err == nil || errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatalf("server kept the incomplete request open: %v", err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

// startBlockedStatus serves a frontend whose upstream holds every status request
// until release closes or the request is cancelled. entered closes on arrival.
func startBlockedStatus(t *testing.T, shutdownTimeout time.Duration) (addr string, stop func() error, closed <-chan struct{}, entered, release chan struct{}) {
	t.Helper()
	entered, release = make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
			_, _ = io.WriteString(w, `{"status":"scaffold"}`)
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(upstream.Close)
	c := frontendConfig(upstream.URL)
	c.ShutdownTimeout = shutdownTimeout
	addr, stop, closed = start(t, c, io.Discard)
	return addr, stop, closed, entered, release
}

func requestStatus(addr string) <-chan error {
	result := make(chan error, 1)
	go func() {
		res, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + addr + "/api/v1/status")
		if err == nil {
			_, err = io.Copy(io.Discard, res.Body)
			res.Body.Close()
			if err == nil && res.StatusCode != 200 {
				err = errors.New(res.Status)
			}
		}
		result <- err
	}()
	return result
}

func TestShutdownDrainsInFlightRequest(t *testing.T) {
	// A generous deadline: Shutdown re-checks connections with a backoff of up to 500 ms.
	addr, stop, closed, entered, release := startBlockedStatus(t, 10*time.Second)
	result := requestStatus(addr)
	<-entered
	stopped := make(chan error, 1)
	go func() { stopped <- stop() }()
	<-closed
	close(release)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal("in-flight request failed during graceful drain:", err)
	}
}

func TestShutdownDeadlineForcesClose(t *testing.T) {
	addr, stop, _, entered, _ := startBlockedStatus(t, 100*time.Millisecond)
	result := requestStatus(addr)
	<-entered
	began := time.Now()
	if err := stop(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("forced shutdown returned %v", err)
	}
	if elapsed := time.Since(began); elapsed > time.Second {
		t.Fatalf("shutdown took %v", elapsed)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("request survived forced shutdown")
		}
	case <-time.After(time.Second):
		t.Fatal("forced shutdown left the connection open")
	}
}

// writeCertificate writes a self-signed certificate for 127.0.0.1 and its key.
func writeCertificate(t *testing.T, dir string) (certFile, keyFile string, roots *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots = x509.NewCertPool()
	roots.AddCert(certificate)
	certFile, keyFile = filepath.Join(dir, "fullchain.pem"), filepath.Join(dir, "privkey.pem")
	for file, block := range map[string]*pem.Block{certFile: {Type: "CERTIFICATE", Bytes: der}, keyFile: {Type: "PRIVATE KEY", Bytes: keyDER}} {
		if err := os.WriteFile(file, pem.EncodeToMemory(block), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return certFile, keyFile, roots
}

func TestFrontendTLS(t *testing.T) {
	c := frontendConfig("http://127.0.0.1:1") // nothing listens there
	var roots *x509.CertPool
	c.TLSCert, c.TLSKey, roots = writeCertificate(t, t.TempDir())
	addr, stop, _ := start(t, c, io.Discard)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}
	client := &http.Client{Transport: transport, Timeout: time.Second}

	for path, want := range map[string]int{"/healthz": 200, "/readyz": 200, "/": 200, "/api/v1/status": 502} {
		res, err := client.Get("https://" + addr + path)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != want || res.TLS == nil || res.TLS.Version < tls.VersionTLS12 {
			t.Fatalf("%s: status %d over %v", path, res.StatusCode, res.TLS)
		}
		if res.Header.Get("Strict-Transport-Security") != "max-age=31536000" {
			t.Fatalf("%s: no HSTS", path)
		}
		if want == 502 && (res.Header.Get("Content-Type") != "application/json" ||
			!strings.HasSuffix(res.Header.Get("Content-Security-Policy"), "; sandbox")) {
			t.Fatalf("%s: HTTPS error response is not inert: %v", path, res.Header)
		}
	}

	obsolete := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11}
	if conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", addr, obsolete); err == nil {
		conn.Close()
		t.Fatal("obsolete TLS version accepted")
	}
	// Shutdown waits up to 5 s for connections that never sent a request, and the
	// client may hold one in its pool, so release them first.
	transport.CloseIdleConnections()
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

func TestMissingTLSMaterialFailsClosed(t *testing.T) {
	c := publicFrontend()
	dir := t.TempDir()
	c.TLSCert, c.TLSKey = filepath.Join(dir, "missing.pem"), filepath.Join(dir, "missing.key")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := Run(context.Background(), c, listener, slog.New(slog.DiscardHandler)); err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Fatalf("missing TLS material did not fail closed: %v", err)
	}
}
