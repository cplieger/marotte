package steering

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cplieger/marotte/internal/git"
)

func writeWorkspace(ctx context.Context, b *strings.Builder, workDir string) {
	entries, err := os.ReadDir(workDir)
	if err != nil || len(entries) == 0 {
		b.WriteString("## Workspace\n\nEmpty.\n\n")
		return
	}
	repos, dirs := classifyEntries(ctx, entries, workDir)
	foundFiles := findNotableFiles(workDir)
	isRoot := git.IsRepo(ctx, workDir)
	b.WriteString("## Workspace\n\n")
	if isRoot {
		b.WriteString("The workspace root (`" + workDir + "`) is itself a git repository.\n\n")
	}
	if len(repos) > 0 {
		b.WriteString("### Git repositories\n\n")
		b.WriteString("Multiple repos coexist under `" + workDir + "`. ")
		b.WriteString("Use the `cwd` parameter in shell commands to target a specific repo ")
		b.WriteString("(e.g. `cwd: \"myrepo\"` runs in `" + workDir + "/myrepo/`). ")
		b.WriteString("File paths like `myrepo/src/main.go` work with the file tools ")
		b.WriteString("(read_file, read_code, grep_search).\n\n")
		hasKiro := writeRepoList(b, workDir, repos)
		b.WriteString("\n")
		if hasKiro {
			// kiro-cli auto-loads steering only for its boot cwd, so per-repo steering needs this nudge.
			writeRepoSteeringInstructions(b, workDir)
		}
	}
	if len(foundFiles) > 0 {
		b.WriteString("### Notable files\n\n")
		for _, f := range foundFiles {
			fmt.Fprintf(b, "- `%s`\n", f)
		}
		b.WriteString("\n")
	}
	if len(dirs) > 0 && !isRoot {
		b.WriteString("### Directories\n\n")
		for _, d := range dirs[:min(len(dirs), maxDirectories)] {
			fmt.Fprintf(b, "- `%s/`\n", defuse(d))
		}
		if extra := len(dirs) - maxDirectories; extra > 0 {
			fmt.Fprintf(b, "- … and %d more (`ls %s`)\n", extra, workDir)
		}
		b.WriteString("\n")
	}
	// Only when repos sit UNDER the workspace root (same guard as Directories above).
	if len(repos) > 0 && !isRoot {
		writeScratchGuidance(b, workDir)
	}
}

// writeScratchGuidance suggests a scratch location outside every repo. A workflow step
// session receives only the always-on steering, so this is the one place the rule reaches
// it. A preference, not a prohibition (a permissions.yaml deny rule is the enforcement).
func writeScratchGuidance(b *strings.Builder, workDir string) {
	b.WriteString("### Scratch files\n\n")
	b.WriteString("Prefer a directory OUTSIDE every repo for files that are not headed for a ")
	b.WriteString("commit: plans, investigation notes, probe output, draft commit messages, ")
	b.WriteString("review verdicts, run state. ")
	fmt.Fprintf(b, "`%s/_scratch/<task>/` is a good default; any path outside a repo working "+
		"tree works.\n\n", workDir)
	b.WriteString("This applies to a path a workflow or skill suggests, not just to one you ")
	b.WriteString("pick. Where the guidance you were given names an in-repo artifact path ")
	b.WriteString("(`<repo>/.agents/tasks/...` is the common one), a `_scratch` path can be ")
	b.WriteString("substituted, including in artifact maps and `fileCheck` stop-condition ")
	b.WriteString("paths. Scratch left in a working tree is work for whoever reads ")
	b.WriteString("`git status` next, and it can end up in a commit.\n\n")
}

// kiroRepo is one repo with `.kiro/` content and what discovery found in it.
type kiroRepo struct {
	name   string
	docs   []docEntry
	skills []docEntry
	agents []agentEntry
	hooks  []HookEntry
}

func scanKiroRepo(workDir, r string) kiroRepo {
	dir := filepath.Join(workDir, r)
	return kiroRepo{
		name: r, docs: findRepoDocs(dir), skills: findRepoSkills(dir),
		agents: findRepoAgents(dir), hooks: findRepoHooks(dir),
	}
}

func (k *kiroRepo) empty() bool {
	return len(k.docs)+len(k.skills)+len(k.agents)+len(k.hooks) == 0
}

