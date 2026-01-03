package indexer

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"

	"mcp-semantic-search/types"
)

// Chunker parses source files into semantic chunks
type Chunker struct {
	maxChunkSize int
	overlapLines int
	tsParser     *Parser // Tree-sitter parser for multi-language support
}

// NewChunker creates a new Chunker
func NewChunker(maxChunkSize, overlapLines int) *Chunker {
	return &Chunker{
		maxChunkSize: maxChunkSize,
		overlapLines: overlapLines,
		tsParser:     NewParser(), // Initialize tree-sitter parser
	}
}

// ChunkFile parses a file into chunks based on its language
func (c *Chunker) ChunkFile(content, filePath, language string) []types.Chunk {
	// Try tree-sitter first for supported languages
	if c.tsParser.IsSupported(language) {
		chunks := c.chunkWithTreeSitter(content, filePath, language)
		if len(chunks) > 0 {
			return chunks
		}
	}

	// Fall back to legacy parsers
	var chunks []types.Chunk

	switch language {
	case "go":
		chunks = c.chunkGo(content, filePath)
	case "python":
		chunks = c.chunkPython(content, filePath)
	case "javascript", "typescript":
		chunks = c.chunkJavaScript(content, filePath)
	case "java", "kotlin", "csharp":
		chunks = c.chunkJavaLike(content, filePath, language)
	case "rust":
		chunks = c.chunkRust(content, filePath)
	default:
		chunks = c.chunkByLines(content, filePath, language)
	}

	// If no chunks found, fall back to line-based chunking
	if len(chunks) == 0 {
		chunks = c.chunkByLines(content, filePath, language)
	}

	// Ensure all chunks have proper metadata
	for i := range chunks {
		chunks[i].Language = language
		chunks[i].FilePath = filePath
	}

	return chunks
}

// chunkWithTreeSitter uses tree-sitter for parsing and reference extraction
func (c *Chunker) chunkWithTreeSitter(content, filePath, language string) []types.Chunk {
	ctx := context.Background()
	result, err := c.tsParser.Parse(ctx, []byte(content), language)
	if err != nil || result == nil {
		return nil
	}

	// Detect if this is a test file
	isTestFile := result.IsTest || c.isTestFilePath(filePath)

	chunks := make([]types.Chunk, 0, len(result.Symbols))

	for _, sym := range result.Symbols {
		// Skip oversized chunks - split them
		lines := strings.Split(sym.Content, "\n")
		if len(lines) > c.maxChunkSize {
			// Split into smaller chunks but preserve metadata
			subChunks := c.splitLargeSymbol(sym, language, isTestFile)
			chunks = append(chunks, subChunks...)
			continue
		}

		chunk := types.Chunk{
			Content:    sym.Content,
			Type:       sym.Type,
			Name:       sym.Name,
			Language:   language,
			FilePath:   filePath,
			StartLine:  sym.StartLine,
			EndLine:    sym.EndLine,
			Calls:      sym.Calls,
			References: sym.References,
			IsExported: sym.IsExported,
			IsTest:     isTestFile || strings.HasPrefix(strings.ToLower(sym.Name), "test"),
			Parent:     sym.Parent,
		}

		chunks = append(chunks, chunk)
	}

	return chunks
}

// splitLargeSymbol splits an oversized symbol into smaller chunks
func (c *Chunker) splitLargeSymbol(sym SymbolInfo, language string, isTestFile bool) []types.Chunk {
	lines := strings.Split(sym.Content, "\n")
	var chunks []types.Chunk

	for i := 0; i < len(lines); i += c.maxChunkSize - c.overlapLines {
		endLine := i + c.maxChunkSize
		if endLine > len(lines) {
			endLine = len(lines)
		}

		chunkContent := strings.Join(lines[i:endLine], "\n")
		partNum := i/(c.maxChunkSize-c.overlapLines) + 1

		chunk := types.Chunk{
			Content:    chunkContent,
			Type:       sym.Type,
			Name:       sym.Name, // Keep original name
			Language:   language,
			StartLine:  sym.StartLine + i,
			EndLine:    sym.StartLine + endLine - 1,
			Calls:      sym.Calls,      // Keep calls from full symbol
			References: sym.References, // Keep references
			IsExported: sym.IsExported,
			IsTest:     isTestFile,
			Parent:     sym.Parent,
		}

		// Mark as part if split
		if partNum > 1 || endLine < len(lines) {
			chunk.Name = sym.Name + " (part " + string(rune('0'+partNum)) + ")"
		}

		chunks = append(chunks, chunk)

		if endLine >= len(lines) {
			break
		}
	}

	return chunks
}

