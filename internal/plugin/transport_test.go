package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/LeGambiArt/wtmcp/internal/protocol"
)

// mockServiceHandler implements ServiceHandler for testing.
type mockServiceHandler struct {
	httpHandler   func(ctx context.Context, pluginName string, req protocol.Message) protocol.Message
	cacheHandler  func(ctx context.Context, pluginName string, req protocol.Message) protocol.Message
	fileIOHandler func(ctx context.Context, pluginName string, req protocol.Message) protocol.Message
}

var _ ServiceHandler = (*mockServiceHandler)(nil)

func (m *mockServiceHandler) HandleHTTP(ctx context.Context, pluginName string, req protocol.Message) protocol.Message {
	if m.httpHandler != nil {
		return m.httpHandler(ctx, pluginName, req)
	}
	return protocol.Message{ID: req.ID, Type: protocol.TypeHTTPResponse, Status: 200}
}

func (m *mockServiceHandler) HandleCache(ctx context.Context, pluginName string, req protocol.Message) protocol.Message {
	if m.cacheHandler != nil {
		return m.cacheHandler(ctx, pluginName, req)
	}
	hit := true
	return protocol.Message{ID: req.ID, Type: protocol.TypeCacheGet, Hit: &hit}
}

func (m *mockServiceHandler) HandleFileIO(ctx context.Context, pluginName string, req protocol.Message) protocol.Message {
	if m.fileIOHandler != nil {
		return m.fileIOHandler(ctx, pluginName, req)
	}
	return protocol.Message{ID: req.ID, Type: protocol.TypeFileWriteResponse, Path: "/mock/path"}
}

