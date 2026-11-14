// The document-oriented `.kiro` inventory behind GET /api/workspace/kiro-docs: one row per
// file with its front-matter. Each category names its own root, so markdown no category claims
// gets no row.

package server

import (
	"cmp"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/go/pathinside"
	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/spec"
	"github.com/cplieger/marotte/internal/steering"
)

// Document categories, matching the page's sub-tabs. Wire values the client keys tabs off.
const (
	catSteering = "steering"
	catSkill    = "skill"
	catAgent    = "agent"
	catSpec     = "spec"
	catHook     = "hook"
	catPrompt   = "prompt"
)

const skillsDir = "skills"

// Per-category and overall bounds, set above any real corpus while still refusing an
// unbounded tree.
const (
	maxDocsPerCategory = 500
	maxDocsTotal       = 2000
	// maxSpecWalkDepth bounds the specs walk: `.kiro/specs/<feature>/<file>.md`
	// is two levels, so three allows one unexpected nesting level and no more.
	maxSpecWalkDepth = 3
	// mdSigDepth is sigDepth for a walkMarkdown category: the walk enters directories down to
	// maxSpecWalkDepth, so its files sit one level deeper.
	mdSigDepth = maxSpecWalkDepth + 1
)

// KiroDoc is one row on the configuration browser. Fields are per-category and mostly
// omitempty; the client shapes each tab's columns.
type KiroDoc struct {
	Category string `json:"category"`
	// Name is the row label: front-matter `name`, else the first H1, else the basename.
	Name string `json:"name"`
	Path string `json:"path"`
	// Group is the parent label for a nested category: a spec's feature
	// directory. Three files named requirements.md in a flat list identify
	// nothing.
	Group string `json:"group,omitempty"`

	Description string `json:"description,omitempty"`
	Inclusion   string `json:"inclusion,omitempty"`
	FileMatch   string `json:"file_match,omitempty"`
	Model       string `json:"model,omitempty"`

	// Trigger and Action carry a hook row (hooks are JSON, not markdown).
	Trigger string `json:"trigger,omitempty"`
	Action  string `json:"action,omitempty"`

	Tools            []string `json:"tools,omitempty"`
	SteeringOverride bool     `json:"steering_override,omitempty"`

	// ReadOnly says this row is not writable, so it renders without edit or delete. omitempty, so
	// read-only is only ever asserted by the server, never produced by a field failing to arrive.
	ReadOnly bool `json:"read_only,omitempty"`

	// DeleteProtected says this row renders without DELETE but keeps its edit: set for an entry
	// reached through a symlink, where the delete route would unlink the link's TARGET.
	DeleteProtected bool `json:"delete_protected,omitempty"`
}

// KiroDocsResponse is GET /api/workspace/kiro-docs's reply. Truncated says a cap
// or a cancelled request stopped the scan before it read the whole tree, so a
// short list is not read as the whole inventory.
type KiroDocsResponse struct {
	Docs      []KiroDoc `json:"docs"`
	Truncated bool      `json:"truncated"`
}

// docScan accumulates one scan: the rows kept and whether anything was left unread.
type docScan struct {
	docs      []KiroDoc
	truncated bool
}

// add keeps a row while the category has room and reports whether it did. A
// row past the cap is dropped and the cut recorded, so exactly the cap's worth
// of rows is not a cut and one more is.
func (sc *docScan) add(d *KiroDoc) bool {
	if len(sc.docs) >= maxDocsPerCategory {
		sc.truncated = true
		return false
	}
	sc.docs = append(sc.docs, *d)
	return true
}

func (sc *docScan) absorb(part docScan) {
	sc.docs = append(sc.docs, part.docs...)
	sc.truncated = sc.truncated || part.truncated
}

// docsCache memoizes one scan behind a cheap directory signature (the page refetches on every
// settings_updated). The mutex also serializes concurrent requests into one scan.
type docsCache struct {
	sig string
	res KiroDocsResponse
	mu  sync.Mutex
}

func (s *Server) handleKiroDocs(w http.ResponseWriter, r *http.Request) {
	if !httpreply.RequireMethod(w, r, http.MethodGet) {
		return
	}
	res := s.collectKiroDocs(r.Context())
	if res.Docs == nil {
		res.Docs = []KiroDoc{}
	}
	httpserver.WriteJSON(w, res)
}

