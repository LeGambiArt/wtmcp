package stats

import (
	"testing"
	"time"
)

func TestCollector_Record(t *testing.T) {
	c := NewCollector(CharsTokenizer{}, false)

	c.Record("get_issues", "jira", time.Now(), []byte(`{"project":"TEST"}`), `{"issues":[]}`, false)

	summary := c.Summary()
	if len(summary) != 1 {
		t.Fatalf("expected 1 tool summary, got %d", len(summary))
	}
	s := summary[0]
	if s.ToolName != "get_issues" {
		t.Errorf("ToolName = %q, want %q", s.ToolName, "get_issues")
	}
	if s.PluginName != "jira" {
		t.Errorf("PluginName = %q, want %q", s.PluginName, "jira")
	}
	if s.CallCount != 1 {
		t.Errorf("CallCount = %d, want 1", s.CallCount)
	}
	if s.ErrorCount != 0 {
		t.Errorf("ErrorCount = %d, want 0", s.ErrorCount)
	}
	if s.TotalInputTokens == 0 {
		t.Error("TotalInputTokens should be > 0")
	}
	if s.TotalOutputTokens == 0 {
		t.Error("TotalOutputTokens should be > 0")
	}
}

func TestCollector_Record_Error(t *testing.T) {
	c := NewCollector(CharsTokenizer{}, false)

	c.Record("create_issue", "jira", time.Now(), []byte(`{}`), "permission denied", true)

	summary := c.Summary()
	if len(summary) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summary))
	}
	if summary[0].ErrorCount != 1 {
		t.Errorf("ErrorCount = %d, want 1", summary[0].ErrorCount)
	}
}

func TestCollector_Record_RingBuffer(t *testing.T) {
	c := NewCollector(CharsTokenizer{}, false)

	// Fill beyond maxEntries to test ring buffer wrap.
	for i := 0; i < maxEntries+100; i++ {
		c.Record("tool", "plugin", time.Now(), nil, "ok", false)
	}

	summary := c.Summary()
	if len(summary) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summary))
	}
	if summary[0].CallCount != maxEntries+100 {
		t.Errorf("CallCount = %d, want %d", summary[0].CallCount, maxEntries+100)
	}
}

func TestCollector_Record_ClosedIsNoop(t *testing.T) {
	c := NewCollector(CharsTokenizer{}, false)

	c.closed.Store(true)
	c.Record("tool", "plugin", time.Now(), nil, "ok", false)

	if len(c.Summary()) != 0 {
		t.Error("Record should be no-op after close")
	}
}

func TestCollector_RecordSchema(t *testing.T) {
	c := NewCollector(CharsTokenizer{}, false)

	c.RecordSchema("get_issues", "jira", "Get Jira issues", []byte(`{"type":"object"}`))

	cost := c.SchemaCost(nil)
	if cost.TotalTools != 1 {
		t.Errorf("TotalTools = %d, want 1", cost.TotalTools)
	}
	if cost.TotalSchemaTokens == 0 {
		t.Error("TotalSchemaTokens should be > 0")
	}
	if len(cost.ByPlugin) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(cost.ByPlugin))
	}
	if cost.ByPlugin[0].Plugin != "jira" {
		t.Errorf("Plugin = %q, want %q", cost.ByPlugin[0].Plugin, "jira")
	}
}

func TestCollector_RecordSchema_OverwriteOnReload(t *testing.T) {
	c := NewCollector(CharsTokenizer{}, false)

	c.RecordSchema("get_issues", "jira", "v1 description", []byte(`{"v":1}`))
	c.RecordSchema("get_issues", "jira", "v2 description with more text", []byte(`{"v":2,"extra":"field"}`))

	cost := c.SchemaCost(nil)
	if cost.TotalTools != 1 {
		t.Errorf("TotalTools = %d, want 1 (should overwrite)", cost.TotalTools)
	}
}

