package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/LeGambiArt/wtmcp/internal/profile"
)

// registerToolSearch adds the tool_search meta-tool for discovering
// tools by keyword. Useful in both full and progressive modes.
func registerToolSearch(srv *mcpserver.MCPServer, index *ToolIndex, excludeWrite bool) {
	description := "Search for available tools by keyword. Returns tool " +
		"names, descriptions, and parameter schemas. Found tools can be " +
		"called directly by name."

	// The category summary is a single global string baked into the tool
	// description, so it cannot be filtered per connection. When profiles
	// are active it would advertise tool names and counts a restricted
	// agent may not call, so omit it; agents still discover their allowed
	// tools via primary tools/list entries and keyword searches (whose
	// results are profile-filtered below). Without profiles there is
	// nothing to hide, so keep the summary as a discovery aid.
	if !index.ProfilesActive() {
		description += "\n\nAvailable tool categories:\n" + index.CategorySummary()
	}

	tool := mcp.NewTool("tool_search",
		mcp.WithDescription(description),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Search keywords (matches tool names, "+
				"descriptions, params)"),
		),
		mcp.WithString("plugin_name",
			mcp.Description("Filter results to a specific plugin "+
				"(e.g., 'jira', 'confluence')"),
		),
		mcp.WithNumber("max_results",
			mcp.Description("Maximum results to return (default: "+
				"10, max: 50)"),
		),
	)
	readOnly := true
	tool.Annotations.ReadOnlyHint = &readOnly

	srv.AddTool(tool,
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			query, _ := args["query"].(string)
			pluginFilter, _ := args["plugin_name"].(string)
			limit := 0
			if mr, ok := args["max_results"].(float64); ok {
				limit = int(mr)
			}

			results := index.Search(query, pluginFilter, limit, excludeWrite)

			// Filter results by the connection's profile so an agent does
			// not discover tools it cannot call. tool_search itself is
			// exempt, but its *results* are still filtered.
			if filter := profile.FilterFromContext(ctx); filter != nil {
				filtered := results[:0:0]
				for _, r := range results {
					if filter.IsAllowed(r.Plugin, r.Name) {
						filtered = append(filtered, r)
					}
				}
				results = filtered
			}

			out := make([]searchResult, len(results))
			for i, r := range results {
				out[i] = r.toSearchResult()
			}

			data, err := json.Marshal(out)
			if err != nil {
				return nil, fmt.Errorf("marshal search results: %w", err)
			}
			return sanitizedTextResult(string(data)), nil
		},
	)
}
