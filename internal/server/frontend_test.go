package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"runlink/internal/frontend"
)

const taskURL = "/12345678-1234-1234-1234-123456789abc"

func frontendHandler(upstream string) http.Handler {
	return Handler(frontendConfig(upstream), new(atomic.Bool))
}

// hostileUpstream answers every request with status and body, plus headers that
// would make the body executable, persistent, or a worker if A passed them on.
func hostileUpstream(t *testing.T, status int, body string) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != statusPath || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("A forwarded recipient data: %s %v", r.URL, r.Header)
		}
		h := w.Header()
		h.Set("Content-Type", "text/javascript")
		h.Set("Content-Security-Policy", "default-src * 'unsafe-inline'")
		h.Set("Service-Worker-Allowed", "/")
		h.Set("Set-Cookie", "secret=evil")
		h.Set("Location", "/evil.js")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestIngressRelaysOnlyBoundedJSON(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   int
	}{
		{"html", 200, "<script>window.pwned=true</script>", 502},
		{"svg", 200, "<svg onload='alert(1)'/>", 502},
		{"javascript", 200, "window.pwned=true", 502},
		{"redirect", 302, `{}`, 502},
		{"failed upgrade", 101, `{}`, 502},
		{"unlisted status", 500, `{"error":"internal"}`, 502},
		{"html error", 503, "<html>bad</html>", 502},
		{"oversized", 200, strings.Repeat(" ", maxUpstreamBody) + `{}`, 502},
		{"status JSON", 200, `{"status":"scaffold"}`, 200},
		{"JSON error", 503, `{"error":"unavailable"}`, 503},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			upstream := hostileUpstream(t, tc.status, tc.body)
			r := httptest.NewRequest("GET", statusPath, nil)
			r.Header.Set("Authorization", "secret")
			r.Header.Set("Cookie", "secret")
			w := httptest.NewRecorder()
			frontendHandler(upstream.URL).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d", w.Code, tc.want)
			}
			if tc.want == 200 && w.Body.String() != tc.body {
				t.Fatalf("body %q", w.Body)
			}
			h := w.Header()
			if h.Get("Content-Type") != "application/json" || h.Get("X-Content-Type-Options") != "nosniff" ||
				!strings.HasSuffix(h.Get("Content-Security-Policy"), "; sandbox") {
				t.Fatalf("inert headers replaced: %v", h)
			}
			for _, name := range []string{"Location", "Set-Cookie", "Service-Worker-Allowed"} {
				if h.Get(name) != "" {
					t.Fatalf("upstream %s relayed", name)
				}
			}
		})
	}
}

// A disallowed status is rejected from its headers alone, so a stalled body
// cannot hold the recipient's request open.
func TestIngressRejectsStatusBeforeBody(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusFound)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	defer close(release)
	done := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		frontendHandler(upstream.URL).ServeHTTP(w, httptest.NewRequest("GET", statusPath, nil))
		done <- w.Code
	}()
	select {
	case code := <-done:
		if code != http.StatusBadGateway {
			t.Fatalf("status %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("A waited for the body of a disallowed status")
	}
}

func TestIngressRoutesAreFixed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("unexpected upstream request")
	}))
	defer upstream.Close()
	handler := frontendHandler(upstream.URL)

	for _, path := range []string{"/api/unknown", "/assets/gui.js", "/assets/capture.js", "/evil.js",
		"/../api/v1/status", "/api/v1/status?secret=value", "/?secret=value", strings.ToUpper(taskURL), taskURL + "/"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code < 400 || w.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("%s: status %d, type %s", path, w.Code, w.Header().Get("Content-Type"))
		}
	}

	pages := map[string]string{
		"/":                     "text/html; charset=utf-8",
		taskURL:                 "text/html; charset=utf-8",
		"/assets/loader.mjs":    "text/javascript; charset=utf-8",
		"/assets/style.css":     "text/css; charset=utf-8",
		"/assets/manifest.json": "application/json",
	}
	for path, contentType := range pages {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || w.Header().Get("Content-Type") != contentType || w.Header().Get("Content-Security-Policy") != frontend.CSP ||
			w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s: status %d, headers %v", path, w.Code, w.Header())
		}
		for header, value := range map[string]string{"Service-Worker": "script", "Sec-Fetch-Dest": "serviceworker", "Upgrade": "websocket"} {
			r := httptest.NewRequest("GET", path, nil)
			r.Header.Set(header, value)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code < 400 || w.Header().Get("Service-Worker-Allowed") != "" {
				t.Fatalf("%s with %s: status %d", path, header, w.Code)
			}
		}
	}
	if !strings.Contains(frontend.CSP, "worker-src 'none'") {
		t.Fatal("page CSP allows workers")
	}
}
