package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"mcp-semantic-search/config"
	"mcp-semantic-search/types"
)

// CallerIndex provides O(1) lookup for finding callers of any symbol.
// This replaces the slow semantic search approach in FindCallers.
type CallerIndex struct {
	cfg *config.Config

	// callers maps symbolName -> list of callers
	// Key is the called symbol name (e.g., "SearchWithUsage", "main")
	// Value is list of CallerInfo for functions that call this symbol
	callers map[string][]types.CallerInfo

	mu sync.RWMutex
}

// NewCallerIndex creates a new caller index
func NewCallerIndex(cfg *config.Config) *CallerIndex {
	idx := &CallerIndex{
		cfg:     cfg,
		callers: make(map[string][]types.CallerInfo),
	}
	// Try to load existing index
	_ = idx.Load()
	return idx
}

// indexFilePath returns the path to the caller index file
func (c *CallerIndex) indexFilePath() string {
	return filepath.Join(c.cfg.DBPath, "caller_index.json")
}

// Load loads the caller index from disk
func (c *CallerIndex) Load() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := os.ReadFile(c.indexFilePath())
	if err != nil {
		if os.IsNotExist(err) {
			c.callers = make(map[string][]types.CallerInfo)
			return nil
		}
		return err
	}

	return json.Unmarshal(data, &c.callers)
}

// Save persists the caller index to disk
func (c *CallerIndex) Save() error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	data, err := json.MarshalIndent(c.callers, "", "  ")
	if err != nil {
		return err
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(c.indexFilePath()), 0755); err != nil {
		return err
	}

	// Write atomically
	tmpPath := c.indexFilePath() + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, c.indexFilePath())
}

// AddChunkCalls indexes all calls made by a chunk.
// For each symbol that this chunk calls, we record the chunk as a caller.
func (c *CallerIndex) AddChunkCalls(chunk types.Chunk) {
	if len(chunk.Calls) == 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Parse line number from chunk
	startLine := chunk.StartLine

	callerInfo := types.CallerInfo{
		Name:     chunk.Name,
		FilePath: chunk.FilePath,
		Line:     startLine,
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

		// Add this chunk as a caller of the symbol
		c.callers[calledSymbol] = append(c.callers[calledSymbol], callerInfo)

		// Also index the short name if it's a method call (e.g., "idx.SearchWithUsage" -> "SearchWithUsage")
		if idx := strings.LastIndex(calledSymbol, "."); idx != -1 {
			shortName := calledSymbol[idx+1:]
			if shortName != "" {
				c.callers[shortName] = append(c.callers[shortName], callerInfo)
			}
		}
	}
}

// RemoveFileCalls removes all caller entries for chunks from a specific file.
// Called when a file is re-indexed or deleted.
func (c *CallerIndex) RemoveFileCalls(absolutePath string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for symbol, callerList := range c.callers {
		filtered := make([]types.CallerInfo, 0, len(callerList))
		for _, caller := range callerList {
			if caller.FilePath != absolutePath {
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
func (c *CallerIndex) FindCallers(symbolName string, maxResults int) []types.CallerInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	callerList, ok := c.callers[symbolName]
	if !ok {
		return nil
	}

	// Deduplicate by name (same function might call multiple times)
	seen := make(map[string]bool)
	result := make([]types.CallerInfo, 0, len(callerList))

	for _, caller := range callerList {
		if seen[caller.Name] {
			continue
		}
		seen[caller.Name] = true
		result = append(result, caller)

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
	c.callers = make(map[string][]types.CallerInfo)
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