// isTestFilePath checks if the file path indicates a test file
func (c *Chunker) isTestFilePath(filePath string) bool {
	base := strings.ToLower(filepath.Base(filePath))

	// Common test file patterns
	testPatterns := []string{
		"_test.go",
		"test_",
		"_test.py",
		"_test.js",
		"_test.ts",
		".test.js",
		".test.ts",
		".spec.js",
		".spec.ts",
		"test.py",
		"tests.py",
	}

	for _, pattern := range testPatterns {
		if strings.Contains(base, pattern) {
			return true
		}
	}

	// Check directory name
	dir := strings.ToLower(filepath.Dir(filePath))
	if strings.Contains(dir, "/test/") || strings.Contains(dir, "/tests/") ||
		strings.Contains(dir, "\\test\\") || strings.Contains(dir, "\\tests\\") {
		return true
	}

	return false
}

// chunkGo parses Go source code using go/parser
func (c *Chunker) chunkGo(content, filePath string) []types.Chunk {
	var chunks []types.Chunk

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filePath, content, parser.ParseComments)
	if err != nil {
		return nil // Fall back to line-based
	}

	lines := strings.Split(content, "\n")

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncDecl:
			startLine := fset.Position(node.Pos()).Line
			endLine := fset.Position(node.End()).Line

			name := node.Name.Name
			chunkType := types.ChunkTypeFunction

			// Check if it's a method
			if node.Recv != nil {
				chunkType = types.ChunkTypeMethod
				// Get receiver type name
				if len(node.Recv.List) > 0 {
					if starExpr, ok := node.Recv.List[0].Type.(*ast.StarExpr); ok {
						if ident, ok := starExpr.X.(*ast.Ident); ok {
							name = ident.Name + "." + name
						}
					} else if ident, ok := node.Recv.List[0].Type.(*ast.Ident); ok {
						name = ident.Name + "." + name
					}
				}
			}

			chunkContent := getLines(lines, startLine, endLine)
			chunks = append(chunks, types.Chunk{
				Content:   chunkContent,
				Type:      chunkType,
				Name:      name,
				StartLine: startLine,
				EndLine:   endLine,
			})

		case *ast.GenDecl:
			if node.Tok == token.TYPE {
				for _, spec := range node.Specs {
					if typeSpec, ok := spec.(*ast.TypeSpec); ok {
						startLine := fset.Position(node.Pos()).Line
						endLine := fset.Position(node.End()).Line

						chunkContent := getLines(lines, startLine, endLine)
						chunks = append(chunks, types.Chunk{
							Content:   chunkContent,
							Type:      types.ChunkTypeClass, // Using "class" for type definitions
							Name:      typeSpec.Name.Name,
							StartLine: startLine,
							EndLine:   endLine,
						})
					}
				}
			}
		}
		return true
	})

	return chunks
}

