package filebrowse

import (
	"path"
	"strings"
	"testing"
	"time"
)

// includedBy compiles raw as a Files filter and reports whether it INCLUDES the entry at srel.
func includedBy(t *testing.T, raw string, caseSensitive bool, srel string, isDir bool) bool {
	t.Helper()
	f, err := compileFilesFilter(raw, caseSensitive)
	if err != nil {
		t.Fatalf("compileFilesFilter(%q): %v", raw, err)
	}
	return f.includes(newEntrySubject(path.Base(srel), srel, isDir, caseSensitive))
}

type filterCase struct {
	srel  string
	isDir bool
	want  bool
}

func checkFilter(t *testing.T, raw string, caseSensitive bool, cases []filterCase) {
	t.Helper()
	for _, c := range cases {
		if got := includedBy(t, raw, caseSensitive, c.srel, c.isDir); got != c.want {
			t.Errorf("filter %q on %q (dir %v) = %v, want %v", raw, c.srel, c.isDir, got, c.want)
		}
	}
}

func TestFilesFilter_BareDotTermMatchesExtension(t *testing.T) {
	checkFilter(t, ".md", false, []filterCase{
		{srel: "a.md", want: true},
		{srel: "docs/deep/b.md", want: true},
		{srel: ".md", want: true},
		{srel: "a.mdx", want: false},
		{srel: "md", want: false},
		{srel: "a.md.bak", want: false},
	})
}

func TestFilesFilter_BareNameMatchesFileOrDirAtAnyDepth(t *testing.T) {
	checkFilter(t, "node_modules", false, []filterCase{
		{srel: "node_modules", isDir: true, want: true},
		{srel: "web/static-src/node_modules", isDir: true, want: true},
		{srel: "a/node_modules", want: true},
		{srel: "node_modules2", isDir: true, want: false},
		{srel: "my_node_modules", isDir: true, want: false},
	})
}

func TestFilesFilter_StarGlobMatchesBasename(t *testing.T) {
	checkFilter(t, "*.css", false, []filterCase{
		{srel: "a.css", want: true},
		{srel: "static-src/css/19-files.css", want: true},
		{srel: "a.css.map", want: false},
		{srel: "css/readme", want: false},
	})
	checkFilter(t, "test_*", false, []filterCase{
		{srel: "test_a.go", want: true},
		{srel: "test_dir/a.go", want: false},
	})
}

func TestFilesFilter_SlashPatternImpliesDoubleStarUnlessAnchored(t *testing.T) {
	checkFilter(t, "css/*.css", false, []filterCase{
		{srel: "css/a.css", want: true},
		{srel: "static-src/css/a.css", want: true},
		{srel: "css/deep/a.css", want: false},
	})
	for _, anchored := range []string{"./css/*.css", "/css/*.css"} {
		checkFilter(t, anchored, false, []filterCase{
			{srel: "css/a.css", want: true},
			{srel: "static-src/css/a.css", want: false},
		})
	}
}

func TestFilesFilter_DoubleStarCrossesSegments(t *testing.T) {
	checkFilter(t, "src/**/*.ts", false, []filterCase{
		{srel: "src/a.ts", want: true},
		{srel: "src/x/y/z/a.ts", want: true},
		{srel: "pkg/src/x/a.ts", want: true},
		{srel: "lib/a.ts", want: false},
	})
	checkFilter(t, "src/*.ts", false, []filterCase{
		{srel: "src/a.ts", want: true},
		{srel: "src/x/a.ts", want: false},
	})
}

func TestFilesFilter_TrailingSlashMatchesDirectoriesOnly(t *testing.T) {
	checkFilter(t, "build/", false, []filterCase{
		{srel: "build", isDir: true, want: true},
		{srel: "a/build", isDir: true, want: true},
		{srel: "build", want: false},
	})
}

func TestFilesFilter_BracesHoldCommas(t *testing.T) {
	checkFilter(t, "{*.css,*.scss}, *.md", false, []filterCase{
		{srel: "a.css", want: true},
		{srel: "a.scss", want: true},
		{srel: "a.md", want: true},
		{srel: "a.go", want: false},
	})
	f, err := compileFilesFilter("{*.css,*.scss}, *.md", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.include) != 2 {
		t.Errorf("compileFilesFilter split into %d patterns, want 2: a comma inside braces is the alternation's", len(f.include))
	}
	checkFilter(t, "{a,b}{c,d}{e,f}{g,h}{i,j}", false, []filterCase{
		{srel: "acegi", want: true},
		{srel: "bdfhj", want: true},
		{srel: "abcde", want: false},
	})
}

