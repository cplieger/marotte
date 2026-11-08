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
		writeDirectories(b, dirs, workDir)
	}
	// Only when repos sit UNDER the workspace root (same guard as Directories above).
	if len(repos) > 0 && !isRoot {
		writeScratchGuidance(b, workDir)
	}
}

func writeDirectories(b *strings.Builder, dirs []string, workDir string) {
	b.WriteString("### Directories\n\n")
	for _, d := range dirs[:min(len(dirs), maxDirectories)] {
		fmt.Fprintf(b, "- `%s/`\n", defuse(d))
	}
	if extra := len(dirs) - maxDirectories; extra > 0 {
		fmt.Fprintf(b, "- … and %d more (`ls %s`)\n", extra, workDir)
	}
	b.WriteString("\n")
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

// kiroRepo is one repo with `.kiro/` content, what discovery read of it, and how much it holds.
type kiroRepo struct {
	name        string
	docs        []docEntry
	skills      []docEntry
	agents      []agentEntry
	hooks       []HookEntry
	docsTotal   int
	skillsTotal int
	agentsTotal int
	moreHooks   bool
}

func scanKiroRepo(workDir, r string) kiroRepo {
	dir := filepath.Join(workDir, r)
	k := kiroRepo{name: r}
	k.docs, k.docsTotal = findRepoDocs(dir)
	k.skills, k.skillsTotal = findRepoSkills(dir)
	k.agents, k.agentsTotal = findRepoAgents(dir)
	k.hooks, k.moreHooks = findRepoHooks(dir)
	return k
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
	shown := kiro[:min(len(kiro), maxKiroRepos)]
	for i, kept := range allotRouting(shown, maxRouting) {
		writeKiroRepo(b, &shown[i], kept)
	}
	if len(kiro) > maxKiroRepos {
		names := make([]string, 0, len(kiro)-maxKiroRepos)
		for i := range kiro[maxKiroRepos:] {
			names = append(names, kiro[maxKiroRepos+i].name)
		}
		b.WriteString("- More repositories with .kiro content: ")
		writeNameList(b, names, "`ls -ad "+workDir+"/*/.kiro "+workDir+"/.[!.]*/.kiro`")
		b.WriteString(". Before working in one, list its `.kiro/steering/`, `.kiro/skills/`, `.kiro/hooks/` and " +
			"`.kiro/agents/`, and apply the protocol below to them.\n")
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

// routed is the share of one repo's steering and skills the routing budget renders.
type routed struct{ docs, skills []docEntry }

// Routing ranks: the protocol has an agent read always-loaded steering unprompted, a path
// triggers a file-match entry, and the rest wait for a task to name them.
const (
	rankAlways = iota
	rankFileMatch
	rankOnDemand
	routingRanks
)

func docRank(d docEntry) int {
	switch d.Inclusion {
	case inclusionFileMatch:
		return rankFileMatch
	case inclusionManual, inclusionAuto:
		return rankOnDemand
	default:
		return rankAlways
	}
}

func skillRank(d docEntry) int {
	if d.Inclusion == inclusionFileMatch {
		return rankFileMatch
	}
	return rankOnDemand
}

// allotRouting spends budget across repos by rank, then repo order, then filename, so a later
// repo's always-loaded steering outranks an earlier repo's on-demand entries.
func allotRouting(repos []kiroRepo, budget int) []routed {
	out := make([]routed, len(repos))
	for rank := range routingRanks {
		for i := range repos {
			out[i].docs = takeRank(out[i].docs, repos[i].docs, docRank, rank, &budget)
			out[i].skills = takeRank(out[i].skills, repos[i].skills, skillRank, rank, &budget)
		}
	}
	return out
}

func takeRank(kept, entries []docEntry, rankOf func(docEntry) int, rank int, budget *int) []docEntry {
	for _, d := range entries {
		if *budget > 0 && rankOf(d) == rank {
			kept = append(kept, d)
			*budget--
		}
	}
	return kept
}

func writeKiroRepo(b *strings.Builder, k *kiroRepo, kept routed) {
	// One defusal per repo, threaded into every writer below. k.name stays raw as a path component.
	label := defuse(k.name)
	fmt.Fprintf(b, "- `%s/`\n", label)
	if len(kept.docs) > 0 {
		writeRepoSteering(b, label, kept.docs)
	}
	if len(kept.skills) > 0 {
		writeRepoSkills(b, label, kept.skills)
	}
	if cut := k.docsTotal + k.skillsTotal - len(kept.docs) - len(kept.skills); cut > 0 {
		fmt.Fprintf(b, "  - … %d more .kiro entries: list `%s/.kiro/steering/` and `%s/.kiro/skills/`\n", cut, label, label)
	}
	if len(k.agents) > 0 {
		writeRepoAgents(b, label, k)
	}
	if len(k.hooks) > 0 {
		writeRepoHooks(b, label, k)
	}
}

func writeRepoAgents(b *strings.Builder, repo string, k *kiroRepo) {
	fmt.Fprintf(b, "  - **Custom agents** (`%s/.kiro/agents/`):", repo)
	for _, a := range k.agents {
		fmt.Fprintf(b, " `%s`", a.Name)
	}
	if extra := k.agentsTotal - len(k.agents); extra > 0 {
		fmt.Fprintf(b, " … and %d more", extra)
	}
	b.WriteString("\n")
}

func writeRepoHooks(b *strings.Builder, repo string, k *kiroRepo) {
	fmt.Fprintf(b, "  - **Hooks** (`%s/.kiro/hooks/`):\n", repo)
	for _, h := range k.hooks {
		// Name and trigger are bounded here, not in parseHookDoc: the REST scanner shows them whole.
		trigger := cmp.Or(capPreview(h.Trigger), "unknown")
		fmt.Fprintf(b, "    - `%s`", h.Filename)
		if h.Name != "" {
			fmt.Fprintf(b, " %s", capPreview(h.Name))
		}
		fmt.Fprintf(b, " [%s]", trigger)
		if h.Command != "" {
			fmt.Fprintf(b, " → `%s`", h.Command)
		}
		b.WriteString("\n")
	}
	if k.moreHooks {
		fmt.Fprintf(b, "    - … more hooks: list `%s/.kiro/hooks/`\n", repo)
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