func (s *Server) collectKiroDocs(ctx context.Context) KiroDocsResponse {
	roots := s.kiroRoots()
	sig := dirSignature(roots)

	if s.kiroDocs == nil {
		// No cache wired (the zero Server in tests): scan directly.
		return scanKiroRoots(ctx, roots, s.sensitive)
	}
	s.kiroDocs.mu.Lock()
	defer s.kiroDocs.mu.Unlock()
	if s.kiroDocs.sig == sig && s.kiroDocs.res.Docs != nil {
		return s.kiroDocs.res
	}
	res := scanKiroRoots(ctx, roots, s.sensitive)
	// A cancelled scan is partial; caching it would serve a truncated list.
	if ctx.Err() != nil {
		return res
	}
	s.kiroDocs.sig = sig
	s.kiroDocs.res = res
	return res
}

// kiroRoot is one `.kiro` tree: where it lives and the path prefix its rows
// carry (which is what the client opens in the editor).
type kiroRoot struct {
	fsPath string
	prefix string
}

// kiroRoots enumerates the `.kiro` trees in scope: the workspace root's plus one per non-dot
// subdirectory.
func (s *Server) kiroRoots() []kiroRoot {
	workBase := strings.TrimPrefix(s.workDir, "/")
	specRoots := spec.Roots(s.workDir)
	roots := make([]kiroRoot, 0, len(specRoots))
	for _, r := range specRoots {
		roots = append(roots, kiroRoot{fsPath: r.Dir, prefix: workBase + "/" + r.Rel})
	}
	return roots
}

// sigDepth is how many directory levels below each category the signature reads: the levels a
// scan builds rows from. A change deeper than its category's depth here would serve a stale scan.
var sigDepth = map[string]int{
	"": 1, "steering": mdSigDepth, skillsDir: 2, "agents": 1,
	"specs": mdSigDepth, "hooks": 1, "prompts": 1,
}

// dirSignature builds a cache key from each root's category directories: the mtime AND the
// entry names down to sigDepth. Names are load-bearing: Linux stamps mtimes from a coarse clock,
// so two changes inside one tick (a write then the settings_updated refetch) give identical
// mtimes. An in-place body edit is still undetected; it changes only the description.
func dirSignature(roots []kiroRoot) string {
	var b strings.Builder
	for _, root := range roots {
		for _, sub := range []string{"", "steering", skillsDir, "agents", "specs", "hooks", "prompts"} {
			p := root.fsPath
			if sub != "" {
				p = filepath.Join(p, sub)
			}
			info, err := os.Stat(p)
			if err != nil {
				b.WriteString(p + "=-;")
				continue
			}
			b.WriteString(p + "=" + info.ModTime().UTC().Format("20060102150405.000000000"))
			// Only skills builds a row through a linked directory; the markdown walks never descend one.
			writeEntryNames(&b, p, sigDepth[sub], sub == skillsDir)
			b.WriteString(";")
		}
	}
	return b.String()
}

// writeEntryNames appends dir's entry names, descending into subdirectories while depth allows,
// and through a link only with followLinks.
func writeEntryNames(b *strings.Builder, dir string, depth int, followLinks bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		b.WriteString("#?")
	}
	for _, e := range entries {
		b.WriteString("#" + e.Name())
		if depth > 1 && (e.IsDir() || followLinks && e.Type()&fs.ModeSymlink != 0) {
			b.WriteString("{")
			writeEntryNames(b, filepath.Join(dir, e.Name()), depth-1, followLinks)
			b.WriteString("}")
		}
	}
}

// scanKiroRoots scans every root in category order, applying the total cap. A
// root left unread because the cap was already reached counts as a cut, whatever
// it would have held.
func scanKiroRoots(ctx context.Context, roots []kiroRoot, sensitive filebrowse.Sensitive) KiroDocsResponse {
	var sc docScan
	for _, root := range roots {
		if ctx.Err() != nil || len(sc.docs) >= maxDocsTotal {
			sc.truncated = true
			break
		}
		sc.absorb(scanKiroDocsFS(ctx, kiroDirFS(root.fsPath), root.prefix,
			newRootGuard(root.fsPath, root.prefix, sensitive)))
	}
	if len(sc.docs) > maxDocsTotal {
		sc.docs = sc.docs[:maxDocsTotal]
		sc.truncated = true
	}
	return KiroDocsResponse{Docs: sc.docs, Truncated: sc.truncated}
}

// scanKiroDocsFS scans one `.kiro` tree over fs.FS, so it is unit-testable with
// fstest.MapFS. Category order here is the page's fixed tab order.
func scanKiroDocsFS(ctx context.Context, root fs.FS, prefix string, guard pathGuard) docScan {
	var sc docScan
	for _, scan := range []func(context.Context, fs.FS, string, pathGuard) docScan{
		scanDocsSteering, scanDocsSkills, scanDocsAgents, scanDocsSpecs, scanDocsHooks, scanDocsPrompts,
	} {
		if ctx.Err() != nil {
			sc.truncated = true
			return sc
		}
		sc.absorb(scan(ctx, root, prefix, guard))
	}
	return sc
}

