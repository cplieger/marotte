package filebrowse

import (
	"path"
	"strings"
	"testing"
)

// FuzzFilesFilter_NeverPanics feeds untrusted filter text and paths through compile and match; a
// compiled filter must answer every subject, and an exclude must be what its include would be.
func FuzzFilesFilter_NeverPanics(f *testing.F) {
	seeds := []struct{ raw, srel string }{
		{"*.css, !node_modules", "web/static-src/css/a.css"},
		{".md", "docs/a.md"},
		{"src/**/*.ts", "src/x/y/a.ts"},
		{"./docs/**", "docs/a/b"},
		{"{*.css,*.scss}", "a.scss"},
		{"{a,{b,{c,d}}}", "c"},
		{"[!a-z]?", "Q1"},
		{`\*`, "*"},
		{"build/", "build"},
		{"**", "a/b/c"},
		{"[a-", "a"},
		{"\x00,\xff", "\xff"},
		{"!", ""},
		{"*.CSS", "web/A.css"},
		{strings.Repeat("{a,b}", 7), "a"},
	}
	for _, s := range seeds {
		f.Add(s.raw, s.srel, false)
	}
	f.Fuzz(func(t *testing.T, raw, srel string, caseSensitive bool) {
		filter, err := compileFilesFilter(raw, caseSensitive)
		if err != nil {
			return
		}
		sub := newEntrySubject(path.Base(srel), srel, false, caseSensitive)
		inc := filter.includes(sub)
		negated := make([]string, 0, len(filter.include))
		for _, part := range splitTopLevelCommas(raw) {
			if p := strings.TrimSpace(part); p != "" && !strings.HasPrefix(p, "!") {
				negated = append(negated, "!"+p)
			}
		}
		asExcludes, err := compileFilesFilter(strings.Join(negated, ","), caseSensitive)
		if err != nil {
			t.Fatalf("filter %q compiled but its negation %q did not: %v", raw, negated, err)
		}
		if got := asExcludes.excludes(newEntrySubject(path.Base(srel), srel, false, caseSensitive)); got != inc {
			t.Fatalf("filter %q includes %q = %v, but as excludes = %v", raw, srel, inc, got)
		}
	})
}

// FuzzGlob_AgreesWithPathMatch is the oracle for the segment matcher: on a basename pattern
// without the three extensions path.Match lacks (`**`, braces, `[!`), both accept the same
// patterns and match the same names.
func FuzzGlob_AgreesWithPathMatch(f *testing.F) {
	seeds := []struct{ pattern, name string }{
		{"*.go", "a.go"},
		{"*.go", "a.goo"},
		{"a?c", "abc"},
		{"[a-c]x", "bx"},
		{"[^a-c]x", "bx"},
		{`\*`, "*"},
		{`a\`, "a"},
		{"[]", "a"},
		{"[a-]", "a"},
		{"*\xa9", "é"},
		{"*??", "\u6ad6"},
		{"é*", "één.go"},
		{"\xff", "\xfe"},
		{"*a*a*a*b", "aaaaaaaaaaaaaaaaaaaa"},
		{"", ""},
	}
	for _, s := range seeds {
		f.Add(s.pattern, s.name)
	}
	f.Fuzz(func(t *testing.T, pattern, name string) {
		if strings.Contains(pattern, "**") || strings.ContainsAny(pattern, "{/") ||
			strings.Contains(pattern, "[!") || strings.Contains(name, "/") {
			return
		}
		want, wantErr := path.Match(pattern, name)
		g, err := compileSegment(pattern, starRunIsStar)
		if (err != nil) != (wantErr != nil) {
			t.Fatalf("compileSegment(%q) error = %v, path.Match error = %v", pattern, err, wantErr)
		}
		if err != nil {
			return
		}
		if got := g.match(name); got != want {
			t.Fatalf("segGlob(%q).match(%q) = %v, path.Match = %v", pattern, name, got, want)
		}
	})
}
