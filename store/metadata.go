package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"mcp-semantic-search/config"
	"mcp-semantic-search/types"

	"github.com/ncruces/go-sqlite3"
)

// FileHashStore stores file hashes for incremental indexing using SQLite
type FileHashStore struct {
	db *sqlite3.Conn
	mu *sync.Mutex // Shared mutex with Store to prevent concurrent db access
}

// NewFileHashStore creates a new file hash store using the provided database connection
func NewFileHashStore(db *sqlite3.Conn, mu *sync.Mutex) *FileHashStore {
	return &FileHashStore{
		db: db,
		mu: mu,
	}
}

// LoadProjectHashes is a no-op since data is already in SQLite
func (f *FileHashStore) LoadProjectHashes(projectPath string) error {
	return nil
}

// SaveProjectHashes is a no-op since data is already in SQLite
func (f *FileHashStore) SaveProjectHashes(projectPath string) error {
	return nil
}

// GetFileHash gets the stored hash for a file
func (f *FileHashStore) GetFileHash(projectPath, filePath string) string {
	f.mu.Lock()
	defer f.mu.Unlock()

	stmt, _, err := f.db.Prepare(`SELECT hash FROM file_hashes WHERE project_path = ? AND file_path = ?`)
	if err != nil {
		return ""
	}
	defer stmt.Close()

	stmt.BindText(1, projectPath)
	stmt.BindText(2, filePath)

	if stmt.Step() {
		return stmt.ColumnText(0)
	}
	return ""
}

// SetFileHash sets the hash for a file
func (f *FileHashStore) SetFileHash(projectPath, filePath, hash string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	stmt, _, err := f.db.Prepare(`INSERT OR REPLACE INTO file_hashes (project_path, file_path, hash) VALUES (?, ?, ?)`)
	if err != nil {
		return
	}
	defer stmt.Close()

	stmt.BindText(1, projectPath)
	stmt.BindText(2, filePath)
	stmt.BindText(3, hash)
	stmt.Exec()
}

// RemoveFileHash removes the hash for a file
func (f *FileHashStore) RemoveFileHash(projectPath, filePath string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	stmt, _, err := f.db.Prepare(`DELETE FROM file_hashes WHERE project_path = ? AND file_path = ?`)
	if err != nil {
		return
	}
	defer stmt.Close()

	stmt.BindText(1, projectPath)
	stmt.BindText(2, filePath)
	stmt.Exec()
}

