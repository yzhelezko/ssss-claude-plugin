package indexer

import (
	"context"
	"strings"

	"mcp-semantic-search/types"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/bash"
	"github.com/smacker/go-tree-sitter/c"
	"github.com/smacker/go-tree-sitter/cpp"
	"github.com/smacker/go-tree-sitter/csharp"
	"github.com/smacker/go-tree-sitter/css"
	"github.com/smacker/go-tree-sitter/cue"
	"github.com/smacker/go-tree-sitter/dockerfile"
	"github.com/smacker/go-tree-sitter/elixir"
	"github.com/smacker/go-tree-sitter/elm"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/groovy"
	"github.com/smacker/go-tree-sitter/hcl"
	"github.com/smacker/go-tree-sitter/html"
	"github.com/smacker/go-tree-sitter/java"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/kotlin"
	"github.com/smacker/go-tree-sitter/lua"
	"github.com/smacker/go-tree-sitter/ocaml"
	"github.com/smacker/go-tree-sitter/php"
	"github.com/smacker/go-tree-sitter/protobuf"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/ruby"
	"github.com/smacker/go-tree-sitter/rust"
	"github.com/smacker/go-tree-sitter/scala"
	"github.com/smacker/go-tree-sitter/sql"
	"github.com/smacker/go-tree-sitter/svelte"
	"github.com/smacker/go-tree-sitter/swift"
	"github.com/smacker/go-tree-sitter/toml"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
	"github.com/smacker/go-tree-sitter/yaml"
)

// Parser uses tree-sitter for multi-language code parsing
type Parser struct {
	parsers map[string]*sitter.Parser
}

// NewParser creates a new tree-sitter based parser
func NewParser() *Parser {
	p := &Parser{
		parsers: make(map[string]*sitter.Parser),
	}

	// Initialize parsers for each language (31 languages supported!)
	languages := map[string]*sitter.Language{
		// Core programming languages
		"go":         golang.GetLanguage(),
		"python":     python.GetLanguage(),
		"javascript": javascript.GetLanguage(),
		"typescript": typescript.GetLanguage(),
		"java":       java.GetLanguage(),
		"ruby":       ruby.GetLanguage(),
		"rust":       rust.GetLanguage(),
		"c":          c.GetLanguage(),
		"cpp":        cpp.GetLanguage(),
		"csharp":     csharp.GetLanguage(),
		"php":        php.GetLanguage(),
		"swift":      swift.GetLanguage(),
		"kotlin":     kotlin.GetLanguage(),
		"scala":      scala.GetLanguage(),

		// Functional languages
		"elixir": elixir.GetLanguage(),
		"elm":    elm.GetLanguage(),
		"ocaml":  ocaml.GetLanguage(),

		// Scripting languages
		"bash": bash.GetLanguage(),
		"lua":  lua.GetLanguage(),

		// Web technologies
		"html":   html.GetLanguage(),
		"css":    css.GetLanguage(),
		"svelte": svelte.GetLanguage(),

		// Data/Config formats
		"yaml":     yaml.GetLanguage(),
		"toml":     toml.GetLanguage(),
		"sql":      sql.GetLanguage(),
		"protobuf": protobuf.GetLanguage(),

		// Infrastructure/DevOps
		"dockerfile": dockerfile.GetLanguage(),
		"hcl":        hcl.GetLanguage(), // Terraform, etc.

		// Other
		"groovy": groovy.GetLanguage(),
		"cue":    cue.GetLanguage(),
	}

	for name, lang := range languages {
		parser := sitter.NewParser()
		parser.SetLanguage(lang)
		p.parsers[name] = parser
	}

	return p
}

// SymbolInfo represents extracted symbol information
type SymbolInfo struct {
	Name       string
	Type       types.ChunkType
	StartLine  int
	EndLine    int
	StartByte  uint32
	EndByte    uint32
	Content    string
	IsExported bool
	Calls      []string // Functions/methods this symbol calls
	References []string // Types/variables this symbol references
	Parent     string   // Parent symbol (e.g., class name for methods)
}

// ParseResult contains all extracted information from a file
type ParseResult struct {
	Symbols []SymbolInfo
	Imports []string
	IsTest  bool
}

