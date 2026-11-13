// Confinement is inherited from the resolved search ROOT: every open is one name against the
// parent's descriptor with O_NOFOLLOW, Sensitive.Blocks runs on every entry, and resolvePath is NOT
// re-run per entry, since EvalSymlinks could re-root it.

package filebrowse

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/textsearch"
)

const (
	// maxSearchEntries bounds the directory entries a contents search LISTS; its file and
	// directory budgets stop it far sooner, so this is the backstop.
	maxSearchEntries = 1_000_000
	// maxNameSearchEntries bounds a names search's walk. Names mode ranks over the whole walk, so
	// nothing stops it early and this plus nameSearchDeadline set a wide-scope query's latency:
	// about 5 to 10 microseconds an entry.
	maxNameSearchEntries = 100_000
	// maxNameSearchDirs is above contents mode's: a names walk opens no file per directory.
	maxNameSearchDirs = 30_000
	// maxSearchDirs bounds directories visited in contents mode, which maxSearchFiles does not:
	// a tree of empty directories costs one listing each and never fills the file budget.
	maxSearchDirs = 20_000
	// maxSearchDepth bounds how deep the walk descends: the cursor holds one directory handle per
	// level below a root, beside the roots' own, so depth bounds the handles a search adds to them.
	maxSearchDepth = 128
	// maxSearchMatches caps the reply: past this a search is a listing the reader refines.
	maxSearchMatches = 200
	// searchReadDirChunk bounds one ReadDir call's allocation and gives cancellation somewhere to
	// land; filesafe.WalkDirInRoot's batch size. Contents mode reads candidates in batches of it.
	searchReadDirChunk = 256
)

// searchDeadline bounds a contents search's walk and nameSearchDeadline a names search's, which
// runs on every keystroke and so must answer in about a second or two even on a cold disk. A deep
// search, asked for by a click rather than a keystroke, waits longer; the contents one stays under
// the client's 30-second request timeout (static-src/api-client.ts). All are vars so a test can set
// them to zero.
var (
	searchDeadline         = 10 * time.Second
	nameSearchDeadline     = 2 * time.Second
	deepSearchDeadline     = 20 * time.Second
	deepNameSearchDeadline = 10 * time.Second
)

// deepFactor scales a deep search's entry, directory and file budgets.
const deepFactor = 10

// searchBudget is one search's limits. files bounds contents mode only.
type searchBudget struct {
	deadline time.Duration
	entries  int
	dirs     int
	files    int
}

func budgetFor(m searchMode, deep bool) searchBudget {
	b := searchBudget{deadline: searchDeadline, entries: maxSearchEntries, dirs: maxSearchDirs, files: maxSearchFiles}
	deadline := deepSearchDeadline
	if m == modeNames {
		b = searchBudget{deadline: nameSearchDeadline, entries: maxNameSearchEntries, dirs: maxNameSearchDirs}
		deadline = deepNameSearchDeadline
	}
	if deep {
		b.deadline = deadline
		b.entries *= deepFactor
		b.dirs *= deepFactor
		b.files *= deepFactor
	}
	return b
}

// errSearchDeadline is the cause a search's own deadline carries, which a client's cancel does not.
var errSearchDeadline = errors.New("filebrowse: search deadline reached")

// FileMatchKind is what matched. A defined type with constants rather than a
// bare string, because the client branches on it.
type FileMatchKind string

const (
	// matchKindContent is a matching LINE: Line >= 1 and Excerpt is that line.
	matchKindContent FileMatchKind = "content"
	// matchKindName is a matching FILE name. Line is 0 and Excerpt is empty.
	matchKindName FileMatchKind = "name"
	// matchKindDir is a matching DIRECTORY name. Line 0, Excerpt empty.
	matchKindDir FileMatchKind = "dir"
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
	// RootIgnored says the ignore rules hide every root searched, so nothing was walked; a search
	// that includes ignored files never sets it.
	RootIgnored bool `json:"root_ignored"`
	// CanSearchDeeper says a budget or the deadline stopped the walk, so the same request with
	// deep=1 reads further. A deep search never sets it.
	CanSearchDeeper bool `json:"can_search_deeper"`
}

type searchMode int

