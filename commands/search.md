---
description: Semantic search for code - find functions, classes, and patterns using natural language
---

# Semantic Code Search

Use the SSSS MCP server to perform a semantic search for: "$ARGUMENTS"

Search the indexed codebase and return relevant results. For each result, show:
1. The file path and line numbers
2. The code snippet
3. Usage information (if available):
   - Whether it's exported/private
   - Functions that call it
   - Whether it has test coverage

Present results in a clear, organized format with the most relevant matches first.

## Available Filters

Use these parameters to narrow down search results:

| Parameter | Description | Example |
|-----------|-------------|---------|
| `path` | Filter to subdirectory | `"src/components"` |
| `language` | Filter by language | `"go"`, `"python"`, `"typescript"` |
| `type` | Filter by chunk type | `"function"`, `"class"`, `"method"` |
| `code_only` | Exclude non-code files | `true` (excludes JSON, YAML, MD, etc.) |
| `min_similarity` | Minimum match threshold | `0.5` (50% similarity) |
| `limit` | Max results (default: 5) | `10` |

## Examples

```
# Search only Go files
query: "error handling", language: "go"

# Search only functions in src folder
query: "authentication", path: "src", type: "function"

# High-confidence code results only
query: "database", code_only: true, min_similarity: 0.6
```
