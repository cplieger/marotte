package steering

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseSteeringFrontmatter(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want docEntry
	}{
		{
			name: "no frontmatter defaults to always",
			in:   "# Title\n\nBody.\n",
			want: docEntry{Inclusion: "always"},
		},
		{
			name: "explicit always",
			in:   "---\ninclusion: always\n---\nbody",
			want: docEntry{Inclusion: "always"},
		},
		{
			name: "fileMatch with pattern",
			in:   "---\ninclusion: fileMatch\nfileMatchPattern: \"internal/**/*.go\"\ndescription: Go layout\n---\n",
			want: docEntry{Inclusion: "fileMatch", FileMatch: "internal/**/*.go", Description: "Go layout"},
		},
		{
			name: "manual",
			in:   "---\ninclusion: manual\ndescription: Incident runbook\n---\n",
			want: docEntry{Inclusion: "manual", Description: "Incident runbook"},
		},
		{
			name: "unknown inclusion falls back to always",
			in:   "---\ninclusion: bogus\n---\n",
			want: docEntry{Inclusion: "always"},
		},
		{
			name: "single-quoted values",
			in:   "---\ninclusion: 'fileMatch'\nfileMatchPattern: 'cmd/*.go'\n---\n",
			want: docEntry{Inclusion: "fileMatch", FileMatch: "cmd/*.go"},
		},
		{
			name: "missing closing fence falls back to always",
			in:   "---\ninclusion: fileMatch\nbody without closing\n",
			want: docEntry{Inclusion: "always"},
		},
		{
			name: "empty file",
			in:   "",
			want: docEntry{Inclusion: "always"},
		},
		{
			name: "front-matter without an inclusion key",
			in:   "---\ndescription: no inclusion key\n---\n# Doc",
			want: docEntry{Inclusion: "always", Description: "no inclusion key"},
		},
		{
			name: "inclusion value padded with spaces",
			in:   "---\ninclusion:   manual  \n---\n# Doc",
			want: docEntry{Inclusion: "manual"},
		},
		{
			name: "CRLF line endings",
			in:   "---\r\ninclusion: manual\r\ndescription: runbook\r\n---\r\nbody",
			want: docEntry{Inclusion: "manual", Description: "runbook"},
		},
		{
			name: "leading UTF-8 BOM",
			in:   "\ufeff---\ninclusion: fileMatch\nfileMatchPattern: \"**/*.go\"\n---\n",
			want: docEntry{Inclusion: "fileMatch", FileMatch: "**/*.go"},
		},
		{
			name: "BOM and CRLF together",
			in:   "\ufeff---\r\ninclusion: manual\r\n---\r\n",
			want: docEntry{Inclusion: "manual"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseSteeringFrontmatter([]byte(tc.in))
			if got != tc.want {
				t.Errorf("parseSteeringFrontmatter() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestFindRepoDocs_ClassifiesByFrontmatter(t *testing.T) {
	dir := t.TempDir()
	steering := filepath.Join(dir, ".kiro", "steering")
	if err := os.MkdirAll(steering, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"architecture.md": "---\ninclusion: always\ndescription: Arch overview\n---\nbody",
		"go-layout.md":    "---\ninclusion: fileMatch\nfileMatchPattern: \"internal/**/*.go\"\n---\nbody",
		"runbook.md":      "---\ninclusion: manual\n---\nbody",
		"plain.md":        "no frontmatter\n", // defaults to always
	}
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(steering, n), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	docs, _ := findRepoDocs(dir)
	if len(docs) != 4 {
		t.Fatalf("got %d docs, want 4", len(docs))
	}
	byName := map[string]docEntry{}
	for _, d := range docs {
		byName[d.Filename] = d
	}
	if d := byName["architecture.md"]; d.Inclusion != "always" || d.Description != "Arch overview" {
		t.Errorf("architecture.md = %+v", d)
	}
	if d := byName["go-layout.md"]; d.Inclusion != "fileMatch" || d.FileMatch != "internal/**/*.go" {
		t.Errorf("go-layout.md = %+v", d)
	}
	if d := byName["runbook.md"]; d.Inclusion != "manual" {
		t.Errorf("runbook.md = %+v", d)
	}
	if d := byName["plain.md"]; d.Inclusion != "always" {
		t.Errorf("plain.md = %+v (no frontmatter should default to always)", d)
	}
}

func TestFindRepoDocs_NoSteeringDir(t *testing.T) {
	dir := t.TempDir()
	if docs, seen := findRepoDocs(dir); docs != nil || seen != (dirCount{}) {
		t.Errorf("expected nil, got %+v", docs)
	}
}

func TestFindRepoDocs_CapAt20(t *testing.T) {
	dir := t.TempDir()
	steering := filepath.Join(dir, ".kiro", "steering")
	if err := os.MkdirAll(steering, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 25 {
		name := fmt.Sprintf("doc-%02d.md", i)
		if err := os.WriteFile(filepath.Join(steering, name), []byte("body"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	docs, seen := findRepoDocs(dir)
	if len(docs) != 20 || seen.total != 25 {
		t.Errorf("got %d docs of %d, want 20 (capped) of 25", len(docs), seen.total)
	}
}

// An "auto" doc is on-demand, so it belongs under the Manual header ("read on
// demand") and must NOT be announced to the agent as always-loaded. KAS groups
// the two the same way, offering both as slash commands.
func TestWriteRepoSteering_AutoIsOnDemandNotAlwaysLoaded(t *testing.T) {
	var b strings.Builder
	writeRepoSteering(&b, "myrepo", []docEntry{{Filename: "auto.md", Inclusion: inclusionAuto}})
	out := b.String()
	if strings.Contains(out, "Always-loaded steering") {
		t.Errorf("an auto doc was announced as always-loaded:\n%s", out)
	}
	if !strings.Contains(out, "Manual steering") {
		t.Errorf("an auto doc is missing from the on-demand group:\n%s", out)
	}
}

// TestWriteRepoSteering_OmitsEmptyGroupHeaders verifies each inclusion
// group's header is emitted only when that group holds at least one doc.
func TestWriteRepoSteering_OmitsEmptyGroupHeaders(t *testing.T) {
	t.Run("only fileMatch docs omit the Always-loaded header", func(t *testing.T) {
		var b strings.Builder
		writeRepoSteering(&b, "myrepo", []docEntry{{Filename: "match.md", Inclusion: "fileMatch"}})
		out := b.String()
		if strings.Contains(out, "Always-loaded steering") {
			t.Errorf("writeRepoSteering(only fileMatch) emitted Always-loaded header:\n%s", out)
		}
		if !strings.Contains(out, "File-match steering") {
			t.Errorf("writeRepoSteering(only fileMatch) missing File-match header:\n%s", out)
		}
	})
	t.Run("only always docs omit the File-match and Manual headers", func(t *testing.T) {
		var b strings.Builder
		writeRepoSteering(&b, "myrepo", []docEntry{{Filename: "always.md", Inclusion: "always"}})
		out := b.String()
		if strings.Contains(out, "File-match steering") {
			t.Errorf("writeRepoSteering(only always) emitted File-match header:\n%s", out)
		}
		if strings.Contains(out, "Manual steering") {
			t.Errorf("writeRepoSteering(only always) emitted Manual header:\n%s", out)
		}
		if !strings.Contains(out, "Always-loaded steering") {
			t.Errorf("writeRepoSteering(only always) missing Always-loaded header:\n%s", out)
		}
	})
}

// TestWriteRepoSkills_HeadersAndEntryFields verifies a file-match skill sits apart from every other
// skill, a group header appears only for a non-empty group, and the FileMatch / Description
// annotations render only when set.
func TestWriteRepoSkills_HeadersAndEntryFields(t *testing.T) {
	t.Run("fileMatch and manual: both groups, fields rendered", func(t *testing.T) {
		var b strings.Builder
		writeRepoSkills(&b, "myrepo", []docEntry{
			{Filename: "match/SKILL.md", Inclusion: "fileMatch", FileMatch: "internal/**", Description: "go layout"},
			{Filename: "ref/SKILL.md", Inclusion: "manual"},
		})
		out := b.String()
		if !strings.Contains(out, "  - **File-match skills** (`myrepo/.kiro/skills/`):\n    - `myrepo/.kiro/skills/match/SKILL.md` (matches `internal/**`) — go layout\n") {
			t.Errorf("the fileMatch skill is not under File-match skills with its fields:\n%s", out)
		}
		if !strings.Contains(out, "  - **Skills** (`myrepo/.kiro/skills/`; read a SKILL.md when its description matches the task):\n    - `myrepo/.kiro/skills/ref/SKILL.md`\n") {
			t.Errorf("the manual skill is not under Skills:\n%s", out)
		}
	})
	t.Run("an always skill is read on description match, not announced as loaded", func(t *testing.T) {
		var b strings.Builder
		writeRepoSkills(&b, "myrepo", []docEntry{{Filename: "build/SKILL.md", Inclusion: "always"}})
		out := b.String()
		if strings.Contains(out, "File-match skills") || strings.Contains(out, "Always-loaded") {
			t.Errorf("an always skill got a header other than Skills:\n%s", out)
		}
		if !strings.Contains(out, "**Skills**") {
			t.Errorf("missing Skills header:\n%s", out)
		}
		if strings.Contains(out, "(matches") || strings.Contains(out, "SKILL.md` —") {
			t.Errorf("rendered an annotation for an empty field:\n%s", out)
		}
	})
}

func TestCapWords(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"at the cap is kept whole", words(20), words(20)},
		{"over the cap is cut with a marker", words(21), words(20) + " …"},
		{"short is kept", "one two", "one two"},
		{"empty stays empty", "", ""},
		{"one long token is cut at the byte cap", strings.Repeat("x", 400), strings.Repeat("x", 300) + " …"},
		{"a byte cut never splits a rune", strings.Repeat("é", 200), strings.Repeat("é", 150) + " …"},
		{"a byte cut drops the space it lands after", strings.Repeat("x", 299) + " yyyy", strings.Repeat("x", 299) + " …"},
		// words(19) and its space take 76 bytes.
		{"a word cut over the byte cap is marked once", words(19) + " " + strings.Repeat("z", 400) + " w", words(19) + " " + strings.Repeat("z", 300-76) + " …"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := capWords(tc.in, maxDescWords); got != tc.want {
				t.Errorf("capWords(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func hookDoc(hooks ...string) string {
	return `{"version":"v1","hooks":[` + strings.Join(hooks, ",") + `]}`
}

// TestParseHookDoc_CommandLengthBoundary pins the 80-byte action-preview
// cap: a command of exactly 80 bytes is kept verbatim, while 81 bytes is
// truncated to 80 chars ending in "...".
func TestParseHookDoc_CommandLengthBoundary(t *testing.T) {
	cmd80 := strings.Repeat("a", 80)
	hs := parseHookDoc([]byte(hookDoc(
		`{"name":"n","trigger":"PreToolUse","action":{"type":"command","command":"` + cmd80 + `"}}`,
	)))
	if len(hs) != 1 || hs[0].Command != cmd80 {
		t.Errorf("parseHookDoc(80-byte command) = %+v, want one entry with the 80-byte command unchanged", hs)
	}
	cmd81 := strings.Repeat("b", 81)
	hs2 := parseHookDoc([]byte(hookDoc(
		`{"action":{"type":"command","command":"` + cmd81 + `"}}`,
	)))
	if len(hs2) != 1 || len(hs2[0].Command) != 80 || !strings.HasSuffix(hs2[0].Command, "...") {
		t.Errorf("parseHookDoc(81-byte command) = %+v, want 80 bytes ending in '...'", hs2)
	}
}

// TestParseHookDoc_Shapes covers the v1 envelope variants: multiple hooks
// per file, agent hooks previewing their prompt, malformed JSON, and the
// legacy pre-v1 shape (no hooks array) yielding nothing.
func TestParseHookDoc_Shapes(t *testing.T) {
	t.Run("multiple hooks in one document", func(t *testing.T) {
		hs := parseHookDoc([]byte(hookDoc(
			`{"name":"A","trigger":"PostFileSave","action":{"type":"command","command":"lint"}}`,
			`{"name":"B","trigger":"SessionStart","action":{"type":"agent","prompt":"load context"}}`,
		)))
		if len(hs) != 2 {
			t.Fatalf("parseHookDoc(2 hooks) = %+v, want 2 entries", hs)
		}
		if hs[0].Name != "A" || hs[0].Trigger != "PostFileSave" || hs[0].Command != "lint" {
			t.Errorf("hs[0] = %+v, want {A PostFileSave lint}", hs[0])
		}
		// Agent hooks preview their prompt in the Command field.
		if hs[1].Name != "B" || hs[1].Trigger != "SessionStart" || hs[1].Command != "load context" {
			t.Errorf("hs[1] = %+v, want {B SessionStart load context}", hs[1])
		}
	})
	t.Run("malformed and legacy shapes yield nil", func(t *testing.T) {
		for _, data := range []string{`not json`, ``, `{"event_type":"preToolUse","command":"x"}`} {
			if hs := parseHookDoc([]byte(data)); len(hs) != 0 {
				t.Errorf("parseHookDoc(%q) = %+v, want empty", data, hs)
			}
		}
	})
	t.Run("hostile fields are sanitised", func(t *testing.T) {
		hs := parseHookDoc([]byte(hookDoc(
			`{"name":"evil\ninject","trigger":"PreToolUse","action":{"type":"command","command":"run \u0060rm\u0060\nline2"}}`,
		)))
		if len(hs) != 1 {
			t.Fatalf("parseHookDoc = %+v, want 1 entry", hs)
		}
		if strings.ContainsAny(hs[0].Name+hs[0].Command, "\n\r\t`") {
			t.Errorf("sanitisation left newline/backtick in fields: %+v", hs[0])
		}
	})
}

// TestFindRepoAgents_ReadableDir verifies a readable agents dir yields the
// agent, and a missing dir yields nil.
func TestFindRepoAgents_ReadableDir(t *testing.T) {
	repo := t.TempDir()
	mustWriteFile(t, filepath.Join(repo, ".kiro", "agents", "deploy.json"), `{"name":"deploy"}`)

	got, _ := findRepoAgents(repo)
	if len(got) != 1 {
		t.Fatalf("findRepoAgents(readable dir) = %+v (len %d), want 1 agent", got, len(got))
	}
	if got[0].Name != "deploy" {
		t.Errorf("findRepoAgents()[0] = %+v, want {Name: deploy}", got[0])
	}
	if got, _ := findRepoAgents(filepath.Join(repo, "does-not-exist")); got != nil {
		t.Errorf("findRepoAgents(missing dir) = %+v, want nil", got)
	}
}

// TestFindRepoHooks_ReadableDir verifies a readable hooks dir yields every
// hook in a multi-hook v1 document (stamped with the filename), and a
// missing dir yields nil.
func TestFindRepoHooks_ReadableDir(t *testing.T) {
	repo := t.TempDir()
	mustWriteFile(t, filepath.Join(repo, ".kiro", "hooks", "guard.json"), hookDoc(
		`{"name":"Guard","trigger":"PreToolUse","action":{"type":"command","command":"echo hi"}}`,
		`{"name":"Regen","trigger":"PostFileSave","action":{"type":"command","command":"make gen"}}`,
	))

	got, more := findRepoHooks(repo)
	if len(got) != 2 || more {
		t.Fatalf("findRepoHooks(readable dir) = %+v (len %d), want 2 hooks from one document", got, len(got))
	}
	if got[0].Trigger != "PreToolUse" || got[0].Filename != "guard.json" || got[0].Name != "Guard" {
		t.Errorf("findRepoHooks()[0] = %+v, want {Filename: guard.json, Name: Guard, Trigger: PreToolUse}", got[0])
	}
	if got[1].Trigger != "PostFileSave" || got[1].Filename != "guard.json" {
		t.Errorf("findRepoHooks()[1] = %+v, want the second hook from the same file", got[1])
	}
	if got, _ := findRepoHooks(filepath.Join(repo, "does-not-exist")); got != nil {
		t.Errorf("findRepoHooks(missing dir) = %+v, want nil", got)
	}
}

// TestFindMdDocsInDir_ReadableDir verifies a readable dir with a .md file
// yields the doc, and a missing dir yields nil.
func TestFindMdDocsInDir_ReadableDir(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "doc.md"), "body\n")

	got, _ := findMdDocsInDir(dir)
	if len(got) != 1 {
		t.Fatalf("findMdDocsInDir(readable dir) = %+v (len %d), want 1 doc", got, len(got))
	}
	if got[0].Filename != "doc.md" || got[0].Inclusion != "always" {
		t.Errorf("findMdDocsInDir()[0] = %+v, want {Filename: doc.md, Inclusion: always}", got[0])
	}
	if got, _ := findMdDocsInDir(filepath.Join(dir, "does-not-exist")); got != nil {
		t.Errorf("findMdDocsInDir(missing dir) = %+v, want nil", got)
	}
}

// TestDiscoveryEntryCaps pins the EXACT caps: findRepoAgents and findRepoHooks 10,
// findMdDocsInDir 20.
func TestDiscoveryEntryCaps(t *testing.T) {
	t.Run("findRepoAgents caps at 10", func(t *testing.T) {
		repo := t.TempDir()
		for i := range 12 {
			mustWriteFile(t,
				filepath.Join(repo, ".kiro", "agents", fmt.Sprintf("agent%02d.json", i)),
				fmt.Sprintf(`{"name":"a%02d"}`, i))
		}
		if got, total := findRepoAgents(repo); len(got) != 10 || total != 12 {
			t.Errorf("findRepoAgents(12 files) returned %d of %d, want exactly 10 (cap) of 12", len(got), total)
		}
	})
	t.Run("findRepoHooks caps at 10 entries", func(t *testing.T) {
		repo := t.TempDir()
		// 6 files x 2 hooks: the cap counts ENTRIES, not files.
		for i := range 6 {
			mustWriteFile(t,
				filepath.Join(repo, ".kiro", "hooks", fmt.Sprintf("hook%02d.json", i)),
				hookDoc(
					`{"name":"a","trigger":"PreToolUse","action":{"type":"command","command":"x"}}`,
					`{"name":"b","trigger":"PostToolUse","action":{"type":"command","command":"y"}}`,
				))
		}
		if got, more := findRepoHooks(repo); len(got) != 10 || !more {
			t.Errorf("findRepoHooks(12 hooks in 6 files) returned %d (more %v), want exactly 10 (cap) and more", len(got), more)
		}
	})
	t.Run("findRepoHooks with exactly 10 entries reports no more", func(t *testing.T) {
		repo := t.TempDir()
		for i := range 5 {
			mustWriteFile(t,
				filepath.Join(repo, ".kiro", "hooks", fmt.Sprintf("hook%02d.json", i)),
				hookDoc(
					`{"name":"a","trigger":"PreToolUse","action":{"type":"command","command":"x"}}`,
					`{"name":"b","trigger":"PostToolUse","action":{"type":"command","command":"y"}}`,
				))
		}
		if got, more := findRepoHooks(repo); len(got) != 10 || more {
			t.Errorf("findRepoHooks(10 hooks in 5 files) returned %d (more %v), want 10 and no more", len(got), more)
		}
	})
	t.Run("findMdDocsInDir caps at 20", func(t *testing.T) {
		dir := t.TempDir()
		for i := range 25 {
			mustWriteFile(t, filepath.Join(dir, fmt.Sprintf("doc%02d.md", i)), "body\n")
		}
		if got, seen := findMdDocsInDir(dir); len(got) != 20 || seen.total != 25 {
			t.Errorf("findMdDocsInDir(25 files) returned %d of %d, want exactly 20 (cap) of 25", len(got), seen.total)
		}
	})
	t.Run("findMdDocsInDir counts past its scan bound", func(t *testing.T) {
		dir := t.TempDir()
		for i := range 105 {
			mustWriteFile(t, filepath.Join(dir, fmt.Sprintf("doc%03d.md", i)), "body\n")
		}
		got, seen := findMdDocsInDir(dir)
		if len(got) != 20 || seen.total != 105 {
			t.Errorf("findMdDocsInDir(105 files) returned %d of %d, want 20 of 105", len(got), seen.total)
		}
		if seen.byRank != [routingRanks]int{rankAlways: 100} {
			t.Errorf("ranks read = %v, want the first 100 always-loaded and none past the scan bound", seen.byRank)
		}
	})
}

// The per-repo cap keeps entries by rank, so a doc the protocol has an agent read unprompted
// survives up to maxScannedEntries on-demand docs sorting before it.
func TestFindMdDocsInDir_KeepsAnAlwaysDocThatSortsAfterTheCap(t *testing.T) {
	dir := t.TempDir()
	for i := range 21 {
		mustWriteFile(t, filepath.Join(dir, fmt.Sprintf("a%02d.md", i)), "---\ninclusion: manual\n---\n")
	}
	mustWriteFile(t, filepath.Join(dir, "y-match.md"), "---\ninclusion: fileMatch\nfileMatchPattern: \"*.go\"\n---\n")
	mustWriteFile(t, filepath.Join(dir, "z-rules.md"), "---\ninclusion: always\n---\n")

	got, seen := findMdDocsInDir(dir)

	if len(got) != 20 || seen.total != 23 {
		t.Fatalf("findMdDocsInDir returned %d of %d, want 20 of 23", len(got), seen.total)
	}
	if want := [routingRanks]int{rankAlways: 1, rankFileMatch: 1, rankOnDemand: 21}; seen.byRank != want {
		t.Errorf("ranks read = %v, want %v, counted before the cut", seen.byRank, want)
	}
	if got[0].Filename != "z-rules.md" || got[1].Filename != "y-match.md" {
		t.Errorf("first two kept = %q, %q, want the always doc then the file-match doc", got[0].Filename, got[1].Filename)
	}
	if got[2].Filename != "a00.md" || got[19].Filename != "a17.md" {
		t.Errorf("manual docs kept = %q..%q, want a00.md..a17.md in filename order", got[2].Filename, got[19].Filename)
	}
}

// TestFindRepoSkills_ScansSubdirsWithSkillMd pins skills as subdirectories pointing at
// SKILL.md, classified by it, with a manifest-less subdir counted, an empty one and a flat .md
// ignored.
func TestFindRepoSkills_ScansSubdirsWithSkillMd(t *testing.T) {
	repo := t.TempDir()
	mustWriteFile(t, filepath.Join(repo, ".kiro", "skills", "alpha", "SKILL.md"),
		"---\ninclusion: manual\ndescription: Alpha skill\n---\n")
	mustWriteFile(t, filepath.Join(repo, ".kiro", "skills", "beta", "SKILL.md"), "# beta\n")
	mustWriteFile(t, filepath.Join(repo, ".kiro", "skills", "gamma", "notes.txt"), "no skill.md here\n")
	mustWriteFile(t, filepath.Join(repo, ".kiro", "skills", "loose.md"), "# not a skill\n")
	if err := os.Mkdir(filepath.Join(repo, ".kiro", "skills", "emptied"), 0o750); err != nil {
		t.Fatal(err)
	}

	skills, seen := findRepoSkills(repo)
	// alpha, beta, gamma are directories -> 3 skills. loose.md is a flat
	// file -> ignored.
	if len(skills) != 3 {
		t.Fatalf("findRepoSkills = %+v (len %d), want 3 skill dirs (flat .md ignored)", skills, len(skills))
	}
	byFile := map[string]docEntry{}
	for _, d := range skills {
		byFile[d.Filename] = d
	}
	alpha, ok := byFile["alpha/SKILL.md"]
	if !ok {
		t.Fatalf("missing alpha/SKILL.md in %+v", skills)
	}
	if alpha.Inclusion != "manual" || alpha.Description != "Alpha skill" {
		t.Errorf("alpha = %+v, want {Inclusion: manual, Description: Alpha skill}", alpha)
	}
	if beta, ok := byFile["beta/SKILL.md"]; !ok || beta.Inclusion != "always" {
		t.Errorf("beta = %+v (ok=%v), want inclusion always", beta, ok)
	}
	if gamma, ok := byFile["gamma/SKILL.md"]; !ok || gamma.Inclusion != "always" {
		t.Errorf("gamma (no SKILL.md) = %+v (ok=%v), want listed with inclusion always", gamma, ok)
	}
	if _, ok := byFile["loose.md"]; ok {
		t.Error("flat skills/loose.md should not be treated as a skill")
	}
	if _, ok := byFile["emptied/SKILL.md"]; ok {
		t.Error("an empty skills/emptied directory was listed as a skill")
	}
	if seen.total != 3 {
		t.Errorf("seen.total = %d, want 3: an empty directory is not counted", seen.total)
	}
}

// TestFindRepoSkills_LinkedSkillDirectory pins that a link to a skill directory inside .kiro is
// a skill, as KAS follows one and the docs page lists it, while a link out of .kiro, to an empty
// directory, or back to skills/ or .kiro itself is not; the presence check agrees.
func TestFindRepoSkills_LinkedSkillDirectory(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	kiro := filepath.Join(repo, ".kiro")
	mustWriteFile(t, filepath.Join(kiro, "shared", "inside", "SKILL.md"), "---\ndescription: Linked in\n---\n")
	mustWriteFile(t, filepath.Join(base, "outside", "SKILL.md"), "---\ndescription: Linked out\n---\n")
	if err := os.MkdirAll(filepath.Join(kiro, "shared", "hollow"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(kiro, "skills"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"inside":  filepath.Join(kiro, "shared", "inside"),
		"outside": filepath.Join(base, "outside"),
		"hollow":  filepath.Join(kiro, "shared", "hollow"),
		"self":    ".",
		"root":    "..",
	} {
		if err := os.Symlink(target, filepath.Join(kiro, "skills", name)); err != nil {
			t.Fatal(err)
		}
	}

	skills, _ := findRepoSkills(repo)
	if len(skills) != 1 || skills[0].Filename != "inside/SKILL.md" || skills[0].Description != "Linked in" {
		t.Errorf("findRepoSkills = %+v, want only the in-tree link with its manifest", skills)
	}
	if !hasKiroContent(repo) {
		t.Error("hasKiroContent = false for a repo whose only skill is an in-tree link")
	}
}

func TestFindRepoSkills_NoSkillsDir(t *testing.T) {
	if got, seen := findRepoSkills(t.TempDir()); got != nil || seen != (dirCount{}) {
		t.Errorf("findRepoSkills(no skills dir) = %+v, want nil", got)
	}
}

// TestFindRepoSkills_CapAt20 feeds 25 skill directories and asserts the
// exact 20-entry cap.
func TestFindRepoSkills_CapAt20(t *testing.T) {
	repo := t.TempDir()
	for i := range 25 {
		mustWriteFile(t,
			filepath.Join(repo, ".kiro", "skills", fmt.Sprintf("skill%02d", i), "SKILL.md"),
			"body\n")
	}
	if got, seen := findRepoSkills(repo); len(got) != 20 || seen.total != 25 {
		t.Errorf("findRepoSkills(25 skill dirs) = %d of %d, want 20 (capped) of 25", len(got), seen.total)
	}
}

func TestFindRepoSkills_KeepsAFileMatchSkillThatSortsAfterTheCap(t *testing.T) {
	repo := t.TempDir()
	for i := range 20 {
		mustWriteFile(t, filepath.Join(repo, ".kiro", "skills", fmt.Sprintf("a%02d", i), "SKILL.md"), "---\ndescription: d\n---\n")
	}
	mustWriteFile(t, filepath.Join(repo, ".kiro", "skills", "z-sql", "SKILL.md"),
		"---\ninclusion: fileMatch\nfileMatchPattern: \"*.sql\"\n---\n")

	got, seen := findRepoSkills(repo)

	if len(got) != 20 || seen.total != 21 {
		t.Fatalf("findRepoSkills returned %d of %d, want 20 of 21", len(got), seen.total)
	}
	if want := [routingRanks]int{rankFileMatch: 1, rankOnDemand: 20}; seen.byRank != want {
		t.Errorf("ranks read = %v, want %v: a skill is never always-loaded", seen.byRank, want)
	}
	if got[0].Filename != "z-sql/SKILL.md" || got[19].Filename != "a18/SKILL.md" {
		t.Errorf("kept %q..%q, want the file-match skill first, then a00..a18", got[0].Filename, got[19].Filename)
	}
}

// TestFindRepoAgents_DedupsPairedFiles verifies a paired reviewer.json +
// reviewer.md collapses to ONE agent, while a .json-only and a .md-only agent
// are each listed once.
func TestFindRepoAgents_DedupsPairedFiles(t *testing.T) {
	repo := t.TempDir()
	agentsDir := filepath.Join(repo, ".kiro", "agents")
	mustWriteFile(t, filepath.Join(agentsDir, "reviewer.json"), `{"name":"reviewer"}`)
	mustWriteFile(t, filepath.Join(agentsDir, "reviewer.md"), "# reviewer\n")
	mustWriteFile(t, filepath.Join(agentsDir, "deploy.json"), `{"name":"deploy"}`)
	mustWriteFile(t, filepath.Join(agentsDir, "notes.md"), "# notes\n")

	agents, _ := findRepoAgents(repo)
	if len(agents) != 3 {
		t.Fatalf("findRepoAgents = %+v (len %d), want 3 (reviewer paired -> 1)", agents, len(agents))
	}
	for _, name := range []string{"reviewer", "deploy", "notes"} {
		if !slices.ContainsFunc(agents, func(a agentEntry) bool { return a.Name == name }) {
			t.Errorf("findRepoAgents = %+v, want an agent named %q", agents, name)
		}
	}
}

// TestWriteRepoSteeringInstructions_NestedAgentsAndHooksAreInert pins that the protocol does not
// claim kiro-cli loads a nested repo's agents or runs its hooks: a model told a hook runs skips
// the check the hook stood for.
func TestWriteRepoSteeringInstructions_NestedAgentsAndHooksAreInert(t *testing.T) {
	var b strings.Builder
	writeRepoSteeringInstructions(&b, "/work")
	out := b.String()
	for _, want := range []string{
		"this session cannot load or switch to them",
		"Hooks do not run for nested repos",
		"run its command yourself",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("protocol omits %q; output:\n%s", want, out)
		}
	}
	for _, stale := range []string{"runs them automatically", "you know what's available"} {
		if strings.Contains(out, stale) {
			t.Errorf("protocol still says %q; output:\n%s", stale, out)
		}
	}
}
