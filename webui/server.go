package webui

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"mcp-semantic-search/config"
	"mcp-semantic-search/indexer"
	"mcp-semantic-search/types"
)

//go:embed static/*
var staticFiles embed.FS

// Server represents the web UI HTTP server
type Server struct {
	cfg        *config.Config
	idx        *indexer.Indexer
	server     *http.Server
	port       int
	actualPort int // The port actually bound (may differ if original was busy)
	version    string

	// SSE clients for progress updates
	sseClients   map[chan types.ProgressEvent]bool
	sseClientsMu sync.RWMutex
}

// NewServer creates a new web UI server
func NewServer(cfg *config.Config, idx *indexer.Indexer, port int, version string) *Server {
	s := &Server{
		cfg:        cfg,
		idx:        idx,
		port:       port,
		version:    version,
		sseClients: make(map[chan types.ProgressEvent]bool),
	}

	// Set up progress callback
	idx.SetProgressCallback(s.broadcastProgress)

	return s
}

// broadcastProgress sends a progress event to all connected SSE clients
func (s *Server) broadcastProgress(event types.ProgressEvent) {
	s.sseClientsMu.RLock()
	defer s.sseClientsMu.RUnlock()

	for ch := range s.sseClients {
		select {
		case ch <- event:
		default:
			// Channel full, skip
		}
	}
}

// Start starts the HTTP server, finding an available port if needed
func (s *Server) Start() error {
	mux := http.NewServeMux()

	// API endpoints
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/search", s.handleSearch)
	mux.HandleFunc("/api/scan", s.handleScan)
	mux.HandleFunc("/api/index", s.handleIndex)
	mux.HandleFunc("/api/reindex", s.handleReindex)
	mux.HandleFunc("/api/remove", s.handleRemove)
	mux.HandleFunc("/api/progress", s.handleSSE)

	// Static files (embedded)
	mux.HandleFunc("/", s.handleStatic)

	// Find an available port
	maxRetry := s.cfg.MaxPortRetry
	if maxRetry <= 0 {
		maxRetry = 10
	}

	var listener net.Listener
	var err error
	selectedPort := s.port

	for i := 0; i <= maxRetry; i++ {
		testPort := s.port + i
		listener, err = net.Listen("tcp", fmt.Sprintf(":%d", testPort))
		if err == nil {
			selectedPort = testPort
			break
		}
		if i < maxRetry {
			log.Printf("Port %d is busy, trying %d...", testPort, testPort+1)
		}
	}

	if err != nil {
		return fmt.Errorf("failed to find available port after %d attempts: %w", maxRetry+1, err)
	}

	s.actualPort = selectedPort

	s.server = &http.Server{
		Handler:      corsMiddleware(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // No timeout for SSE
	}

	if selectedPort != s.port {
		log.Printf("Port %d was busy, using port %d instead", s.port, selectedPort)
	}
	log.Printf("Web UI available at http://localhost:%d", selectedPort)

	go func() {
		if err := s.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("Web UI server error: %v", err)
		}
	}()

	return nil
}

// GetActualPort returns the port the server is actually running on
func (s *Server) GetActualPort() int {
	return s.actualPort
}

// Stop stops the HTTP server
func (s *Server) Stop() error {
	if s.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.server.Shutdown(ctx)
	}
	return nil
}

// corsMiddleware adds CORS headers for local development
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// handleStatic serves embedded static files
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/" {
		path = "/index.html"
	}

	data, err := staticFiles.ReadFile("static" + path)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	// Set content type based on extension
	switch {
	case len(path) > 5 && path[len(path)-5:] == ".html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case len(path) > 3 && path[len(path)-3:] == ".js":
		w.Header().Set("Content-Type", "application/javascript")
	case len(path) > 4 && path[len(path)-4:] == ".css":
		w.Header().Set("Content-Type", "text/css")
	}

	if _, err := w.Write(data); err != nil {
		log.Printf("Failed to write response: %v", err)
	}
}

// handleSSE handles Server-Sent Events for real-time progress updates
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Create client channel
	clientChan := make(chan types.ProgressEvent, 10)

	// Register client
	s.sseClientsMu.Lock()
	s.sseClients[clientChan] = true
	s.sseClientsMu.Unlock()

	// Clean up on disconnect
	defer func() {
		s.sseClientsMu.Lock()
		delete(s.sseClients, clientChan)
		s.sseClientsMu.Unlock()
		close(clientChan)
	}()

	// Flush helper
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	// Send initial ping
	fmt.Fprintf(w, "event: ping\ndata: connected\n\n")
	flusher.Flush()

	// Stream events
	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-clientChan:
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "event: progress\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// handleStatus returns the current indexing status
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status, err := s.idx.GetStatus(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Add version to status
	status.Version = s.version

	writeJSON(w, http.StatusOK, status)
}

// handleScan scans a folder without indexing
func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Path string `json:"path"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}

	if req.Path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Path is required"})
		return
	}

	result, err := s.idx.ScanProject(r.Context(), req.Path)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// handleSearch performs semantic search with usage analysis
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Query   string `json:"query"`
		Project string `json:"project"`
		Limit   int    `json:"limit"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}

	if req.Query == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Query is required"})
		return
	}

	if req.Limit <= 0 {
		req.Limit = 10
	}
	if req.Limit > 50 {
		req.Limit = 50
	}

	// Use SearchWithUsage to get usage maps and call graphs
	response, err := s.idx.SearchWithUsage(r.Context(), req.Query, req.Project, req.Limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, response)
}

// handleIndex starts indexing a project
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Path  string `json:"path"`
		Watch bool   `json:"watch"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}

	if req.Path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Path is required"})
		return
	}

	// Index in background
	go func() {
		ctx := context.Background()
		result, err := s.idx.IndexProject(ctx, req.Path, req.Watch)
		if err != nil {
			log.Printf("Indexing failed for %s: %v", req.Path, err)
			s.broadcastProgress(types.ProgressEvent{
				Type:    "error",
				Project: req.Path,
				Message: "Indexing failed",
				Error:   err.Error(),
			})
		} else {
			log.Printf("Indexing complete for %s: %d files, %d chunks", req.Path, result.FilesIndexed, result.ChunksStored)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":  "indexing_started",
		"message": "Indexing started in background. Connect to /api/progress for updates.",
	})
}

// handleReindex forces a complete reindex
func (s *Server) handleReindex(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Path string `json:"path"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}

	if req.Path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Path is required"})
		return
	}

	// Reindex in background
	go func() {
		ctx := context.Background()
		result, err := s.idx.ReindexProject(ctx, req.Path)
		if err != nil {
			log.Printf("Reindexing failed for %s: %v", req.Path, err)
			s.broadcastProgress(types.ProgressEvent{
				Type:    "error",
				Project: req.Path,
				Message: "Reindexing failed",
				Error:   err.Error(),
			})
		} else {
			log.Printf("Reindexing complete for %s: %d files, %d chunks", req.Path, result.FilesIndexed, result.ChunksStored)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":  "reindexing_started",
		"message": "Reindexing started in background",
	})
}

// handleRemove removes a project from the index
func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Path string `json:"path"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}

	if req.Path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Path is required"})
		return
	}

	if err := s.idx.RemoveProject(r.Context(), req.Path); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "removed",
		"message": fmt.Sprintf("Project removed: %s", req.Path),
	})
}

// writeJSON writes a JSON response
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("Failed to encode JSON response: %v", err)
	}
}