func TestFilesFilter_FollowsMatchCase(t *testing.T) {
	checkFilter(t, "*.CSS", false, []filterCase{{srel: "a.css", want: true}, {srel: "B.Css", want: true}})
	checkFilter(t, "*.CSS", true, []filterCase{{srel: "a.css", want: false}, {srel: "a.CSS", want: true}})
	checkFilter(t, "README", false, []filterCase{{srel: "docs/readme", want: true}})
	checkFilter(t, "README", true, []filterCase{{srel: "docs/readme", want: false}})
}

func TestFilesFilter_ExcludePrefixMakesAnExclude(t *testing.T) {
	f, err := compileFilesFilter("*.ts, !*.test.ts", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.include) != 1 || len(f.exclude) != 1 {
		t.Fatalf("compileFilesFilter = %d includes, %d excludes, want 1 and 1", len(f.include), len(f.exclude))
	}
	sub := newEntrySubject("a.test.ts", "a.test.ts", false, false)
	if !f.excludes(sub) {
		t.Error("!*.test.ts did not exclude a.test.ts")
	}
	for _, raw := range []string{"!.md", "!*.md"} {
		ex, err := compileFilesFilter(raw, false)
		if err != nil {
			t.Fatal(err)
		}
		if !ex.excludes(newEntrySubject("notes.md", "docs/notes.md", false, false)) {
			t.Errorf("%q did not exclude docs/notes.md", raw)
		}
	}
}

func TestCompileFilesFilter_RejectsMalformedAndOversize(t *testing.T) {
	tooMany := strings.Repeat("*.a,", maxFilterItems) + "*.b"
	tests := map[string]string{
		"unclosed class":         "[a-",
		"empty class":            "[]",
		"unclosed brace":         "{a,b",
		"trailing escape":        `a\`,
		"bare exclude":           "!",
		"bare slash":             "/",
		"empty segment":          "a//b",
		"too many patterns":      tooMany,
		"oversize pattern":       strings.Repeat("a", maxFilterItemLen+1),
		"braces nested too deep": "{a,{b,{c,{d,{e,f}}}}}",
		"too many alternatives":  strings.Repeat("{a,b}", 7),
		"one-alternative chain":  strings.Repeat("{a}", maxBraceSteps),
		"double star in a name":  "src/a**b/file.ts",
		"double star suffix":     "**.css",
		"double star in a base":  "a**",
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := compileFilesFilter(raw, false); err == nil {
				t.Errorf("compileFilesFilter(%q) = nil error, want a refusal", raw)
			}
		})
	}
	for _, ok := range []string{
		"", " , ,", strings.Repeat("a", maxFilterItemLen), strings.Repeat("*.a,", maxFilterItems-1) + "*.b",
		"**", "src/**", "src/**/*.ts", `a\**`,
		"{a,b}{c,d}{e,f}{g,h}{i,j}", strings.Repeat("{a,b}", 6), "{a,{b,{c,{d,e}}}}",
	} {
		if _, err := compileFilesFilter(ok, false); err != nil {
			t.Errorf("compileFilesFilter(%.40q…) = %v, want accepted", ok, err)
		}
	}
}

func TestCompileQuery_RejectsTooManyAndOversizeTerms(t *testing.T) {
	for name, raw := range map[string]string{
		"too many terms":        strings.Repeat("a ", maxFilterItems+1),
		"oversize term":         strings.Repeat("a", maxFilterItemLen+1),
		"malformed glob":        "*.[c",
		"double star in a name": "a**b",
	} {
		if _, err := compileQuery(raw, false); err == nil {
			t.Errorf("%s: compileQuery = nil error, want a refusal", name)
		}
	}
	q, err := compileQuery(`"half typed`, false)
	if err != nil || len(q.terms) != 1 {
		t.Errorf(`compileQuery("\"half typed") = %d terms, %v; want one literal term`, len(q.terms), err)
	}
}

// An exponential matcher backtracks every star against every position; this one keeps one
// backtrack point, so 100 stars over 64 KiB costs about 6.5M steps.
func TestGlob_PathologicalStarsStayLinear(t *testing.T) {
	pattern := strings.Repeat("a*", 100) + "b"
	subject := strings.Repeat("a", 64<<10)
	g, err := compileSegment(pattern, starRunIsStar)
	if err != nil {
		t.Fatal(err)
	}
	segs := strings.Split(strings.Repeat("a/", 200)+"x", "/")
	p, err := compilePath(strings.Repeat("**/a/", 50)+"b", starRunIsStar)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	gotSeg := g.match(subject)
	gotPath := p.match(segs)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("matching took %v, want well under a second: the matcher is not linear", elapsed)
	}
	if gotSeg || gotPath {
		t.Errorf("match = %v, %v, want false for both: neither subject ends with b", gotSeg, gotPath)
	}
}
