// Package filebrowse serves the browser's file surface: browsing, reading, editing, uploading and
// downloading. The browsable surface is an ALLOW-LIST of granted roots (/workspace, /config,
// uploads, plus MAROTTE_BROWSE_ROOTS grants and the WithLiveRoots list), each kernel-confined
// through its own os.Root; a sensitive-path list additionally blocks the credential and state
// files inside /config. URLs are container-absolute paths, and "/" lists the mounts.
// Every route that takes a path runs it through resolveOrForbid (paths.go documents the layers).
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
	"reflect"
	"syscall"

	"github.com/cplieger/go/pathinside"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/route"
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
)

// Handler serves /api/file/* and /api/files/*.
type Handler struct {
	owners      []pathOwner // registration order, so the first match is deterministic
	toolOutputs string      // AllowToolOutputs; "" grants nothing
	mounts      []mount     // sorted longest-dir-first (see sortMounts)
	liveRoots   func(ctx context.Context) []string
	sensitive   Sensitive
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

// routeMux is what a route registration needs: the server's registrar, or a test's mux. An alias, so
// RegisterRoutes has the server's routeHandler signature.
type routeMux = interface{ Bind(route.Binding) }

// PathQuery is the query of the routes that address one path.
type PathQuery struct {
	Path string `json:"path"`
}

// DownloadQuery is GET /api/file/download's query: the path, and the identity it was read under.
type DownloadQuery struct {
	Path   string `json:"path"`
	FileID string `json:"file_id,omitempty"`
}

// FilesSearchQuery is GET /api/files/search's query; Case and Ignored are "1" to match case and to
// search ignored files.
type FilesSearchQuery struct {
	Q       string `json:"q,omitempty"`
	Path    string `json:"path,omitempty"`
	Mode    string `json:"mode,omitempty"`
	Files   string `json:"files,omitempty"`
	Case    string `json:"case,omitempty"`
	Ignored string `json:"ignored,omitempty"`
}

// FileList is GET /api/files' body.
type FileList struct {
	Path     string      `json:"path"`
	Files    []FileEntry `json:"files"`
	Writable bool        `json:"writable"`
}

// UploadResult is POST /api/file/upload's body: the names written.
type UploadResult struct {
	Uploaded []string `json:"uploaded"`
	OK       bool     `json:"ok"`
}

// ZipRequest is POST /api/files/download's body.
type ZipRequest struct {
	Paths []string `json:"paths"`
}

// RegisterRoutes binds every /api/file* and /api/files* route.
func (h *Handler) RegisterRoutes(m routeMux) {
	path := reflect.TypeFor[PathQuery]()
	bind := func(p string, fn func(*Handler, http.ResponseWriter, *http.Request), ms ...route.MethodTypes) {
		m.Bind(route.Methods(p, h.scoped(fn), ms...))
	}
	bind("/api/file", (*Handler).handleFile,
		route.Typed[struct{}, FileRead](http.MethodGet).WithQuery(path),
		route.Typed[FileWrite, FileWriteResult](http.MethodPut).WithQuery(path))
	bind("/api/file/stat", (*Handler).handleStat, route.Typed[struct{}, FileStat](http.MethodGet).WithQuery(path))
	m.Bind(route.WithQuery[DownloadQuery](route.Raw(DownloadPath, h.scoped((*Handler).handleDownload))))
	// A multipart form (dir, files), which the endpoint table does not type; the reply is JSON.
	bind("/api/file/upload", (*Handler).handleUpload, route.Typed[struct{}, UploadResult](http.MethodPost))
	bind("/api/files", (*Handler).handleFiles, route.Typed[struct{}, FileList](http.MethodGet).WithQuery(path))
	bind("/api/files/action", (*Handler).handleFilesAction, route.Typed[FileAction, httpreply.OKBody](http.MethodPost))
	m.Bind(route.Binding{
		Pattern: "/api/files/download", Handler: h.scoped((*Handler).handleDownloadZip), Kind: route.KindRaw,
		Methods: []route.MethodTypes{{Method: http.MethodPost, Request: reflect.TypeFor[ZipRequest]()}},
	})
	bind("/api/files/search", (*Handler).handleFilesSearch,
		route.Typed[struct{}, FileSearchResult](http.MethodGet).WithQuery(reflect.TypeFor[FilesSearchQuery]()))
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
	case http.MethodGet, http.MethodHead:
		h.handleRead(w, r, reqPath)
	case http.MethodPut:
		l, ok := h.resolveOrForbid(w, reqPath)
		if !ok {
			return
		}
		writeFile(w, r, l, h.ownerFor(l))
	}
}
