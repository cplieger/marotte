// Confinement is inherited from the resolved search ROOT: every open is one name against the
// parent's descriptor with O_NOFOLLOW, Sensitive.Blocks runs on every entry, and resolvePath is NOT
// re-run per entry, since EvalSymlinks could re-root it.

package filebrowse

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/textsearch"
	"github.com/cplieger/webhttp/v3"
)

const (
	// maxSearchEntries bounds the directory entries one search LISTS, in either mode: names mode
	// opens no files, so this is what bounds its walk.
	maxSearchEntries = 1_000_000
	// maxNameSearchDirs is five times contents mode's: a names walk opens no file per directory.
	maxNameSearchDirs = 100_000
	// maxSearchDirs bounds directories visited in contents mode, which maxSearchFiles does not:
	// a tree of empty directories costs one listing each and never fills the file budget.
	maxSearchDirs = 20_000
	// maxSearchDepth bounds how deep the walk descends: a directory handle stays open while the
	// walk is inside it, so depth IS the number of directory handles one search holds.
	maxSearchDepth = 128
	// maxSearchMatches caps the reply: past this a search is a listing the reader refines.
	maxSearchMatches = 200
	// searchReadDirChunk bounds one ReadDir call's allocation and gives cancellation somewhere to
	// land; atomicfile.WalkDirInRoot's batch size. Contents mode reads candidates in batches of it.
	searchReadDirChunk = 256
)

// searchDeadline bounds one search's walk; a var so a test can set it to zero.
var searchDeadline = 10 * time.Second

// errSearchDeadline is the cause a search's own deadline carries, which a client's cancel does not.
var errSearchDeadline = errors.New("filebrowse: search deadline reached")

// FileMatchKind is what matched. A defined type with constants rather than a
// bare string, because the client branches on it.
type FileMatchKind string

const (
	// MatchKindContent is a matching LINE: Line >= 1 and Excerpt is that line.
	MatchKindContent FileMatchKind = "content"
	// MatchKindName is a matching FILE name. Line is 0 and Excerpt is empty.
	MatchKindName FileMatchKind = "name"
	// MatchKindDir is a matching DIRECTORY name. Line 0, Excerpt empty.
	MatchKindDir FileMatchKind = "dir"
)

// MatchRange is one highlighted span, in UTF-16 code units: the client slices a JS string with it.
type MatchRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// FileMatch is one hit: a matching LINE, or an entry whose NAME matched. A line
// number rather than a byte offset, because the client opens a content result at the
// editor's `/file/{path}#L<line>`; a name hit carries Line 0, which matchLines never
// produces.
type FileMatch struct {
	// Path is the container-absolute path, the same namespace every other
	// /api/file* route speaks.
	Path    string        `json:"path"`
	Excerpt string        `json:"excerpt"`
	Kind    FileMatchKind `json:"kind"`
	// Ranges index the BASENAME of Path for a name or dir row, and Excerpt for a content row.
	Ranges []MatchRange `json:"ranges"`
	Line   int          `json:"line"`
}

// FileSearchResult is GET /api/files/search's reply: the hits, cut at maxSearchMatches, beside the
// tally. Scanned counts entries visited in names mode and files read in contents mode.
type FileSearchResult struct {
	Matches []FileMatch `json:"matches"`
	textsearch.Tally
}

type searchMode int

const (
	modeNames searchMode = iota
	modeContents
)

func dirBudget(m searchMode) int {
	if m == modeNames {
		return maxNameSearchDirs
	}
	return maxSearchDirs
}

func parseSearchMode(s string) (searchMode, bool) {
	switch s {
	case "", "names":
		return modeNames, true
	case "contents":
		return modeContents, true
	}
	return 0, false
}

func (h *Handler) handleFilesSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	q := r.URL.Query()
	// Roots first, so a path outside the grants is a 403 whatever else the query carries.
	roots, ok := h.searchRoots(w, q.Get("path"))
	if !ok {
		return
	}
	mode, ok := parseSearchMode(q.Get("mode"))
	if !ok {
		httpreply.BadRequest(w, "invalid mode")
		return
	}
	caseSensitive := q.Get("case") == "1"
	filter, err := compileFilesFilter(q.Get("files"), caseSensitive)
	if err != nil {
		httpreply.BadRequest(w, "invalid files filter")
		return
	}
	ctx, cancel := context.WithTimeoutCause(r.Context(), searchDeadline, errSearchDeadline)
	defer cancel()
	v, answered := newSearchVisitor(ctx, w, mode, q.Get("q"), caseSensitive, filter)
	if answered {
		return
	}
	walk := &searchWalk{
		ctx:           ctx,
		v:             v,
		sensitive:     h.sensitive,
		filter:        filter,
		caseSensitive: caseSensitive,
		ignoreRules:   q.Get("ignored") != "1",
		maxDirs:       dirBudget(mode),
	}
	for _, root := range roots {
		if !walk.addRoot(root) {
			break
		}
	}
	// A client that left gets no body: a half-scan reported whole is what the caps prevent. The
	// search's own deadline is a stop it reports.
	if r.Context().Err() != nil {
		return
	}
	webhttp.WriteJSON(w, v.result(walk.truncated || ctx.Err() != nil))
}

