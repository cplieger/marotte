package filebrowse

import (
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cplieger/marotte/internal/logsafe"
)

const (
	// maxGitignoreBytes bounds one rule file's read; the rest is dropped.
	maxGitignoreBytes = 256 << 10
	// maxGitignoreRules bounds one rule file's rules, so a hostile clone cannot make every entry
	// cost an unbounded scan.
	maxGitignoreRules = 10_000
)

type ignoreRule struct {
	// base matches the basename at any depth: the pattern held no "/" but a trailing one.
	base segGlob
	// path matches the path under the rule file's directory: the pattern held a leading or middle "/".
	path     pathGlob
	anchored bool
	negate   bool
	dirOnly  bool
}

// ignoreLevel is one rule file, chained to the files above it in the same repository. The deepest
// level is asked first and, within a level, the last rule first: git's last-match-wins order.
type ignoreLevel struct {
	parent *ignoreLevel
	// base is the rule file's directory relative to the repository root, "" at the root.
	base  string
	rules []ignoreRule
}

// ignored reports whether git ignores the entry at rrel (relative to the repository root). A
// re-include under an excluded directory cannot happen here, because the walk never enters one.
func (l *ignoreLevel) ignored(rrel, name string, isDir bool) bool {
	for lv := l; lv != nil; lv = lv.parent {
		if verdict, hit := lv.match(rrel, name, isDir); hit {
			return verdict
		}
	}
	return false
}

// match is this level's verdict, last rule first, and whether any rule matched at all.
func (l *ignoreLevel) match(rrel, name string, isDir bool) (ignored, hit bool) {
	rel := rrel
	if l.base != "" {
		rel = strings.TrimPrefix(rrel, l.base+"/")
	}
	var segs []string
	for i := len(l.rules) - 1; i >= 0; i-- {
		r := &l.rules[i]
		if r.dirOnly && !isDir {
			continue
		}
		if !r.anchored {
			if r.base.match(name) {
				return !r.negate, true
			}
			continue
		}
		if segs == nil {
			segs = strings.Split(rel, "/")
		}
		if r.path.match(segs) {
			return !r.negate, true
		}
	}
	return false, false
}

// parseIgnoreRules reads gitignore(5) lines. Matching is case-sensitive, as git's is on Linux, and
// braces are literal. A pattern the matcher cannot compile matches nothing, as in git.
func parseIgnoreRules(data []byte, abs string) []ignoreRule {
	var rules []ignoreRule
	for line := range strings.SplitSeq(string(data), "\n") {
		r, ok := parseIgnoreLine(line)
		if !ok {
			continue
		}
		if len(rules) == maxGitignoreRules {
			slog.Debug("filebrowse: search ignore file rule cap reached", "path", logsafe.Field(abs))
			break
		}
		rules = append(rules, r)
	}
	return rules
}

func parseIgnoreLine(line string) (ignoreRule, bool) {
	line = trimIgnoreTrailingSpace(strings.TrimSuffix(line, "\r"))
	if line == "" || line[0] == '#' {
		return ignoreRule{}, false
	}
	var r ignoreRule
	if line[0] == '!' {
		r.negate = true
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		r.dirOnly = true
		line = line[:len(line)-1]
	}
	if line == "" {
		return ignoreRule{}, false
	}
	if !strings.Contains(line, "/") {
		g, err := compileSegment(line, starRunIsStar)
		if err != nil {
			return ignoreRule{}, false
		}
		r.base = g
		return r, true
	}
	g, err := compilePath(strings.TrimPrefix(line, "/"), starRunIsStar)
	if err != nil {
		return ignoreRule{}, false
	}
	// A trailing "/**" matches everything INSIDE, never the directory itself, so that
	// `dir/**` plus `!dir/keep` can re-include: one segment, then any number.
	if g[len(g)-1].double {
		g = append(g[:len(g)-1], pathSeg{glob: segGlob{{kind: tokStar}}}, pathSeg{double: true})
	}
	r.anchored, r.path = true, g
	return r, true
}

