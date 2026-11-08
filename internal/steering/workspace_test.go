package steering

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteWorkspace_Empty(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	writeWorkspace(t.Context(), &b, dir)
	if !strings.Contains(b.String(), "Empty.") {
		t.Error("expected 'Empty.' for empty workspace")
	}
}

func TestWriteWorkspace_WithFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test"), 0o644)
	os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch"), 0o644)

	var b strings.Builder
	writeWorkspace(t.Context(), &b, dir)
	out := b.String()
	if !strings.Contains(out, "go.mod") {
		t.Error("missing go.mod in notable files")
	}
	if !strings.Contains(out, "Dockerfile") {
		t.Error("missing Dockerfile in notable files")
	}
}

func TestWriteWorkspace_WithGitRepo(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "myrepo")
	os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755)

	var b strings.Builder
	writeWorkspace(t.Context(), &b, dir)
	out := b.String()
	if !strings.Contains(out, "myrepo") {
		t.Error("missing git repo")
	}
	if !strings.Contains(out, "Git repositories") {
		t.Error("missing Git repositories header")
	}
}

func TestWriteWorkspace_WithDirs(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	os.MkdirAll(filepath.Join(dir, "docs"), 0o755)

	var b strings.Builder
	writeWorkspace(t.Context(), &b, dir)
	out := b.String()
	if !strings.Contains(out, "src") {
		t.Error("missing src directory")
	}
}

// TestWriteWorkspace_OmitsEmptySectionHeaders verifies the per-section
// headers (Git repositories, Notable files, Directories) are emitted only
// when their slice is non-empty.
func TestWriteWorkspace_OmitsEmptySectionHeaders(t *testing.T) {
	t.Run("no git repos / no notable files", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "plaindir"), 0o755); err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		writeWorkspace(t.Context(), &b, dir)
		out := b.String()
		if strings.Contains(out, "### Git repositories") {
			t.Errorf("Git repositories header emitted with zero repos:\n%s", out)
		}
		if strings.Contains(out, "### Notable files") {
			t.Errorf("Notable files header emitted with zero notable files:\n%s", out)
		}
	})
	t.Run("no directories, not a root repo", func(t *testing.T) {
		dir := t.TempDir()
		// Only a README (a file, not a dir) -> dirs empty, isRoot false.
		mustWriteFile(t, filepath.Join(dir, "README.md"), "A workspace readme line\n")
		var b strings.Builder
		writeWorkspace(t.Context(), &b, dir)
		out := b.String()
		if strings.Contains(out, "### Directories") {
			t.Errorf("Directories header emitted with zero dirs:\n%s", out)
		}
		if !strings.Contains(out, "### Notable files") {
			t.Errorf("expected Notable files section for README workspace:\n%s", out)
		}
	})
}

func TestWriteWorkspace_RendersGroupedSteering(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "myrepo")
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	steering := filepath.Join(repoDir, ".kiro", "steering")
	if err := os.MkdirAll(steering, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(steering, "always.md"),
		[]byte("---\ninclusion: always\ndescription: Always-on doc\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(steering, "go-files.md"),
		[]byte("---\ninclusion: fileMatch\nfileMatchPattern: \"**/*.go\"\ndescription: Go conventions\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(steering, "incident.md"),
		[]byte("---\ninclusion: manual\ndescription: On-call runbook\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	writeWorkspace(t.Context(), &b, dir)
	out := b.String()
	checks := []string{
		"Always-loaded steering",
		"File-match steering",
		"Manual steering",
		"`myrepo/.kiro/steering/always.md` — Always-on doc",
		"`myrepo/.kiro/steering/go-files.md` (matches `**/*.go`) — Go conventions",
		"`myrepo/.kiro/steering/incident.md` — On-call runbook",
		"Per-repo .kiro protocol",
		"is not loaded",
		"routing table",
	}
	for _, want := range checks {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n--- output ---\n%s", want, out)
		}
	}
}

// TestWriteWorkspace_ScratchGuidance covers the three states of the scratch
// suggestion: present with repos under a non-repo root, and absent in the two
// cases where no sibling path outside a repo can be suggested.
func TestWriteWorkspace_ScratchGuidance(t *testing.T) {
	t.Run("emitted with a repo under a non-repo root", func(t *testing.T) {
		dir := t.TempDir()
		mustWriteFile(t, filepath.Join(dir, "myrepo", ".git", "HEAD"), "ref: refs/heads/main\n")
		var b strings.Builder
		writeWorkspace(t.Context(), &b, dir)
		out := b.String()
		for _, want := range []string{
			"### Scratch files",
			"OUTSIDE every repo",
			// Built from workDir, not a hardcoded /workspace.
			"`" + dir + "/_scratch/<task>/`",
			// The redirect half: a suggested in-repo path may be substituted.
			"`<repo>/.agents/tasks/...`",
			"`fileCheck` stop-condition",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("output missing %q\n--- output ---\n%s", want, out)
			}
		}
	})
	t.Run("omitted with zero repos", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "plaindir"), 0o755); err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		writeWorkspace(t.Context(), &b, dir)
		if out := b.String(); strings.Contains(out, "### Scratch files") {
			t.Errorf("scratch guidance emitted with zero repos:\n%s", out)
		}
	})
	t.Run("omitted when the workspace root is itself a repo", func(t *testing.T) {
		dir := t.TempDir()
		// The root is a repo, so there is no sibling _scratch to suggest.
		mustWriteFile(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/main\n")
		mustWriteFile(t, filepath.Join(dir, "nested", ".git", "HEAD"), "ref: refs/heads/main\n")
		var b strings.Builder
		writeWorkspace(t.Context(), &b, dir)
		if out := b.String(); strings.Contains(out, "### Scratch files") {
			t.Errorf("scratch guidance emitted with the root itself a repo:\n%s", out)
		}
	})
}

