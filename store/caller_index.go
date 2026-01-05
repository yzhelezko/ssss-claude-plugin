package store

import (
	"encoding/gob"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mcp-semantic-search/config"
	"mcp-semantic-search/types"
)

// CompactCaller is a space-efficient representation of CallerInfo
// File paths are stored as indices into a path table to avoid duplication
type CompactCaller struct {
	Name      string
	PathIdx   int // Index into PathTable
	Line      int
	Language  string
	IsTest    bool
	Parent    string
}

// CallerIndexData is the serialized format for the caller index
type CallerIndexData struct {
	PathTable []string                    // Deduplicated file paths
	Callers   map[string][]CompactCaller  // symbolName -> compact callers
}

// CallerIndex provides O(1) lookup for finding callers of any symbol.
// Uses gob encoding with path deduplication for compact storage.
type CallerIndex struct {
	cfg *config.Config

	// callers maps symbolName -> list of callers
	// Key is the called symbol name (e.g., "SearchWithUsage", "main")
	// Value is list of CompactCaller for functions that call this symbol
	callers map[string][]CompactCaller

	// pathTable stores deduplicated file paths
	pathTable []string
	// pathLookup maps path -> index for O(1) lookup during add
	pathLookup map[string]int

	mu sync.RWMutex
}

// NewCallerIndex creates a new caller index
func NewCallerIndex(cfg *config.Config) *CallerIndex {
	idx := &CallerIndex{
		cfg:        cfg,
		callers:    make(map[string][]CompactCaller),
		pathTable:  make([]string, 0),
		pathLookup: make(map[string]int),
	}
	// Try to load existing index (handles migration from JSON)
	_ = idx.Load()
	return idx
}

// indexFilePath returns the path to the caller index file (gob format)
func (c *CallerIndex) indexFilePath() string {
	return filepath.Join(c.cfg.DBPath, "caller_index.gob")
}

// oldJSONPath returns the path to the old JSON format file (for migration)
func (c *CallerIndex) oldJSONPath() string {
	return filepath.Join(c.cfg.DBPath, "caller_index.json")
}

// lockFilePath returns the path to the lock file
func (c *CallerIndex) lockFilePath() string {
	return filepath.Join(c.cfg.DBPath, "caller_index.lock")
}

// acquireFileLock acquires an exclusive file lock for cross-process synchronization
// Returns a cleanup function to release the lock
func (c *CallerIndex) acquireFileLock() (func(), error) {
	lockPath := c.lockFilePath()

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(lockPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create lock directory: %w", err)
	}

	// Try to acquire lock with retries
	var lockFile *os.File
	var err error
	maxRetries := 50 // 5 seconds total (50 * 100ms)

	for i := 0; i < maxRetries; i++ {
		// Try to create lock file exclusively
		lockFile, err = os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err == nil {
			// Got the lock
			// Write PID for debugging
			fmt.Fprintf(lockFile, "%d", os.Getpid())
			break
		}

		if os.IsExist(err) {
			// Lock file exists - check if it's stale (older than 60 seconds)
			if info, statErr := os.Stat(lockPath); statErr == nil {
				if time.Since(info.ModTime()) > 60*time.Second {
					// Stale lock - remove it
					log.Printf("Removing stale lock file (age: %v)", time.Since(info.ModTime()))
					os.Remove(lockPath)
					continue
				}
			}
			// Wait and retry
			time.Sleep(100 * time.Millisecond)
			continue
		}

		// Other error
		return nil, fmt.Errorf("failed to acquire lock: %w", err)
	}

	if lockFile == nil {
		return nil, fmt.Errorf("failed to acquire lock after %d retries", maxRetries)
	}

	// Return cleanup function
	cleanup := func() {
		lockFile.Close()
		os.Remove(lockPath)
	}

	return cleanup, nil
}

// getOrAddPath returns the index for a path, adding it if necessary
func (c *CallerIndex) getOrAddPath(path string) int {
	if idx, ok := c.pathLookup[path]; ok {
		return idx
	}
	idx := len(c.pathTable)
	c.pathTable = append(c.pathTable, path)
	c.pathLookup[path] = idx
	return idx
}

