# SSSS - Stupid Simple Semantic Search

[![CI](https://github.com/yzhelezko/ssss-claude-plugin/actions/workflows/ci.yml/badge.svg)](https://github.com/yzhelezko/ssss-claude-plugin/actions/workflows/ci.yml)
[![Release](https://github.com/yzhelezko/ssss-claude-plugin/actions/workflows/release.yml/badge.svg)](https://github.com/yzhelezko/ssss-claude-plugin/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

A Claude Code plugin that provides **AI-powered semantic code search** using local Ollama embeddings. Find code by meaning, not just text - search for "authentication logic" and find relevant functions even if they don't contain those exact words.

## Features

- **Semantic Search**: Find code by meaning using AI embeddings
- **31+ Languages**: Go, Python, JavaScript, TypeScript, Rust, Java, C/C++, Ruby, and more
- **Usage Tracking**: See what calls what, find unused code, identify untested functions
- **Call Graph Analysis**: Explore 3 levels of callers to understand code relationships
- **Local Processing**: All embeddings generated locally via Ollama - your code never leaves your machine
- **Real-time Updates**: File watcher automatically re-indexes changed files
- **Web UI**: Visual interface at `http://localhost:9420` for browsing and searching
- **Multi-instance Support**: Automatically finds available ports when running multiple instances

## Quick Start

### Prerequisites

1. **Ollama** - Install from [ollama.ai](https://ollama.ai)
2. **Embedding Model** - Pull the default model:
   ```bash
   ollama pull qwen3-embedding:8b
   ```

### Installation

**Linux/macOS:**
```bash
curl -fsSL https://raw.githubusercontent.com/yzhelezko/ssss-claude-plugin/main/scripts/install.sh | bash
```

**Windows (PowerShell):**
```powershell
irm https://raw.githubusercontent.com/yzhelezko/ssss-claude-plugin/main/scripts/install.ps1 | iex
```

### Install Claude Code Plugin

After installing the binary, install the plugin in Claude Code:

**Step 1: Add the marketplace**
```
/plugin marketplace add yzhelezko/ssss-claude-plugin
```

**Step 2: Install the plugin**
```
/plugin install ssss@ssss-marketplace
```

### Install as Standalone MCP Server

You can also use SSSS as a standalone MCP server with Claude Desktop, Cursor, or any MCP-compatible client.

#### Option 1: Claude Desktop

Add to your Claude Desktop config file:

**macOS**: `~/Library/Application Support/Claude/claude_desktop_config.json`
**Windows**: `%APPDATA%\Claude\claude_desktop_config.json`
**Linux**: `~/.config/Claude/claude_desktop_config.json`

```json
{
  "mcpServers": {
    "ssss": {
      "command": "ssss",
      "env": {
        "MCP_OLLAMA_URL": "http://localhost:11434",
        "MCP_EMBEDDING_MODEL": "qwen3-embedding:8b",
        "MCP_AUTO_INDEX": "true",
        "MCP_WATCH_ENABLED": "true",
        "MCP_WEBUI_ENABLED": "true",
        "MCP_AUTO_OPEN_UI": "false"
      }
    }
  }
}
```

> **Note**: If `ssss` is not in your PATH, use the full path to the binary:
> - macOS/Linux: `"/usr/local/bin/ssss"` or `"~/.local/bin/ssss"`
> - Windows: `"C:\\Users\\YourName\\.ssss-claude-plugin\\ssss.exe"`

#### Option 2: Cursor IDE

Add to your Cursor MCP settings (Settings → MCP Servers):

```json
{
  "ssss": {
    "command": "ssss",
    "env": {
      "MCP_EMBEDDING_MODEL": "qwen3-embedding:8b"
    }
  }
}
```

#### Option 3: Manual/Stdio Mode

Run directly for any MCP client using stdio transport:

```bash
# Basic usage (uses defaults)
ssss

# With custom settings
MCP_EMBEDDING_MODEL=nomic-embed-text MCP_WEBUI_ENABLED=false ssss
```

The server communicates via stdin/stdout using the MCP protocol.

#### Available MCP Tools

When running as an MCP server, the following tool is exposed:

| Tool | Parameters | Description |
|------|------------|-------------|
| `search` | `query` (required), `limit` (optional) | Semantic code search with usage analysis |

**Example tool call:**
```json
{
  "name": "search",
  "arguments": {
    "query": "authentication middleware",
    "limit": 10
  }
}
```

**Response includes:**
- `results`: Array of matching code snippets with file path, lines, content
- `usage_graph`: Call relationships between found functions
- Each result has `calls`, `called_by`, `is_unused`, `not_tested`, `is_exported` flags

## Usage

### Slash Commands

| Command | Description |
|---------|-------------|
| `/ssss:search <query>` | Semantic search for code |
| `/ssss:unused` | Find potentially dead code |
| `/ssss:untested` | Find code without test coverage |
| `/ssss:callers <function>` | Show call hierarchy for a function |

### Examples

```
/ssss:search authentication middleware
/ssss:search error handling in database operations
/ssss:unused in the api package
/ssss:callers handleRequest
```

### Web UI

The Web UI is available at `http://localhost:9420` (opens automatically by default).

Features:
- Visual search interface
- Index new folders
- View usage analysis
- Real-time progress updates

## Configuration

Configure via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `MCP_OLLAMA_URL` | `http://localhost:11434` | Ollama API URL |
| `MCP_EMBEDDING_MODEL` | `qwen3-embedding:8b` | Embedding model name |
| `MCP_WEBUI_ENABLED` | `true` | Enable Web UI |
| `MCP_WEBUI_PORT` | `9420` | Web UI port |
| `MCP_AUTO_OPEN_UI` | `true` | Auto-open browser on start |
| `MCP_AUTO_INDEX` | `true` | Auto-index current folder |
| `MCP_WATCH_ENABLED` | `true` | Watch for file changes |
| `MCP_EMBEDDING_WORKERS` | `4` | Parallel embedding workers |
| `MCP_MAX_FILE_SIZE` | `1048576` | Max file size (1MB) |
| `MCP_DB_PATH` | `~/.ssss-claude-plugin/data` | Database location |

### Example Configuration

```bash
# Use a different embedding model
export MCP_EMBEDDING_MODEL="nomic-embed-text"

# Disable auto-open browser
export MCP_AUTO_OPEN_UI=false

# Use more workers for faster indexing
export MCP_EMBEDDING_WORKERS=8
```

## How It Works

### Indexing

1. **Scan**: Walks the directory tree, respecting `.gitignore`
2. **Parse**: Uses Tree-sitter for accurate AST parsing of 31+ languages
3. **Chunk**: Splits code into semantic chunks (functions, classes, methods)
4. **Embed**: Generates vector embeddings via Ollama
5. **Store**: Saves to local ChromemDB for fast retrieval

### Search

1. **Query Embedding**: Your search query is embedded using the same model
2. **Vector Search**: Finds semantically similar code chunks
3. **Usage Analysis**: Enriches results with call graph information
4. **Results**: Returns ranked matches with file paths, code, and usage data

### Usage Tracking

Each search result includes:
- **calls**: Functions this code calls
- **called_by**: Functions that call this code (up to 3 levels)
- **is_exported**: Whether it's a public API
- **is_unused**: No callers found (potential dead code)
- **not_tested**: Not called from any test file

## Supported Languages

| Category | Languages |
|----------|-----------|
| **Systems** | Go, Rust, C, C++, Zig |
| **JVM** | Java, Kotlin, Scala, Groovy |
| **Web** | JavaScript, TypeScript, HTML, CSS |
| **Scripting** | Python, Ruby, PHP, Perl, Lua |
| **Functional** | Haskell, OCaml, Elixir, Erlang, Elm, Clojure |
| **Mobile** | Swift, Dart |
| **Config** | JSON, YAML, TOML, HCL |
| **Other** | Bash, SQL, Markdown, Dockerfile |

## Architecture

```
ssss-claude-plugin/
├── .claude-plugin/      # Plugin manifest
├── commands/            # Slash commands
├── skills/              # Agent skills
├── .mcp.json           # MCP server config
├── config/             # Go: Configuration
├── indexer/            # Go: Scanning, parsing, embedding
├── store/              # Go: Vector database (ChromemDB)
├── tools/              # Go: MCP tool definitions
├── watcher/            # Go: File system watcher
├── webui/              # Go: Web UI server
└── main.go             # Entry point
```

## Building from Source

```bash
# Clone
git clone https://github.com/yzhelezko/ssss-claude-plugin.git
cd ssss-claude-plugin

# Build
go build -o ssss .

# Run
./ssss
```

## Contributing

Contributions welcome! Please:

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Run tests: `go test ./...`
5. Submit a pull request

## License

MIT License - see [LICENSE](LICENSE) for details.

## Acknowledgments

- [Ollama](https://ollama.ai) - Local LLM inference
- [Tree-sitter](https://tree-sitter.github.io) - Incremental parsing
- [ChromemDB](https://github.com/philippgille/chromem-go) - Vector database
- [MCP](https://github.com/mark3labs/mcp-go) - Model Context Protocol