func TestWriteWorkspace_OmitsProtocolWhenNoSteering(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "myrepo")
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Repo exists but has no .kiro/steering/.
	var b strings.Builder
	writeWorkspace(t.Context(), &b, dir)
	out := b.String()
	if strings.Contains(out, "Per-repo .kiro protocol") {
		t.Errorf("protocol section emitted with no per-repo steering\n--- output ---\n%s", out)
	}
}

// TestWriteKiroRepo_RendersEveryGroup verifies a repo's line carries only its name, followed by
// its skills, agents and hooks groups.
func TestWriteKiroRepo_RendersEveryGroup(t *testing.T) {
	work := t.TempDir()
	repo := filepath.Join(work, "myrepo")
	mustWriteFile(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/feature-x\n")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "A widget toolkit\n")
	// Skills are directories containing SKILL.md, not flat .md files.
	mustWriteFile(t, filepath.Join(repo, ".kiro", "skills", "build", "SKILL.md"), "body\n")
	mustWriteFile(t, filepath.Join(repo, ".kiro", "agents", "deploy.json"), `{"name":"deploy"}`)
	mustWriteFile(t, filepath.Join(repo, ".kiro", "hooks", "guard.json"),
		`{"version":"v1","hooks":[{"name":"Guard","trigger":"PreToolUse","action":{"type":"command","command":"echo hi"}}]}`)

	var b strings.Builder
	k := scanKiroRepo(work, "myrepo")
	budget := maxRouting
	writeKiroRepo(&b, &k, &budget)
	out := b.String()

	if !strings.HasPrefix(out, "- `myrepo/`\n") {
		t.Errorf("the repo line carries more than its name:\n%s", out)
	}
	for _, want := range []string{"**Skills**", "build/SKILL.md", "**Custom agents**", "`deploy`", "**Hooks**", "guard.json", "Guard [PreToolUse]", "echo hi"} {
		if !strings.Contains(out, want) {
			t.Errorf("writeKiroRepo omitted %q\n--- output ---\n%s", want, out)
		}
	}
	if strings.Contains(out, "[unknown]") {
		t.Errorf("hook with a known trigger rendered [unknown]:\n%s", out)
	}
	if budget != maxRouting-1 {
		t.Errorf("one skill spent %d of the routing budget, want 1", maxRouting-budget)
	}
}

// TestWriteKiroRepo_OmitsAgentAndHookHeadersWhenEmpty verifies a repo with only steering emits
// neither the Custom agents nor the Hooks header.
func TestWriteKiroRepo_OmitsAgentAndHookHeadersWhenEmpty(t *testing.T) {
	work := t.TempDir()
	mustWriteFile(t, filepath.Join(work, "r", ".kiro", "steering", "s.md"), "# S\n")
	var b strings.Builder
	k := scanKiroRepo(work, "r")
	budget := maxRouting
	writeKiroRepo(&b, &k, &budget)
	out := b.String()
	if strings.Contains(out, "**Custom agents**") || strings.Contains(out, "**Hooks**") {
		t.Errorf("an agent or hook header emitted with none:\n%s", out)
	}
}

// A repo partly over the routing budget keeps the entries that fit and counts the rest.
func TestWriteKiroRepo_CountsTheEntriesPastTheBudget(t *testing.T) {
	work := t.TempDir()
	for _, n := range []string{"a", "b", "c"} {
		mustWriteFile(t, filepath.Join(work, "r", ".kiro", "steering", n+".md"), "# S\n")
	}
	var b strings.Builder
	k := scanKiroRepo(work, "r")
	budget := 2
	writeKiroRepo(&b, &k, &budget)
	out := b.String()
	if got := len(routingEntry.FindAllString(out, -1)); got != 2 {
		t.Errorf("rendered %d entries with a budget of 2:\n%s", got, out)
	}
	if !strings.Contains(out, "  - … 1 more .kiro entries: list `r/.kiro/steering/` and `r/.kiro/skills/`\n") {
		t.Errorf("the entry past the budget is not counted:\n%s", out)
	}
	if budget != 0 {
		t.Errorf("budget left = %d, want 0", budget)
	}
}

func TestTruncateUTF8_LenEqualsN(t *testing.T) {
	if got := truncateUTF8("abcd", 4); got != "abcd" {
		t.Errorf("truncateUTF8(%q, 4) = %q, want %q", "abcd", got, "abcd")
	}
}

// TestTruncateUTF8_AllContinuationBytes verifies an all-continuation-byte
// input drives the rune-walk down to n==0 and returns "" without panicking.
func TestTruncateUTF8_AllContinuationBytes(t *testing.T) {
	if got := truncateUTF8("\x80\x80\x80", 2); got != "" {
		t.Errorf("truncateUTF8(0x80 0x80 0x80, 2) = %q, want \"\"", got)
	}
}

func TestFindNotableFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x"), 0o644)
	os.WriteFile(filepath.Join(dir, "random.txt"), []byte("x"), 0o644)

	found := findNotableFiles(dir)
	hasGoMod := false
	hasRandom := false
	for _, f := range found {
		if f == "go.mod" {
			hasGoMod = true
		}
		if f == "random.txt" {
			hasRandom = true
		}
	}
	if !hasGoMod {
		t.Error("go.mod should be notable")
	}
	if hasRandom {
		t.Error("random.txt should not be notable")
	}
}

