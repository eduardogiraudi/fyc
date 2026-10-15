//go:build !windows && cgo

package tools

import (
	"fmt"
	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/bash"
	"github.com/smacker/go-tree-sitter/c"
	"github.com/smacker/go-tree-sitter/cpp"
	"github.com/smacker/go-tree-sitter/csharp"
	"github.com/smacker/go-tree-sitter/css"
	"github.com/smacker/go-tree-sitter/html"
	"github.com/smacker/go-tree-sitter/java"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/kotlin"
	"github.com/smacker/go-tree-sitter/php"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/ruby"
	"github.com/smacker/go-tree-sitter/rust"
	"github.com/smacker/go-tree-sitter/sql"
	"github.com/smacker/go-tree-sitter/swift"
	"github.com/smacker/go-tree-sitter/toml"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
	"github.com/smacker/go-tree-sitter/yaml"
	"os"
	"path/filepath"
	"strings"
)

func extractSymbolsFromFile(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		return extractGoAST(path)
	case ".js", ".jsx", ".mjs", ".cjs":
		return extractTreeSitter(path, "javascript")
	case ".ts", ".tsx", ".mts", ".cts":
		return extractTreeSitter(path, "typescript")
	case ".py":
		return extractTreeSitter(path, "python")
	case ".rs":
		return extractTreeSitter(path, "rust")
	case ".java":
		return extractTreeSitter(path, "java")
	case ".cpp":
		return extractTreeSitter(path, "cpp")
	case ".c", ".h":
		return extractTreeSitter(path, "c")
	case ".cs":
		return extractTreeSitter(path, "csharp")
	case ".php":
		return extractTreeSitter(path, "php")
	case ".rb":
		return extractTreeSitter(path, "ruby")
	case ".swift":
		return extractTreeSitter(path, "swift")
	case ".kt", ".kts":
		return extractTreeSitter(path, "kotlin")
	case ".sh", ".bash":
		return extractTreeSitter(path, "bash")
	case ".css", ".scss", ".less":
		return extractTreeSitter(path, "css")
	case ".html", ".htm":
		return extractTreeSitter(path, "html")
	case ".yaml", ".yml":
		return extractTreeSitter(path, "yaml")
	case ".toml":
		return extractTreeSitter(path, "toml")
	case ".sql":
		return extractTreeSitter(path, "sql")
	default:
		return ""
	}
}

func extractTreeSitter(path string, lang string) string {
	code, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	parser := sitter.NewParser()
	var langObj *sitter.Language
	switch lang {
	case "javascript":
		langObj = javascript.GetLanguage()
	case "typescript":
		langObj = typescript.GetLanguage()
	case "python":
		langObj = python.GetLanguage()
	case "rust":
		langObj = rust.GetLanguage()
	case "java":
		langObj = java.GetLanguage()
	case "cpp":
		langObj = cpp.GetLanguage()
	case "c":
		langObj = c.GetLanguage()
	case "csharp":
		langObj = csharp.GetLanguage()
	case "php":
		langObj = php.GetLanguage()
	case "ruby":
		langObj = ruby.GetLanguage()
	case "swift":
		langObj = swift.GetLanguage()
	case "kotlin":
		langObj = kotlin.GetLanguage()
	case "bash":
		langObj = bash.GetLanguage()
	case "css":
		langObj = css.GetLanguage()
	case "html":
		langObj = html.GetLanguage()
	case "yaml":
		langObj = yaml.GetLanguage()
	case "toml":
		langObj = toml.GetLanguage()
	case "sql":
		langObj = sql.GetLanguage()
	default:
		return fmt.Sprintf("error: no tree-sitter grammar for %s", lang)
	}
	parser.SetLanguage(langObj)
	tree := parser.Parse(nil, code)
	if tree == nil {
		return fmt.Sprintf("error: failed to parse %s with tree-sitter", path)
	}
	root := tree.RootNode()
	var out []string
	walkTree(root, code, &out, lang)
	if len(out) == 0 {
		return fmt.Sprintf("[context_snapshot warning] no symbols found in %s", path)
	}
	return strings.Join(out, "\n")
}