// DeleteProjectHashes deletes all hashes for a project
func (f *FileHashStore) DeleteProjectHashes(projectPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	stmt, _, err := f.db.Prepare(`DELETE FROM file_hashes WHERE project_path = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	stmt.BindText(1, projectPath)
	return stmt.Exec()
}

// GetChangedFiles returns files that have changed (new, modified, or deleted)
func (f *FileHashStore) GetChangedFiles(folderPath string, currentFiles map[string]string) (added, modified, deleted []string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Get stored hashes from database
	storedHashes := make(map[string]string)
	stmt, _, err := f.db.Prepare(`SELECT file_path, hash FROM file_hashes WHERE project_path = ?`)
	if err == nil {
		stmt.BindText(1, folderPath)
		for stmt.Step() {
			storedHashes[stmt.ColumnText(0)] = stmt.ColumnText(1)
		}
		stmt.Close()
	}

	// Check for new and modified files
	for filePath, currentHash := range currentFiles {
		storedHash, exists := storedHashes[filePath]
		if !exists {
			added = append(added, filePath)
		} else if storedHash != currentHash {
			modified = append(modified, filePath)
		}
	}

	// Check for deleted files
	for filePath := range storedHashes {
		if _, exists := currentFiles[filePath]; !exists {
			deleted = append(deleted, filePath)
		}
	}

	return
}

// GetAllFilePaths returns all indexed file paths for a folder
func (f *FileHashStore) GetAllFilePaths(folderPath string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	stmt, _, err := f.db.Prepare(`SELECT file_path FROM file_hashes WHERE project_path = ?`)
	if err != nil {
		return nil
	}
	defer stmt.Close()

	stmt.BindText(1, folderPath)

	var paths []string
	for stmt.Step() {
		paths = append(paths, stmt.ColumnText(0))
	}
	return paths
}

// ListIndexedFolders returns all folder paths that have been indexed
func (f *FileHashStore) ListIndexedFolders() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	stmt, _, err := f.db.Prepare(`SELECT DISTINCT project_path FROM file_hashes`)
	if err != nil {
		return nil
	}
	defer stmt.Close()

	var folders []string
	for stmt.Step() {
		folders = append(folders, stmt.ColumnText(0))
	}
	return folders
}

// Metadata manages project metadata persistence
type Metadata struct {
	cfg      *config.Config
	projects map[string]*types.Project
	mu       sync.RWMutex
}

// MetadataFile represents the JSON structure for persistence
type MetadataFile struct {
	Version  int              `json:"version"`
	Projects []*types.Project `json:"projects"`
}

// NewMetadata creates a new Metadata manager
func NewMetadata(cfg *config.Config) (*Metadata, error) {
	m := &Metadata{
		cfg:      cfg,
		projects: make(map[string]*types.Project),
	}

	// Ensure directory exists
	if err := os.MkdirAll(cfg.DBPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create metadata directory: %w", err)
	}

	// Load existing metadata
	if err := m.Load(); err != nil {
		// If file doesn't exist, that's fine
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to load metadata: %w", err)
		}
	}

	return m, nil
}

// Load loads metadata from disk
func (m *Metadata) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.cfg.MetadataPath())
	if err != nil {
		return err
	}

	var file MetadataFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("failed to parse metadata: %w", err)
	}

	m.projects = make(map[string]*types.Project)
	for _, p := range file.Projects {
		m.projects[p.Path] = p
	}

	return nil
}

// Save saves metadata to disk
func (m *Metadata) Save() error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	projects := make([]*types.Project, 0, len(m.projects))
	for _, p := range m.projects {
		projects = append(projects, p)
	}

	file := MetadataFile{
		Version:  1,
		Projects: projects,
	}

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	// Write to temp file first, then rename for atomic write
	tmpPath := m.cfg.MetadataPath() + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write metadata: %w", err)
	}

	if err := os.Rename(tmpPath, m.cfg.MetadataPath()); err != nil {
		return fmt.Errorf("failed to rename metadata file: %w", err)
	}

	return nil
}

// GetProject retrieves a project by path
func (m *Metadata) GetProject(path string) *types.Project {
	m.mu.RLock()
	defer m.mu.RUnlock()

	absPath, _ := filepath.Abs(path)
	return m.projects[absPath]
}

// GetProjectByID retrieves a project by ID
func (m *Metadata) GetProjectByID(id string) *types.Project {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, p := range m.projects {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// SetProject adds or updates a project
func (m *Metadata) SetProject(project *types.Project) error {
	m.mu.Lock()
	m.projects[project.Path] = project
	m.mu.Unlock()

	return m.Save()
}

// UpdateProjectStatus updates the status of a project
func (m *Metadata) UpdateProjectStatus(path, status string, errMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	absPath, _ := filepath.Abs(path)
	p, ok := m.projects[absPath]
	if !ok {
		return fmt.Errorf("project not found: %s", path)
	}

	p.Status = status
	p.Error = errMsg

	return m.Save()
}

// UpdateProjectStats updates file and chunk counts
func (m *Metadata) UpdateProjectStats(path string, fileCount, chunkCount int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	absPath, _ := filepath.Abs(path)
	p, ok := m.projects[absPath]
	if !ok {
		return fmt.Errorf("project not found: %s", path)
	}

	p.FileCount = fileCount
	p.ChunkCount = chunkCount
	p.LastIndexed = time.Now()

	return m.Save()
}

// SetWatching updates the watching status
func (m *Metadata) SetWatching(path string, watching bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	absPath, _ := filepath.Abs(path)
	p, ok := m.projects[absPath]
	if !ok {
		return fmt.Errorf("project not found: %s", path)
	}

	p.Watching = watching

	return m.Save()
}

// RemoveProject removes a project from metadata
func (m *Metadata) RemoveProject(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	absPath, _ := filepath.Abs(path)
	delete(m.projects, absPath)

	return m.Save()
}

// ListProjects returns all tracked projects
func (m *Metadata) ListProjects() []*types.Project {
	m.mu.RLock()
	defer m.mu.RUnlock()

	projects := make([]*types.Project, 0, len(m.projects))
	for _, p := range m.projects {
		projects = append(projects, p)
	}

	return projects
}

// GetTotalStats returns total file and chunk counts across all projects
func (m *Metadata) GetTotalStats() (totalFiles, totalChunks int) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, p := range m.projects {
		totalFiles += p.FileCount
		totalChunks += p.ChunkCount
	}

	return
}

// CreateProject creates a new project entry
func (m *Metadata) CreateProject(path string) (*types.Project, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %w", err)
	}

	project := &types.Project{
		ID:          GenerateProjectID(absPath),
		Path:        absPath,
		Name:        filepath.Base(absPath),
		LastIndexed: time.Time{},
		FileCount:   0,
		ChunkCount:  0,
		Status:      "pending",
		Watching:    false,
	}

	if err := m.SetProject(project); err != nil {
		return nil, err
	}

	return project, nil
}

// GetOrCreateProject gets an existing project or creates a new one
func (m *Metadata) GetOrCreateProject(path string) (*types.Project, error) {
	absPath, _ := filepath.Abs(path)

	m.mu.RLock()
	p, ok := m.projects[absPath]
	m.mu.RUnlock()

	if ok {
		return p, nil
	}

	return m.CreateProject(path)
}

// GenerateProjectID creates a unique ID for a project
func GenerateProjectID(path string) string {
	// Use the collection name hash as the ID
	return projectCollectionName(path)
}