func TestTransportSend(t *testing.T) {
	var buf bytes.Buffer
	tr := NewTransport(&buf, strings.NewReader(""), strings.NewReader(""), 1024*1024)

	msg := protocol.Message{ID: "test-1", Type: protocol.TypeToolCall, Tool: "hello"}
	if err := tr.Send(msg); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	// Verify output is a single JSON line
	line := buf.String()
	if !strings.HasSuffix(line, "\n") {
		t.Error("message should end with newline")
	}
	line = strings.TrimSpace(line)

	var decoded protocol.Message
	if err := json.Unmarshal([]byte(line), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if decoded.ID != "test-1" {
		t.Errorf("ID = %q, want %q", decoded.ID, "test-1")
	}
	if decoded.Type != protocol.TypeToolCall {
		t.Errorf("Type = %q, want %q", decoded.Type, protocol.TypeToolCall)
	}
}

func TestTransportGenerateID(t *testing.T) {
	tr := NewTransport(io.Discard, strings.NewReader(""), strings.NewReader(""), 1024)

	id1 := tr.GenerateID("http")
	id2 := tr.GenerateID("http")
	id3 := tr.GenerateID("cache")

	if id1 == id2 {
		t.Error("IDs should be unique")
	}
	if !strings.HasPrefix(id3, "cache-") {
		t.Errorf("ID %q should have prefix 'cache-'", id3)
	}
}

func TestReadLoopRoutesToolResult(t *testing.T) {
	// Simulate plugin sending a tool_result
	toolResult := protocol.Message{ID: "req-1", Type: protocol.TypeToolResult, Result: json.RawMessage(`{"ok":true}`)}
	data, _ := json.Marshal(toolResult)

	pluginStdout := strings.NewReader(string(data) + "\n")
	var pluginStdin bytes.Buffer

	tr := NewTransport(&pluginStdin, pluginStdout, strings.NewReader(""), 1024*1024)

	ch := make(chan protocol.Message, 1)
	tr.pending.Store("req-1", ch)

	handler := &mockServiceHandler{}
	go tr.ReadLoop("test-plugin", 1, handler)

	select {
	case resp := <-ch:
		if resp.Type != protocol.TypeToolResult {
			t.Errorf("Type = %q, want %q", resp.Type, protocol.TypeToolResult)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for tool_result")
	}
}

func TestReadLoopHandlesHTTPSync(t *testing.T) {
	// Plugin sends an http_request, then a tool_result
	httpReq := protocol.Message{ID: "http-1", Type: protocol.TypeHTTPRequest, Method: "GET", Path: "/test"}
	toolResult := protocol.Message{ID: "req-1", Type: protocol.TypeToolResult, Result: json.RawMessage(`{}`)}

	var lines []string
	for _, msg := range []protocol.Message{httpReq, toolResult} {
		data, _ := json.Marshal(msg)
		lines = append(lines, string(data))
	}
	pluginStdout := strings.NewReader(strings.Join(lines, "\n") + "\n")

	var pluginStdin bytes.Buffer
	tr := NewTransport(&pluginStdin, pluginStdout, strings.NewReader(""), 1024*1024)

	httpCalled := false
	handler := &mockServiceHandler{
		httpHandler: func(_ context.Context, _ string, req protocol.Message) protocol.Message {
			httpCalled = true
			return protocol.Message{ID: req.ID, Type: protocol.TypeHTTPResponse, Status: 200}
		},
	}

	// Register pending for tool_result
	ch := make(chan protocol.Message, 1)
	tr.pending.Store("req-1", ch)

	go tr.ReadLoop("test-plugin", 1, handler)

	select {
	case <-ch:
		// got tool_result
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for tool_result")
	}

	if !httpCalled {
		t.Error("HTTP handler should have been called")
	}

	// Verify http_response was written back
	output := pluginStdin.String()
	if !strings.Contains(output, `"http_response"`) {
		t.Error("http_response should have been written to plugin stdin")
	}
}

func TestReadLoopDrainsPendingOnExit(t *testing.T) {
	// Empty stdout — ReadLoop will exit immediately
	tr := NewTransport(io.Discard, strings.NewReader(""), strings.NewReader(""), 1024)

	ch := make(chan protocol.Message, 1)
	tr.pending.Store("req-1", ch)

	handler := &mockServiceHandler{}
	go tr.ReadLoop("test-plugin", 1, handler)

	// Channel should be closed when ReadLoop exits
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("channel should be closed, not receive a message")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout — pending channel was not closed")
	}
}

func TestToolContextDefault(t *testing.T) {
	tr := NewTransport(io.Discard, strings.NewReader(""), strings.NewReader(""), 1024)

	ctx, found := tr.ToolContext("")
	if ctx == nil {
		t.Fatal("ToolContext should not return nil")
	}
	if found {
		t.Error("found should be false when no context is set")
	}
	if ctx.Err() != nil {
		t.Error("default context should not be cancelled")
	}
}

func TestToolContextSetAndClear(t *testing.T) {
	tr := NewTransport(io.Discard, strings.NewReader(""), strings.NewReader(""), 1024)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tr.SetToolContext("call-1", &ctx)
	got, found := tr.ToolContext("call-1")
	if !found {
		t.Error("found should be true after SetToolContext")
	}
	if got != ctx {
		t.Error("ToolContext should return the set context")
	}

	tr.ClearToolContext("call-1")
	got, found = tr.ToolContext("call-1")
	if found {
		t.Error("found should be false after ClearToolContext")
	}
	if got.Err() != nil {
		t.Error("after clearing, ToolContext should return a non-cancelled context")
	}
}

func TestToolContextBackwardCompat(t *testing.T) {
	tr := NewTransport(io.Discard, strings.NewReader(""), strings.NewReader(""), 1024)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tr.SetToolContext("call-1", &ctx)

	// Empty parent_id with exactly one active call: backward compat
	got, found := tr.ToolContext("")
	if !found {
		t.Error("single active call with empty parent_id should return found=true")
	}
	if got != ctx {
		t.Error("should return the single active context")
	}
}

func TestToolContextMultipleCallsEmptyParentID(t *testing.T) {
	tr := NewTransport(io.Discard, strings.NewReader(""), strings.NewReader(""), 1024)

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	tr.SetToolContext("call-1", &ctx1)
	tr.SetToolContext("call-2", &ctx2)

	// Empty parent_id with multiple active calls: ambiguous
	_, found := tr.ToolContext("")
	if found {
		t.Error("multiple active calls with empty parent_id should return found=false")
	}
}

func TestToolContextClearOneDoesNotAffectOther(t *testing.T) {
	tr := NewTransport(io.Discard, strings.NewReader(""), strings.NewReader(""), 1024)

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	tr.SetToolContext("call-1", &ctx1)
	tr.SetToolContext("call-2", &ctx2)

	tr.ClearToolContext("call-1")

	got, found := tr.ToolContext("call-2")
	if !found {
		t.Error("clearing call-1 should not affect call-2")
	}
	if got != ctx2 {
		t.Error("call-2 context should still be accessible")
	}
}

func TestReadLoopPassesContextToHTTPHandler(t *testing.T) {
	// Plugin sends an http_request. Verify that ReadLoop passes the
	// tool context to HandleHTTP and that cancelling it unblocks the
	// handler.
	httpReq := protocol.Message{ID: "http-1", Type: protocol.TypeHTTPRequest, Method: "GET", Path: "/slow"}
	data, _ := json.Marshal(httpReq)
	pluginStdout := strings.NewReader(string(data) + "\n")

	var pluginStdin bytes.Buffer
	tr := NewTransport(&pluginStdin, pluginStdout, strings.NewReader(""), 1024*1024)

	// Set a cancellable tool context
	toolCtx, toolCancel := context.WithCancel(context.Background())
	tr.SetToolContext("test-call", &toolCtx)

	handlerDone := make(chan struct{})
	var receivedCtx context.Context

	handler := &mockServiceHandler{
		httpHandler: func(ctx context.Context, _ string, req protocol.Message) protocol.Message {
			receivedCtx = ctx
			// Block until context is cancelled (simulates hung upstream)
			<-ctx.Done()
			close(handlerDone)
			return protocol.Message{ID: req.ID, Type: protocol.TypeHTTPResponse, Status: 0,
				Error: &protocol.Error{Code: "cancelled", Message: "cancelled"}}
		},
	}

	go tr.ReadLoop("test-plugin", 1, handler)

	// Give ReadLoop time to call HandleHTTP
	time.Sleep(50 * time.Millisecond)

	// Cancel the tool context — this should unblock HandleHTTP
	toolCancel()

	select {
	case <-handlerDone:
		// HandleHTTP unblocked
	case <-time.After(2 * time.Second):
		t.Fatal("HandleHTTP was not unblocked by context cancellation")
	}

	if receivedCtx == nil {
		t.Fatal("HandleHTTP should have received a context")
	}
	if receivedCtx.Err() == nil {
		t.Error("received context should be cancelled")
	}
}

func TestReadLoopRoutesContextByParentID(t *testing.T) {
	httpReq := protocol.Message{
		ID:       "http-1",
		ParentID: "call-A",
		Type:     protocol.TypeHTTPRequest,
		Method:   "GET",
		Path:     "/api/test",
	}
	data, _ := json.Marshal(httpReq)
	pluginStdout := strings.NewReader(string(data) + "\n")

	var pluginStdin bytes.Buffer
	tr := NewTransport(&pluginStdin, pluginStdout, strings.NewReader(""), 1024*1024)

	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()

	tr.SetToolContext("call-A", &ctxA)
	tr.SetToolContext("call-B", &ctxB)

	var receivedCtx context.Context
	handler := &mockServiceHandler{
		httpHandler: func(ctx context.Context, _ string, req protocol.Message) protocol.Message {
			receivedCtx = ctx
			return protocol.Message{ID: req.ID, Type: protocol.TypeHTTPResponse, Status: 200}
		},
	}

	tr.ReadLoop("test-plugin", 1, handler)

	if receivedCtx == nil {
		t.Fatal("HandleHTTP should have received a context")
	}
	if receivedCtx != ctxA {
		t.Error("HandleHTTP should receive ctxA (parent_id=call-A), not ctxB")
	}
}

func TestReadLoopOrphanedToolResult(t *testing.T) {
	// Send a tool_result with no matching pending entry.
	// Should not panic and should not log "unknown message type".
	toolResult := protocol.Message{ID: "orphan-1", Type: protocol.TypeToolResult, Result: json.RawMessage(`{}`)}
	data, err := json.Marshal(toolResult)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	pluginStdout := strings.NewReader(string(data) + "\n")

	tr := NewTransport(io.Discard, pluginStdout, strings.NewReader(""), 1024*1024)

	handler := &mockServiceHandler{}
	// ReadLoop should handle this gracefully (log orphan, not panic)
	tr.ReadLoop("test-plugin", 1, handler)
	// If we get here without panic, the test passes
}

func TestSendAndWaitReturnsResponse(t *testing.T) {
	// Verify SendAndWait returns a normal response with context.Background().
	resp := protocol.Message{ID: "req-1", Type: protocol.TypeToolResult, Result: json.RawMessage(`{"ok":true}`)}
	data, _ := json.Marshal(resp)

	pluginStdout := strings.NewReader(string(data) + "\n")
	var pluginStdin bytes.Buffer
	tr := NewTransport(&pluginStdin, pluginStdout, strings.NewReader(""), 1024*1024)

	go tr.ReadLoop("test", 1, &mockServiceHandler{})

	got, err := tr.SendAndWait(context.Background(), "req-1", protocol.Message{Type: protocol.TypeToolCall})
	if err != nil {
		t.Fatalf("SendAndWait returned error: %v", err)
	}
	if got.Type != protocol.TypeToolResult {
		t.Errorf("Type = %q, want %q", got.Type, protocol.TypeToolResult)
	}
}

func TestSendAndWaitTimeout(t *testing.T) {
	br := newBlockingReader()
	t.Cleanup(br.Close)

	tr := NewTransport(io.Discard, br, strings.NewReader(""), 1024*1024)
	go tr.ReadLoop("test", 1, &mockServiceHandler{})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := tr.SendAndWait(ctx, "req-1", protocol.Message{Type: protocol.TypeToolCall})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}

	if _, ok := tr.pending.Load("req-1"); ok {
		t.Error("pending entry should be deleted after SendAndWait returns")
	}
}

