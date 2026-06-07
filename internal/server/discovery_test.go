package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/LeGambiArt/wtmcp/internal/config"
	"github.com/LeGambiArt/wtmcp/internal/plugin"
)

func TestDiscoveryFullMode(t *testing.T) {
	mgr := plugin.NewManagerForTest()
	mgr.SetManifest("alpha", &plugin.Manifest{
		Name: "alpha",
		Tools: []plugin.ToolDef{
			{Name: "alpha_search", Description: "Search", Access: "read", Visibility: "primary"},
			{Name: "alpha_export", Description: "Export", Access: "read"},
		},
	})
	mgr.SetHandle("alpha")

	cfg := config.DefaultConfig()
	cfg.Tools.Discovery = "full"

	index := NewToolIndex(mgr, false)
	srv, _ := New("test", mgr, cfg, index, nil, nil, nil, nil, true)
	tools := srv.ListTools()

	// All tools should be registered (no DeferLoading)
	if _, ok := tools["alpha_search"]; !ok {
		t.Error("alpha_search should be registered")
	}
	if _, ok := tools["alpha_export"]; !ok {
		t.Error("alpha_export should be registered")
	}
	if _, ok := tools["tool_search"]; !ok {
		t.Error("tool_search should be registered")
	}

	// No tool should have DeferLoading in full mode
	for name, st := range tools {
		if st.Tool.DeferLoading {
			t.Errorf("tool %q has DeferLoading=true in full mode", name)
		}
	}
}

func TestDiscoveryProgressiveMode(t *testing.T) {
	mgr := plugin.NewManagerForTest()
	mgr.SetManifest("alpha", &plugin.Manifest{
		Name: "alpha",
		Tools: []plugin.ToolDef{
			{Name: "alpha_search", Description: "Search", Access: "read", Visibility: "primary"},
			{Name: "alpha_export", Description: "Export", Access: "read"},
		},
	})
	mgr.SetHandle("alpha")

	cfg := config.DefaultConfig()
	cfg.Tools.Discovery = "progressive"

	index := NewToolIndex(mgr, false)
	srv, _ := New("test", mgr, cfg, index, nil, nil, nil, nil, true)
	tools := srv.ListTools()

	// All tools should still be registered
	if _, ok := tools["alpha_search"]; !ok {
		t.Error("alpha_search should be registered")
	}
	if _, ok := tools["alpha_export"]; !ok {
		t.Error("alpha_export should be registered")
	}

	// Primary tool should NOT have DeferLoading
	if tools["alpha_search"].Tool.DeferLoading {
		t.Error("primary tool alpha_search should not have DeferLoading")
	}

	// Deferred tool SHOULD have DeferLoading
	if !tools["alpha_export"].Tool.DeferLoading {
		t.Error("deferred tool alpha_export should have DeferLoading=true")
	}

	// tool_search should be registered
	if _, ok := tools["tool_search"]; !ok {
		t.Error("tool_search should be registered")
	}
}

func TestDiscoveryToolSearchRegistered(t *testing.T) {
	mgr := plugin.NewManagerForTest()
	mgr.SetManifest("alpha", &plugin.Manifest{
		Name: "alpha",
		Tools: []plugin.ToolDef{
			{Name: "alpha_search", Description: "Search alpha", Access: "read", Visibility: "primary"},
		},
	})
	mgr.SetHandle("alpha")

	cfg := config.DefaultConfig()
	index := NewToolIndex(mgr, false)
	srv, _ := New("test", mgr, cfg, index, nil, nil, nil, nil, true)

	tools := srv.ListTools()
	ts, ok := tools["tool_search"]
	if !ok {
		t.Fatal("tool_search should be registered")
	}

	// Verify it has a description with category summary
	if ts.Tool.Description == "" {
		t.Error("tool_search should have a description")
	}

	// Verify read-only annotation
	if ts.Tool.Annotations.ReadOnlyHint == nil || !*ts.Tool.Annotations.ReadOnlyHint {
		t.Error("tool_search should be marked read-only")
	}
}

func TestDiscoveryToolSearchSafeResponse(t *testing.T) {
	mgr := plugin.NewManagerForTest()
	mgr.SetManifest("alpha", &plugin.Manifest{
		Name: "alpha",
		Tools: []plugin.ToolDef{
			{
				Name: "alpha_search", Description: "Search", Access: "read",
				Params: map[string]plugin.ParamDef{
					"query": {Type: "string", Required: true},
				},
			},
		},
	})
	mgr.SetHandle("alpha")

	index := NewToolIndex(mgr, false)
	results := index.Search("search", "", 10)
	if len(results) == 0 {
		t.Fatal("expected search results")
	}

	sr := results[0].toSearchResult()
	data, err := json.Marshal(sr)
	if err != nil {
		t.Fatal(err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}

	// Only safe keys
	allowedKeys := map[string]bool{
		"name": true, "plugin": true, "description": true,
		"access": true, "params": true,
	}
	for key := range raw {
		if !allowedKeys[key] {
			t.Errorf("unexpected key in search result: %q", key)
		}
	}
}

func TestNewSandboxInactiveInjectsWarning(t *testing.T) {
	mgr := plugin.NewManagerForTest()
	cfg := config.DefaultConfig()
	index := NewToolIndex(mgr, false)

	srv, _ := New("test", mgr, cfg, index, nil, nil, nil, nil, false)

	resp := srv.HandleMessage(context.Background(), json.RawMessage(`{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": {"protocolVersion": "2025-03-26",
		           "clientInfo": {"name": "test", "version": "1"},
		           "capabilities": {}}
	}`))
	r, ok := resp.(mcp.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected JSONRPCResponse, got %T", resp)
	}
	b, _ := json.Marshal(r.Result)
	if !strings.Contains(string(b), "WITHOUT sandbox isolation") {
		t.Errorf("initialize response should contain sandbox warning, got: %s", b)
	}
}

func TestDiscoveryFullModeBackwardCompatible(t *testing.T) {
	mgr := plugin.NewManagerForTest()
	mgr.SetManifest("alpha", &plugin.Manifest{
		Name: "alpha",
		Tools: []plugin.ToolDef{
			{Name: "alpha_one", Description: "Tool one", Access: "read", Visibility: "primary"},
			{Name: "alpha_two", Description: "Tool two", Access: "read"},
		},
	})
	mgr.SetHandle("alpha")

	cfg := config.DefaultConfig()
	cfg.Tools.Discovery = "full"

	index := NewToolIndex(mgr, false)
	srv, _ := New("test", mgr, cfg, index, nil, nil, nil, nil, true)
	tools := srv.ListTools()

	// Expected: alpha_one, alpha_two, tool_search, plugin_list, plugin_reload
	expectedTools := []string{"alpha_one", "alpha_two", "tool_search", "plugin_list", "plugin_reload"}
	for _, name := range expectedTools {
		if _, ok := tools[name]; !ok {
			t.Errorf("expected tool %q in full mode", name)
		}
	}

	// No DeferLoading on any tool
	for name, st := range tools {
		if st.Tool.DeferLoading {
			t.Errorf("tool %q should not have DeferLoading in full mode", name)
		}
	}
}
