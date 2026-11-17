// Package filebrowse serves the browser's file surface: browsing, reading, editing, uploading and
// downloading. The browsable surface is an ALLOW-LIST of granted roots (/workspace, /config,
// uploads, plus MAROTTE_BROWSE_ROOTS grants and the WithLiveRoots list), each kernel-confined
// through its own os.Root; a sensitive-path list additionally blocks the credential and state
// files inside /config. URLs are container-absolute paths, and "/" lists the mounts.
// Every route that takes a path runs it through resolveOrForbid, or resolveEntry for an action on
// the entry itself (paths.go documents the layers).
// Directory operations also consult protectedDir, and a secondary path (rename's name, copy/move's
// dest) re-runs the full guard. The recursive routes (zip download, search) resolve their root once
// and re-check the sensitive list per entry.
package filebrowse

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"syscall"

	"github.com/cplieger/go/pathinside"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
)

// WholeFileMax is the largest file the viewer reads, identifies and saves. A larger file
// has no identity and no viewer; it is only downloaded.
const WholeFileMax = 2 << 20

const (
	maxUploadSize = 256 * 1024 * 1024 // 256 MB per multipart upload
	binarySniffN  = 8192              // bytes of prefix checked for NUL
	// multipartMaxMemory is the in-RAM buffer ParseMultipartForm uses before spilling parts to a
	// tmpfile, net/http's 1 MiB default, so concurrent uploads do not stack in RAM; the body cap is
	// MaxBytesReader's.
	multipartMaxMemory = 1 * 1024 * 1024

	respPath = "path"
)

// Handler serves /api/file/* and /api/files/*.
type Handler struct {
	owners      []pathOwner // registration order, so the first match is deterministic
	toolOutputs string      // AllowToolOutputs; "" grants nothing
	mounts      []mount     // sorted longest-dir-first (see sortMounts)
	liveRoots   func(ctx context.Context) []string
	notify      func(ctx context.Context, ch Change)
	inEditor    func(path string) bool
	sensitive   Sensitive
}

// Change is one file mutation the browser made: every path it created, wrote, removed or renamed, each in the
// request's own spelling made absolute and cleaned. From and To are set by a rename only.
type Change struct {
	From, To string
	Paths    []string
}

// WithChangeNotify has every successful mutation (a file action, a save, an upload) call fn before its answer is
// written, so whatever fn changes is in place when the client reads the answer. fn runs detached from the
// request's cancellation: the change has landed, so a client that walks away must not stop its surfaces moving.
func WithChangeNotify(fn func(ctx context.Context, ch Change)) Option {
	return func(h *Handler) { h.notify = fn }
}

// WithOpenInEditor has a rename refuse 409 when fn reports an editor tab at or under its destination. fn takes
// the destination in Change's spelling; command.Membership.EditorTabAt owns why.
func WithOpenInEditor(fn func(path string) bool) Option {
	return func(h *Handler) { h.inEditor = fn }
}

func (h *Handler) changed(ctx context.Context, ch Change) {
	if h.notify != nil {
		h.notify(durable.Context(ctx), ch)
	}
}

// cleanReqPath is a request path in the spelling a Change reports.
func cleanReqPath(reqPath string) string {
	return filepath.Clean("/" + reqPath)
}

// Option is an argument to New, which applies options in argument order after the browse
// roots open, so where two set the same thing the later one wins.
type Option func(*Handler)

// Owner is the one writer of a file the browser serves. Replace writes content as the whole file
// under the owner's lock when check accepts the bytes then on disk (nil when absent or not a
// regular file, non-nil and possibly empty for a regular file; a nil check always accepts), and
// answers the bytes it wrote once they are on disk: the save's answer ends only when Replace
// returns, so a slow after-write effect goes to the owner's own worker. A file there whose bytes
// the owner cannot read refuses a checked save with the read's error, unseen by check. A
// SaveRefusalError answers 400 with its text; check's own error answers the stale 409.
type Owner interface {
	Replace(ctx context.Context, content []byte, check func(current []byte) error) (written []byte, err error)
}

// SaveRefusalError is an owner's refusal of the content itself, worded for the person saving.
type SaveRefusalError struct{ Err error }

func (r SaveRefusalError) Error() string { return r.Err.Error() }

func (r SaveRefusalError) Unwrap() error { return r.Err }

type pathOwner struct {
	owner Owner
	path  string
}

// WithOwner routes every save whose target resolves to wherever path, an absolute path, points at
// the time of that save through o, and refuses the file actions that would create, delete or
// rename it. A later owner for the same cleaned path replaces this one; of two spellings that
// resolve to one file, the one registered first owns it.
func WithOwner(path string, o Owner) Option {
	return func(h *Handler) {
		path = filepath.Clean(path)
		for i := range h.owners {
			if h.owners[i].path == path {
				h.owners[i].owner = o
				return
			}
		}
		h.owners = append(h.owners, pathOwner{path: path, owner: o})
	}
}

