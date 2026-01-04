package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"mcp-semantic-search/indexer"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegisterTools registers all MCP tools with the server
func RegisterTools(s *server.MCPServer, idx *indexer.Indexer) {
	registerSearch(s, idx)
}

// registerSearch registers the search tool - the main (and only) tool
func registerSearch(s *server.MCPServer, idx *indexer.Indexer) {
	tool := mcp.NewTool("search",
		mcp.WithDescription(`Semantic code search with usage analysis.

IMPORTANT: Use this tool PROACTIVELY whenever you need to:
- Find code, functions, classes, or patterns in the codebase
- Understand how code works or is structured
- Locate implementations, definitions, or usages
- Analyze dependencies or call relationships
- Find unused or untested code

DO NOT rely only on grep/glob - this tool understands code semantically and provides richer context including call graphs and usage analysis.

Returns code snippets matching the query semantically, with:
- File path, line numbers, function/class name, code content
- Usage map: what functions it calls, what calls it (3 levels deep)
- Flags: is_unused (never called), not_tested (no test coverage), is_exported

Use natural language queries like:
- "function that handles authentication"
- "error handling for API calls"
- "unused functions"
- "database connection"`),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Natural language search query"),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of results to return (default: 10, max: 50)"),
		),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError("query parameter is required"), nil
		}

		limit := req.GetInt("limit", 10)
		if limit > 50 {
			limit = 50
		}
		if limit < 1 {
			limit = 1
		}

		// Search with usage analysis
		response, err := idx.SearchWithUsage(ctx, query, "", limit)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Search failed: %v", err)), nil
		}

		if response.Count == 0 {
			return mcp.NewToolResultText("No matching results found. Make sure you have indexed projects first."), nil
		}

		// Return full response with usage info and graph
		return toolResultJSON(response)
	})
}

// toolResultJSON converts a value to JSON and returns as MCP tool result
func toolResultJSON(v interface{}) (*mcp.CallToolResult, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("JSON encoding failed: %v", err)), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}