func TestSendAndWaitCancelled(t *testing.T) {
	br := newBlockingReader()
	t.Cleanup(br.Close)

	tr := NewTransport(io.Discard, br, strings.NewReader(""), 1024*1024)
	go tr.ReadLoop("test", 1, &mockServiceHandler{})

	ctx, cancel := context.WithCancel(context.Background())
	cancelled := make(chan struct{})
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
		close(cancelled)
	}()

	_, err := tr.SendAndWait(ctx, "req-1", protocol.Message{Type: protocol.TypeToolCall})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	<-cancelled
}

// blockingReader blocks until Close is called, preventing goroutine leaks in tests.
type blockingReader struct {
	done chan struct{}
}

func newBlockingReader() *blockingReader {
	return &blockingReader{done: make(chan struct{})}
}

func (b *blockingReader) Read([]byte) (int, error) {
	<-b.done
	return 0, io.EOF
}

func (b *blockingReader) Close() {
	select {
	case <-b.done:
	default:
		close(b.done)
	}
}

func TestError(t *testing.T) {
	err := &protocol.Error{Code: "api_error", Message: "not found"}
	expected := "[api_error] not found"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

// --- File I/O wiring tests ---

func TestReadLoopDispatchesFileWrite(t *testing.T) {
	fwReq := protocol.Message{ID: "fw-1", Type: protocol.TypeFileWrite, Path: "test.json", Content: "data"}
	toolResult := protocol.Message{ID: "req-1", Type: protocol.TypeToolResult, Result: json.RawMessage(`{}`)}

	var lines []string
	for _, msg := range []protocol.Message{fwReq, toolResult} {
		data, _ := json.Marshal(msg)
		lines = append(lines, string(data))
	}
	pluginStdout := strings.NewReader(strings.Join(lines, "\n") + "\n")

	var pluginStdin bytes.Buffer
	tr := NewTransport(&pluginStdin, pluginStdout, strings.NewReader(""), 1024*1024)

	fileIOCalled := false
	handler := &mockServiceHandler{
		fileIOHandler: func(_ context.Context, _ string, req protocol.Message) protocol.Message {
			fileIOCalled = true
			if req.Type != protocol.TypeFileWrite {
				t.Errorf("expected file_write, got %s", req.Type)
			}
			return protocol.Message{ID: req.ID, Type: protocol.TypeFileWriteResponse, Path: "/mock/path"}
		},
	}

	// Set tool context so the gate passes.
	ctx := context.Background()
	tr.SetToolContext("test-call", &ctx)

	ch := make(chan protocol.Message, 1)
	tr.pending.Store("req-1", ch)

	go tr.ReadLoop("test-plugin", 1, handler)

	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for tool_result")
	}

	if !fileIOCalled {
		t.Error("HandleFileIO should have been called for file_write")
	}
	if !strings.Contains(pluginStdin.String(), `"file_write_response"`) {
		t.Error("file_write_response should have been written to plugin stdin")
	}
}

