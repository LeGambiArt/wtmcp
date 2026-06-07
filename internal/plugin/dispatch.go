package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sync"
	"time"

	"github.com/LeGambiArt/wtmcp/internal/protocol"
	"github.com/LeGambiArt/wtmcp/internal/sandbox"
)

// CallToolResult holds the result of a tool call along with any actions.
type CallToolResult struct {
	Result  json.RawMessage
	Actions []protocol.Action
}

const (
	maxRestartsPerWindow = 3
	restartWindow        = time.Minute
)

var warnUnsandboxedOnce sync.Once

// Handle wraps a plugin process and serializes tool calls based on
// the plugin's concurrency setting.
type Handle struct {
	process        *Process
	manifest       *Manifest
	handler        ServiceHandler
	groupVars      map[string]string
	processCfg     ProcessConfig
	sbMgr          *sandbox.Manager
	sessionDir     string
	outputDir      string
	resolvedConfig json.RawMessage // snapshot from preparePlugin, avoids racing on manifest field
	mu             sync.Mutex      // serialize tool calls for concurrency:1
	resMu          sync.RWMutex    // protects resources
	restartMu      sync.Mutex      // protects restarts slice
	resources      []protocol.ResourceDef
	toolTimeout    time.Duration
	restarts       []time.Time // timestamps of recent auto-restarts
}

// NewHandle creates a Handle for dispatching tool calls to a plugin.
func NewHandle(manifest *Manifest, handler ServiceHandler, cfg ProcessConfig, toolTimeout time.Duration, groupVars map[string]string) *Handle {
	return &Handle{
		manifest:    manifest,
		handler:     handler,
		groupVars:   groupVars,
		processCfg:  cfg,
		toolTimeout: toolTimeout,
	}
}

// SetSandbox configures the sandbox manager for this handle's processes.
func (h *Handle) SetSandbox(sb *sandbox.Manager) {
	h.sbMgr = sb
}

// Start launches the plugin process.
func (h *Handle) Start(ctx context.Context) error {
	h.process = NewProcess(h.manifest, h.handler, h.processCfg, h.groupVars)
	h.process.sessionDir = h.sessionDir
	h.process.outputDir = h.outputDir
	h.process.resolvedConfig = h.resolvedConfig
	if h.sbMgr != nil {
		h.process.SetSandbox(h.sbMgr)
	}
	if err := h.process.Start(ctx); err != nil {
		return err
	}
	// Copy initial resources from process
	if h.manifest.ProvidesResources() {
		h.SetResources(h.process.Resources)
	}
	return nil
}

// InitDomains returns dynamic domains registered during plugin init.
func (h *Handle) InitDomains() []string {
	if h.process == nil {
		return nil
	}
	return h.process.Domains
}

// AuthBindings returns per-domain auth bindings registered during
// plugin init. Keys are domain names, values are env var names.
func (h *Handle) AuthBindings() map[string]string {
	if h.process == nil {
		return nil
	}
	return h.process.AuthBindings
}

// IsReady returns true if the handle can accept tool calls.
// Non-persistent (oneshot) plugins are always ready; persistent
// plugins are ready once Start has been called and succeeded.
func (h *Handle) IsReady() bool {
	if h.manifest.Execution != "persistent" {
		return true
	}
	return h.process != nil && h.process.State() == StateRunning
}

// Stop gracefully shuts down the plugin process.
func (h *Handle) Stop(ctx context.Context) error {
	if h.process == nil {
		return nil
	}
	return h.process.Stop(ctx)
}

// CallTool dispatches a tool call to the plugin.
// For persistent plugins, sends via the transport.
// For oneshot plugins, spawns a fresh process per call.
func (h *Handle) CallTool(ctx context.Context, toolName string, params json.RawMessage) (*CallToolResult, error) {
	if h.manifest.Concurrency <= 1 {
		h.mu.Lock()
		defer h.mu.Unlock()
	}

	// Auto-restart crashed persistent plugins (rate-limited).
	// restartAllowed/recordRestart access h.restarts under h.restartMu
	// (separate from h.mu to be safe for concurrency > 1 plugins).
	if h.manifest.Execution == "persistent" && h.process != nil && h.process.State() == StateFailed {
		if !h.restartAllowed() {
			return nil, &protocol.Error{
				Code:    "restart_suppressed",
				Message: fmt.Sprintf("%s crashed too many times (%d in %s), not restarting; use plugin_reload to retry", h.manifest.Name, maxRestartsPerWindow, restartWindow),
			}
		}
		log.Printf("[%s] auto-restarting crashed plugin", h.manifest.Name)
		h.recordRestart()
		if err := h.Start(ctx); err != nil {
			return nil, &protocol.Error{
				Code:    "restart_failed",
				Message: fmt.Sprintf("failed to restart %s: %v", h.manifest.Name, err),
			}
		}
	}

	if h.manifest.Execution == "oneshot" {
		return h.callOneshot(ctx, toolName, params)
	}
	return h.callPersistent(ctx, toolName, params)
}

