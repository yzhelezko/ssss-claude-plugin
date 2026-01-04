package types

import (
	"context"
	"fmt"
	"time"
)

// EmbeddingFunc is the function signature for generating embeddings
type EmbeddingFunc func(ctx context.Context, text string) ([]float32, error)

// FormatForEmbedding prepares text for embedding with context prefix
func FormatForEmbedding(language, chunkType, name, content string) string {
	// Add context to help the embedding model understand the content
	if name != "" {
		return fmt.Sprintf("%s %s: %s\n%s", language, chunkType, name, content)
	}
	return fmt.Sprintf("%s %s:\n%s", language, chunkType, content)
}

// Chunk represents a parsed code segment (function, class, method, or block)
type Chunk struct {
	ID        string            // Unique identifier: {project}:{file}:{index}
	Content   string            // The actual code content
	Type      ChunkType         // function, class, method, block
	Name      string            // Name of the function/class/method
	Language  string            // Programming language
	FilePath  string            // Relative path within project
	StartLine int               // Starting line number
	EndLine   int               // Ending line number
	Metadata  map[string]string // Additional metadata for filtering

	// Reference tracking for usage maps
	Calls      []string // Functions/methods this chunk calls
	References []string // Types/variables this chunk references
	IsExported bool     // Whether this symbol is public/exported
	IsTest     bool     // Whether this is in a test file
	Parent     string   // Parent symbol (e.g., class name for methods)
}

// ChunkType represents the type of code chunk
type ChunkType string

const (
	ChunkTypeFunction ChunkType = "function"
	ChunkTypeClass    ChunkType = "class"
	ChunkTypeMethod   ChunkType = "method"
	ChunkTypeBlock    ChunkType = "block"
	ChunkTypeFile     ChunkType = "file"
)

// FileInfo represents a file to be indexed
type FileInfo struct {
	Path         string    // Absolute path
	RelativePath string    // Path relative to project root
	Size         int64     // File size in bytes
	ModTime      time.Time // Last modification time
	Hash         string    // Content hash for change detection
	Language     string    // Detected programming language
}

// Project represents an indexed project
type Project struct {
	ID          string    `json:"id"`           // Unique hash of path
	Path        string    `json:"path"`         // Absolute path
	Name        string    `json:"name"`         // Project name (folder name)
	LastIndexed time.Time `json:"last_indexed"` // When last indexed
	FileCount   int       `json:"file_count"`   // Number of files indexed
	ChunkCount  int       `json:"chunk_count"`  // Number of chunks stored
	Status      string    `json:"status"`       // indexing, ready, error
	Watching    bool      `json:"watching"`     // File watcher active
	Error       string    `json:"error,omitempty"`
}

// SearchResult represents a single search result
type SearchResult struct {
	FilePath     string  `json:"file_path"`      // Relative file path (e.g., ./folder/file.go)
	AbsolutePath string  `json:"absolute_path"`  // Full absolute path to file
	ChunkType    string  `json:"chunk_type"`     // function, class, etc.
	Name         string  `json:"name"`           // Function/class name
	Lines        string  `json:"lines"`          // e.g., "45-78"
	Content      string  `json:"content"`        // The matching code
	Similarity   float32 `json:"similarity"`     // Cosine similarity score
	Language     string  `json:"language"`       // Programming language

	// Usage map information
	Usage *UsageInfo `json:"usage,omitempty"` // Usage information (callers, calls, etc.)
}

// UsageInfo contains information about how a symbol is used
type UsageInfo struct {
	Calls      []CallInfo   `json:"calls,omitempty"`       // Functions this symbol calls
	CalledBy   []CallerInfo `json:"called_by,omitempty"`   // Functions that call this symbol
	References []string     `json:"references,omitempty"`  // Types/variables referenced
	IsExported bool         `json:"is_exported"`           // Whether symbol is public
	IsTest     bool         `json:"is_test"`               // Whether in test file
	IsUnused   bool         `json:"is_unused"`             // Never called (and exported)
	NotTested  bool         `json:"not_tested"`            // Not called from any test
}

// CallInfo represents a function/method being called
type CallInfo struct {
	Name       string `json:"name"`                  // Function/method name
	FilePath   string `json:"file_path,omitempty"`   // File where callee is defined (if found)
	Line       int    `json:"line,omitempty"`        // Line number (if found)
	Language   string `json:"language,omitempty"`    // Programming language
	IsExternal bool   `json:"is_external,omitempty"` // True if not found in index (external/stdlib)
}

