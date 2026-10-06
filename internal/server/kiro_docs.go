// The document-oriented `.kiro` inventory behind GET /api/workspace/kiro-docs: one row per
// file with its front-matter. /api/workspace/kiro-config stays ENTITY-oriented because
// role-picker.ts depends on that shape. Each category names its own root, so markdown no
// category claims gets no row.

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

	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/spec"
	"github.com/cplieger/marotte/internal/steering"
	"github.com/cplieger/webhttp/v3"
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

// Per-category and overall bounds, set above any real corpus while still refusing an
// unbounded tree.
const (
	maxDocsPerCategory = 500
	maxDocsTotal       = 2000
	// maxSpecWalkDepth bounds the specs walk: `.kiro/specs/<feature>/<file>.md`
	// is two levels, so three allows one unexpected nesting level and no more.
	maxSpecWalkDepth = 3
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

	// ReadOnly says this row is not writable, so it renders without edit or delete. Nothing
	// sets it today: no writability source exists yet. omitempty, so read-only is only ever
	// asserted by the server, never produced by a field failing to arrive.
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

// absorb merges a category's or a root's scan into this one.
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
	webhttp.WriteJSON(w, res)
}

// collectKiroDocs returns the cached inventory, rescanning when the signature changed.
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
// subdirectory, the same walk as collectKiroConfig.
func (s *Server) kiroRoots() []kiroRoot {
	workBase := strings.TrimPrefix(s.workDir, "/")
	specRoots := spec.Roots(s.workDir)
	roots := make([]kiroRoot, 0, len(specRoots))
	for _, r := range specRoots {
		roots = append(roots, kiroRoot{fsPath: r.Dir, prefix: workBase + "/" + r.Rel})
	}
	return roots
}

// dirSignature builds a cache key from each root's category directories: the mtime AND the
// entry names. Names are load-bearing: Linux stamps mtimes from a coarse clock, so two
// changes inside one tick (a write then the settings_updated refetch) give identical mtimes.
// An in-place body edit is still undetected; it changes only the description.
func dirSignature(roots []kiroRoot) string {
	var b strings.Builder
	for _, root := range roots {
		for _, sub := range []string{"", "steering", "skills", "agents", "specs", "hooks", "prompts"} {
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
			entries, dirErr := os.ReadDir(p)
			if dirErr != nil {
				b.WriteString("#?")
			}
			for _, e := range entries {
				b.WriteString("#" + e.Name())
			}
			b.WriteString(";")
		}
	}
	return b.String()
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
		sc.absorb(scanKiroDocsFS(ctx, os.DirFS(root.fsPath), root.prefix,
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

// scanDocsSkills emits one row per skill MANIFEST (`skills/<name>/SKILL.md`); other markdown
// under a skill directory is reference material.
func scanDocsSkills(ctx context.Context, root fs.FS, prefix string, guard pathGuard) docScan {
	entries, err := readGuardedDir(root, "skills", guard)
	if err != nil {
		return docScan{}
	}
	sc := docScan{docs: make([]KiroDoc, 0, len(entries))}
	for _, e := range entries {
		if ctx.Err() != nil {
			sc.truncated = true
			return sc
		}
		if !e.IsDir() || strings.ContainsRune(e.Name(), 0) {
			continue
		}
		rel := e.Name() + "/SKILL.md"
		data, verdict, rErr := readGuardedFS(root, "skills/"+rel, guard)
		if rErr != nil {
			// A directory with no manifest is still a skill, keeping the editable default.
			data = nil
		}
		fm := steering.Parse(data)
		name := cmp.Or(fm.Name, e.Name())
		// A DECLARED mode only, never steering.Parse's "always" default: KAS's skill schema declares
		// no `inclusion`, so the default badged every skill always-loaded. A declared `manual` or
		// `auto` does make a skill a slash command, so that is shown.
		inclusion := ""
		if fm.HasInclusion {
			inclusion = fm.Inclusion
		}
		kept := sc.add(&KiroDoc{
			Category:         catSkill,
			Name:             name,
			Path:             prefix + "/skills/" + rel,
			Description:      fm.Description,
			Inclusion:        inclusion,
			SteeringOverride: fm.SteeringOverride,
			DeleteProtected:  verdict.deleteProtected,
		})
		if !kept {
			return sc
		}
	}
	return sc
}

// scanDocsAgents emits one row per agent, de-duplicating the `.json`/`.md` pair
// and preferring the markdown (which is what carries the front-matter).
func scanDocsAgents(ctx context.Context, root fs.FS, prefix string, guard pathGuard) docScan {
	entries, err := readGuardedDir(root, "agents", guard)
	if err != nil {
		return docScan{}
	}
	agents := steering.DedupeAgentFiles(entries)
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
			Category:        catAgent,
			Name:            name,
			Path:            prefix + "/agents/" + file,
			Description:     fm.Description,
			Model:           fm.Model,
			Tools:           fm.Tools,
			DeleteProtected: verdict.deleteProtected,
		})
		if !kept {
			return sc
		}
	}
	return sc
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

// walkMarkdown walks `sub` under root for `.md` files, bounded in depth and count.
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

// step is fs.WalkDir's visitor. A single unreadable directory is skipped rather
// than aborting the category: one bad permission must not empty the page.
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