// chunkPython parses Python source code using regex
func (c *Chunker) chunkPython(content, filePath string) []types.Chunk {
	var chunks []types.Chunk
	lines := strings.Split(content, "\n")

	// Regex patterns for Python
	funcPattern := regexp.MustCompile(`^(\s*)def\s+(\w+)\s*\(`)
	classPattern := regexp.MustCompile(`^(\s*)class\s+(\w+)`)
	asyncFuncPattern := regexp.MustCompile(`^(\s*)async\s+def\s+(\w+)\s*\(`)

	var currentChunk *types.Chunk
	var currentIndent int

	for i, line := range lines {
		lineNum := i + 1

		// Check for class
		if matches := classPattern.FindStringSubmatch(line); matches != nil {
			if currentChunk != nil {
				currentChunk.EndLine = lineNum - 1
				currentChunk.Content = getLines(lines, currentChunk.StartLine, currentChunk.EndLine)
				chunks = append(chunks, *currentChunk)
			}
			currentChunk = &types.Chunk{
				Type:      types.ChunkTypeClass,
				Name:      matches[2],
				StartLine: lineNum,
			}
			currentIndent = len(matches[1])
			continue
		}

		// Check for async function
		if matches := asyncFuncPattern.FindStringSubmatch(line); matches != nil {
			if currentChunk != nil {
				currentChunk.EndLine = lineNum - 1
				currentChunk.Content = getLines(lines, currentChunk.StartLine, currentChunk.EndLine)
				chunks = append(chunks, *currentChunk)
			}
			currentChunk = &types.Chunk{
				Type:      types.ChunkTypeFunction,
				Name:      matches[2],
				StartLine: lineNum,
			}
			currentIndent = len(matches[1])
			continue
		}

		// Check for function
		if matches := funcPattern.FindStringSubmatch(line); matches != nil {
			if currentChunk != nil {
				currentChunk.EndLine = lineNum - 1
				currentChunk.Content = getLines(lines, currentChunk.StartLine, currentChunk.EndLine)
				chunks = append(chunks, *currentChunk)
			}
			chunkType := types.ChunkTypeFunction
			// If indented, it's likely a method
			if len(matches[1]) > 0 {
				chunkType = types.ChunkTypeMethod
			}
			currentChunk = &types.Chunk{
				Type:      chunkType,
				Name:      matches[2],
				StartLine: lineNum,
			}
			currentIndent = len(matches[1])
			continue
		}

		// Check if we've dedented (end of current block)
		if currentChunk != nil {
			trimmed := strings.TrimSpace(line)
			// Non-empty, non-comment line at same or lower indent level ends the block
			if len(trimmed) > 0 && !strings.HasPrefix(trimmed, "#") && getIndent(line) <= currentIndent {
				currentChunk.EndLine = lineNum - 1
				currentChunk.Content = getLines(lines, currentChunk.StartLine, currentChunk.EndLine)
				chunks = append(chunks, *currentChunk)
				currentChunk = nil
			}
		}
	}

	// Handle last chunk
	if currentChunk != nil {
		currentChunk.EndLine = len(lines)
		currentChunk.Content = getLines(lines, currentChunk.StartLine, currentChunk.EndLine)
		chunks = append(chunks, *currentChunk)
	}

	return chunks
}

