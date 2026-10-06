// Package filebrowse serves the browser's file surface: browsing, reading,
// editing, uploading, and downloading. The browsable surface is
// an ALLOW-LIST of granted roots (the /workspace, /config and uploads
// mounts by default, plus any MAROTTE_BROWSE_ROOTS grants), each kernel-confined
// through its own os.Root; everything outside the grants is denied by
// default. A sensitive-path list additionally blocks the credential
// and state files living inside /config. The URL namespace is the
// container-absolute path ("/workspace/...", "/config/..."), and "/"
// lists the granted mounts.
//
// Defense layers are documented on paths.go. The handler is the
// gatekeeper for everything the user can do from the browser; every
// route that takes a path runs it through resolveOrForbid so that the
// symlink-aware resolvePath is applied uniformly. Operations that
// touch a DIRECTORY (delete, upload target) additionally consult
// protectedDir; operations with a secondary path argument (rename's
// name, copy/move's dest) re-run the full guard on the new path. The
// two RECURSIVE routes (the zip download and the content search)
// resolve their root once and then stay in that mount by
// construction, re-checking the sensitive-path list per entry —
// see search.go for why both halves are required.
package filebrowse

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"syscall"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
)

// MaxFileSize is the editor's read and write cap on /api/file. The spec
// endpoint reads through the same constant so the two doors cannot drift.
const MaxFileSize = 2 * 1024 * 1024

const (
	maxUploadSize = 256 * 1024 * 1024 // 256 MB per multipart upload
	binarySniffN  = 8192              // bytes of prefix checked for NUL
	// multipartMaxMemory is the in-RAM buffer ceiling
	// ParseMultipartForm uses before spilling parts to a tmpfile.
	// Small on purpose: the HTTP body cap is enforced by
	// MaxBytesReader; this value only decides when parts page to disk.
	// Matches the 1 MiB default net/http uses via `defaultMaxMemory`
	// and keeps concurrent uploads from stacking 256 MiB each in RAM.
	multipartMaxMemory = 1 * 1024 * 1024

	// respPath is the response-body key echoing the request path back
	// to the client (listing and read responses).
	respPath = "path"
)

// Handler serves /api/file/* and /api/files/*.
type Handler struct {
	mounts    []mount // sorted longest-dir-first (see openMounts)
	sensitive Sensitive
}

// New creates a file handler whose browsable surface is exactly rootDirs,
// minus what sensitive blocks inside them. Each granted directory gets its
// own os.Root (TOCTOU-free). A grant that cannot be opened is skipped with a
// warning; zero usable mounts is a hard error.
func New(sensitive Sensitive, rootDirs ...string) (*Handler, error) {
	mounts, errs := openMounts(rootDirs)
	for _, err := range errs {
		slog.Warn("filebrowse: skipping browse root", "error", err)
	}
	if len(mounts) == 0 {
		return nil, fmt.Errorf("filebrowse: no usable browse roots in %q", rootDirs)
	}
	return &Handler{mounts: mounts, sensitive: sensitive}, nil
}

// RegisterRoutes wires all /api/file* and /api/files* routes onto mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/file", h.handleFile)
	mux.HandleFunc("/api/file/download", h.handleDownload)
	mux.HandleFunc("/api/file/upload", h.handleUpload)
	mux.HandleFunc("/api/files", h.handleFiles)
	mux.HandleFunc("/api/files/action", h.handleFilesAction)
	mux.HandleFunc("/api/files/download", h.handleDownloadZip)
	mux.HandleFunc("/api/files/search", h.handleFilesSearch)
}

// errHandled signals that an action function has already written its
// error response (e.g. for a validation failure with a specific status
// code) and handleFilesAction should not double-write.
var errHandled = errors.New("handled")

// errNoSpaceLeft is the client message every 507 on this surface carries.
const errNoSpaceLeft = "not enough space left on the volume"

// isOutOfSpace reports whether err is a volume-full write failure, which the
// write paths answer with 507 rather than a generic 500 so the user is told
// what to fix rather than told it broke.
//
// EDQUOT as well as ENOSPC: a volume on a dataset with a quota reports EDQUOT
// when the quota is exhausted, where a genuinely full filesystem reports ENOSPC. TestIsOutOfSpace_MatchesThroughAtomicfileWrapping
// pins the wrapping errors.Is walks to reach either errno.
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
	l, ok := h.resolveOrForbid(w, reqPath)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		readFile(r.Context(), w, l, reqPath)
	case http.MethodPut:
		writeFile(w, r, l)
	default:
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodPut)
	}
}
