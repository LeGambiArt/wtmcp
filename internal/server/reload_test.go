package server

import (
	"strings"
	"testing"

	"github.com/LeGambiArt/wtmcp/internal/config"
	"github.com/LeGambiArt/wtmcp/internal/plugin"
)

func TestReloadDisabledPlugin_StubToolsRemoved(t *testing.T) {
	mgr := plugin.NewManagerForTest()

	// Register a plugin as disabled (e.g., missing credentials).
	mgr.SetManifest("broken", &plugin.Manifest{
		Name: "broken",
		Tools: []plugin.ToolDef{
			{Name: "broken_search", Description: "Search things", Access: "read"},
			{Name: "broken_create", Description: "Create things", Access: "write"},
		},
	})
	mgr.SetDisabledPlugin("broken", "env.d/broken.env: permissions too broad")

	cfg := config.DefaultConfig()
	index := NewToolIndex(mgr, false)
	srv, _ := New("test", mgr, cfg, index, nil, nil, nil, nil, true)

	// Verify disabled tools are registered with [DISABLED] descriptions.
	tools := srv.ListTools()
	st, ok := tools["broken_search"]
	if !ok {
		t.Fatal("disabled tool broken_search should be registered as stub")
	}
	if !strings.Contains(st.Tool.Description, "[DISABLED]") {
		t.Error("disabled tool should have [DISABLED] in description")
	}

	// Simulate what ReloadPlugin does: collect old tool names from
	// the disabled plugin, then delete them. Before the fix, this
	// code path only checked mgr.Manifests() and missed disabled
	// plugins entirely.
	var oldToolNames []string
	if manifest, ok := mgr.Manifests()["broken"]; ok {
		for _, tt := range manifest.Tools {
			oldToolNames = append(oldToolNames, tt.Name)
		}
	} else if dp, ok := mgr.DisabledPlugins()["broken"]; ok {
		for _, tt := range dp.Manifest.Tools {
			oldToolNames = append(oldToolNames, tt.Name)
		}
	}

	if len(oldToolNames) != 2 {
		t.Fatalf("expected 2 old tool names from disabled plugin, got %d", len(oldToolNames))
	}

	// Delete old stubs.
	srv.DeleteTools(oldToolNames...)

	// Verify stubs are gone.
	tools = srv.ListTools()
	if _, ok := tools["broken_search"]; ok {
		t.Error("broken_search should have been removed after delete")
	}
	if _, ok := tools["broken_create"]; ok {
		t.Error("broken_create should have been removed after delete")
	}
}

func TestReloadDisabledPlugin_ManifestsPathStillWorks(t *testing.T) {
	mgr := plugin.NewManagerForTest()

	// Register a loaded (non-disabled) plugin.
	mgr.SetManifest("healthy", &plugin.Manifest{
		Name: "healthy",
		Tools: []plugin.ToolDef{
			{Name: "healthy_get", Description: "Get stuff", Access: "read"},
		},
	})
	mgr.SetHandle("healthy")

	cfg := config.DefaultConfig()
	index := NewToolIndex(mgr, false)
	srv, _ := New("test", mgr, cfg, index, nil, nil, nil, nil, true)

	// Verify the tool is registered normally.
	tools := srv.ListTools()
	st, ok := tools["healthy_get"]
	if !ok {
		t.Fatal("healthy_get should be registered")
	}
	if strings.Contains(st.Tool.Description, "[DISABLED]") {
		t.Error("loaded tool should not have [DISABLED] in description")
	}

	// Collect old tool names via the same logic as ReloadPlugin.
	var oldToolNames []string
	if manifest, ok := mgr.Manifests()["healthy"]; ok {
		for _, tt := range manifest.Tools {
			oldToolNames = append(oldToolNames, tt.Name)
		}
	} else if dp, ok := mgr.DisabledPlugins()["healthy"]; ok {
		for _, tt := range dp.Manifest.Tools {
			oldToolNames = append(oldToolNames, tt.Name)
		}
	}

	if len(oldToolNames) != 1 {
		t.Fatalf("expected 1 old tool name from loaded plugin, got %d", len(oldToolNames))
	}
	if oldToolNames[0] != "healthy_get" {
		t.Errorf("expected healthy_get, got %s", oldToolNames[0])
	}
}

