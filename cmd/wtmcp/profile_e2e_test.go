package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
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

	"github.com/LeGambiArt/wtmcp/internal/config"
	"github.com/LeGambiArt/wtmcp/internal/plugin"
	"github.com/LeGambiArt/wtmcp/internal/profile"
	"github.com/LeGambiArt/wtmcp/internal/server"
	"github.com/LeGambiArt/wtmcp/internal/transport"
)

// TestProfileFilterOverMTLS is the end-to-end case that ties the transport and
// server tests together: a client presents a verified certificate, the
// production httpProfileContextFunc resolves it to a profile, and the resulting
// filter is applied to real MCP calls over the streamable-http transport.
//
//   - CN "code-review-bot" -> profile "cr" (allow only alpha_get_.*)
//   - any other CN         -> no rule, no default -> deny-all (fail closed)
//
// It drives /mcp with raw JSON-RPC over HTTP (carrying Mcp-Session-Id), the
// same way the transport tests do, rather than pulling in an MCP client.
func TestProfileFilterOverMTLS(t *testing.T) {
	resolver, err := profile.NewResolver("", &config.ProfileLoadResult{
		Definitions: map[string]config.ProfileDefinition{
			"cr": {Allow: map[string][]string{"alpha": {"alpha_get_.*"}}},
		},
		Rules: []config.ProfileRule{
			{Match: config.ProfileMatch{CN: "code-review-bot"}, Profile: "cr", File: "f.yaml"},
		},
	})
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}

	addr, ca := startProfileServer(t, resolver)

	// Matched agent: sees only its allowed tool, and denied calls are blocked.
	t.Run("matched profile filters tools", func(t *testing.T) {
		c := dialMCP(t, addr, ca, "code-review-bot")

		names := c.listToolNames(t)
		if !names["alpha_get_data"] {
			t.Errorf("alpha_get_data should be visible to the cr profile, got %v", sortedKeys(names))
		}
		if names["alpha_delete_data"] {
			t.Errorf("alpha_delete_data must be hidden by the cr profile")
		}
		if names["beta_run"] {
			t.Errorf("beta_run must be hidden by the cr profile")
		}
		if !names["plugin_list"] {
			t.Errorf("plugin_list (exempt) should remain visible")
		}

		// A denied tool call is rejected with a JSON-RPC error.
		if rr := c.callTool(t, "alpha_delete_data"); rr.Error == nil {
			t.Errorf("calling a denied tool should return a JSON-RPC error, got result %s", rr.Result)
		}

		// An exempt tool call round-trips successfully over mTLS.
		if rr := c.callTool(t, "plugin_list"); rr.Error != nil {
			t.Errorf("calling an exempt tool should succeed, got error %q", rr.Error.Message)
		}
	})

	// Unmatched agent: no rule, no default -> deny-all, only exempt tools.
	t.Run("unmatched cert fails closed", func(t *testing.T) {
		c := dialMCP(t, addr, ca, "stranger")

		names := c.listToolNames(t)
		if names["alpha_get_data"] || names["alpha_delete_data"] || names["beta_run"] {
			t.Errorf("unmatched cert must see no plugin tools, got %v", sortedKeys(names))
		}
		if !names["plugin_list"] {
			t.Errorf("exempt tools must remain visible even under deny-all")
		}
	})
}

// startProfileServer builds an MCP server with two plugins and serves it over
// mTLS using the production httpProfileContextFunc, so the certificate->profile
// resolution under test is exactly the one main() wires up. It returns the base
// URL and the CA the client should trust and be signed by.
func startProfileServer(t *testing.T, resolver *profile.Resolver) (addr string, ca *certPair) {
	t.Helper()

	dir := t.TempDir()
	ca = genCert(t, "test-ca", true, nil, nil)
	caFile := writePEM(t, dir, "ca.crt", ca.certPEM)

	srvCert := genCert(t, "localhost", false, ca, func(c *x509.Certificate) {
		c.IsCA = false
		c.DNSNames = []string{"localhost"}
		c.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	})
	certFile := writePEM(t, dir, "server.crt", srvCert.certPEM)
	keyFile := writePEM(t, dir, "server.key", srvCert.keyPEM)

	mgr := plugin.NewManagerForTest()
	mgr.SetManifest("alpha", &plugin.Manifest{
		Name: "alpha",
		Tools: []plugin.ToolDef{
			{Name: "alpha_get_data", Description: "Get", Access: "read", Visibility: "primary"},
			{Name: "alpha_delete_data", Description: "Delete", Access: "read", Visibility: "primary"},
		},
	})
	mgr.SetHandle("alpha")
	mgr.SetManifest("beta", &plugin.Manifest{
		Name: "beta",
		Tools: []plugin.ToolDef{
			{Name: "beta_run", Description: "Run", Access: "read", Visibility: "primary"},
		},
	})
	mgr.SetHandle("beta")

	cfg := config.DefaultConfig()
	cfg.Tools.Discovery = "full"
	index := server.NewToolIndex(mgr, false)
	srv, _ := server.New("test", mgr, cfg, index, nil, nil, nil, nil, true)

	port := freePort(t)
	srvCfg := &config.ServerConfig{
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

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = transport.ListenAndServe(ctx, srv, srvCfg, slog.Default(), nil, nil,
			transport.WithHTTPContextFunc(httpProfileContextFunc(resolver)))
	}()

	addr = fmt.Sprintf("https://localhost:%d", port)
	waitHealthy(t, addr, ca, "healthz-probe")
	return addr, ca
}

