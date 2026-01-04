package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"mcp-semantic-search/config"
	"mcp-semantic-search/types"

	"github.com/philippgille/chromem-go"
)

// Store manages the vector database with a single global collection
type Store struct {
	db            *chromem.DB
	embeddingFunc types.EmbeddingFunc
	cfg           *config.Config
	collection    *chromem.Collection
	mu            sync.RWMutex
}

const globalCollectionName = "global_index"

// NewStore creates a new Store instance with persistent chromem database
func NewStore(cfg *config.Config, embeddingFunc types.EmbeddingFunc) (*Store, error) {
	// Ensure database directory exists
	if err := os.MkdirAll(cfg.ChromemPath(), 0755); err != nil {
		return nil, fmt.Errorf("failed to create db directory: %w", err)
	}

	// Create persistent database with compression
	db, err := chromem.NewPersistentDB(cfg.ChromemPath(), true)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize chromem: %w", err)
	}

	store := &Store{
		db:            db,
		embeddingFunc: embeddingFunc,
		cfg:           cfg,
	}

	// Get or create the global collection
	embFunc := store.chromemEmbeddingFunc()
	collection := db.GetCollection(globalCollectionName, embFunc)
	if collection == nil {
		collection, err = db.CreateCollection(globalCollectionName, nil, embFunc)
		if err != nil {
			return nil, fmt.Errorf("failed to create global collection: %w", err)
		}
	}
	store.collection = collection

	return store, nil
}

// chromemEmbeddingFunc wraps the embedding function for chromem-go compatibility
func (s *Store) chromemEmbeddingFunc() func(ctx context.Context, text string) ([]float32, error) {
	return func(ctx context.Context, text string) ([]float32, error) {
		return s.embeddingFunc(ctx, text)
	}
}

// AddChunks adds chunks to the global collection
func (s *Store) AddChunks(ctx context.Context, chunks []types.Chunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	docs := make([]chromem.Document, len(chunks))
	for i, chunk := range chunks {
		// Format content for better embedding
		embeddingText := types.FormatForEmbedding(
			chunk.Language,
			string(chunk.Type),
			chunk.Name,
			chunk.Content,
		)

		docs[i] = chromem.Document{
			ID:      chunk.ID,
			Content: embeddingText,
			Metadata: map[string]string{
				"absolute_path": chunk.FilePath, // Store absolute path
				"chunk_type":    string(chunk.Type),
				"name":          chunk.Name,
				"language":      chunk.Language,
				"start_line":    fmt.Sprintf("%d", chunk.StartLine),
				"end_line":      fmt.Sprintf("%d", chunk.EndLine),
				"raw":           chunk.Content, // Store original content
				// Reference tracking metadata
				"calls":       strings.Join(chunk.Calls, ","),
				"references":  strings.Join(chunk.References, ","),
				"is_exported": fmt.Sprintf("%t", chunk.IsExported),
				"is_test":     fmt.Sprintf("%t", chunk.IsTest),
				"parent":      chunk.Parent,
			},
		}
	}

	// Add documents with parallel embedding
	workers := s.cfg.EmbeddingWorkers
	if workers < 1 {
		workers = 4
	}
	if err := s.collection.AddDocuments(ctx, docs, workers); err != nil {
		return fmt.Errorf("failed to add documents: %w", err)
	}

	return nil
}

