package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/LeGambiArt/wtmcp/internal/config"
	"github.com/LeGambiArt/wtmcp/internal/plugin"
	"github.com/LeGambiArt/wtmcp/internal/profile"
)

// buildFilter builds a *profile.Filter for tests using only the exported
// resolver API (Filter's own constructor is unexported).
func buildFilter(t *testing.T, def config.ProfileDefinition) *profile.Filter {
	t.Helper()
	loaded := &config.ProfileLoadResult{
		Definitions: map[string]config.ProfileDefinition{"p": def},
		Rules: []config.ProfileRule{
			{Match: config.ProfileMatch{CN: "bot"}, Profile: "p", File: "f.yaml"},
		},
	}
	resolver, err := profile.NewResolver("", loaded)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	f := resolver.FilterFor(profile.Identity{CN: "bot"})
	if f == nil {
		t.Fatalf("expected non-nil filter")
	}
	return f
}

func newFilterTestServer(t *testing.T) *mcpServerWithInit {
	t.Helper()
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
	// gamma is discovered but disabled: its tools register as [DISABLED]
	// stubs. They must still carry their plugin owner so profile rules
	// that name the plugin apply to them.
	mgr.SetManifest("gamma", &plugin.Manifest{
		Name: "gamma",
		Tools: []plugin.ToolDef{
			{Name: "gamma_op", Description: "Op", Access: "read", Visibility: "primary"},
		},
	})
	mgr.SetDisabledPlugin("gamma", "boom")

	cfg := config.DefaultConfig()
	cfg.Tools.Discovery = "full"

	index := NewToolIndex(mgr, false)
	srv, _ := New("test", mgr, cfg, index, nil, nil, nil, nil, true)

	s := &mcpServerWithInit{srv: srv}
	s.initialize(t)
	return s
}

type mcpServerWithInit struct {
	srv interface {
		HandleMessage(context.Context, json.RawMessage) mcp.JSONRPCMessage
	}
}

func (s *mcpServerWithInit) initialize(t *testing.T) {
	t.Helper()
	s.srv.HandleMessage(context.Background(), json.RawMessage(`{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": {"protocolVersion": "2025-03-26",
		           "clientInfo": {"name": "test", "version": "1"},
		           "capabilities": {}}
	}`))
}

// listToolNames drives tools/list under ctx and returns the tool names.
func (s *mcpServerWithInit) listToolNames(ctx context.Context, t *testing.T) map[string]bool {
	t.Helper()
	resp := s.srv.HandleMessage(ctx, json.RawMessage(`{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}
	}`))
	r, ok := resp.(mcp.JSONRPCResponse)
	if !ok {
		t.Fatalf("tools/list: expected JSONRPCResponse, got %T", resp)
	}
	b, _ := json.Marshal(r.Result)
	var parsed struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("unmarshal tools/list: %v", err)
	}
	names := make(map[string]bool)
	for _, tl := range parsed.Tools {
		names[tl.Name] = true
	}
	return names
}

func TestToolFilterListHidesDeniedTools(t *testing.T) {
	s := newFilterTestServer(t)
	filter := buildFilter(t, config.ProfileDefinition{
		Allow: map[string][]string{"alpha": {"alpha_get_.*"}},
	})
	ctx := profile.WithFilter(context.Background(), filter)

	names := s.listToolNames(ctx, t)
	if !names["alpha_get_data"] {
		t.Errorf("alpha_get_data should be visible")
	}
	if names["alpha_delete_data"] {
		t.Errorf("alpha_delete_data should be hidden by profile")
	}
	// Exempt introspection tools remain visible.
	if !names["tool_search"] {
		t.Errorf("tool_search must remain visible (exempt)")
	}
}

func TestToolFilterListNoProfileShowsAll(t *testing.T) {
	s := newFilterTestServer(t)
	// No filter in context -> backward compatible, all tools visible.
	names := s.listToolNames(context.Background(), t)
	if !names["alpha_get_data"] || !names["alpha_delete_data"] {
		t.Errorf("without a profile all tools should be visible, got %v", names)
	}
}

func TestToolFilterDenyAllHidesEverythingButExempt(t *testing.T) {
	s := newFilterTestServer(t)
	// Empty allow -> deny all (only exempt tools pass).
	filter := buildFilter(t, config.ProfileDefinition{})
	ctx := profile.WithFilter(context.Background(), filter)

	names := s.listToolNames(ctx, t)
	if names["alpha_get_data"] || names["alpha_delete_data"] {
		t.Errorf("deny-all should hide plugin tools, got %v", names)
	}
	if !names["tool_search"] || !names["plugin_list"] {
		t.Errorf("exempt tools must remain, got %v", names)
	}
}

func TestToolFilterBlocksDeniedCall(t *testing.T) {
	s := newFilterTestServer(t)
	filter := buildFilter(t, config.ProfileDefinition{
		Allow: map[string][]string{"alpha": {"alpha_get_.*"}},
	})
	ctx := profile.WithFilter(context.Background(), filter)

	// Calling a denied tool must be rejected by mcp-go before the handler.
	resp := s.srv.HandleMessage(ctx, json.RawMessage(`{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": {"name": "alpha_delete_data", "arguments": {}}
	}`))
	if _, isErr := resp.(mcp.JSONRPCError); !isErr {
		t.Fatalf("denied tools/call should return a JSON-RPC error, got %T", resp)
	}
}