// getPath returns the path for an index
func (c *CallerIndex) getPath(idx int) string {
	if idx < 0 || idx >= len(c.pathTable) {
		return ""
	}
	return c.pathTable[idx]
}

// Load loads the caller index from disk
func (c *CallerIndex) Load() error {
	// Acquire file lock for cross-process safety
	unlock, err := c.acquireFileLock()
	if err != nil {
		log.Printf("Warning: could not acquire file lock for load: %v", err)
		// Continue without lock - better than failing completely
	} else {
		defer unlock()
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Try to load gob format first
	file, err := os.Open(c.indexFilePath())
	if err == nil {
		defer file.Close()
		var data CallerIndexData
		if err := gob.NewDecoder(file).Decode(&data); err != nil {
			log.Printf("Warning: failed to decode caller index: %v", err)
			c.callers = make(map[string][]CompactCaller)
			c.pathTable = make([]string, 0)
			c.pathLookup = make(map[string]int)
			return nil
		}
		c.callers = data.Callers
		c.pathTable = data.PathTable
		// Rebuild pathLookup
		c.pathLookup = make(map[string]int, len(c.pathTable))
		for i, p := range c.pathTable {
			c.pathLookup[p] = i
		}
		// Delete old JSON file if it exists (migration complete)
		_ = os.Remove(c.oldJSONPath())
		return nil
	}

	// If gob doesn't exist, initialize empty
	if os.IsNotExist(err) {
		c.callers = make(map[string][]CompactCaller)
		c.pathTable = make([]string, 0)
		c.pathLookup = make(map[string]int)
		// Check for old JSON file and delete it (will rebuild on next index)
		if _, err := os.Stat(c.oldJSONPath()); err == nil {
			log.Printf("Found old caller_index.json, will rebuild index in new format")
			_ = os.Remove(c.oldJSONPath())
		}
		return nil
	}

	return err
}

// Save persists the caller index to disk using gob encoding
// It also compacts the path table by removing unused paths
func (c *CallerIndex) Save() error {
	// Acquire file lock for cross-process safety
	unlock, err := c.acquireFileLock()
	if err != nil {
		return fmt.Errorf("failed to acquire file lock for save: %w", err)
	}
	defer unlock()

	c.mu.Lock() // Need write lock for compaction
	defer c.mu.Unlock()

	// Compact path table before saving - remove paths no longer referenced
	c.compactPathTable()

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(c.indexFilePath()), 0755); err != nil {
		return err
	}

	// Prepare data for serialization
	data := CallerIndexData{
		PathTable: c.pathTable,
		Callers:   c.callers,
	}

	// Write atomically
	tmpPath := c.indexFilePath() + ".tmp"
	file, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	if err := gob.NewEncoder(file).Encode(data); err != nil {
		file.Close()
		os.Remove(tmpPath)
		return err
	}

	if err := file.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}

	return os.Rename(tmpPath, c.indexFilePath())
}

// compactPathTable removes unused paths and remaps indices
// Must be called with write lock held
func (c *CallerIndex) compactPathTable() {
	// Collect all used path indices
	usedIndices := make(map[int]bool)
	for _, callerList := range c.callers {
		for _, caller := range callerList {
			usedIndices[caller.PathIdx] = true
		}
	}

	// If all paths are used, nothing to compact
	if len(usedIndices) == len(c.pathTable) {
		return
	}

	// Build new path table with only used paths
	oldToNew := make(map[int]int) // old index -> new index
	newPathTable := make([]string, 0, len(usedIndices))
	newPathLookup := make(map[string]int, len(usedIndices))

	for oldIdx, path := range c.pathTable {
		if usedIndices[oldIdx] {
			newIdx := len(newPathTable)
			oldToNew[oldIdx] = newIdx
			newPathTable = append(newPathTable, path)
			newPathLookup[path] = newIdx
		}
	}

	// Remap all caller indices
	for symbol, callerList := range c.callers {
		for i := range callerList {
			callerList[i].PathIdx = oldToNew[callerList[i].PathIdx]
		}
		c.callers[symbol] = callerList
	}

	// Replace old tables
	c.pathTable = newPathTable
	c.pathLookup = newPathLookup
}