// Search performs semantic search across the global collection
// cwd: Current working directory for computing relative paths
// opts: Search options including path filter, language filter, etc.
// Returns paths relative to cwd, filtered according to options
func (s *Store) Search(ctx context.Context, query string, cwd string, opts types.SearchOptions) ([]types.SearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	limit := opts.Limit
	if limit <= 0 {
		limit = 5
	}

	// Query more results than needed since we'll filter
	queryLimit := limit * 5
	if queryLimit < 50 {
		queryLimit = 50
	}

	chromemResults, err := s.collection.Query(ctx, query, queryLimit, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	// Resolve filterPath to absolute if provided
	var absFilterPath string
	if opts.Path != "" {
		// Handle relative paths - resolve from cwd
		if !filepath.IsAbs(opts.Path) {
			absFilterPath = filepath.Join(cwd, opts.Path)
		} else {
			absFilterPath = opts.Path
		}
		absFilterPath = filepath.Clean(absFilterPath)
	}

	// Normalize language filter to lowercase
	languageFilter := strings.ToLower(opts.Language)

	// Normalize chunk type filter to lowercase
	chunkTypeFilter := strings.ToLower(opts.ChunkType)

	results := make([]types.SearchResult, 0, limit)
	for _, r := range chromemResults {
		if len(results) >= limit {
			break
		}

		// Apply minimum similarity filter
		if opts.MinSimilarity > 0 && r.Similarity < opts.MinSimilarity {
			continue
		}

		absolutePath := r.Metadata["absolute_path"]
		language := r.Metadata["language"]
		chunkType := r.Metadata["chunk_type"]

		// Apply language filter
		if languageFilter != "" && strings.ToLower(language) != languageFilter {
			continue
		}

		// Apply code_only filter (exclude non-code files)
		if opts.CodeOnly && types.NonCodeLanguages[strings.ToLower(language)] {
			continue
		}

		// Apply chunk type filter
		if chunkTypeFilter != "" && chunkTypeFilter != "all" {
			if strings.ToLower(chunkType) != chunkTypeFilter {
				continue
			}
		}

		// Apply path filter if specified
		if absFilterPath != "" {
			cleanAbsPath := filepath.Clean(absolutePath)
			// Check if file is within the filter path
			if !strings.HasPrefix(cleanAbsPath, absFilterPath) {
				continue
			}
			// Ensure it's a proper directory prefix (not partial match)
			if len(cleanAbsPath) > len(absFilterPath) && cleanAbsPath[len(absFilterPath)] != filepath.Separator {
				continue
			}
		}

		// Convert to relative path from cwd
		relativePath := absolutePath
		if cwd != "" {
			rel, err := filepath.Rel(cwd, absolutePath)
			if err != nil {
				continue // Skip if can't compute relative path
			}

			// Skip files outside cwd (../ paths) only if no filter specified
			if absFilterPath == "" && strings.HasPrefix(rel, "..") {
				continue
			}

			relativePath = "./" + filepath.ToSlash(rel)
		}

		// Extract original content from metadata
		rawContent := r.Metadata["raw"]
		if rawContent == "" {
			rawContent = r.Content
		}

		result := types.SearchResult{
			FilePath:     relativePath,
			AbsolutePath: absolutePath,
			ChunkType:    chunkType,
			Name:         r.Metadata["name"],
			Lines:        fmt.Sprintf("%s-%s", r.Metadata["start_line"], r.Metadata["end_line"]),
			Content:      rawContent,
			Similarity:   r.Similarity,
			Language:     language,
		}
		results = append(results, result)
	}

	return results, nil
}

// DeleteFileChunks removes all chunks for a specific file (by absolute path)
func (s *Store) DeleteFileChunks(ctx context.Context, absolutePath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Delete by metadata filter
	whereFilter := map[string]string{"absolute_path": absolutePath}
	if err := s.collection.Delete(ctx, whereFilter, nil); err != nil {
		return fmt.Errorf("failed to delete file chunks: %w", err)
	}

	return nil
}

// GetTotalChunkCount returns the total number of chunks in the global collection
func (s *Store) GetTotalChunkCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.collection.Count()
}

