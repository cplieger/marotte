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
	saveHooks   []savePathHook // registration order, so the first match is deterministic
	toolOutputs string         // AllowToolOutputs; "" grants nothing
	mounts      []mount        // sorted longest-dir-first (see openMounts)
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

type savePathHook struct {
	hook SaveHook
	path string
}

// WithSaveHook runs hook on every save whose target resolves to wherever path, an absolute path,
// points at the time of that save. A later hook for the same cleaned path replaces this one; of
// two spellings that resolve to one file, the one registered first runs.
func WithSaveHook(path string, hook SaveHook) Option {
	return func(h *Handler) {
		path = filepath.Clean(path)
		for i := range h.saveHooks {
			if h.saveHooks[i].path == path {
				h.saveHooks[i].hook = hook
				return
			}
		}
		h.saveHooks = append(h.saveHooks, savePathHook{path: path, hook: hook})
	}
}

// saveHookFor resolves each hook's path per save, so a symlink swapped in after New cannot
// route a save around its hook.
func (h *Handler) saveHookFor(l loc) SaveHook {
	for _, r := range h.saveHooks {
		if resolved, err := resolveRealPath(r.path); err == nil && resolved == l.abs {
			return r.hook
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
	mux.HandleFunc("/api/file/stat", h.handleStat)
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
		writeFile(w, r, l, h.saveHookFor(l))
	default:
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodPut)
	}
}