func (h *Handle) callPersistent(ctx context.Context, toolName string, params json.RawMessage) (*CallToolResult, error) {
	ctx, cancel := context.WithTimeout(ctx, h.toolTimeout)
	defer cancel()

	transport := h.process.Transport

	// Set the tool call context so ReadLoop can pass it to service
	// handlers (HTTP, cache). When this call returns (timeout or
	// success), cancel() fires, cancelling any in-flight HTTP
	// request and unblocking ReadLoop.
	id := transport.GenerateID("req")

	transport.SetToolContext(id, &ctx)
	defer transport.ClearToolContext(id)

	ch := make(chan protocol.Message, 1)
	transport.pending.Store(id, ch)
	defer transport.pending.Delete(id)

	if err := transport.Send(protocol.Message{
		ID:     id,
		Type:   protocol.TypeToolCall,
		Tool:   toolName,
		Params: params,
		Config: h.resolvedConfig,
	}); err != nil {
		return nil, &protocol.Error{Code: "send_failed", Message: err.Error()}
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, &protocol.Error{
				Code:    "plugin_exited",
				Message: fmt.Sprintf("plugin exited while handling %s", toolName),
			}
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return &CallToolResult{Result: resp.Result, Actions: resp.Actions}, nil
	case <-ctx.Done():
		// For persistent handlers, the plugin process may still be
		// finishing the timed-out call (e.g. reading an HTTP response).
		// Drain the orphaned tool_result before releasing the mutex to
		// prevent stdin message ordering races with the next call.
		drainTimer := time.NewTimer(2 * time.Second)
		select {
		case <-ch:
		case <-drainTimer.C:
		}
		drainTimer.Stop()
		return nil, &protocol.Error{
			Code:    "timeout",
			Message: fmt.Sprintf("tool call %s timed out after %s", toolName, h.toolTimeout),
		}
	}
}

func (h *Handle) callOneshot(ctx context.Context, toolName string, params json.RawMessage) (*CallToolResult, error) {
	ctx, cancel := context.WithTimeout(ctx, h.toolTimeout)
	defer cancel()

	var stdin io.WriteCloser
	var stdout io.ReadCloser
	var stderr io.ReadCloser
	var cleanup func()

	if h.sbMgr != nil && h.sbMgr.Enabled() {
		env := buildPluginEnvMap(h.manifest, h.groupVars)
		sbProc, err := h.sbMgr.Launch(ctx, sandbox.PluginInfo{
			Name:            h.manifest.Name,
			Dir:             h.manifest.Dir,
			Handler:         h.manifest.Handler,
			CredentialGroup: h.manifest.CredentialGroup,
			SessionDir:      h.sessionDir,
			OutputDir:       h.outputDir,
		}, env)
		if err != nil {
			return nil, fmt.Errorf("sandbox launch: %w", err)
		}
		stdin = sbProc.Stdin()
		stdout = sbProc.Stdout()
		stderr = sbProc.Stderr()
		cleanup = func() {
			stdin.Close() //nolint:errcheck,gosec // best effort
			sbProc.Wait() //nolint:errcheck,gosec // reap process
			sbProc.Cleanup()
			h.sbMgr.CleanupTmpDir(h.manifest.Name)
		}
	} else {
		if h.sbMgr != nil {
			warnUnsandboxedOnce.Do(func() {
				log.Println("WARNING: sandbox not available (binary built without libarapuca) — plugin processes are not isolated")
			})
		}
		if err := validateHandlerAtLaunch(h.manifest); err != nil {
			return nil, err
		}
		cmd := exec.CommandContext(ctx, h.manifest.HandlerPath()) //nolint:gosec // handler path validated by Manifest.Validate()
		cmd.Dir = h.manifest.Dir
		cmd.Env = buildPluginEnv(h.manifest, h.groupVars)

		var err error
		stdin, err = cmd.StdinPipe()
		if err != nil {
			return nil, fmt.Errorf("stdin pipe: %w", err)
		}
		stdout, err = cmd.StdoutPipe()
		if err != nil {
			return nil, fmt.Errorf("stdout pipe: %w", err)
		}
		stderr, err = cmd.StderrPipe()
		if err != nil {
			return nil, fmt.Errorf("stderr pipe: %w", err)
		}

		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("start oneshot handler: %w", err)
		}
		cleanup = func() {
			stdin.Close() //nolint:errcheck,gosec // best effort
			cmd.Wait()    //nolint:errcheck,gosec // reap child
		}
	}
	defer cleanup()

	go forwardStderr(stderr, h.manifest.Name)

	// Send tool_call
	id := fmt.Sprintf("oneshot-%d", time.Now().UnixNano())
	enc := json.NewEncoder(stdin)
	if err := enc.Encode(protocol.Message{
		ID:     id,
		Type:   protocol.TypeToolCall,
		Tool:   toolName,
		Params: params,
		Config: h.resolvedConfig,
	}); err != nil {
		return nil, fmt.Errorf("send tool_call: %w", err)
	}

	// Read messages until we get tool_result
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0), h.processCfg.MaxMessageSize)

	for scanner.Scan() {
		var msg protocol.Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			log.Printf("[%s] malformed oneshot message: %v", h.manifest.Name, err)
			continue
		}

		switch msg.Type {
		case protocol.TypeHTTPRequest:
			resp := h.handler.HandleHTTP(ctx, h.manifest.Name, msg)
			if err := enc.Encode(resp); err != nil {
				return nil, fmt.Errorf("send http_response: %w", err)
			}
		case protocol.TypeCacheGet, protocol.TypeCacheSet, protocol.TypeCacheDel, protocol.TypeCacheList, protocol.TypeCacheFlush:
			resp := h.handler.HandleCache(ctx, h.manifest.Name, msg)
			if err := enc.Encode(resp); err != nil {
				return nil, fmt.Errorf("send cache response: %w", err)
			}
		case protocol.TypeFileWrite, protocol.TypeFileRead:
			resp := h.handler.HandleFileIO(ctx, h.manifest.Name, msg)
			if err := enc.Encode(resp); err != nil {
				return nil, fmt.Errorf("send file_io response: %w", err)
			}
		case protocol.TypeToolResult:
			if msg.Error != nil {
				return nil, msg.Error
			}
			return &CallToolResult{Result: msg.Result, Actions: msg.Actions}, nil
		default:
			log.Printf("[%s] unexpected oneshot message type: %q", h.manifest.Name, msg.Type)
		}
	}

	return nil, &protocol.Error{Code: "no_response", Message: "oneshot handler exited without tool_result"}
}