func walkTree(node *sitter.Node, code []byte, out *[]string, lang string) {
	switch lang {
	case "javascript":
		extractJS(node, code, out)
	case "typescript":
		extractJS(node, code, out)
	case "python":
		extractPython(node, code, out)
	case "rust":
		extractRust(node, code, out)
	case "java":
		extractJava(node, code, out)
	case "cpp":
		extractCpp(node, code, out)
	case "c":
		extractC(node, code, out)
	case "csharp":
		extractCsharp(node, code, out)
	case "php":
		extractPhp(node, code, out)
	case "ruby":
		extractRuby(node, code, out)
	case "swift":
		extractSwift(node, code, out)
	case "kotlin":
		extractKotlin(node, code, out)
	case "bash":
		extractBash(node, code, out)
	case "css":
		extractCSS(node, code, out)
	case "html":
		extractHTML(node, code, out)
	case "yaml":
		extractYAML(node, code, out)
	case "toml":
		extractTOML(node, code, out)
	case "sql":
		extractSQL(node, code, out)
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		walkTree(child, code, out, lang)
	}
}

func extractJS(node *sitter.Node, code []byte, out *[]string) {
	t := node.Type()
	switch t {
	case "function_declaration":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "function: "+string(code[name.StartByte():name.EndByte()]))
		}
	case "class_declaration":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "class: "+string(code[name.StartByte():name.EndByte()]))
		}
	case "method_definition":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  method: "+string(code[name.StartByte():name.EndByte()]))
		}
	case "variable_declarator":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  var: "+string(code[name.StartByte():name.EndByte()]))
		}
	case "interface_declaration":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  interface: "+string(code[name.StartByte():name.EndByte()]))
		}
	case "type_alias_declaration":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  type: "+string(code[name.StartByte():name.EndByte()]))
		}
	case "enum_declaration":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  enum: "+string(code[name.StartByte():name.EndByte()]))
		}
	}
}

func extractPython(node *sitter.Node, code []byte, out *[]string) {
	t := node.Type()
	switch t {
	case "function_definition":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "function: "+string(code[name.StartByte():name.EndByte()]))
		}
	case "class_definition":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "class: "+string(code[name.StartByte():name.EndByte()]))
		}
	case "decorated_definition":
		def := node.NamedChild(0)
		if def != nil {
			name := def.ChildByFieldName("name")
			if name != nil {
				*out = append(*out, "decorated: "+string(code[name.StartByte():name.EndByte()]))
			}
		}
	}
}

func extractRust(node *sitter.Node, code []byte, out *[]string) {
	t := node.Type()
	switch t {
	case "function_item":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  fn: "+string(code[name.StartByte():name.EndByte()]))
		}
	case "struct_item":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  struct: "+string(code[name.StartByte():name.EndByte()]))
		}
	case "impl_item":
		name := node.ChildByFieldName("type")
		if name != nil {
			*out = append(*out, "  impl: "+string(code[name.StartByte():name.EndByte()]))
		}
	case "trait_item":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  trait: "+string(code[name.StartByte():name.EndByte()]))
		}
	case "enum_item":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  enum: "+string(code[name.StartByte():name.EndByte()]))
		}
	case "const_item":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  const: "+string(code[name.StartByte():name.EndByte()]))
		}
	case "type_item":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  type: "+string(code[name.StartByte():name.EndByte()]))
		}
	case "mod_item":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  module: "+string(code[name.StartByte():name.EndByte()]))
		}
	}
}

func extractJava(node *sitter.Node, code []byte, out *[]string) {
	t := node.Type()
	switch t {
	case "class_declaration", "interface_declaration", "enum_declaration", "record_declaration":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  type: "+string(code[name.StartByte():name.EndByte()]))
		}
	case "method_declaration":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  method: "+string(code[name.StartByte():name.EndByte()]))
		}
	}
}

func extractCpp(node *sitter.Node, code []byte, out *[]string) {
	t := node.Type()
	switch t {
	case "class_specifier", "struct_specifier", "enum_specifier", "union_specifier":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  type: "+string(code[name.StartByte():name.EndByte()]))
		}
	case "function_definition":
		decl := node.ChildByFieldName("declarator")
		if decl != nil {
			name := decl.ChildByFieldName("declarator")
			if name != nil {
				id := name.ChildByFieldName("name")
				if id != nil {
					*out = append(*out, "  func: "+string(code[id.StartByte():id.EndByte()]))
				}
			}
		}
	}
}

