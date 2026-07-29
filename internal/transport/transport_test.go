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
	"sync"
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

// freePort returns an available TCP port. There is an inherent TOCTOU
// race between closing this listener and the server binding the port;
// parallel tests could steal it. In practice, the OS rarely reassigns
// ports this fast, and test retries cover the edge case.
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

func TestListenAndServeUnsupportedTransport(t *testing.T) {
	srv := newTestMCPServer()
	cfg := &config.ServerConfig{
		Transport: "websocket",
		Host:      "localhost",
		Port:      8080,
	}

	err := ListenAndServe(context.Background(), srv, cfg, slog.Default(), nil, nil)
	if err == nil {
		t.Fatal("expected error for unsupported transport")
	}
	if !strings.Contains(err.Error(), "unsupported transport") {
		t.Errorf("error should mention unsupported transport, got: %v", err)
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

func TestListenAndServeHTTPConcurrentSessions(t *testing.T) {
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
		ListenAndServe(ctx, srv, cfg, slog.Default(), nil, nil) //nolint:errcheck,gosec
	}()

	addr := fmt.Sprintf("http://localhost:%d", port)
	waitForHealthy(t, addr)

	const numClients = 5
	var wg sync.WaitGroup
	errors := make(chan error, numClients)

	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()

			// Initialize
			initReq := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test-client","version":"1.0"}}}`
			resp, err := http.Post(addr+"/mcp", "application/json", strings.NewReader(initReq)) //nolint:noctx // test
			if err != nil {
				errors <- fmt.Errorf("client %d init: %w", clientID, err)
				return
			}
			sessionID := resp.Header.Get("Mcp-Session-Id")
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errors <- fmt.Errorf("client %d init status: %d", clientID, resp.StatusCode)
				return
			}

			// List tools (carry session ID from initialize)
			listReq := `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`
			req, _ := http.NewRequest(http.MethodPost, addr+"/mcp", strings.NewReader(listReq)) //nolint:noctx // test
			req.Header.Set("Content-Type", "application/json")
			if sessionID != "" {
				req.Header.Set("Mcp-Session-Id", sessionID)
			}
			resp, err = http.DefaultClient.Do(req)
			if err != nil {
				errors <- fmt.Errorf("client %d list: %w", clientID, err)
				return
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errors <- fmt.Errorf("client %d list status: %d", clientID, resp.StatusCode)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Error(err)
	}
}

func TestListenAndServeHTTPDrainBeforeShutdown(t *testing.T) {
	// Register a tool that takes 500ms to respond.
	srv := mcpserver.NewMCPServer("test-server", "1.0.0",
		mcpserver.WithToolCapabilities(true),
	)
	srv.AddTool(mcp.Tool{
		Name:        "slow",
		Description: "slow tool",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]any{},
		},
	}, func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		time.Sleep(500 * time.Millisecond)
		return mcp.NewToolResultText("completed"), nil
	})

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

	// Initialize a session
	initReq := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
	initResp, err := http.Post(addr+"/mcp", "application/json", strings.NewReader(initReq)) //nolint:noctx // test
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	sessionID := initResp.Header.Get("Mcp-Session-Id")
	_ = initResp.Body.Close()

	// Start a slow tool call in a goroutine
	toolDone := make(chan struct{})
	var toolErr error
	var toolStatus int
	go func() {
		defer close(toolDone)
		callReq := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"slow","arguments":{}}}`
		req, _ := http.NewRequest("POST", addr+"/mcp", strings.NewReader(callReq)) //nolint:noctx // test
		req.Header.Set("Content-Type", "application/json")
		if sessionID != "" {
			req.Header.Set("Mcp-Session-Id", sessionID)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			toolErr = err
			return
		}
		toolStatus = resp.StatusCode
		_ = resp.Body.Close()
	}()

	// Give the tool call time to start, then cancel
	time.Sleep(100 * time.Millisecond)
	cancel()

	// The tool call should complete despite shutdown
	select {
	case <-toolDone:
		if toolErr != nil {
			t.Fatalf("tool call failed during drain: %v", toolErr)
		}
		if toolStatus != http.StatusOK {
			t.Errorf("tool call status = %d during drain, want 200", toolStatus)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("tool call did not complete during drain")
	}

	// Server should have shut down cleanly
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("ListenAndServe error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ListenAndServe did not return")
	}
}

// TestRWMutexReloadPattern validates the locking pattern used by
// Handle.reloadMu: read locks (tool calls) run concurrently, but a
// write lock (reload) blocks until all reads complete. This tests the
// pattern, not the Handle integration — a Handle-level test requires
// a running plugin process. See dispatch.go:126 for the RLock site.
func TestRWMutexReloadPattern(t *testing.T) {
	var mu sync.RWMutex
	var order []string
	var orderMu sync.Mutex

	record := func(event string) {
		orderMu.Lock()
		order = append(order, event)
		orderMu.Unlock()
	}

	callStarted := make(chan struct{})
	callDone := make(chan struct{})

	// Simulate in-flight tool call
	go func() {
		mu.RLock()
		record("call-start")
		close(callStarted) // signal that we hold the read lock
		time.Sleep(100 * time.Millisecond)
		record("call-end")
		mu.RUnlock()
		close(callDone)
	}()

	<-callStarted // wait for read lock to be held

	// Reload blocks until call completes
	mu.Lock()
	record("reload")
	mu.Unlock()

	<-callDone

	orderMu.Lock()
	defer orderMu.Unlock()

	if len(order) != 3 {
		t.Fatalf("expected 3 events, got %v", order)
	}
	if order[0] != "call-start" || order[1] != "call-end" || order[2] != "reload" {
		t.Errorf("expected [call-start, call-end, reload], got %v", order)
	}
}
