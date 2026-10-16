package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"runlink/internal/frontend"
)

var turnSecret = strings.Repeat("0123456789abcdef", 4) // a fixture, as C's 64 hex characters

// startSignaling runs B with TURN credentials and A in front of it, and returns
// A's WebSocket origin and a stop for each.
func startSignaling(t *testing.T) (origin string, stopA, stopB func() error) {
	t.Helper()
	app := appConfig(t)
	app.TURNURIs = []string{"stun:127.0.0.1:3478", "turn:127.0.0.1:3478?transport=udp"}
	app.TURNSecretFile = filepath.Join(t.TempDir(), "turn-secret")
	if err := os.WriteFile(app.TURNSecretFile, []byte(turnSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, stopB, _ := start(t, app, io.Discard)
	a, stopA, _ := start(t, frontendConfig("http://"+b), io.Discard)
	return "ws://" + a, stopA, stopB
}

func dial(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(t.Context(), url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func receive[T any](t *testing.T, conn *websocket.Conn) T {
	t.Helper()
	var v T
	if err := wsjson.Read(t.Context(), conn, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

type signalMessage struct {
	Task, Session, SDP, Error string
	ICE                       []iceServer
}

func TestSignalingThroughFrontend(t *testing.T) {
	origin, stopA, stopB := startSignaling(t)
	owner := dial(t, origin+sharePath)
	task := receive[signalMessage](t, owner).Task
	if !frontend.TaskPath.MatchString("/" + task) {
		t.Fatalf("task %q", task)
	}

	browser := dial(t, origin+connectPrefix+"/"+task)
	ice := receive[signalMessage](t, browser).ICE
	if len(ice) != 1 || len(ice[0].URLs) != 2 {
		t.Fatalf("ICE servers %+v", ice)
	}
	// coturn's TURN REST scheme, checked independently: an expiry an hour out.
	expiry, _, _ := strings.Cut(ice[0].Username, ":")
	seconds, err := strconv.ParseInt(expiry, 10, 64)
	mac := hmac.New(sha1.New, []byte(turnSecret))
	mac.Write([]byte(ice[0].Username))
	if err != nil || time.Until(time.Unix(seconds, 0)).Round(time.Minute) != turnLifetime ||
		ice[0].Credential != base64.StdEncoding.EncodeToString(mac.Sum(nil)) {
		t.Fatalf("TURN credential %+v", ice[0])
	}
	if err := wsjson.Write(t.Context(), browser, map[string]string{"sdp": "offer"}); err != nil {
		t.Fatal(err)
	}
	offer := receive[signalMessage](t, owner)
	if offer.SDP != "offer" || offer.Session == "" || len(offer.ICE) != 1 || offer.ICE[0].Username == ice[0].Username {
		t.Fatalf("forwarded offer %+v", offer)
	}
	if err := wsjson.Write(t.Context(), owner, map[string]string{"session": offer.Session, "sdp": "answer"}); err != nil {
		t.Fatal(err)
	}
	if answer := receive[signalMessage](t, browser); answer.SDP != "answer" {
		t.Fatalf("answer %+v", answer)
	}
	if _, _, err := browser.Read(t.Context()); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("signaling stayed open after the answer: %v", err)
	}

	// An owner that answers with nothing declines at once.
	browser = dial(t, origin+connectPrefix+"/"+task)
	receive[signalMessage](t, browser)
	_ = wsjson.Write(t.Context(), browser, map[string]string{"sdp": "offer"})
	_ = wsjson.Write(t.Context(), owner, map[string]string{"session": receive[signalMessage](t, owner).Session})
	if got := receive[signalMessage](t, browser).Error; got != "unanswered" {
		t.Fatalf("declined offer: %q", got)
	}

	// An owner that disconnects mid-exchange ends the exchange and its task.
	browser = dial(t, origin+connectPrefix+"/"+task)
	receive[signalMessage](t, browser)
	_ = wsjson.Write(t.Context(), browser, map[string]string{"sdp": "offer"})
	receive[signalMessage](t, owner)
	_ = owner.Close(websocket.StatusNormalClosure, "")
	if got := receive[signalMessage](t, browser).Error; got != "unanswered" {
		t.Fatalf("owner gone: %q", got)
	}
	for _, id := range []string{task, "12345678-1234-4234-8234-123456789abc"} {
		if got := receive[signalMessage](t, dial(t, origin+connectPrefix+"/"+id)).Error; got != "unavailable" {
			t.Fatalf("task %s: %q", id, got)
		}
	}

	// Shutdown closes open owner connections instead of waiting for them.
	owner = dial(t, origin+sharePath)
	receive[signalMessage](t, owner)
	began := time.Now()
	if err := stopB(); err != nil {
		t.Fatal(err)
	}
	if err := stopA(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := owner.Read(t.Context()); err == nil || time.Since(began) > 2*time.Second {
		t.Fatalf("owner connection survived shutdown: %v", err)
	}
}

func TestSignalingCapacity(t *testing.T) {
	s := &signaling{ctx: t.Context(), owners: map[string]*owner{}}
	for i := range maxOwners {
		s.owners[strconv.Itoa(i)] = &owner{}
	}
	server := httptest.NewServer(http.HandlerFunc(s.share))
	defer server.Close()
	if got := receive[signalMessage](t, dial(t, server.URL)).Error; got != "capacity_exceeded" || len(s.owners) != maxOwners {
		t.Fatalf("full registry: %q, %d owners", got, len(s.owners))
	}
}

// B is not trusted with A's origin: its handshake headers never reach the
// client, client headers never reach it, and it can send only small JSON text.
func TestRelayConfinesUpstream(t *testing.T) {
	headers := make(chan http.Header, 8)
	sends := make(chan func(*websocket.Conn), 8)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Clone()
		w.Header().Set("Set-Cookie", "secret=evil")
		w.Header().Set("Service-Worker-Allowed", "/")
		w.Header().Set("Content-Security-Policy", "default-src *")
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		(<-sends)(conn)
		_, _, _ = conn.Read(context.Background())
	}))
	defer upstream.Close()
	a, _, _ := start(t, frontendConfig(upstream.URL), io.Discard)

	for name, send := range map[string]func(*websocket.Conn){
		"binary":   func(c *websocket.Conn) { _ = c.Write(context.Background(), websocket.MessageBinary, []byte(`{}`)) },
		"not JSON": func(c *websocket.Conn) { _ = c.Write(context.Background(), websocket.MessageText, []byte(`<script>`)) },
		"oversized": func(c *websocket.Conn) {
			_ = c.Write(context.Background(), websocket.MessageText, []byte(`"`+strings.Repeat("x", 2*maxSignalMessage)+`"`))
		},
	} {
		sends <- send
		conn, res, err := websocket.Dial(t.Context(), "ws://"+a+sharePath, &websocket.DialOptions{
			HTTPHeader: http.Header{"Cookie": {"recipient=secret"}, "Authorization": {"secret"}}})
		if err != nil {
			t.Fatal(name, err)
		}
		forwarded := <-headers
		if forwarded.Get("Cookie") != "" || forwarded.Get("Authorization") != "" {
			t.Fatalf("%s: client headers reached B: %v", name, forwarded)
		}
		for _, header := range []string{"Set-Cookie", "Service-Worker-Allowed"} {
			if res.Header.Get(header) != "" {
				t.Fatalf("%s: B's %s reached the client", name, header)
			}
		}
		if _, data, err := conn.Read(t.Context()); err == nil {
			t.Fatalf("%s: relayed %q", name, data)
		}
	}

	// Without B, the handshake fails with A's inert JSON error.
	upstream.Close()
	req, _ := http.NewRequest("GET", "http://"+a+sharePath, nil)
	for k, v := range map[string]string{"Connection": "Upgrade", "Upgrade": "websocket", "Sec-WebSocket-Version": "13", "Sec-WebSocket-Key": base64.StdEncoding.EncodeToString([]byte("the sample nonce"))} {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadGateway || res.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("no upstream: %d %v", res.StatusCode, res.Header)
	}
}

func TestRunRefusesUnsafeTURNSecret(t *testing.T) {
	dir := t.TempDir()
	for name, mode := range map[string]os.FileMode{"missing": 0, "shared": 0o644, "malformed": 0o600} {
		c := appConfig(t)
		c.TURNURIs, c.TURNSecretFile = []string{"turn:127.0.0.1:3478"}, filepath.Join(dir, name)
		if mode != 0 {
			content := turnSecret
			if name == "malformed" {
				content = "not-hex"
			}
			if err := os.WriteFile(c.TURNSecretFile, []byte(content), mode); err != nil || os.Chmod(c.TURNSecretFile, mode) != nil {
				t.Fatal(err)
			}
		}
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		if err := Run(t.Context(), c, listener, slog.New(slog.DiscardHandler)); err == nil || !strings.Contains(err.Error(), "TURN secret") {
			t.Errorf("%s secret: %v", name, err)
		}
		listener.Close()
	}
}