// Parse parses source code and extracts symbols with their references
func (p *Parser) Parse(ctx context.Context, content []byte, language string) (*ParseResult, error) {
	parser, ok := p.parsers[language]
	if !ok {
		// Fall back to nil for unsupported languages
		return nil, nil
	}

	tree, err := parser.ParseCtx(ctx, nil, content)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	result := &ParseResult{
		Symbols: make([]SymbolInfo, 0),
		Imports: make([]string, 0),
	}

	// Extract symbols based on language
	rootNode := tree.RootNode()
	p.extractSymbols(rootNode, content, language, result, "")

	return result, nil
}

// extractSymbols recursively extracts symbols from the AST
func (p *Parser) extractSymbols(node *sitter.Node, content []byte, language string, result *ParseResult, parent string) {
	if node == nil {
		return
	}

	nodeType := node.Type()

	// Check for test files
	if !result.IsTest {
		result.IsTest = p.isTestFile(node, content, language)
	}

	// Extract imports
	if p.isImportNode(nodeType, language) {
		importText := string(content[node.StartByte():node.EndByte()])
		result.Imports = append(result.Imports, importText)
	}

	// Extract symbols based on node type and language
	if symbol := p.extractSymbol(node, content, language, parent); symbol != nil {
		// Extract calls and references from the symbol's body
		symbol.Calls = p.extractCalls(node, content, language)
		symbol.References = p.extractReferences(node, content, language)
		result.Symbols = append(result.Symbols, *symbol)

		// For classes/structs, set parent for child methods
		if symbol.Type == types.ChunkTypeClass {
			parent = symbol.Name
		}
	}

	// Recurse into children
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		p.extractSymbols(child, content, language, result, parent)
	}
}