func TestCollector_SchemaCost_Keep(t *testing.T) {
	c := NewCollector(CharsTokenizer{}, false)

	c.RecordSchema("get_issues", "jira", "desc", []byte(`{}`))
	c.RecordSchema("create_issue", "jira", "desc", []byte(`{}`))
	c.RecordSchema("list_agents", "keylime", "desc", []byte(`{}`))

	// Keep only jira/get_issues: jira's row and totals must exclude
	// create_issue, and keylime must drop out entirely.
	cost := c.SchemaCost(func(plugin, tool string) bool {
		return plugin == "jira" && tool == "get_issues"
	})
	if cost.TotalTools != 1 {
		t.Errorf("TotalTools = %d, want 1", cost.TotalTools)
	}
	if len(cost.ByPlugin) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(cost.ByPlugin))
	}
	if cost.ByPlugin[0].Plugin != "jira" || cost.ByPlugin[0].Tools != 1 {
		t.Errorf("jira: plugin=%q tools=%d, want jira,1", cost.ByPlugin[0].Plugin, cost.ByPlugin[0].Tools)
	}
}

func TestCollector_RemovePluginSchemas(t *testing.T) {
	c := NewCollector(CharsTokenizer{}, false)

	c.RecordSchema("get_issues", "jira", "desc", []byte(`{}`))
	c.RecordSchema("create_issue", "jira", "desc", []byte(`{}`))
	c.RecordSchema("list_agents", "keylime", "desc", []byte(`{}`))

	c.RemovePluginSchemas("jira")

	cost := c.SchemaCost(nil)
	if cost.TotalTools != 1 {
		t.Errorf("TotalTools = %d, want 1 after removing jira schemas", cost.TotalTools)
	}
	if cost.ByPlugin[0].Plugin != "keylime" {
		t.Errorf("remaining plugin = %q, want keylime", cost.ByPlugin[0].Plugin)
	}
}

func TestCollector_RecordResourceRead(t *testing.T) {
	c := NewCollector(CharsTokenizer{}, false)

	c.RecordResourceRead("wtmcp://jira/context/context.md", "jira", "context", "# Jira Plugin\nUse this to...")

	resources := c.ResourceSummary()
	if len(resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(resources))
	}
	r := resources[0]
	if r.URI != "wtmcp://jira/context/context.md" {
		t.Errorf("URI = %q", r.URI)
	}
	if r.ReadCount != 1 {
		t.Errorf("ReadCount = %d, want 1", r.ReadCount)
	}
	if r.ContentTokens == 0 {
		t.Error("ContentTokens should be > 0")
	}
}

func TestCollector_RecordResourceRead_Increments(t *testing.T) {
	c := NewCollector(CharsTokenizer{}, false)

	c.RecordResourceRead("uri://test", "plugin", "context", "content v1")
	c.RecordResourceRead("uri://test", "plugin", "context", "content v2 longer")

	resources := c.ResourceSummary()
	if len(resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(resources))
	}
	if resources[0].ReadCount != 2 {
		t.Errorf("ReadCount = %d, want 2", resources[0].ReadCount)
	}
}

func TestCollector_RemovePluginResources(t *testing.T) {
	c := NewCollector(CharsTokenizer{}, false)

	c.RecordResourceRead("uri://jira/1", "jira", "provided", "data")
	c.RecordResourceRead("uri://keylime/1", "keylime", "context", "data")

	c.RemovePluginResources("jira")

	resources := c.ResourceSummary()
	if len(resources) != 1 {
		t.Errorf("expected 1 resource after removal, got %d", len(resources))
	}
	if resources[0].PluginName != "keylime" {
		t.Errorf("remaining resource plugin = %q, want keylime", resources[0].PluginName)
	}
}

func TestCollector_PluginSummaries(t *testing.T) {
	c := NewCollector(CharsTokenizer{}, false)

	c.Record("get_issues", "jira", time.Now(), nil, "ok", false)
	c.Record("create_issue", "jira", time.Now(), nil, "ok", false)
	c.Record("list_agents", "keylime", time.Now(), nil, "ok", false)

	ps := c.PluginSummaries(nil)
	if len(ps) != 2 {
		t.Fatalf("expected 2 plugins, got %d", len(ps))
	}

	// Sorted by name: jira, keylime
	if ps[0].PluginName != "jira" || ps[0].CallCount != 2 || ps[0].ToolCount != 2 {
		t.Errorf("jira: calls=%d tools=%d, want 2,2", ps[0].CallCount, ps[0].ToolCount)
	}
	if ps[1].PluginName != "keylime" || ps[1].CallCount != 1 || ps[1].ToolCount != 1 {
		t.Errorf("keylime: calls=%d tools=%d, want 1,1", ps[1].CallCount, ps[1].ToolCount)
	}
}