// chunkJavaScript parses JavaScript/TypeScript using regex
func (c *Chunker) chunkJavaScript(content, filePath string) []types.Chunk {
	var chunks []types.Chunk
	lines := strings.Split(content, "\n")

	// Patterns for JS/TS
	funcPattern := regexp.MustCompile(`^\s*(?:export\s+)?(?:async\s+)?function\s+(\w+)`)
	arrowFuncPattern := regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?\([^)]*\)\s*=>`)
	classPattern := regexp.MustCompile(`^\s*(?:export\s+)?class\s+(\w+)`)
	methodPattern := regexp.MustCompile(`^\s+(?:async\s+)?(\w+)\s*\([^)]*\)\s*\{`)

	braceCount := 0
	var currentChunk *types.Chunk

	for i, line := range lines {
		lineNum := i + 1

		// Check for class
		if matches := classPattern.FindStringSubmatch(line); matches != nil {
			currentChunk = &types.Chunk{
				Type:      types.ChunkTypeClass,
				Name:      matches[1],
				StartLine: lineNum,
			}
			braceCount = 0
		}

		// Check for function
		if matches := funcPattern.FindStringSubmatch(line); matches != nil && currentChunk == nil {
			currentChunk = &types.Chunk{
				Type:      types.ChunkTypeFunction,
				Name:      matches[1],
				StartLine: lineNum,
			}
			braceCount = 0
		}

		// Check for arrow function
		if matches := arrowFuncPattern.FindStringSubmatch(line); matches != nil && currentChunk == nil {
			currentChunk = &types.Chunk{
				Type:      types.ChunkTypeFunction,
				Name:      matches[1],
				StartLine: lineNum,
			}
			braceCount = 0
		}

		// Check for method inside class
		if matches := methodPattern.FindStringSubmatch(line); matches != nil && currentChunk != nil && currentChunk.Type == types.ChunkTypeClass {
			// Add method as separate chunk
			methodChunk := types.Chunk{
				Type:      types.ChunkTypeMethod,
				Name:      currentChunk.Name + "." + matches[1],
				StartLine: lineNum,
			}

			// Find end of method
			methodBraces := 0
			methodEnd := lineNum
			for j := i; j < len(lines); j++ {
				methodBraces += strings.Count(lines[j], "{") - strings.Count(lines[j], "}")
				if methodBraces <= 0 && j > i {
					methodEnd = j + 1
					break
				}
			}
			methodChunk.EndLine = methodEnd
			methodChunk.Content = getLines(lines, methodChunk.StartLine, methodChunk.EndLine)
			chunks = append(chunks, methodChunk)
		}

		// Track braces
		braceCount += strings.Count(line, "{") - strings.Count(line, "}")

		// Check if chunk ended
		if currentChunk != nil && braceCount <= 0 && strings.Contains(line, "}") {
			currentChunk.EndLine = lineNum
			currentChunk.Content = getLines(lines, currentChunk.StartLine, currentChunk.EndLine)
			chunks = append(chunks, *currentChunk)
			currentChunk = nil
			braceCount = 0
		}
	}

	// Handle unclosed chunk
	if currentChunk != nil {
		currentChunk.EndLine = len(lines)
		currentChunk.Content = getLines(lines, currentChunk.StartLine, currentChunk.EndLine)
		chunks = append(chunks, *currentChunk)
	}

	return chunks
}

// chunkJavaLike parses Java/Kotlin/C# using brace counting
func (c *Chunker) chunkJavaLike(content, filePath, language string) []types.Chunk {
	var chunks []types.Chunk
	lines := strings.Split(content, "\n")

	classPattern := regexp.MustCompile(`^\s*(?:public|private|protected|internal|abstract|sealed|final|open)?\s*(?:static\s+)?(?:class|interface|enum|record|object)\s+(\w+)`)
	methodPattern := regexp.MustCompile(`^\s*(?:public|private|protected|internal|abstract|override|virtual|static|final|suspend|inline)?\s*(?:fun|void|[A-Z]\w*)\s+(\w+)\s*\(`)

	braceCount := 0
	var currentChunk *types.Chunk

	for i, line := range lines {
		lineNum := i + 1

		// Check for class/interface
		if matches := classPattern.FindStringSubmatch(line); matches != nil {
			if currentChunk != nil && currentChunk.Type == types.ChunkTypeClass {
				// Nested class - save current
				currentChunk.EndLine = lineNum - 1
				currentChunk.Content = getLines(lines, currentChunk.StartLine, currentChunk.EndLine)
				chunks = append(chunks, *currentChunk)
			}
			currentChunk = &types.Chunk{
				Type:      types.ChunkTypeClass,
				Name:      matches[1],
				StartLine: lineNum,
			}
		}

		// Check for method
		if matches := methodPattern.FindStringSubmatch(line); matches != nil {
			methodStart := lineNum
			methodBraces := 0
			methodEnd := lineNum

			// Find end of method
			for j := i; j < len(lines); j++ {
				methodBraces += strings.Count(lines[j], "{") - strings.Count(lines[j], "}")
				if methodBraces <= 0 && j > i && strings.Contains(lines[j], "}") {
					methodEnd = j + 1
					break
				}
			}

			methodChunk := types.Chunk{
				Type:      types.ChunkTypeMethod,
				Name:      matches[1],
				StartLine: methodStart,
				EndLine:   methodEnd,
				Content:   getLines(lines, methodStart, methodEnd),
			}
			chunks = append(chunks, methodChunk)
		}

		braceCount += strings.Count(line, "{") - strings.Count(line, "}")

		// Check if class ended
		if currentChunk != nil && currentChunk.Type == types.ChunkTypeClass && braceCount <= 0 && strings.Contains(line, "}") {
			currentChunk.EndLine = lineNum
			currentChunk.Content = getLines(lines, currentChunk.StartLine, currentChunk.EndLine)
			chunks = append(chunks, *currentChunk)
			currentChunk = nil
		}
	}

	return chunks
}

// chunkRust parses Rust source code
func (c *Chunker) chunkRust(content, filePath string) []types.Chunk {
	var chunks []types.Chunk
	lines := strings.Split(content, "\n")

	fnPattern := regexp.MustCompile(`^\s*(?:pub\s+)?(?:async\s+)?fn\s+(\w+)`)
	implPattern := regexp.MustCompile(`^\s*impl(?:<[^>]+>)?\s+(?:(\w+)\s+for\s+)?(\w+)`)
	structPattern := regexp.MustCompile(`^\s*(?:pub\s+)?struct\s+(\w+)`)
	enumPattern := regexp.MustCompile(`^\s*(?:pub\s+)?enum\s+(\w+)`)

	braceCount := 0
	var currentChunk *types.Chunk

	for i, line := range lines {
		lineNum := i + 1

		// Check for struct
		if matches := structPattern.FindStringSubmatch(line); matches != nil && currentChunk == nil {
			currentChunk = &types.Chunk{
				Type:      types.ChunkTypeClass,
				Name:      matches[1],
				StartLine: lineNum,
			}
			braceCount = 0
		}

		// Check for enum
		if matches := enumPattern.FindStringSubmatch(line); matches != nil && currentChunk == nil {
			currentChunk = &types.Chunk{
				Type:      types.ChunkTypeClass,
				Name:      matches[1],
				StartLine: lineNum,
			}
			braceCount = 0
		}

		// Check for impl block
		if matches := implPattern.FindStringSubmatch(line); matches != nil {
			name := matches[2]
			if matches[1] != "" {
				name = matches[1] + " for " + matches[2]
			}
			currentChunk = &types.Chunk{
				Type:      types.ChunkTypeClass,
				Name:      "impl " + name,
				StartLine: lineNum,
			}
			braceCount = 0
		}

		// Check for function
		if matches := fnPattern.FindStringSubmatch(line); matches != nil {
			fnStart := lineNum
			fnBraces := 0
			fnEnd := lineNum

			for j := i; j < len(lines); j++ {
				fnBraces += strings.Count(lines[j], "{") - strings.Count(lines[j], "}")
				if fnBraces <= 0 && j > i && strings.Contains(lines[j], "}") {
					fnEnd = j + 1
					break
				}
			}

			fnChunk := types.Chunk{
				Type:      types.ChunkTypeFunction,
				Name:      matches[1],
				StartLine: fnStart,
				EndLine:   fnEnd,
				Content:   getLines(lines, fnStart, fnEnd),
			}
			chunks = append(chunks, fnChunk)
		}

		braceCount += strings.Count(line, "{") - strings.Count(line, "}")

		if currentChunk != nil && braceCount <= 0 && strings.Contains(line, "}") {
			currentChunk.EndLine = lineNum
			currentChunk.Content = getLines(lines, currentChunk.StartLine, currentChunk.EndLine)
			chunks = append(chunks, *currentChunk)
			currentChunk = nil
		}
	}

	return chunks
}

// chunkByLines splits content into line-based chunks with overlap
func (c *Chunker) chunkByLines(content, filePath, language string) []types.Chunk {
	var chunks []types.Chunk
	lines := strings.Split(content, "\n")

	// If file is small enough, treat as single chunk
	if len(lines) <= c.maxChunkSize {
		chunks = append(chunks, types.Chunk{
			Content:   content,
			Type:      types.ChunkTypeFile,
			Name:      filePath,
			StartLine: 1,
			EndLine:   len(lines),
		})
		return chunks
	}

	// Split into overlapping chunks
	for i := 0; i < len(lines); i += c.maxChunkSize - c.overlapLines {
		endLine := i + c.maxChunkSize
		if endLine > len(lines) {
			endLine = len(lines)
		}

		chunkContent := strings.Join(lines[i:endLine], "\n")
		chunks = append(chunks, types.Chunk{
			Content:   chunkContent,
			Type:      types.ChunkTypeBlock,
			Name:      "",
			StartLine: i + 1,
			EndLine:   endLine,
		})

		if endLine >= len(lines) {
			break
		}
	}

	return chunks
}

// Helper functions

func getLines(lines []string, start, end int) string {
	if start < 1 {
		start = 1
	}
	if end > len(lines) {
		end = len(lines)
	}
	if start > end {
		return ""
	}
	return strings.Join(lines[start-1:end], "\n")
}

func getIndent(line string) int {
	count := 0
	for _, ch := range line {
		if ch == ' ' {
			count++
		} else if ch == '\t' {
			count += 4
		} else {
			break
		}
	}
	return count
}
