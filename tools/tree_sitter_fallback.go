//go:build windows || !cgo

package tools

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Dependency-free implementation of the symbol extractor, used whenever
// tree-sitter cannot be linked:
//   - on Windows (tree-sitter is cgo-only and has no Windows grammar support);
//   - on any platform when the binary is built with CGO_ENABLED=0.
//
// Fidelity: .go files keep full fidelity through extractGoAST (pure-Go parser).
// Every other language is approximated with line-oriented regexes: the declared
// names are usually right, nesting/visibility are not modelled and less common
// declaration forms are missed. It never fails hard: worst case it returns "".
//
// The labels deliberately mirror the tree-sitter ones ("  fn:", "  type:" …)
// so the snapshot output stays consistent across build flavours.

const (
	fallbackMaxSymbols = 4000
	fallbackMaxLineLen = 1 << 20
)

type symbolRule struct {
	re         *regexp.Regexp
	kind       string // label used for top-level matches
	nestedKind string // label used for indented matches ("" = kind)
}

func rule(re, kind, nestedKind string) symbolRule {
	return symbolRule{re: regexp.MustCompile(re), kind: kind, nestedKind: nestedKind}
}

var (
	jsRules = []symbolRule{
		rule(`^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s*\*?\s*([A-Za-z_$][\w$]*)`, "function", "function"),
		rule(`^\s*(?:export\s+)?(?:default\s+)?(?:abstract\s+)?class\s+([A-Za-z_$][\w$]*)`, "class", "class"),
		rule(`^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*(?:async\s+)?(?:function\b|\([^)]*\)\s*=>|[A-Za-z_$][\w$]*\s*=>)`, "  var", "  var"),
	}
	tsExtraRules = []symbolRule{
		rule(`^\s*(?:export\s+)?(?:declare\s+)?interface\s+([A-Za-z_$][\w$]*)`, "  interface", "  interface"),
		rule(`^\s*(?:export\s+)?(?:declare\s+)?type\s+([A-Za-z_$][\w$]*)\s*=`, "  type", "  type"),
		rule(`^\s*(?:export\s+)?(?:declare\s+)?(?:const\s+)?enum\s+([A-Za-z_$][\w$]*)`, "  enum", "  enum"),
	}
	tsRules = append(append([]symbolRule{}, jsRules...), tsExtraRules...)

	pyRules = []symbolRule{
		rule(`^\s*(?:async\s+)?def\s+([A-Za-z_]\w*)\s*\(`, "function", "  method"),
		rule(`^\s*class\s+([A-Za-z_]\w*)`, "class", "class"),
	}
	rsRules = []symbolRule{
		rule(`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?(?:unsafe\s+)?(?:extern\s+"[^"]*"\s+)?fn\s+([A-Za-z_]\w*)`, "  fn", "  fn"),
		rule(`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:struct|enum|union|trait|type)\s+([A-Za-z_]\w*)`, "  type", "  type"),
		rule(`^\s*(?:pub\s+)?impl(?:<[^>]*>)?\s+([A-Za-z_]\w*)`, "  impl", "  impl"),
		rule(`^\s*(?:pub\s+)?(?:const|static)\s+([A-Za-z_]\w*)`, "  const", "  const"),
		rule(`^\s*(?:pub\s+)?mod\s+([A-Za-z_]\w*)`, "  module", "  module"),
	}
	javaRules = []symbolRule{
		rule(`^\s*(?:(?:public|private|protected|static|final|abstract|sealed|non-sealed|strictfp)\s+)*(?:class|interface|enum|record)\s+([A-Za-z_$]\w*)`, "  type", "  type"),
		rule(`^\s*(?:(?:public|private|protected|static|final|abstract|synchronized|native|default|strictfp)\s+)+[\w<>\[\],.?]+\s+([A-Za-z_$]\w*)\s*\(`, "  method", "  method"),
	}
	csRules = []symbolRule{
		rule(`^\s*(?:(?:public|private|protected|internal|static|abstract|sealed|partial|readonly)\s+)*(?:class|struct|interface|enum|record|delegate)\s+([A-Za-z_$]\w*)`, "  type", "  type"),
		rule(`^\s*(?:(?:public|private|protected|internal|static|virtual|override|abstract|sealed|async|partial|extern|unsafe|new)\s+)+[\w<>\[\],.?]+\s+([A-Za-z_$]\w*)\s*\(`, "  method", "  method"),
	}
	swiftRules = []symbolRule{
		rule(`^\s*(?:(?:public|private|fileprivate|internal|open|final|indirect)\s+)*(?:class|struct|enum|protocol|extension|actor|typealias)\s+([A-Za-z_]\w*)`, "  type", "  type"),
		rule(`^\s*(?:(?:public|private|fileprivate|internal|open|final|static|class|override|mutating|nonmutating|async|convenience|required)\s+)*func\s+([A-Za-z_]\w*)`, "  func", "  func"),
	}
	ktRules = []symbolRule{
		rule(`^\s*(?:(?:public|private|protected|internal|open|final|abstract|sealed|data|inline|value|enum|annotation)\s+)*(?:class|interface|object|enum\s+class|data\s+class|sealed\s+class)\s+([A-Za-z_]\w*)`, "  type", "  type"),
		rule(`^\s*(?:(?:public|private|protected|internal|open|final|abstract|override|suspend|inline|operator|tailrec|external|private)\s+)*fun\s+([A-Za-z_]\w*)`, "  func", "  func"),
	}
	phpRules = []symbolRule{
		rule(`^\s*(?:final\s+|abstract\s+|readonly\s+)*(?:class|interface|trait|enum)\s+([A-Za-z_]\w*)`, "  type", "  type"),
		rule(`^\s*(?:(?:public|private|protected|static|final|abstract)\s+)*function\s+&?\s*([A-Za-z_]\w*)`, "  func", "  func"),
	}
	rbRules = []symbolRule{
		rule(`^\s*def\s+(?:self\.)?([A-Za-z_]\w*[!?=]?)`, "  method", "  method"),
		rule(`^\s*(?:class|module)\s+([A-Za-z_]\w*(?:::[A-Za-z_]\w*)*)`, "  type", "  type"),
	}
	shRules = []symbolRule{
		rule(`^\s*(?:function\s+)?([A-Za-z_][\w.-]*)\s*\(\s*\)\s*\{`, "  func", "  func"),
	}
	cLikeTypeRe = `^\s*(?:(?:public|private|protected|internal|static|final|abstract|typedef|template\s*<[^>]*>|export)\s+)*(?:class|struct|interface|enum|union|trait)\s+([A-Za-z_]\w*)`
	cRules      = []symbolRule{
		rule(cLikeTypeRe, "  type", "  type"),
		rule(`^[A-Za-z_][\w\s*&:<>,\[\]]*\b([A-Za-z_]\w*)\s*\([^;{}]*\)\s*\{$`, "  func", "  func"),
	}
	cppRules = append([]symbolRule{
		rule(cLikeTypeRe, "  type", "  type"),
		rule(`^[A-Za-z_][\w\s*&:<>,\[\]]*\b([A-Za-z_]\w*)\s*\([^;{}]*\)\s*\{$`, "  func", "  func"),
	}, rule(`^\s*namespace\s+([A-Za-z_]\w*)`, "  module", "  module"))
	cssRules = []symbolRule{
		rule(`^([.#]?[A-Za-z][^{};]*?)\s*\{`, "  selector", "  selector"),
	}
	htmlRules = []symbolRule{
		rule(`<\s*(header|main|section|article|nav|aside|footer|form|template)\b`, "  element", "  element"),
	}
	yamlRules = []symbolRule{
		rule(`^([A-Za-z_][\w.\-]*)\s*:(?:\s|$)`, "  key", "  key"),
	}
	tomlRules = []symbolRule{
		rule(`^\[([^\]\n]+)\]`, "  table", "  table"),
		rule(`^([A-Za-z_][\w.\-]*)\s*=`, "  key", "  key"),
	}
	sqlRules = []symbolRule{
		rule(`(?i)^\s*create\s+(?:or\s+replace\s+)?(?:unique\s+index|materialized\s+view|table|index|view|function|procedure)\s+(?:if\s+not\s+exists\s+)?([\w."\[\]]+)`, "  table", "  table"),
	}
)

