package filebrowse

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/webhttp/v3"
)

const errReadFailed = "read failed"

// tooLargeSentence is the viewer's whole answer for a file over WholeFileMax.
const tooLargeSentence = "File is too large to display. Download it to view."

// looksBinary reports whether the file's first binarySniffN bytes contain a
// NUL. bytes.IndexByte is used directly to hit the runtime's SIMD
// implementation on amd64/arm64.
func looksBinary(data []byte) bool {
	if len(data) > binarySniffN {
		data = data[:binarySniffN]
	}
	return bytes.IndexByte(data, 0) >= 0
}

func modifiedOf(info fs.FileInfo) string {
	return info.ModTime().UTC().Format(time.RFC3339Nano)
}

// --- GET /api/file: the whole read the viewer adopts ---

func (h *Handler) handleRead(w http.ResponseWriter, r *http.Request, reqPath string) {
	rd, ok := h.openForRead(w, reqPath)
	if !ok {
		return
	}
	defer rd.f.Close()
	data, info, err := readStable(r.Context(), rd.f)
	if err != nil {
		writeStableError(w, rd.abs, err)
		return
	}
	if looksBinary(data) {
		webhttp.WriteJSONStatus(w, http.StatusUnsupportedMediaType,
			FileRefusal{Error: "binary file", Code: RefusalBinary})
		return
	}
	webhttp.WriteJSON(w, FileRead{
		Path:     reqPath,
		FileID:   fileIDOf(data),
		Modified: modifiedOf(info),
		Content:  string(data),
		Size:     info.Size(),
		UTF8:     utf8.Valid(data),
		ReadOnly: rd.readOnly,
	})
}

// writeStableError answers a readStable failure: too large is the viewer's refusal, and a
// file that would not hold still is a 409.
func writeStableError(w http.ResponseWriter, abs string, err error) {
	if tl, ok := errors.AsType[*tooLargeError](err); ok {
		size := tl.size
		webhttp.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
			FileRefusal{Error: tooLargeSentence, Code: RefusalTooLarge, Size: &size})
		return
	}
	writeOpenError(w, abs, err)
}

// --- GET /api/file/stat: the facts the viewer picks a view from ---

func (h *Handler) handleStat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" {
		httpreply.BadRequest(w, "missing path")
		return
	}
	rd, ok := h.openForRead(w, reqPath)
	if !ok {
		return
	}
	defer rd.f.Close()
	out := FileStat{Path: reqPath, ReadOnly: rd.readOnly}
	if rd.info.Size() > WholeFileMax {
		binary, err := sniffBinary(rd.f)
		if err != nil {
			writeOpenError(w, rd.abs, err)
			return
		}
		out.Size, out.Modified, out.Large, out.Binary = rd.info.Size(), modifiedOf(rd.info), true, binary
		webhttp.WriteJSON(w, out)
		return
	}
	data, info, err := readStable(r.Context(), rd.f)
	if err != nil {
		if tl, isTooLarge := errors.AsType[*tooLargeError](err); isTooLarge {
			// Grew past the cap between the open and the read.
			out.Size, out.Modified, out.Large = tl.size, modifiedOf(rd.info), true
			webhttp.WriteJSON(w, out)
			return
		}
		writeOpenError(w, rd.abs, err)
		return
	}
	out.FileID = fileIDOf(data)
	out.Size = info.Size()
	out.Modified = modifiedOf(info)
	out.Binary = looksBinary(data)
	out.UTF8 = utf8.Valid(data)
	webhttp.WriteJSON(w, out)
}

// --- GET /api/file/download ---

// handleDownload serves a file as an attachment. With file_id it serves exactly the bytes
// of that identity or refuses with 409 before any header, so an image or a download the
// viewer offers can never deliver bytes other than the ones it shows.
func (h *Handler) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	q := r.URL.Query()
	reqPath := q.Get("path")
	if reqPath == "" {
		httpreply.BadRequest(w, "missing path")
		return
	}
	want := ""
	if q.Has("file_id") {
		id, err := parseFileID(q.Get("file_id"))
		if err != nil {
			webhttp.WriteJSONStatus(w, http.StatusBadRequest,
				FileRefusal{Error: errInvalidFileID.Error(), Code: RefusalInvalidFileID})
			return
		}
		want = id
	}
	rd, ok := h.openForRead(w, reqPath)
	if !ok {
		return
	}
	defer rd.f.Close()
	if want == "" {
		serveDownload(w, r, rd.f, rd.info, rd.abs)
		return
	}
	data, info, err := readStable(r.Context(), rd.f)
	if err != nil {
		if _, isTooLarge := errors.AsType[*tooLargeError](err); isTooLarge {
			writeChanged(w, "")
			return
		}
		writeOpenError(w, rd.abs, err)
		return
	}
	got := fileIDOf(data)
	if got != want {
		writeChanged(w, got)
		return
	}
	setAttachmentHeaders(w, rd.abs)
	w.Header().Set("ETag", etagOf(got))
	http.ServeContent(w, r, filepath.Base(rd.abs), info.ModTime(), bytes.NewReader(data))
}

// serveDownload streams an open, confined regular file with no identity check: the file
// browser's download and the Download offered for a file over WholeFileMax.
func serveDownload(w http.ResponseWriter, r *http.Request, f *os.File, info fs.FileInfo, abs string) {
	setAttachmentHeaders(w, abs)
	// A cache validator, not an identity: Last-Modified truncates to a second, so size plus
	// mtime in nanoseconds keeps a same-second rewrite from revalidating as unchanged. The
	// quoting is load-bearing: unquoted, ServeContent falls back to the mtime.
	w.Header().Set("ETag", strconv.Quote(fmt.Sprintf("%x-%x", info.Size(), info.ModTime().UnixNano())))
	slog.Debug("filebrowse: download", "path", logsafe.Field(abs), "size", info.Size())
	// Deliberately UNCAPPED: a size guard here runs before ServeContent reads
	// the Range header, so it answered 413 to the cheapest request there is.
	http.ServeContent(w, r, filepath.Base(abs), info.ModTime(), f)
}

func setAttachmentHeaders(w http.ResponseWriter, abs string) {
	name := filepath.Base(abs)
	ct := cmp.Or(mime.TypeByExtension(filepath.Ext(name)), "application/octet-stream")
	w.Header().Set("Content-Type", ct)
	// `attachment` is a SECURITY CONTROL: an SVG served inline would run script as a document, and
	// CSP's frame-src falls back to `default-src 'self'`. Inline would make the download anchor
	// stored XSS (TestHandleDownload_SVGIsAttachment).
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, name))
	// Revalidate every impression: a validator with no explicit lifetime gets heuristic caching.
	w.Header().Set("Cache-Control", "no-cache")
}