// scanDocsSteering walks `steering/` recursively for markdown.
func scanDocsSteering(ctx context.Context, root fs.FS, prefix string, guard pathGuard) docScan {
	return walkMarkdown(ctx, root, "steering", catSteering, guard, func(rel string, fm steering.FrontMatter, data []byte, v docVerdict) KiroDoc {
		return KiroDoc{
			Category:         catSteering,
			Name:             docLabel(&fm, data, rel),
			Path:             prefix + "/steering/" + rel,
			Group:            path.Dir(rel), // "." for the flat common case
			Description:      fm.Description,
			Inclusion:        fm.Inclusion,
			FileMatch:        fm.FileMatch,
			SteeringOverride: fm.SteeringOverride,
			DeleteProtected:  v.deleteProtected,
		}
	})
}

// scanDocsSkills emits one row per skill directory; other markdown under one is reference material.
func scanDocsSkills(ctx context.Context, root fs.FS, prefix string, guard pathGuard) docScan {
	entries, err := readGuardedDir(root, skillsDir, guard)
	if err != nil {
		return docScan{}
	}
	sc := docScan{docs: make([]KiroDoc, 0, len(entries))}
	skills := guard.verdict(skillsDir).resolved
	for _, e := range entries {
		if ctx.Err() != nil {
			sc.truncated = true
			return sc
		}
		// A linked directory too, as KAS loads one; the guard refuses a link out of the tree. A dot
		// directory is skipped, as steering's skill list skips it.
		if (!e.IsDir() && e.Type()&fs.ModeSymlink == 0) || strings.HasPrefix(e.Name(), ".") ||
			strings.ContainsRune(e.Name(), 0) {
			continue
		}
		doc, ok := skillRow(root, prefix, e.Name(), skills, guard)
		if ok && !sc.add(&doc) {
			return sc
		}
	}
	return sc
}

// skillRow builds the row for skills/<name>, or reports there is no skill there. skills is
// skills/' resolved path, empty when the guard cannot tell.
func skillRow(root fs.FS, prefix, name, skills string, guard pathGuard) (KiroDoc, bool) {
	dir := "skills/" + name
	v := guard.verdict(dir)
	// A link back to skills/ or a directory holding it would list that tree as a skill.
	if !v.allowed || pathinside.Root(v.resolved).Contains(skills) {
		return KiroDoc{}, false
	}
	data, _, rErr := readGuardedFS(root, dir+"/SKILL.md", guard)
	if rErr != nil && !holdsFiles(root, dir, guard) {
		return KiroDoc{}, false
	}
	fm := steering.Parse(data)
	// A DECLARED mode only, never steering.Parse's "always" default: KAS's skill schema declares
	// no `inclusion`, so the default badged every skill always-loaded. A declared `manual` or
	// `auto` does make a skill a slash command, so that is shown.
	inclusion := ""
	if fm.HasInclusion {
		inclusion = fm.Inclusion
	}
	return KiroDoc{
		Category:         catSkill,
		Name:             cmp.Or(fm.Name, name),
		Path:             prefix + "/" + dir + "/SKILL.md",
		Description:      fm.Description,
		Inclusion:        inclusion,
		SteeringOverride: fm.SteeringOverride,
		// The delete removes the directory, so a linked directory is what must not offer it.
		DeleteProtected: v.deleteProtected,
	}, true
}

// holdsFiles reports whether a manifest-less skill directory is a broken skill to repair or
// delete (it holds something) rather than nothing at all. KAS loads no skill without a readable
// SKILL.md, so an empty directory has nothing to show.
func holdsFiles(root fs.FS, dir string, guard pathGuard) bool {
	entries, err := readGuardedDir(root, dir, guard)
	return err == nil && len(entries) > 0
}