// FindCallers finds all chunks that call a specific symbol
func (s *Store) FindCallers(ctx context.Context, symbolName string, maxResults int) ([]types.CallerInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if maxResults <= 0 {
		maxResults = 50
	}

	// Query all chunks and filter by calls metadata
	// Note: chromem-go doesn't support substring search in metadata,
	// so we use a semantic query with a high limit and filter
	query := symbolName + " function call"
	chromemResults, err := s.collection.Query(ctx, query, maxResults*3, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	callers := make([]types.CallerInfo, 0)
	seen := make(map[string]bool)

	for _, r := range chromemResults {
		if len(callers) >= maxResults {
			break
		}

		// Check if this chunk calls our symbol
		calls := r.Metadata["calls"]
		if calls == "" {
			continue
		}

		// Check if symbolName is in the calls list
		callList := strings.Split(calls, ",")
		found := false
		for _, call := range callList {
			// Handle both exact matches and method calls (e.g., "obj.Method")
			call = strings.TrimSpace(call)
			if call == symbolName || strings.HasSuffix(call, "."+symbolName) {
				found = true
				break
			}
		}

		if !found {
			continue
		}

		// Avoid duplicates
		name := r.Metadata["name"]
		if seen[name] {
			continue
		}
		seen[name] = true

		// Parse line number
		startLine := 0
		if lineStr := r.Metadata["start_line"]; lineStr != "" {
			startLine, _ = strconv.Atoi(lineStr)
		}

		callers = append(callers, types.CallerInfo{
			Name:     name,
			FilePath: r.Metadata["absolute_path"],
			Line:     startLine,
			Language: r.Metadata["language"],
			IsTest:   r.Metadata["is_test"] == "true",
			Parent:   r.Metadata["parent"],
		})
	}

	return callers, nil
}

// FindSymbolLocation looks up where a symbol is defined in the index
func (s *Store) FindSymbolLocation(ctx context.Context, symbolName string) (*types.CallInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Query for the symbol by name
	chromemResults, err := s.collection.Query(ctx, symbolName, 20, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	// Find exact or best match
	for _, r := range chromemResults {
		name := r.Metadata["name"]
		// Check for exact match or method match (e.g., "Class.Method" matches "Method")
		if name == symbolName || strings.HasSuffix(name, "."+symbolName) {
			startLine := 0
			if lineStr := r.Metadata["start_line"]; lineStr != "" {
				startLine, _ = strconv.Atoi(lineStr)
			}

			return &types.CallInfo{
				Name:       name,
				FilePath:   r.Metadata["absolute_path"],
				Line:       startLine,
				Language:   r.Metadata["language"],
				IsExternal: false,
			}, nil
		}
	}

	// Not found in index - likely external/stdlib
	return &types.CallInfo{
		Name:       symbolName,
		IsExternal: true,
	}, nil
}

// FindCallersDeep finds callers up to N levels deep
func (s *Store) FindCallersDeep(ctx context.Context, symbolName string, depth int, maxPerLevel int) (map[int][]types.CallerInfo, error) {
	result := make(map[int][]types.CallerInfo)

	if depth <= 0 {
		depth = 3
	}
	if maxPerLevel <= 0 {
		maxPerLevel = 10
	}

	// Level 1: Direct callers
	currentSymbols := []string{symbolName}
	seenSymbols := make(map[string]bool)
	seenSymbols[symbolName] = true

	for level := 1; level <= depth; level++ {
		levelCallers := make([]types.CallerInfo, 0)
		nextSymbols := make([]string, 0)

		for _, sym := range currentSymbols {
			callers, err := s.FindCallers(ctx, sym, maxPerLevel)
			if err != nil {
				continue
			}

			for _, caller := range callers {
				// Skip if we've already seen this symbol
				if seenSymbols[caller.Name] {
					continue
				}
				seenSymbols[caller.Name] = true

				levelCallers = append(levelCallers, caller)
				nextSymbols = append(nextSymbols, caller.Name)
			}
		}

		if len(levelCallers) > 0 {
			result[level] = levelCallers
		}

		// Move to next level
		currentSymbols = nextSymbols
		if len(currentSymbols) == 0 {
			break
		}
	}

	return result, nil
}

// GetChunkMetadata retrieves metadata for a specific symbol by name
func (s *Store) GetChunkMetadata(ctx context.Context, symbolName string) (map[string]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Query for the symbol
	chromemResults, err := s.collection.Query(ctx, symbolName, 10, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	// Find exact match
	for _, r := range chromemResults {
		if r.Metadata["name"] == symbolName {
			return r.Metadata, nil
		}
	}

	return nil, nil
}

// ClearAll removes all chunks from the global collection
func (s *Store) ClearAll(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Delete and recreate the collection
	if err := s.db.DeleteCollection(globalCollectionName); err != nil {
		return fmt.Errorf("failed to delete collection: %w", err)
	}

	collection, err := s.db.CreateCollection(globalCollectionName, nil, s.chromemEmbeddingFunc())
	if err != nil {
		return fmt.Errorf("failed to recreate collection: %w", err)
	}
	s.collection = collection

	return nil
}

// Close closes the store (currently no-op as chromem handles persistence)
func (s *Store) Close() error {
	// chromem-go handles persistence automatically
	return nil
}

// projectCollectionName generates a collection name from a project path
// Used by metadata.go for project ID generation
func projectCollectionName(projectPath string) string {
	hash := sha256.Sum256([]byte(projectPath))
	shortHash := hex.EncodeToString(hash[:8])
	return fmt.Sprintf("project:%s", shortHash)
}

// GenerateChunkID creates a unique ID for a chunk using absolute file path
func GenerateChunkID(absolutePath string, index int) string {
	// Normalize path separators and create hash-based ID
	normalizedPath := filepath.ToSlash(absolutePath)
	hash := sha256.Sum256([]byte(normalizedPath))
	shortHash := hex.EncodeToString(hash[:16])
	return fmt.Sprintf("%s:%d", shortHash, index)
}