// extractSymbol extracts a symbol from a node if applicable
func (p *Parser) extractSymbol(node *sitter.Node, content []byte, language, parent string) *SymbolInfo {
	nodeType := node.Type()

	var symbolType types.ChunkType
	var nameNode *sitter.Node
	var isExported bool

	switch language {
	case "go":
		switch nodeType {
		case "function_declaration":
			symbolType = types.ChunkTypeFunction
			nameNode = node.ChildByFieldName("name")
		case "method_declaration":
			symbolType = types.ChunkTypeMethod
			nameNode = node.ChildByFieldName("name")
			// Get receiver type for full name
			if recv := node.ChildByFieldName("receiver"); recv != nil {
				parent = p.getReceiverType(recv, content)
			}
		case "type_declaration":
			symbolType = types.ChunkTypeClass
			// Find type_spec child
			for i := 0; i < int(node.ChildCount()); i++ {
				child := node.Child(i)
				if child.Type() == "type_spec" {
					nameNode = child.ChildByFieldName("name")
					break
				}
			}
		}

	case "python":
		switch nodeType {
		case "function_definition":
			if parent != "" {
				symbolType = types.ChunkTypeMethod
			} else {
				symbolType = types.ChunkTypeFunction
			}
			nameNode = node.ChildByFieldName("name")
		case "class_definition":
			symbolType = types.ChunkTypeClass
			nameNode = node.ChildByFieldName("name")
		}

	case "javascript", "typescript":
		switch nodeType {
		case "function_declaration":
			symbolType = types.ChunkTypeFunction
			nameNode = node.ChildByFieldName("name")
		case "method_definition":
			symbolType = types.ChunkTypeMethod
			nameNode = node.ChildByFieldName("name")
		case "class_declaration":
			symbolType = types.ChunkTypeClass
			nameNode = node.ChildByFieldName("name")
		case "arrow_function":
			// Check if it's assigned to a variable
			if parent := node.Parent(); parent != nil && parent.Type() == "variable_declarator" {
				symbolType = types.ChunkTypeFunction
				nameNode = parent.ChildByFieldName("name")
			}
		}

	case "java", "csharp":
		switch nodeType {
		case "method_declaration":
			symbolType = types.ChunkTypeMethod
			nameNode = node.ChildByFieldName("name")
		case "class_declaration":
			symbolType = types.ChunkTypeClass
			nameNode = node.ChildByFieldName("name")
		case "interface_declaration":
			symbolType = types.ChunkTypeClass
			nameNode = node.ChildByFieldName("name")
		case "constructor_declaration":
			symbolType = types.ChunkTypeMethod
			nameNode = node.ChildByFieldName("name")
		}

	case "rust":
		switch nodeType {
		case "function_item":
			symbolType = types.ChunkTypeFunction
			nameNode = node.ChildByFieldName("name")
		case "impl_item":
			symbolType = types.ChunkTypeClass
			// impl blocks have type_identifier
			for i := 0; i < int(node.ChildCount()); i++ {
				child := node.Child(i)
				if child.Type() == "type_identifier" {
					nameNode = child
					break
				}
			}
		case "struct_item":
			symbolType = types.ChunkTypeClass
			nameNode = node.ChildByFieldName("name")
		case "enum_item":
			symbolType = types.ChunkTypeClass
			nameNode = node.ChildByFieldName("name")
		}

	case "ruby":
		switch nodeType {
		case "method":
			symbolType = types.ChunkTypeMethod
			nameNode = node.ChildByFieldName("name")
		case "singleton_method":
			symbolType = types.ChunkTypeMethod
			nameNode = node.ChildByFieldName("name")
		case "class":
			symbolType = types.ChunkTypeClass
			nameNode = node.ChildByFieldName("name")
		case "module":
			symbolType = types.ChunkTypeClass
			nameNode = node.ChildByFieldName("name")
		}

	case "c", "cpp":
		switch nodeType {
		case "function_definition":
			symbolType = types.ChunkTypeFunction
			if declarator := node.ChildByFieldName("declarator"); declarator != nil {
				nameNode = p.findIdentifier(declarator)
			}
		case "class_specifier", "struct_specifier":
			symbolType = types.ChunkTypeClass
			nameNode = node.ChildByFieldName("name")
		}

	case "php":
		switch nodeType {
		case "function_definition":
			symbolType = types.ChunkTypeFunction
			nameNode = node.ChildByFieldName("name")
		case "method_declaration":
			symbolType = types.ChunkTypeMethod
			nameNode = node.ChildByFieldName("name")
		case "class_declaration":
			symbolType = types.ChunkTypeClass
			nameNode = node.ChildByFieldName("name")
		}

	case "swift":
		switch nodeType {
		case "function_declaration":
			symbolType = types.ChunkTypeFunction
			nameNode = node.ChildByFieldName("name")
		case "class_declaration":
			symbolType = types.ChunkTypeClass
			nameNode = node.ChildByFieldName("name")
		case "struct_declaration":
			symbolType = types.ChunkTypeClass
			nameNode = node.ChildByFieldName("name")
		case "protocol_declaration":
			symbolType = types.ChunkTypeClass
			nameNode = node.ChildByFieldName("name")
		}

	case "kotlin":
		switch nodeType {
		case "function_declaration":
			symbolType = types.ChunkTypeFunction
			nameNode = node.ChildByFieldName("name")
		case "class_declaration":
			symbolType = types.ChunkTypeClass
			nameNode = node.ChildByFieldName("name")
		case "object_declaration":
			symbolType = types.ChunkTypeClass
			nameNode = node.ChildByFieldName("name")
		}

	case "scala":
		switch nodeType {
		case "function_definition":
			symbolType = types.ChunkTypeFunction
			nameNode = node.ChildByFieldName("name")
		case "class_definition":
			symbolType = types.ChunkTypeClass
			nameNode = node.ChildByFieldName("name")
		case "object_definition":
			symbolType = types.ChunkTypeClass
			nameNode = node.ChildByFieldName("name")
		case "trait_definition":
			symbolType = types.ChunkTypeClass
			nameNode = node.ChildByFieldName("name")
		}

	case "elixir":
		switch nodeType {
		case "call": // def, defp, defmodule
			// Check if it's a function definition
			if fn := node.ChildByFieldName("target"); fn != nil {
				fnName := string(content[fn.StartByte():fn.EndByte()])
				if fnName == "def" || fnName == "defp" {
					symbolType = types.ChunkTypeFunction
					if args := node.ChildByFieldName("arguments"); args != nil && args.ChildCount() > 0 {
						nameNode = args.Child(0)
					}
				} else if fnName == "defmodule" {
					symbolType = types.ChunkTypeClass
					if args := node.ChildByFieldName("arguments"); args != nil && args.ChildCount() > 0 {
						nameNode = args.Child(0)
					}
				}
			}
		}

	case "lua":
		switch nodeType {
		case "function_declaration":
			symbolType = types.ChunkTypeFunction
			nameNode = node.ChildByFieldName("name")
		case "local_function":
			symbolType = types.ChunkTypeFunction
			nameNode = node.ChildByFieldName("name")
		}

	case "bash":
		switch nodeType {
		case "function_definition":
			symbolType = types.ChunkTypeFunction
			nameNode = node.ChildByFieldName("name")
		}

	case "sql":
		switch nodeType {
		case "create_function_statement":
			symbolType = types.ChunkTypeFunction
			nameNode = node.ChildByFieldName("name")
		case "create_table_statement":
			symbolType = types.ChunkTypeClass
			nameNode = node.ChildByFieldName("name")
		case "create_view_statement":
			symbolType = types.ChunkTypeClass
			nameNode = node.ChildByFieldName("name")
		}

	case "hcl":
		switch nodeType {
		case "block":
			symbolType = types.ChunkTypeBlock
			// HCL blocks have type and labels
			if typeNode := node.Child(0); typeNode != nil {
				nameNode = typeNode
			}
		}

	case "dockerfile":
		// Dockerfile doesn't have traditional functions, treat stages as blocks
		switch nodeType {
		case "from_instruction":
			symbolType = types.ChunkTypeBlock
			nameNode = node.ChildByFieldName("image")
		}

	case "elm":
		switch nodeType {
		case "function_declaration_left":
			symbolType = types.ChunkTypeFunction
			nameNode = node.Child(0) // First child is the function name
		case "type_alias_declaration":
			symbolType = types.ChunkTypeClass
			nameNode = node.ChildByFieldName("name")
		case "type_declaration":
			symbolType = types.ChunkTypeClass
			nameNode = node.ChildByFieldName("name")
		}

	case "ocaml":
		switch nodeType {
		case "let_binding":
			symbolType = types.ChunkTypeFunction
			if pattern := node.ChildByFieldName("pattern"); pattern != nil {
				nameNode = pattern
			}
		case "type_binding":
			symbolType = types.ChunkTypeClass
			nameNode = node.ChildByFieldName("name")
		case "module_binding":
			symbolType = types.ChunkTypeClass
			nameNode = node.ChildByFieldName("name")
		}

	case "groovy":
		switch nodeType {
		case "method_declaration":
			symbolType = types.ChunkTypeMethod
			nameNode = node.ChildByFieldName("name")
		case "class_declaration":
			symbolType = types.ChunkTypeClass
			nameNode = node.ChildByFieldName("name")
		}

	case "protobuf":
		switch nodeType {
		case "message":
			symbolType = types.ChunkTypeClass
			nameNode = node.ChildByFieldName("name")
		case "service":
			symbolType = types.ChunkTypeClass
			nameNode = node.ChildByFieldName("name")
		case "rpc":
			symbolType = types.ChunkTypeFunction
			nameNode = node.ChildByFieldName("name")
		}

	case "css", "html", "svelte", "yaml", "toml", "cue":
		// These are markup/config languages - use block-based chunking
		// They don't have traditional function/class structures
		return nil
	}

	if symbolType == "" || nameNode == nil {
		return nil
	}

	name := string(content[nameNode.StartByte():nameNode.EndByte()])

	// Check if exported (public)
	isExported = p.isExported(name, node, language)

	// Build full name with parent
	fullName := name
	if parent != "" && symbolType == types.ChunkTypeMethod {
		fullName = parent + "." + name
	}

	return &SymbolInfo{
		Name:       fullName,
		Type:       symbolType,
		StartLine:  int(node.StartPoint().Row) + 1,
		EndLine:    int(node.EndPoint().Row) + 1,
		StartByte:  node.StartByte(),
		EndByte:    node.EndByte(),
		Content:    string(content[node.StartByte():node.EndByte()]),
		IsExported: isExported,
		Parent:     parent,
	}
}

