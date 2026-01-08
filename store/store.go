package store

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mcp-semantic-search/config"
	"mcp-semantic-search/types"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/ncruces"
	"github.com/ncruces/go-sqlite3"
)

// Store manages the SQLite vector database using ncruces driver
type Store struct {
	db             *sqlite3.Conn
	dbPath         string
	embeddingFunc  types.EmbeddingFunc
	cfg            *config.Config
	mu             sync.Mutex
	embeddingDim   int // Detected embedding dimension from model
}

// NewStore creates a new Store instance with SQLite + sqlite-vec
func NewStore(cfg *config.Config, embeddingFunc types.EmbeddingFunc) (*Store, error) {
	// Ensure database directory exists
	if err := os.MkdirAll(cfg.DBPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create db directory: %w", err)
	}

	dbPath := filepath.Join(cfg.DBPath, "vectors.db")

	// Log the path being used for debugging
	fmt.Fprintf(os.Stderr, "Opening SQLite database at: %s\n", dbPath)

	// Try to open and verify database; if corrupted, delete and retry
	db, err := openAndVerifyDB(dbPath)
	if err != nil {
		return nil, err
	}

	store := &Store{
		db:            db,
		dbPath:        dbPath,
		embeddingFunc: embeddingFunc,
		cfg:           cfg,
	}

	// Detect embedding dimension from the model
	embDim, err := store.detectEmbeddingDimension()
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to detect embedding dimension: %w", err)
	}
	store.embeddingDim = embDim
	log.Printf("Detected embedding dimension: %d", embDim)

	if err := store.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return store, nil
}

// openAndVerifyDB opens a database and verifies its integrity.
// If the database is corrupted, it deletes and recreates it.
func openAndVerifyDB(dbPath string) (*sqlite3.Conn, error) {
	db, err := sqlite3.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database at %s: %w", dbPath, err)
	}

	// Check vec version - this verifies sqlite-vec is loaded
	stmt, _, err := db.Prepare(`SELECT vec_version()`)
	if err != nil {
		db.Close()
		// Try to recover by deleting corrupted database
		log.Printf("Database appears corrupted, attempting recovery...")
		if err := os.Remove(dbPath); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to remove corrupted database: %w", err)
		}
		// Also remove any journal files
		os.Remove(dbPath + "-journal")
		os.Remove(dbPath + "-wal")
		os.Remove(dbPath + "-shm")

		// Retry opening
		db, err = sqlite3.Open(dbPath)
		if err != nil {
			return nil, fmt.Errorf("failed to open database after recovery: %w", err)
		}
		stmt, _, err = db.Prepare(`SELECT vec_version()`)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to check vec_version after recovery: %w", err)
		}
	}
	if stmt.Step() {
		log.Printf("sqlite-vec version: %s", stmt.ColumnText(0))
	}
	stmt.Close()

	// Run integrity check on existing database
	if fileExists(dbPath) {
		integrityOK := true
		stmt, _, err := db.Prepare(`PRAGMA integrity_check`)
		if err != nil {
			integrityOK = false
		} else {
			if stmt.Step() {
				result := stmt.ColumnText(0)
				if result != "ok" {
					integrityOK = false
					log.Printf("Integrity check failed: %s", result)
				}
			}
			stmt.Close()
		}

		if !integrityOK {
			db.Close()
			log.Printf("Database integrity check failed, recreating...")
			if err := os.Remove(dbPath); err != nil && !os.IsNotExist(err) {
				return nil, fmt.Errorf("failed to remove corrupted database: %w", err)
			}
			os.Remove(dbPath + "-journal")
			os.Remove(dbPath + "-wal")
			os.Remove(dbPath + "-shm")

			db, err = sqlite3.Open(dbPath)
			if err != nil {
				return nil, fmt.Errorf("failed to create new database: %w", err)
			}
		}
	}

	// Use DELETE journal mode instead of WAL (more compatible across platforms)
	err = db.Exec("PRAGMA journal_mode=DELETE")
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to set journal mode: %w", err)
	}

	err = db.Exec("PRAGMA busy_timeout=5000")
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to set busy timeout: %w", err)
	}

	// Sync mode for better reliability
	err = db.Exec("PRAGMA synchronous=NORMAL")
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to set synchronous mode: %w", err)
	}

	return db, nil
}

// fileExists checks if a file exists
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// detectEmbeddingDimension generates a test embedding to determine the model's output dimension
func (s *Store) detectEmbeddingDimension() (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Generate a test embedding with a simple string
	testEmb, err := s.embeddingFunc(ctx, "test")
	if err != nil {
		return 0, fmt.Errorf("failed to generate test embedding: %w", err)
	}

	if len(testEmb) == 0 {
		return 0, fmt.Errorf("embedding model returned empty vector")
	}

	return len(testEmb), nil
}

