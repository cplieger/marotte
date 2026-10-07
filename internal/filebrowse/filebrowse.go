// Package filebrowse serves the browser's file surface: browsing, reading, editing, uploading and
// downloading. The browsable surface is an ALLOW-LIST of granted roots (/workspace, /config,
// uploads, plus MAROTTE_BROWSE_ROOTS grants), each kernel-confined through its own os.Root; a
// sensitive-path list additionally blocks the credential and state files inside /config. URLs are
// container-absolute paths, and "/" lists the mounts.
// Every route that takes a path runs it through resolveOrForbid (paths.go documents the layers).
// Directory operations also consult protectedDir, and a secondary path (rename's name, copy/move's
// dest) re-runs the full guard. The recursive routes (zip download, search) resolve their root once
// and re-check the sensitive list per entry.
package filebrowse

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
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
	// multipartMaxMemory is the in-RAM buffer ParseMultipartForm uses before spilling parts to a
	// tmpfile, net/http's 1 MiB default, so concurrent uploads do not stack in RAM; the body cap is
	// MaxBytesReader's.
	multipartMaxMemory = 1 * 1024 * 1024

	// respPath is the response-body key echoing the request path back
	// to the client (listing and read responses).
	respPath = "path"
)

// Handler serves /api/file/* and /api/files/*.
type Handler struct {
	saveHooks   map[string]SaveHook // keyed by the path as configured, resolved per save
	toolOutputs string              // AllowToolOutputs; "" grants nothing
	mounts      []mount             // sorted longest-dir-first (see openMounts)
	sensitive   Sensitive
}

// Option is an argument to New, which applies options in argument order after the browse
// roots open, so where two set the same thing the later one wins.
type Option func(*Handler)

// SaveHook governs editor saves (PUT /api/file) of one file. Either func may be nil.
type SaveHook struct {
	// Check runs before anything is written; a non-nil error refuses the save with a 400 whose
	// message is the error's text, so it must read as an answer to the person saving.
	Check func(content []byte) error
	// Saved runs after the content is on disk, on the request goroutine, before the response.
	Saved func()
}

// WithSaveHook runs hook on every save whose target resolves to wherever path, an absolute path,
// points at the time of that save. A later hook for the same cleaned path replaces this one.
func WithSaveHook(path string, hook SaveHook) Option {
	return func(h *Handler) {
		if h.saveHooks == nil {
			h.saveHooks = make(map[string]SaveHook)
		}
		h.saveHooks[filepath.Clean(path)] = hook
	}
}

// saveHookFor resolves each hook's path per save, so a symlink swapped in after New cannot
// route a save around its hook.
func (h *Handler) saveHookFor(l loc) SaveHook {
	for path, hook := range h.saveHooks {
		if resolved, err := resolveRealPath(path); err == nil && resolved == l.abs {
			return hook
		}
	}
	return SaveHook{}
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
	if r.Method == http.MethodGet {
		if out, granted, err := h.openToolOutput(reqPath); granted {
			if err != nil {
				readFileError(w, out.abs, err)
				return
			}
			defer out.f.Close()
			readToolOutput(r.Context(), w, out, reqPath)
			return
		}
	}
	l, ok := h.resolveOrForbid(w, reqPath)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		readFile(r.Context(), w, l, reqPath)
	case http.MethodPut:
		writeFile(w, r, l, h.saveHookFor(l))
	default:
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodPut)
	}
}
