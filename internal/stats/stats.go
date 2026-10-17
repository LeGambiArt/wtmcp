package stats

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// maxEntries is the ring buffer capacity for individual tool call entries.
const maxEntries = 1000

// Entry records a single tool call.
type Entry struct {
	ToolName     string    `json:"tool_name"`
	PluginName   string    `json:"plugin_name"`
	Timestamp    time.Time `json:"timestamp"`
	DurationMs   int64     `json:"duration_ms"`
	InputBytes   int       `json:"input_bytes"`
	OutputBytes  int       `json:"output_bytes"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	IsError      bool      `json:"is_error"`
}

// SchemaEntry records a tool's registration cost.
type SchemaEntry struct {
	ToolName     string `json:"tool_name"`
	PluginName   string `json:"plugin_name"`
	DescBytes    int    `json:"desc_bytes"`
	SchemaBytes  int    `json:"schema_bytes"`
	DescTokens   int    `json:"desc_tokens"`
	SchemaTokens int    `json:"schema_tokens"`
	TotalTokens  int    `json:"total_tokens"`
}

// ResourceEntry records a resource read.
type ResourceEntry struct {
	URI           string `json:"uri"`
	PluginName    string `json:"plugin_name"`
	ResourceType  string `json:"resource_type"`
	ContentBytes  int    `json:"content_bytes"`
	ContentTokens int    `json:"content_tokens"`
	ReadCount     int    `json:"read_count"`
}

// ToolSummary holds per-tool aggregated stats.
type ToolSummary struct {
	ToolName          string `json:"tool_name"`
	PluginName        string `json:"plugin_name"`
	CallCount         int    `json:"call_count"`
	ErrorCount        int    `json:"error_count"`
	TotalInputTokens  int    `json:"total_input_tokens"`
	TotalOutputTokens int    `json:"total_output_tokens"`
	AvgInputTokens    int    `json:"avg_input_tokens"`
	AvgOutputTokens   int    `json:"avg_output_tokens"`
	TotalDurationMs   int64  `json:"total_duration_ms"`
	AvgDurationMs     int64  `json:"avg_duration_ms"`
	MaxOutputTokens   int    `json:"max_output_tokens"`
}

// PluginSummary holds per-plugin aggregated stats.
type PluginSummary struct {
	PluginName        string `json:"plugin_name"`
	ToolCount         int    `json:"tool_count"`
	CallCount         int    `json:"call_count"`
	ErrorCount        int    `json:"error_count"`
	TotalInputTokens  int    `json:"total_input_tokens"`
	TotalOutputTokens int    `json:"total_output_tokens"`
	AvgDurationMs     int64  `json:"avg_duration_ms"`
}

// SchemaCostSummary aggregates schema token overhead.
type SchemaCostSummary struct {
	TotalTools        int                   `json:"total_tools"`
	TotalSchemaTokens int                   `json:"total_schema_tokens"`
	ByPlugin          []PluginSchemaSummary `json:"by_plugin"`
}

// PluginSchemaSummary holds schema costs for one plugin.
type PluginSchemaSummary struct {
	Plugin       string `json:"plugin"`
	Tools        int    `json:"tools"`
	SchemaTokens int    `json:"schema_tokens"`
}

// runningAggregate tracks cumulative stats for a single tool.
type runningAggregate struct {
	PluginName        string
	CallCount         int
	ErrorCount        int
	TotalInputTokens  int
	TotalOutputTokens int
	TotalDurationMs   int64
	MaxOutputTokens   int
}

func (a *runningAggregate) toSummary(toolName string) ToolSummary {
	s := ToolSummary{
		ToolName:          toolName,
		PluginName:        a.PluginName,
		CallCount:         a.CallCount,
		ErrorCount:        a.ErrorCount,
		TotalInputTokens:  a.TotalInputTokens,
		TotalOutputTokens: a.TotalOutputTokens,
		TotalDurationMs:   a.TotalDurationMs,
		MaxOutputTokens:   a.MaxOutputTokens,
	}
	if a.CallCount > 0 {
		s.AvgInputTokens = a.TotalInputTokens / a.CallCount
		s.AvgOutputTokens = a.TotalOutputTokens / a.CallCount
		s.AvgDurationMs = a.TotalDurationMs / int64(a.CallCount)
	}
	return s
}

// Collector accumulates tool call stats (thread-safe).
type Collector struct {
	mu              sync.Mutex
	closed          atomic.Bool
	entries         []Entry
	head            int
	full            bool
	aggregates      map[string]*runningAggregate
	dailyAggregates map[string]map[string]*runningAggregate // date -> tool -> aggregate
	schemas         map[string]SchemaEntry
	resources       map[string]*ResourceEntry
	tokenizer       Tokenizer
	logCalls        bool
	persistPath     string
	persistTmr      *time.Timer
	startedAt       *time.Time
	retentionDays   int
}

// NewCollector creates a Collector with the given tokenizer.
func NewCollector(tok Tokenizer, logCalls bool) *Collector {
	if tok == nil {
		tok = CharsTokenizer{}
	}
	return &Collector{
		entries:         make([]Entry, maxEntries),
		aggregates:      make(map[string]*runningAggregate),
		dailyAggregates: make(map[string]map[string]*runningAggregate),
		schemas:         make(map[string]SchemaEntry),
		resources:       make(map[string]*ResourceEntry),
		tokenizer:       tok,
		logCalls:        logCalls,
	}
}

// Record records a tool call. Raw inputRaw/outputText are used only
// for byte and token estimation — they must never be stored.
func (c *Collector) Record(
	toolName, pluginName string,
	start time.Time,
	inputRaw []byte,
	outputText string,
	isError bool,
) {
	if c.closed.Load() {
		return
	}

	// Compute metrics outside the lock.
	durationMs := time.Since(start).Milliseconds()
	inputBytes := len(inputRaw)
	outputBytes := len(outputText)
	inputTokens := c.tokenizer.CountBytes(inputRaw)
	outputTokens := c.tokenizer.Count(outputText)

	if c.logCalls {
		logToolCall(pluginName, toolName, durationMs, inputTokens, outputTokens, outputBytes)
	}

	entry := Entry{
		ToolName:     toolName,
		PluginName:   pluginName,
		Timestamp:    start,
		DurationMs:   durationMs,
		InputBytes:   inputBytes,
		OutputBytes:  outputBytes,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		IsError:      isError,
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Ring buffer append.
	c.entries[c.head] = entry
	c.head = (c.head + 1) % maxEntries
	if c.head == 0 {
		c.full = true
	}

	// Update running aggregate.
	agg, ok := c.aggregates[toolName]
	if !ok {
		agg = &runningAggregate{PluginName: pluginName}
		c.aggregates[toolName] = agg
	}
	agg.CallCount++
	if isError {
		agg.ErrorCount++
	}
	agg.TotalInputTokens += inputTokens
	agg.TotalOutputTokens += outputTokens
	agg.TotalDurationMs += durationMs
	if outputTokens > agg.MaxOutputTokens {
		agg.MaxOutputTokens = outputTokens
	}

	// Update daily bucket (local timezone).
	dateKey := start.Format("2006-01-02")
	dayAggs, ok := c.dailyAggregates[dateKey]
	if !ok {
		dayAggs = make(map[string]*runningAggregate)
		c.dailyAggregates[dateKey] = dayAggs
	}
	dagg, ok := dayAggs[toolName]
	if !ok {
		dagg = &runningAggregate{PluginName: pluginName}
		dayAggs[toolName] = dagg
	}
	dagg.CallCount++
	if isError {
		dagg.ErrorCount++
	}
	dagg.TotalInputTokens += inputTokens
	dagg.TotalOutputTokens += outputTokens
	dagg.TotalDurationMs += durationMs
	if outputTokens > dagg.MaxOutputTokens {
		dagg.MaxOutputTokens = outputTokens
	}

	c.scheduleSave()
}

// RecordSchema records a tool's schema token cost. Overwrites any
// existing entry for the same tool name (safe for reload).
func (c *Collector) RecordSchema(toolName, pluginName, description string, schemaJSON []byte) {
	if c.closed.Load() {
		return
	}

	descTokens := c.tokenizer.Count(description)
	schemaTokens := c.tokenizer.CountBytes(schemaJSON)
	nameTokens := c.tokenizer.Count(toolName)

	c.mu.Lock()
	defer c.mu.Unlock()

	c.schemas[toolName] = SchemaEntry{
		ToolName:     toolName,
		PluginName:   pluginName,
		DescBytes:    len(description),
		SchemaBytes:  len(schemaJSON),
		DescTokens:   descTokens,
		SchemaTokens: schemaTokens,
		TotalTokens:  descTokens + schemaTokens + nameTokens,
	}
}

// RecordResourceRead records a resource read. Content is used only
// for byte and token estimation — it must never be stored.
// On first read, creates the entry; on subsequent reads, increments
// ReadCount and updates ContentBytes/ContentTokens.
func (c *Collector) RecordResourceRead(uri, pluginName, resourceType, content string) {
	if c.closed.Load() {
		return
	}

	contentBytes := len(content)
	contentTokens := c.tokenizer.Count(content)

	c.mu.Lock()
	defer c.mu.Unlock()

	if r, ok := c.resources[uri]; ok {
		r.ReadCount++
		r.ContentBytes = contentBytes
		r.ContentTokens = contentTokens
	} else {
		c.resources[uri] = &ResourceEntry{
			URI:           uri,
			PluginName:    pluginName,
			ResourceType:  resourceType,
			ContentBytes:  contentBytes,
			ContentTokens: contentTokens,
			ReadCount:     1,
		}
	}

	c.scheduleSave()
}

// RemovePluginSchemas clears all schema entries for a plugin.
func (c *Collector) RemovePluginSchemas(pluginName string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for name, s := range c.schemas {
		if s.PluginName == pluginName {
			delete(c.schemas, name)
		}
	}
}

// RemovePluginResources clears all resource entries for a plugin.
func (c *Collector) RemovePluginResources(pluginName string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for uri, r := range c.resources {
		if r.PluginName == pluginName {
			delete(c.resources, uri)
		}
	}
}

// Summary returns per-tool aggregated stats sorted by tool name.
func (c *Collector) Summary() []ToolSummary {
	c.mu.Lock()
	defer c.mu.Unlock()

	result := make([]ToolSummary, 0, len(c.aggregates))
	for name, agg := range c.aggregates {
		result = append(result, agg.toSummary(name))
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].ToolName < result[j].ToolName
	})
	return result
}

// PluginSummaries returns per-plugin aggregated stats sorted by plugin name.
// If keep is non-nil, only tools for which keep(pluginName, toolName) reports
// true contribute to their plugin's totals, and plugins with no kept tools are
// omitted. A nil keep includes every tool.
func (c *Collector) PluginSummaries(keep func(pluginName, toolName string) bool) []PluginSummary {
	c.mu.Lock()
	defer c.mu.Unlock()

	byPlugin := make(map[string]*PluginSummary)
	toolCounts := make(map[string]map[string]bool)

	for toolName, agg := range c.aggregates {
		if keep != nil && !keep(agg.PluginName, toolName) {
			continue
		}
		ps, ok := byPlugin[agg.PluginName]
		if !ok {
			ps = &PluginSummary{PluginName: agg.PluginName}
			byPlugin[agg.PluginName] = ps
			toolCounts[agg.PluginName] = make(map[string]bool)
		}
		toolCounts[agg.PluginName][toolName] = true
		ps.CallCount += agg.CallCount
		ps.ErrorCount += agg.ErrorCount
		ps.TotalInputTokens += agg.TotalInputTokens
		ps.TotalOutputTokens += agg.TotalOutputTokens
		ps.AvgDurationMs += agg.TotalDurationMs
	}

	result := make([]PluginSummary, 0, len(byPlugin))
	for name, ps := range byPlugin {
		ps.ToolCount = len(toolCounts[name])
		if ps.CallCount > 0 {
			ps.AvgDurationMs /= int64(ps.CallCount)
		}
		result = append(result, *ps)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].PluginName < result[j].PluginName
	})
	return result
}

// SchemaCost returns the total token cost of registered tool schemas.
// If keep is non-nil, only tools for which keep(pluginName, toolName) reports
// true contribute to the totals and per-plugin rows, and plugins with no kept
// tools are omitted. A nil keep includes every tool.
func (c *Collector) SchemaCost(keep func(pluginName, toolName string) bool) SchemaCostSummary {
	c.mu.Lock()
	defer c.mu.Unlock()

	byPlugin := make(map[string]*PluginSchemaSummary)
	total := 0
	totalTools := 0

	for _, s := range c.schemas {
		if keep != nil && !keep(s.PluginName, s.ToolName) {
			continue
		}
		total += s.TotalTokens
		totalTools++
		ps, ok := byPlugin[s.PluginName]
		if !ok {
			ps = &PluginSchemaSummary{Plugin: s.PluginName}
			byPlugin[s.PluginName] = ps
		}
		ps.Tools++
		ps.SchemaTokens += s.TotalTokens
	}

	plugins := make([]PluginSchemaSummary, 0, len(byPlugin))
	for _, ps := range byPlugin {
		plugins = append(plugins, *ps)
	}
	sort.Slice(plugins, func(i, j int) bool {
		return plugins[i].Plugin < plugins[j].Plugin
	})

	return SchemaCostSummary{
		TotalTools:        totalTools,
		TotalSchemaTokens: total,
		ByPlugin:          plugins,
	}
}

// ResourceSummary returns per-resource read stats sorted by URI.
func (c *Collector) ResourceSummary() []ResourceEntry {
	c.mu.Lock()
	defer c.mu.Unlock()

	result := make([]ResourceEntry, 0, len(c.resources))
	for _, r := range c.resources {
		result = append(result, *r)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].URI < result[j].URI
	})
	return result
}

// TotalTokens returns grand total input + output tokens across all calls.
// If keep is non-nil, only tools for which keep(pluginName, toolName) reports
// true are counted. A nil keep includes every tool.
func (c *Collector) TotalTokens(keep func(pluginName, toolName string) bool) (input, output int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for toolName, agg := range c.aggregates {
		if keep != nil && !keep(agg.PluginName, toolName) {
			continue
		}
		input += agg.TotalInputTokens
		output += agg.TotalOutputTokens
	}
	return input, output
}

// TokenizerName returns the name of the configured tokenizer.
func (c *Collector) TokenizerName() string {
	return c.tokenizer.Name()
}

// SetRetentionDays sets the number of days of daily data to keep.
// 0 means no pruning. Must be >= 0.
func (c *Collector) SetRetentionDays(days int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.retentionDays = days
}

// AggregateDailyRange merges daily ToolSummary buckets for the given
// date range (inclusive, "2006-01-02" format). Empty since/until means
// no lower/upper bound. Returns sorted by tool name.
//
// This is a shared function used by both the Collector and the CLI
// to ensure identical merge logic. Averages are recomputed from
// summed totals (never averaged from daily averages).
func AggregateDailyRange(daily map[string]map[string]ToolSummary, since, until string) []ToolSummary {
	merged := make(map[string]*runningAggregate)

	for dateKey, toolMap := range daily {
		if since != "" && dateKey < since {
			continue
		}
		if until != "" && dateKey > until {
			continue
		}
		for toolName, ts := range toolMap {
			m, ok := merged[toolName]
			if !ok {
				m = &runningAggregate{PluginName: ts.PluginName}
				merged[toolName] = m
			}
			m.CallCount += ts.CallCount
			m.ErrorCount += ts.ErrorCount
			m.TotalInputTokens += ts.TotalInputTokens
			m.TotalOutputTokens += ts.TotalOutputTokens
			m.TotalDurationMs += ts.TotalDurationMs
			if ts.MaxOutputTokens > m.MaxOutputTokens {
				m.MaxOutputTokens = ts.MaxOutputTokens
			}
		}
	}

	result := make([]ToolSummary, 0, len(merged))
	for name, agg := range merged {
		result = append(result, agg.toSummary(name))
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].ToolName < result[j].ToolName
	})
	return result
}

// SummaryForRange returns per-tool stats aggregated across the given
// date range (inclusive). Delegates to AggregateDailyRange.
func (c *Collector) SummaryForRange(since, until time.Time) []ToolSummary {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Convert internal dailyAggregates to ToolSummary maps.
	daily := make(map[string]map[string]ToolSummary, len(c.dailyAggregates))
	for dateKey, dayAggs := range c.dailyAggregates {
		toolSummaries := make(map[string]ToolSummary, len(dayAggs))
		for toolName, agg := range dayAggs {
			toolSummaries[toolName] = agg.toSummary(toolName)
		}
		daily[dateKey] = toolSummaries
	}

	sinceStr := ""
	untilStr := ""
	if !since.IsZero() {
		sinceStr = since.Format("2006-01-02")
	}
	if !until.IsZero() {
		untilStr = until.Format("2006-01-02")
	}

	return AggregateDailyRange(daily, sinceStr, untilStr)
}

// StartedAt returns when stats accumulation began (nil if unknown).
func (c *Collector) StartedAt() *time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.startedAt
}
