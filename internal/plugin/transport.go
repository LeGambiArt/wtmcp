package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sync"
	"sync/atomic"

	"github.com/LeGambiArt/wtmcp/internal/protocol"
	"github.com/LeGambiArt/wtmcp/internal/proxy"
)

// Transport manages bidirectional JSON-lines communication with a
// plugin process over stdin/stdout.
type Transport struct {
	stdin   io.Writer
	stdout  io.Reader
	stderr  io.Reader
	mu      sync.Mutex // serialize writes to stdin
	pending sync.Map   // id -> chan protocol.Message
	maxSize int        // max message size in bytes
	nextID  atomic.Int64
	done    chan struct{} // closed when ReadLoop exits

	toolCtxMu sync.RWMutex
	toolCtxs  map[string]*context.Context // tool call ID -> context
}

// NewTransport creates a Transport for communicating with a plugin process.
func NewTransport(stdin io.Writer, stdout, stderr io.Reader, maxSize int) *Transport {
	return &Transport{
		stdin:    stdin,
		stdout:   stdout,
		stderr:   stderr,
		maxSize:  maxSize,
		done:     make(chan struct{}),
		toolCtxs: make(map[string]*context.Context),
	}
}

// GenerateID returns a unique message ID for service requests.
func (t *Transport) GenerateID(prefix string) string {
	n := t.nextID.Add(1)
	return fmt.Sprintf("%s-%d", prefix, n)
}

// Send writes a JSON message to the plugin's stdin.
// Thread-safe: serializes writes via mutex to guarantee atomic lines.
func (t *Transport) Send(msg protocol.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	data = append(data, '\n')

	t.mu.Lock()
	defer t.mu.Unlock()

	_, err = t.stdin.Write(data)
	return err
}

// SendAndWait sends a message and waits for a response with the same ID.
// The response is routed by ReadLoop. The context controls the maximum
// wait time — if the context is cancelled or its deadline expires,
// SendAndWait returns immediately with the context error.
func (t *Transport) SendAndWait(ctx context.Context, id string, msg protocol.Message) (protocol.Message, error) {
	ch := make(chan protocol.Message, 1)
	t.pending.Store(id, ch)
	defer t.pending.Delete(id)

	msg.ID = id
	if err := t.Send(msg); err != nil {
		return protocol.Message{}, fmt.Errorf("send: %w", err)
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return protocol.Message{}, fmt.Errorf("plugin exited while waiting for response to %s", id)
		}
		return resp, nil
	case <-ctx.Done():
		return protocol.Message{}, ctx.Err()
	case <-t.done:
		return protocol.Message{}, fmt.Errorf("transport closed while waiting for response to %s", id)
	}
}

// SetToolContext associates a tool call's context with its message ID.
// ReadLoop uses the parent_id field on service requests to look up the
// correct context for each in-flight call.
func (t *Transport) SetToolContext(id string, ctx *context.Context) {
	t.toolCtxMu.Lock()
	t.toolCtxs[id] = ctx
	t.toolCtxMu.Unlock()
}

// ClearToolContext removes a tool call's context after the call completes.
func (t *Transport) ClearToolContext(id string) {
	t.toolCtxMu.Lock()
	delete(t.toolCtxs, id)
	t.toolCtxMu.Unlock()
}

// ToolContext looks up the context for a service request by its parent_id.
// Returns (ctx, true) when found. When parentID is empty (old SDK):
// if exactly one call is active, returns that context (backward compat
// for concurrency=1). Otherwise returns (Background, false).
// The found flag preserves the nil-gate semantic: cache writes and
// file I/O reject when !found.
func (t *Transport) ToolContext(parentID string) (context.Context, bool) {
	t.toolCtxMu.RLock()
	defer t.toolCtxMu.RUnlock()

	if parentID != "" {
		if p, ok := t.toolCtxs[parentID]; ok {
			return *p, true
		}
		return context.Background(), false
	}

	// Backward compat: empty parent_id with exactly one active call.
	// Restricted to read-only access — write operations must use
	// explicit parent_id. Deprecated: plugins should propagate the
	// tool call ID.
	if len(t.toolCtxs) == 1 {
		for _, p := range t.toolCtxs {
			log.Printf("WARNING: service request with empty parent_id — using legacy single-call fallback (deprecated, read-only)")
			return proxy.WithToolAccess(*p, "read"), true
		}
	}
	return context.Background(), false
}

