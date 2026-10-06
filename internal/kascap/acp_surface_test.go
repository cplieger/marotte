package kascap

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// acpMethodRe matches an ACP method name written as a Go string literal.
//
// The four families are the whole wire vocabulary: the base protocol
// (`session/*`, `fs/*`, `terminal/*`, `initialize`) and kiro-cli's extensions
// (`_kiro/*`, `_session/*`).
var acpMethodRe = regexp.MustCompile(
	`"(_?kiro/[a-zA-Z/_]+|_?session/[a-zA-Z/_]+|fs/[a-zA-Z/_]+|terminal/[a-zA-Z/_]+|initialize)"`,
)

// acpMethodFloor is the count below which the derivation is presumed broken rather than the surface
// shrunk (82 when set).
const acpMethodFloor = 60

// TestACPMethodsPresent is the kiro-cli upgrade gate: does the pinned agent server still handle
// every ACP method marotte's sources name.
func TestACPMethodsPresent(t *testing.T) {
	active := activeKASVersion(t)
	src, path := bundleSource(t, active)
	methods := marotteACPMethods(t)

	if len(methods) < acpMethodFloor {
		t.Fatalf(`derived only %d ACP method name(s) from marotte's own sources, want >= %d.
The sweep, not the surface, is what changed: a method spelled by concatenation
or moved into a file this walk skips is invisible to it, and every assertion
below would then pass for having nothing to check.`, len(methods), acpMethodFloor)
	}

	var absent []string
	for _, m := range methods {
		if !strings.Contains(src, `"`+m+`"`) && !strings.Contains(src, `'`+m+`'`) {
			absent = append(absent, m)
		}
	}
	if len(absent) > 0 {
		t.Errorf(`kiro-cli %s does not carry %d ACP method name(s) marotte uses:
  %s
marotte either calls these and gets an unknown-method error, or serves them and
the handler is now dead. Read each one against the bundle: a RENAME upstream
needs the same rename here, and a REMOVAL needs the feature retired rather than
left calling into the void.
Bundle: %s`, active, len(absent), strings.Join(absent, "\n  "), path)
		return
	}
	t.Logf("kiro-cli %s carries all %d ACP method names marotte uses", active, len(methods))
}

// marotteACPMethods returns every ACP method name marotte's production sources name, sorted and
// deduplicated, derived so a new method is covered automatically.
func marotteACPMethods(t *testing.T) []string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	seen := make(map[string]bool)
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "static", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range acpMethodRe.FindAllStringSubmatch(string(raw), -1) {
			if strings.HasSuffix(m[1], "/") {
				continue
			}
			seen[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	slices.Sort(out)
	return out
}
