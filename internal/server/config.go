package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// One binary serves both roles: the app (B) listens privately, and the trusted
// frontend (A) serves the loader and forwards fixed routes to B.
const (
	RoleApp      = "app"
	RoleFrontend = "frontend"
)

const maxConfigBytes = 64 << 10

// Config is the operator configuration. Files and flags set the JSON fields; the
// HTTP bounds are fixed in production and shortened only by tests.
type Config struct {
	Role     string `json:"role"`
	Listen   string `json:"listen"`    // explicit IP:port
	StateDir string `json:"state_dir"` // app only
	Upstream string `json:"upstream"`  // frontend only: B's private origin, http://IP:port
	TLSCert  string `json:"tls_cert"`  // frontend only: absolute paths
	TLSKey   string `json:"tls_key"`

	ReadHeaderTimeout time.Duration `json:"-"`
	ReadTimeout       time.Duration `json:"-"`
	WriteTimeout      time.Duration `json:"-"`
	IdleTimeout       time.Duration `json:"-"`
	ShutdownTimeout   time.Duration `json:"-"`
}

// DefaultConfig is the local development app.
func DefaultConfig() Config {
	return Config{
		Role:              RoleApp,
		Listen:            "127.0.0.1:8081",
		StateDir:          "var/app",
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
		ShutdownTimeout:   10 * time.Second,
	}
}

// LoadConfig decodes one JSON object of known fields over the defaults and
// validates the result.
func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("cannot open configuration: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return Config{}, fmt.Errorf("cannot read configuration: %w", err)
	}
	if len(data) > maxConfigBytes {
		return Config{}, errors.New("configuration exceeds 64 KiB")
	}
	// Decoding null succeeds without changing anything, so insist on an object.
	if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		return Config{}, errors.New("configuration must be a JSON object")
	}
	c := DefaultConfig()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("invalid configuration JSON: %w", err)
	}
	if dec.Decode(new(any)) != io.EOF {
		return Config{}, errors.New("configuration must contain exactly one JSON object")
	}
	return c, c.Validate()
}

// Validate rejects configurations that could expose plain HTTP publicly, put
// anything but a bare origin into the upstream, or run without bounds. Messages
// never echo configured values.
func (c Config) Validate() error {
	if c.Role != RoleApp && c.Role != RoleFrontend {
		return errors.New("role must be app or frontend")
	}
	listen, err := parseAddrPort(c.Listen)
	if err != nil {
		return errors.New("listen must be an explicit IP:port")
	}
	ip := listen.Addr()
	if !ip.IsGlobalUnicast() && !ip.IsLoopback() && !ip.IsUnspecified() {
		return errors.New("listen must use a unicast or unspecified IP")
	}
	hasTLS := c.TLSCert != "" || c.TLSKey != ""
	switch {
	case (c.TLSCert == "") != (c.TLSKey == ""):
		return errors.New("TLS needs both a certificate and a private key path")
	case hasTLS && (c.Role != RoleFrontend || !filepath.IsAbs(c.TLSCert) || !filepath.IsAbs(c.TLSKey)):
		return errors.New("TLS requires the frontend role and absolute certificate/key paths")
	case !ip.IsLoopback() && !ip.IsPrivate() && !hasTLS:
		return errors.New("public listeners require the frontend role with TLS")
	}
	switch c.Role {
	case RoleApp:
		if c.StateDir == "" {
			return errors.New("state_dir is required for the app role")
		}
		if c.Upstream != "" {
			return errors.New("the app role cannot have an upstream")
		}
	case RoleFrontend:
		if !validUpstream(c.Upstream) {
			return errors.New("upstream must be exactly http://IP:port with a private or loopback IP")
		}
	}
	for _, d := range []time.Duration{c.ReadHeaderTimeout, c.ReadTimeout, c.WriteTimeout, c.IdleTimeout, c.ShutdownTimeout} {
		if d <= 0 || d > time.Minute {
			return errors.New("HTTP timeouts must be positive and at most one minute")
		}
	}
	return nil
}

// validUpstream accepts only a bare origin, so appending a route path cannot
// turn into a query, fragment, userinfo, or another port.
func validUpstream(upstream string) bool {
	hostPort, ok := strings.CutPrefix(upstream, "http://")
	addr, err := parseAddrPort(hostPort)
	return ok && err == nil && addr.Port() != 0 && (addr.Addr().IsPrivate() || addr.Addr().IsLoopback())
}

// parseAddrPort parses IP:port without an IPv6 zone. netip accepts any zone text,
// including URL syntax such as "]:9090/path".
func parseAddrPort(s string) (netip.AddrPort, error) {
	addr, err := netip.ParseAddrPort(s)
	if err == nil && addr.Addr().Zone() != "" {
		return netip.AddrPort{}, errors.New("IPv6 zones are not supported")
	}
	return addr, err
}