// ReadLoop reads messages from the plugin's stdout and routes them.
//
// For concurrency <= 1, service requests (http_request, cache_*) are
// handled synchronously — no goroutines, no race conditions. This
// guarantees that sequential plugins can use simple blocking read/write
// loops.
//
// For concurrency > 1, service requests are handled in goroutines.
//
// The handler functions are provided by the caller (proxy and cache).
func (t *Transport) ReadLoop(pluginName string, concurrency int, serviceHandler ServiceHandler) {
	defer close(t.done)

	scanner := bufio.NewScanner(t.stdout)
	scanner.Buffer(make([]byte, 0), t.maxSize)

	for scanner.Scan() {
		var msg protocol.Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			log.Printf("[%s] malformed message: %v", pluginName, err)
			continue
		}

		switch msg.Type {
		case protocol.TypeHTTPRequest:
			ctx, found := t.ToolContext(msg.ParentID)
			if !found {
				resp := protocol.Message{
					ID:    msg.ID,
					Type:  protocol.TypeHTTPResponse,
					Error: &protocol.Error{Code: "auth_error", Message: "HTTP requests not allowed outside tool calls"},
				}
				if err := t.Send(resp); err != nil {
					log.Printf("[%s] failed to send http error: %v", pluginName, err)
				}
				continue
			}
			if concurrency <= 1 {
				resp := serviceHandler.HandleHTTP(ctx, pluginName, msg)
				if err := t.Send(resp); err != nil {
					log.Printf("[%s] failed to send http_response: %v", pluginName, err)
				}
			} else {
				go func(c context.Context, m protocol.Message) {
					resp := serviceHandler.HandleHTTP(c, pluginName, m)
					if err := t.Send(resp); err != nil {
						log.Printf("[%s] failed to send http_response: %v", pluginName, err)
					}
				}(ctx, msg)
			}

		case protocol.TypeCacheGet, protocol.TypeCacheList:
			ctx, found := t.ToolContext(msg.ParentID)
			if !found {
				resp := transportCacheError(msg.ID, msg.Type, "cache reads not allowed outside tool calls")
				if err := t.Send(resp); err != nil {
					log.Printf("[%s] failed to send cache error: %v", pluginName, err)
				}
				continue
			}
			if concurrency <= 1 {
				resp := serviceHandler.HandleCache(ctx, pluginName, msg)
				if err := t.Send(resp); err != nil {
					log.Printf("[%s] failed to send cache response: %v", pluginName, err)
				}
			} else {
				go func(c context.Context, m protocol.Message) {
					resp := serviceHandler.HandleCache(c, pluginName, m)
					if err := t.Send(resp); err != nil {
						log.Printf("[%s] failed to send cache response: %v", pluginName, err)
					}
				}(ctx, msg)
			}

		case protocol.TypeCacheSet, protocol.TypeCacheDel, protocol.TypeCacheFlush:
			ctx, found := t.ToolContext(msg.ParentID)
			if !found {
				resp := transportCacheError(msg.ID, msg.Type, "cache writes not allowed outside tool calls")
				if err := t.Send(resp); err != nil {
					log.Printf("[%s] failed to send cache error: %v", pluginName, err)
				}
				continue
			}
			if proxy.IsDryRun(ctx) {
				resp := transportCacheError(msg.ID, msg.Type, "cache writes not allowed during dry-run")
				if err := t.Send(resp); err != nil {
					log.Printf("[%s] failed to send cache error: %v", pluginName, err)
				}
				continue
			}
			if concurrency <= 1 {
				resp := serviceHandler.HandleCache(ctx, pluginName, msg)
				if err := t.Send(resp); err != nil {
					log.Printf("[%s] failed to send cache response: %v", pluginName, err)
				}
			} else {
				go func(c context.Context, m protocol.Message) {
					resp := serviceHandler.HandleCache(c, pluginName, m)
					if err := t.Send(resp); err != nil {
						log.Printf("[%s] failed to send cache response: %v", pluginName, err)
					}
				}(ctx, msg)
			}

		case protocol.TypeFileWrite, protocol.TypeFileRead:
			ctx, found := t.ToolContext(msg.ParentID)
			if !found {
				resp := fileIOError(msg.ID, msg.Type, "file I/O not allowed outside tool calls")
				if err := t.Send(resp); err != nil {
					log.Printf("[%s] failed to send file_io error: %v", pluginName, err)
				}
				continue
			}
			switch {
			case concurrency <= 1:
				resp := serviceHandler.HandleFileIO(ctx, pluginName, msg)
				if err := t.Send(resp); err != nil {
					log.Printf("[%s] failed to send file_io response: %v", pluginName, err)
				}
			case msg.Type == protocol.TypeFileWrite:
				fioCtx := context.WithoutCancel(ctx)
				go func(c context.Context, m protocol.Message) {
					resp := serviceHandler.HandleFileIO(c, pluginName, m)
					if err := t.Send(resp); err != nil {
						log.Printf("[%s] failed to send file_io response: %v", pluginName, err)
					}
				}(fioCtx, msg)
			default:
				go func(c context.Context, m protocol.Message) {
					resp := serviceHandler.HandleFileIO(c, pluginName, m)
					if err := t.Send(resp); err != nil {
						log.Printf("[%s] failed to send file_io response: %v", pluginName, err)
					}
				}(ctx, msg)
			}

		case protocol.TypeToolResult, protocol.TypeInitOK, protocol.TypeInitError, protocol.TypeShutdownOK, protocol.TypeAuthResponse,
			protocol.TypeListResourcesOK, protocol.TypeReadResourceOK:
			if ch, ok := t.pending.LoadAndDelete(msg.ID); ok {
				ch.(chan protocol.Message) <- msg
			} else {
				log.Printf("[%s] orphaned %s (id: %s) — likely from a timed-out call",
					pluginName, msg.Type, msg.ID)
			}

		default:
			log.Printf("[%s] unknown message type: %q (id: %s)", pluginName, msg.Type, msg.ID)
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[%s] read error: %v", pluginName, err)
	}

	// Drain pending channels so blocked callers get immediate errors.
	t.pending.Range(func(key, value any) bool {
		close(value.(chan protocol.Message))
		t.pending.Delete(key)
		return true
	})

	// Clear context map to prevent leaks from in-flight tool calls.
	t.toolCtxMu.Lock()
	clear(t.toolCtxs)
	t.toolCtxMu.Unlock()
}