// writeRepoList lists the repos: one routing block per repo with `.kiro/` content, the rest
// by name on one line. It reports whether any repo carries `.kiro/` content.
func writeRepoList(b *strings.Builder, workDir string, repos []string) bool {
	var kiro []kiroRepo
	var plain []string
	for _, r := range repos {
		if k := scanKiroRepo(workDir, r); !k.empty() {
			kiro = append(kiro, k)
		} else {
			plain = append(plain, r)
		}
	}
	budget := maxRouting
	for i := range kiro[:min(len(kiro), maxKiroRepos)] {
		writeKiroRepo(b, &kiro[i], &budget)
	}
	if len(kiro) > maxKiroRepos {
		names := make([]string, 0, len(kiro)-maxKiroRepos)
		for _, k := range kiro[maxKiroRepos:] {
			names = append(names, k.name)
		}
		b.WriteString("- More repositories with .kiro content: ")
		writeNameList(b, names, "`ls -d "+workDir+"/*/.kiro`")
		b.WriteString(". Before working in one, list its `.kiro/steering/` and `.kiro/skills/`\n")
	}
	if len(plain) > 0 {
		b.WriteString("- Other repositories: ")
		writeNameList(b, plain, "`ls "+workDir+"`")
		b.WriteString("\n")
	}
	return len(kiro) > 0
}

// writeNameList writes up to maxPlainRepos names as `name/` spans, then an overflow naming how.
func writeNameList(b *strings.Builder, names []string, how string) {
	spans := make([]string, 0, min(len(names), maxPlainRepos))
	for _, n := range names[:min(len(names), maxPlainRepos)] {
		spans = append(spans, "`"+defuse(n)+"/`")
	}
	b.WriteString(strings.Join(spans, ", "))
	if extra := len(names) - maxPlainRepos; extra > 0 {
		fmt.Fprintf(b, ", … and %d more (%s)", extra, how)
	}
}

// writeKiroRepo spends the workspace-wide routing budget on the repo's steering, then its skills.
func writeKiroRepo(b *strings.Builder, k *kiroRepo, budget *int) {
	// One defusal per repo, threaded into every writer below. k.name stays raw as a path component.
	label := defuse(k.name)
	fmt.Fprintf(b, "- `%s/`\n", label)
	docs := k.docs[:min(len(k.docs), *budget)]
	*budget -= len(docs)
	skills := k.skills[:min(len(k.skills), *budget)]
	*budget -= len(skills)
	if len(docs) > 0 {
		writeRepoSteering(b, label, docs)
	}
	if len(skills) > 0 {
		writeRepoSkills(b, label, skills)
	}
	if cut := len(k.docs) + len(k.skills) - len(docs) - len(skills); cut > 0 {
		fmt.Fprintf(b, "  - … %d more .kiro entries: list `%s/.kiro/steering/` and `%s/.kiro/skills/`\n", cut, label, label)
	}
	if len(k.agents) > 0 {
		writeRepoAgents(b, label, k.agents)
	}
	if len(k.hooks) > 0 {
		writeRepoHooks(b, label, k.hooks)
	}
}

func writeRepoAgents(b *strings.Builder, repo string, agents []agentEntry) {
	fmt.Fprintf(b, "  - **Custom agents** (`%s/.kiro/agents/`):", repo)
	for _, a := range agents {
		fmt.Fprintf(b, " `%s`", a.Name)
	}
	b.WriteString("\n")
}

func writeRepoHooks(b *strings.Builder, repo string, hooks []HookEntry) {
	fmt.Fprintf(b, "  - **Hooks** (`%s/.kiro/hooks/`):\n", repo)
	for _, h := range hooks {
		trigger := cmp.Or(h.Trigger, "unknown")
		fmt.Fprintf(b, "    - `%s`", h.Filename)
		if h.Name != "" {
			fmt.Fprintf(b, " %s", h.Name)
		}
		fmt.Fprintf(b, " [%s]", trigger)
		if h.Command != "" {
			fmt.Fprintf(b, " → `%s`", h.Command)
		}
		b.WriteString("\n")
	}
}

// Dot-named repos (".kiro", ".github") are listed; dot-named non-repos (.cache, .venv) stay hidden.
func classifyEntries(ctx context.Context, entries []os.DirEntry, workDir string) (repos, dirs []string) {
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || name == ".git" {
			continue
		}
		if git.IsRepo(ctx, filepath.Join(workDir, name)) {
			repos = append(repos, name)
		} else if !strings.HasPrefix(name, ".") {
			dirs = append(dirs, name)
		}
	}
	return repos, dirs
}

func findNotableFiles(workDir string) []string {
	notable := []string{
		"README.md", "readme.md", "package.json", "go.mod",
		"Cargo.toml", "pyproject.toml", "requirements.txt",
		"Makefile", "docker-compose.yml", "docker-compose.yaml",
		"compose.yaml", "compose.yml", "Dockerfile",
		".env", "tsconfig.json", "pom.xml", "build.gradle",
	}
	var found []string
	for _, f := range notable {
		if _, err := os.Stat(filepath.Join(workDir, f)); err == nil {
			found = append(found, f)
		}
	}
	return found
}

// truncateUTF8 returns s truncated to at most n bytes without splitting a multi-byte rune.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Back up past continuation bytes (10xxxxxx) to a leading byte.
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