// extractCalls extracts function/method calls from a node
func (p *Parser) extractCalls(node *sitter.Node, content []byte, language string) []string {
	calls := make(map[string]bool)
	p.findCalls(node, content, language, calls)

	result := make([]string, 0, len(calls))
	for call := range calls {
		result = append(result, call)
	}
	return result
}

// findCalls recursively finds all function calls
func (p *Parser) findCalls(node *sitter.Node, content []byte, language string, calls map[string]bool) {
	if node == nil {
		return
	}

	nodeType := node.Type()

	// Detect call expressions based on language
	isCall := false
	var nameNode *sitter.Node

	switch language {
	case "go":
		if nodeType == "call_expression" {
			if fn := node.ChildByFieldName("function"); fn != nil {
				nameNode = fn
				isCall = true
			}
		}
	case "python":
		if nodeType == "call" {
			if fn := node.ChildByFieldName("function"); fn != nil {
				nameNode = fn
				isCall = true
			}
		}
	case "javascript", "typescript":
		if nodeType == "call_expression" {
			if fn := node.ChildByFieldName("function"); fn != nil {
				nameNode = fn
				isCall = true
			}
		}
	case "java", "csharp":
		if nodeType == "method_invocation" || nodeType == "invocation_expression" {
			nameNode = node.ChildByFieldName("name")
			isCall = true
		}
	case "rust":
		if nodeType == "call_expression" {
			if fn := node.ChildByFieldName("function"); fn != nil {
				nameNode = fn
				isCall = true
			}
		}
	case "ruby":
		if nodeType == "call" || nodeType == "method_call" {
			nameNode = node.ChildByFieldName("method")
			isCall = true
		}
	case "c", "cpp":
		if nodeType == "call_expression" {
			if fn := node.ChildByFieldName("function"); fn != nil {
				nameNode = fn
				isCall = true
			}
		}
	case "php":
		if nodeType == "function_call_expression" || nodeType == "method_call_expression" {
			nameNode = node.ChildByFieldName("name")
			isCall = true
		}
	}

	if isCall && nameNode != nil {
		callName := p.extractCallName(nameNode, content)
		if callName != "" && !isKeyword(callName, language) {
			calls[callName] = true
		}
	}

	// Recurse into children
	for i := 0; i < int(node.ChildCount()); i++ {
		p.findCalls(node.Child(i), content, language, calls)
	}
}