// ForwardStderr reads the plugin's stderr and logs it with a prefix.
func (t *Transport) ForwardStderr(pluginName string) {
	scanner := bufio.NewScanner(t.stderr)
	for scanner.Scan() {
		log.Printf("[%s] %s", pluginName, scanner.Text())
	}
}

// fileIOError returns an error response for file I/O operations,
// mapping request type to response type (file_write → file_write_response).
func fileIOError(id, reqType, msg string) protocol.Message {
	respType := protocol.TypeFileWriteResponse
	if reqType == protocol.TypeFileRead {
		respType = protocol.TypeFileReadResponse
	}
	return protocol.Message{
		ID:    id,
		Type:  respType,
		Error: &protocol.Error{Code: "fileio_error", Message: msg},
	}
}

// transportCacheError returns an error response for cache operations
// rejected at the transport layer (e.g., outside a tool call).
func transportCacheError(id, msgType, msg string) protocol.Message {
	return protocol.Message{
		ID:    id,
		Type:  msgType,
		Error: &protocol.Error{Code: "cache_error", Message: msg},
	}
}

// ServiceHandler handles service requests from plugins.
// Implemented by the proxy, cache, and file I/O subsystems.
type ServiceHandler interface {
	HandleHTTP(ctx context.Context, pluginName string, req protocol.Message) protocol.Message
	HandleCache(ctx context.Context, pluginName string, req protocol.Message) protocol.Message
	HandleFileIO(ctx context.Context, pluginName string, req protocol.Message) protocol.Message
}
