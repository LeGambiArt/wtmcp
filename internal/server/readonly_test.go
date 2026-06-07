package server

import (
	"strings"
	"testing"

	"github.com/LeGambiArt/wtmcp/internal/config"
	"github.com/LeGambiArt/wtmcp/internal/plugin"
)

func TestReadOnly_OnlyReadToolsRegistered(t *testing.T) {
	mgr := testManager()
	cfg := config.DefaultConfig()
	cfg.ReadOnly = true

	index := NewToolIndex(mgr, true)
	srv, _ := New("test", mgr, cfg, index, nil, nil, nil, nil, true)
	tools := srv.ListTools()

	// Read tools should be registered
	readTools := []string{
		"jira_search", "jira_get_issues", "jira_export_sprint_data",
		"jira_debug_fields", "gmail_list_messages",
	}
	for _, name := range readTools {
		if _, ok := tools[name]; !ok {
			t.Errorf("read tool %q should be registered in read-only mode", name)
		}
	}

	// Write tools should NOT be registered
	writeTools := []string{
		"jira_create_issue", "gmail_send_message", "gmail_modify_labels",
	}
	for _, name := range writeTools {
		if _, ok := tools[name]; ok {
			t.Errorf("write tool %q should NOT be registered in read-only mode", name)
		}
	}
}

func TestReadOnly_PluginReloadNotRegistered(t *testing.T) {
	mgr := testManager()
	cfg := config.DefaultConfig()
	cfg.ReadOnly = true

	index := NewToolIndex(mgr, true)
	srv, _ := New("test", mgr, cfg, index, nil, nil, nil, nil, true)
	tools := srv.ListTools()

	if _, ok := tools["plugin_reload"]; ok {
		t.Error("plugin_reload should NOT be registered in read-only mode")
	}

	// plugin_list and tool_search should still be registered
	if _, ok := tools["plugin_list"]; !ok {
		t.Error("plugin_list should be registered in read-only mode")
	}
	if _, ok := tools["tool_search"]; !ok {
		t.Error("tool_search should be registered in read-only mode")
	}
}

func TestReadOnly_ToolIndexExcludesWriteTools(t *testing.T) {
	mgr := testManager()
	idx := NewToolIndex(mgr, true)

	// Read tools should be findable
	if _, ok := idx.Get("jira_search"); !ok {
		t.Error("jira_search should be in index")
	}

	// Write tools should NOT be in index
	if _, ok := idx.Get("jira_create_issue"); ok {
		t.Error("jira_create_issue should NOT be in read-only index")
	}
	if _, ok := idx.Get("gmail_send_message"); ok {
		t.Error("gmail_send_message should NOT be in read-only index")
	}
}

func TestReadOnly_ToolSearchExcludesWriteTools(t *testing.T) {
	mgr := testManager()
	idx := NewToolIndex(mgr, true)

	// Search for "create" — should find nothing (only write tools match)
	results := idx.Search("create", "", 10, false)
	for _, r := range results {
		if r.Access == "write" {
			t.Errorf("search returned write tool %q in read-only mode", r.Name)
		}
	}

	// Search for "search" — should find read tools
	results = idx.Search("search", "", 10, false)
	if len(results) == 0 {
		t.Error("expected read tools in search results")
	}
}

func TestReadOnly_DisabledPluginWriteToolsExcluded(t *testing.T) {
	mgr := plugin.NewManagerForTest()

	mgr.SetManifest("broken", &plugin.Manifest{
		Name: "broken",
		Tools: []plugin.ToolDef{
			{Name: "broken_read", Description: "Read tool", Access: "read"},
			{Name: "broken_write", Description: "Write tool", Access: "write"},
		},
	})
	mgr.SetDisabledPlugin("broken", "missing credentials")

	cfg := config.DefaultConfig()
	cfg.ReadOnly = true

	index := NewToolIndex(mgr, true)
	srv, _ := New("test", mgr, cfg, index, nil, nil, nil, nil, true)
	tools := srv.ListTools()

	// Disabled read tool should have [DISABLED] stub
	if _, ok := tools["broken_read"]; !ok {
		t.Error("disabled read tool should still be registered as stub")
	}

	// Disabled write tool should NOT be registered at all
	if _, ok := tools["broken_write"]; ok {
		t.Error("disabled write tool should NOT be registered in read-only mode")
	}
}

func TestReadOnly_RebuildPreservesFilter(t *testing.T) {
	mgr := testManager()
	idx := NewToolIndex(mgr, true)

	// Add a new plugin with write tools
	mgr.SetManifest("new-plugin", &plugin.Manifest{
		Name: "new-plugin",
		Tools: []plugin.ToolDef{
			{Name: "new_read", Description: "New read", Access: "read"},
			{Name: "new_write", Description: "New write", Access: "write"},
		},
	})
	mgr.SetHandle("new-plugin")

	idx.Rebuild(mgr)

	// Read tool from new plugin should be in index
	if _, ok := idx.Get("new_read"); !ok {
		t.Error("new_read should be in index after rebuild")
	}

	// Write tool from new plugin should NOT be in index
	if _, ok := idx.Get("new_write"); ok {
		t.Error("new_write should NOT be in read-only index after rebuild")
	}
}

func TestReadOnly_CategorySummaryOnlyCountsReadTools(t *testing.T) {
	mgr := testManager()
	idx := NewToolIndex(mgr, true)
	summary := idx.CategorySummary()

	// jira has 4 read tools (not 5 total)
	if strings.Contains(summary, "5 tools") {
		t.Errorf("summary should not count write tools, got:\n%s", summary)
	}

	// gmail has 1 read tool (not 3 total)
	if strings.Contains(summary, "3 tools") {
		t.Errorf("summary should not count write tools, got:\n%s", summary)
	}
}

func TestReadOnly_NormalModeUnaffected(t *testing.T) {
	mgr := testManager()
	cfg := config.DefaultConfig()

	index := NewToolIndex(mgr, false)
	srv, _ := New("test", mgr, cfg, index, nil, nil, nil, nil, true)
	tools := srv.ListTools()

	// All tools should be registered in normal mode
	allTools := []string{
		"jira_search", "jira_create_issue",
		"gmail_list_messages", "gmail_send_message",
		"plugin_list", "plugin_reload", "tool_search",
	}
	for _, name := range allTools {
		if _, ok := tools[name]; !ok {
			t.Errorf("tool %q should be registered in normal mode", name)
		}
	}
}
