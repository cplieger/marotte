package filebrowse

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/webhttp/v3"
)

const (
	errFileTooLarge = "file too large"
	errReadFailed   = "read failed"
)

// --- /api/file (GET read) + /api/file/download ---

func readFile(ctx context.Context, w http.ResponseWriter, l loc, reqPath string) {
	// ReadBoundedInRoot stats the OPEN handle, requires a regular file, and opens non-blocking so a
	// FIFO cannot wedge the handler.
	data, err := atomicfile.ReadBoundedInRoot(ctx, l.m.root, l.rel(), MaxFileSize)
	if err != nil {
		readFileError(w, l.abs, err)
		return
	}
	writeRead(w, data, reqPath, false)
}

// readToolOutput serves a KAS tool output from the descriptor openToolOutput
// pinned, marked read-only.
func readToolOutput(ctx context.Context, w http.ResponseWriter, out toolOutput, reqPath string) {
	data, err := atomicfile.ReadBoundedFile(ctx, out.f, MaxFileSize)
	if err != nil {
		readFileError(w, out.abs, err)
		return
	}
	writeRead(w, data, reqPath, true)
}

// writeRead answers a read; readOnly marks a file the editor must not offer to
// edit (a KAS tool output).
func writeRead(w http.ResponseWriter, data []byte, reqPath string, readOnly bool) {
	if looksBinary(data) {
		webhttp.WriteJSONStatus(w, http.StatusUnsupportedMediaType,
			httpreply.ErrorJSON("binary file"))
		return
	}
	// content_hash comes back on save as expected_hash, turning a blind overwrite into a detected
	// conflict. A digest, not the mtime: two writes inside one coarse clock tick share an mtime.
	resp := map[string]any{
		"content":      string(data),
		"content_hash": contentHash(data),
		respPath:       reqPath,
	}
	if readOnly {
		resp["read_only"] = true
	}
	webhttp.WriteJSON(w, resp)
}

// contentHash is the stale-write guard's comparison key: a hex SHA-256 of
// the served bytes.
func contentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// looksBinary reports whether the file's first binarySniffN bytes contain a
// NUL. bytes.IndexByte is used directly to hit the runtime's SIMD
// implementation on amd64/arm64.
func looksBinary(data []byte) bool {
	if len(data) > binarySniffN {
		data = data[:binarySniffN]
	}
	return bytes.IndexByte(data, 0) >= 0
}

// --- /api/file/download (GET binary download with Content-Disposition) ---

func (h *Handler) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" {
		httpreply.BadRequest(w, "missing path")
		return
	}
	if out, granted, err := h.openToolOutput(reqPath); granted {
		if err != nil {
			readFileError(w, out.abs, err)
			return
		}
		defer out.f.Close()
		serveDownload(w, r, out.f, out.info, out.abs)
		return
	}
	l, ok := h.resolveOrForbid(w, reqPath)
	if !ok {
		return
	}
	// Through the mount's os.Root, not http.ServeFile, which re-opens by path and follows symlinks
	// at serve time.
	f, info, err := atomicfile.OpenRegularInRoot(l.m.root, l.rel())
	if err != nil {
		var nr *atomicfile.NotRegularError
		switch {
		case os.IsNotExist(err):
			httpreply.NotFound(w, "not found")
		case errors.As(err, &nr) && nr.Mode.IsDir():
			httpreply.BadRequest(w, "cannot download directory")
		case errors.Is(err, atomicfile.ErrNotRegular):
			httpreply.BadRequest(w, "not a regular file")
		default:
			slog.Warn("filebrowse: download open failed", "path", logsafe.Field(l.abs), "error", logsafe.Field(err.Error()))
			webhttp.WriteJSONStatus(w, http.StatusInternalServerError,
				httpreply.ErrorJSON(errReadFailed))
		}
		return
	}
	defer f.Close()
	serveDownload(w, r, f, info, l.abs)
}

// serveDownload streams an already-open, confined regular file as an attachment.
func serveDownload(w http.ResponseWriter, r *http.Request, f *os.File, info fs.FileInfo, abs string) {
	name := filepath.Base(abs)
	ct := cmp.Or(mime.TypeByExtension(filepath.Ext(name)), "application/octet-stream")
	w.Header().Set("Content-Type", ct)
	// `attachment` is a SECURITY CONTROL: an SVG served inline would run script as a document, and
	// CSP's frame-src falls back to `default-src 'self'`. Inline would make the download anchor
	// stored XSS (TestHandleDownload_SVGIsAttachment).
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, name))
	// Revalidate every impression: a validator with no explicit lifetime gets heuristic caching,
	// which serves a re-shot screenshot under the same filename stale.
	w.Header().Set("Cache-Control", "no-cache")
	// A strong validator: Last-Modified truncates to a second, so size plus mtime in nanoseconds
	// narrows the window to one clock tick at no I/O cost. The quoting is load-bearing: unquoted,
	// ServeContent falls back to the mtime.
	w.Header().Set("ETag", strconv.Quote(fmt.Sprintf("%x-%x", info.Size(), info.ModTime().UnixNano())))
	// Debug: the resolved path can name a workspace file. ServeContent serves the confined fd,
	// Range and conditionals included.
	slog.Debug("filebrowse: download", "path", logsafe.Field(abs), "size", info.Size())
	// Deliberately UNCAPPED: a size guard here runs before ServeContent reads
	// the Range header, so it answered 413 to the cheapest request there is.
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// readFileError maps a confined-read failure onto the HTTP status the
// client needs. atomicfile's sentinels distinguish the cases without
// inspecting error text; a cancelled request is silent since the client
// is gone.
func readFileError(w http.ResponseWriter, abs string, err error) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return
	case errors.Is(err, fs.ErrNotExist):
		httpreply.NotFound(w, "not found")
	case errors.Is(err, atomicfile.ErrNotRegular):
		httpreply.BadRequest(w, "not a regular file")
	case errors.Is(err, atomicfile.ErrFileTooLarge):
		webhttp.WriteJSONStatus(w, http.StatusRequestEntityTooLarge, httpreply.ErrorJSON(errFileTooLarge))
	default:
		slog.Warn("filebrowse: read failed", "path", logsafe.Field(abs), "error", logsafe.Field(err.Error()))
		webhttp.WriteJSONStatus(w, http.StatusInternalServerError, httpreply.ErrorJSON(errReadFailed))
	}
}