// extractCallName extracts the function name from a call expression
func (p *Parser) extractCallName(node *sitter.Node, content []byte) string {
	if node == nil {
		return ""
	}

	nodeType := node.Type()

	// Handle different call patterns
	switch nodeType {
	case "identifier", "type_identifier":
		return string(content[node.StartByte():node.EndByte()])
	case "selector_expression", "member_expression", "attribute":
		// Get the full selector (e.g., "obj.method")
		return string(content[node.StartByte():node.EndByte()])
	case "field_expression":
		if field := node.ChildByFieldName("field"); field != nil {
			return string(content[field.StartByte():field.EndByte()])
		}
	}

	// For complex expressions, try to get the rightmost identifier
	text := string(content[node.StartByte():node.EndByte()])
	if idx := strings.LastIndex(text, "."); idx != -1 {
		return text[idx+1:]
	}
	return text
}

// extractReferences extracts type/variable references from a node
func (p *Parser) extractReferences(node *sitter.Node, content []byte, language string) []string {
	refs := make(map[string]bool)
	p.findReferences(node, content, language, refs)

	result := make([]string, 0, len(refs))
	for ref := range refs {
		result = append(result, ref)
	}
	return result
}

// findReferences recursively finds type references
func (p *Parser) findReferences(node *sitter.Node, content []byte, language string, refs map[string]bool) {
	if node == nil {
		return
	}

	nodeType := node.Type()

	// Detect type references based on language
	switch language {
	case "go":
		if nodeType == "type_identifier" {
			name := string(content[node.StartByte():node.EndByte()])
			if !isBuiltinType(name, language) {
				refs[name] = true
			}
		}
	case "python":
		// Python type hints
		if nodeType == "type" || nodeType == "identifier" {
			parent := node.Parent()
			if parent != nil && (parent.Type() == "type" || strings.Contains(parent.Type(), "annotation")) {
				name := string(content[node.StartByte():node.EndByte()])
				if !isBuiltinType(name, language) {
					refs[name] = true
				}
			}
		}
	case "java", "csharp", "typescript":
		if nodeType == "type_identifier" || nodeType == "identifier" {
			parent := node.Parent()
			if parent != nil && strings.Contains(parent.Type(), "type") {
				name := string(content[node.StartByte():node.EndByte()])
				if !isBuiltinType(name, language) {
					refs[name] = true
				}
			}
		}
	}

	// Recurse into children
	for i := 0; i < int(node.ChildCount()); i++ {
		p.findReferences(node.Child(i), content, language, refs)
	}
}

// Helper functions