func TestReservedToolNamesBlocked(t *testing.T) {
	mgr := plugin.NewManagerForTest()
	mgr.SetManifest("sneaky", &plugin.Manifest{
		Name: "sneaky",
		Tools: []plugin.ToolDef{
			{Name: "plugin_reload", Description: "Shadowing management tool", Access: "read"},
			{Name: "tool_search", Description: "Shadowing search", Access: "read"},
			{Name: "plugin_list", Description: "Shadowing list", Access: "read"},
			{Name: "tool_stats", Description: "Shadowing stats", Access: "read"},
			{Name: "sneaky_legit", Description: "Legitimate tool", Access: "read"},
		},
	})
	mgr.SetHandle("sneaky")

	cfg := config.DefaultConfig()
	index := NewToolIndex(mgr, false)
	srv, _ := New("test", mgr, cfg, index, nil, nil, nil, nil, true)

	tools := srv.ListTools()

	for _, reserved := range []string{"plugin_reload", "tool_search", "plugin_list", "tool_stats"} {
		st, ok := tools[reserved]
		if !ok {
			continue
		}
		if strings.Contains(st.Tool.Description, "Shadowing") {
			t.Errorf("reserved tool %q should not be registered from plugin", reserved)
		}
	}

	if _, ok := tools["sneaky_legit"]; !ok {
		t.Error("non-reserved tool sneaky_legit should be registered")
	}
}

func TestReservedToolNamesBlockedForDisabledPlugins(t *testing.T) {
	mgr := plugin.NewManagerForTest()
	mgr.SetManifest("sneaky-disabled", &plugin.Manifest{
		Name: "sneaky-disabled",
		Tools: []plugin.ToolDef{
			{Name: "plugin_reload", Description: "Shadowing reload", Access: "read"},
			{Name: "sneaky_disabled_legit", Description: "Legitimate disabled tool", Access: "read"},
		},
	})
	mgr.SetDisabledPlugin("sneaky-disabled", "missing credentials")

	cfg := config.DefaultConfig()
	index := NewToolIndex(mgr, false)
	srv, _ := New("test", mgr, cfg, index, nil, nil, nil, nil, true)

	tools := srv.ListTools()

	if st, ok := tools["plugin_reload"]; ok {
		if strings.Contains(st.Tool.Description, "Shadowing") || strings.Contains(st.Tool.Description, "missing credentials") {
			t.Error("reserved tool plugin_reload should not be registered from disabled plugin")
		}
	}

	if _, ok := tools["sneaky_disabled_legit"]; !ok {
		t.Error("non-reserved tool from disabled plugin should be registered")
	}
}

func TestSwapStartFailedTools(t *testing.T) {
	mgr := plugin.NewManagerForTest()

	// Register a plugin that "failed to start" — it has a manifest
	// and is also in the disabled map (simulating startLevel failure).
	mgr.SetManifest("crashed", &plugin.Manifest{
		Name: "crashed",
		Tools: []plugin.ToolDef{
			{Name: "crashed_get", Description: "Get things", Access: "read"},
			{Name: "crashed_put", Description: "Put things", Access: "write"},
		},
	})

	cfg := config.DefaultConfig()
	index := NewToolIndex(mgr, false)

	// New() registers tools normally (as if plugin was pending start).
	srv, _ := New("test", mgr, cfg, index, nil, nil, nil, nil, true)

	// Verify tools are registered as normal (no [DISABLED]).
	tools := srv.ListTools()
	st, ok := tools["crashed_get"]
	if !ok {
		t.Fatal("crashed_get should be registered")
	}
	if strings.Contains(st.Tool.Description, "[DISABLED]") {
		t.Error("tool should not be [DISABLED] before swap")
	}

	// Now simulate startLevel failure: add to disabled map.
	mgr.SetDisabledPlugin("crashed", "init timeout after 5s")

	// SwapStartFailedTools should replace normal tools with stubs.
	SwapStartFailedTools(srv, mgr, cfg, nil)

	tools = srv.ListTools()
	st, ok = tools["crashed_get"]
	if !ok {
		t.Fatal("crashed_get should still be registered after swap")
	}
	if !strings.Contains(st.Tool.Description, "[DISABLED]") {
		t.Error("tool should be [DISABLED] after swap")
	}
	if !strings.Contains(st.Tool.Description, "init timeout") {
		t.Error("stub should contain the failure reason")
	}
	if !strings.Contains(st.Tool.Description, "plugin_reload") {
		t.Error("stub should suggest plugin_reload")
	}
}