// checkAndUpdateDimension checks if the embedding dimension has changed and updates the stored value
// Returns true if dimension changed (requiring table recreation), false otherwise
func (s *Store) checkAndUpdateDimension() (bool, error) {
	currentDimStr := strconv.Itoa(s.embeddingDim)

	// Try to get stored dimension
	stmt, _, err := s.db.Prepare("SELECT value FROM store_config WHERE key = 'embedding_dimension'")
	if err != nil {
		return false, fmt.Errorf("failed to prepare query: %w", err)
	}

	var storedDim string
	if stmt.Step() {
		storedDim = stmt.ColumnText(0)
	}
	stmt.Close()

	// If no stored dimension, this is first run - store it
	if storedDim == "" {
		insertStmt, _, err := s.db.Prepare("INSERT INTO store_config (key, value) VALUES ('embedding_dimension', ?)")
		if err != nil {
			return false, fmt.Errorf("failed to prepare insert: %w", err)
		}
		insertStmt.BindText(1, currentDimStr)
		err = insertStmt.Exec()
		insertStmt.Close()
		if err != nil {
			return false, fmt.Errorf("failed to store dimension: %w", err)
		}
		return false, nil // First run, no change
	}

	// Check if dimension changed
	if storedDim != currentDimStr {
		log.Printf("Embedding dimension changed from %s to %s", storedDim, currentDimStr)
		// Update stored dimension
		updateStmt, _, err := s.db.Prepare("UPDATE store_config SET value = ? WHERE key = 'embedding_dimension'")
		if err != nil {
			return false, fmt.Errorf("failed to prepare update: %w", err)
		}
		updateStmt.BindText(1, currentDimStr)
		err = updateStmt.Exec()
		updateStmt.Close()
		if err != nil {
			return false, fmt.Errorf("failed to update dimension: %w", err)
		}
		return true, nil // Dimension changed
	}

	return false, nil // No change
}

