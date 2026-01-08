package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds all configuration for the MCP semantic search server
type Config struct {
	// Database settings
	DBPath string // Path to store SQLite database and metadata

	// Ollama settings
	OllamaURL      string // Ollama API URL (e.g., http://localhost:11434)
	EmbeddingModel string // Embedding model name (e.g., qwen3-embedding:8b)

	// Web UI settings
	WebUIEnabled bool // Enable web UI HTTP server
	WebUIPort    int  // Port for web UI server
	AutoOpenUI   bool // Auto-open browser when server starts
	MaxPortRetry int  // Max ports to try if default is busy

	// Indexing settings
	AutoIndex        bool  // Auto-index current folder on startup
	WatchEnabled     bool  // Enable file watching for auto-updates
	DebounceMs       int   // Debounce delay for file watcher in ms
	MaxFileSize      int64 // Maximum file size to index in bytes
	MaxChunkSize     int   // Maximum chunk size for line-based fallback
	ChunkOverlap     int   // Overlap lines for line-based chunking
	EmbeddingWorkers int   // Number of parallel embedding workers (1-8)

	// File filtering
	ExcludeDirs []string // Directories to always exclude
	ExcludeExts []string // File extensions to exclude (binary files)
	IncludeExts []string // If set, only include these extensions

	// Auto-update settings
	AutoUpdateEnabled bool // Enable automatic update checking
	AutoUpdateApply   bool // Automatically apply updates (requires restart)
}

// DefaultConfig returns the default configuration
func DefaultConfig() *Config {
	homeDir, _ := os.UserHomeDir()
	dbPath := filepath.Join(homeDir, ".ssss-claude-plugin")

	return &Config{
		DBPath:           dbPath,
		OllamaURL:        "http://localhost:11434",
		EmbeddingModel:   "qwen3-embedding:8b",
		WebUIEnabled:     true,
		WebUIPort:        9420,
		AutoOpenUI:       true, // Auto-open browser by default
		MaxPortRetry:     10,   // Try up to 10 ports if busy
		AutoIndex:        true, // Auto-index current folder by default
		WatchEnabled:     true,
		DebounceMs:       500,
		MaxFileSize:      1024 * 1024, // 1MB
		MaxChunkSize:     500,         // 500 lines per chunk
		ChunkOverlap:     20,          // 20 lines overlap
		EmbeddingWorkers: 4,           // 4 parallel embedding workers

		ExcludeDirs: []string{
			".git",
			".hg",
			".svn",
			"node_modules",
			"vendor",
			"__pycache__",
			".venv",
			"venv",
			".idea",
			".vscode",
			"dist",
			"build",
			"target",
			".next",
			".nuxt",
			"coverage",
			".pytest_cache",
			".mypy_cache",
		},

		ExcludeExts: []string{
			// Binary/compiled
			".exe", ".dll", ".so", ".dylib", ".a", ".o", ".obj",
			".pyc", ".pyo", ".class", ".jar", ".war",
			// Archives
			".zip", ".tar", ".gz", ".bz2", ".7z", ".rar",
			// Images
			".png", ".jpg", ".jpeg", ".gif", ".bmp", ".ico", ".svg", ".webp",
			// Audio/Video
			".mp3", ".mp4", ".wav", ".avi", ".mov", ".mkv",
			// Documents (usually binary)
			".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx",
			// Databases
			".db", ".sqlite", ".sqlite3",
			// Lock files
			".lock",
			// Other binary
			".wasm", ".bin", ".dat",
		},

		IncludeExts: []string{}, // Empty means include all text files

		AutoUpdateEnabled: true, // Check for updates by default
		AutoUpdateApply:   true, // Auto-apply updates by default
	}
}

// LoadFromEnv loads configuration from environment variables
func LoadFromEnv() *Config {
	cfg := DefaultConfig()

	if v := os.Getenv("MCP_DB_PATH"); v != "" {
		cfg.DBPath = expandPath(v)
	}

	if v := os.Getenv("MCP_OLLAMA_URL"); v != "" {
		cfg.OllamaURL = v
	}

	if v := os.Getenv("MCP_EMBEDDING_MODEL"); v != "" {
		cfg.EmbeddingModel = v
	}

	if v := os.Getenv("MCP_WATCH_ENABLED"); v != "" {
		cfg.WatchEnabled = strings.ToLower(v) == "true" || v == "1"
	}

	if v := os.Getenv("MCP_DEBOUNCE_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil {
			cfg.DebounceMs = ms
		}
	}

	if v := os.Getenv("MCP_MAX_FILE_SIZE"); v != "" {
		if size, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.MaxFileSize = size
		}
	}

	if v := os.Getenv("MCP_WEBUI_ENABLED"); v != "" {
		cfg.WebUIEnabled = strings.ToLower(v) == "true" || v == "1"
	}

	if v := os.Getenv("MCP_WEBUI_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			cfg.WebUIPort = port
		}
	}

	if v := os.Getenv("MCP_AUTO_OPEN_UI"); v != "" {
		cfg.AutoOpenUI = strings.ToLower(v) == "true" || v == "1"
	}

	if v := os.Getenv("MCP_MAX_PORT_RETRY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.MaxPortRetry = n
		}
	}

	if v := os.Getenv("MCP_AUTO_INDEX"); v != "" {
		cfg.AutoIndex = strings.ToLower(v) == "true" || v == "1"
	}

	if v := os.Getenv("MCP_EMBEDDING_WORKERS"); v != "" {
		if workers, err := strconv.Atoi(v); err == nil {
			if workers < 1 {
				workers = 1
			}
			if workers > 8 {
				workers = 8
			}
			cfg.EmbeddingWorkers = workers
		}
	}

	if v := os.Getenv("MCP_AUTO_UPDATE"); v != "" {
		cfg.AutoUpdateEnabled = strings.ToLower(v) == "true" || v == "1"
	}

	if v := os.Getenv("MCP_AUTO_UPDATE_APPLY"); v != "" {
		cfg.AutoUpdateApply = strings.ToLower(v) == "true" || v == "1"
	}

	return cfg
}

// expandPath expands ~ to home directory
func expandPath(path string) string {
	if strings.HasPrefix(path, "~") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[1:])
	}
	return path
}

// SQLitePath returns the path for SQLite vector database
func (c *Config) SQLitePath() string {
	return filepath.Join(c.DBPath, "vectors.db")
}

// MetadataPath returns the path for metadata file
func (c *Config) MetadataPath() string {
	return filepath.Join(c.DBPath, "projects.json")
}

// IsExcludedDir checks if a directory should be excluded
func (c *Config) IsExcludedDir(name string) bool {
	for _, excluded := range c.ExcludeDirs {
		if name == excluded {
			return true
		}
	}
	return false
}

// IsExcludedExt checks if a file extension should be excluded
func (c *Config) IsExcludedExt(ext string) bool {
	ext = strings.ToLower(ext)
	for _, excluded := range c.ExcludeExts {
		if ext == excluded {
			return true
		}
	}
	return false
}

// ShouldIncludeExt checks if a file extension should be included
func (c *Config) ShouldIncludeExt(ext string) bool {
	if len(c.IncludeExts) == 0 {
		return true // Include all if no whitelist
	}
	ext = strings.ToLower(ext)
	for _, included := range c.IncludeExts {
		if ext == included {
			return true
		}
	}
	return false
}