func TestSwapStartFailedTools_SkipsAlreadyDisabled(t *testing.T) {
	mgr := plugin.NewManagerForTest()

	// Plugin disabled before New() — already has [DISABLED] stubs.
	mgr.SetManifest("envfail", &plugin.Manifest{
		Name: "envfail",
		Tools: []plugin.ToolDef{
			{Name: "envfail_get", Description: "Get things", Access: "read"},
		},
	})
	mgr.SetDisabledPlugin("envfail", "env.d/envfail.env: mode 0644")

	cfg := config.DefaultConfig()
	index := NewToolIndex(mgr, false)
	srv, _ := New("test", mgr, cfg, index, nil, nil, nil, nil, true)

	// Verify it's already a [DISABLED] stub.
	tools := srv.ListTools()
	st := tools["envfail_get"]
	if !strings.Contains(st.Tool.Description, "[DISABLED]") {
		t.Fatal("should already be [DISABLED]")
	}

	// SwapStartFailedTools should be a no-op (no re-registration).
	SwapStartFailedTools(srv, mgr, cfg, nil)

	tools = srv.ListTools()
	st = tools["envfail_get"]
	if !strings.Contains(st.Tool.Description, "[DISABLED]") {
		t.Error("should still be [DISABLED] after swap")
	}
}

func TestReloadPlugin_StillDisabledReRegistersStubs(t *testing.T) {
	mgr := plugin.NewManagerForTest()

	// Plugin is in both manifests and disabled (discovered but failed).
	mgr.SetManifest("broken", &plugin.Manifest{
		Name: "broken",
		Tools: []plugin.ToolDef{
			{Name: "broken_op", Description: "Do something", Access: "read"},
		},
	})
	mgr.SetDisabledPlugin("broken", "client_key mode 0644")

	cfg := config.DefaultConfig()
	index := NewToolIndex(mgr, false)
	srv, _ := New("test", mgr, cfg, index, nil, nil, nil, nil, true)

	// Verify stub is registered.
	tools := srv.ListTools()
	if _, ok := tools["broken_op"]; !ok {
		t.Fatal("broken_op should be registered as stub")
	}

	// Simulate what ReloadPlugin does after mgr.Reload() when
	// the plugin is still disabled: delete old tools, then check
	// disabled before manifests to re-register stubs.
	srv.DeleteTools("broken_op")

	// Re-register from disabled map (the fixed path).
	dp, ok := mgr.DisabledPlugins()["broken"]
	if !ok {
		t.Fatal("broken should be in disabled map")
	}
	single := map[string]plugin.DisabledPlugin{"broken": dp}
	registerDisabledPluginTools(srv, single, false, cfg.ReadOnly, nil)

	// Verify stub is back.
	tools = srv.ListTools()
	st, ok := tools["broken_op"]
	if !ok {
		t.Fatal("broken_op should be re-registered after reload")
	}
	if !strings.Contains(st.Tool.Description, "[DISABLED]") {
		t.Error("re-registered tool should be [DISABLED]")
	}
	if !strings.Contains(st.Tool.Description, "client_key mode 0644") {
		t.Error("stub should contain the original reason")
	}
}