// CallerInfo represents a caller of a function
type CallerInfo struct {
	Name     string `json:"name"`                // Caller function name
	FilePath string `json:"file_path"`           // File where caller is defined
	Line     int    `json:"line"`                // Line number
	Language string `json:"language,omitempty"`  // Programming language
	IsTest   bool   `json:"is_test"`             // Whether caller is a test
	Parent   string `json:"parent,omitempty"`    // Parent class/struct (for methods)
}

// SearchResponse is the full response for a search query
type SearchResponse struct {
	Count   int             `json:"count"`             // Number of results
	Results []SearchResult  `json:"results"`           // Search results
	Graph   *UsageGraph     `json:"graph,omitempty"`   // Optional usage graph
}

// UsageGraph represents the call graph for search results
type UsageGraph struct {
	Nodes []GraphNode `json:"nodes"` // All symbols
	Edges []GraphEdge `json:"edges"` // Call relationships
}

// GraphNode represents a symbol in the usage graph
type GraphNode struct {
	ID         string `json:"id"`          // Symbol name
	Type       string `json:"type"`        // function, method, class
	FilePath   string `json:"file_path"`   // File location
	IsExported bool   `json:"is_exported"` // Public API
	IsTest     bool   `json:"is_test"`     // Test symbol
	IsUnused   bool   `json:"is_unused"`   // Never called
}

// GraphEdge represents a call relationship
type GraphEdge struct {
	From  string `json:"from"`  // Caller symbol
	To    string `json:"to"`    // Callee symbol
	Count int    `json:"count"` // Number of calls
}

// IndexResult represents the result of an indexing operation
type IndexResult struct {
	Status       string `json:"status"`
	Project      string `json:"project"`
	FilesIndexed int    `json:"files_indexed"`
	ChunksStored int    `json:"chunks_stored"`
	TimeTakenMs  int64  `json:"time_taken_ms"`
	Skipped      int    `json:"skipped,omitempty"`  // Files skipped (unchanged)
	Deleted      int    `json:"deleted,omitempty"`  // Files deleted
	Error        string `json:"error,omitempty"`
}

// StatusResult represents the overall status of the server
type StatusResult struct {
	Version        string `json:"version"`                  // Application version
	TotalChunks    int    `json:"total_chunks"`
	OllamaStatus   string `json:"ollama_status"`            // connected, disconnected
	DBPath         string `json:"db_path"`
	CurrentFolder  string `json:"current_folder,omitempty"` // Current working directory
}

// ScanResult represents the result of scanning a folder (before indexing)
type ScanResult struct {
	Path         string     `json:"path"`          // Absolute path scanned
	TotalFiles   int        `json:"total_files"`   // Total files found
	TotalSize    int64      `json:"total_size"`    // Total size in bytes
	Files        []FileInfo `json:"files"`         // List of files to index
	NewFiles     int        `json:"new_files"`     // Files not yet indexed
	ModifiedFiles int       `json:"modified_files"` // Files changed since last index
	UnchangedFiles int      `json:"unchanged_files"` // Files already indexed
	ByLanguage   map[string]int `json:"by_language"` // File count by language
}

// SearchOptions contains optional filters for search
type SearchOptions struct {
	Path          string  // Filter to subdirectory path
	Language      string  // Filter by programming language (e.g., "go", "python")
	ChunkType     string  // Filter by chunk type: "function", "class", "method", "all"
	CodeOnly      bool    // Exclude non-code files (JSON, YAML, MD, etc.)
	MinSimilarity float32 // Minimum similarity threshold (0.0-1.0)
	Limit         int     // Maximum results to return
}

// NonCodeLanguages lists languages that are typically config/docs, not code
var NonCodeLanguages = map[string]bool{
	"json":       true,
	"yaml":       true,
	"toml":       true,
	"markdown":   true,
	"xml":        true,
	"html":       true,
	"css":        true,
	"dockerfile": true,
	"plaintext":  true,
}

// ProgressEvent represents a progress update during indexing
type ProgressEvent struct {
	Type       string  `json:"type"`        // scanning, embedding, complete, error
	Project    string  `json:"project"`     // Project name
	Message    string  `json:"message"`     // Human readable message
	Current    int     `json:"current"`     // Current item number
	Total      int     `json:"total"`       // Total items
	Percent    float64 `json:"percent"`     // Percentage complete
	File       string  `json:"file"`        // Current file being processed
	Error      string  `json:"error,omitempty"` // Error message if any
}
