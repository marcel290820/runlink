package devstack

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pion/turn/v4"
)

// The secret file format C's startup helper accepts, spelled independently of B.
const maxSecretFile = 4096

var secretFormat = regexp.MustCompile(`^[0-9a-f]{64}$`)

// restCredential computes coturn's TURN REST credential independently of Pion,
// as scripts/turn-check.py does for the real server.
func restCredential(secret, name string, expiry time.Time) (username, password string) {
	username = strconv.FormatInt(expiry.Unix(), 10) + ":" + name
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(username))
	return username, base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// turnClient connects a TURN client to the stack over UDP from its own socket,
// so every client has its own allocation.
func turnClient(t *testing.T, address, username, password string) *turn.Client {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return newTURNClient(t, address, conn, username, password)
}

// tcpTURNClient reaches the stack over TCP, as a browser restricted to TURN
// over TCP does.
func tcpTURNClient(t *testing.T, address, username, password string) (*turn.Client, net.Conn) {
	t.Helper()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	return newTURNClient(t, address, turn.NewSTUNConn(conn), username, password), conn
}

func newTURNClient(t *testing.T, address string, conn net.PacketConn, username, password string) *turn.Client {
	t.Helper()
	client, err := turn.NewClient(&turn.ClientConfig{
		STUNServerAddr: address, TURNServerAddr: address, Username: username, Password: password,
		Conn: conn, LoggerFactory: pionLogger{slog.New(slog.DiscardHandler)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close(); _ = conn.Close() })
	return client
}

func TestTURNRelaysBetweenAllocations(t *testing.T) {
	stack := startStack(t)
	expiry := time.Now().Add(10 * time.Minute)
	var relays []net.PacketConn
	for _, name := range []string{"owner", "recipient"} {
		username, password := restCredential(stack.TURNSecret, name, expiry)
		relay, err := turnClient(t, stack.TURN, username, password).Allocate()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = relay.Close() })
		addr := relay.LocalAddr().(*net.UDPAddr)
		if !addr.IP.IsLoopback() || addr.Port < relayMinPort || addr.Port > relayMaxPort {
			t.Fatalf("relay address %v outside the loopback relay range", addr)
		}
		relays = append(relays, relay)
	}

	// Each side permits the other, then data crosses relay to relay, the path
	// two peers forced onto C take.
	if _, err := relays[1].WriteTo([]byte("permit"), relays[0].LocalAddr()); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	for attempt := 0; ; attempt++ {
		if _, err := relays[0].WriteTo([]byte("ping"), relays[1].LocalAddr()); err != nil {
			t.Fatal(err)
		}
		_ = relays[1].SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, from, err := relays[1].ReadFrom(buf)
		if err == nil {
			if string(buf[:n]) != "ping" || from.String() != relays[0].LocalAddr().String() {
				t.Fatalf("received %q from %v", buf[:n], from)
			}
			break
		}
		if attempt == 10 {
			t.Fatal(err)
		}
	}
}

func TestTURNRejectsInvalidCredentials(t *testing.T) {
	stack := startStack(t)
	future := time.Now().Add(10 * time.Minute)
	expiredUser, expiredPassword := restCredential(stack.TURNSecret, "expired", time.Now().Add(-time.Minute))
	forgedUser, forgedPassword := restCredential(strings.Repeat("0", 64), "forged", future)
	wrongUser, _ := restCredential(stack.TURNSecret, "wrong", future)
	for _, credential := range [][2]string{{expiredUser, expiredPassword}, {forgedUser, forgedPassword}, {wrongUser, "wrong"}} {
		if _, err := turnClient(t, stack.TURN, credential[0], credential[1]).Allocate(); err == nil {
			t.Errorf("%s allocated a relay", credential[0])
		}
	}
}

func TestTURNKeepsCoturnBounds(t *testing.T) {
	stack := startStack(t)
	username, password := restCredential(stack.TURNSecret, "quota", time.Now().Add(10*time.Minute))
	for i := range userQuota {
		relay, err := turnClient(t, stack.TURN, username, password).Allocate()
		if err != nil {
			t.Fatalf("allocation %d: %v", i+1, err)
		}
		t.Cleanup(func() { _ = relay.Close() })
	}
	if _, err := turnClient(t, stack.TURN, username, password).Allocate(); err == nil {
		t.Fatal("allocation beyond the per-credential quota succeeded")
	}

	username, password = restCredential(stack.TURNSecret, "policy", time.Now().Add(10*time.Minute))
	client := turnClient(t, stack.TURN, username, password)
	relay, err := client.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	if err := client.CreatePermission(&net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 9}); err == nil {
		t.Fatal("permission for a peer outside loopback was granted")
	}
	if err := client.CreatePermission(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9}); err != nil {
		t.Fatalf("loopback peer: %v", err)
	}
}

func TestTURNSecretFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "turn-secret")
	secret, err := loadOrCreateSecret(path)
	if err != nil || !secretFormat.MatchString(secret) {
		t.Fatalf("generated %q: %v", secret, err)
	}
	if again, err := loadOrCreateSecret(path); err != nil || again != secret {
		t.Fatalf("reloaded %q: %v", again, err)
	}

	// C's startup helper strips surrounding whitespace; so does the stand-in.
	valid := strings.Repeat("ab", 32)
	if err := os.WriteFile(path, []byte(" "+valid+"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := loadOrCreateSecret(path); err != nil || got != valid {
		t.Fatalf("whitespace: %q %v", got, err)
	}
	for content, mode := range map[string]os.FileMode{
		strings.ToUpper(valid):                     0o600,
		valid[:62]:                                 0o600,
		valid + "\nextra=injected":                 0o600,
		valid + strings.Repeat(" ", maxSecretFile): 0o600,
		valid: 0o640,
	} {
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		_, err := loadOrCreateSecret(path)
		if err == nil || strings.Contains(err.Error(), content[:8]) {
			t.Errorf("mode %v: accepted or echoed: %v", mode, err)
		}
	}
}