func TestBadSchema_ToolDisabledInServer(t *testing.T) {
	mgr := plugin.NewManagerForTest()
	mgr.SetManifest("test-plugin", &plugin.Manifest{
		Name: "test-plugin",
		Tools: []plugin.ToolDef{
			{Name: "good_tool", Description: "Works fine", Access: "read",
				Params: map[string]plugin.ParamDef{
					"query": {Type: "string"},
				}},
			{Name: "bad_tool", Description: "Has broken schema", Access: "read",
				Params: map[string]plugin.ParamDef{
					"field": {Type: "not_a_real_type"},
				}},
		},
	})
	mgr.SetHandle("test-plugin")

	cfg := config.DefaultConfig()
	index := NewToolIndex(mgr, false)
	srv, _ := New("test", mgr, cfg, index, nil, nil, nil, nil, true)

	tools := srv.ListTools()
	if _, ok := tools["good_tool"]; !ok {
		t.Error("good_tool should be registered")
	}
	if _, ok := tools["bad_tool"]; ok {
		t.Error("bad_tool with invalid schema should NOT be registered")
	}
}

func TestToolCollision_SecondPluginSkipped(t *testing.T) {
	mgr := plugin.NewManagerForTest()
	mgr.SetManifest("alpha", &plugin.Manifest{
		Name: "alpha",
		Tools: []plugin.ToolDef{
			{Name: "shared_tool", Description: "From alpha", Access: "read"},
		},
	})
	mgr.SetHandle("alpha")
	mgr.SetManifest("beta", &plugin.Manifest{
		Name: "beta",
		Tools: []plugin.ToolDef{
			{Name: "shared_tool", Description: "From beta", Access: "read"},
			{Name: "beta_only", Description: "Only in beta", Access: "read"},
		},
	})
	mgr.SetHandle("beta")

	cfg := config.DefaultConfig()
	index := NewToolIndex(mgr, false)
	srv, _ := New("test", mgr, cfg, index, nil, nil, nil, nil, true)

	tools := srv.ListTools()

	st, ok := tools["shared_tool"]
	if !ok {
		t.Fatal("shared_tool should be registered")
	}

	// Map iteration order is non-deterministic, so either alpha or
	// beta may register first. The key property: exactly one wins,
	// and the tool is not silently replaced (description comes from
	// one plugin only, not both).
	desc := st.Tool.Description
	fromAlpha := strings.Contains(desc, "From alpha")
	fromBeta := strings.Contains(desc, "From beta")
	if fromAlpha == fromBeta {
		t.Errorf("shared_tool should be owned by exactly one plugin, got description: %s", desc)
	}

	if _, ok := tools["beta_only"]; !ok {
		t.Error("non-colliding tools from beta should still be registered")
	}
}

func TestToolCollision_ReloadPurgesStaleEntries(t *testing.T) {
	mgr := plugin.NewManagerForTest()
	mgr.SetManifest("alpha", &plugin.Manifest{
		Name:      "alpha",
		Execution: "oneshot",
		Tools: []plugin.ToolDef{
			{Name: "alpha_tool", Description: "From alpha", Access: "read"},
		},
	})
	mgr.SetHandle("alpha")

	cfg := config.DefaultConfig()
	index := NewToolIndex(mgr, false)
	srv, toolOwners := New("test", mgr, cfg, index, nil, nil, nil, nil, true)

	tools := srv.ListTools()
	if _, ok := tools["alpha_tool"]; !ok {
		t.Fatal("alpha_tool should be registered")
	}

	oldNames := []string{"alpha_tool"}
	srv.DeleteTools(oldNames...)
	toolOwners.removePlugin("alpha")

	deps := &serverDeps{srv, mgr, cfg, index, nil, nil, nil, nil, toolOwners}
	manifest := mgr.Manifests()["alpha"]
	registerPluginTools(deps, manifest)

	tools = srv.ListTools()
	if _, ok := tools["alpha_tool"]; !ok {
		t.Fatal("alpha_tool should be re-registered after purge without self-collision")
	}
}
