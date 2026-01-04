package indexer

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mcp-semantic-search/config"
	"mcp-semantic-search/types"

	ignore "github.com/sabhiram/go-gitignore"
)

// Scanner handles file discovery and filtering
type Scanner struct {
	cfg      *config.Config
	ignorers map[string]*ignore.GitIgnore // Map of directory path -> gitignore
	rootPath string
}

// NewScanner creates a new Scanner for a project directory
func NewScanner(cfg *config.Config, rootPath string) (*Scanner, error) {
	absPath, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, err
	}

	scanner := &Scanner{
		cfg:      cfg,
		rootPath: absPath,
		ignorers: make(map[string]*ignore.GitIgnore),
	}

	// Load root .gitignore if it exists
	scanner.loadGitignore(absPath)

	return scanner, nil
}

// loadGitignore loads .gitignore from a directory if it exists
func (s *Scanner) loadGitignore(dirPath string) {
	gitignorePath := filepath.Join(dirPath, ".gitignore")
	if _, err := os.Stat(gitignorePath); err == nil {
		if ignorer, err := ignore.CompileIgnoreFile(gitignorePath); err == nil {
			s.ignorers[dirPath] = ignorer
		}
	}
}

// isIgnoredByGitignore checks if a path is ignored by any applicable .gitignore
func (s *Scanner) isIgnoredByGitignore(absPath string, isDir bool) bool {
	// Get relative path from root
	relPath, err := filepath.Rel(s.rootPath, absPath)
	if err != nil {
		return false
	}

	// For directories, append "/" for proper gitignore matching
	matchPath := filepath.ToSlash(relPath)
	if isDir {
		matchPath += "/"
	}

	// Check all gitignore files from root to parent directory
	currentDir := s.rootPath
	pathParts := strings.Split(filepath.ToSlash(relPath), "/")

	// Check root gitignore first
	if ignorer, ok := s.ignorers[s.rootPath]; ok {
		if ignorer.MatchesPath(matchPath) {
			return true
		}
	}

	// Check gitignores in each parent directory
	for i := 0; i < len(pathParts)-1; i++ {
		currentDir = filepath.Join(currentDir, pathParts[i])
		if ignorer, ok := s.ignorers[currentDir]; ok {
			// Get path relative to this gitignore's directory
			subRelPath, err := filepath.Rel(currentDir, absPath)
			if err != nil {
				continue
			}
			subMatchPath := filepath.ToSlash(subRelPath)
			if isDir {
				subMatchPath += "/"
			}
			if ignorer.MatchesPath(subMatchPath) {
				return true
			}
		}
	}

	return false
}

// Scan walks the directory tree and returns all indexable files
func (s *Scanner) Scan() ([]types.FileInfo, error) {
	var files []types.FileInfo

	err := filepath.Walk(s.rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip files we can't access
		}

		// Get path relative to root
		relPath, err := filepath.Rel(s.rootPath, path)
		if err != nil {
			return nil
		}

		// Check if directory should be excluded
		if info.IsDir() {
			// Skip root directory itself
			if path != s.rootPath {
				if s.shouldExcludeDir(info.Name(), path) {
					return filepath.SkipDir
				}
			}
			// Load .gitignore from this directory if it exists
			s.loadGitignore(path)
			return nil
		}

		// Check if file should be indexed
		if !s.shouldIncludeFile(info, path) {
			return nil
		}

		// Calculate file hash
		hash, err := s.hashFile(path)
		if err != nil {
			return nil // Skip files we can't hash
		}

		// Detect language
		language := detectLanguage(path)

		files = append(files, types.FileInfo{
			Path:         path,
			RelativePath: relPath,
			Size:         info.Size(),
			ModTime:      info.ModTime(),
			Hash:         hash,
			Language:     language,
		})

		return nil
	})

	return files, err
}

// shouldExcludeDir checks if a directory should be excluded
// absPath is the absolute path to the directory
func (s *Scanner) shouldExcludeDir(name, absPath string) bool {
	// Always exclude configured directories
	if s.cfg.IsExcludedDir(name) {
		return true
	}

	// Check all applicable .gitignore files
	if s.isIgnoredByGitignore(absPath, true) {
		return true
	}

	return false
}

// shouldIncludeFile checks if a file should be indexed
// absPath is the absolute path to the file
func (s *Scanner) shouldIncludeFile(info os.FileInfo, absPath string) bool {
	// Check file size
	if info.Size() > s.cfg.MaxFileSize {
		return false
	}

	// Check file size is not zero
	if info.Size() == 0 {
		return false
	}

	// Check extension
	ext := strings.ToLower(filepath.Ext(info.Name()))
	if s.cfg.IsExcludedExt(ext) {
		return false
	}

	if !s.cfg.ShouldIncludeExt(ext) {
		return false
	}

	// Check all applicable .gitignore files
	if s.isIgnoredByGitignore(absPath, false) {
		return false
	}

	// Check if it's a binary file (will be checked again when reading)
	return true
}

// hashFile calculates SHA256 hash of a file's content
func (s *Scanner) hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// IsBinaryFile checks if a file is binary by reading first 512 bytes
func IsBinaryFile(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	// Read first 512 bytes
	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return false, err
	}
	buf = buf[:n]

	// Check for null bytes (common in binary files)
	for _, b := range buf {
		if b == 0 {
			return true, nil
		}
	}

	return false, nil
}

