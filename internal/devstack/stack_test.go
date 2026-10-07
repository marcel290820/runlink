package devstack

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"runlink/internal/buildinfo"
)

var ephemeral = Config{Frontend: "127.0.0.1:0", App: "127.0.0.1:0", TURN: "127.0.0.1:0"}

// startStack runs a stack on free loopback ports in a temporary state directory
// and stops it when the test ends.
func startStack(t *testing.T) *Stack {
	t.Helper()
	c := ephemeral
	c.StateDir = t.TempDir()
	stack, err := Start(context.Background(), c, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stack.Stop(); err != nil {
			t.Error(err)
		}
	})
	return stack
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(body)
}

func TestStackRunsProductionRoles(t *testing.T) {
	stack := startStack(t)
	// A serves the loader and relays B's status, exactly as in production.
	if status, body := get(t, stack.Frontend+"/api/v1/status"); status != http.StatusOK || body != "{\"status\":\"scaffold\"}\n" {
		t.Fatalf("relayed status = %d %q", status, body)
	}
	if status, body := get(t, stack.Frontend+"/12345678-1234-1234-1234-123456789abc"); status != http.StatusOK || !strings.Contains(body, "<title>Runlink</title>") {
		t.Fatalf("task page = %d", status)
	}
	if status, _ := get(t, stack.App+"/readyz"); status != http.StatusOK {
		t.Fatalf("app readiness = %d", status)
	}
	want := []string{"stun:" + stack.TURN, "turn:" + stack.TURN + "?transport=udp", "turn:" + stack.TURN + "?transport=tcp"}
	if strings.Join(stack.TURNURIs, " ") != strings.Join(want, " ") || !secretFormat.MatchString(stack.TURNSecret) {
		t.Fatalf("TURN URIs %q or secret format", stack.TURNURIs)
	}

	frontend := stack.Frontend
	if err := stack.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := http.Get(frontend + "/healthz"); err == nil {
		t.Fatal("frontend still serving after Stop")
	}
}

func TestStackKeepsStateAndSecret(t *testing.T) {
	c := ephemeral
	c.StateDir = filepath.Join(t.TempDir(), "dev")
	var secrets []string
	for range 2 {
		stack, err := Start(context.Background(), c, slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatal(err)
		}
		secrets = append(secrets, stack.TURNSecret)
		if err := stack.Stop(); err != nil {
			t.Fatal(err)
		}
	}
	if secrets[0] != secrets[1] {
		t.Fatal("the TURN secret changed across restarts")
	}
	for name, mode := range map[string]os.FileMode{"": 0o700, "app": 0o700, "turn-secret": 0o600} {
		info, err := os.Stat(filepath.Join(c.StateDir, name))
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("%q: %v %v, want %v", name, info.Mode(), err, mode)
		}
	}
}

func TestStackStaysOnLoopback(t *testing.T) {
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Frontend = "0.0.0.0:8080" },
		func(c *Config) { c.App = "192.168.1.2:8081" },
		func(c *Config) { c.TURN = "[::]:3478" },
		func(c *Config) { c.TURN = "[::1]:3478" },
		func(c *Config) { c.TURN = "localhost:3478" },
		func(c *Config) { c.App = "[fe80::1%lo]:8081" },
		func(c *Config) { c.StateDir = "" },
	} {
		c := DefaultConfig()
		mutate(&c)
		if c.Validate() == nil {
			t.Errorf("accepted %+v", c)
		}
		if _, err := Start(context.Background(), c, slog.New(slog.DiscardHandler)); err == nil {
			t.Errorf("started %+v", c)
		}
	}
	ipv6 := DefaultConfig()
	ipv6.Frontend, ipv6.App = "[::1]:8080", "[::1]:8081"
	for _, c := range []Config{DefaultConfig(), ipv6} {
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	// Development builds of the owner CLI find the default stack without configuration.
	if "http://"+DefaultConfig().Frontend != buildinfo.Service {
		t.Fatalf("buildinfo.Service %q does not target the default stack", buildinfo.Service)
	}
}

func TestStackReportsStartupFailures(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	c := ephemeral
	c.StateDir = t.TempDir()
	c.App = busy.Addr().String()
	if _, err := Start(context.Background(), c, slog.New(slog.DiscardHandler)); err == nil || !strings.HasPrefix(err.Error(), "app: ") {
		t.Fatalf("port conflict: %v", err)
	}

	// The stack refuses a shared state directory before touching its contents.
	c = ephemeral
	c.StateDir = t.TempDir()
	if err := os.Chmod(c.StateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(context.Background(), c, slog.New(slog.DiscardHandler)); err == nil ||
		err.Error() != "state directory must be private (0700)" {
		t.Fatalf("shared state directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(c.StateDir, "turn-secret")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("secret written into a shared directory: %v", err)
	}

	// B refuses a shared state directory of its own; the whole stack stops with its reason.
	c = ephemeral
	c.StateDir = t.TempDir()
	shared := filepath.Join(c.StateDir, "app")
	if err := errors.Join(os.Mkdir(shared, 0o700), os.Chmod(shared, 0o755)); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	started := time.Now()
	_, err = Start(context.Background(), c, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err == nil || !strings.Contains(err.Error(), "app: state directory must be private") {
		t.Fatalf("shared state: %v", err)
	}
	// The failure stops the stack at once rather than at the readiness deadline.
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("startup failure took %v", elapsed)
	}
	if !strings.Contains(logs.String(), `"msg":"server_stopped","service":"frontend"`) {
		t.Fatalf("frontend kept running after the app failed:\n%s", logs.String())
	}
}

func TestStackStopsWithContext(t *testing.T) {
	c := ephemeral
	c.StateDir = t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	stack, err := Start(ctx, c, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := stack.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestStackStopsWhenTURNFails(t *testing.T) {
	c := ephemeral
	c.StateDir = t.TempDir()
	stack, err := Start(context.Background(), c, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	// Closing Pion's sockets behind the stand-in ends its loops, as a socket
	// failure would. The stack's own shutdown may close them first.
	_ = stack.turn.pion.Close()
	if err := stack.Wait(); err == nil || !strings.HasPrefix(err.Error(), "turn: ") {
		t.Fatalf("stack ended with %v", err)
	}
	if _, err := http.Get(stack.Frontend + "/healthz"); err == nil {
		t.Fatal("frontend still serving after C failed")
	}
}
