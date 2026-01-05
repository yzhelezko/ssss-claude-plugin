# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

SSSS (Stupid Simple Semantic Search) is a Claude Code plugin and MCP server that provides AI-powered semantic code search using local Ollama embeddings. It parses code with Tree-sitter, generates vector embeddings via Ollama, and stores them in ChromemDB for fast semantic retrieval.

## Build & Development Commands

```bash
# Build the binary
go build -o ssss .

# Build with version info
go build -ldflags "-X main.Version=1.2.3" -o ssss .

# Run tests
go test ./...

# Run a single test
go test -run TestName ./package/...

# Run the server (communicates via stdio MCP protocol)
./ssss
```

## Architecture

### Core Components

- **main.go**: Entry point - initializes config, embedder, store, indexer, watcher, and MCP server
- **config/**: Environment-based configuration (`MCP_*` env vars)
- **indexer/**: Core indexing pipeline
  - `scanner.go`: Directory traversal respecting .gitignore
  - `parser.go`: Tree-sitter AST parsing for 31+ languages
  - `chunker.go`: Splits code into semantic chunks (functions, classes, methods)
  - `embedder.go`: Ollama API client for generating embeddings
  - `indexer.go`: Orchestrates the indexing process with incremental support
- **store/**: Vector database layer
  - `store.go`: ChromemDB wrapper with a single global collection
  - `metadata.go`: File hash storage for incremental indexing
  - `caller_index.go`: Inverted index for O(1) caller lookups
- **tools/**: MCP tool definitions - single `search` tool with usage analysis
- **watcher/**: fsnotify-based file watcher for real-time re-indexing
- **webui/**: HTTP server for visual interface at localhost:9420
- **types/**: Shared type definitions (Chunk, SearchResult, UsageInfo, etc.)
- **updater/**: Self-update functionality from GitHub releases

### Data Flow

1. **Index**: Scanner → Parser (Tree-sitter) → Chunker → Embedder (Ollama) → Store (ChromemDB)
2. **Search**: Query → Embedder → ChromemDB similarity search → CallerIndex enrichment → Results with usage graph

### Plugin Structure

- `.claude-plugin/plugin.json`: Plugin manifest
- `commands/`: Slash command definitions (search, unused, untested, callers)
- `skills/`: Agent skills for proactive code exploration

## Key Patterns

- All file paths in the store use absolute paths for global uniqueness
- Search results return paths relative to current working directory
- CallerIndex provides 3-level deep call hierarchy lookups
- Incremental indexing via content hashing (SHA256)
- Version is injected at build time via ldflags