// initSchema creates the database tables and indexes
func (s *Store) initSchema() error {
	// Create store_config table to track settings like embedding dimension
	err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS store_config (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create store_config table: %w", err)
	}

	// Check if embedding dimension has changed
	dimensionChanged, err := s.checkAndUpdateDimension()
	if err != nil {
		return fmt.Errorf("failed to check embedding dimension: %w", err)
	}

	if dimensionChanged {
		log.Printf("Embedding dimension changed, recreating vector table...")
		// Drop existing vec_chunks, mapping, and clear chunks data
		s.db.Exec("DROP TABLE IF EXISTS vec_chunks")
		s.db.Exec("DROP TABLE IF EXISTS vec_chunk_map")
		s.db.Exec("DELETE FROM chunks")
	}

	// Create chunks table
	err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS chunks (
			id TEXT PRIMARY KEY,
			absolute_path TEXT NOT NULL,
			chunk_type TEXT NOT NULL,
			name TEXT NOT NULL,
			language TEXT NOT NULL,
			start_line INTEGER NOT NULL,
			end_line INTEGER NOT NULL,
			raw_content TEXT NOT NULL,
			embedding_text TEXT NOT NULL,
			calls TEXT,
			refs TEXT,
			is_exported INTEGER NOT NULL DEFAULT 0,
			is_test INTEGER NOT NULL DEFAULT 0,
			parent TEXT
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create chunks table: %w", err)
	}

	// Create indexes
	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_chunks_path ON chunks(absolute_path)",
		"CREATE INDEX IF NOT EXISTS idx_chunks_language ON chunks(language)",
		"CREATE INDEX IF NOT EXISTS idx_chunks_type ON chunks(chunk_type)",
		"CREATE INDEX IF NOT EXISTS idx_chunks_name ON chunks(name)",
	}
	for _, idx := range indexes {
		if err := s.db.Exec(idx); err != nil {
			return fmt.Errorf("failed to create index: %w", err)
		}
	}

	// Create vec0 virtual table for vector search with dynamic dimension
	// Note: Using rowid instead of TEXT PRIMARY KEY for better compatibility with ncruces driver
	createVecSQL := fmt.Sprintf(`
		CREATE VIRTUAL TABLE IF NOT EXISTS vec_chunks USING vec0(
			embedding float[%d] distance_metric=cosine
		)
	`, s.embeddingDim)
	err = s.db.Exec(createVecSQL)
	if err != nil {
		return fmt.Errorf("failed to create vec_chunks table: %w", err)
	}

	// Create mapping table from chunk_id to vec_chunks rowid
	err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS vec_chunk_map (
			chunk_id TEXT PRIMARY KEY,
			vec_rowid INTEGER NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create vec_chunk_map table: %w", err)
	}

	// Index for fast caller lookups (LIKE queries on calls field)
	err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_chunks_calls ON chunks(calls)`)
	if err != nil {
		return fmt.Errorf("failed to create calls index: %w", err)
	}

	// Index for fast type reference lookups (LIKE queries on refs field)
	err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_chunks_refs ON chunks(refs)`)
	if err != nil {
		return fmt.Errorf("failed to create refs index: %w", err)
	}

	// Create file_hashes table for incremental indexing
	err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS file_hashes (
			project_path TEXT NOT NULL,
			file_path TEXT NOT NULL,
			hash TEXT NOT NULL,
			PRIMARY KEY (project_path, file_path)
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create file_hashes table: %w", err)
	}

	// Index for project-based queries
	err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_file_hashes_project ON file_hashes(project_path)`)
	if err != nil {
		return fmt.Errorf("failed to create file_hashes index: %w", err)
	}

	return nil
}

// AddChunks adds chunks to the database with their embeddings
func (s *Store) AddChunks(ctx context.Context, chunks []types.Chunk) error {
	if len(chunks) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Generate embeddings for all chunks
	embeddings := make([][]float32, len(chunks))
	embeddingTexts := make([]string, len(chunks))

	for i, chunk := range chunks {
		embeddingText := types.FormatForEmbedding(
			chunk.Language,
			string(chunk.Type),
			chunk.Name,
			chunk.Content,
		)
		embeddingTexts[i] = embeddingText

		emb, err := s.embeddingFunc(ctx, embeddingText)
		if err != nil {
			return fmt.Errorf("embedding failed for chunk %s: %w", chunk.ID, err)
		}
		embeddings[i] = emb
	}

	// Begin transaction
	err := s.db.Exec("BEGIN IMMEDIATE TRANSACTION")
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	// Prepare chunk insert statement
	chunkStmt, _, err := s.db.Prepare(`
		INSERT OR REPLACE INTO chunks
		(id, absolute_path, chunk_type, name, language, start_line, end_line,
		 raw_content, embedding_text, calls, refs, is_exported, is_test, parent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		s.db.Exec("ROLLBACK")
		return fmt.Errorf("failed to prepare chunk statement: %w", err)
	}
	defer chunkStmt.Close()

	// Prepare to delete old vec_chunks entries via mapping
	vecMapDelStmt, _, err := s.db.Prepare(`DELETE FROM vec_chunk_map WHERE chunk_id = ?`)
	if err != nil {
		s.db.Exec("ROLLBACK")
		return fmt.Errorf("failed to prepare vec map delete statement: %w", err)
	}
	defer vecMapDelStmt.Close()

	// Get old vec rowid for deletion
	getOldRowidStmt, _, err := s.db.Prepare(`SELECT vec_rowid FROM vec_chunk_map WHERE chunk_id = ?`)
	if err != nil {
		s.db.Exec("ROLLBACK")
		return fmt.Errorf("failed to prepare get rowid statement: %w", err)
	}
	defer getOldRowidStmt.Close()

	// Delete from vec_chunks by rowid
	vecDelStmt, _, err := s.db.Prepare(`DELETE FROM vec_chunks WHERE rowid = ?`)
	if err != nil {
		s.db.Exec("ROLLBACK")
		return fmt.Errorf("failed to prepare vec delete statement: %w", err)
	}
	defer vecDelStmt.Close()

	// Prepare vector insert statement (uses auto-generated rowid)
	vecStmt, _, err := s.db.Prepare(`INSERT INTO vec_chunks(embedding) VALUES (?)`)
	if err != nil {
		s.db.Exec("ROLLBACK")
		return fmt.Errorf("failed to prepare vec statement: %w", err)
	}
	defer vecStmt.Close()

	// Prepare mapping insert
	vecMapStmt, _, err := s.db.Prepare(`INSERT OR REPLACE INTO vec_chunk_map(chunk_id, vec_rowid) VALUES (?, ?)`)
	if err != nil {
		s.db.Exec("ROLLBACK")
		return fmt.Errorf("failed to prepare vec map statement: %w", err)
	}
	defer vecMapStmt.Close()

	// Insert chunks and embeddings
	for i, chunk := range chunks {
		chunkStmt.BindText(1, chunk.ID)
		chunkStmt.BindText(2, chunk.FilePath)
		chunkStmt.BindText(3, string(chunk.Type))
		chunkStmt.BindText(4, chunk.Name)
		chunkStmt.BindText(5, chunk.Language)
		chunkStmt.BindInt(6, chunk.StartLine)
		chunkStmt.BindInt(7, chunk.EndLine)
		chunkStmt.BindText(8, chunk.Content)
		chunkStmt.BindText(9, embeddingTexts[i])
		chunkStmt.BindText(10, strings.Join(chunk.Calls, ","))
		chunkStmt.BindText(11, strings.Join(chunk.References, ","))
		chunkStmt.BindInt(12, boolToInt(chunk.IsExported))
		chunkStmt.BindInt(13, boolToInt(chunk.IsTest))
		chunkStmt.BindText(14, chunk.Parent)

		err = chunkStmt.Exec()
		if err != nil {
			s.db.Exec("ROLLBACK")
			return fmt.Errorf("failed to insert chunk %s: %w", chunk.ID, err)
		}
		chunkStmt.Reset()

		// Serialize embedding for sqlite-vec
		embeddingBlob, err := sqlite_vec.SerializeFloat32(embeddings[i])
		if err != nil {
			s.db.Exec("ROLLBACK")
			return fmt.Errorf("failed to serialize vector for %s: %w", chunk.ID, err)
		}

		// Delete old vector if exists (lookup old rowid from mapping)
		getOldRowidStmt.BindText(1, chunk.ID)
		if getOldRowidStmt.Step() {
			oldRowid := getOldRowidStmt.ColumnInt64(0)
			vecDelStmt.BindInt64(1, oldRowid)
			vecDelStmt.Exec()
			vecDelStmt.Reset()
		}
		getOldRowidStmt.Reset()

		// Delete old mapping
		vecMapDelStmt.BindText(1, chunk.ID)
		vecMapDelStmt.Exec()
		vecMapDelStmt.Reset()

		// Insert new vector
		vecStmt.BindBlob(1, embeddingBlob)
		err = vecStmt.Exec()
		if err != nil {
			s.db.Exec("ROLLBACK")
			return fmt.Errorf("failed to insert vector for %s: %w", chunk.ID, err)
		}

		// Get the new rowid
		newRowid := s.db.LastInsertRowID()
		vecStmt.Reset()

		// Insert mapping
		vecMapStmt.BindText(1, chunk.ID)
		vecMapStmt.BindInt64(2, newRowid)
		err = vecMapStmt.Exec()
		if err != nil {
			s.db.Exec("ROLLBACK")
			return fmt.Errorf("failed to insert vec mapping for %s: %w", chunk.ID, err)
		}
		vecMapStmt.Reset()
	}

	return s.db.Exec("COMMIT")
}

