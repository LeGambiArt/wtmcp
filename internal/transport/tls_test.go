package transport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LeGambiArt/wtmcp/internal/config"
)

// certPair holds a generated certificate and its PEM-encoded files.
type certPair struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
	keyPEM  []byte
}

// genCert creates a certificate, optionally signed by a parent (CA). If
// parent is nil, the certificate is self-signed (a CA).
func genCert(t *testing.T, cn string, isCA bool, parent *certPair, opts func(*x509.Certificate)) *certPair {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  isCA,
	}
	if opts != nil {
		opts(tmpl)
	}

	signer := tmpl
	signerKey := key
	if parent != nil {
		signer = parent.cert
		signerKey = parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return &certPair{cert: cert, key: key, certPEM: certPEM, keyPEM: keyPEM}
}

func writePEM(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

func TestBuildServerTLS(t *testing.T) {
	dir := t.TempDir()
	ca := genCert(t, "test-ca", true, nil, nil)
	caFile := writePEM(t, dir, "ca.crt", ca.certPEM)

	tests := []struct {
		name       string
		clientAuth string
		want       tls.ClientAuthType
	}{
		{"require", "require", tls.RequireAndVerifyClientCert},
		{"request", "request", tls.VerifyClientCertIfGiven},
		{"none", "none", tls.NoClientCert},
		{"empty", "", tls.NoClientCert},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.ServerTLSConfig{CAFile: caFile, ClientAuth: tt.clientAuth}
			tlsCfg, err := buildServerTLS(cfg)
			if err != nil {
				t.Fatalf("buildServerTLS: %v", err)
			}
			if tlsCfg.ClientAuth != tt.want {
				t.Errorf("ClientAuth = %v, want %v", tlsCfg.ClientAuth, tt.want)
			}
			if tlsCfg.ClientCAs == nil {
				t.Errorf("ClientCAs should be populated from CA file")
			}
		})
	}
}

func TestBuildServerTLSBadCA(t *testing.T) {
	dir := t.TempDir()
	bad := writePEM(t, dir, "bad.crt", []byte("not a certificate"))
	if _, err := buildServerTLS(&config.ServerTLSConfig{CAFile: bad}); err == nil {
		t.Fatalf("expected error for invalid CA file")
	}
	if _, err := buildServerTLS(&config.ServerTLSConfig{CAFile: filepath.Join(dir, "nope.crt")}); err == nil {
		t.Fatalf("expected error for missing CA file")
	}
}

// TestListenHTTPmTLS verifies that the streamable-http transport, under
// client_auth: require, accepts a client presenting a CA-signed cert
// (extracting its identity via the context func) and refuses a client
// with no certificate.
func TestListenHTTPmTLS(t *testing.T) {
	dir := t.TempDir()
	ca := genCert(t, "test-ca", true, nil, nil)
	caFile := writePEM(t, dir, "ca.crt", ca.certPEM)

	server := genCert(t, "localhost", false, ca, func(c *x509.Certificate) {
		c.IsCA = false
		c.DNSNames = []string{"localhost"}
		c.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	})
	certFile := writePEM(t, dir, "server.crt", server.certPEM)
	keyFile := writePEM(t, dir, "server.key", server.keyPEM)

	client := genCert(t, "code-review-bot", false, ca, func(c *x509.Certificate) {
		c.IsCA = false
		c.URIs = []*url.URL{{Scheme: "spiffe", Host: "example.com", Path: "/agent/cr"}}
	})

	port := freePort(t)
	cfg := &config.ServerConfig{
		Transport: config.TransportStreamableHTTP,
		Host:      "localhost",
		Port:      port,
		TLS: &config.ServerTLSConfig{
			CertFile:   certFile,
			KeyFile:    keyFile,
			CAFile:     caFile,
			ClientAuth: config.ClientAuthRequire,
		},
	}

	// Capture identities extracted by the context func.
	var mu sync.Mutex
	var seenCNs []string
	ctxFunc := func(ctx context.Context, r *http.Request) context.Context {
		mu.Lock()
		defer mu.Unlock()
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			seenCNs = append(seenCNs, r.TLS.PeerCertificates[0].Subject.CommonName)
		}
		return ctx
	}

	srv := newTestMCPServer()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = ListenAndServe(ctx, srv, cfg, slog.Default(), nil, nil, WithHTTPContextFunc(ctxFunc))
	}()

	addr := fmt.Sprintf("https://localhost:%d", port)

	// Build a client trust pool for the server cert.
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.certPEM)

	clientTLSCert, err := tls.X509KeyPair(client.certPEM, client.keyPEM)
	if err != nil {
		t.Fatalf("client keypair: %v", err)
	}

	// Client WITH certificate: should succeed.
	withCert := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:      pool,
				Certificates: []tls.Certificate{clientTLSCert},
			},
		},
	}
	waitForHealthy(t, withCert, addr)

	// POST to /mcp to trigger the mcp-go handler (and thus the context
	// func that extracts the client identity). /healthz is our own
	// handler and does not run the context func.
	initReq := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
	resp, err := withCert.Post(addr+"/mcp", "application/json", strings.NewReader(initReq)) //nolint:noctx // test
	if err != nil {
		t.Fatalf("mTLS request with client cert failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("mcp status = %d, want 200", resp.StatusCode)
	}

	mu.Lock()
	sawBot := false
	for _, cn := range seenCNs {
		if cn == "code-review-bot" {
			sawBot = true
		}
	}
	mu.Unlock()
	if !sawBot {
		t.Errorf("context func should have observed client CN 'code-review-bot', saw %v", seenCNs)
	}

	// Client WITHOUT certificate: handshake must be refused.
	noCert := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool},
		},
	}
	if resp, err := noCert.Get(addr + "/healthz"); err == nil { //nolint:noctx // test
		_ = resp.Body.Close()
		t.Errorf("request without client cert should be refused under client_auth=require")
	}
}