// When answered, newSearchVisitor has written the reply itself and the caller must not walk.
func newSearchVisitor(ctx context.Context, w http.ResponseWriter, mode searchMode, query string, caseSensitive bool, filter filesFilter) (v searchVisitor, answered bool) {
	empty := FileSearchResult{Matches: []FileMatch{}}
	if mode == modeContents {
		// The needle is literal, so its spaces are part of it: only "" is no query.
		if query == "" {
			webhttp.WriteJSON(w, empty)
			return nil, true
		}
		return newContentVisitor(ctx, query, caseSensitive), false
	}
	nq, err := compileQuery(query, caseSensitive)
	if err != nil {
		httpreply.BadRequest(w, "invalid query")
		return nil, true
	}
	if nq.empty() && filter.empty() {
		webhttp.WriteJSON(w, empty)
		return nil, true
	}
	return newNameVisitor(nq), false
}

// searchRoots resolves the `path` parameter into the locations to walk, in path order.
//
// "/" is not a real directory in the allow-list model, so it fans out over every granted mount:
// "search everything" is the honest reading of the root listing, and the caps are shared across
// the fan-out rather than multiplied by it.
func (h *Handler) searchRoots(w http.ResponseWriter, reqPath string) (roots []loc, ok bool) {
	if reqPath == "" || reqPath == "." || filepath.Clean("/"+reqPath) == "/" {
		roots = make([]loc, 0, len(h.mounts))
		for i := range h.mounts {
			m := &h.mounts[i]
			roots = append(roots, loc{m: m, abs: m.dir})
		}
		slices.SortFunc(roots, func(a, b loc) int { return strings.Compare(a.abs, b.abs) })
		return roots, true
	}
	l, resolved := h.resolveOrForbid(w, reqPath)
	if !resolved {
		return nil, false
	}
	return []loc{l}, true
}

// searchVisitor is what one mode does with the entries the walker admits. The walker calls it in
// exact path order; the only state they share is the walk's own budget accounting.
type searchVisitor interface {
	// file is one admitted non-directory entry of d, whether or not it passes the includes
	// (e.inFilter), since names mode counts it either way. False stops the walk.
	file(d *walkDir, e *walkEntry) bool
	// dir is one admitted directory of d, before the walk descends into it. False stops the walk.
	dir(d *walkDir, e *walkEntry) bool
	// leave runs once d's entries are done, while d's descriptor is still open.
	leave(d *walkDir)
	// full reports that the reply holds all it can; the walk then stops at the entry in hand.
	full() bool
	// rootFile searches a root that turned out to be a file, once the gates admitted it. False
	// stops the walk.
	rootFile(f *os.File, abs string, info os.FileInfo) bool
	result(truncated bool) FileSearchResult
}

// walkDir is one directory the walk is inside: the open handle that PINS it, against which every
// child is opened by name so no ancestor swap can redirect a read, plus its coordinates and the
// ignore and filter state its children inherit. One descriptor per level.
type walkDir struct {
	f *os.File
	// ign is the gitignore stack for this directory's children; nil when none applies.
	ign *ignoreLevel
	// abs is the container-absolute path, for the response and the denylist.
	abs string
	// srel is the path relative to the SEARCH ROOT ("" at the root): what the patterns match.
	srel string
	// rrel is the path relative to the enclosing git repository's root.
	rrel string
	// depth is 0 at the search root and counts the open handles held.
	depth int
	// inRepo says a git repository encloses this directory, so its .gitignore applies.
	inRepo bool
	// included says an include pattern matched this directory or an ancestor.
	included bool
	// pruneGit is false only under a search root that is itself inside a .git directory.
	pruneGit bool
}

func (d *walkDir) child(name string) (abs, srel string) {
	return filepath.Join(d.abs, name), path.Join(d.srel, name)
}

// walkEntry is one entry every gate admitted.
type walkEntry struct {
	sub  *entrySubject
	name string
	abs  string
	srel string
	rrel string
	typ  fs.FileMode
	// inFilter says the entry passes the include half of the Files filter.
	inFilter bool
	included bool
}

