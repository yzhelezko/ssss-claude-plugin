package watcher

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mcp-semantic-search/config"

	"github.com/bep/debounce"
	"github.com/fsnotify/fsnotify"
	ignore "github.com/sabhiram/go-gitignore"
)

// FileHandler is the interface for handling file changes
type FileHandler interface {
	UpdateFile(ctx context.Context, folderPath, filePath string) error
	DeleteFile(ctx context.Context, filePath string) error
	DeleteFolder(ctx context.Context, folderPath string) error
}

// Watcher monitors a project directory for file changes
type Watcher struct {
	projectPath   string
	cfg           *config.Config
	handler       FileHandler
	watcher       *fsnotify.Watcher
	ignorer       *ignore.GitIgnore
	debouncer     func(func())
	stopChan      chan struct{}
	mu            sync.Mutex
	pending       map[string]fsnotify.Op
	watchedDirs   map[string]bool // Track watched directories to detect folder deletions
	watchedDirsMu sync.RWMutex
}

// NewWatcher creates a new file watcher for a project
func NewWatcher(projectPath string, cfg *config.Config, handler FileHandler) (*Watcher, error) {
	fsWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	w := &Watcher{
		projectPath: projectPath,
		cfg:         cfg,
		handler:     handler,
		watcher:     fsWatcher,
		stopChan:    make(chan struct{}),
		pending:     make(map[string]fsnotify.Op),
		watchedDirs: make(map[string]bool),
	}

	// Load .gitignore
	gitignorePath := filepath.Join(projectPath, ".gitignore")
	if _, err := os.Stat(gitignorePath); err == nil {
		w.ignorer, _ = ignore.CompileIgnoreFile(gitignorePath)
	}

	// Create debouncer
	debounceTime := time.Duration(cfg.DebounceMs) * time.Millisecond
	w.debouncer = debounce.New(debounceTime)

	return w, nil
}

// Start begins watching the project directory
func (w *Watcher) Start() error {
	// Add all directories to watcher
	if err := w.addWatchRecursive(w.projectPath); err != nil {
		return err
	}

	// Start event processing goroutine
	go w.processEvents()

	return nil
}

// Stop stops the watcher
func (w *Watcher) Stop() error {
	close(w.stopChan)
	return w.watcher.Close()
}

// addWatchRecursive adds a directory and all subdirectories to the watcher
func (w *Watcher) addWatchRecursive(path string) error {
	return filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip inaccessible paths
		}

		if !info.IsDir() {
			return nil
		}

		// Check if directory should be excluded
		if w.shouldExcludeDir(info.Name(), p) {
			return filepath.SkipDir
		}

		// Add directory to watcher
		if err := w.watcher.Add(p); err != nil {
			// Log but continue
			log.Printf("Failed to watch %s: %v", p, err)
		} else {
			// Track watched directory
			w.watchedDirsMu.Lock()
			w.watchedDirs[p] = true
			w.watchedDirsMu.Unlock()
		}

		return nil
	})
}

// shouldExcludeDir checks if a directory should be excluded from watching
func (w *Watcher) shouldExcludeDir(name, path string) bool {
	// Always exclude certain directories
	if w.cfg.IsExcludedDir(name) {
		return true
	}

	// Check .gitignore
	if w.ignorer != nil {
		relPath, err := filepath.Rel(w.projectPath, path)
		if err == nil && w.ignorer.MatchesPath(relPath+"/") {
			return true
		}
	}

	return false
}

// shouldProcessFile checks if a file should trigger an update
func (w *Watcher) shouldProcessFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}

	if info.IsDir() {
		return false
	}

	// Check file size
	if info.Size() > w.cfg.MaxFileSize {
		return false
	}

	// Check extension
	ext := strings.ToLower(filepath.Ext(path))
	if w.cfg.IsExcludedExt(ext) {
		return false
	}

	// Check .gitignore
	if w.ignorer != nil {
		relPath, err := filepath.Rel(w.projectPath, path)
		if err == nil && w.ignorer.MatchesPath(relPath) {
			return false
		}
	}

	return true
}

// processEvents handles file system events
func (w *Watcher) processEvents() {
	for {
		select {
		case <-w.stopChan:
			return

		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			w.handleEvent(event)

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("Watcher error: %v", err)
		}
	}
}