// mcpConn is a client bound to one presented certificate identity. Each request
// re-runs the server's context func, so the profile is resolved from the cert
// on every call.
type mcpConn struct {
	client  *http.Client
	addr    string
	session string
}

// dialMCP opens an mTLS connection presenting a client cert with the given CN
// and completes the MCP initialize handshake.
func dialMCP(t *testing.T, addr string, ca *certPair, cn string) *mcpConn {
	t.Helper()
	c := &mcpConn{client: mtlsClient(t, ca, cn), addr: addr}
	initReq := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
	_, session := c.post(t, initReq)
	c.session = session
	return c
}

// rpcResp is the subset of a JSON-RPC response the test inspects.
type rpcResp struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// post sends one JSON-RPC message and returns the parsed response plus any
// session ID the server assigned (only the initialize response carries one).
func (c *mcpConn) post(t *testing.T, body string) (rpcResp, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, c.addr+"/mcp", strings.NewReader(body)) //nolint:noctx // test
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.session != "" {
		req.Header.Set("Mcp-Session-Id", c.session)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		t.Fatalf("mcp request: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // test
	raw, _ := io.ReadAll(resp.Body)
	return parseRPC(t, resp.Header.Get("Content-Type"), raw), resp.Header.Get("Mcp-Session-Id")
}

func (c *mcpConn) listToolNames(t *testing.T) map[string]bool {
	t.Helper()
	rr, _ := c.post(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	if rr.Error != nil {
		t.Fatalf("tools/list error: %s", rr.Error.Message)
	}
	var parsed struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(rr.Result, &parsed); err != nil {
		t.Fatalf("unmarshal tools/list: %v", err)
	}
	names := make(map[string]bool, len(parsed.Tools))
	for _, tl := range parsed.Tools {
		names[tl.Name] = true
	}
	return names
}

func (c *mcpConn) callTool(t *testing.T, name string) rpcResp {
	t.Helper()
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":%q,"arguments":{}}}`, name)
	rr, _ := c.post(t, body)
	return rr
}

// parseRPC extracts the JSON-RPC message from either a plain application/json
// body or a text/event-stream frame ("event: message\ndata: {json}\n\n").
func parseRPC(t *testing.T, contentType string, raw []byte) rpcResp {
	t.Helper()
	payload := raw
	if strings.HasPrefix(contentType, "text/event-stream") {
		var data string
		for _, line := range strings.Split(string(raw), "\n") {
			if s, ok := strings.CutPrefix(line, "data: "); ok {
				data = s // keep the last data line
			}
		}
		payload = []byte(data)
	}
	var rr rpcResp
	if err := json.Unmarshal(payload, &rr); err != nil {
		t.Fatalf("parse rpc response: %v (content-type=%q body=%q)", err, contentType, string(raw))
	}
	return rr
}

// mtlsClient builds an HTTP client that trusts ca and presents a client cert
// with the given CN, signed by ca.
func mtlsClient(t *testing.T, ca *certPair, cn string) *http.Client {
	t.Helper()
	c := genCert(t, cn, false, ca, func(x *x509.Certificate) { x.IsCA = false })
	pair, err := tls.X509KeyPair(c.certPEM, c.keyPEM)
	if err != nil {
		t.Fatalf("client keypair (%s): %v", cn, err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.certPEM)
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{pair}},
		},
	}
}

// waitHealthy polls /healthz over mTLS until the server answers 200.
func waitHealthy(t *testing.T, addr string, ca *certPair, cn string) {
	t.Helper()
	client := mtlsClient(t, ca, cn)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(addr + "/healthz") //nolint:noctx // test helper
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server at %s did not become healthy within 5s", addr)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// --- certificate helpers (self-contained; the transport package's copies are
// unexported and live in its own test files) ---

type certPair struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
	keyPEM  []byte
}

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

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}