func (p *Parser) isImportNode(nodeType, language string) bool {
	switch language {
	case "go":
		return nodeType == "import_declaration"
	case "python":
		return nodeType == "import_statement" || nodeType == "import_from_statement"
	case "javascript", "typescript":
		return nodeType == "import_statement"
	case "java":
		return nodeType == "import_declaration"
	case "rust":
		return nodeType == "use_declaration"
	case "ruby":
		return nodeType == "require" || nodeType == "require_relative"
	case "csharp":
		return nodeType == "using_directive"
	case "php":
		return nodeType == "namespace_use_declaration"
	}
	return false
}

func (p *Parser) isTestFile(node *sitter.Node, content []byte, language string) bool {
	// Check based on content patterns
	contentStr := string(content)

	switch language {
	case "go":
		return strings.Contains(contentStr, "func Test") || strings.Contains(contentStr, "testing.T")
	case "python":
		return strings.Contains(contentStr, "def test_") || strings.Contains(contentStr, "unittest") || strings.Contains(contentStr, "pytest")
	case "javascript", "typescript":
		return strings.Contains(contentStr, "describe(") || strings.Contains(contentStr, "it(") || strings.Contains(contentStr, "test(")
	case "java":
		return strings.Contains(contentStr, "@Test") || strings.Contains(contentStr, "junit")
	case "ruby":
		return strings.Contains(contentStr, "RSpec") || strings.Contains(contentStr, "def test_")
	case "rust":
		return strings.Contains(contentStr, "#[test]") || strings.Contains(contentStr, "#[cfg(test)]")
	case "csharp":
		return strings.Contains(contentStr, "[Test]") || strings.Contains(contentStr, "[Fact]")
	case "php":
		return strings.Contains(contentStr, "PHPUnit") || strings.Contains(contentStr, "function test")
	}
	return false
}

func (p *Parser) isExported(name string, node *sitter.Node, language string) bool {
	switch language {
	case "go":
		// Go: exported if first letter is uppercase
		return len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z'
	case "python":
		// Python: not exported if starts with underscore
		return !strings.HasPrefix(name, "_")
	case "javascript", "typescript":
		// Check for export keyword in ancestors
		return p.hasExportModifier(node)
	case "java", "csharp":
		// Check for public modifier
		return p.hasPublicModifier(node)
	case "rust":
		// Check for pub keyword
		return p.hasPubModifier(node)
	case "ruby":
		// Ruby methods are public by default unless in private section
		return true
	case "php":
		return p.hasPublicModifier(node)
	}
	return true
}

func (p *Parser) hasExportModifier(node *sitter.Node) bool {
	// Walk up to find export_statement
	current := node
	for current != nil {
		if current.Type() == "export_statement" {
			return true
		}
		current = current.Parent()
	}
	return false
}

func (p *Parser) hasPublicModifier(node *sitter.Node) bool {
	// Check for modifiers child with "public"
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "modifiers" || child.Type() == "modifier" {
			// This is simplified - would need content check
			return true
		}
	}
	return false
}

func (p *Parser) hasPubModifier(node *sitter.Node) bool {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "visibility_modifier" {
			return true
		}
	}
	return false
}

func (p *Parser) getReceiverType(recv *sitter.Node, content []byte) string {
	// Extract receiver type from Go method receiver
	for i := 0; i < int(recv.ChildCount()); i++ {
		child := recv.Child(i)
		if child.Type() == "parameter_declaration" {
			if typeNode := child.ChildByFieldName("type"); typeNode != nil {
				typeText := string(content[typeNode.StartByte():typeNode.EndByte()])
				// Remove pointer prefix
				typeText = strings.TrimPrefix(typeText, "*")
				return typeText
			}
		}
	}
	return ""
}

func (p *Parser) findIdentifier(node *sitter.Node) *sitter.Node {
	if node.Type() == "identifier" {
		return node
	}
	for i := 0; i < int(node.ChildCount()); i++ {
		if found := p.findIdentifier(node.Child(i)); found != nil {
			return found
		}
	}
	return nil
}

