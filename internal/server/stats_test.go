package server

import (
	"testing"

	"github.com/LeGambiArt/wtmcp/internal/config"
	"github.com/LeGambiArt/wtmcp/internal/plugin"
	"github.com/LeGambiArt/wtmcp/internal/stats"
)

func TestStats_SchemaRecording(t *testing.T) {
	collector := stats.NewCollector(stats.CharsTokenizer{}, false)

	mgr := plugin.NewManagerForTest()
	mgr.SetManifest("jira", &plugin.Manifest{
		Name: "jira",
		Tools: []plugin.ToolDef{
			{Name: "get_issues", Description: "Get Jira issues matching a JQL query", Access: "read", Visibility: "primary",
				Params: map[string]plugin.ParamDef{"jql": {Type: "string", Description: "JQL query"}}},
			{Name: "create_issue", Description: "Create a new issue", Access: "write"},
		},
	})
	mgr.SetHandle("jira")

	cfg := config.DefaultConfig()
	index := NewToolIndex(mgr, false)
	_, _ = New("test", mgr, cfg, index, collector, nil, nil, nil, true)

	cost := collector.SchemaCost()
	if cost.TotalTools != 2 {
		t.Errorf("TotalTools = %d, want 2", cost.TotalTools)
	}
	if cost.TotalSchemaTokens == 0 {
		t.Error("TotalSchemaTokens should be > 0")
	}
	if len(cost.ByPlugin) != 1 {
		t.Fatalf("expected 1 plugin in schema cost, got %d", len(cost.ByPlugin))
	}
	if cost.ByPlugin[0].Plugin != "jira" {
		t.Errorf("Plugin = %q, want jira", cost.ByPlugin[0].Plugin)
	}
}

func TestStats_SchemaRecordingMultiplePlugins(t *testing.T) {
	collector := stats.NewCollector(stats.CharsTokenizer{}, false)

	mgr := plugin.NewManagerForTest()
	mgr.SetManifest("jira", &plugin.Manifest{
		Name: "jira",
		Tools: []plugin.ToolDef{
			{Name: "get_issues", Description: "Get issues", Access: "read"},
		},
	})
	mgr.SetManifest("keylime", &plugin.Manifest{
		Name: "keylime",
		Tools: []plugin.ToolDef{
			{Name: "list_agents", Description: "List agents", Access: "read"},
			{Name: "get_agent", Description: "Get agent", Access: "read"},
		},
	})
	mgr.SetHandle("jira")
	mgr.SetHandle("keylime")

	cfg := config.DefaultConfig()
	index := NewToolIndex(mgr, false)
	_, _ = New("test", mgr, cfg, index, collector, nil, nil, nil, true)

	cost := collector.SchemaCost()
	if cost.TotalTools != 3 {
		t.Errorf("TotalTools = %d, want 3", cost.TotalTools)
	}
	if len(cost.ByPlugin) != 2 {
		t.Errorf("expected 2 plugins in schema cost, got %d", len(cost.ByPlugin))
	}
}

func TestStats_NilCollector(t *testing.T) {
	mgr := plugin.NewManagerForTest()
	mgr.SetManifest("test", &plugin.Manifest{
		Name: "test",
		Tools: []plugin.ToolDef{
			{Name: "tool1", Description: "Tool 1"},
		},
	})
	mgr.SetHandle("test")

	cfg := config.DefaultConfig()
	index := NewToolIndex(mgr, false)

	// Should not panic with nil collector.
	srv, _ := New("test", mgr, cfg, index, nil, nil, nil, nil, true)
	tools := srv.ListTools()
	if _, ok := tools["tool1"]; !ok {
		t.Error("tool1 should be registered even with nil collector")
	}
}

func TestStats_ToolStatsRegistered(t *testing.T) {
	collector := stats.NewCollector(stats.CharsTokenizer{}, false)

	mgr := plugin.NewManagerForTest()
	mgr.SetManifest("test", &plugin.Manifest{
		Name: "test",
		Tools: []plugin.ToolDef{
			{Name: "tool1", Description: "Tool 1"},
		},
	})
	mgr.SetHandle("test")

	cfg := config.DefaultConfig()
	index := NewToolIndex(mgr, false)
	srv, _ := New("test", mgr, cfg, index, collector, nil, nil, nil, true)

	tools := srv.ListTools()
	if _, ok := tools["tool_stats"]; !ok {
		t.Error("tool_stats should be registered when collector is non-nil")
	}
}

func TestStats_ToolStatsNotRegisteredWithNilCollector(t *testing.T) {
	mgr := plugin.NewManagerForTest()
	cfg := config.DefaultConfig()
	index := NewToolIndex(mgr, false)
	srv, _ := New("test", mgr, cfg, index, nil, nil, nil, nil, true)

	tools := srv.ListTools()
	if _, ok := tools["tool_stats"]; ok {
		t.Error("tool_stats should NOT be registered when collector is nil")
	}
}

func TestStats_ExcludedToolsSet(t *testing.T) {
	excluded := ExcludedTools()
	expected := []string{"tool_stats", "plugin_list", "plugin_reload", "tool_search"}
	for _, name := range expected {
		if !excluded[name] {
			t.Errorf("%q should be in excluded tools set", name)
		}
	}
}
