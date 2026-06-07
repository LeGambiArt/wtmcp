package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/LeGambiArt/wtmcp/internal/plugin"
)

// testManager creates a Manager with in-memory manifests for testing.
// Does not start any plugin processes.
func testManager() *plugin.Manager {
	mgr := plugin.NewManagerForTest()

	mgr.SetManifest("jira", &plugin.Manifest{
		Name: "jira",
		Tools: []plugin.ToolDef{
			{Name: "jira_search", Description: "Search Jira issues using JQL", Access: "read", Visibility: "primary"},
			{Name: "jira_get_issues", Description: "Get one or more Jira issues by key", Access: "read", Visibility: "primary"},
			{Name: "jira_create_issue", Description: "Create a new Jira issue", Access: "write", Visibility: "primary"},
			{Name: "jira_export_sprint_data", Description: "Export sprint data to JSON file", Access: "read"},
			{Name: "jira_debug_fields", Description: "List Jira custom fields", Access: "read"},
		},
	})
	mgr.SetHandle("jira")

	mgr.SetManifest("gmail", &plugin.Manifest{
		Name: "gmail",
		Tools: []plugin.ToolDef{
			{Name: "gmail_list_messages", Description: "List Gmail messages matching a search query", Access: "read", Visibility: "primary"},
			{Name: "gmail_send_message", Description: "Send an email message", Access: "write", Visibility: "primary"},
			{Name: "gmail_modify_labels", Description: "Add or remove labels on a message", Access: "write"},
		},
	})
	mgr.SetHandle("gmail")

	return mgr
}

func TestToolIndexSearch_ExactName(t *testing.T) {
	idx := NewToolIndex(testManager(), false)
	results := idx.Search("jira_search", "", 10, false)
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	if results[0].Name != "jira_search" {
		t.Errorf("first result = %q, want jira_search", results[0].Name)
	}
}

func TestToolIndexSearch_PartialName(t *testing.T) {
	idx := NewToolIndex(testManager(), false)
	results := idx.Search("export", "", 10, false)
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	found := false
	for _, r := range results {
		if r.Name == "jira_export_sprint_data" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected jira_export_sprint_data in results")
	}
}

func TestToolIndexSearch_ByDescription(t *testing.T) {
	idx := NewToolIndex(testManager(), false)
	results := idx.Search("custom fields", "", 10, false)
	if len(results) == 0 {
		t.Fatal("expected results for description match")
	}
	found := false
	for _, r := range results {
		if r.Name == "jira_debug_fields" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected jira_debug_fields in results")
	}
}

func TestToolIndexSearch_PluginFilter(t *testing.T) {
	idx := NewToolIndex(testManager(), false)
	results := idx.Search("", "gmail", 50, false)
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3 (all gmail tools)", len(results))
	}
	for _, r := range results {
		if r.Plugin != "gmail" {
			t.Errorf("result %q has plugin %q, want gmail", r.Name, r.Plugin)
		}
	}
}

func TestToolIndexSearch_CombinedQueryAndFilter(t *testing.T) {
	idx := NewToolIndex(testManager(), false)
	results := idx.Search("send", "gmail", 10, false)
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	if results[0].Name != "gmail_send_message" {
		t.Errorf("first result = %q, want gmail_send_message", results[0].Name)
	}
}

func TestToolIndexSearch_MaxResults(t *testing.T) {
	idx := NewToolIndex(testManager(), false)
	results := idx.Search("jira", "", 2, false)
	if len(results) != 2 {
		t.Errorf("got %d results, want 2", len(results))
	}
}

func TestToolIndexSearch_MaxResultsClamped(t *testing.T) {
	idx := NewToolIndex(testManager(), false)
	results := idx.Search("jira", "", 1000, false)
	if len(results) > maxResultsCap {
		t.Errorf("got %d results, want at most %d", len(results), maxResultsCap)
	}
}

func TestToolIndexSearch_QueryTruncated(t *testing.T) {
	idx := NewToolIndex(testManager(), false)
	longQuery := strings.Repeat("jira ", 200) // way over 500 chars
	results := idx.Search(longQuery, "", 10, false)
	// Should not panic and should still return results
	if len(results) == 0 {
		t.Error("expected results even with long query")
	}
}

