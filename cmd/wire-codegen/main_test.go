package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const repoRoot = "../.."

// committedOutputs is every generated file in the tree: the *.gen.ts files under static-src/wire
// and the two copies of the OpenAPI document.
func committedOutputs(t *testing.T) []string {
	t.Helper()
	gen, err := filepath.Glob(filepath.Join(repoRoot, "static-src", "wire", "*.gen.ts"))
	if err != nil {
		t.Fatalf("glob static-src/wire: %v", err)
	}
	out := []string{filepath.Join("docs", "openapi.json"), filepath.Join("internal", "apitable", "openapi.json")}
	for _, g := range gen {
		rel, err := filepath.Rel(repoRoot, g)
		if err != nil {
			t.Fatalf("rel %s: %v", g, err)
		}
		out = append(out, rel)
	}
	slices.Sort(out)
	return out
}

func TestCodegen_GeneratedFilesMatch(t *testing.T) {
	tmp := t.TempDir()
	if err := generate(t.Context(), tmp); err != nil {
		t.Fatalf("generate: %v", err)
	}
	var written []string
	err := filepath.WalkDir(tmp, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(tmp, path)
		if err != nil {
			return err
		}
		written = append(written, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk the generated tree: %v", err)
	}
	slices.Sort(written)
	for _, rel := range written {
		got, err := os.ReadFile(filepath.Join(tmp, rel))
		if err != nil {
			t.Fatalf("read generated %s: %v", rel, err)
		}
		want, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Errorf("%s is generated but not committed; run go run ./cmd/wire-codegen", rel)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from a fresh generation; run go run ./cmd/wire-codegen", rel)
		}
	}
	for _, rel := range committedOutputs(t) {
		if !slices.Contains(written, rel) {
			t.Errorf("%s is committed but the generator no longer writes it", rel)
		}
	}
	if !slices.Contains(written, filepath.Join("static-src", "wire", "sockets.gen.ts")) {
		t.Error("the generator wrote no sockets.gen.ts")
	}
}

// apiLiteral is a quoted or template string that names an /api/ path.
var apiLiteral = regexp.MustCompile("[\"'`]/api(/|[\"'`?])")

// stripComments drops // line comments and /* */ blocks outside string literals, so a docstring
// naming a path is not a hand-written call.
func stripComments(src string) string {
	var b strings.Builder
	var quote byte
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case quote != 0:
			b.WriteByte(c)
			if c == '\\' && i+1 < len(src) {
				i++
				b.WriteByte(src[i])
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
			b.WriteByte(c)
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			b.WriteByte('\n')
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return b.String()
			}
			b.WriteString(strings.Repeat("\n", strings.Count(src[i:i+2+end+2], "\n")))
			i += 2 + end + 1
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func TestClient_NoHandWrittenAPIPaths(t *testing.T) {
	root := filepath.Join(repoRoot, "static-src")
	scanned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", "wire", "__test-helpers__", "testdata", "e2e-sse", "scripts":
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".test.ts") || strings.HasSuffix(name, ".d.ts") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		for i, line := range strings.Split(stripComments(string(src)), "\n") {
			if apiLiteral.MatchString(line) {
				rel, _ := filepath.Rel(repoRoot, path)
				t.Errorf("%s:%d: a hand-written /api/ path; call the generated descriptor: %s", rel, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan static-src: %v", err)
	}
	if scanned < 100 {
		t.Fatalf("scanned %d client files, want the whole client", scanned)
	}
}
