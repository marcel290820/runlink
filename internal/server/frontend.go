package server

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"runlink/internal/frontend"
)

// frontendRoutes belongs to A. Only the fixed status API can reach B in this scaffold.
// WSS and other product routes remain closed until their protocols are implemented.
func frontendRoutes(upstream string) http.HandlerFunc {
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:                  nil,
			MaxIdleConns:           8,
			MaxIdleConnsPerHost:    4,
			IdleConnTimeout:        30 * time.Second,
			ResponseHeaderTimeout:  5 * time.Second,
			MaxResponseHeaderBytes: 16384,
		},
		Timeout:       8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			jsonError(w, 400, "query_not_allowed")
			return
		}
		if r.URL.Path == "/api/v1/status" {
			req, err := http.NewRequestWithContext(r.Context(), "GET", upstream+"/api/v1/status", nil)
			if err != nil {
				jsonError(w, 502, "upstream_unavailable")
				return
			}
			// Do not forward recipient headers, cookies, credentials, query strings or bodies.
			res, err := client.Do(req)
			if err != nil {
				jsonError(w, 502, "upstream_unavailable")
				return
			}
			defer res.Body.Close()
			if res.StatusCode != 200 && res.StatusCode != 400 && res.StatusCode != 404 && res.StatusCode != 429 && res.StatusCode != 503 {
				jsonError(w, 502, "upstream_rejected")
				return
			}
			body, err := io.ReadAll(io.LimitReader(res.Body, 65537))
			if err != nil || len(body) > 65536 || !json.Valid(body) {
				jsonError(w, 502, "upstream_rejected")
				return
			}
			// All upstream headers are discarded, including Set-Cookie and SW scope headers.
			w.WriteHeader(res.StatusCode)
			_, _ = w.Write(body)
			return
		}
		data, contentType, ok := frontend.Asset(r.URL.Path)
		if !ok {
			jsonError(w, 404, "not_found")
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Security-Policy", frontend.CSP)
		_, _ = w.Write(data)
	}
}