// searchWalk carries one search's walk accounting. Every field is owned by the walk goroutine.
type searchWalk struct {
	ctx           context.Context
	v             searchVisitor
	sensitive     Sensitive
	filter        filesFilter
	entries       int
	dirs          int
	maxDirs       int
	caseSensitive bool
	// ignoreRules applies .gitignore, .git/info/exclude and the node_modules prune.
	ignoreRules bool
	// truncated is set only where part of the tree went unvisited.
	truncated bool
}

// addRoot's false stops the whole scan, the other roots of a "/" fan-out included.
func (w *searchWalk) addRoot(l loc) bool {
	f, err := openPinnedRoot(l)
	if err != nil {
		// A root that cannot be opened contributes nothing, and on the "/" fan-out the other
		// mounts still answer, so the reply would otherwise present some mounts as all of them.
		w.truncated = true
		slog.Warn("filebrowse: search open failed", "path", logsafe.Field(l.abs), "error", logsafe.Field(err.Error()))
		return true
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		w.truncated = true
		slog.Warn("filebrowse: search stat failed", "path", logsafe.Field(l.abs), "error", logsafe.Field(err.Error()))
		return true
	}
	parent, hidden := w.rootFrame(l, info.IsDir())
	switch {
	case hidden:
		_ = f.Close()
		return true
	case !info.IsDir():
		defer func() { _ = f.Close() }()
		e, admitted := w.admit(parent, fs.FileInfoToDirEntry(info))
		if !admitted || !e.inFilter {
			return true
		}
		return w.v.rootFile(f, l.abs, info)
	}
	return w.walk(&walkDir{
		f: f, ign: parent.ign, abs: l.abs, rrel: path.Join(parent.rrel, filepath.Base(l.abs)),
		inRepo: parent.inRepo, pruneGit: parent.pruneGit,
	})
}

// rootFrame is the listing a walk from the mount would have met root l in, and whether the nearest
// repository's ignore model would have kept that walk from reaching l: node_modules or a
// git-excluded directory above it, or l itself when it is a directory. A clone inside an outer
// repository's ignored directory answers by its own rules below its root, as git does from inside
// it. The Files filter frames the search, so only a file root meets it, through admit. A root
// inside .git is exempt, as a walk there is.
func (w *searchWalk) rootFrame(l loc, isDir bool) (parent *walkDir, hidden bool) {
	parent = &walkDir{abs: filepath.Dir(l.abs), pruneGit: !insideGitDir(l.abs)}
	if !w.ignoreRules || !parent.pruneGit {
		return parent, false
	}
	rc := ancestorRepo(l, w.sensitive)
	parent.ign, parent.rrel, parent.inRepo = rc.ign, path.Dir(rc.rrel), rc.inRepo
	if rc.hidden || underNodeModules(l, isDir) {
		return parent, true
	}
	return parent, isDir && rc.ign != nil && rc.ign.ignored(rc.rrel, filepath.Base(l.abs), true)
}

// underNodeModules reports a node_modules directory between the mount and l, counting l itself
// only when l is a directory.
func underNodeModules(l loc, isDir bool) bool {
	segs := strings.Split(relUnder(l.m.dir, l.abs), "/")
	if !isDir {
		segs = segs[:len(segs)-1]
	}
	return slices.Contains(segs, "node_modules")
}

// walk closes d only after everything opened against it, and refuses a directory past the budget
// before listing it.
func (w *searchWalk) walk(d *walkDir) bool {
	defer func() { _ = d.f.Close() }()
	if w.dirs >= w.maxDirs {
		w.truncated = true
		return false
	}
	w.dirs++
	entries, ok := w.list(d)
	if !ok {
		return false
	}
	if w.ignoreRules && d.pruneGit {
		w.enterIgnore(d, entries)
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(pathOrderKey(a), pathOrderKey(b)) })
	for _, e := range entries {
		if w.ctx.Err() != nil {
			return false
		}
		if w.v.full() {
			w.truncated = true
			return false
		}
		if !w.entry(d, e) {
			return false
		}
	}
	w.v.leave(d)
	return true
}

// pathOrderKey sorts a directory as its children's full paths sort: `b.txt` before `b/x`.
func pathOrderKey(e fs.DirEntry) string {
	if e.IsDir() {
		return e.Name() + "/"
	}
	return e.Name()
}

