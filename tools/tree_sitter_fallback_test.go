//go:build windows || !cgo

package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests only run on the dependency-free build flavour: Windows, or any
// platform with CGO_ENABLED=0 (`CGO_ENABLED=0 go test ./tools/`).

func writeFallbackSample(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

func TestFallbackGoSymbols(t *testing.T) {
	p := writeFallbackSample(t, "sample.go", `package demo

const Answer = 42

var counter int

type Config struct{}

type Doer interface {
	Do() error
}

type MyInt int

func main() {}

func (c Config) Run() {}
`)
	got := extractSymbolsFromFile(p)
	for _, want := range []string{
		"  const: Answer", "  var: counter", "  struct: Config",
		"  interface: Doer", "  type: MyInt", "  func: main", "  method: Run",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFallbackRegexSymbols(t *testing.T) {
	cases := []struct {
		file    string
		content string
		want    []string
	}{
		{
			"sample.py",
			"import os\n\ndef top(a):\n    pass\n\nclass Widget:\n    def method(self):\n        pass\n",
			[]string{"function: top", "class: Widget", "  method: method"},
		},
		{
			"sample.js",
			"export function run() {}\nclass Greeter {}\nconst of = async () => 1;\n",
			[]string{"function: run", "class: Greeter", "  var: of"},
		},
		{
			"sample.ts",
			"export interface Opts {}\ntype Id = string;\nenum Color { Red }\n",
			[]string{"  interface: Opts", "  type: Id", "  enum: Color"},
		},
		{
			"sample.rs",
			"pub fn main() {}\nstruct Point;\nimpl Point {}\npub const MAX: u8 = 1;\n",
			[]string{"  fn: main", "  type: Point", "  impl: Point", "  const: MAX"},
		},
		{
			"sample.java",
			"public class App {\n    public static void main(String[] a) {\n    }\n}\n",
			[]string{"  type: App", "  method: main"},
		},
		{
			"sample.c",
			"static int add(int a, int b) {\n  return a + b;\n}\n",
			[]string{"  func: add"},
		},
		{
			"sample.sh",
			"my_fn() {\n  echo hi\n}\n",
			[]string{"  func: my_fn"},
		},
		{
			"sample.scss",
			".card, .panel {\n  color: red;\n}\n",
			[]string{"  selector: .card, .panel"},
		},
		{
			"config.toml",
			"[server]\nport = 8080\n",
			[]string{"  table: server", "  key: port"},
		},
		{
			"schema.sql",
			"create table users (id int);\n",
			[]string{"  table: users"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			got := extractSymbolsFromFile(writeFallbackSample(t, tc.file, tc.content))
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("%s: missing %q in:\n%s", tc.file, want, got)
				}
			}
		})
	}
}

func TestFallbackNoFalsePositives(t *testing.T) {
	cases := []struct {
		file    string
		content string
	}{
		{"sample.c", "int main(void) {\n  if (x) {\n  }\n  while (y) {\n  }\n  return 0;\n}\n"},
		{"sample.py", "# def commented(a):\n\"\"\"docstring\"\"\"\n"},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			got := extractSymbolsFromFile(writeFallbackSample(t, tc.file, tc.content))
			for _, bad := range []string{"if:", "while:", "return:", "commented"} {
				if strings.Contains(got, bad) {
					t.Errorf("%s: unexpected %q in:\n%s", tc.file, bad, got)
				}
			}
		})
	}
}

func TestFallbackUnsupportedAndMissing(t *testing.T) {
	if got := extractSymbolsFromFile(writeFallbackSample(t, "notes.txt", "hello\n")); got != "" {
		t.Errorf("unsupported extension: want empty, got %q", got)
	}
	if got := extractSymbolsFromFile(filepath.Join(t.TempDir(), "nope.py")); got != "" {
		t.Errorf("missing file: want empty, got %q", got)
	}
}