const (
	modeNames searchMode = iota
	modeContents
)

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
	deep := q.Get("deep") == "1"
	budget := budgetFor(mode, deep)
	ctx, cancel := context.WithTimeoutCause(r.Context(), budget.deadline, errSearchDeadline)
	defer cancel()
	v, answered := newSearchVisitor(ctx, w, budget, mode, q.Get("q"), caseSensitive, filter)
	if answered {
		return
	}
	walk := newSearchWalk(ctx, v, budget)
	walk.sensitive = h.sensitive
	walk.filter = filter
	walk.caseSensitive = caseSensitive
	walk.ignoreRules = q.Get("ignored") != "1"
	walk.walkRoots(roots)
	// A client that left gets no body: a half-scan reported whole is what the caps prevent. The
	// search's own deadline is a stop it reports.
	if r.Context().Err() != nil {
		return
	}
	res := v.result(walk.truncated || ctx.Err() != nil)
	res.RootIgnored = len(roots) > 0 && walk.hiddenRoots == len(roots)
	spent := walk.spent || v.spent() || errors.Is(context.Cause(ctx), errSearchDeadline)
	res.CanSearchDeeper = spent && !deep
	httpserver.WriteJSON(w, res)
}

// newSearchWalk is a walk holding b's entry and directory budgets; the handler and the tests build
// every walk through it.
func newSearchWalk(ctx context.Context, v searchVisitor, b searchBudget) *searchWalk {
	return &searchWalk{ctx: ctx, v: v, maxDirs: b.dirs, maxEntries: b.entries}
}