func TestCollector_PluginSummaries_Keep(t *testing.T) {
	c := NewCollector(CharsTokenizer{}, false)

	c.Record("get_issues", "jira", time.Now(), nil, "ok", false)
	c.Record("create_issue", "jira", time.Now(), nil, "ok", false)
	c.Record("list_agents", "keylime", time.Now(), nil, "ok", false)

	// Allow only jira/get_issues: jira's totals must exclude create_issue and
	// keylime must drop out entirely since none of its tools are kept.
	ps := c.PluginSummaries(func(plugin, tool string) bool {
		return plugin == "jira" && tool == "get_issues"
	})
	if len(ps) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(ps))
	}
	if ps[0].PluginName != "jira" || ps[0].CallCount != 1 || ps[0].ToolCount != 1 {
		t.Errorf("jira: calls=%d tools=%d, want 1,1", ps[0].CallCount, ps[0].ToolCount)
	}
}

func TestCollector_TotalTokens(t *testing.T) {
	c := NewCollector(CharsTokenizer{}, false)

	c.Record("tool1", "p", time.Now(), []byte("input"), "output text here", false)
	c.Record("tool2", "p", time.Now(), []byte("more"), "more output", false)

	input, output := c.TotalTokens()
	if input == 0 {
		t.Error("total input tokens should be > 0")
	}
	if output == 0 {
		t.Error("total output tokens should be > 0")
	}
}

func TestCollector_Nil_Tokenizer_Defaults(t *testing.T) {
	c := NewCollector(nil, false)
	if c.TokenizerName() != "chars" {
		t.Errorf("default tokenizer should be chars, got %q", c.TokenizerName())
	}
}

func TestCollector_DailyBucketing_SameDay(t *testing.T) {
	c := NewCollector(CharsTokenizer{}, false)
	now := time.Now()

	c.Record("tool", "plugin", now, nil, "ok", false)
	c.Record("tool", "plugin", now, nil, "ok", false)

	result := c.SummaryForRange(now, now)
	if len(result) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result))
	}
	if result[0].CallCount != 2 {
		t.Errorf("CallCount = %d, want 2", result[0].CallCount)
	}
}

func TestCollector_DailyBucketing_DifferentDays(t *testing.T) {
	c := NewCollector(CharsTokenizer{}, false)
	day1 := time.Date(2026, 4, 1, 10, 0, 0, 0, time.Local)
	day2 := time.Date(2026, 4, 2, 10, 0, 0, 0, time.Local)

	c.Record("tool", "plugin", day1, nil, "ok", false)
	c.Record("tool", "plugin", day1, nil, "ok", false)
	c.Record("tool", "plugin", day2, nil, "ok", false)

	// Query day1 only.
	result := c.SummaryForRange(day1, day1)
	if len(result) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result))
	}
	if result[0].CallCount != 2 {
		t.Errorf("day1 CallCount = %d, want 2", result[0].CallCount)
	}

	// Query day2 only.
	result = c.SummaryForRange(day2, day2)
	if len(result) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result))
	}
	if result[0].CallCount != 1 {
		t.Errorf("day2 CallCount = %d, want 1", result[0].CallCount)
	}

	// Query both days.
	result = c.SummaryForRange(day1, day2)
	if result[0].CallCount != 3 {
		t.Errorf("both days CallCount = %d, want 3", result[0].CallCount)
	}
}

func TestCollector_DailyBucketing_EmptyRange(t *testing.T) {
	c := NewCollector(CharsTokenizer{}, false)
	day := time.Date(2026, 4, 1, 10, 0, 0, 0, time.Local)
	c.Record("tool", "plugin", day, nil, "ok", false)

	// Query a range with no data.
	other := time.Date(2026, 5, 1, 0, 0, 0, 0, time.Local)
	result := c.SummaryForRange(other, other)
	if len(result) != 0 {
		t.Errorf("expected 0 results for empty range, got %d", len(result))
	}
}

