package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
- Find who calls a function (callers analysis)
- Find who uses a type/struct/class (type reference analysis)
- Find unused or untested code

DO NOT rely only on grep/glob - this tool understands code semantically and provides richer context including caller and type usage analysis.

Returns code snippets matching the query semantically, with:
- File path, line numbers, function/class name, code content
- Called By: functions that call this symbol (3 levels deep)
- Used By: types/functions that reference this type (for structs, classes, interfaces)
- Flags: is_unused (never called/used), not_tested (no test coverage), is_exported

Use natural language queries like:
- "function that handles authentication"
- "error handling for API calls"
- "unused functions"
- "database connection"
- "type UsageInfo" (to see what uses a type)`),
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

		// Return plain text response for AI consumption
		return mcp.NewToolResultText(formatTextResponse(response)), nil
	})
}

// formatTextResponse formats search results as plain text for AI consumption
func formatTextResponse(resp *types.SearchResponse) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Found %d results:\n", resp.Count))

	for i, r := range resp.Results {
		// Header: name (type) file:lines [flags]
		flags := formatFlags(r.Usage)
		sb.WriteString(fmt.Sprintf("\n%d. %s (%s) %s:%s%s\n",
			i+1, r.Name, r.ChunkType, r.FilePath, r.Lines, flags))

		// Called by (for functions)
		if r.Usage != nil && len(r.Usage.CalledBy) > 0 {
			items := make([]string, 0, len(r.Usage.CalledBy))
			for _, c := range r.Usage.CalledBy {
				items = append(items, formatCallerCompact(c))
			}
			sb.WriteString(fmt.Sprintf("   Called by: %s\n", strings.Join(items, ", ")))
		}

		// Used by (for types)
		if r.Usage != nil && len(r.Usage.ReferencedBy) > 0 {
			items := make([]string, 0, len(r.Usage.ReferencedBy))
			for _, c := range r.Usage.ReferencedBy {
				items = append(items, formatCallerCompact(c))
			}
			sb.WriteString(fmt.Sprintf("   Used by: %s\n", strings.Join(items, ", ")))
		}

		// Code content (indented)
		sb.WriteString("   ```\n")
		for _, line := range strings.Split(r.Content, "\n") {
			sb.WriteString("   " + line + "\n")
		}
		sb.WriteString("   ```\n")
	}

	return sb.String()
}

// formatCallerCompact formats a caller/referencer as "Name (type, file:line)"
func formatCallerCompact(c types.CallerInfo) string {
	// Extract just filename from path
	file := c.FilePath
	if idx := strings.LastIndex(file, "/"); idx >= 0 {
		file = file[idx+1:]
	}
	if idx := strings.LastIndex(file, "\\"); idx >= 0 {
		file = file[idx+1:]
	}

	// Build compact format: Name (type, file:line)
	chunkType := c.Type
	if chunkType == "" {
		chunkType = "func"
	}
	return fmt.Sprintf("%s (%s, %s:%d)", c.Name, chunkType, file, c.Line)
}

// formatFlags formats usage flags as a bracketed string
func formatFlags(usage *types.UsageInfo) string {
	if usage == nil {
		return ""
	}

	var flags []string
	if usage.IsExported {
		flags = append(flags, "exported")
	}
	if usage.IsUnused {
		flags = append(flags, "UNUSED")
	}
	if usage.NotTested {
		flags = append(flags, "no-tests")
	}
	if usage.IsTest {
		flags = append(flags, "test")
	}

	if len(flags) == 0 {
		return ""
	}
	return " [" + strings.Join(flags, ", ") + "]"
}

// Suppress unused import warning
var _ = json.Marshal
