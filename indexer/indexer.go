package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mcp-semantic-search/config"
	"mcp-semantic-search/store"
	"mcp-semantic-search/types"
	"mcp-semantic-search/watcher"
)

// computeFileHash computes SHA256 hash of content
func computeFileHash(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

// ProgressCallback is called during indexing to report progress
type ProgressCallback func(event types.ProgressEvent)

// Indexer orchestrates the indexing process
type Indexer struct {
	cfg            *config.Config
	store          *store.Store
	hashStore      *store.FileHashStore
	embedder       *Embedder
	chunker        *Chunker
	watcherMgr     *watcher.WatcherManager
	indexingMu     sync.Mutex // Prevent concurrent indexing of same folder
	progressCb     ProgressCallback
	progressCbLock sync.RWMutex
}

// NewIndexer creates a new Indexer instance
func NewIndexer(cfg *config.Config, st *store.Store, hashStore *store.FileHashStore, embedder *Embedder) *Indexer {
	return &Indexer{
		cfg:       cfg,
		store:     st,
		hashStore: hashStore,
		embedder:  embedder,
		chunker:   NewChunker(cfg.MaxChunkSize, cfg.ChunkOverlap),
	}
}

// SetWatcherManager sets the watcher manager for the indexer
// This is called after creation to avoid circular dependencies
func (idx *Indexer) SetWatcherManager(wm *watcher.WatcherManager) {
	idx.watcherMgr = wm
}

// SetProgressCallback sets a callback function for progress updates
func (idx *Indexer) SetProgressCallback(cb ProgressCallback) {
	idx.progressCbLock.Lock()
	defer idx.progressCbLock.Unlock()
	idx.progressCb = cb
}

// sendProgress sends a progress event if a callback is set
func (idx *Indexer) sendProgress(event types.ProgressEvent) {
	idx.progressCbLock.RLock()
	cb := idx.progressCb
	idx.progressCbLock.RUnlock()
	if cb != nil {
		cb(event)
	}
}

// ScanProject scans a folder and returns file info without indexing
func (idx *Indexer) ScanProject(ctx context.Context, projectPath string) (*types.ScanResult, error) {
	// Resolve absolute path
	absPath, err := filepath.Abs(projectPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve path: %w", err)
	}

	idx.sendProgress(types.ProgressEvent{
		Type:    "scanning",
		Project: filepath.Base(absPath),
		Message: "Scanning folder for files...",
	})

	// Create scanner
	scanner, err := NewScanner(idx.cfg, absPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create scanner: %w", err)
	}

	// Scan for files
	files, err := scanner.Scan()
	if err != nil {
		return nil, fmt.Errorf("failed to scan directory: %w", err)
	}

	// Load existing hashes to determine new/modified/unchanged
	if err := idx.hashStore.LoadProjectHashes(absPath); err != nil {
		log.Printf("Warning: failed to load file hashes for %s: %v", absPath, err)
	}

	// Build current file hash map and count by language
	currentFiles := make(map[string]string)
	byLanguage := make(map[string]int)
	var totalSize int64

	for _, f := range files {
		currentFiles[f.RelativePath] = f.Hash
		byLanguage[f.Language]++
		totalSize += f.Size
	}

	// Get change stats
	added, modified, _ := idx.hashStore.GetChangedFiles(absPath, currentFiles)
	unchanged := len(files) - len(added) - len(modified)

	idx.sendProgress(types.ProgressEvent{
		Type:    "scan_complete",
		Project: filepath.Base(absPath),
		Message: fmt.Sprintf("Found %d files (%d new, %d modified, %d unchanged)", len(files), len(added), len(modified), unchanged),
		Total:   len(files),
	})

	return &types.ScanResult{
		Path:           absPath,
		TotalFiles:     len(files),
		TotalSize:      totalSize,
		Files:          files,
		NewFiles:       len(added),
		ModifiedFiles:  len(modified),
		UnchangedFiles: unchanged,
		ByLanguage:     byLanguage,
	}, nil
}

// IndexFolder indexes a folder with incremental support using global collection
func (idx *Indexer) IndexProject(ctx context.Context, folderPath string, enableWatch bool) (*types.IndexResult, error) {
	startTime := time.Now()

	// Resolve absolute path
	absPath, err := filepath.Abs(folderPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve path: %w", err)
	}

	folderName := filepath.Base(absPath)

	// Prevent concurrent indexing
	idx.indexingMu.Lock()
	defer idx.indexingMu.Unlock()

	idx.sendProgress(types.ProgressEvent{
		Type:    "indexing_started",
		Project: folderName,
		Message: "Starting indexing...",
	})

	// Load existing file hashes for incremental indexing
	if err := idx.hashStore.LoadProjectHashes(absPath); err != nil {
		log.Printf("Warning: failed to load file hashes for %s: %v", absPath, err)
	}

	idx.sendProgress(types.ProgressEvent{
		Type:    "scanning",
		Project: folderName,
		Message: "Scanning folder for files...",
	})

	// Create scanner
	scanner, err := NewScanner(idx.cfg, absPath)
	if err != nil {
		idx.sendProgress(types.ProgressEvent{
			Type:    "error",
			Project: folderName,
			Message: "Failed to create scanner",
			Error:   err.Error(),
		})
		return nil, fmt.Errorf("failed to create scanner: %w", err)
	}

	// Scan for files
	files, err := scanner.Scan()
	if err != nil {
		idx.sendProgress(types.ProgressEvent{
			Type:    "error",
			Project: folderName,
			Message: "Failed to scan directory",
			Error:   err.Error(),
		})
		return nil, fmt.Errorf("failed to scan directory: %w", err)
	}

	// Build current file hash map (keyed by absolute path for global uniqueness)
	currentFiles := make(map[string]string)
	fileInfoMap := make(map[string]types.FileInfo)
	for _, f := range files {
		currentFiles[f.Path] = f.Hash // Use absolute path as key
		fileInfoMap[f.Path] = f
	}

	// Get changed files (incremental indexing)
	added, modified, deleted := idx.hashStore.GetChangedFiles(absPath, currentFiles)

	idx.sendProgress(types.ProgressEvent{
		Type:    "scan_complete",
		Project: folderName,
		Message: fmt.Sprintf("Found %d files (%d new, %d modified, %d unchanged)", len(files), len(added), len(modified), len(files)-len(added)-len(modified)),
		Total:   len(added) + len(modified),
	})

	// Process changes
	totalChunks := 0
	filesProcessed := 0

	// Delete chunks for removed/modified files (using absolute paths)
	for _, absFilePath := range deleted {
		if err := idx.store.DeleteFileChunks(ctx, absFilePath); err != nil {
			log.Printf("Warning: failed to delete chunks for %s: %v", absFilePath, err)
		}
		idx.hashStore.RemoveFileHash(absPath, absFilePath)
	}

	for _, absFilePath := range modified {
		if err := idx.store.DeleteFileChunks(ctx, absFilePath); err != nil {
			log.Printf("Warning: failed to delete chunks for %s: %v", absFilePath, err)
		}
	}

	// Process new and modified files
	filesToProcess := append(added, modified...)
	totalToProcess := len(filesToProcess)

	for i, absFilePath := range filesToProcess {
		select {
		case <-ctx.Done():
			idx.sendProgress(types.ProgressEvent{
				Type:    "error",
				Project: folderName,
				Message: "Indexing cancelled",
				Error:   "cancelled",
			})
			return nil, ctx.Err()
		default:
		}

		// Send progress with relative path for display
		relPath, _ := filepath.Rel(absPath, absFilePath)
		percent := float64(i+1) / float64(totalToProcess) * 100
		idx.sendProgress(types.ProgressEvent{
			Type:    "embedding",
			Project: folderName,
			Message: fmt.Sprintf("Embedding file %d/%d", i+1, totalToProcess),
			Current: i + 1,
			Total:   totalToProcess,
			Percent: percent,
			File:    relPath,
		})

		file := fileInfoMap[absFilePath]
		chunks, err := idx.processFile(ctx, file)
		if err != nil {
			log.Printf("Warning: failed to process %s: %v", absFilePath, err)
			continue
		}

		if len(chunks) > 0 {
			if err := idx.store.AddChunks(ctx, chunks); err != nil {
				log.Printf("Warning: failed to add chunks for %s: %v", absFilePath, err)
				continue
			}
			totalChunks += len(chunks)
		}

		// Update file hash
		idx.hashStore.SetFileHash(absPath, absFilePath, file.Hash)
		filesProcessed++
	}

	// Save file hashes
	if err := idx.hashStore.SaveProjectHashes(absPath); err != nil {
		log.Printf("Warning: failed to save file hashes: %v", err)
	}

	// Start file watcher if enabled
	if enableWatch && idx.cfg.WatchEnabled {
		idx.startWatcher(absPath)
	}

	elapsed := time.Since(startTime)

	idx.sendProgress(types.ProgressEvent{
		Type:    "complete",
		Project: folderName,
		Message: fmt.Sprintf("Indexing complete: %d files, %d chunks in %dms", filesProcessed, totalChunks, elapsed.Milliseconds()),
		Current: totalToProcess,
		Total:   totalToProcess,
		Percent: 100,
	})

	return &types.IndexResult{
		Status:       "success",
		Project:      folderName,
		FilesIndexed: filesProcessed,
		ChunksStored: totalChunks,
		TimeTakenMs:  elapsed.Milliseconds(),
		Skipped:      len(files) - filesProcessed,
		Deleted:      len(deleted),
	}, nil
}

// processFile reads and chunks a single file
func (idx *Indexer) processFile(ctx context.Context, file types.FileInfo) ([]types.Chunk, error) {
	// Read file content
	content, err := ReadFileContent(file.Path)
	if err != nil {
		return nil, err
	}

	// Skip empty or binary files
	if content == "" {
		return nil, nil
	}

	// Chunk the file
	chunks := idx.chunker.ChunkFile(content, file.RelativePath, file.Language)

	// Assign IDs and absolute paths to chunks
	for i := range chunks {
		chunks[i].ID = store.GenerateChunkID(file.Path, i) // Use absolute path for ID
		chunks[i].FilePath = file.Path                     // Store absolute path
		chunks[i].Language = file.Language
	}

	return chunks, nil
}

// ReindexProject forces a complete reindex of a folder
func (idx *Indexer) ReindexProject(ctx context.Context, folderPath string) (*types.IndexResult, error) {
	absPath, err := filepath.Abs(folderPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve path: %w", err)
	}

	// Stop existing watcher
	idx.stopWatcher(absPath)

	// Delete file hashes to force full reindex (chunks will be replaced during indexing)
	if err := idx.hashStore.DeleteProjectHashes(absPath); err != nil {
		log.Printf("Warning: failed to delete file hashes: %v", err)
	}

	// Reindex
	return idx.IndexProject(ctx, folderPath, true)
}

// RemoveProject removes all indexed files from a folder
func (idx *Indexer) RemoveProject(ctx context.Context, folderPath string) error {
	absPath, err := filepath.Abs(folderPath)
	if err != nil {
		return fmt.Errorf("failed to resolve path: %w", err)
	}

	// Stop watcher
	idx.stopWatcher(absPath)

	// Load file hashes to get list of indexed files
	if err := idx.hashStore.LoadProjectHashes(absPath); err != nil {
		log.Printf("Warning: failed to load file hashes: %v", err)
	}

	// Get all indexed files and delete their chunks
	indexedFiles := idx.hashStore.GetAllFilePaths(absPath)
	for _, filePath := range indexedFiles {
		if err := idx.store.DeleteFileChunks(ctx, filePath); err != nil {
			log.Printf("Warning: failed to delete chunks for %s: %v", filePath, err)
		}
	}

	// Delete file hashes
	if err := idx.hashStore.DeleteProjectHashes(absPath); err != nil {
		log.Printf("Warning: failed to delete file hashes: %v", err)
	}

	return nil
}

// Search performs semantic search across the global index
func (idx *Indexer) Search(ctx context.Context, query string, opts types.SearchOptions) ([]types.SearchResult, error) {
	// Get current working directory for relative path computation
	cwd, _ := filepath.Abs(".")

	return idx.store.Search(ctx, query, cwd, opts)
}

// SearchWithUsage performs semantic search and includes usage information
func (idx *Indexer) SearchWithUsage(ctx context.Context, query string, opts types.SearchOptions) (*types.SearchResponse, error) {
	// Get current working directory for relative path computation
	cwd, _ := filepath.Abs(".")

	// Get base search results with filtering
	results, err := idx.store.Search(ctx, query, cwd, opts)
	if err != nil {
		return nil, err
	}

	// Process results in parallel for faster response
	var wg sync.WaitGroup
	var graphMu sync.Mutex
	graphNodes := make([]types.GraphNode, 0)
	graphEdges := make([]types.GraphEdge, 0)
	seenNodes := make(map[string]bool)

	for i := range results {
		if results[i].Name == "" {
			continue
		}

		wg.Add(1)
		go func(result *types.SearchResult) {
			defer wg.Done()

			// Get metadata for this result
			metadata, err := idx.store.GetChunkMetadata(ctx, result.Name)
			if err != nil {
				return
			}

			// Parse calls and look up their locations in parallel
			var callInfos []types.CallInfo
			var references []string
			if metadata != nil {
				if callsStr := metadata["calls"]; callsStr != "" {
					callNames := splitAndTrim(callsStr)
					// Lookup call locations in parallel
					callInfosChan := make(chan types.CallInfo, len(callNames))
					var callWg sync.WaitGroup
					for _, callName := range callNames {
						callWg.Add(1)
						go func(name string) {
							defer callWg.Done()
							callInfo, err := idx.store.FindSymbolLocation(ctx, name)
							if err != nil {
								callInfo = &types.CallInfo{Name: name, IsExternal: true}
							}
							// Convert absolute path to relative
							if callInfo.FilePath != "" && cwd != "" {
								if rel, err := filepath.Rel(cwd, callInfo.FilePath); err == nil {
									callInfo.FilePath = "./" + filepath.ToSlash(rel)
								}
							}
							callInfosChan <- *callInfo
						}(callName)
					}
					callWg.Wait()
					close(callInfosChan)
					for ci := range callInfosChan {
						callInfos = append(callInfos, ci)
					}
				}
				if refsStr := metadata["references"]; refsStr != "" {
					references = splitAndTrim(refsStr)
				}
			}

			// Find callers (3 levels deep)
			callersByLevel, err := idx.store.FindCallersDeep(ctx, result.Name, 3, 10)
			if err != nil {
				log.Printf("Warning: failed to find callers for %s: %v", result.Name, err)
			}

			// Flatten callers for the result
			allCallers := make([]types.CallerInfo, 0)
			hasTestCaller := false
			for level := 1; level <= 3; level++ {
				if callers, ok := callersByLevel[level]; ok {
					for _, caller := range callers {
						// Convert absolute path to relative
						relPath := caller.FilePath
						if cwd != "" {
							if rel, err := filepath.Rel(cwd, caller.FilePath); err == nil {
								relPath = "./" + filepath.ToSlash(rel)
							}
						}
						caller.FilePath = relPath
						allCallers = append(allCallers, caller)

						if caller.IsTest {
							hasTestCaller = true
						}
					}
				}
			}

			isExported := metadata != nil && metadata["is_exported"] == "true"
			isTest := metadata != nil && metadata["is_test"] == "true"
			isUnused := isExported && len(allCallers) == 0
			notTested := isExported && !isTest && !hasTestCaller

			result.Usage = &types.UsageInfo{
				Calls:      callInfos,
				CalledBy:   allCallers,
				References: references,
				IsExported: isExported,
				IsTest:     isTest,
				IsUnused:   isUnused,
				NotTested:  notTested,
			}

			// Build graph nodes and edges (thread-safe)
			graphMu.Lock()
			defer graphMu.Unlock()

			if !seenNodes[result.Name] {
				seenNodes[result.Name] = true
				graphNodes = append(graphNodes, types.GraphNode{
					ID:         result.Name,
					Type:       result.ChunkType,
					FilePath:   result.FilePath,
					IsExported: isExported,
					IsTest:     isTest,
					IsUnused:   isUnused,
				})
			}

			// Add edges for calls
			for _, call := range callInfos {
				graphEdges = append(graphEdges, types.GraphEdge{
					From:  result.Name,
					To:    call.Name,
					Count: 1,
				})
			}

			// Add edges for callers
			for _, caller := range allCallers {
				if !seenNodes[caller.Name] {
					seenNodes[caller.Name] = true
					graphNodes = append(graphNodes, types.GraphNode{
						ID:       caller.Name,
						Type:     "function", // Assume function
						FilePath: caller.FilePath,
						IsTest:   caller.IsTest,
					})
				}
				graphEdges = append(graphEdges, types.GraphEdge{
					From:  caller.Name,
					To:    result.Name,
					Count: 1,
				})
			}
		}(&results[i])
	}

	// Wait for all parallel processing to complete
	wg.Wait()

	return &types.SearchResponse{
		Count:   len(results),
		Results: results,
		Graph: &types.UsageGraph{
			Nodes: graphNodes,
			Edges: graphEdges,
		},
	}, nil
}

// splitAndTrim splits a comma-separated string and trims whitespace
func splitAndTrim(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// GetStatus returns the status of the global index
func (idx *Indexer) GetStatus(ctx context.Context) (*types.StatusResult, error) {
	// Get total chunk count from the global collection
	totalChunks := idx.store.GetTotalChunkCount()

	// Test Ollama connection
	ollamaStatus := "connected"
	if err := idx.embedder.TestConnection(ctx); err != nil {
		ollamaStatus = "disconnected"
	}

	// Get current working directory
	cwd, _ := filepath.Abs(".")

	return &types.StatusResult{
		TotalChunks:   totalChunks,
		OllamaStatus:  ollamaStatus,
		DBPath:        idx.cfg.DBPath,
		CurrentFolder: cwd,
	}, nil
}

// UpdateFile updates the index for a single file (called by watcher)
func (idx *Indexer) UpdateFile(ctx context.Context, folderPath, filePath string) error {
	absFilePath, err := filepath.Abs(filePath)
	if err != nil {
		return err
	}

	absFolderPath, err := filepath.Abs(folderPath)
	if err != nil {
		return err
	}

	relPath, _ := filepath.Rel(absFolderPath, absFilePath)
	log.Printf("Watcher: Re-indexing file: %s", relPath)

	// Send progress event for UI
	idx.sendProgress(types.ProgressEvent{
		Type:    "file_update",
		Project: filepath.Base(absFolderPath),
		Message: fmt.Sprintf("Re-indexing: %s", relPath),
		File:    relPath,
	})

	// Delete existing chunks for this file
	if err := idx.store.DeleteFileChunks(ctx, absFilePath); err != nil {
		log.Printf("Warning: failed to delete existing chunks: %v", err)
	}

	// Read and reindex file
	content, err := ReadFileContent(absFilePath)
	if err != nil {
		log.Printf("Watcher: Failed to read file %s: %v", relPath, err)
		return err
	}
	if content == "" {
		log.Printf("Watcher: Skipping empty/binary file: %s", relPath)
		return nil
	}

	// Calculate file hash
	hash := computeFileHash(content)

	language := detectLanguage(absFilePath)
	chunks := idx.chunker.ChunkFile(content, relPath, language)
	log.Printf("Watcher: Created %d chunks for %s", len(chunks), relPath)

	for i := range chunks {
		chunks[i].ID = store.GenerateChunkID(absFilePath, i)
		chunks[i].FilePath = absFilePath // Store absolute path
		chunks[i].Language = language
	}

	if len(chunks) > 0 {
		log.Printf("Watcher: Embedding %d chunks for %s...", len(chunks), relPath)
		if err := idx.store.AddChunks(ctx, chunks); err != nil {
			log.Printf("Watcher: Failed to embed chunks for %s: %v", relPath, err)
			idx.sendProgress(types.ProgressEvent{
				Type:    "file_update_error",
				Project: filepath.Base(absFolderPath),
				Message: fmt.Sprintf("Failed to re-index: %s", relPath),
				File:    relPath,
				Error:   err.Error(),
			})
			return err
		}
		log.Printf("Watcher: Successfully re-indexed %s (%d chunks)", relPath, len(chunks))
	}

	// Update hash store
	idx.hashStore.SetFileHash(absFolderPath, absFilePath, hash)
	if err := idx.hashStore.SaveProjectHashes(absFolderPath); err != nil {
		log.Printf("Warning: failed to save file hash: %v", err)
	}

	// Send completion event
	idx.sendProgress(types.ProgressEvent{
		Type:    "file_update_complete",
		Project: filepath.Base(absFolderPath),
		Message: fmt.Sprintf("Re-indexed: %s (%d chunks)", relPath, len(chunks)),
		File:    relPath,
	})

	return nil
}

// DeleteFile removes a file from the index (called by watcher)
func (idx *Indexer) DeleteFile(ctx context.Context, filePath string) error {
	absFilePath, err := filepath.Abs(filePath)
	if err != nil {
		return err
	}

	log.Printf("Watcher: Removing file from index: %s", absFilePath)

	// Delete from vector store
	if err := idx.store.DeleteFileChunks(ctx, absFilePath); err != nil {
		log.Printf("Watcher: Failed to delete chunks for %s: %v", absFilePath, err)
		return err
	}

	// Update hash store - find which folder this file belongs to
	folders := idx.hashStore.ListIndexedFolders()
	for _, folder := range folders {
		if hasPrefix(absFilePath, folder) {
			idx.hashStore.RemoveFileHash(folder, absFilePath)
			if err := idx.hashStore.SaveProjectHashes(folder); err != nil {
				log.Printf("Warning: failed to save project hashes: %v", err)
			}

			relPath, _ := filepath.Rel(folder, absFilePath)
			idx.sendProgress(types.ProgressEvent{
				Type:    "file_deleted",
				Project: filepath.Base(folder),
				Message: fmt.Sprintf("Removed from index: %s", relPath),
				File:    relPath,
			})
			log.Printf("Watcher: Successfully removed %s from index", relPath)
			break
		}
	}

	return nil
}

// DeleteFolder removes all files in a folder from the index (called by watcher)
func (idx *Indexer) DeleteFolder(ctx context.Context, folderPath string) error {
	absFolderPath, err := filepath.Abs(folderPath)
	if err != nil {
		return err
	}

	log.Printf("Watcher: Removing folder from index: %s", absFolderPath)

	// Find which indexed folder contains this subfolder
	folders := idx.hashStore.ListIndexedFolders()
	deletedCount := 0

	for _, folder := range folders {
		if hasPrefix(absFolderPath, folder) {
			// Get all files that start with this folder path
			allFiles := idx.hashStore.GetAllFilePaths(folder)
			for _, filePath := range allFiles {
				if hasPrefix(filePath, absFolderPath) {
					// Delete from vector store
					if err := idx.store.DeleteFileChunks(ctx, filePath); err != nil {
						log.Printf("Warning: failed to delete chunks for %s: %v", filePath, err)
					}
					// Remove from hash store
					idx.hashStore.RemoveFileHash(folder, filePath)
					deletedCount++
				}
			}
			// Save updated hashes
			if err := idx.hashStore.SaveProjectHashes(folder); err != nil {
				log.Printf("Warning: failed to save project hashes: %v", err)
			}

			relPath, _ := filepath.Rel(folder, absFolderPath)
			idx.sendProgress(types.ProgressEvent{
				Type:    "folder_deleted",
				Project: filepath.Base(folder),
				Message: fmt.Sprintf("Removed folder: %s (%d files)", relPath, deletedCount),
				File:    relPath,
			})
			log.Printf("Watcher: Successfully removed folder %s (%d files)", relPath, deletedCount)
			break
		}
	}

	return nil
}

// hasPrefix checks if path starts with prefix (handles path separators properly)
func hasPrefix(path, prefix string) bool {
	// Normalize paths
	path = filepath.Clean(path)
	prefix = filepath.Clean(prefix)

	// Check if path starts with prefix
	if len(path) < len(prefix) {
		return false
	}
	if path[:len(prefix)] != prefix {
		return false
	}
	// Ensure it's a proper path prefix (not partial match)
	if len(path) > len(prefix) && path[len(prefix)] != filepath.Separator {
		return false
	}
	return true
}

// startWatcher starts a file watcher for a project
func (idx *Indexer) startWatcher(projectPath string) {
	if idx.watcherMgr == nil {
		log.Printf("Warning: watcher manager not set, cannot watch %s", projectPath)
		return
	}

	if err := idx.watcherMgr.StartWatching(projectPath); err != nil {
		log.Printf("Failed to start watcher for %s: %v", projectPath, err)
	} else {
		log.Printf("Started watching: %s", projectPath)
	}
}

// stopWatcher stops the file watcher for a project
func (idx *Indexer) stopWatcher(projectPath string) {
	if idx.watcherMgr == nil {
		return
	}

	if err := idx.watcherMgr.StopWatching(projectPath); err != nil {
		log.Printf("Failed to stop watcher for %s: %v", projectPath, err)
	}
}

// Close shuts down the indexer
func (idx *Indexer) Close() {
	// Watcher cleanup is handled by WatcherManager.StopAll()
}
