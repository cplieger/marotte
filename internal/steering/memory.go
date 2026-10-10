package steering

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/cplieger/marotte/internal/settings"
)

// Every chat runs from /workspace, so KAS injects only Global-scope memories; this section
// gives the agent each clone's tag and when to ask.

// writeMemory writes the Memory section, or nothing when the Memory setting is
// Off (the agent has no memory tool then).
func writeMemory(ctx context.Context, b *strings.Builder, workDir, configDir string) {
	mode := settings.DefaultMemoryMode
	var v string
	if settings.FieldInto(ctx, configDir, settings.KeyMemoryMode, &v) {
		mode = v
	}
	if settings.MemoryPreferenceFor(mode).Mode == "disabled" {
		return
	}
	b.WriteString("## Memory\n\n")
	b.WriteString("Your memory index at session start shows only the Global scope, because every chat starts in `/workspace`. ")
	b.WriteString("Each repository below has its own scope tag. ")
	b.WriteString("When you start working in one of them, run `memory list` with `filter` set to its tag to read what is stored for it. ")
	b.WriteString("To save a fact about a repository, run `memory add` with a `path` inside that repository, so it is filed under the repository's tag rather than Global.\n\n")
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return
	}
	repos, _ := classifyEntries(ctx, entries, workDir)
	tagged := 0
	for _, r := range repos {
		tag := memoryRepoTag(filepath.Join(workDir, r))
		if tag == "" {
			continue
		}
		fmt.Fprintf(b, "- `%s/`: `%s`\n", defuse(r), defuse(tag))
		tagged++
	}
	if tagged == 0 {
		b.WriteString("No repository under `/workspace` has an `origin` remote KAS can tag, so repository facts will be filed under Global.\n")
	}
	b.WriteString("\n")
}

// memoryRepoTag derives the `repo:<provider>/<owner>/<name>` scope tag KAS
// gives a memory saved with a path inside repoDir, porting its RepoResolver:
// the origin url from the repo's git config (following a linked worktree's
// `.git` file to its common dir), parsed by host. "" when there is none.
func memoryRepoTag(repoDir string) string {
	gitDir, ok := gitCommonDir(repoDir)
	if !ok {
		return ""
	}
	data, err := readCappedFile(filepath.Join(gitDir, "config"), 64*1024)
	if err != nil {
		return ""
	}
	provider, owner, name, ok := parseRepoRemote(originURL(string(data)))
	if !ok {
		return ""
	}
	tag := "repo:" + provider + "/" + owner + "/" + name
	// The origin url is workspace content: a tag that is not plain path-shaped
	// (markup, an `@` in the path) is refused rather than rendered.
	if !safeRepoTag.MatchString(tag) {
		return ""
	}
	return tag
}

var safeRepoTag = regexp.MustCompile(`^repo:[A-Za-z0-9][A-Za-z0-9._~-]*(?:/[A-Za-z0-9._~-]+)+$`)

// gitCommonDir returns the directory holding repoDir's git config: `.git`
// itself, or for a `.git` file the gitdir it names, then that gitdir's
// commondir when one is present.
func gitCommonDir(repoDir string) (string, bool) {
	dotGit := filepath.Join(repoDir, ".git")
	info, err := os.Lstat(dotGit)
	if err != nil {
		return "", false
	}
	if info.IsDir() {
		return dotGit, true
	}
	data, err := readCappedFile(dotGit, 4096)
	if err != nil {
		return "", false
	}
	m := gitdirRe.FindStringSubmatch(string(data))
	if m == nil {
		return "", false
	}
	dir := resolveFrom(repoDir, strings.TrimSpace(m[1]))
	common, err := readCappedFile(filepath.Join(dir, "commondir"), 4096)
	if err != nil {
		return dir, true
	}
	return resolveFrom(dir, strings.TrimSpace(string(common))), true
}

var gitdirRe = regexp.MustCompile(`(?m)^gitdir:\s*(.+)$`)

func resolveFrom(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(base, p)
}

// originURL reads the `[remote "origin"]` url the way KAS does: the section
// ends at the next header, and the line must read `url = `.
func originURL(config string) string {
	in := false
	for line := range strings.SplitSeq(config, "\n") {
		t := strings.TrimSpace(line)
		if t == `[remote "origin"]` {
			in = true
			continue
		}
		if in && strings.HasPrefix(t, "[") {
			return ""
		}
		if in && strings.HasPrefix(t, "url = ") {
			return strings.TrimSpace(t[len("url = "):])
		}
	}
	return ""
}

var remoteForms = []struct {
	re  *regexp.Regexp
	scp bool
}{
	{regexp.MustCompile(`^git@([^:]+):(.+?)(?:\.git)?$`), true},
	{regexp.MustCompile(`^ssh://([^/]+)/(.+?)(?:\.git)?$`), false},
	{regexp.MustCompile(`^https?://([^/]+)/(.+?)(?:\.git)?$`), false},
}

var portSuffix = regexp.MustCompile(`:\d+$`)

// parseRepoRemote ports KAS's remote parser and per-host owner/name split.
func parseRepoRemote(url string) (provider, owner, name string, ok bool) {
	if url == "" {
		return "", "", "", false
	}
	for _, f := range remoteForms {
		m := f.re.FindStringSubmatch(url)
		if m == nil {
			continue
		}
		if f.scp && strings.Contains(m[2], "@") {
			return "", "", "", false
		}
		host := m[1][strings.LastIndex(m[1], "@")+1:]
		return splitRepoPath(portSuffix.ReplaceAllString(host, ""), m[2])
	}
	return "", "", "", false
}

func splitRepoPath(host, path string) (provider, owner, name string, ok bool) {
	segs := strings.Split(path, "/")
	if host == "git.amazon.com" || strings.HasSuffix(host, ".git.amazon.com") {
		n, found := strings.CutPrefix(path, "pkg/")
		if !found {
			n = segs[len(segs)-1]
		}
		return "gitfarm", "gitfarm", n, n != ""
	}
	// Sequential rather than a switch: KAS falls through to the next host
	// test when a shape does not fit.
	if strings.Contains(host, "github") && len(segs) >= 2 {
		return "github", segs[0], strings.Join(segs[1:], "/"), true
	}
	if i := strings.LastIndex(path, "/"); strings.Contains(host, "gitlab") && i > 0 {
		return "gitlab", path[:i], path[i+1:], true
	}
	if strings.Contains(host, "bitbucket") && len(segs) >= 2 {
		return "bitbucket", segs[0], segs[1], true
	}
	if i := slices.Index(segs, "_git"); strings.Contains(host, "azure") && i >= 1 && i < len(segs)-1 {
		return "azure", strings.Join(segs[:i], "/"), segs[i+1], true
	}
	if len(segs) >= 2 {
		return host, segs[0], strings.Join(segs[1:], "/"), true
	}
	if segs[0] != "" {
		return host, host, segs[0], true
	}
	return "", "", "", false
}