func TestClassifyEntries(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "repo1", ".git"), 0o755)
	os.MkdirAll(filepath.Join(dir, "plain"), 0o755)
	os.MkdirAll(filepath.Join(dir, ".hidden"), 0o755)
	// Dot-named git repos (.kiro, .github) are listed; ".git" and dot-named non-repos are not.
	os.MkdirAll(filepath.Join(dir, ".kiro", ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0o644)

	entries, _ := os.ReadDir(dir)
	repos, dirs := classifyEntries(t.Context(), entries, dir)

	wantRepos := []string{".kiro", "repo1"}
	if len(repos) != 2 || repos[0] != wantRepos[0] || repos[1] != wantRepos[1] {
		t.Errorf("repos = %v, want %v", repos, wantRepos)
	}
	if len(dirs) != 1 || dirs[0] != "plain" {
		t.Errorf("dirs = %v, want [plain] (dot-named non-repos stay hidden)", dirs)
	}
}

func isValidUTF8(s string) bool {
	return strings.ToValidUTF8(s, "\uFFFD") == s
}

// KIRO_WORK_DIR and KIRO_CONFIG_DIR move the folders, so both docs name the configured ones.
func TestRender_NamesTheConfiguredFoldersNotTheDefaults(t *testing.T) {
	workDir, configDir := t.TempDir(), t.TempDir()
	repo := seedRepo(t, workDir, "repo")
	mustWriteFile(t, filepath.Join(repo, ".kiro", "steering", "s.md"), "# S\n")
	mustWriteFile(t, filepath.Join(configDir, "config.json"), `{"memory_mode":"read_only"}`)
	g := New(workDir, configDir, filepath.Join(configDir, "tools"))
	env := string(g.render(t.Context(), MCPSnapshot{}, false, ForgeSnapshot{}, false, ToolsSnapshot{}, false))
	chat := g.renderChat(ForgeSnapshot{})

	for _, want := range []string{
		"Multiple repos coexist under `" + workDir + "`. ",
		"runs in `" + workDir + "/myrepo/`",
		"kiro-cli loads `.kiro/` only from `" + workDir + "`, so",
		"every chat starts in `" + workDir + "`. ",
		"No repository under `" + workDir + "` has an `origin`",
		"`" + workDir + "/_scratch/<task>/` is a good default",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("environment.md is missing %q:\n%s", want, env)
		}
	}
	for _, want := range []string{
		"mounts `" + workDir + "`, `" + configDir + "` and the uploads folder",
		"(never `" + configDir + "/`)",
	} {
		if !strings.Contains(chat, want) {
			t.Errorf("the chat doc is missing %q:\n%s", want, chat)
		}
	}
	for name, out := range map[string]string{"environment.md": env, "chat doc": chat} {
		if strings.Contains(out, "`/workspace") || strings.Contains(out, "`/config") {
			t.Errorf("%s names a default folder instead of the configured one:\n%s", name, out)
		}
	}
}