// isKeyword checks if a name is a language keyword
func isKeyword(name, language string) bool {
	keywords := map[string]map[string]bool{
		"go": {
			"if": true, "else": true, "for": true, "range": true, "switch": true,
			"case": true, "default": true, "return": true, "break": true, "continue": true,
			"go": true, "defer": true, "select": true, "chan": true, "map": true,
			"make": true, "new": true, "len": true, "cap": true, "append": true,
			"copy": true, "delete": true, "panic": true, "recover": true, "print": true,
			"println": true, "close": true, "error": true, "nil": true, "true": true, "false": true,
		},
		"python": {
			"if": true, "else": true, "elif": true, "for": true, "while": true,
			"try": true, "except": true, "finally": true, "with": true, "as": true,
			"import": true, "from": true, "class": true, "def": true, "return": true,
			"yield": true, "raise": true, "pass": true, "break": true, "continue": true,
			"lambda": true, "and": true, "or": true, "not": true, "in": true, "is": true,
			"None": true, "True": true, "False": true, "print": true, "len": true,
			"range": true, "list": true, "dict": true, "set": true, "tuple": true,
			"str": true, "int": true, "float": true, "bool": true, "type": true,
			"self": true, "cls": true, "super": true, "isinstance": true, "hasattr": true,
		},
		"javascript": {
			"if": true, "else": true, "for": true, "while": true, "do": true,
			"switch": true, "case": true, "default": true, "break": true, "continue": true,
			"return": true, "throw": true, "try": true, "catch": true, "finally": true,
			"function": true, "class": true, "new": true, "this": true, "super": true,
			"import": true, "export": true, "const": true, "let": true, "var": true,
			"async": true, "await": true, "typeof": true, "instanceof": true,
			"null": true, "undefined": true, "true": true, "false": true,
			"console": true, "require": true, "module": true, "exports": true,
			"Array": true, "Object": true, "String": true, "Number": true, "Boolean": true,
			"Promise": true, "Map": true, "Set": true, "JSON": true, "Math": true,
		},
		"typescript": {
			"if": true, "else": true, "for": true, "while": true, "do": true,
			"switch": true, "case": true, "default": true, "break": true, "continue": true,
			"return": true, "throw": true, "try": true, "catch": true, "finally": true,
			"function": true, "class": true, "new": true, "this": true, "super": true,
			"import": true, "export": true, "const": true, "let": true, "var": true,
			"async": true, "await": true, "typeof": true, "instanceof": true,
			"null": true, "undefined": true, "true": true, "false": true,
			"interface": true, "type": true, "enum": true, "namespace": true,
			"public": true, "private": true, "protected": true, "readonly": true,
			"any": true, "unknown": true, "never": true, "void": true,
		},
	}

	if langKeywords, ok := keywords[language]; ok {
		return langKeywords[name]
	}

	// Default common keywords
	commonKeywords := map[string]bool{
		"if": true, "else": true, "for": true, "while": true, "return": true,
		"break": true, "continue": true, "true": true, "false": true, "null": true,
		"new": true, "this": true, "self": true, "class": true, "function": true,
	}
	return commonKeywords[name]
}

// isBuiltinType checks if a type name is a built-in type
func isBuiltinType(name, language string) bool {
	builtins := map[string]map[string]bool{
		"go": {
			"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
			"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
			"float32": true, "float64": true, "complex64": true, "complex128": true,
			"string": true, "bool": true, "byte": true, "rune": true, "error": true,
			"any": true, "comparable": true,
		},
		"python": {
			"int": true, "float": true, "str": true, "bool": true, "list": true,
			"dict": true, "set": true, "tuple": true, "None": true, "bytes": true,
			"object": true, "type": true, "range": true, "slice": true,
		},
		"javascript": {
			"string": true, "number": true, "boolean": true, "object": true,
			"function": true, "undefined": true, "symbol": true, "bigint": true,
		},
		"typescript": {
			"string": true, "number": true, "boolean": true, "object": true,
			"any": true, "unknown": true, "never": true, "void": true, "null": true,
			"undefined": true, "symbol": true, "bigint": true,
		},
	}

	if langBuiltins, ok := builtins[language]; ok {
		return langBuiltins[name]
	}
	return false
}

// SupportedLanguages returns the list of languages supported by tree-sitter
func (p *Parser) SupportedLanguages() []string {
	langs := make([]string, 0, len(p.parsers))
	for lang := range p.parsers {
		langs = append(langs, lang)
	}
	return langs
}

// IsSupported checks if a language is supported
func (p *Parser) IsSupported(language string) bool {
	_, ok := p.parsers[language]
	return ok
}
