package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"mcp-semantic-search/indexer"
	"mcp-semantic-search/types"

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
		mcp.WithString("path",
			mcp.Description("Filter results to this subdirectory path (e.g., 'src/components' or './lib'). Only returns results from files within this path."),
		),
		mcp.WithString("language",
			mcp.Description("Filter by programming language (e.g., 'go', 'python', 'javascript', 'typescript'). Case-insensitive."),
		),
		mcp.WithString("type",
			mcp.Description("Filter by chunk type: 'function', 'class', 'method', or 'all' (default: 'all')."),
		),
		mcp.WithBoolean("code_only",
			mcp.Description("Exclude non-code files like JSON, YAML, Markdown, HTML, CSS (default: true)."),
		),
		mcp.WithNumber("min_similarity",
			mcp.Description("Minimum similarity score threshold (0.0-1.0). Results below this score are filtered out."),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of results to return (default: 5, max: 50)"),
		),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError("query parameter is required"), nil
		}

		// Build search options from parameters
		opts := types.SearchOptions{
			Path:      req.GetString("path", ""),
			Language:  req.GetString("language", ""),
			ChunkType: req.GetString("type", ""),
			CodeOnly:  req.GetBool("code_only", true),
		}

		// Get min_similarity (0.0-1.0)
		if minSim := req.GetFloat("min_similarity", 0.0); minSim > 0 && minSim <= 1.0 {
			opts.MinSimilarity = float32(minSim)
		}

		// Get limit with new default of 5
		opts.Limit = req.GetInt("limit", 5)
		if opts.Limit > 50 {
			opts.Limit = 50
		}
		if opts.Limit < 1 {
			opts.Limit = 1
		}

		// Search with usage analysis
		response, err := idx.SearchWithUsage(ctx, query, opts)
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