// A FIFO or symlink at the secret path is refused at once instead of blocking
// startup or reading whatever the link points at.
func TestTURNSecretRefusesSpecialFiles(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid")
	if err := os.WriteFile(valid, []byte(strings.Repeat("ab", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	idle, fed, link := filepath.Join(dir, "idle"), filepath.Join(dir, "fed"), filepath.Join(dir, "link")
	if err := errors.Join(syscall.Mkfifo(idle, 0o600), syscall.Mkfifo(fed, 0o600), os.Symlink(valid, link)); err != nil {
		t.Fatal(err)
	}
	// Another process feeds one FIFO a well-formed secret and keeps it open.
	writer, err := os.OpenFile(fed, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.WriteString(strings.Repeat("cd", 32)); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{idle, fed, link} {
		done := make(chan error, 1)
		go func() { _, err := loadOrCreateSecret(path); done <- err }()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("%s accepted", filepath.Base(path))
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s blocked", filepath.Base(path))
		}
	}
}

func TestTURNLogsRejectionsWithoutPasswords(t *testing.T) {
	var logs bytes.Buffer
	c := ephemeral
	c.StateDir = t.TempDir()
	stack, err := Start(t.Context(), c, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	username, _ := restCredential(stack.TURNSecret, "debug-me", time.Now().Add(time.Minute))
	_, _ = turnClient(t, stack.TURN, username, "never-logged").Allocate()
	if err := stack.Stop(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), `"msg":"turn_auth_rejected"`) || !strings.Contains(logs.String(), username) ||
		strings.Contains(logs.String(), "never-logged") || strings.Contains(logs.String(), stack.TURNSecret) {
		t.Fatalf("logs:\n%s", logs.String())
	}
}

func TestTURNQuotaHoldsUnderConcurrentAllocations(t *testing.T) {
	stack := startStack(t)
	username, password := restCredential(stack.TURNSecret, "burst", time.Now().Add(10*time.Minute))
	clients := make([]*turn.Client, 3*userQuota)
	for i := range clients {
		clients[i], _ = tcpTURNClient(t, stack.TURN, username, password)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	granted := 0
	for _, client := range clients {
		wg.Go(func() {
			if relay, err := client.Allocate(); err == nil {
				t.Cleanup(func() { _ = relay.Close() })
				mu.Lock()
				granted++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if granted != userQuota {
		t.Fatalf("%d concurrent allocations granted, want %d", granted, userQuota)
	}
}

func TestQuotaReleasesFailedReservations(t *testing.T) {
	q := &quota{users: map[string]int{}, reserved: map[string]string{}}
	clients := make([]net.Addr, userQuota+1)
	for i := range clients {
		clients[i] = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5000 + i}
	}
	for _, client := range clients[:userQuota] {
		if !q.allow("user", turnRealm, client) {
			t.Fatal("reservation within quota refused")
		}
	}
	if q.allow("user", turnRealm, clients[userQuota]) {
		t.Fatal("reservation beyond quota granted")
	}
	q.created(clients[0], nil, "UDP", "user", turnRealm, nil, 0)
	q.failed(clients[0], nil, "UDP", "a later request failed") // the allocation exists; nothing to release
	q.failed(clients[1], nil, "UDP", "allocation failed")
	if !q.allow("user", turnRealm, clients[userQuota]) {
		t.Fatal("failed allocation kept its reservation")
	}
	q.deleted(clients[0], nil, "UDP", "user", turnRealm)
	if !q.allow("user", turnRealm, clients[1]) || q.total != userQuota {
		t.Fatalf("deleted allocation kept its slot: total %d", q.total)
	}
}

func TestTURNCloseReleasesClientsAndRelays(t *testing.T) {
	secret := strings.Repeat("ab", 32)
	server, address, err := startTURN(netip.MustParseAddrPort("127.0.0.1:0"), secret, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	username, password := restCredential(secret, "shutdown", time.Now().Add(10*time.Minute))
	udpRelay, err := turnClient(t, address.String(), username, password).Allocate()
	if err != nil {
		t.Fatal(err)
	}
	tcpClient, tcpConn := tcpTURNClient(t, address.String(), username, password)
	tcpRelay, err := tcpClient.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	open := len(server.conns) + server.relays
	server.mu.Unlock()
	if open != 0 {
		t.Fatalf("Close returned with %d TURN connections or relays open", open)
	}
	// Pion's own Close would leave both relay ports bound and the TCP client connected.
	for _, relay := range []net.PacketConn{udpRelay, tcpRelay} {
		port, err := net.ListenUDP("udp4", relay.LocalAddr().(*net.UDPAddr))
		if err != nil {
			t.Fatalf("relay port still bound after Close: %v", err)
		}
		_ = port.Close()
	}
	_ = tcpConn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := tcpConn.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("TCP client still connected after Close: %v", err)
	}
}