func TestToolIndexSearch_NoResults(t *testing.T) {
	idx := NewToolIndex(testManager(), false)
	results := idx.Search("nonexistent_xyz", "", 10, false)
	if len(results) != 0 {
		t.Errorf("expected empty results, got %d", len(results))
	}
}

func TestToolIndexSearch_EmptyQuery(t *testing.T) {
	idx := NewToolIndex(testManager(), false)
	results := idx.Search("", "", 10, false)
	if len(results) != 0 {
		t.Errorf("expected empty results for empty query, got %d", len(results))
	}
}

func TestToolIndexGet_Found(t *testing.T) {
	idx := NewToolIndex(testManager(), false)
	entry, ok := idx.Get("gmail_send_message")
	if !ok {
		t.Fatal("expected to find gmail_send_message")
	}
	if entry.Plugin != "gmail" {
		t.Errorf("plugin = %q, want gmail", entry.Plugin)
	}
}

func TestToolIndexGet_NotFound(t *testing.T) {
	idx := NewToolIndex(testManager(), false)
	_, ok := idx.Get("nonexistent")
	if ok {
		t.Error("expected not found")
	}
}

func TestToolIndexCategorySummary(t *testing.T) {
	idx := NewToolIndex(testManager(), false)
	summary := idx.CategorySummary()
	if !strings.Contains(summary, "jira") {
		t.Error("summary should contain 'jira'")
	}
	if !strings.Contains(summary, "gmail") {
		t.Error("summary should contain 'gmail'")
	}
	if !strings.Contains(summary, "5 tools") {
		t.Errorf("summary should show jira tool count, got:\n%s", summary)
	}
}

func TestToolIndexRebuild(t *testing.T) {
	mgr := testManager()
	idx := NewToolIndex(mgr, false)

	// Verify initial state
	_, ok := idx.Get("jira_search")
	if !ok {
		t.Fatal("expected jira_search before rebuild")
	}

	// Simulate reload: change the manifest
	mgr.SetManifest("jira", &plugin.Manifest{
		Name: "jira",
		Tools: []plugin.ToolDef{
			{Name: "jira_search_v2", Description: "New search", Access: "read"},
		},
	})

	idx.Rebuild(mgr)

	// Old tool should be gone
	_, ok = idx.Get("jira_search")
	if ok {
		t.Error("jira_search should not exist after rebuild")
	}

	// New tool should be present
	_, ok = idx.Get("jira_search_v2")
	if !ok {
		t.Error("jira_search_v2 should exist after rebuild")
	}
}

func TestToolEntryToSearchResult(t *testing.T) {
	entry := ToolEntry{
		Name:        "test_tool",
		Plugin:      "test",
		Description: "A test tool",
		Access:      "read",
		Visibility:  "primary",
		ParamNames:  []string{"query"},
		Def: plugin.ToolDef{
			Name:        "test_tool",
			Description: "A test tool",
			Access:      "read",
			Visibility:  "primary",
			Params: map[string]plugin.ParamDef{
				"query": {Type: "string", Required: true, Description: "Search query"},
			},
		},
	}

	sr := entry.toSearchResult()

	// Verify safe fields are present
	if sr.Name != "test_tool" {
		t.Errorf("Name = %q", sr.Name)
	}
	if sr.Plugin != "test" {
		t.Errorf("Plugin = %q", sr.Plugin)
	}
	if sr.Access != "read" {
		t.Errorf("Access = %q", sr.Access)
	}

	// Verify JSON only contains safe fields
	data, err := json.Marshal(sr)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}

	allowedKeys := map[string]bool{
		"name": true, "plugin": true, "description": true,
		"access": true, "params": true,
	}
	for key := range raw {
		if !allowedKeys[key] {
			t.Errorf("unexpected key in search result JSON: %q", key)
		}
	}
}

func TestToolIndexUnloadedPluginExcluded(t *testing.T) {
	mgr := plugin.NewManagerForTest()

	// Add manifest but do NOT set handle (simulates failed load)
	mgr.SetManifest("broken", &plugin.Manifest{
		Name: "broken",
		Tools: []plugin.ToolDef{
			{Name: "broken_tool", Description: "Should not appear"},
		},
	})

	idx := NewToolIndex(mgr, false)
	_, ok := idx.Get("broken_tool")
	if ok {
		t.Error("tool from unloaded plugin should not be in index")
	}
}