func TestCollector_DailyBucketing_InvariantMatchesAllTime(t *testing.T) {
	c := NewCollector(CharsTokenizer{}, false)
	day1 := time.Date(2026, 4, 1, 10, 0, 0, 0, time.Local)
	day2 := time.Date(2026, 4, 2, 10, 0, 0, 0, time.Local)
	day3 := time.Date(2026, 4, 3, 10, 0, 0, 0, time.Local)

	c.Record("tool_a", "p", day1, []byte("in"), "output1", false)
	c.Record("tool_a", "p", day2, []byte("in"), "output2", false)
	c.Record("tool_b", "p", day2, []byte("in"), "output3", true)
	c.Record("tool_a", "p", day3, []byte("in"), "output4", false)

	// All-time totals from Summary().
	allTime := c.Summary()
	allTimeCalls := 0
	for _, ts := range allTime {
		allTimeCalls += ts.CallCount
	}

	// Sum of all daily buckets via SummaryForRange (no bounds).
	daily := c.SummaryForRange(time.Time{}, time.Time{})
	dailyCalls := 0
	for _, ts := range daily {
		dailyCalls += ts.CallCount
	}

	if allTimeCalls != dailyCalls {
		t.Errorf("invariant broken: all-time calls=%d, daily sum=%d",
			allTimeCalls, dailyCalls)
	}
}

func TestAggregateDailyRange(t *testing.T) {
	daily := map[string]map[string]ToolSummary{
		"2026-04-01": {
			"tool_a": {ToolName: "tool_a", PluginName: "p", CallCount: 3, TotalInputTokens: 30, TotalOutputTokens: 60, TotalDurationMs: 900, MaxOutputTokens: 25},
		},
		"2026-04-02": {
			"tool_a": {ToolName: "tool_a", PluginName: "p", CallCount: 2, TotalInputTokens: 20, TotalOutputTokens: 40, TotalDurationMs: 400, MaxOutputTokens: 30},
			"tool_b": {ToolName: "tool_b", PluginName: "p", CallCount: 1, ErrorCount: 1, TotalDurationMs: 100},
		},
		"2026-04-03": {
			"tool_a": {ToolName: "tool_a", PluginName: "p", CallCount: 1, TotalInputTokens: 10, TotalOutputTokens: 20, TotalDurationMs: 200, MaxOutputTokens: 20},
		},
	}

	// Full range.
	result := AggregateDailyRange(daily, "", "")
	if len(result) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(result))
	}

	// tool_a: 3+2+1=6 calls, 30+20+10=60 input, max=30
	a := result[0] // sorted: tool_a before tool_b
	if a.CallCount != 6 {
		t.Errorf("tool_a CallCount = %d, want 6", a.CallCount)
	}
	if a.TotalInputTokens != 60 {
		t.Errorf("tool_a TotalInputTokens = %d, want 60", a.TotalInputTokens)
	}
	if a.MaxOutputTokens != 30 {
		t.Errorf("tool_a MaxOutputTokens = %d, want 30", a.MaxOutputTokens)
	}
	// Avg recomputed from totals: 1500ms / 6 = 250ms
	if a.AvgDurationMs != 250 {
		t.Errorf("tool_a AvgDurationMs = %d, want 250 (recomputed from totals)", a.AvgDurationMs)
	}

	// Filtered range: only 2026-04-02.
	result = AggregateDailyRange(daily, "2026-04-02", "2026-04-02")
	if len(result) != 2 {
		t.Fatalf("expected 2 tools for 04-02, got %d", len(result))
	}
	for _, ts := range result {
		if ts.ToolName == "tool_a" && ts.CallCount != 2 {
			t.Errorf("filtered tool_a CallCount = %d, want 2", ts.CallCount)
		}
		if ts.ToolName == "tool_b" && ts.CallCount != 1 {
			t.Errorf("filtered tool_b CallCount = %d, want 1", ts.CallCount)
		}
	}
}
