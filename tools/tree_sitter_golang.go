package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// extractGoAST lists the top-level declarations of a Go file with the pure-Go
// parser. It needs no cgo and no tree-sitter, so it is shared by the tree-sitter
// build (!windows && cgo) and by the dependency-free fallback build.
func extractGoAST(path string) string {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return ""
	}
	var out []string
	for _, decl := range node.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv != nil {
				out = append(out, "  method: "+d.Name.Name)
			} else {
				out = append(out, "  func: "+d.Name.Name)
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					switch s.Type.(type) {
					case *ast.InterfaceType:
						out = append(out, "  interface: "+s.Name.Name)
					case *ast.StructType:
						out = append(out, "  struct: "+s.Name.Name)
					default:
						out = append(out, "  type: "+s.Name.Name)
					}
				case *ast.ValueSpec:
					for _, n := range s.Names {
						switch d.Tok {
						case token.VAR:
							out = append(out, "  var: "+n.Name)
						case token.CONST:
							out = append(out, "  const: "+n.Name)
						}
					}
				}
			}
		}
	}
	return strings.Join(out, "\n")
}
