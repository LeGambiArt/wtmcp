package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/LeGambiArt/wtmcp/internal/config"
)

func newTestMCPServer() *mcpserver.MCPServer {
	srv := mcpserver.NewMCPServer("test-server", "1.0.0",
		mcpserver.WithToolCapabilities(true),
	)
	srv.AddTool(mcp.Tool{
		Name:        "echo",
		Description: "echoes input",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]any{},
		},
	}, func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	})
	return srv
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

func waitForHealthy(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(addr + "/healthz") //nolint:noctx // test helper
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server did not become healthy within 3s")
}

func TestListenAndServeStdioReturnsOnCancel(t *testing.T) {
	srv := newTestMCPServer()
	cfg := &config.ServerConfig{
		Transport: config.TransportStdio,
		Host:      "localhost",
		Port:      8080,
	}

	ctx, cancel := context.WithCancel(context.Background())

	pr, pw := io.Pipe()

	errCh := make(chan error, 1)
	go func() {
		errCh <- ListenAndServe(ctx, srv, cfg, slog.Default(), pr, pw)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()
	_ = pw.Close()
	_ = pr.Close()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("expected nil error on cancel, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe did not return after cancel")
	}
}

func TestListenAndServeHTTPStartAndHealth(t *testing.T) {
	srv := newTestMCPServer()
	port := freePort(t)

	cfg := &config.ServerConfig{
		Transport: config.TransportStreamableHTTP,
		Host:      "localhost",
		Port:      port,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- ListenAndServe(ctx, srv, cfg, slog.Default(), nil, nil)
	}()

	addr := fmt.Sprintf("http://localhost:%d", port)
	waitForHealthy(t, addr)

	resp, err := http.Get(addr + "/healthz") //nolint:noctx // test
	if err != nil {
		t.Fatalf("healthz request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz status = %d, want 200", resp.StatusCode)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode healthz body: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("healthz status = %q, want ok", body["status"])
	}

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("expected nil error on shutdown, got: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ListenAndServe did not return after cancel")
	}
}

func TestListenAndServeHTTPPortInUse(t *testing.T) {
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	port := listener.Addr().(*net.TCPAddr).Port

	srv := newTestMCPServer()
	cfg := &config.ServerConfig{
		Transport: config.TransportStreamableHTTP,
		Host:      "localhost",
		Port:      port,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = ListenAndServe(ctx, srv, cfg, slog.Default(), nil, nil)
	if err == nil {
		t.Fatal("expected error for port in use")
	}
}

func TestListenAndServeHTTPMCPEndpoint(t *testing.T) {
	srv := newTestMCPServer()
	port := freePort(t)

	cfg := &config.ServerConfig{
		Transport: config.TransportStreamableHTTP,
		Host:      "localhost",
		Port:      port,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = ListenAndServe(ctx, srv, cfg, slog.Default(), nil, nil)
	}()

	addr := fmt.Sprintf("http://localhost:%d", port)
	waitForHealthy(t, addr)

	initReq := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
	resp, err := http.Post(addr+"/mcp", "application/json", strings.NewReader(initReq)) //nolint:noctx // test
	if err != nil {
		t.Fatalf("MCP request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("MCP status = %d, body = %s", resp.StatusCode, body)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode MCP response: %v", err)
	}
	if result["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc = %v, want 2.0", result["jsonrpc"])
	}
}

func TestListenURL(t *testing.T) {
	if got := ListenURL(&config.ServerConfig{Transport: config.TransportStdio}); got != "stdio" {
		t.Errorf("ListenURL(stdio) = %q, want stdio", got)
	}
	if got := ListenURL(&config.ServerConfig{Transport: config.TransportStreamableHTTP, Host: "localhost", Port: 8080}); got != "http://localhost:8080/mcp" {
		t.Errorf("ListenURL(http) = %q, want http://localhost:8080/mcp", got)
	}
}
