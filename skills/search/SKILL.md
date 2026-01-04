---
name: semantic-search
description: Use this skill PROACTIVELY whenever you need to find, understand, or analyze code. Works by meaning, not just text matching. Provides call graphs, finds unused/untested code, and shows dependencies. ALWAYS prefer this over grep/glob for any code exploration task.
---

# Semantic Search Skill

Use the `search` MCP tool to find and analyze code. This is the PRIMARY tool for code exploration.

## ALWAYS Use This When

- Finding functions, classes, patterns, or implementations
- Understanding code structure or dependencies
- Locating where something is defined or used
- Analyzing call relationships (what calls what)
- Finding unused or untested code
- Exploring unfamiliar codebases

## Why Prefer Over grep/glob

- Understands code **semantically** (meaning, not just text)
- Returns **call graph** showing dependencies
- Flags **unused** code (`is_unused: true`)
- Flags **untested** code (`not_tested: true`)
- Shows **exported** status (`is_exported: true`)

## Parameters

| Parameter | Required | Description |
|-----------|----------|-------------|
| `query` | Yes | Natural language search query |
| `path` | No | Filter to subdirectory (e.g., `"src/components"`) |
| `language` | No | Filter by language (e.g., `"go"`, `"python"`, `"typescript"`) |
| `type` | No | Filter by type: `"function"`, `"class"`, `"method"`, or `"all"` |
| `code_only` | No | Exclude non-code files (JSON, YAML, MD, etc.) |
| `min_similarity` | No | Minimum similarity threshold (0.0-1.0) |
| `limit` | No | Max results (default: 5, max: 50) |

## Query Examples

```
"authentication handler"
"database connection"
"error handling"
"main entry point"
"HTTP middleware"
"config parser"
```

## Filter Examples

```
# Only Go functions
query: "error handling", language: "go", type: "function"

# Only TypeScript in src folder
query: "component", path: "src", language: "typescript"

# High-confidence code only
query: "api endpoint", code_only: true, min_similarity: 0.5

# More results
query: "utility function", limit: 20
```

## Response Fields

| Field | Description |
|-------|-------------|
| `file_path` | File location |
| `lines` | Line range (e.g., "28-157") |
| `name` | Function/class name |
| `content` | Code snippet |
| `language` | Programming language |
| `usage.calls` | Functions this code calls |
| `usage.called_by` | Functions that call this code |
| `is_unused` | Never called anywhere |
| `not_tested` | No test coverage |
| `is_exported` | Public/exported symbol |