// ListResources queries the handler for its current resource list.
// Uses transport directly (not h.mu) to avoid deadlock with tool calls.
func (h *Handle) ListResources(ctx context.Context) ([]protocol.ResourceDef, error) {
	transport := h.process.Transport
	id := transport.GenerateID("res")
	resp, err := transport.SendAndWait(ctx, id, protocol.Message{
		Type: protocol.TypeListResources,
	})
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Resources, nil
}

// ReadResource requests the content of a specific resource URI.
// Uses transport directly (not h.mu) to avoid deadlock with tool calls.
func (h *Handle) ReadResource(ctx context.Context, uri string) (string, string, error) {
	transport := h.process.Transport
	id := transport.GenerateID("res")
	resp, err := transport.SendAndWait(ctx, id, protocol.Message{
		Type: protocol.TypeReadResource,
		URI:  uri,
	})
	if err != nil {
		return "", "", err
	}
	if resp.Error != nil {
		return "", "", resp.Error
	}
	return resp.Content, resp.MIMEType, nil
}

// InitialResources returns the cached resource list (thread-safe).
func (h *Handle) InitialResources() []protocol.ResourceDef {
	h.resMu.RLock()
	defer h.resMu.RUnlock()
	return h.resources
}

// SetResources updates the cached resource list (thread-safe).
func (h *Handle) SetResources(resources []protocol.ResourceDef) {
	h.resMu.Lock()
	defer h.resMu.Unlock()
	h.resources = resources
}

func (h *Handle) restartAllowed() bool {
	h.restartMu.Lock()
	defer h.restartMu.Unlock()
	now := time.Now()
	cutoff := now.Add(-restartWindow)
	recent := 0
	for _, t := range h.restarts {
		if t.After(cutoff) {
			recent++
		}
	}
	return recent < maxRestartsPerWindow
}

func (h *Handle) recordRestart() {
	h.restartMu.Lock()
	defer h.restartMu.Unlock()
	now := time.Now()
	cutoff := now.Add(-restartWindow)
	h.restarts = append(h.pruneRestarts(cutoff), now)
}

func (h *Handle) pruneRestarts(cutoff time.Time) []time.Time {
	var kept []time.Time
	for _, t := range h.restarts {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	return kept
}

func forwardStderr(r interface{ Read([]byte) (int, error) }, pluginName string) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		log.Printf("[%s] %s", pluginName, scanner.Text())
	}
}
