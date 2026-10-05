package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type Config struct {
	Role              string        `json:"role"`
	Listen            string        `json:"listen"`
	StateDir          string        `json:"state_dir"`
	Upstream          string        `json:"upstream"`
	TLSCert           string        `json:"tls_cert"`
	TLSKey            string        `json:"tls_key"`
	ReadHeaderTimeout time.Duration `json:"-"`
	ReadTimeout       time.Duration `json:"-"`
	WriteTimeout      time.Duration `json:"-"`
	IdleTimeout       time.Duration `json:"-"`
	ShutdownTimeout   time.Duration `json:"-"`
}

func DefaultConfig() Config {
	return Config{Role: "app", Listen: "127.0.0.1:8081", StateDir: "var/app", ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, ShutdownTimeout: 10 * time.Second}
}

func LoadConfig(path string) (Config, error) {
	c := DefaultConfig()
	f, err := os.Open(path)
	if err != nil {
		return c, errors.New("cannot open configuration")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(data) > 65536 {
		return c, errors.New("configuration exceeds the read limit")
	}
	if len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
		return c, errors.New("configuration must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, errors.New("invalid configuration JSON")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return c, errors.New("configuration must contain one JSON object")
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	if c.Role != "app" && c.Role != "frontend" {
		return errors.New("role must be app or frontend")
	}
	host, port, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return errors.New("listen must be an explicit IP and port")
	}
	ip := net.ParseIP(host)
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 || ip == nil || !(ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsUnspecified()) {
		return errors.New("listen must use an explicit unicast IP and valid port")
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return errors.New("both TLS certificate and private key paths are required")
	}
	if c.TLSCert != "" && (c.Role != "frontend" || !filepath.IsAbs(c.TLSCert) || !filepath.IsAbs(c.TLSKey)) {
		return errors.New("TLS requires frontend role and absolute certificate/key paths")
	}
	if !ip.IsLoopback() && !ip.IsPrivate() && (c.Role != "frontend" || c.TLSCert == "") {
		return errors.New("public listeners require frontend role and TLS")
	}
	if c.Role == "app" && c.StateDir == "" {
		return errors.New("state_dir is required")
	}
	if c.Role == "app" && c.Upstream != "" {
		return errors.New("app role cannot have an upstream")
	}
	if c.Role == "frontend" {
		u, err := url.Parse(c.Upstream)
		if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
			return errors.New("upstream must be a private HTTP origin")
		}
		ip := net.ParseIP(u.Hostname())
		p, err := strconv.Atoi(u.Port())
		if ip == nil || (!ip.IsPrivate() && !ip.IsLoopback()) || err != nil || p < 1 || p > 65535 {
			return errors.New("upstream must use an explicit private IP and port")
		}
	}
	for _, d := range []time.Duration{c.ReadHeaderTimeout, c.ReadTimeout, c.WriteTimeout, c.IdleTimeout, c.ShutdownTimeout} {
		if d <= 0 || d > time.Minute {
			return errors.New("HTTP timeouts must be positive and at most one minute")
		}
	}
	return nil
}