func extractC(node *sitter.Node, code []byte, out *[]string) {
	t := node.Type()
	switch t {
	case "struct_specifier", "enum_specifier", "union_specifier":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  type: "+string(code[name.StartByte():name.EndByte()]))
		}
	case "function_definition":
		decl := node.ChildByFieldName("declarator")
		if decl != nil {
			id := decl.ChildByFieldName("declarator")
			if id != nil {
				*out = append(*out, "  func: "+string(code[id.StartByte():id.EndByte()]))
			}
		}
	}
}

func extractCsharp(node *sitter.Node, code []byte, out *[]string) {
	t := node.Type()
	switch t {
	case "class_declaration", "interface_declaration", "struct_declaration", "enum_declaration", "record_declaration":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  type: "+string(code[name.StartByte():name.EndByte()]))
		}
	case "method_declaration":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  method: "+string(code[name.StartByte():name.EndByte()]))
		}
	}
}

func extractPhp(node *sitter.Node, code []byte, out *[]string) {
	t := node.Type()
	switch t {
	case "class_declaration", "interface_declaration", "trait_declaration", "enum_declaration":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  type: "+string(code[name.StartByte():name.EndByte()]))
		}
	case "function_definition":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  func: "+string(code[name.StartByte():name.EndByte()]))
		}
	case "method_declaration":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  method: "+string(code[name.StartByte():name.EndByte()]))
		}
	}
}

func extractRuby(node *sitter.Node, code []byte, out *[]string) {
	t := node.Type()
	switch t {
	case "method":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  method: "+string(code[name.StartByte():name.EndByte()]))
		}
	case "class", "module":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  type: "+string(code[name.StartByte():name.EndByte()]))
		}
	}
}

func extractSwift(node *sitter.Node, code []byte, out *[]string) {
	t := node.Type()
	switch t {
	case "class_declaration", "struct_declaration", "enum_declaration", "protocol_declaration", "extension_declaration":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  type: "+string(code[name.StartByte():name.EndByte()]))
		}
	case "function_declaration":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  func: "+string(code[name.StartByte():name.EndByte()]))
		}
	}
}

func extractKotlin(node *sitter.Node, code []byte, out *[]string) {
	t := node.Type()
	switch t {
	case "class_declaration", "object_declaration", "interface_declaration", "enum_declaration":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  type: "+string(code[name.StartByte():name.EndByte()]))
		}
	case "function_declaration":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  func: "+string(code[name.StartByte():name.EndByte()]))
		}
	}
}

func extractBash(node *sitter.Node, code []byte, out *[]string) {
	t := node.Type()
	switch t {
	case "function_definition":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  func: "+string(code[name.StartByte():name.EndByte()]))
		}
	}
}

func extractCSS(node *sitter.Node, code []byte, out *[]string) {
	t := node.Type()
	switch t {
	case "rule_set":
		sel := node.ChildByFieldName("selector")
		if sel != nil {
			*out = append(*out, "  selector: "+string(code[sel.StartByte():sel.EndByte()]))
		}
	}
}

func extractHTML(node *sitter.Node, code []byte, out *[]string) {
	t := node.Type()
	switch t {
	case "element":
		tag := node.ChildByFieldName("tag_name")
		if tag != nil {
			*out = append(*out, "  element: "+string(code[tag.StartByte():tag.EndByte()]))
		}
	}
}

func extractYAML(node *sitter.Node, code []byte, out *[]string) {
	t := node.Type()
	switch t {
	case "block_mapping_pair":
		key := node.ChildByFieldName("key")
		if key != nil {
			*out = append(*out, "  key: "+string(code[key.StartByte():key.EndByte()]))
		}
	}
}

func extractTOML(node *sitter.Node, code []byte, out *[]string) {
	t := node.Type()
	switch t {
	case "table":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  table: "+string(code[name.StartByte():name.EndByte()]))
		}
	}
}

func extractSQL(node *sitter.Node, code []byte, out *[]string) {
	t := node.Type()
	switch t {
	case "create_table_statement":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  table: "+string(code[name.StartByte():name.EndByte()]))
		}
	case "create_index_statement":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  index: "+string(code[name.StartByte():name.EndByte()]))
		}
	case "create_view_statement":
		name := node.ChildByFieldName("name")
		if name != nil {
			*out = append(*out, "  view: "+string(code[name.StartByte():name.EndByte()]))
		}
	}
}