// When answered, newSearchVisitor has written the reply itself and the caller must not walk.
func newSearchVisitor(ctx context.Context, w http.ResponseWriter, b searchBudget, mode searchMode, query string, caseSensitive bool, filter filesFilter) (v searchVisitor, answered bool) {
	empty := FileSearchResult{Matches: []FileMatch{}}
	if mode == modeContents {
		// The needle is literal, so its spaces are part of it: only "" is no query.
		if query == "" {
			httpserver.WriteJSON(w, empty)
			return nil, true
		}
		cv := newContentVisitor(ctx, query, caseSensitive)
		cv.maxFiles = b.files
		return cv, false
	}
	nq, err := compileQuery(query, caseSensitive)
	if err != nil {
		httpreply.BadRequest(w, "invalid query")
		return nil, true
	}
	if nq.empty() && filter.empty() {
		httpserver.WriteJSON(w, empty)
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
// level order, each directory's entries in path order; the only state they share is the walk's
// own budget accounting.
type searchVisitor interface {
	// file is one admitted non-directory entry of d, whether or not it passes the includes
	// (e.inFilter), since names mode counts it either way. False stops the walk.
	file(d *walkDir, e *walkEntry) bool
	// dir is one admitted directory of d, before the walk queues it. False stops the walk.
	dir(d *walkDir, e *walkEntry) bool
	// leave runs once d's entries are done, while d's descriptor is still open.
	leave(d *walkDir)
	// full reports that the reply holds all it can; the walk then stops at the entry in hand.
	full() bool
	// spent reports that the visitor's own budget stopped the walk.
	spent() bool
	// rootFile searches a root that turned out to be a file, once the gates admitted it. False
	// stops the walk.
	rootFile(f *os.File, abs string, info os.FileInfo) bool
	result(truncated bool) FileSearchResult
}

// walkDir is one entered directory of the walk: its coordinates and the ignore and filter state
// its children inherit, plus the open handle that PINS it while the cursor holds it, against which
// every child is opened by name so no ancestor swap can redirect a read.
type walkDir struct {
	// f is nil while the cursor does not hold the directory.
	f *os.File
	// parent is nil at the search root.
	parent *walkDir
	// ign is the gitignore stack for this directory's children; nil when none applies.
	ign *ignoreLevel
	// name is the entry name in parent, which the cursor opens it by.
	name string
	// abs is the container-absolute path, for the response and the denylist.
	abs string
	// srel is the path relative to the SEARCH ROOT ("" at the root): what the patterns match.
	srel string
	// rrel is the path relative to the enclosing git repository's root.
	rrel string
	// depth is 0 at the search root and counts the handles the cursor holds below it.
	depth int
	// id is the directory's identity at the cursor's first open, which every reopen must match.
	id     dirID
	opened bool
	// lost says an open of this directory failed, so the cursor never opens it, or below it, again.
	lost bool
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

// queuedDir is a directory waiting for its level: a root, or a name in an entered parent. It
// carries only what its parent cannot rebuild, since the queue holds up to the directory budget.
type queuedDir struct {
	root     *walkDir
	parent   *walkDir
	name     string
	included bool
}

// dir builds the walkDir q names. Every field it derives from the parent was settled by the
// parent's enterIgnore before the parent offered q, so it equals what admit saw.
func (q queuedDir) dir() *walkDir {
	if q.root != nil {
		return q.root
	}
	p := q.parent
	abs, srel := p.child(q.name)
	return &walkDir{
		parent: p, name: q.name, ign: p.ign, abs: abs, srel: srel, rrel: path.Join(p.rrel, q.name),
		depth: p.depth + 1, inRepo: p.inRepo, included: q.included, pruneGit: p.pruneGit,
	}
}

// walkEntry is one entry every gate admitted.
type walkEntry struct {
	sub  *entrySubject
	name string
	abs  string
	srel string
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
	maxEntries    int
	hiddenRoots   int
	caseSensitive bool
	// ignoreRules applies .gitignore, .git/info/exclude and the node_modules prune.
	ignoreRules bool
	// truncated is set only where part of the tree went unvisited.
	truncated bool
	// spent says the entry or directory budget left part of the tree unvisited.
	spent bool
}

// walkRoots searches every root in one breadth-first walk, so on a "/" fan-out a budget cuts the
// deepest level across all mounts rather than the last mount whole.
func (w *searchWalk) walkRoots(roots []loc) {
	dirs := make([]*walkDir, 0, len(roots))
	for _, l := range roots {
		d, more := w.openRoot(l)
		if d != nil {
			dirs = append(dirs, d)
		}
		if !more {
			for _, opened := range dirs {
				_ = opened.f.Close()
			}
			return
		}
	}
	w.walkDirs(dirs)
}

// openRoot answers root l as a directory to walk, or searches it on the spot when it is a file;
// more is false when that search stopped the walk.
func (w *searchWalk) openRoot(l loc) (d *walkDir, more bool) {
	f, err := openPinnedRoot(l)
	if err != nil {
		// A root that cannot be opened contributes nothing, and on the "/" fan-out the other
		// mounts still answer, so the reply would otherwise present some mounts as all of them.
		w.truncated = true
		slog.Warn("filebrowse: search open failed", "path", logsafe.Field(l.abs), "error", logsafe.Field(err.Error()))
		return nil, true
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		w.truncated = true
		slog.Warn("filebrowse: search stat failed", "path", logsafe.Field(l.abs), "error", logsafe.Field(err.Error()))
		return nil, true
	}
	parent, hidden := w.rootFrame(l, info.IsDir())
	switch {
	case hidden:
		_ = f.Close()
		w.hiddenRoots++
		return nil, true
	case !info.IsDir():
		defer func() { _ = f.Close() }()
		e, admitted := w.admit(parent, fs.FileInfoToDirEntry(info))
		if !admitted {
			if name := info.Name(); parent.pruneGit && w.ignoredByRules(parent, path.Join(parent.rrel, name), name, false) {
				w.hiddenRoots++
			}
			return nil, true
		}
		if !e.inFilter {
			return nil, true
		}
		return nil, w.v.rootFile(f, l.abs, info)
	}
	return &walkDir{
		f: f, ign: parent.ign, abs: l.abs, rrel: path.Join(parent.rrel, filepath.Base(l.abs)),
		inRepo: parent.inRepo, pruneGit: parent.pruneGit,
	}, true
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

// walkDirs visits the trees under roots breadth-first, each level's directories in path order, so
// a budget or the deadline cuts the deepest unvisited level and never a shallow entry:
// nameVisitor's depth rank and the contents reply's shallowest-first order rest on it. It closes
// every root.
func (w *searchWalk) walkDirs(roots []*walkDir) {
	c := &dirCursor{}
	defer func() {
		c.close()
		for _, r := range roots {
			_ = r.f.Close()
		}
	}()
	queue := make([]queuedDir, 0, len(roots))
	for _, r := range roots {
		queue = append(queue, queuedDir{root: r})
	}
	w.drain(c, queue)
}

// drain visits queue in order, appending each directory's subdirectories, until it is empty or the
// walk stops. Every root is entered; the directory budget refuses a directory when child queues it.
func (w *searchWalk) drain(c *dirCursor, queue []queuedDir) {
	for len(queue) > 0 {
		q := queue[0]
		queue[0] = queuedDir{}
		queue = queue[1:]
		if w.ctx.Err() != nil {
			return
		}
		if w.v.full() {
			w.truncated = true
			return
		}
		d, entered := w.enter(c, q)
		if !entered {
			continue
		}
		w.dirs++
		next, ok := w.visit(d, len(queue))
		if !ok {
			return
		}
		queue = append(queue, next...)
	}
}

// enter has the cursor open q's directory. One that vanished, became a symlink or a non-directory,
// or whose inode changed between two of the cursor's opens, is skipped, the refusal being the
// guarantee; any other failure lost its subtree and is reported once, the directories queued below
// it skipping silently.
func (w *searchWalk) enter(c *dirCursor, q queuedDir) (*walkDir, bool) {
	d := q.dir()
	if d.parent == nil {
		return d, true
	}
	failed, err := c.open(d)
	switch {
	case err == nil:
		return d, true
	case errors.Is(err, fs.ErrNotExist), isSwapRefusal(err), errors.Is(err, errDirReplaced), errors.Is(err, errAncestorLost):
	default:
		w.truncated = true
		slog.Warn("filebrowse: search opendir failed", "path", logsafe.Field(failed), "error", logsafe.Field(err.Error()))
	}
	return nil, false
}

// visit lists d and offers its entries in path order, returning the subdirectories to visit at
// the next level; queued is how many already wait, which the directory budget counts. After a
// listing the entry budget cut, it offers what was read and stops the walk.
func (w *searchWalk) visit(d *walkDir, queued int) ([]queuedDir, bool) {
	entries, whole, more := w.list(d)
	if w.ctx.Err() != nil {
		return nil, false
	}
	if w.ignoreRules && d.pruneGit {
		w.enterIgnore(d, entries, whole)
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(pathOrderKey(a), pathOrderKey(b)) })
	var next []queuedDir
	for _, e := range entries {
		if w.ctx.Err() != nil {
			return nil, false
		}
		if w.v.full() {
			w.truncated = true
			return nil, false
		}
		sub, queue, ok := w.offer(d, e, queued+len(next))
		if !ok {
			return nil, false
		}
		if queue {
			next = append(next, sub)
		}
	}
	w.v.leave(d)
	return next, more
}

// offer hands one entry of d to the visitor and answers the directory to queue for it, if any.
// A directory is queued whatever the includes say: an include never prunes.
func (w *searchWalk) offer(d *walkDir, e fs.DirEntry, queued int) (sub queuedDir, queue, ok bool) {
	we, admitted := w.admit(d, e)
	switch {
	case !admitted:
		return queuedDir{}, false, true
	case !e.IsDir():
		return queuedDir{}, false, w.v.file(d, we)
	case !w.v.dir(d, we):
		return queuedDir{}, false, false
	}
	sub, queue = w.child(d, we, queued)
	return sub, queue, true
}

// child is e as a directory to visit, refused past the depth cap or when the directory budget is
// spent counting the directories already queued: the walk's only directory guard, which also
// bounds the queue by the budget.
func (w *searchWalk) child(d *walkDir, e *walkEntry, queued int) (queuedDir, bool) {
	if d.depth+1 > maxSearchDepth {
		w.truncated = true
		slog.Debug("filebrowse: search depth cap reached", "path", logsafe.Field(e.abs))
		return queuedDir{}, false
	}
	if w.dirs+queued >= w.maxDirs {
		w.truncated, w.spent = true, true
		return queuedDir{}, false
	}
	return queuedDir{parent: d, name: e.name, included: e.included}, true
}

// dirCursor holds one open handle per level from a search root down to the directory being
// listed, and moves to the next directory by reopening only the names below the shared ancestor,
// each by name against its parent's handle with O_NOFOLLOW. chain[0] is a root, which the cursor
// never closes.
type dirCursor struct {
	chain []*walkDir
}

// errDirReplaced is a directory whose inode changed between two of the cursor's opens.
var errDirReplaced = errors.New("filebrowse: directory replaced between two opens")

// errAncestorLost is a directory below one whose open already failed, which the cursor skips
// without opening.
var errAncestorLost = errors.New("filebrowse: an ancestor failed to open")

// open makes d the cursor's deepest level, or answers the path that failed to open.
func (c *dirCursor) open(d *walkDir) (failed string, err error) {
	line := make([]*walkDir, d.depth+1)
	for a := d; a != nil; a = a.parent {
		line[a.depth] = a
	}
	if len(c.chain) == 0 || c.chain[0] != line[0] {
		c.truncate(1)
		c.chain = append(c.chain[:0], line[0])
	}
	keep := 1
	for keep < len(c.chain) && keep < len(line) && c.chain[keep] == line[keep] {
		keep++
	}
	c.truncate(keep)
	for _, next := range line[keep:] {
		if next.lost {
			return next.abs, errAncestorLost
		}
		if openErr := reopen(c.chain[len(c.chain)-1].f, next); openErr != nil {
			next.lost = true
			return next.abs, openErr
		}
		c.chain = append(c.chain, next)
	}
	return "", nil
}

// reopen opens d by name against its parent's handle and, from the second open on, refuses a
// directory whose inode differs from the first open's: the cursor lets go of a directory between
// its listing and its children's visit, and their names were read from the first one.
func reopen(parent *os.File, d *walkDir) error {
	f, err := openChild(parent, d.name, d.abs, pinnedDirFlags)
	if err != nil {
		return err
	}
	id, idErr := dirIdentity(f)
	switch {
	case idErr != nil:
		_ = f.Close()
		return idErr
	case !d.opened:
		d.id, d.opened = id, true
	case id != d.id:
		_ = f.Close()
		return fmt.Errorf("%w: %s", errDirReplaced, d.abs)
	}
	d.f = f
	return nil
}

type dirID struct{ dev, ino uint64 }

func dirIdentity(f *os.File) (dirID, error) {
	info, err := f.Stat()
	if err != nil {
		return dirID{}, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return dirID{}, fmt.Errorf("filebrowse: %s: stat carries no inode", f.Name())
	}
	return dirID{dev: st.Dev, ino: st.Ino}, nil
}

// truncate closes every level from n down, never the root at 0.
func (c *dirCursor) truncate(n int) {
	n = max(n, 1)
	if n >= len(c.chain) {
		return
	}
	for _, d := range c.chain[n:] {
		_ = d.f.Close()
		d.f = nil
	}
	clear(c.chain[n:])
	c.chain = c.chain[:n]
}

func (c *dirCursor) close() {
	c.truncate(1)
	c.chain = nil
}

// pathOrderKey sorts a directory as its children's full paths sort: `b.txt` before `b/x`.
func pathOrderKey(e fs.DirEntry) string {
	if e.IsDir() {
		return e.Name() + "/"
	}
	return e.Name()
}

// list returns d's entries, or those read before the entry budget or a read failure stopped it,
// with whole false; more is false when the walk must stop after offering them.
func (w *searchWalk) list(d *walkDir) (entries []fs.DirEntry, whole, more bool) {
	var all []fs.DirEntry
	for {
		if w.ctx.Err() != nil {
			return nil, false, false
		}
		chunk, err := d.f.ReadDir(searchReadDirChunk)
		if left := w.maxEntries - w.entries; len(chunk) > left {
			w.entries = w.maxEntries
			w.truncated, w.spent = true, true
			return append(all, chunk[:left]...), false, false
		}
		w.entries += len(chunk)
		all = append(all, chunk...)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return all, true, true
			}
			w.truncated = true
			slog.Warn("filebrowse: search readdir failed", "path", logsafe.Field(d.abs), "error", logsafe.Field(err.Error()))
			return all, false, true
		}
		if len(chunk) == 0 {
			return all, true, true
		}
	}
}

// enterIgnore loads d's own rule files. From a whole listing it opens only what the listing names,
// so a directory with no rule file costs no syscall; a partial one may have dropped either name, so
// it looks both up against d's handle.
func (w *searchWalk) enterIgnore(d *walkDir, entries []fs.DirEntry, whole bool) {
	hasGit, gitIsDir, hasIgnore := ruleFilesListed(entries)
	if !whole {
		var err error
		hasGit, gitIsDir, hasIgnore, err = ruleFilesAt(d.f, d.abs)
		if err != nil {
			logSearchReadError(d.abs, err)
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

func ruleFilesListed(entries []fs.DirEntry) (hasGit, gitIsDir, hasIgnore bool) {
	for _, e := range entries {
		switch e.Name() {
		case ".git":
			hasGit, gitIsDir = true, e.IsDir()
		case ".gitignore":
			hasIgnore = e.Type().IsRegular()
		}
	}
	return hasGit, gitIsDir, hasIgnore
}

// ruleFilesAt looks both rule files up in dir; a failed lookup counts as absent and is returned.
func ruleFilesAt(dir *os.File, abs string) (hasGit, gitIsDir, hasIgnore bool, err error) {
	gitType, hasGit, gitErr := lstatTypeAt(dir, ".git", filepath.Join(abs, ".git"))
	ignoreType, found, ignoreErr := lstatTypeAt(dir, ".gitignore", filepath.Join(abs, ".gitignore"))
	return hasGit, gitType == fs.ModeDir, found && ignoreType == 0, errors.Join(gitErr, ignoreErr)
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
		sub: sub, name: name, abs: abs, srel: srel, typ: e.Type(),
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
