package frontend

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

//go:embed assets/*
var Assets embed.FS

var taskPath = regexp.MustCompile(`^/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func ControlHeaders(h http.Header) {
	h.Set("Content-Type", "application/json")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; sandbox")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
}
func JSONError(w http.ResponseWriter, code int, message string) {
	ControlHeaders(w.Header())
	w.WriteHeader(code)
	_, _ = fmt.Fprintf(w, "{\"error\":%q}\n", message)
}
func hash(data []byte) string {
	value := sha256.Sum256(data)
	return base64.StdEncoding.EncodeToString(value[:])
}

// Handler belongs to A. Only the fixed status API can reach B in this scaffold.
// WSS and other product routes remain closed until their protocols are implemented.
func Handler(upstream string) http.Handler {
	transport := &http.Transport{Proxy: nil, MaxIdleConns: 8, MaxIdleConnsPerHost: 4, IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 16384}
	client := &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	capture, _ := Assets.ReadFile("assets/capture.js")
	loader, _ := Assets.ReadFile("assets/loader.mjs")
	csp := "default-src 'none'; script-src 'sha256-" + hash([]byte(strings.TrimSuffix(string(capture), "\n"))) + "' 'sha256-" + hash(loader) + "' 'strict-dynamic'; style-src 'self'; connect-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; worker-src 'none'; require-trusted-types-for 'script'; trusted-types runlink-gui"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ControlHeaders(w.Header())
		if r.Header.Get("Sec-Fetch-Dest") == "serviceworker" || r.Header.Get("Service-Worker") != "" {
			JSONError(w, 403, "service_workers_disabled")
			return
		}
		if r.Header.Get("Upgrade") != "" {
			JSONError(w, 400, "upgrades_disabled")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			JSONError(w, 405, "method_not_allowed")
			return
		}
		if r.URL.RawQuery != "" {
			JSONError(w, 400, "query_not_allowed")
			return
		}
		if r.URL.Path == "/api/v1/status" {
			req, err := http.NewRequestWithContext(r.Context(), "GET", upstream+"/api/v1/status", nil)
			if err != nil {
				JSONError(w, 502, "upstream_unavailable")
				return
			}
			// Do not forward recipient headers, cookies, credentials, query strings or bodies.
			res, err := client.Do(req)
			if err != nil {
				JSONError(w, 502, "upstream_unavailable")
				return
			}
			defer res.Body.Close()
			if res.StatusCode != 200 && res.StatusCode != 400 && res.StatusCode != 404 && res.StatusCode != 429 && res.StatusCode != 503 {
				JSONError(w, 502, "upstream_rejected")
				return
			}
			body, err := io.ReadAll(io.LimitReader(res.Body, 65537))
			if err != nil || len(body) > 65536 || !json.Valid(body) {
				JSONError(w, 502, "upstream_rejected")
				return
			}
			// All upstream headers are discarded, including Set-Cookie and SW scope headers.
			w.WriteHeader(res.StatusCode)
			_, _ = w.Write(body)
			return
		}
		name := ""
		mime := "text/html; charset=utf-8"
		if r.URL.Path == "/" || taskPath.MatchString(r.URL.Path) {
			name = "index.html"
		}
		switch r.URL.Path {
		case "/assets/loader.mjs":
			name, mime = "loader.mjs", "text/javascript; charset=utf-8"
		case "/assets/style.css":
			name, mime = "style.css", "text/css; charset=utf-8"
		case "/assets/manifest.json":
			name, mime = "manifest.json", "application/json"
		}
		if name == "" {
			JSONError(w, 404, "not_found")
			return
		}
		data, err := Assets.ReadFile("assets/" + name)
		if err != nil {
			JSONError(w, 500, "asset_unavailable")
			return
		}
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Content-Security-Policy", csp)
		if r.Method != "HEAD" {
			_, _ = w.Write(data)
		}
	})
}

func VerifyAssets() error {
	data, err := Assets.ReadFile("assets/manifest.json")
	if err != nil {
		return err
	}
	var manifest map[string]struct {
		SHA256 string `json:"sha256"`
		Size   int    `json:"size"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return err
	}
	entries, err := Assets.ReadDir("assets")
	if err != nil {
		return err
	}
	if len(manifest) != len(entries)-1 {
		return fmt.Errorf("asset manifest coverage mismatch")
	}
	for _, e := range entries {
		if e.Name() == "manifest.json" {
			continue
		}
		content, err := Assets.ReadFile("assets/" + e.Name())
		if err != nil {
			return err
		}
		sum := sha256.Sum256(content)
		record, ok := manifest[e.Name()]
		if !ok || record.Size != len(content) || record.SHA256 != hex.EncodeToString(sum[:]) {
			return fmt.Errorf("asset verification failed")
		}
	}
	return nil
}