// scanDocsAgents emits one row per agent, de-duplicating the `.json`/`.md` pair
// and preferring the markdown (which is what carries the front-matter).
func scanDocsAgents(ctx context.Context, root fs.FS, prefix string, guard pathGuard) docScan {
	entries, err := readGuardedDir(root, "agents", guard)
	if err != nil {
		return docScan{}
	}
	entries = dropLinksToNonFiles(root, entries)
	agents := steering.DedupeAgentFiles(entries)
	linked := make(map[string]bool)
	for _, e := range entries {
		if e.Type()&fs.ModeSymlink != 0 {
			linked[e.Name()] = true
		}
	}
	sc := docScan{docs: make([]KiroDoc, 0, len(agents))}
	for _, a := range agents {
		if ctx.Err() != nil {
			sc.truncated = true
			return sc
		}
		base, file := a.Base, a.File
		data, verdict, rErr := readGuardedFS(root, "agents/"+file, guard)
		if rErr != nil {
			slog.Warn("kiro docs: read agent", "name", logsafe.Field(file), "error", logsafe.Field(rErr.Error()))
			data = nil
		}
		fm := steering.Parse(data)
		name := cmp.Or(fm.Name, base)
		kept := sc.add(&KiroDoc{
			Category:    catAgent,
			Name:        name,
			Path:        prefix + "/agents/" + file,
			Description: fm.Description,
			Model:       fm.Model,
			Tools:       fm.Tools,
			// The delete removes both spellings, so a linked twin withholds it too.
			DeleteProtected: verdict.deleteProtected || linked[agentTwin(file)],
		})
		if !kept {
			return sc
		}
	}
	return sc
}

// dropLinksToNonFiles drops an agents/ link that does not resolve to a regular file, before
// DedupeAgentFiles picks a spelling, so it cannot shadow a readable twin.
func dropLinksToNonFiles(root fs.FS, entries []fs.DirEntry) []fs.DirEntry {
	return slices.DeleteFunc(entries, func(e fs.DirEntry) bool {
		if e.Type()&fs.ModeSymlink == 0 {
			return false
		}
		fi, err := fs.Stat(root, "agents/"+e.Name())
		return err != nil || !fi.Mode().IsRegular()
	})
}

// scanDocsSpecs walks `specs/` and groups each document under its feature directory. Specs
// carry no front-matter (the label is the H1) and no fixed document trio, so a feature is a
// group with arbitrary children, ordered requirements → design → tasks → lexical.
func scanDocsSpecs(ctx context.Context, root fs.FS, prefix string, guard pathGuard) docScan {
	sc := walkMarkdown(ctx, root, "specs", catSpec, guard, func(rel string, fm steering.FrontMatter, data []byte, v docVerdict) KiroDoc {
		group := path.Dir(rel)
		if group == "." {
			group = "" // a doc loose in specs/ has no feature
		}
		return KiroDoc{
			Category:        catSpec,
			Name:            docLabel(&fm, data, rel),
			Path:            prefix + "/specs/" + rel,
			Group:           group,
			Description:     fm.Description,
			DeleteProtected: v.deleteProtected,
		}
	})
	sortSpecDocs(sc.docs)
	return sc
}

// specFileRank orders the conventional spec documents ahead of anything else,
// so a feature group reads requirements → design → tasks → whatever it added.
// It is spec.Rank so /docs/specs and GET /api/specs/{dir} list one order.
func specFileRank(p string) int {
	return spec.Rank(p)
}

// sortSpecDocs groups by feature, then applies specFileRank, then lexical.
func sortSpecDocs(docs []KiroDoc) {
	slices.SortStableFunc(docs, func(a, b KiroDoc) int {
		return cmp.Or(
			cmp.Compare(a.Group, b.Group),
			cmp.Compare(specFileRank(a.Path), specFileRank(b.Path)),
			cmp.Compare(a.Path, b.Path),
		)
	})
}

// scanDocsHooks emits one row per hook, expanding a v1 envelope. steering.ParseHooks keeps
// the fields sanitized (hook files are workspace content).
func scanDocsHooks(ctx context.Context, root fs.FS, prefix string, guard pathGuard) docScan {
	entries, err := readGuardedDir(root, "hooks", guard)
	if err != nil {
		return docScan{}
	}
	var sc docScan
	for _, e := range entries {
		if ctx.Err() != nil {
			sc.truncated = true
			return sc
		}
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.ContainsRune(e.Name(), 0) {
			continue
		}
		data, verdict, rErr := readGuardedFS(root, "hooks/"+e.Name(), guard)
		if rErr != nil {
			slog.Warn("kiro docs: read hook", "name", logsafe.Field(e.Name()), "error", logsafe.Field(rErr.Error()))
			continue
		}
		rows := hookRows(data, prefix, e.Name(), verdict.deleteProtected)
		for i := range rows {
			if !sc.add(&rows[i]) {
				return sc
			}
		}
	}
	return sc
}