// list returns the entries read before a failure with false, so the answer reads truncated.
func (w *searchWalk) list(d *walkDir) ([]fs.DirEntry, bool) {
	var all []fs.DirEntry
	for {
		if w.ctx.Err() != nil {
			return nil, false
		}
		chunk, err := d.f.ReadDir(searchReadDirChunk)
		w.entries += len(chunk)
		if w.entries > maxSearchEntries {
			w.truncated = true
			return nil, false
		}
		all = append(all, chunk...)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				w.truncated = true
				slog.Warn("filebrowse: search readdir failed", "path", logsafe.Field(d.abs), "error", logsafe.Field(err.Error()))
			}
			return all, true
		}
		if len(chunk) == 0 {
			return all, true
		}
	}
}

// enterIgnore opens only what d's listing names, so a directory with no rule file costs no open.
func (w *searchWalk) enterIgnore(d *walkDir, entries []fs.DirEntry) {
	var hasGit, gitIsDir, hasIgnore bool
	for _, e := range entries {
		switch e.Name() {
		case ".git":
			hasGit, gitIsDir = true, e.IsDir()
		case ".gitignore":
			hasIgnore = e.Type().IsRegular()
		}
	}
	if hasGit {
		d.inRepo, d.rrel, d.ign = true, "", nil
		if gitIsDir {
			d.ign = readInfoExclude(d.f, d.abs, w.sensitive)
		}
	}
	if hasIgnore && d.inRepo {
		d.ign = readIgnoreLevel(d.f, ".gitignore", filepath.Join(d.abs, ".gitignore"), d.rrel, d.ign, w.sensitive)
	}
}

// A directory is descended whatever the includes say: an include never prunes.
func (w *searchWalk) entry(d *walkDir, e fs.DirEntry) bool {
	we, admitted := w.admit(d, e)
	if !admitted {
		return true
	}
	if !e.IsDir() {
		return w.v.file(d, we)
	}
	if !w.v.dir(d, we) {
		return false
	}
	if w.v.full() {
		w.truncated = true
		return false
	}
	return w.descend(d, we)
}

// admit records the include half of the Files filter on the entry rather than applying it, since
// each visitor decides what an entry outside the includes is worth.
func (w *searchWalk) admit(d *walkDir, e fs.DirEntry) (*walkEntry, bool) {
	name := e.Name()
	abs, srel := d.child(name)
	if w.sensitive.Blocks(abs) {
		return nil, false
	}
	isDir := e.IsDir()
	rrel := path.Join(d.rrel, name)
	if d.pruneGit && ((isDir && name == ".git") || w.ignoredByRules(d, rrel, name, isDir)) {
		return nil, false
	}
	sub := newEntrySubject(name, srel, isDir, w.caseSensitive)
	if w.filter.excludes(sub) {
		return nil, false
	}
	included := d.included || (w.filter.hasIncludes() && w.filter.includes(sub))
	return &walkEntry{
		sub: sub, name: name, abs: abs, srel: srel, rrel: rrel, typ: e.Type(),
		inFilter: !w.filter.hasIncludes() || included, included: included,
	}, true
}

func (w *searchWalk) ignoredByRules(d *walkDir, rrel, name string, isDir bool) bool {
	if !w.ignoreRules {
		return false
	}
	if isDir && name == "node_modules" {
		return true
	}
	return d.ign != nil && d.ign.ignored(rrel, name, isDir)
}

func (w *searchWalk) descend(d *walkDir, e *walkEntry) bool {
	if d.depth+1 > maxSearchDepth {
		// Refusing to descend leaves entries unvisited, which is what Truncated says.
		w.truncated = true
		slog.Debug("filebrowse: search depth cap reached", "path", logsafe.Field(e.abs))
		return true
	}
	f, err := openChild(d.f, e.name, e.abs, pinnedDirFlags)
	if err != nil {
		// A directory that vanished or was swapped under the walk is normal; the refusal is the
		// guarantee. Anything else lost the whole subtree, which Truncated reports.
		if !errors.Is(err, fs.ErrNotExist) && !isSwapRefusal(err) {
			w.truncated = true
			slog.Warn("filebrowse: search opendir failed", "path", logsafe.Field(e.abs), "error", logsafe.Field(err.Error()))
		}
		return true
	}
	return w.walk(&walkDir{
		f: f, ign: d.ign, abs: e.abs, srel: e.srel, rrel: e.rrel, depth: d.depth + 1,
		inRepo: d.inRepo, included: e.included, pruneGit: d.pruneGit,
	})
}

// matchesOrEmpty is ms with a nil slice replaced, because a nil slice serialises as JSON null.
func matchesOrEmpty(ms []FileMatch) []FileMatch {
	if ms == nil {
		return []FileMatch{}
	}
	return ms
}

// rangesOrEmpty is rs with a nil slice replaced, for the same reason.
func rangesOrEmpty(rs []MatchRange) []MatchRange {
	if rs == nil {
		return []MatchRange{}
	}
	return rs
}
