package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func frontendConfig(upstream string) Config {
	c := DefaultConfig()
	c.Role, c.Upstream = RoleFrontend, upstream
	return c
}

func TestValidateAcceptsSupportedConfigs(t *testing.T) {
	tests := map[string]func(*Config){
		"development app":   func(*Config) {},
		"test port zero":    func(c *Config) { c.Listen = "127.0.0.1:0" },
		"private app on B":  func(c *Config) { c.Listen = "10.77.0.20:8081" },
		"loopback IPv6":     func(c *Config) { c.Listen = "[::1]:8081" },
		"frontend":          func(c *Config) { *c = frontendConfig("http://127.0.0.1:8081") },
		"IPv6 upstream":     func(c *Config) { *c = frontendConfig("http://[::1]:8081") },
		"public TLS on A":   func(c *Config) { *c = publicFrontend() },
		"public IPv4 TLS":   func(c *Config) { *c = publicFrontend(); c.Listen = "0.0.0.0:443" },
		"global unicast A":  func(c *Config) { *c = publicFrontend(); c.Listen = "198.51.100.10:443" },
		"loopback with TLS": func(c *Config) { *c = publicFrontend(); c.Listen = "127.0.0.1:8443" },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			c := DefaultConfig()
			change(&c)
			if err := c.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func publicFrontend() Config {
	c := frontendConfig("http://10.77.0.20:8081")
	c.Listen, c.TLSCert, c.TLSKey = "[::]:443", "/etc/runlink/tls/fullchain.pem", "/etc/runlink/tls/privkey.pem"
	return c
}

func TestValidateRejectsUnsafeConfigs(t *testing.T) {
	tests := map[string]func(*Config){
		"unknown role":            func(c *Config) { c.Role = "secret-value" },
		"public plaintext":        func(c *Config) { c.Listen = "0.0.0.0:8081" },
		"public plaintext IPv6":   func(c *Config) { c.Listen = "[::]:8081" },
		"public plaintext A":      func(c *Config) { *c = frontendConfig("http://127.0.0.1:8081"); c.Listen = "0.0.0.0:443" },
		"hostname":                func(c *Config) { c.Listen = "localhost:8081" },
		"missing port":            func(c *Config) { c.Listen = "127.0.0.1" },
		"port out of range":       func(c *Config) { c.Listen = "127.0.0.1:65536" },
		"multicast":               func(c *Config) { c.Listen = "224.0.0.1:8081" },
		"link-local":              func(c *Config) { c.Listen = "[fe80::1%eth0]:8081" },
		"loopback zone":           func(c *Config) { c.Listen = "[::1%lo]:8081" },
		"missing state":           func(c *Config) { c.StateDir = "" },
		"app upstream":            func(c *Config) { c.Upstream = "http://127.0.0.1:8082" },
		"app TLS":                 func(c *Config) { c.TLSCert, c.TLSKey = "/tls/cert.pem", "/tls/key.pem" },
		"unpaired TLS":            func(c *Config) { *c = publicFrontend(); c.TLSKey = "" },
		"relative TLS key":        func(c *Config) { *c = publicFrontend(); c.TLSKey = "tls/privkey.pem" },
		"zero timeout":            func(c *Config) { c.ReadTimeout = 0 },
		"long timeout":            func(c *Config) { c.ShutdownTimeout = 2 * time.Minute },
		"missing upstream":        func(c *Config) { *c = frontendConfig("") },
		"credential upstream":     func(c *Config) { *c = frontendConfig("http://secret@127.0.0.1:8081") },
		"public upstream":         func(c *Config) { *c = frontendConfig("http://8.8.8.8:8081") },
		"HTTPS upstream":          func(c *Config) { *c = frontendConfig("https://127.0.0.1:8081") },
		"upstream query":          func(c *Config) { *c = frontendConfig("http://127.0.0.1:8081?secret") },
		"upstream empty query":    func(c *Config) { *c = frontendConfig("http://127.0.0.1:8081?") },
		"upstream fragment":       func(c *Config) { *c = frontendConfig("http://127.0.0.1:8081#") },
		"upstream path":           func(c *Config) { *c = frontendConfig("http://127.0.0.1:8081/") },
		"upstream hostname":       func(c *Config) { *c = frontendConfig("http://localhost:8081") },
		"upstream without port":   func(c *Config) { *c = frontendConfig("http://127.0.0.1") },
		"upstream port zero":      func(c *Config) { *c = frontendConfig("http://127.0.0.1:0") },
		"upstream scheme casing":  func(c *Config) { *c = frontendConfig("HTTP://127.0.0.1:8081") },
		"upstream zone":           func(c *Config) { *c = frontendConfig("http://[::1%lo]:8081") },
		"upstream zone smuggling": func(c *Config) { *c = frontendConfig("http://[::1%25lo]:9090/evil]:8081") },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			c := DefaultConfig()
			change(&c)
			err := c.Validate()
			if err == nil {
				t.Fatal("unsafe configuration accepted")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("error echoes a configured value: %v", err)
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	file := filepath.Join(t.TempDir(), "server.json")
	load := func(data string) (Config, error) {
		t.Helper()
		if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		return LoadConfig(file)
	}

	// The shape OpenTofu renders for A, over the defaults.
	c, err := load(`{"role":"frontend","listen":"[::]:443","upstream":"http://10.77.0.20:8081",` +
		`"tls_cert":"/etc/runlink/tls/fullchain.pem","tls_key":"/etc/runlink/tls/privkey.pem"}`)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "[::]:443" || c.ShutdownTimeout != DefaultConfig().ShutdownTimeout {
		t.Fatalf("file values not applied over defaults: %+v", c)
	}

	for name, data := range map[string]string{
		"unknown field":   `{"unknown":"secret-value"}`,
		"invalid value":   `{"listen":"0.0.0.0:80"}`,
		"wrong type":      `{"role":["secret-value"]}`,
		"two objects":     `{} {}`,
		"trailing data":   `{} secret-value`,
		"null":            `null`,
		"array":           `[]`,
		"empty":           ``,
		"over size limit": "{}" + strings.Repeat(" ", maxConfigBytes),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := load(data)
			if err == nil {
				t.Fatal("invalid configuration accepted")
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("error echoes file content: %v", err)
			}
		})
	}
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing configuration accepted")
	}
}