// ReadFileContent reads and returns file content, checking for binary
func ReadFileContent(path string) (string, error) {
	// Check if binary first
	isBinary, err := IsBinaryFile(path)
	if err != nil {
		return "", err
	}
	if isBinary {
		return "", nil // Return empty for binary files
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	return string(content), nil
}

// detectLanguage detects programming language from file extension
func detectLanguage(path string) string {
	ext := strings.ToLower(filepath.Ext(path))

	languageMap := map[string]string{
		// Go
		".go": "go",
		// Python
		".py": "python", ".pyw": "python", ".pyx": "python",
		// JavaScript/TypeScript
		".js": "javascript", ".jsx": "javascript",
		".ts": "typescript", ".tsx": "typescript",
		".mjs": "javascript", ".cjs": "javascript",
		// Web
		".html": "html", ".htm": "html",
		".css": "css", ".scss": "css", ".sass": "css", ".less": "css",
		".vue": "html", ".svelte": "svelte",
		// C family
		".c": "c", ".h": "c",
		".cpp": "cpp", ".cc": "cpp", ".cxx": "cpp", ".hpp": "cpp", ".hxx": "cpp",
		".cs": "csharp",
		// JVM
		".java": "java",
		".kt": "kotlin", ".kts": "kotlin",
		".scala": "scala",
		".groovy": "groovy", ".gvy": "groovy", ".gy": "groovy", ".gsh": "groovy",
		// Ruby
		".rb": "ruby", ".erb": "ruby", ".rake": "ruby",
		// Rust
		".rs": "rust",
		// Swift/Objective-C
		".swift": "swift",
		".m": "c", ".mm": "cpp", // Objective-C uses C/C++ parser
		// PHP
		".php": "php", ".phtml": "php",
		// Shell
		".sh": "bash", ".bash": "bash", ".zsh": "bash",
		".ps1": "bash", ".psm1": "bash", // PowerShell uses bash parser as fallback
		".bat": "bash", ".cmd": "bash",
		// Data/Config
		".json": "json",
		".yaml": "yaml", ".yml": "yaml",
		".toml": "toml",
		".xml": "html", // XML uses HTML parser
		".ini": "toml", // INI is similar to TOML
		".env": "bash",
		// Documentation
		".md": "markdown", ".markdown": "markdown",
		".rst": "text",
		".txt": "text",
		// SQL
		".sql": "sql",
		// Lua
		".lua": "lua",
		// Perl
		".pl": "perl", ".pm": "perl",
		// Functional languages
		".hs": "haskell",
		".ml": "ocaml", ".mli": "ocaml",
		".ex": "elixir", ".exs": "elixir",
		".erl": "erlang",
		".elm": "elm",
		".clj": "clojure", ".cljs": "clojure",
		// Other
		".dart": "dart",
		".zig": "zig",
		".nim": "nim",
		".v": "vlang",
		".cue": "cue",
		".proto": "protobuf",
		".tf": "hcl", ".tfvars": "hcl", // Terraform
		".hcl": "hcl",
		// R
		".r": "r", ".R": "r",
	}

	// Check by filename for files without extensions
	filenameMap := map[string]string{
		"Makefile":   "bash",
		"Dockerfile": "dockerfile",
		"Jenkinsfile": "groovy",
		"BUILD":      "python", // Bazel
		"WORKSPACE":  "python", // Bazel
		".bashrc":    "bash",
		".zshrc":     "bash",
		".gitignore": "text",
	}

	// Check by extension first
	if lang, ok := languageMap[ext]; ok {
		return lang
	}

	// Check by filename (for files without extension or special files)
	basename := filepath.Base(path)
	if lang, ok := filenameMap[basename]; ok {
		return lang
	}

	// Check if basename matches any pattern in languageMap
	if lang, ok := languageMap[basename]; ok {
		return lang
	}

	return "text"
}

// FindGitRoot finds the nearest .git directory
func FindGitRoot(path string) (string, bool) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}

	current := absPath
	for {
		gitPath := filepath.Join(current, ".git")
		if info, err := os.Stat(gitPath); err == nil && info.IsDir() {
			return current, true
		}

		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}

	return "", false
}

// FindNestedProjects finds all nested projects (directories with .git)
func FindNestedProjects(rootPath string) ([]string, error) {
	var projects []string

	absPath, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, err
	}

	// Check if root itself is a git repo
	if _, err := os.Stat(filepath.Join(absPath, ".git")); err == nil {
		projects = append(projects, absPath)
	}

	// Walk to find nested projects
	err = filepath.Walk(absPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || !info.IsDir() {
			return nil
		}

		// Skip the root path
		if path == absPath {
			return nil
		}

		// Check for .git
		if info.Name() == ".git" {
			parentDir := filepath.Dir(path)
			if parentDir != absPath {
				projects = append(projects, parentDir)
			}
			return filepath.SkipDir
		}

		// Skip common excluded directories
		excludedDirs := []string{"node_modules", "vendor", ".git", "__pycache__", ".venv"}
		for _, excluded := range excludedDirs {
			if info.Name() == excluded {
				return filepath.SkipDir
			}
		}

		return nil
	})

	return projects, err
}