func TestReadLoopDispatchesFileRead(t *testing.T) {
	frReq := protocol.Message{ID: "fr-1", Type: protocol.TypeFileRead, Path: "test.json"}
	toolResult := protocol.Message{ID: "req-1", Type: protocol.TypeToolResult, Result: json.RawMessage(`{}`)}

	var lines []string
	for _, msg := range []protocol.Message{frReq, toolResult} {
		data, _ := json.Marshal(msg)
		lines = append(lines, string(data))
	}
	pluginStdout := strings.NewReader(strings.Join(lines, "\n") + "\n")

	var pluginStdin bytes.Buffer
	tr := NewTransport(&pluginStdin, pluginStdout, strings.NewReader(""), 1024*1024)

	handler := &mockServiceHandler{
		fileIOHandler: func(_ context.Context, _ string, req protocol.Message) protocol.Message {
			if req.Type != protocol.TypeFileRead {
				t.Errorf("expected file_read, got %s", req.Type)
			}
			return protocol.Message{ID: req.ID, Type: protocol.TypeFileReadResponse, Content: "data"}
		},
	}

	ctx := context.Background()
	tr.SetToolContext("test-call", &ctx)

	ch := make(chan protocol.Message, 1)
	tr.pending.Store("req-1", ch)

	go tr.ReadLoop("test-plugin", 1, handler)

	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for tool_result")
	}

	if !strings.Contains(pluginStdin.String(), `"file_read_response"`) {
		t.Error("file_read_response should have been written to plugin stdin")
	}
}

