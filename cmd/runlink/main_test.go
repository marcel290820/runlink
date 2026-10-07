package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"runlink/internal/buildinfo"
)

func env(service string) func(string) string {
	return func(key string) string {
		if key == "RUNLINK_SERVICE" {
			return service
		}
		return ""
	}
}

func TestStatusReportsService(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/status" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"status":"scaffold"}`))
	}))
	defer upstream.Close()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"status"}, env(upstream.URL), &stdout, &stderr); code != 0 || stdout.String() != upstream.URL+": scaffold\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}

func TestServiceOrigin(t *testing.T) {
	for value, want := range map[string]string{
		"":                          buildinfo.Service,
		"https://runlink.dev":       "https://runlink.dev",
		"https://example.test:8443": "https://example.test:8443",
		"http://127.0.0.1:9090":     "http://127.0.0.1:9090",
		"http://[::1]:9090":         "http://[::1]:9090",
	} {
		if got, err := service(env(value)); err != nil || got != want {
			t.Errorf("%q: got %q, %v", value, got, err)
		}
	}
	for _, value := range []string{
		"http://runlink.dev", "http://localhost:8080", "http://192.168.1.2:8080", "runlink.dev",
		"https://runlink.dev/", "https://runlink.dev/path", "https://user:secret@runlink.dev",
		"https://runlink.dev?q", "https://runlink.dev#fragment", "ftp://runlink.dev", "https://",
	} {
		if _, err := service(env(value)); err != errOrigin && err != errScheme {
			t.Errorf("%q: %v", value, err)
		}
	}
}

func TestStatusRejectsUnexpectedResponses(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"error status": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) },
		"redirect":     func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/elsewhere", http.StatusFound) },
		"not JSON":     func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) },
		"terminal escape": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"status":"\u001b[2Jok"}`))
		},
		"trailing data": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"ok"}garbage`)) },
		"oversized": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"status":"ok","padding":"` + strings.Repeat("x", maxStatusBody) + `"}`))
		},
		"truncated": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "100")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		},
	} {
		upstream := httptest.NewServer(handler)
		var stdout, stderr bytes.Buffer
		if code := run([]string{"status"}, env(upstream.URL), &stdout, &stderr); code != 1 || stdout.Len() != 0 ||
			!strings.Contains(stderr.String(), "unexpected response") {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", name, code, stdout.String(), stderr.String())
		}
		upstream.Close()
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"status"}, env("http://127.0.0.1:1"), &stdout, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "start the local stack with scripts/dev.sh") {
		t.Fatalf("unreachable: exit %d, stderr %q", code, stderr.String())
	}
}
