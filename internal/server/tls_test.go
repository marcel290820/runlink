package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFrontendTLSHealthIngressAndProtocolBounds(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	c := DefaultConfig()
	c.Role = "frontend"
	c.Upstream = "http://127.0.0.1:1"
	c.TLSCert = filepath.Join(directory, "certificate.pem")
	c.TLSKey = filepath.Join(directory, "private.key")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(c.TLSCert, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.TLSKey, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, c, listener, slog.New(slog.NewJSONHandler(io.Discard, nil))) }()
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certPEM)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	origin := "https://" + listener.Addr().String()
	for _, path := range []string{"/healthz", "/readyz", "/"} {
		response, err := client.Get(origin + path)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != 200 || response.TLS == nil || response.TLS.Version < tls.VersionTLS12 || response.Header.Get("Strict-Transport-Security") == "" {
			t.Fatal("TLS health/loader/HSTS failed")
		}
	}
	response, err := client.Get(origin + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 502 || response.Header.Get("Content-Type") != "application/json" || !strings.Contains(response.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatal("TLS upstream error boundary failed")
	}
	obsolete := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11}
	connection, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", listener.Addr().String(), obsolete)
	if err == nil {
		connection.Close()
		t.Fatal("obsolete TLS protocol accepted")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestTLSConfigurationFailsClosed(t *testing.T) {
	c := DefaultConfig()
	c.Role = "frontend"
	c.Listen = "0.0.0.0:443"
	c.Upstream = "http://127.0.0.1:8081"
	if err := c.Validate(); err == nil {
		t.Fatal("public plaintext frontend accepted")
	}
	c.TLSCert = "/fixture/certificate.pem"
	if err := c.Validate(); err == nil {
		t.Fatal("unpaired TLS certificate accepted")
	}
	c.TLSKey = "relative/private.key"
	if err := c.Validate(); err == nil {
		t.Fatal("relative key path accepted")
	}
	c.TLSKey = "/fixture/private.key"
	if err := c.Validate(); err != nil {
		t.Fatal("public TLS config rejected", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := Run(context.Background(), c, listener, slog.New(slog.NewJSONHandler(io.Discard, nil))); err == nil {
		t.Fatal("missing TLS material did not fail closed")
	}
}