func TestReadLoopRejectsFileIOWithoutToolContext(t *testing.T) {
	fwReq := protocol.Message{ID: "fw-1", Type: protocol.TypeFileWrite, Path: "test.json", Content: "data"}
	toolResult := protocol.Message{ID: "req-1", Type: protocol.TypeToolResult, Result: json.RawMessage(`{}`)}

	var lines []string
	for _, msg := range []protocol.Message{fwReq, toolResult} {
		data, _ := json.Marshal(msg)
		lines = append(lines, string(data))
	}
	pluginStdout := strings.NewReader(strings.Join(lines, "\n") + "\n")

	var pluginStdin bytes.Buffer
	tr := NewTransport(&pluginStdin, pluginStdout, strings.NewReader(""), 1024*1024)

	fileIOCalled := false
	handler := &mockServiceHandler{
		fileIOHandler: func(_ context.Context, _ string, _ protocol.Message) protocol.Message {
			fileIOCalled = true
			return protocol.Message{}
		},
	}

	// Do NOT set tool context — gate should reject.
	ch := make(chan protocol.Message, 1)
	tr.pending.Store("req-1", ch)

	go tr.ReadLoop("test-plugin", 1, handler)

	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for tool_result")
	}

	if fileIOCalled {
		t.Error("HandleFileIO should NOT have been called without tool context")
	}
	output := pluginStdin.String()
	if !strings.Contains(output, `"fileio_error"`) {
		t.Error("error response should contain fileio_error code")
	}
	if !strings.Contains(output, "not allowed outside tool calls") {
		t.Error("error should mention 'not allowed outside tool calls'")
	}
}

