package server

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/LeGambiArt/wtmcp/internal/plugin"
)

// Search bounds.
const (
	maxQueryLen    = 500
	maxQueryTerms  = 10
	maxResultsCap  = 50
	defaultResults = 10
)

// ToolEntry is a searchable record for one plugin tool.
// Contains only safe-to-expose fields — no manifest pointer,
// no resolved config, no credentials.
type ToolEntry struct {
	Name          string
	Plugin        string
	Description   string
	Access        string
	Visibility    string
	ParamNames    []string
	Def           plugin.ToolDef // safe: contains only YAML-level schema
	Disabled      bool
	DisableReason string
}

// searchResult is the safe subset returned to callers via JSON.
type searchResult struct {
	Name        string         `json:"name"`
	Plugin      string         `json:"plugin"`
	Description string         `json:"description"`
	Access      string         `json:"access"`
	Params      map[string]any `json:"params"`
}

// toSearchResult converts a ToolEntry to a safe serializable form.
func (e ToolEntry) toSearchResult() searchResult {
	return searchResult{
		Name:        e.Name,
		Plugin:      e.Plugin,
		Description: e.Description,
		Access:      e.Access,
		Params:      e.Def.ParamsSchema(),
	}
}

// ToolIndex holds all plugin tools for keyword search.
type ToolIndex struct {
	mu       sync.RWMutex
	entries  []ToolEntry
	readOnly bool
}

// NewToolIndex builds the index from loaded plugin manifests.
// When readOnly is true, write-access tools are excluded.
func NewToolIndex(mgr *plugin.Manager, readOnly bool) *ToolIndex {
	idx := &ToolIndex{readOnly: readOnly}
	idx.entries = buildEntries(mgr, readOnly)
	return idx
}

// Rebuild replaces all index entries by re-scanning the manager's
// loaded plugins. Thread-safe.
func (idx *ToolIndex) Rebuild(mgr *plugin.Manager) {
	newEntries := buildEntries(mgr, idx.readOnly)
	idx.mu.Lock()
	idx.entries = newEntries
	idx.mu.Unlock()
}

// Search finds tools matching query terms. Supports optional plugin
// filter. Query is capped at maxQueryLen characters and maxQueryTerms
// terms. maxResults is clamped to [1, maxResultsCap].
func (idx *ToolIndex) Search(query, pluginFilter string, limit int, excludeWrite bool) []ToolEntry {
	if len(query) > maxQueryLen {
		query = truncateUTF8(query, maxQueryLen)
	}

	terms := strings.Fields(strings.ToLower(query))
	if len(terms) > maxQueryTerms {
		terms = terms[:maxQueryTerms]
	}

	if limit <= 0 {
		limit = defaultResults
	}
	if limit > maxResultsCap {
		limit = maxResultsCap
	}

	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if len(terms) == 0 && pluginFilter == "" {
		return nil
	}

	type scored struct {
		entry ToolEntry
		score int
	}

	var results []scored
	for _, entry := range idx.entries {
		if pluginFilter != "" && entry.Plugin != pluginFilter {
			continue
		}
		if excludeWrite && entry.Access != "read" {
			continue
		}

		score := 0
		if len(terms) == 0 {
			// Plugin-only filter: include all tools from plugin.
			score = 1
		} else {
			nameLower := strings.ToLower(entry.Name)
			descLower := strings.ToLower(entry.Description)
			pluginLower := strings.ToLower(entry.Plugin)

			for _, term := range terms {
				if nameLower == term {
					score += 100
				} else if strings.Contains(nameLower, term) {
					score += 40
				}

				if strings.Contains(pluginLower, term) {
					score += 20
				}

				if strings.Contains(descLower, term) {
					score += 10
				}

				for _, p := range entry.ParamNames {
					if strings.Contains(strings.ToLower(p), term) {
						score += 5
						break
					}
				}
			}
		}

		if score > 0 {
			results = append(results, scored{entry: entry, score: score})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	if len(results) > limit {
		results = results[:limit]
	}

	entries := make([]ToolEntry, len(results))
	for i, r := range results {
		entries[i] = r.entry
	}
	return entries
}

// Get returns a single tool entry by exact name.
func (idx *ToolIndex) Get(name string) (ToolEntry, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	for _, entry := range idx.entries {
		if entry.Name == name {
			return entry, true
		}
	}
	return ToolEntry{}, false
}

// CategorySummary returns a compact per-plugin summary string for
// embedding in the tool_search description.
func (idx *ToolIndex) CategorySummary() string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	plugins := make(map[string][]string)
	var pluginOrder []string
	for _, entry := range idx.entries {
		if _, seen := plugins[entry.Plugin]; !seen {
			pluginOrder = append(pluginOrder, entry.Plugin)
		}
		plugins[entry.Plugin] = append(plugins[entry.Plugin], entry.Name)
	}

	// Track which plugins are disabled (any disabled tool marks the plugin).
	disabledPlugins := make(map[string]bool)
	for _, entry := range idx.entries {
		if entry.Disabled {
			disabledPlugins[entry.Plugin] = true
		}
	}

	var sb strings.Builder
	for _, p := range pluginOrder {
		tools := plugins[p]
		examples := tools
		if len(examples) > 3 {
			examples = examples[:3]
		}
		label := fmt.Sprintf("%d tools", len(tools))
		if disabledPlugins[p] {
			label += ", disabled"
		}
		fmt.Fprintf(&sb, "- %s (%s): %s",
			p, label, strings.Join(examples, ", "))
		if len(tools) > 3 {
			sb.WriteString(", ...")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// buildEntries creates index entries from loaded and disabled plugin manifests.
// When readOnly is true, write-access tools are excluded from the index.
func buildEntries(mgr *plugin.Manager, readOnly bool) []ToolEntry {
	loaded := mgr.LoadedPlugins()
	loadedSet := make(map[string]bool, len(loaded))
	for _, name := range loaded {
		loadedSet[name] = true
	}

	disabled := mgr.DisabledPlugins()

	var entries []ToolEntry
	for _, manifest := range mgr.Manifests() {
		dp, isDisabled := disabled[manifest.Name]
		if !loadedSet[manifest.Name] && !isDisabled {
			continue
		}

		for _, tool := range manifest.Tools {
			if readOnly && !tool.IsReadOnly() {
				continue
			}

			paramNames := make([]string, 0, len(tool.Params))
			for name := range tool.Params {
				paramNames = append(paramNames, name)
			}
			sort.Strings(paramNames)

			desc := tool.Description
			if manifest.IsUserPlugin {
				desc = "[user plugin] " + desc
			}
			entry := ToolEntry{
				Name:        tool.Name,
				Plugin:      manifest.Name,
				Description: desc,
				Access:      tool.Access,
				Visibility:  tool.Visibility,
				ParamNames:  paramNames,
				Def:         tool,
			}
			if isDisabled {
				entry.Disabled = true
				entry.DisableReason = dp.Reason
			}
			entries = append(entries, entry)
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Plugin != entries[j].Plugin {
			return entries[i].Plugin < entries[j].Plugin
		}
		return entries[i].Name < entries[j].Name
	})

	return entries
}