// callToolText drives a tools/call and returns the text of the first
// content block, for tools that return a JSON/text payload.
func (s *mcpServerWithInit) callToolText(ctx context.Context, t *testing.T, raw string) string {
	t.Helper()
	resp := s.srv.HandleMessage(ctx, json.RawMessage(raw))
	r, ok := resp.(mcp.JSONRPCResponse)
	if !ok {
		t.Fatalf("tools/call: expected JSONRPCResponse, got %T", resp)
	}
	b, _ := json.Marshal(r.Result)
	var parsed struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("unmarshal tools/call result: %v", err)
	}
	if len(parsed.Content) == 0 {
		return ""
	}
	return parsed.Content[0].Text
}

func TestPluginListFilteredByProfile(t *testing.T) {
	s := newFilterTestServer(t)
	// Allow only alpha's read tool; beta is entirely denied.
	filter := buildFilter(t, config.ProfileDefinition{
		Allow: map[string][]string{"alpha": {"alpha_get_.*"}},
	})
	ctx := profile.WithFilter(context.Background(), filter)

	body := s.callToolText(ctx, t, `{
		"jsonrpc": "2.0", "id": 5, "method": "tools/call",
		"params": {"name": "plugin_list", "arguments": {}}
	}`)
	if !strings.Contains(body, "alpha") {
		t.Errorf("plugin_list should include alpha (has an allowed tool), got: %s", body)
	}
	if strings.Contains(body, "beta") {
		t.Errorf("plugin_list must NOT include fully-denied beta, got: %s", body)
	}

	// Counts must reflect only the profile-allowed tools: alpha has two
	// tools but the profile allows just alpha_get_data, so the advertised
	// totals must not reveal the gated alpha_delete_data.
	var listed []struct {
		Name    string `json:"name"`
		Tools   int    `json:"tools"`
		Primary int    `json:"primary"`
	}
	if err := json.Unmarshal([]byte(body), &listed); err != nil {
		t.Fatalf("unmarshal plugin_list: %v (body: %s)", err, body)
	}
	var sawAlpha bool
	for _, p := range listed {
		if p.Name != "alpha" {
			continue
		}
		sawAlpha = true
		if p.Tools != 1 {
			t.Errorf("alpha tools count = %d, want 1 (only alpha_get_data allowed): %s", p.Tools, body)
		}
		if p.Primary != 1 {
			t.Errorf("alpha primary count = %d, want 1: %s", p.Primary, body)
		}
	}
	if !sawAlpha {
		t.Errorf("plugin_list did not list alpha: %s", body)
	}
}

func TestPluginListNoProfileShowsAll(t *testing.T) {
	s := newFilterTestServer(t)
	body := s.callToolText(context.Background(), t, `{
		"jsonrpc": "2.0", "id": 6, "method": "tools/call",
		"params": {"name": "plugin_list", "arguments": {}}
	}`)
	if !strings.Contains(body, "alpha") || !strings.Contains(body, "beta") {
		t.Errorf("without a profile plugin_list should include all plugins, got: %s", body)
	}
}

func TestDisabledStubRespectsPluginOwnerInProfile(t *testing.T) {
	s := newFilterTestServer(t)
	// gamma is disabled; a profile that allows gamma by name must still
	// show its [DISABLED] stub. This regresses the case where a stub's
	// owner was empty and only "*" rules could match it.
	filter := buildFilter(t, config.ProfileDefinition{
		Allow: map[string][]string{"gamma": {".*"}},
	})
	ctx := profile.WithFilter(context.Background(), filter)

	names := s.listToolNames(ctx, t)
	if !names["gamma_op"] {
		t.Errorf("disabled stub gamma_op should be visible when profile allows gamma, got %v", names)
	}
	// A plugin the profile does not name stays hidden.
	if names["alpha_get_data"] {
		t.Errorf("alpha_get_data should be hidden (not allowed), got %v", names)
	}
}

func TestDisabledStubHiddenByDenyAll(t *testing.T) {
	s := newFilterTestServer(t)
	filter := buildFilter(t, config.ProfileDefinition{})
	ctx := profile.WithFilter(context.Background(), filter)

	names := s.listToolNames(ctx, t)
	if names["gamma_op"] {
		t.Errorf("deny-all must hide disabled stub gamma_op, got %v", names)
	}
}

func TestToolSearchResultsFilteredByProfile(t *testing.T) {
	s := newFilterTestServer(t)
	filter := buildFilter(t, config.ProfileDefinition{
		Allow: map[string][]string{"alpha": {"alpha_get_.*"}},
	})
	ctx := profile.WithFilter(context.Background(), filter)

	resp := s.srv.HandleMessage(ctx, json.RawMessage(`{
		"jsonrpc": "2.0", "id": 4, "method": "tools/call",
		"params": {"name": "tool_search", "arguments": {"query": "data"}}
	}`))
	r, ok := resp.(mcp.JSONRPCResponse)
	if !ok {
		t.Fatalf("tool_search: expected JSONRPCResponse, got %T", resp)
	}
	b, _ := json.Marshal(r.Result)
	body := string(b)
	if !strings.Contains(body, "alpha_get_data") {
		t.Errorf("tool_search should include allowed alpha_get_data, got: %s", body)
	}
	if strings.Contains(body, "alpha_delete_data") {
		t.Errorf("tool_search must NOT include denied alpha_delete_data, got: %s", body)
	}
}