// scanDocsPrompts reads `prompts/*.md` at the top level only, which is all KAS
// reads; the row name is the basename, the slash command a user types.
func scanDocsPrompts(ctx context.Context, root fs.FS, prefix string, guard pathGuard) docScan {
	entries, err := readGuardedDir(root, "prompts", guard)
	if err != nil {
		return docScan{}
	}
	var sc docScan
	for _, e := range entries {
		if ctx.Err() != nil {
			sc.truncated = true
			return sc
		}
		if e.IsDir() || !isMarkdownEntry(e) {
			continue
		}
		data, verdict, rErr := readGuardedFS(root, "prompts/"+e.Name(), guard)
		if rErr != nil {
			slog.Warn("kiro docs: read prompt", "name", e.Name(), "error", rErr)
			continue
		}
		fm := steering.Parse(data)
		doc := KiroDoc{
			Category:        catPrompt,
			Name:            strings.TrimSuffix(e.Name(), ".md"),
			Path:            prefix + "/prompts/" + e.Name(),
			Description:     cmp.Or(fm.Description, steering.FirstHeading(data)),
			DeleteProtected: verdict.deleteProtected,
		}
		if !sc.add(&doc) {
			return sc
		}
	}
	return sc
}

// hookRows expands one v1 hook envelope into its rows. A file may carry several
// hooks, and each is its own row.
func hookRows(data []byte, prefix, file string, deleteProtected bool) []KiroDoc {
	parsed := steering.ParseHooks(data)
	out := make([]KiroDoc, 0, len(parsed))
	for _, h := range parsed {
		name := cmp.Or(h.Name, strings.TrimSuffix(file, ".json"))
		out = append(out, KiroDoc{
			Category:        catHook,
			Name:            name,
			Path:            prefix + "/hooks/" + file,
			Group:           file,
			Trigger:         h.Trigger,
			Action:          h.Command,
			DeleteProtected: deleteProtected,
		})
	}
	return out
}

func walkMarkdown(
	ctx context.Context,
	root fs.FS,
	sub, category string,
	guard pathGuard,
	mk func(rel string, fm steering.FrontMatter, data []byte, v docVerdict) KiroDoc,
) docScan {
	w := &mdWalker{ctx: ctx, root: root, sub: sub, category: category, guard: guard, mk: mk}
	err := fs.WalkDir(root, sub, w.step)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("kiro docs: walk", "category", category, "error", logsafe.Field(err.Error()))
	}
	return w.sc
}

// mdWalker carries the markdown walk's accounting so the visitor is a named method.
type mdWalker struct {
	ctx      context.Context
	root     fs.FS
	mk       func(rel string, fm steering.FrontMatter, data []byte, v docVerdict) KiroDoc
	guard    pathGuard
	sub      string
	category string
	sc       docScan
}

// A single unreadable directory is skipped rather than aborting the category: one bad permission
// must not empty the page.
func (w *mdWalker) step(p string, d fs.DirEntry, walkErr error) error {
	if walkErr != nil {
		return nil //nolint:nilerr // deliberate: skip this entry and keep walking
	}
	if w.ctx.Err() != nil {
		w.sc.truncated = true
		return fs.SkipAll
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(p, w.sub), "/")
	if d.IsDir() {
		if strings.Count(rel, "/")+1 > maxSpecWalkDepth {
			return fs.SkipDir
		}
		// Refused at the DIRECTORY, so a symlinked `steering/` cannot cause a walk of its target.
		if !w.guard.allows(p) {
			return fs.SkipDir
		}
		return nil
	}
	if !isMarkdownEntry(d) {
		return nil
	}
	data, verdict, err := readGuardedFS(w.root, p, w.guard)
	if err != nil {
		slog.Warn("kiro docs: read", "category", w.category, "path", logsafe.Field(p), "error", logsafe.Field(err.Error()))
		return nil
	}
	doc := w.mk(rel, steering.Parse(data), data, verdict)
	if !w.sc.add(&doc) {
		return fs.SkipAll
	}
	return nil
}

// isMarkdownEntry reports whether a walked entry is a document this scan reads:
// a `.md` file, not a dotfile, with no NUL in its name.
func isMarkdownEntry(d fs.DirEntry) bool {
	name := d.Name()
	return strings.HasSuffix(name, ".md") &&
		!strings.HasPrefix(name, ".") &&
		!strings.ContainsRune(name, 0)
}

// docLabel implements the universal fallback chain: front-matter `name`, else the first H1,
// else the basename without its extension.
func docLabel(fm *steering.FrontMatter, data []byte, rel string) string {
	if fm.Name != "" {
		return fm.Name
	}
	if h := steering.FirstHeading(data); h != "" {
		return h
	}
	return strings.TrimSuffix(path.Base(rel), ".md")
}
