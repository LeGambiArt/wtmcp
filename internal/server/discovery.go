package server

import (
	"context"
	"encoding/json"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// registerToolSearch adds the tool_search meta-tool for discovering
// tools by keyword. Useful in both full and progressive modes.
func registerToolSearch(srv *mcpserver.MCPServer, index *ToolIndex, excludeWrite bool) {
	categorySummary := index.CategorySummary()

	tool := mcp.NewTool("tool_search",
		mcp.WithDescription(
			"Search for available tools by keyword. Returns tool "+
				"names, descriptions, and parameter schemas. Found "+
				"tools can be called directly by name.\n\n"+
				"Available tool categories:\n"+categorySummary,
		),
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
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			query, _ := args["query"].(string)
			pluginFilter, _ := args["plugin_name"].(string)
			limit := 0
			if mr, ok := args["max_results"].(float64); ok {
				limit = int(mr)
			}

			results := index.Search(query, pluginFilter, limit, excludeWrite)

			out := make([]searchResult, len(results))
			for i, r := range results {
				out[i] = r.toSearchResult()
			}

			data, _ := json.Marshal(out)
			return sanitizedTextResult(string(data)), nil
		},
	)
}