// trimIgnoreTrailingSpace drops trailing spaces, except one escaped with a backslash.
func trimIgnoreTrailingSpace(s string) string {
	for strings.HasSuffix(s, " ") {
		body := s[:len(s)-1]
		slashes := len(body) - len(strings.TrimRight(body, `\`))
		if slashes%2 == 1 {
			return s
		}
		s = body
	}
	return s
}

// readIgnoreLevel reads one rule file opened against dir by name, refusing a symlink and any type
// but a regular file, and chains its rules onto parent. A file it cannot read adds nothing.
func readIgnoreLevel(dir *os.File, name, abs, base string, parent *ignoreLevel, sensitive Sensitive) *ignoreLevel {
	if sensitive.Blocks(abs) {
		return parent
	}
	f, err := openChild(dir, name, abs, pinnedFileFlags)
	if err != nil {
		logSearchReadError(abs, err)
		return parent
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return parent
	}
	data, err := io.ReadAll(io.LimitReader(f, maxGitignoreBytes+1))
	if err != nil {
		logSearchReadError(abs, err)
		return parent
	}
	if len(data) > maxGitignoreBytes {
		slog.Debug("filebrowse: search ignore file read cap reached", "path", logsafe.Field(abs))
		data = data[:maxGitignoreBytes]
	}
	rules := parseIgnoreRules(data, abs)
	if len(rules) == 0 {
		return parent
	}
	return &ignoreLevel{parent: parent, base: base, rules: rules}
}

// readInfoExclude reads <repo>/.git/info/exclude, the bottom level of a repository's stack. A
// `.git` FILE (a worktree or submodule) points elsewhere and contributes nothing.
func readInfoExclude(repo *os.File, repoAbs string, sensitive Sensitive) *ignoreLevel {
	gitAbs := filepath.Join(repoAbs, ".git")
	git, err := openChild(repo, ".git", gitAbs, pinnedDirFlags)
	if err != nil {
		return nil
	}
	defer func() { _ = git.Close() }()
	infoAbs := filepath.Join(gitAbs, "info")
	info, err := openChild(git, "info", infoAbs, pinnedDirFlags)
	if err != nil {
		return nil
	}
	defer func() { _ = info.Close() }()
	return readIgnoreLevel(info, "exclude", filepath.Join(infoAbs, "exclude"), "", nil, sensitive)
}

// repoContext is where a search root sits in a git repository found ABOVE it.
type repoContext struct {
	ign *ignoreLevel
	// rrel is the root's path relative to the repository root.
	rrel   string
	inRepo bool
	// hidden says the rules exclude a directory between the repository root and the search root,
	// which a walk from the repository root would have pruned with everything under it.
	hidden bool
}

// ancestorRepo finds the nearest repository strictly above the search root and inside its mount,
// and loads its rule stack from the repository root down to the root's parent. Every directory is
// reopened from the mount one component at a time, so no symlink is followed on the way up.
func ancestorRepo(l loc, sensitive Sensitive) repoContext {
	var chain []string
	for cur := l.abs; cur != l.m.dir; {
		cur = filepath.Dir(cur)
		chain = append(chain, cur)
	}
	for i, dir := range chain {
		isRepo, gitIsDir, ok := hasGitEntry(l.m, dir)
		if !ok {
			return repoContext{}
		}
		if !isRepo {
			continue
		}
		return loadAncestorStack(l, chain[:i+1], gitIsDir, sensitive)
	}
	return repoContext{}
}

func hasGitEntry(m *mount, dir string) (isRepo, isDir, ok bool) {
	f, err := openPinnedRoot(loc{m: m, abs: dir})
	if err != nil {
		return false, false, false
	}
	defer func() { _ = f.Close() }()
	g, err := openChild(f, ".git", filepath.Join(dir, ".git"), pinnedFileFlags)
	if err != nil {
		return false, false, errors.Is(err, fs.ErrNotExist) || isSwapRefusal(err)
	}
	defer func() { _ = g.Close() }()
	info, err := g.Stat()
	if err != nil {
		return false, false, false
	}
	return true, info.IsDir(), true
}

// loadAncestorStack builds the stack for chain, nearest ancestor first and the repository root last.
func loadAncestorStack(l loc, chain []string, gitIsDir bool, sensitive Sensitive) repoContext {
	repoRoot := chain[len(chain)-1]
	var ign *ignoreLevel
	for j, dir := range slices.Backward(chain) {
		if j < len(chain)-1 && ign != nil && ign.ignored(relUnder(repoRoot, dir), filepath.Base(dir), true) {
			return repoContext{inRepo: true, hidden: true}
		}
		f, err := openPinnedRoot(loc{m: l.m, abs: dir})
		if err != nil {
			continue
		}
		if j == len(chain)-1 && gitIsDir {
			ign = readInfoExclude(f, dir, sensitive)
		}
		ign = readIgnoreLevel(f, ".gitignore", filepath.Join(dir, ".gitignore"), relUnder(repoRoot, dir), ign, sensitive)
		_ = f.Close()
	}
	return repoContext{ign: ign, rrel: relUnder(repoRoot, l.abs), inRepo: true}
}

// relUnder is p relative to dir, "" for dir itself; p is dir or beneath it.
func relUnder(dir, p string) string {
	return strings.TrimPrefix(strings.TrimPrefix(p, dir), "/")
}

// insideGitDir reports whether abs is a .git directory or beneath one, where git applies no
// ignore rules and the walk must not prune .git.
func insideGitDir(abs string) bool {
	return strings.Contains(abs+"/", "/.git/")
}