// ownerFor resolves each owned path per request, so a symlink swapped in after New cannot route a
// save around its owner.
func (h *Handler) ownerFor(l loc) Owner {
	for _, r := range h.owners {
		if resolved, err := resolveRealPath(r.path); err == nil && resolved == l.abs {
			return r.owner
		}
	}
	return nil
}

// ownsEntry reports whether abs is an owned file or the entry an owned path names, which differ
// when that path is a link: renaming or removing either takes the file from its owner.
func (h *Handler) ownsEntry(abs string) bool {
	for _, r := range h.owners {
		if resolved, err := resolveRealPath(r.path); err == nil && resolved == abs {
			return true
		}
		if parent, err := resolveRealPath(filepath.Dir(r.path)); err == nil && filepath.Join(parent, filepath.Base(r.path)) == abs {
			return true
		}
	}
	return false
}

// ownedAtOrAbove answers the owned path that is abs or one of its ancestors, or "": creating
// anything below an absent owned file puts a directory at its path.
func (h *Handler) ownedAtOrAbove(abs string) string {
	for _, r := range h.owners {
		if resolved, err := resolveRealPath(r.path); err == nil && pathinside.Root(resolved).Contains(abs) {
			return resolved
		}
	}
	return ""
}

// New creates a file handler whose browsable surface is exactly rootDirs,
// minus what sensitive blocks inside them. Each granted directory gets its
// own os.Root (TOCTOU-free). A grant that cannot be opened is skipped with a
// warning; zero usable mounts is a hard error.
func New(sensitive Sensitive, rootDirs []string, opts ...Option) (*Handler, error) {
	mounts, errs := openMounts(rootDirs)
	for _, err := range errs {
		slog.Warn("filebrowse: skipping browse root", "error", err)
	}
	if len(mounts) == 0 {
		return nil, fmt.Errorf("filebrowse: no usable browse roots in %q", rootDirs)
	}
	h := &Handler{mounts: mounts, sensitive: sensitive}
	for _, opt := range opts {
		opt(h)
	}
	return h, nil
}

// DownloadPath is the file download route, answered by http.ServeContent with byte ranges.
const DownloadPath = "/api/file/download"

// RegisterRoutes wires all /api/file* and /api/files* routes onto mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/file", h.scoped((*Handler).handleFile))
	mux.HandleFunc("/api/file/stat", h.scoped((*Handler).handleStat))
	mux.HandleFunc(DownloadPath, h.scoped((*Handler).handleDownload))
	mux.HandleFunc("/api/file/upload", h.scoped((*Handler).handleUpload))
	mux.HandleFunc("/api/files", h.scoped((*Handler).handleFiles))
	mux.HandleFunc("/api/files/action", h.scoped((*Handler).handleFilesAction))
	mux.HandleFunc("/api/files/download", h.scoped((*Handler).handleDownloadZip))
	mux.HandleFunc("/api/files/search", h.scoped((*Handler).handleFilesSearch))
}

// errHandled signals that an action function has already written its
// error response (e.g. for a validation failure with a specific status
// code) and handleFilesAction should not double-write.
var errHandled = errors.New("handled")

const errNoSpaceLeft = "not enough space left on the volume"

// isOutOfSpace reports whether err is a volume-full write failure, answered with 507 rather than
// 500. EDQUOT as well as ENOSPC: a quota-limited dataset reports EDQUOT.
// TestIsOutOfSpace_MatchesThroughAtomicfileWrapping pins the wrapping.
func isOutOfSpace(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT)
}

// resolveOrForbid is the common path-resolve prelude: returns the resolved
// location or writes a 403 and returns (loc{}, false).
func (h *Handler) resolveOrForbid(w http.ResponseWriter, reqPath string) (loc, bool) {
	l, err := h.resolvePath(reqPath)
	if err != nil {
		slog.Warn("filebrowse: path rejected",
			"path", logsafe.Field(reqPath), "reason", logsafe.Field(err.Error()))
		httpreply.Forbidden(w, err.Error())
		return loc{}, false
	}
	return l, true
}

// --- /api/file (GET read, PUT write) ---

func (h *Handler) handleFile(w http.ResponseWriter, r *http.Request) {
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" {
		httpreply.BadRequest(w, "missing path")
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.handleRead(w, r, reqPath)
	case http.MethodPut:
		l, ok := h.resolveOrForbid(w, reqPath)
		if !ok {
			return
		}
		writeFile(w, r, l, h.ownerFor(l), func() { h.changed(r.Context(), Change{Paths: []string{cleanReqPath(reqPath)}}) })
	default:
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodPut)
	}
}
