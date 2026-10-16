package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"runlink/internal/frontend"
)

// statusPath is the only plain API route. A forwards it and the signaling
// WebSocket routes to B; every other route stays closed.
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
// Cancelling ctx closes relayed signaling connections.
func frontendRoutes(ctx context.Context, upstream string) http.HandlerFunc {
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
		case signalRoute(r.URL.Path):
			relaySignal(ctx, w, r, client, "ws"+strings.TrimPrefix(upstream, "http")+r.URL.Path)
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

// relaySignal opens the same signaling route on B, then accepts the client's
// WebSocket and relays bounded JSON text messages both ways. Neither side sees
// the other's handshake headers, and B can send nothing but small JSON.
func relaySignal(ctx context.Context, w http.ResponseWriter, r *http.Request, client *http.Client, url string) {
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	upstream, _, err := websocket.Dial(dialCtx, url, &websocket.DialOptions{HTTPClient: client})
	cancel()
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_unavailable")
		return
	}
	defer upstream.CloseNow()
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	// The first direction to end, including by shutdown, ends both. A close
	// handshake passes on to the other side.
	ctx, cancel = context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		relay(ctx, conn, upstream)
		cancel()
	}()
	relay(ctx, upstream, conn)
	cancel()
	<-done
}

// relay passes small JSON text messages from one side to the other until either
// fails, then closes the other side with from's close status, if it sent one.
func relay(ctx context.Context, from, to *websocket.Conn) {
	// Room for the ICE servers B adds to an offer it forwards.
	from.SetReadLimit(2 * maxSignalMessage)
	for {
		kind, data, err := from.Read(ctx)
		if status := websocket.CloseStatus(err); status != -1 {
			_ = to.Close(status, "")
		}
		if err != nil || kind != websocket.MessageText || !json.Valid(data) || to.Write(ctx, websocket.MessageText, data) != nil {
			return
		}
	}
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
