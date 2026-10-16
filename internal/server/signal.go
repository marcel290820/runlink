package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"uuid"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"runlink/internal/frontend"
)

// Signaling routes, the only WebSocket routes. An owner keeps one connection open
// on sharePath and gets a fresh task UUID for it. A recipient's browser opens
// connectPrefix+UUID, receives ICE servers, and sends one offer; B forwards it to
// the owner with ICE servers of its own and returns the owner's answer. No ICE
// candidates trickle: each description already carries them.
const (
	sharePath     = "/api/v1/share"
	connectPrefix = "/api/v1/connect"
)

const (
	maxSignalMessage = 32 << 10 // a description with its candidates; A relays no more
	// ponytail: one global cap; per-owner quotas and a durable registry need owner credentials (ADR 0004).
	maxOwners       = 1024
	maxPending      = 16 // offers waiting for one owner's answers
	exchangeTimeout = 30 * time.Second
	// TURN refreshes reauthenticate, so a relayed session lasts until its credential expires.
	turnLifetime = time.Hour
)

// signalRoute reports whether path is a signaling route.
func signalRoute(path string) bool {
	task, ok := strings.CutPrefix(path, connectPrefix)
	return path == sharePath || ok && frontend.TaskPath.MatchString(task)
}

type iceServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// signaling is B's in-memory registry: a task exists while its owner is connected.
type signaling struct {
	ctx    context.Context // cancelled when the server shuts down
	uris   []string
	secret string

	mu     sync.Mutex
	owners map[string]*owner
}

type owner struct {
	conn *websocket.Conn
	gone chan struct{} // closed when the owner disconnects

	mu      sync.Mutex
	pending map[string]chan string // session ID to the browser waiting for its answer
}

// iceServers issues a coturn TURN REST credential: username "expiry:name",
// password base64(HMAC-SHA1(secret, username)).
func (s *signaling) iceServers() []iceServer {
	if len(s.uris) == 0 {
		return []iceServer{}
	}
	username := fmt.Sprintf("%d:%s", time.Now().Add(turnLifetime).Unix(), rand.Text())
	mac := hmac.New(sha1.New, []byte(s.secret))
	mac.Write([]byte(username))
	return []iceServer{{URLs: s.uris, Username: username, Credential: base64.StdEncoding.EncodeToString(mac.Sum(nil))}}
}

// share registers a task for the owner's connection and forwards its answers.
func (s *signaling) share(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(maxSignalMessage)
	task := uuid.NewV4().String()
	o := &owner{conn: conn, gone: make(chan struct{}), pending: map[string]chan string{}}
	defer close(o.gone)
	s.mu.Lock()
	full := len(s.owners) >= maxOwners
	if !full {
		s.owners[task] = o
	}
	s.mu.Unlock()
	if full {
		_ = wsjson.Write(s.ctx, conn, map[string]string{"error": "capacity_exceeded"})
		return
	}
	defer func() {
		s.mu.Lock()
		delete(s.owners, task)
		s.mu.Unlock()
	}()
	if wsjson.Write(s.ctx, conn, map[string]string{"task": task}) != nil {
		return
	}
	for {
		var answer struct {
			Session string `json:"session"`
			SDP     string `json:"sdp"`
		}
		if wsjson.Read(s.ctx, conn, &answer) != nil {
			return
		}
		o.mu.Lock()
		waiting := o.pending[answer.Session]
		delete(o.pending, answer.Session)
		o.mu.Unlock()
		if waiting != nil {
			waiting <- answer.SDP // buffered, and only ever sent once
		}
	}
}

// connect passes one browser offer for task to its owner and returns the answer.
// Errors are fixed codes: unavailable when no owner shares task, unanswered when
// the owner does not answer in time.
func (s *signaling) connect(w http.ResponseWriter, r *http.Request, task string) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(maxSignalMessage)
	ctx, cancel := context.WithTimeout(s.ctx, exchangeTimeout)
	defer cancel()
	answer, code := s.exchange(ctx, conn, task)
	if code != "" {
		_ = wsjson.Write(ctx, conn, map[string]string{"error": code})
		return
	}
	if wsjson.Write(ctx, conn, map[string]string{"sdp": answer}) == nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}
}

func (s *signaling) exchange(ctx context.Context, conn *websocket.Conn, task string) (answer, code string) {
	s.mu.Lock()
	o := s.owners[task]
	s.mu.Unlock()
	if o == nil {
		return "", "unavailable"
	}
	var offer struct {
		SDP string `json:"sdp"`
	}
	if wsjson.Write(ctx, conn, map[string]any{"ice": s.iceServers()}) != nil || wsjson.Read(ctx, conn, &offer) != nil || offer.SDP == "" {
		return "", "unanswered"
	}
	session, reply := rand.Text(), make(chan string, 1)
	o.mu.Lock()
	full := len(o.pending) >= maxPending
	if !full {
		o.pending[session] = reply
	}
	o.mu.Unlock()
	if full {
		return "", "unanswered"
	}
	defer func() {
		o.mu.Lock()
		delete(o.pending, session)
		o.mu.Unlock()
	}()
	if wsjson.Write(ctx, o.conn, map[string]any{"session": session, "sdp": offer.SDP, "ice": s.iceServers()}) != nil {
		return "", "unanswered"
	}
	select {
	case sdp := <-reply:
		if sdp != "" {
			return sdp, ""
		}
	case <-o.gone:
	case <-ctx.Done():
	}
	return "", "unanswered"
}

// The TURN shared secret has the format C's startup helper accepts.
const maxSecretFile = 4096

var secretFormat = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ReadTURNSecret reads the TURN shared secret at path. The file must be a small
// private regular file; symlinks are not followed and a FIFO cannot block. A
// missing file reports os.ErrNotExist.
func ReadTURNSecret(path string) (string, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", fmt.Errorf("cannot read the TURN secret: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("cannot read the TURN secret: %w", err)
	}
	switch {
	case !info.Mode().IsRegular():
		return "", errors.New("the TURN secret must be a regular file")
	case info.Mode().Perm()&0o077 != 0:
		return "", errors.New("the TURN secret file must be private (0600)")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSecretFile+1))
	if err != nil {
		return "", fmt.Errorf("cannot read the TURN secret: %w", err)
	}
	secret := strings.TrimSpace(string(data)) // as C's startup helper reads it
	if len(data) > maxSecretFile || !secretFormat.MatchString(secret) {
		return "", errors.New("the TURN secret must be 64 lowercase hex characters")
	}
	return secret, nil
}