// Search performs semantic search across the database
func (s *Store) Search(ctx context.Context, query string, cwd string, opts types.SearchOptions) ([]types.SearchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	limit := opts.Limit
	if limit <= 0 {
		limit = 5
	}

	// Generate query embedding
	queryEmb, err := s.embeddingFunc(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to embed query: %w", err)
	}

	// Serialize query vector
	queryBlob, err := sqlite_vec.SerializeFloat32(queryEmb)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize query vector: %w", err)
	}

	// Query more results than needed since we'll filter
	queryLimit := limit * 5
	if queryLimit < 50 {
		queryLimit = 50
	}

	// Prepare query terms for keyword boosting
	queryLower := strings.ToLower(query)
	queryTerms := strings.Fields(queryLower)

	// Resolve filterPath to absolute if provided
	var absFilterPath string
	var pathPattern string
	isGlobPattern := false
	if opts.Path != "" {
		if strings.ContainsAny(opts.Path, "*?") {
			isGlobPattern = true
			if !filepath.IsAbs(opts.Path) {
				pathPattern = filepath.Join(cwd, opts.Path)
			} else {
				pathPattern = opts.Path
			}
			pathPattern = filepath.Clean(pathPattern)
		} else {
			if !filepath.IsAbs(opts.Path) {
				absFilterPath = filepath.Join(cwd, opts.Path)
			} else {
				absFilterPath = opts.Path
			}
			absFilterPath = filepath.Clean(absFilterPath)
		}
	}

	// Normalize filters
	languageFilter := strings.ToLower(opts.Language)
	chunkTypeFilter := strings.ToLower(opts.ChunkType)

	// Two-phase query: vector search then join with metadata via mapping table
	stmt, _, err := s.db.Prepare(`
		SELECT
			c.id, c.absolute_path, c.chunk_type, c.name, c.language,
			c.start_line, c.end_line, c.raw_content, c.calls, c.refs,
			c.is_exported, c.is_test, c.parent,
			v.distance
		FROM vec_chunks v
		JOIN vec_chunk_map m ON m.vec_rowid = v.rowid
		JOIN chunks c ON c.id = m.chunk_id
		WHERE v.embedding MATCH ?
		  AND k = ?
		ORDER BY v.distance
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare query: %w", err)
	}
	defer stmt.Close()

	stmt.BindBlob(1, queryBlob)
	stmt.BindInt(2, queryLimit)

	results := make([]types.SearchResult, 0, limit)

	for stmt.Step() {
		id := stmt.ColumnText(0)
		absolutePath := stmt.ColumnText(1)
		chunkType := stmt.ColumnText(2)
		name := stmt.ColumnText(3)
		language := stmt.ColumnText(4)
		startLine := stmt.ColumnInt(5)
		endLine := stmt.ColumnInt(6)
		rawContent := stmt.ColumnText(7)
		calls := stmt.ColumnText(8)
		refs := stmt.ColumnText(9)
		isExported := stmt.ColumnInt(10)
		isTest := stmt.ColumnInt(11)
		parent := stmt.ColumnText(12)
		distance := stmt.ColumnFloat(13)

		// Suppress unused variable warnings
		_ = id
		_ = calls
		_ = refs
		_ = isExported
		_ = isTest
		_ = parent

		// Convert distance to similarity (cosine distance: similarity = 1 - distance)
		similarity := float32(1.0 - distance)

		// Apply minimum similarity filter
		if opts.MinSimilarity > 0 && similarity < opts.MinSimilarity {
			continue
		}

		// Apply language filter
		if languageFilter != "" && strings.ToLower(language) != languageFilter {
			continue
		}

		// Apply code_only filter
		if opts.CodeOnly && types.NonCodeLanguages[strings.ToLower(language)] {
			continue
		}

		// Apply chunk type filter
		if chunkTypeFilter != "" && chunkTypeFilter != "all" {
			if strings.ToLower(chunkType) != chunkTypeFilter {
				continue
			}
		}

		// Apply path filter
		if absFilterPath != "" || isGlobPattern {
			cleanAbsPath := filepath.Clean(absolutePath)
			if isGlobPattern {
				matched, err := matchGlobPattern(pathPattern, cleanAbsPath)
				if err != nil || !matched {
					continue
				}
			} else if absFilterPath != "" {
				if !strings.HasPrefix(cleanAbsPath, absFilterPath) {
					continue
				}
				if len(cleanAbsPath) > len(absFilterPath) && cleanAbsPath[len(absFilterPath)] != filepath.Separator {
					continue
				}
			}
		}

		// Convert to relative path from cwd
		relativePath := absolutePath
		if cwd != "" {
			rel, err := filepath.Rel(cwd, absolutePath)
			if err != nil {
				continue
			}

			// Skip files outside cwd unless filter specified
			if absFilterPath == "" && !isGlobPattern && strings.HasPrefix(rel, "..") {
				continue
			}

			relativePath = "./" + filepath.ToSlash(rel)
		}

		// Apply keyword boosting
		boostedSimilarity := similarity
		if len(queryTerms) > 0 && name != "" {
			nameLower := strings.ToLower(name)
			matchCount := 0
			for _, term := range queryTerms {
				if strings.Contains(nameLower, term) {
					matchCount++
				}
			}
			if matchCount > 0 {
				boost := float32(matchCount) / float32(len(queryTerms)) * 0.3
				boostedSimilarity = similarity + boost
				if boostedSimilarity > 1.0 {
					boostedSimilarity = 1.0
				}
			}
		}

		result := types.SearchResult{
			FilePath:     relativePath,
			AbsolutePath: absolutePath,
			ChunkType:    chunkType,
			Name:         name,
			Lines:        fmt.Sprintf("%d-%d", startLine, endLine),
			Content:      rawContent,
			Similarity:   boostedSimilarity,
			Language:     language,
		}
		results = append(results, result)
	}

	if err := stmt.Err(); err != nil {
		return nil, fmt.Errorf("query iteration failed: %w", err)
	}

	// Re-sort by boosted similarity
	sort.Slice(results, func(i, j int) bool {
		return results[i].Similarity > results[j].Similarity
	})

	// Trim to limit
	if len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}

// DeleteFileChunks removes all chunks for a specific file
func (s *Store) DeleteFileChunks(ctx context.Context, absolutePath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	err := s.db.Exec("BEGIN TRANSACTION")
	if err != nil {
		return err
	}

	// Get chunk IDs for this file
	stmt, _, err := s.db.Prepare("SELECT id FROM chunks WHERE absolute_path = ?")
	if err != nil {
		s.db.Exec("ROLLBACK")
		return err
	}

	stmt.BindText(1, absolutePath)

	var ids []string
	for stmt.Step() {
		ids = append(ids, stmt.ColumnText(0))
	}
	stmt.Close()

	if len(ids) == 0 {
		s.db.Exec("ROLLBACK")
		return nil
	}

	// Get vec_rowids from mapping table and delete from vec_chunks
	getRowidStmt, _, err := s.db.Prepare("SELECT vec_rowid FROM vec_chunk_map WHERE chunk_id = ?")
	if err != nil {
		s.db.Exec("ROLLBACK")
		return err
	}
	delVecStmt, _, err := s.db.Prepare("DELETE FROM vec_chunks WHERE rowid = ?")
	if err != nil {
		getRowidStmt.Close()
		s.db.Exec("ROLLBACK")
		return err
	}
	delMapStmt, _, err := s.db.Prepare("DELETE FROM vec_chunk_map WHERE chunk_id = ?")
	if err != nil {
		getRowidStmt.Close()
		delVecStmt.Close()
		s.db.Exec("ROLLBACK")
		return err
	}

	for _, id := range ids {
		// Get vec_rowid
		getRowidStmt.BindText(1, id)
		if getRowidStmt.Step() {
			rowid := getRowidStmt.ColumnInt64(0)
			// Delete from vec_chunks
			delVecStmt.BindInt64(1, rowid)
			delVecStmt.Exec()
			delVecStmt.Reset()
		}
		getRowidStmt.Reset()

		// Delete from mapping
		delMapStmt.BindText(1, id)
		delMapStmt.Exec()
		delMapStmt.Reset()
	}
	getRowidStmt.Close()
	delVecStmt.Close()
	delMapStmt.Close()

	// Delete from chunks
	delChunkStmt, _, err := s.db.Prepare("DELETE FROM chunks WHERE absolute_path = ?")
	if err != nil {
		s.db.Exec("ROLLBACK")
		return err
	}
	delChunkStmt.BindText(1, absolutePath)
	err = delChunkStmt.Exec()
	delChunkStmt.Close()
	if err != nil {
		s.db.Exec("ROLLBACK")
		return err
	}

	return s.db.Exec("COMMIT")
}

// GetTotalChunkCount returns the total number of chunks in the database
func (s *Store) GetTotalChunkCount() int {
	if s == nil || s.db == nil {
		return 0
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	stmt, _, err := s.db.Prepare("SELECT COUNT(*) FROM chunks")
	if err != nil {
		log.Printf("GetTotalChunkCount error: %v", err)
		return 0
	}
	defer stmt.Close()

	if stmt.Step() {
		return stmt.ColumnInt(0)
	}
	return 0
}

// FindCallers finds all chunks that call a specific symbol
// If pathPrefix is not empty, only returns callers from files within that path (project scoping)
func (s *Store) FindCallers(ctx context.Context, symbolName string, maxResults int, pathPrefix string) ([]types.CallerInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if maxResults <= 0 {
		maxResults = 50
	}

	var stmt *sqlite3.Stmt
	var err error

	if pathPrefix != "" {
		// Scope to specific project/folder
		stmt, _, err = s.db.Prepare(`
			SELECT name, absolute_path, start_line, language, is_test, parent, calls
			FROM chunks
			WHERE calls LIKE ? AND absolute_path LIKE ?
			LIMIT ?
		`)
		if err != nil {
			return nil, fmt.Errorf("query failed: %w", err)
		}
		stmt.BindText(1, "%"+symbolName+"%")
		stmt.BindText(2, pathPrefix+"%")
		stmt.BindInt(3, maxResults*3)
	} else {
		// Search all indexed code
		stmt, _, err = s.db.Prepare(`
			SELECT name, absolute_path, start_line, language, is_test, parent, calls
			FROM chunks
			WHERE calls LIKE ?
			LIMIT ?
		`)
		if err != nil {
			return nil, fmt.Errorf("query failed: %w", err)
		}
		stmt.BindText(1, "%"+symbolName+"%")
		stmt.BindInt(2, maxResults*3)
	}
	defer stmt.Close()

	callers := make([]types.CallerInfo, 0)
	seen := make(map[string]bool)

	for stmt.Step() {
		name := stmt.ColumnText(0)
		absolutePath := stmt.ColumnText(1)
		startLine := stmt.ColumnInt(2)
		language := stmt.ColumnText(3)
		isTest := stmt.ColumnInt(4)
		parent := stmt.ColumnText(5)
		calls := stmt.ColumnText(6)

		// Verify symbol is actually in calls list
		if calls == "" {
			continue
		}

		callList := strings.Split(calls, ",")
		found := false
		for _, call := range callList {
			call = strings.TrimSpace(call)
			if call == symbolName || strings.HasSuffix(call, "."+symbolName) {
				found = true
				break
			}
		}

		if !found {
			continue
		}

		if seen[name] {
			continue
		}
		seen[name] = true

		callers = append(callers, types.CallerInfo{
			Name:     name,
			FilePath: absolutePath,
			Line:     startLine,
			Language: language,
			IsTest:   isTest == 1,
			Parent:   parent,
		})

		if len(callers) >= maxResults {
			break
		}
	}

	return callers, nil
}

// FindCallersDeep finds callers up to N levels deep using the chunks table
// If pathPrefix is not empty, only returns callers from files within that path (project scoping)
func (s *Store) FindCallersDeep(ctx context.Context, symbolName string, depth int, maxPerLevel int, pathPrefix string) map[int][]types.CallerInfo {
	result := make(map[int][]types.CallerInfo)

	if depth <= 0 {
		depth = 3
	}
	if maxPerLevel <= 0 {
		maxPerLevel = 10
	}

	currentSymbols := []string{symbolName}
	seenSymbols := make(map[string]bool)
	seenSymbols[symbolName] = true

	for level := 1; level <= depth; level++ {
		levelCallers := make([]types.CallerInfo, 0)
		nextSymbols := make([]string, 0)

		for _, sym := range currentSymbols {
			callers, err := s.FindCallers(ctx, sym, maxPerLevel, pathPrefix)
			if err != nil {
				continue
			}

			for _, caller := range callers {
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

		currentSymbols = nextSymbols
		if len(currentSymbols) == 0 {
			break
		}
	}

	return result
}

// HasCallers returns true if the symbol has any callers (using chunks table)
func (s *Store) HasCallers(ctx context.Context, symbolName string, pathPrefix string) bool {
	callers, err := s.FindCallers(ctx, symbolName, 1, pathPrefix)
	return err == nil && len(callers) > 0
}

// FindReferencers finds all chunks that reference a specific type/symbol in their refs field
// This is used to find "Used By" for types, structs, classes, interfaces
// If pathPrefix is not empty, only returns referencers from files within that path (project scoping)
func (s *Store) FindReferencers(ctx context.Context, symbolName string, maxResults int, pathPrefix string) ([]types.CallerInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if maxResults <= 0 {
		maxResults = 50
	}

	var stmt *sqlite3.Stmt
	var err error

	if pathPrefix != "" {
		// Scope to specific project/folder
		stmt, _, err = s.db.Prepare(`
			SELECT name, absolute_path, start_line, language, is_test, parent, refs, chunk_type
			FROM chunks
			WHERE refs LIKE ? AND absolute_path LIKE ?
			LIMIT ?
		`)
		if err != nil {
			return nil, fmt.Errorf("query failed: %w", err)
		}
		stmt.BindText(1, "%"+symbolName+"%")
		stmt.BindText(2, pathPrefix+"%")
		stmt.BindInt(3, maxResults*3)
	} else {
		// Search all indexed code
		stmt, _, err = s.db.Prepare(`
			SELECT name, absolute_path, start_line, language, is_test, parent, refs, chunk_type
			FROM chunks
			WHERE refs LIKE ?
			LIMIT ?
		`)
		if err != nil {
			return nil, fmt.Errorf("query failed: %w", err)
		}
		stmt.BindText(1, "%"+symbolName+"%")
		stmt.BindInt(2, maxResults*3)
	}
	defer stmt.Close()

	referencers := make([]types.CallerInfo, 0)
	seen := make(map[string]bool)

	for stmt.Step() {
		name := stmt.ColumnText(0)
		absolutePath := stmt.ColumnText(1)
		startLine := stmt.ColumnInt(2)
		language := stmt.ColumnText(3)
		isTest := stmt.ColumnInt(4)
		parent := stmt.ColumnText(5)
		refs := stmt.ColumnText(6)
		chunkType := stmt.ColumnText(7)

		// Don't include the symbol itself
		if name == symbolName {
			continue
		}

		// Verify symbol is actually in refs list (exact match or qualified match)
		if refs == "" {
			continue
		}

		refList := strings.Split(refs, ",")
		found := false
		for _, ref := range refList {
			ref = strings.TrimSpace(ref)
			if ref == symbolName || strings.HasSuffix(ref, "."+symbolName) {
				found = true
				break
			}
		}

		if !found {
			continue
		}

		if seen[name] {
			continue
		}
		seen[name] = true

		referencers = append(referencers, types.CallerInfo{
			Name:     name,
			FilePath: absolutePath,
			Line:     startLine,
			Language: language,
			IsTest:   isTest == 1,
			Parent:   parent,
			Type:     chunkType, // Include chunk type to distinguish function vs type
		})

		if len(referencers) >= maxResults {
			break
		}
	}

	return referencers, nil
}

// FindReferencersDeep finds referencers up to N levels deep
// If pathPrefix is not empty, only returns referencers from files within that path (project scoping)
func (s *Store) FindReferencersDeep(ctx context.Context, symbolName string, depth int, maxPerLevel int, pathPrefix string) map[int][]types.CallerInfo {
	result := make(map[int][]types.CallerInfo)

	if depth <= 0 {
		depth = 3
	}
	if maxPerLevel <= 0 {
		maxPerLevel = 10
	}

	currentSymbols := []string{symbolName}
	seenSymbols := make(map[string]bool)
	seenSymbols[symbolName] = true

	for level := 1; level <= depth; level++ {
		levelReferencers := make([]types.CallerInfo, 0)
		nextSymbols := make([]string, 0)

		for _, sym := range currentSymbols {
			referencers, err := s.FindReferencers(ctx, sym, maxPerLevel, pathPrefix)
			if err != nil {
				continue
			}

			for _, ref := range referencers {
				if seenSymbols[ref.Name] {
					continue
				}
				seenSymbols[ref.Name] = true

				levelReferencers = append(levelReferencers, ref)
				nextSymbols = append(nextSymbols, ref.Name)
			}
		}

		if len(levelReferencers) > 0 {
			result[level] = levelReferencers
		}

		currentSymbols = nextSymbols
		if len(currentSymbols) == 0 {
			break
		}
	}

	return result
}

// HasTestCaller returns true if any caller is a test
func (s *Store) HasTestCaller(ctx context.Context, symbolName string, pathPrefix string) bool {
	callers, err := s.FindCallers(ctx, symbolName, 50, pathPrefix)
	if err != nil {
		return false
	}
	for _, c := range callers {
		if c.IsTest {
			return true
		}
	}
	return false
}

// GetChunkMetadata retrieves metadata for a specific symbol
func (s *Store) GetChunkMetadata(ctx context.Context, symbolName string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	stmt, _, err := s.db.Prepare(`
		SELECT absolute_path, chunk_type, name, language, start_line, end_line,
		       calls, refs, is_exported, is_test, parent
		FROM chunks
		WHERE name = ?
		LIMIT 1
	`)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer stmt.Close()

	stmt.BindText(1, symbolName)

	if !stmt.Step() {
		return nil, nil
	}

	metadata := map[string]string{
		"absolute_path": stmt.ColumnText(0),
		"chunk_type":    stmt.ColumnText(1),
		"name":          stmt.ColumnText(2),
		"language":      stmt.ColumnText(3),
		"start_line":    strconv.Itoa(stmt.ColumnInt(4)),
		"end_line":      strconv.Itoa(stmt.ColumnInt(5)),
		"is_exported":   strconv.FormatBool(stmt.ColumnInt(8) == 1),
		"is_test":       strconv.FormatBool(stmt.ColumnInt(9) == 1),
	}

	if calls := stmt.ColumnText(6); calls != "" {
		metadata["calls"] = calls
	}
	if refs := stmt.ColumnText(7); refs != "" {
		metadata["references"] = refs
	}
	if parent := stmt.ColumnText(10); parent != "" {
		metadata["parent"] = parent
	}

	return metadata, nil
}

// ClearAll removes all chunks from the database
func (s *Store) ClearAll(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	err := s.db.Exec("BEGIN TRANSACTION")
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	err = s.db.Exec("DELETE FROM vec_chunks")
	if err != nil {
		s.db.Exec("ROLLBACK")
		return fmt.Errorf("failed to clear vec_chunks: %w", err)
	}

	err = s.db.Exec("DELETE FROM vec_chunk_map")
	if err != nil {
		s.db.Exec("ROLLBACK")
		return fmt.Errorf("failed to clear vec_chunk_map: %w", err)
	}

	err = s.db.Exec("DELETE FROM chunks")
	if err != nil {
		s.db.Exec("ROLLBACK")
		return fmt.Errorf("failed to clear chunks: %w", err)
	}

	err = s.db.Exec("DELETE FROM file_hashes")
	if err != nil {
		s.db.Exec("ROLLBACK")
		return fmt.Errorf("failed to clear file_hashes: %w", err)
	}

	return s.db.Exec("COMMIT")
}

// Close closes the database connection
func (s *Store) Close() error {
	return s.db.Close()
}

// NewFileHashStore creates a FileHashStore using this store's database connection and shared mutex
func (s *Store) NewFileHashStore() *FileHashStore {
	return NewFileHashStore(s.db, &s.mu)
}

// Helper functions

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// GenerateChunkID creates a unique ID for a chunk using absolute file path
func GenerateChunkID(absolutePath string, index int) string {
	normalizedPath := filepath.ToSlash(absolutePath)
	// Simple hash for ID
	hash := 0
	for _, c := range normalizedPath {
		hash = hash*31 + int(c)
	}
	return fmt.Sprintf("%x:%d", uint32(hash), index)
}

// matchGlobPattern matches a file path against a glob pattern
func matchGlobPattern(pattern, path string) (bool, error) {
	pattern = filepath.ToSlash(pattern)
	path = filepath.ToSlash(path)

	// Handle ** (double star)
	if strings.Contains(pattern, "**") {
		parts := strings.Split(pattern, "**")
		if len(parts) == 2 {
			prefix := strings.TrimSuffix(parts[0], "/")
			suffix := strings.TrimPrefix(parts[1], "/")

			if prefix != "" && !strings.HasPrefix(path, prefix) {
				return false, nil
			}

			remaining := path
			if prefix != "" {
				remaining = strings.TrimPrefix(path, prefix)
				remaining = strings.TrimPrefix(remaining, "/")
			}

			if suffix == "" {
				return true, nil
			}

			if strings.ContainsAny(suffix, "*?") {
				pathParts := strings.Split(remaining, "/")
				for i := range pathParts {
					candidate := strings.Join(pathParts[i:], "/")
					if matched, _ := filepath.Match(suffix, candidate); matched {
						return true, nil
					}
					if i == len(pathParts)-1 {
						if matched, _ := filepath.Match(suffix, pathParts[i]); matched {
							return true, nil
						}
					}
				}
				return false, nil
			}

			return strings.HasSuffix(path, suffix), nil
		}
	}

	// Simple patterns
	if matched, err := filepath.Match(pattern, path); err == nil && matched {
		return true, nil
	}

	// Pattern ends with /*
	if strings.HasSuffix(pattern, "/*") {
		dirPattern := strings.TrimSuffix(pattern, "/*")
		pathDir := filepath.Dir(path)
		pathDir = filepath.ToSlash(pathDir)

		if matched, _ := filepath.Match(dirPattern, pathDir); matched {
			return true, nil
		}
		if strings.HasPrefix(pathDir, dirPattern) || pathDir == dirPattern {
			return true, nil
		}
	}

	// Check filename match
	fileName := filepath.Base(path)
	patternBase := filepath.Base(pattern)
	if strings.ContainsAny(patternBase, "*?") {
		if matched, _ := filepath.Match(patternBase, fileName); matched {
			patternDir := filepath.Dir(pattern)
			pathDir := filepath.Dir(path)
			if strings.HasPrefix(filepath.ToSlash(pathDir), filepath.ToSlash(patternDir)) {
				return true, nil
			}
		}
	}

	return false, nil
}

// projectCollectionName generates a collection name from a project path
// Used by metadata.go for project ID generation
func projectCollectionName(projectPath string) string {
	hash := sha256.Sum256([]byte(projectPath))
	shortHash := hex.EncodeToString(hash[:8])
	return fmt.Sprintf("project:%s", shortHash)
}
