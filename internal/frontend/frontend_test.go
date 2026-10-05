package frontend

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedAssets(t *testing.T) {
	if err := VerifyAssets(); err != nil {
		t.Fatal(err)
	}
}
func TestIngressRejectsMaliciousUpstream(t *testing.T) {
	tests := []struct {
		name, body string
		status     int
		want       int
	}{
		{"html", "<script>window.pwned=true</script>", 200, 502},
		{"svg", "<svg onload='alert(1)'/>", 200, 502},
		{"js", "window.pwned=true", 200, 502},
		{"redirect", `{}`, 302, 502},
		{"failed upgrade", `{}`, 101, 502},
		{"upstream error html", "<html>bad</html>", 503, 502},
		{"oversized", strings.Repeat(" ", 65536) + `{}`, 200, 502},
		{"json with hostile headers", `{"status":"scaffold"}`, 200, 200},
		{"safe json error", `{"error":"unavailable"}`, 503, 503},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/status" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("forwarded private data")
				}
				w.Header().Set("Content-Type", "text/javascript")
				w.Header().Set("Content-Security-Policy", "default-src * 'unsafe-inline'")
				w.Header().Set("Service-Worker-Allowed", "/")
				w.Header().Set("Set-Cookie", "secret=evil")
				w.Header().Set("Location", "/evil.js")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer upstream.Close()
			req := httptest.NewRequest("GET", "/api/v1/status", nil)
			req.Header.Set("Authorization", "secret")
			req.Header.Set("Cookie", "secret")
			w := httptest.NewRecorder()
			Handler(upstream.URL).ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("got %d, want %d", w.Code, tc.want)
			}
			if w.Header().Get("Content-Type") != "application/json" || w.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
				t.Fatal(w.Header())
			}
			for _, header := range []string{"Location", "Set-Cookie", "Service-Worker-Allowed"} {
				if w.Header().Get(header) != "" {
					t.Fatal(header)
				}
			}
		})
	}
}
func TestIngressRoutesAreFixed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected upstream request") }))
	defer upstream.Close()
	handler := Handler(upstream.URL)
	for _, path := range []string{"/api/unknown", "/assets/gui.js", "/evil.js", "/../api/v1/status", "/api/v1/status?secret=value"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code < 400 {
			t.Fatal(path)
		}
	}
	for _, path := range []string{"/", "/12345678-1234-1234-1234-123456789abc", "/assets/loader.mjs"} {
		for _, header := range []string{"Service-Worker", "Sec-Fetch-Dest", "Upgrade"} {
			r := httptest.NewRequest("GET", path, nil)
			value := "serviceworker"
			if header == "Upgrade" {
				value = "websocket"
			}
			r.Header.Set(header, value)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code < 400 || w.Header().Get("Service-Worker-Allowed") != "" {
				t.Fatal(path, header)
			}
		}
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/12345678-1234-1234-1234-123456789abc", nil))
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Security-Policy"), "worker-src 'none'") {
		t.Fatal(w.Code, w.Header())
	}
}
