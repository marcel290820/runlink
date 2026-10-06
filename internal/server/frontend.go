package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"runlink/internal/frontend"
)

// statusPath is the only API route, and the only one A forwards to B. WSS and
// other product routes stay closed until their protocols and boundary checks exist.
const statusPath = "/api/v1/status"

const maxUpstreamBody = 64 << 10

// relayedStatuses are the upstream statuses A passes on. Everything else,
// including redirects and upgrade responses, becomes 502.
var relayedStatuses = map[int]bool{
	http.StatusOK:                 true,
	http.StatusBadRequest:         true,
	http.StatusNotFound:           true,
	http.StatusTooManyRequests:    true,
	http.StatusServiceUnavailable: true,
}

// frontendRoutes are A's fixed routes. upstream must already be validated.
func frontendRoutes(upstream string) http.HandlerFunc {
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:                  nil, // B is private; never reach it through a proxy
			MaxIdleConns:           8,
			MaxIdleConnsPerHost:    4,
			IdleConnTimeout:        30 * time.Second,
			ResponseHeaderTimeout:  5 * time.Second,
			MaxResponseHeaderBytes: 16 << 10,
		},
		Timeout:       8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	statusURL := upstream + statusPath
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.RawQuery != "":
			writeError(w, http.StatusBadRequest, "query_not_allowed")
		case r.URL.Path == statusPath:
			relayStatus(r.Context(), w, client, statusURL)
		default:
			serveAsset(w, r.URL.Path)
		}
	}
}

// relayStatus asks B for its status without forwarding anything from the
// recipient's request, and passes on only an allowed status with a bounded JSON
// body. Every upstream header is dropped, so A's inert headers apply.
func relayStatus(ctx context.Context, w http.ResponseWriter, client *http.Client, url string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_unavailable")
		return
	}
	res, err := client.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_unavailable")
		return
	}
	defer res.Body.Close()
	if !relayedStatuses[res.StatusCode] {
		writeError(w, http.StatusBadGateway, "upstream_rejected")
		return
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxUpstreamBody+1))
	if err != nil || len(body) > maxUpstreamBody || !json.Valid(body) {
		writeError(w, http.StatusBadGateway, "upstream_rejected")
		return
	}
	w.WriteHeader(res.StatusCode)
	_, _ = w.Write(body)
}

func serveAsset(w http.ResponseWriter, path string) {
	data, contentType, ok := frontend.Asset(path)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Security-Policy", frontend.CSP)
	_, _ = w.Write(data)
}