// handleEvent processes a single file system event
func (w *Watcher) handleEvent(event fsnotify.Event) {
	// Handle directory creation - need to watch new directories
	if event.Has(fsnotify.Create) {
		if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
			if !w.shouldExcludeDir(info.Name(), event.Name) {
				w.watcher.Add(event.Name)
				w.watchedDirsMu.Lock()
				w.watchedDirs[event.Name] = true
				w.watchedDirsMu.Unlock()
			}
			return
		}
	}

	// Handle removal - check if it was a watched directory
	if event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
		w.watchedDirsMu.RLock()
		isDir := w.watchedDirs[event.Name]
		w.watchedDirsMu.RUnlock()

		if isDir {
			// Directory was deleted - untrack it
			w.watchedDirsMu.Lock()
			delete(w.watchedDirs, event.Name)
			w.watchedDirsMu.Unlock()
		}

		// Queue the event for processing (file or folder)
		w.mu.Lock()
		if isDir {
			w.pending[event.Name] = event.Op | 0x100 // Mark as directory with high bit
		} else {
			w.pending[event.Name] = event.Op
		}
		w.mu.Unlock()

		w.debouncer(w.flushPending)
		return
	}

	// Skip if file should not be processed
	if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
		if !w.shouldProcessFile(event.Name) {
			return
		}
	}

	// Queue the event for debounced processing
	w.mu.Lock()
	w.pending[event.Name] = event.Op
	w.mu.Unlock()

	// Debounce the flush
	w.debouncer(w.flushPending)
}

// flushPending processes all pending events
func (w *Watcher) flushPending() {
	w.mu.Lock()
	pending := w.pending
	w.pending = make(map[string]fsnotify.Op)
	w.mu.Unlock()

	ctx := context.Background()

	for path, op := range pending {
		// Check if this was marked as a directory (high bit set)
		isDir := op&0x100 != 0
		op = op & 0xFF // Clear the directory marker

		if op.Has(fsnotify.Remove) || op.Has(fsnotify.Rename) {
			if isDir {
				// Directory was deleted - remove all files in that folder
				log.Printf("Folder deleted, cleaning up: %s", path)
				if err := w.handler.DeleteFolder(ctx, path); err != nil {
					log.Printf("Failed to delete folder from index: %s: %v", path, err)
				}
			} else {
				// File was deleted or renamed
				log.Printf("File deleted: %s", path)
				if err := w.handler.DeleteFile(ctx, path); err != nil {
					log.Printf("Failed to delete file from index: %s: %v", path, err)
				}
			}
		} else if op.Has(fsnotify.Write) || op.Has(fsnotify.Create) {
			// File was created or modified
			log.Printf("File changed: %s", path)
			if err := w.handler.UpdateFile(ctx, w.projectPath, path); err != nil {
				log.Printf("Failed to update file in index: %s: %v", path, err)
			}
		}
	}
}

// WatcherManager manages multiple project watchers
type WatcherManager struct {
	cfg      *config.Config
	handler  FileHandler
	watchers map[string]*Watcher
	mu       sync.RWMutex
}

// NewWatcherManager creates a new watcher manager
func NewWatcherManager(cfg *config.Config, handler FileHandler) *WatcherManager {
	return &WatcherManager{
		cfg:      cfg,
		handler:  handler,
		watchers: make(map[string]*Watcher),
	}
}

// StartWatching starts watching a project
func (wm *WatcherManager) StartWatching(projectPath string) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	// Stop existing watcher if any
	if w, ok := wm.watchers[projectPath]; ok {
		w.Stop()
	}

	// Create new watcher
	w, err := NewWatcher(projectPath, wm.cfg, wm.handler)
	if err != nil {
		return err
	}

	if err := w.Start(); err != nil {
		return err
	}

	wm.watchers[projectPath] = w
	return nil
}

// StopWatching stops watching a project
func (wm *WatcherManager) StopWatching(projectPath string) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	if w, ok := wm.watchers[projectPath]; ok {
		err := w.Stop()
		delete(wm.watchers, projectPath)
		return err
	}
	return nil
}

// StopAll stops all watchers
func (wm *WatcherManager) StopAll() {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	for path, w := range wm.watchers {
		w.Stop()
		delete(wm.watchers, path)
	}
}

// IsWatching checks if a project is being watched
func (wm *WatcherManager) IsWatching(projectPath string) bool {
	wm.mu.RLock()
	defer wm.mu.RUnlock()
	_, ok := wm.watchers[projectPath]
	return ok
}
