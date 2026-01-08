package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"mcp-semantic-search/config"
	"mcp-semantic-search/indexer"
	"mcp-semantic-search/store"
	"mcp-semantic-search/tools"
	"mcp-semantic-search/updater"
	"mcp-semantic-search/watcher"
	"mcp-semantic-search/webui"

	"github.com/mark3labs/mcp-go/server"
)

const (
	serverName = "mcp-semantic-search"
)

// Version is set via ldflags at build time:
// go build -ldflags "-X main.Version=1.2.3"
var Version = "dev"

func main() {
	// Load configuration
	cfg := config.LoadFromEnv()

	// Ensure database directory exists
	if err := os.MkdirAll(cfg.DBPath, 0755); err != nil {
		log.Fatalf("Failed to create database directory: %v", err)
	}

	// Create embedder
	embedder := indexer.NewEmbedder(cfg.OllamaURL, cfg.EmbeddingModel)

	// Test Ollama connection, try to start if not running
	ctx := context.Background()
	if err := embedder.TestConnection(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Ollama not running, attempting to start...\n")
		if startErr := startOllama(); startErr != nil {
			fmt.Fprintf(os.Stderr, "Failed to start Ollama: %v\n", startErr)
			fmt.Fprintf(os.Stderr, "Please start Ollama manually: ollama serve\n")
			os.Exit(1)
		}
		// Wait and retry connection
		if err := embedder.TestConnection(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Ollama still not responding after start: %v\n", err)
			fmt.Fprintf(os.Stderr, "Make sure model '%s' is available: ollama pull %s\n", cfg.EmbeddingModel, cfg.EmbeddingModel)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Ollama started successfully\n")
	}

	// Create store
	vectorStore, err := store.NewStore(cfg, embedder.EmbeddingFunc())
	if err != nil {
		log.Fatalf("Failed to create vector store: %v", err)
	}

	// Create file hash store for incremental indexing (uses SQLite)
	hashStore := vectorStore.NewFileHashStore()

	// Create indexer
	idx := indexer.NewIndexer(cfg, vectorStore, hashStore, embedder)

	// Create watcher manager (connects file watcher to indexer)
	watcherManager := watcher.NewWatcherManager(cfg, idx)

	// Connect watcher manager to indexer (for starting watchers from IndexProject)
	idx.SetWatcherManager(watcherManager)

	// Restore watchers for previously indexed folders
	if cfg.WatchEnabled {
		folders := hashStore.ListIndexedFolders()
		for _, folderPath := range folders {
			if err := watcherManager.StartWatching(folderPath); err != nil {
				log.Printf("Failed to restore watcher for %s: %v", folderPath, err)
			} else {
				log.Printf("Restored watcher for: %s", folderPath)
			}
		}
	}

	// Create MCP server
	mcpServer := server.NewMCPServer(
		serverName,
		Version,
		server.WithToolCapabilities(true),
	)

	// Register all tools
	tools.RegisterTools(mcpServer, idx)

	// Initialize auto-updater (runs in background)
	if cfg.AutoUpdateEnabled {
		appUpdater := updater.NewUpdater(Version, true)
		if cfg.AutoUpdateApply {
			// Auto-apply updates in background and exit to restart with new binary
			appUpdater.BackgroundAutoUpdate(context.Background(), true)
		} else {
			// Just check and notify
			appUpdater.BackgroundCheck(context.Background())
		}
	}

	// Start Web UI server if enabled
	var webServer *webui.Server
	var actualWebUIPort int
	if cfg.WebUIEnabled {
		webServer = webui.NewServer(cfg, idx, cfg.WebUIPort, Version)
		if err := webServer.Start(); err != nil {
			log.Printf("Failed to start web UI: %v", err)
		} else {
			actualWebUIPort = webServer.GetActualPort()
			// Auto-open browser if enabled
			if cfg.AutoOpenUI {
				url := fmt.Sprintf("http://localhost:%d", actualWebUIPort)
				go openBrowser(url)
			}
		}
	}

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Shutting down...")
		if webServer != nil {
			_ = webServer.Stop()
		}
		watcherManager.StopAll()
		idx.Close()
		_ = vectorStore.Close()
		os.Exit(0)
	}()

	// Print startup info to stderr (stdout is for MCP communication)
	fmt.Fprintf(os.Stderr, "Starting %s v%s\n", serverName, Version)
	fmt.Fprintf(os.Stderr, "Database path: %s\n", cfg.DBPath)
	fmt.Fprintf(os.Stderr, "Ollama URL: %s\n", cfg.OllamaURL)
	fmt.Fprintf(os.Stderr, "Embedding model: %s\n", cfg.EmbeddingModel)
	fmt.Fprintf(os.Stderr, "Embedding workers: %d\n", cfg.EmbeddingWorkers)
	fmt.Fprintf(os.Stderr, "File watching: %v\n", cfg.WatchEnabled)
	fmt.Fprintf(os.Stderr, "Auto-index: %v\n", cfg.AutoIndex)
	fmt.Fprintf(os.Stderr, "Auto-update: %v (apply: %v)\n", cfg.AutoUpdateEnabled, cfg.AutoUpdateApply)
	if cfg.WebUIEnabled && actualWebUIPort > 0 {
		fmt.Fprintf(os.Stderr, "Web UI: http://localhost:%d\n", actualWebUIPort)
		if cfg.AutoOpenUI {
			fmt.Fprintf(os.Stderr, "Auto-opening browser...\n")
		}
	}

	// Auto-index current folder if enabled
	if cfg.AutoIndex {
		go func() {
			cwd, err := os.Getwd()
			if err != nil {
				log.Printf("Failed to get current directory: %v", err)
				return
			}
			log.Printf("Auto-indexing current folder: %s", cwd)
			result, err := idx.IndexProject(context.Background(), cwd, cfg.WatchEnabled)
			if err != nil {
				log.Printf("Auto-index failed: %v", err)
			} else {
				log.Printf("Auto-index complete: %d files, %d chunks", result.FilesIndexed, result.ChunksStored)
			}
		}()
	}

	// Start MCP server (stdio transport)
	if err := server.ServeStdio(mcpServer); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

// startOllama attempts to start Ollama in the background
func startOllama() error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "windows":
		// On Windows, try to start ollama serve in background
		cmd = exec.Command("cmd", "/C", "start", "/B", "ollama", "serve")
	default:
		// On Unix, start in background
		cmd = exec.Command("ollama", "serve")
	}

	// Start the process (don't wait for it)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start ollama: %w", err)
	}

	// Give Ollama a moment to start up
	fmt.Fprintf(os.Stderr, "Waiting for Ollama to start...\n")
	for i := 0; i < 10; i++ {
		time.Sleep(500 * time.Millisecond)
		// Check if it's responding now (done in caller)
	}

	return nil
}

// openBrowser opens the specified URL in the default browser
func openBrowser(url string) {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default: // Linux, BSD, etc.
		cmd = exec.Command("xdg-open", url)
	}

	if err := cmd.Start(); err != nil {
		log.Printf("Failed to open browser: %v", err)
	}
}