// AddChunkCalls indexes all calls made by a chunk.
// For each symbol that this chunk calls, we record the chunk as a caller.
func (c *CallerIndex) AddChunkCalls(chunk types.Chunk) {
	if len(chunk.Calls) == 0 || chunk.Name == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Get or create path index for deduplication
	pathIdx := c.getOrAddPath(chunk.FilePath)

	caller := CompactCaller{
		Name:     chunk.Name,
		PathIdx:  pathIdx,
		Line:     chunk.StartLine,
		Language: chunk.Language,
		IsTest:   chunk.IsTest,
		Parent:   chunk.Parent,
	}

	for _, calledSymbol := range chunk.Calls {
		// Normalize the called symbol name
		calledSymbol = strings.TrimSpace(calledSymbol)
		if calledSymbol == "" {
			continue
		}

		// Add this chunk as a caller of the symbol (only store the full name)
		// Short name lookups are handled in FindCallers
		c.callers[calledSymbol] = append(c.callers[calledSymbol], caller)
	}
}

// RemoveFileCalls removes all caller entries for chunks from a specific file.
// Called when a file is re-indexed or deleted.
func (c *CallerIndex) RemoveFileCalls(absolutePath string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Find the path index for this file
	pathIdx, exists := c.pathLookup[absolutePath]
	if !exists {
		return // File not in index
	}

	for symbol, callerList := range c.callers {
		filtered := make([]CompactCaller, 0, len(callerList))
		for _, caller := range callerList {
			if caller.PathIdx != pathIdx {
				filtered = append(filtered, caller)
			}
		}
		if len(filtered) == 0 {
			delete(c.callers, symbol)
		} else {
			c.callers[symbol] = filtered
		}
	}
}

// FindCallers returns all callers of a symbol (O(1) lookup)
// Also searches for method calls (e.g., "obj.Method" matches "Method")
func (c *CallerIndex) FindCallers(symbolName string, maxResults int) []types.CallerInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Collect callers from exact match
	callerList := c.callers[symbolName]

	// Also search for method calls that end with .symbolName
	shortSuffix := "." + symbolName
	for key, callers := range c.callers {
		if strings.HasSuffix(key, shortSuffix) {
			callerList = append(callerList, callers...)
		}
	}

	if len(callerList) == 0 {
		return nil
	}

	// Deduplicate by name and convert to CallerInfo
	seen := make(map[string]bool)
	result := make([]types.CallerInfo, 0, len(callerList))

	for _, caller := range callerList {
		if seen[caller.Name] {
			continue
		}
		seen[caller.Name] = true

		// Convert CompactCaller to CallerInfo
		result = append(result, types.CallerInfo{
			Name:     caller.Name,
			FilePath: c.getPath(caller.PathIdx),
			Line:     caller.Line,
			Language: caller.Language,
			IsTest:   caller.IsTest,
			Parent:   caller.Parent,
		})

		if maxResults > 0 && len(result) >= maxResults {
			break
		}
	}

	return result
}

// FindCallersDeep finds callers up to N levels deep using the index
func (c *CallerIndex) FindCallersDeep(symbolName string, depth int, maxPerLevel int) map[int][]types.CallerInfo {
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
			callers := c.FindCallers(sym, maxPerLevel)

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

	return result
}

// HasCallers returns true if the symbol has any callers
func (c *CallerIndex) HasCallers(symbolName string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	callers, ok := c.callers[symbolName]
	return ok && len(callers) > 0
}

// HasTestCaller returns true if any caller is a test
func (c *CallerIndex) HasTestCaller(symbolName string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	callers, ok := c.callers[symbolName]
	if !ok {
		return false
	}

	for _, caller := range callers {
		if caller.IsTest {
			return true
		}
	}
	return false
}

// Clear removes all entries from the index
func (c *CallerIndex) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.callers = make(map[string][]CompactCaller)
	c.pathTable = make([]string, 0)
	c.pathLookup = make(map[string]int)
}

// Stats returns statistics about the index
func (c *CallerIndex) Stats() (symbolCount int, totalCallers int) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	symbolCount = len(c.callers)
	for _, callers := range c.callers {
		totalCallers += len(callers)
	}
	return
}
