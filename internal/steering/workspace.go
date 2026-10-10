package steering

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cplieger/marotte/internal/git"
	"github.com/cplieger/marotte/internal/sanitize"
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
		b.WriteString("The workspace root (`/workspace`) is itself a git repository.\n\n")
	}
	if len(repos) > 0 {
		b.WriteString("### Git repositories\n\n")
		b.WriteString("Multiple repos coexist under `/workspace`. ")
		b.WriteString("Use the `cwd` parameter in shell commands to target a specific repo ")
		b.WriteString("(e.g. `cwd: \"myrepo\"` runs in `/workspace/myrepo/`). ")
		b.WriteString("File paths like `myrepo/src/main.go` work with the file tools ")
		b.WriteString("(read_file, read_code, grep_search).\n\n")
		for _, r := range repos {
			writeRepoEntry(b, workDir, r)
		}
		b.WriteString("\n")
		// kiro-cli auto-loads steering only for its boot cwd, so per-repo steering needs this nudge.
		writeRepoSteeringInstructions(b, repos, workDir)
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
		for _, d := range dirs {
			fmt.Fprintf(b, "- `%s/`\n", defuse(d))
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

func writeRepoEntry(b *strings.Builder, workDir, r string) {
	repoDir := filepath.Join(workDir, r)
	// One defusal per repo, threaded into every writer below (per-`%s` defusal already lost a
	// channel). `r` stays raw because it is also a path component.
	label := defuse(r)
	origin := readGitOrigin(repoDir)
	branch := readGitBranch(repoDir)
	desc := readFirstLine(filepath.Join(repoDir, "README.md"))
	fmt.Fprintf(b, "- `%s/`", label)
	if branch != "" {
		fmt.Fprintf(b, " on `%s`", branch)
	}
	if host := hostFromGitURL(origin); host != "" {
		fmt.Fprintf(b, " (%s)", host)
	}
	if desc != "" {
		fmt.Fprintf(b, " — %s", desc)
	}
	b.WriteString("\n")
	docs := findRepoDocs(repoDir)
	if len(docs) > 0 {
		writeRepoSteering(b, label, docs)
	}
	skills := findRepoSkills(repoDir)
	if len(skills) > 0 {
		writeRepoSkills(b, label, skills)
	}
	agents := findRepoAgents(repoDir)
	if len(agents) > 0 {
		writeRepoAgents(b, label, agents)
	}
	hooks := findRepoHooks(repoDir)
	if len(hooks) > 0 {
		writeRepoHooks(b, label, hooks)
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

// readGitOrigin returns a repo's origin URL by reading `.git/config` directly: generation is
// synchronous and must not block on a wedged subprocess.
func readGitOrigin(repoDir string) string {
	data, err := readCappedFile(filepath.Join(repoDir, ".git", "config"), 64*1024)
	if err != nil {
		return ""
	}
	inOrigin := false
	for line := range strings.SplitSeq(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inOrigin = trimmed == `[remote "origin"]`
			continue
		}
		if !inOrigin {
			continue
		}
		if k, v, ok := strings.Cut(trimmed, "="); ok {
			if strings.TrimSpace(k) == "url" {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

// "" when detached. Cut at the FIRST line and defused: the file is workspace content.
func readGitBranch(repoDir string) string {
	data, err := readCappedFile(filepath.Join(repoDir, ".git", "HEAD"), 1024)
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(string(data), "\n")
	s := strings.TrimSpace(first)
	const refsPrefix = "ref: refs/heads/"
	if branch, ok := strings.CutPrefix(s, refsPrefix); ok {
		return defuse(branch)
	}
	return ""
}

// Handles both https:// and scp-style git@host:path forms. Returns "" for shapes we don't recognise
// (file://, ext::, etc) and for anything that is not SHAPED like a host (see isHostShaped).
func hostFromGitURL(url string) string {
	url = strings.TrimSpace(url)
	var host string
	if strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") {
		host = hostFromHTTPURL(url)
	} else {
		host = hostFromSCPURL(url)
	}
	if !isHostShaped(host) {
		return ""
	}
	return host
}

// isHostShaped reports whether s could be a DNS host or IPv4 literal with an optional port
// (ASCII letters, digits, dot, dash, underscore, colon), refusing markup and homoglyph
// hosts. Cost: an IPv6-literal remote loses its annotation.
func isHostShaped(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '-', c == '_', c == ':':
		default:
			return false
		}
	}
	return true
}

// hostFromHTTPURL extracts the host from an http(s):// git URL, stripping
// any user[:pass]@ credentials that appear before the first path slash.
// Returns "" when the resulting host still carries an "@" or "/".
func hostFromHTTPURL(url string) string {
	_, rest, _ := strings.Cut(url, "://")
	// Strip credentials only when the @ precedes the first /.
	slash := strings.Index(rest, "/")
	if at := strings.Index(rest, "@"); at >= 0 && (slash < 0 || at < slash) {
		rest = rest[at+1:]
	}
	if i := strings.Index(rest, "/"); i > 0 {
		host := rest[:i]
		if strings.ContainsAny(host, "@/") {
			return ""
		}
		return host
	}
	if strings.ContainsAny(rest, "@/") {
		return ""
	}
	return rest
}

// hostFromSCPURL extracts the host from an scp-style git@host:path URL.
// Returns "" for any other shape (no "@", a leading "@", or no ":"
// separator), and for a host that still contains an "@" or "/".
func hostFromSCPURL(url string) string {
	at := strings.Index(url, "@")
	if at <= 0 {
		return ""
	}
	rest := url[at+1:]
	colon := strings.Index(rest, ":")
	if colon <= 0 {
		return ""
	}
	host := rest[:colon]
	if strings.ContainsAny(host, "@/") {
		return ""
	}
	return host
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

const firstLineWindow = 10

// readFirstLine returns the first non-heading paragraph of the README at path,
// wrapped lines joined and a leading blockquote marker stripped, capped and
// sanitised so hostile repo content cannot inject agent instructions into
// environment.md. One line carrying link syntax, an HTML tag, a backtick or a
// bare URL drops its WHOLE paragraph: the continuation of a wrapped sentence is
// a fragment, not a description. Cost: an opening sentence that quotes a
// `symbol` yields no description.
func readFirstLine(path string) string {
	data, err := readCappedFile(path, firstLineReadCap)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > firstLineWindow {
		lines = lines[:firstLineWindow]
	}
	var para []string
	clean := true
	flush := func() string {
		if !clean || len(para) == 0 {
			return ""
		}
		return capDescription(strings.TrimSpace(strings.Join(para, " ")))
	}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || isMarkdownHeading(line) {
			if desc := flush(); desc != "" {
				return desc
			}
			para, clean = nil, true
			continue
		}
		line = descriptionLine(line)
		if carriesMarkup(line) {
			clean = false
		}
		para = append(para, line)
	}
	return flush()
}

// descriptionLine normalises one README paragraph line: the blockquote marker
// stripped, line breaks and tabs folded to spaces, hidden runes removed.
func descriptionLine(line string) string {
	line = strings.TrimSpace(strings.TrimPrefix(line, ">"))
	line = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return ' '
		}
		return r
	}, line)
	return sanitize.Unicode(line)
}

// carriesMarkup reports whether a line holds link syntax, an HTML tag, a backtick
// or a bare URL, any of which disqualifies its whole paragraph as a description.
func carriesMarkup(line string) bool {
	return strings.ContainsAny(line, "[]<`") ||
		strings.Contains(line, "http://") ||
		strings.Contains(line, "https://")
}

func capDescription(s string) string {
	if len(s) > 100 {
		return truncateUTF8(s, 100) + "..."
	}
	return s
}

// isMarkdownHeading reports whether line is a true CommonMark ATX
// heading: one to six `#` followed by whitespace or end-of-line.
// `#hashtag` content (no space after `#`) is NOT a heading and must
// fall through so legitimate README first lines are kept.
func isMarkdownHeading(line string) bool {
	if line == "" || line[0] != '#' {
		return false
	}
	i := 0
	for i < len(line) && line[i] == '#' {
		i++
	}
	if i > 6 {
		return false
	}
	return i == len(line) || line[i] == ' ' || line[i] == '\t'
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