func TestFileIOErrorTypeMapping(t *testing.T) {
	// file_write request should get file_write_response error
	resp := fileIOError("fw-1", protocol.TypeFileWrite, "test error")
	if resp.Type != protocol.TypeFileWriteResponse {
		t.Errorf("file_write error type = %q, want %q", resp.Type, protocol.TypeFileWriteResponse)
	}

	// file_read request should get file_read_response error
	resp = fileIOError("fr-1", protocol.TypeFileRead, "test error")
	if resp.Type != protocol.TypeFileReadResponse {
		t.Errorf("file_read error type = %q, want %q", resp.Type, protocol.TypeFileReadResponse)
	}

	// Verify error fields
	if resp.Error == nil {
		t.Fatal("error should be set")
	}
	if resp.Error.Code != "fileio_error" {
		t.Errorf("error code = %q, want %q", resp.Error.Code, "fileio_error")
	}
}

func TestReadLoopRejectsCacheWritesWithoutToolContext(t *testing.T) {
	cacheSet := protocol.Message{ID: "cs-1", Type: protocol.TypeCacheSet, Key: "k", Value: json.RawMessage(`"v"`)}
	toolResult := protocol.Message{ID: "req-1", Type: protocol.TypeToolResult, Result: json.RawMessage(`{}`)}

	var lines []string
	for _, msg := range []protocol.Message{cacheSet, toolResult} {
		data, _ := json.Marshal(msg)
		lines = append(lines, string(data))
	}
	pluginStdout := strings.NewReader(strings.Join(lines, "\n") + "\n")

	var pluginStdin bytes.Buffer
	tr := NewTransport(&pluginStdin, pluginStdout, strings.NewReader(""), 1024*1024)

	cacheCalled := false
	handler := &mockServiceHandler{
		cacheHandler: func(_ context.Context, _ string, _ protocol.Message) protocol.Message {
			cacheCalled = true
			return protocol.Message{}
		},
	}

	ch := make(chan protocol.Message, 1)
	tr.pending.Store("req-1", ch)

	go tr.ReadLoop("test-plugin", 1, handler)

	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for tool_result")
	}

	if cacheCalled {
		t.Error("HandleCache should NOT have been called for cache_set without tool context")
	}
	output := pluginStdin.String()
	if !strings.Contains(output, `"cache_error"`) {
		t.Error("error response should contain cache_error code")
	}
	if !strings.Contains(output, "not allowed outside tool calls") {
		t.Error("error should mention 'not allowed outside tool calls'")
	}
}

func TestReadLoopAllowsCacheGetWithoutToolContext(t *testing.T) {
	cacheGet := protocol.Message{ID: "cg-1", Type: protocol.TypeCacheGet, Key: "k"}
	toolResult := protocol.Message{ID: "req-1", Type: protocol.TypeToolResult, Result: json.RawMessage(`{}`)}

	var lines []string
	for _, msg := range []protocol.Message{cacheGet, toolResult} {
		data, _ := json.Marshal(msg)
		lines = append(lines, string(data))
	}
	pluginStdout := strings.NewReader(strings.Join(lines, "\n") + "\n")

	var pluginStdin bytes.Buffer
	tr := NewTransport(&pluginStdin, pluginStdout, strings.NewReader(""), 1024*1024)

	cacheCalled := false
	handler := &mockServiceHandler{
		cacheHandler: func(_ context.Context, _ string, _ protocol.Message) protocol.Message {
			cacheCalled = true
			h := true
			return protocol.Message{ID: "cg-1", Type: protocol.TypeCacheGet, Hit: &h}
		},
	}

	ch := make(chan protocol.Message, 1)
	tr.pending.Store("req-1", ch)

	go tr.ReadLoop("test-plugin", 1, handler)

	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for tool_result")
	}

	if !cacheCalled {
		t.Error("HandleCache should have been called for cache_get without tool context")
	}
}