// controlKeywords filters out control-flow lookalikes caught by the C function
// regex (`if (x) {`, `while (y) {`, …).
var controlKeywords = map[string]bool{
	"if": true, "for": true, "while": true, "switch": true, "catch": true,
	"do": true, "return": true, "sizeof": true, "else": true,
}

func extractSymbolsFromFile(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".go" {
		return extractGoAST(path)
	}
	return extractWithRules(path, fallbackRules(ext))
}

// fallbackRules returns the regex rule set for a file extension, or nil when the
// language has no approximation (the file is silently skipped).
func fallbackRules(ext string) []symbolRule {
	switch ext {
	case ".js", ".jsx", ".mjs", ".cjs":
		return jsRules
	case ".ts", ".tsx", ".mts", ".cts":
		return tsRules
	case ".py":
		return pyRules
	case ".rs":
		return rsRules
	case ".java":
		return javaRules
	case ".cs":
		return csRules
	case ".swift":
		return swiftRules
	case ".kt", ".kts":
		return ktRules
	case ".php":
		return phpRules
	case ".rb":
		return rbRules
	case ".sh", ".bash":
		return shRules
	case ".c", ".h":
		return cRules
	case ".cpp":
		return cppRules
	case ".css", ".scss", ".less":
		return cssRules
	case ".html", ".htm":
		return htmlRules
	case ".yaml", ".yml":
		return yamlRules
	case ".toml":
		return tomlRules
	case ".sql":
		return sqlRules
	default:
		return nil
	}
}

func extractWithRules(path string, rules []symbolRule) string {
	if len(rules) == 0 {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), fallbackMaxLineLen)

	var out []string
	seen := make(map[string]bool)
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*/") || strings.HasPrefix(trimmed, "*") {
			continue
		}
		indented := len(trimmed) != len(line)
		for _, r := range rules {
			m := r.re.FindStringSubmatch(trimmed)
			if m == nil {
				continue
			}
			name := m[1]
			if name == "" || controlKeywords[name] {
				continue
			}
			label := r.kind
			if indented && r.nestedKind != "" {
				label = r.nestedKind
			}
			entry := label + ": " + name
			if seen[entry] {
				break
			}
			seen[entry] = true
			out = append(out, entry)
			break
		}
		if len(out) >= fallbackMaxSymbols {
			break
		}
	}
	return strings.Join(out, "\n")
}