func TestReadLoopRejectsCacheDelFlushWithoutToolContext(t *testing.T) {
	for _, msgType := range []string{protocol.TypeCacheDel, protocol.TypeCacheFlush} {
		t.Run(msgType, func(t *testing.T) {
			cacheMsg := protocol.Message{ID: "cm-1", Type: msgType, Key: "k"}
			toolResult := protocol.Message{ID: "req-1", Type: protocol.TypeToolResult, Result: json.RawMessage(`{}`)}

			var lines []string
			for _, msg := range []protocol.Message{cacheMsg, toolResult} {
				data, _ := json.Marshal(msg)
				lines = append(lines, string(data))
			}
			pluginStdout := strings.NewReader(strings.Join(lines, "\n") + "\n")

			var pluginStdin bytes.Buffer
			tr := NewTransport(&pluginStdin, pluginStdout, strings.NewReader(""), 1024*1024)

			cacheCalled := false
			handler := &mockServiceHandler{
				cacheHandler: func(_ context.Context, _ string, _ protocol.Message) protocol.Message {
					cacheCalled = true
					return protocol.Message{}
				},
			}

			ch := make(chan protocol.Message, 1)
			tr.pending.Store("req-1", ch)

			go tr.ReadLoop("test-plugin", 1, handler)

			select {
			case <-ch:
			case <-time.After(2 * time.Second):
				t.Fatal("timeout waiting for tool_result")
			}

			if cacheCalled {
				t.Errorf("HandleCache should NOT have been called for %s without tool context", msgType)
			}
		})
	}
}

func TestReadLoopRejectsCacheWriteInDryRunContext(t *testing.T) {
	cacheSet := protocol.Message{ID: "cs-dr-1", Type: protocol.TypeCacheSet, Key: "k", Value: json.RawMessage(`"v"`), ParentID: "call-dr"}
	toolResult := protocol.Message{ID: "req-1", Type: protocol.TypeToolResult, Result: json.RawMessage(`{}`)}

	var lines []string
	for _, msg := range []protocol.Message{cacheSet, toolResult} {
		data, _ := json.Marshal(msg)
		lines = append(lines, string(data))
	}
	pluginStdout := strings.NewReader(strings.Join(lines, "\n") + "\n")

	var pluginStdin bytes.Buffer
	tr := NewTransport(&pluginStdin, pluginStdout, strings.NewReader(""), 1024*1024)

	dryRunCtx := proxy.WithDryRun(proxy.WithToolAccess(context.Background(), "read"), true)
	tr.SetToolContext("call-dr", &dryRunCtx)

	cacheCalled := false
	handler := &mockServiceHandler{
		cacheHandler: func(_ context.Context, _ string, _ protocol.Message) protocol.Message {
			cacheCalled = true
			return protocol.Message{}
		},
	}

	ch := make(chan protocol.Message, 1)
	tr.pending.Store("req-1", ch)

	go tr.ReadLoop("test-plugin", 1, handler)

	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for tool_result")
	}

	if cacheCalled {
		t.Error("HandleCache should NOT be called for cache_set in dry-run context")
	}
	output := pluginStdin.String()
	if !strings.Contains(output, "dry-run") {
		t.Errorf("error should mention dry-run context, got: %s", output)
	}
}
