package filebrowse

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
)

const (
	maxZipBytes = 500 * 1024 * 1024 // 500 MB
	maxZipFiles = 10_000
)

func (h *Handler) handleDownloadZip(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	var req struct {
		Paths []string `json:"paths"`
	}
	if !httpreply.DecodeJSON(w, r, &req) {
		return
	}
	if len(req.Paths) == 0 {
		httpreply.BadRequest(w, "no paths provided")
		return
	}

	// Resolve all paths upfront before streaming (can't send error after headers).
	paths, ok := h.resolveZipPaths(w, req.Paths)
	if !ok {
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="download.zip"`)

	zw := zip.NewWriter(w)
	defer zw.Close()

	flusher, _ := w.(http.Flusher)
	z := &zipStream{zw: zw, flusher: flusher, ctx: r.Context(), sensitive: h.sensitive}
	for _, p := range paths {
		if !z.add(p, filepath.Base(p.abs)) {
			break
		}
	}
}

// resolveZipPaths resolves every requested path through the
// path-containment guard BEFORE any bytes are written, so a rejection
// can still surface as an HTTP 403 (headers aren't sent yet). Returns
// ok=false once resolveOrForbid has written the rejection response.
func (h *Handler) resolveZipPaths(w http.ResponseWriter, reqPaths []string) (paths []loc, ok bool) {
	paths = make([]loc, 0, len(reqPaths))
	for _, p := range reqPaths {
		l, resolved := h.resolveOrForbid(w, p)
		if !resolved {
			return nil, false
		}
		paths = append(paths, l)
	}
	return paths, true
}

type zipStream struct {
	zw         *zip.Writer
	flusher    http.Flusher
	ctx        context.Context
	sensitive  Sensitive
	totalBytes int64
	fileCount  int
}

// capped reports whether streaming should stop: context cancelled, or
// either the file-count or byte-size cap already reached.
func (z *zipStream) capped() bool {
	return z.ctx.Err() != nil || z.fileCount >= maxZipFiles || z.totalBytes >= maxZipBytes
}

// add writes one requested root (file or directory, recursively) into the zip.
// It returns false to stop the whole stream and true to continue with the next
// root. The walk is the pinned descent search uses, so a symlink is never
// followed and the deny list judges the real path of every entry.
func (z *zipStream) add(l loc, zipName string) bool {
	if z.capped() {
		return false
	}
	if z.sensitive.Blocks(l.abs) {
		return true
	}
	f, err := openPinnedRoot(l)
	if err != nil {
		slog.Warn("filebrowse: zip open failed", "path", logsafe.Field(l.abs), "error", logsafe.Field(err.Error()))
		return true
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		slog.Warn("filebrowse: zip stat failed", "path", logsafe.Field(l.abs), "error", logsafe.Field(err.Error()))
		return true
	}
	switch {
	case info.IsDir():
		return z.addDir(f, l.abs, zipName, 0)
	case info.Mode().IsRegular():
		return z.writeFile(f, zipName)
	default:
		return true
	}
}

// addDir archives every child of an open directory handle, in bounded ReadDir
// batches. A read failure ends this directory (logged, true); a stop signal from
// any child propagates as false.
func (z *zipStream) addDir(dir *os.File, abs, zipName string, depth int) bool {
	for {
		if z.capped() {
			return false
		}
		entries, err := dir.ReadDir(searchReadDirChunk)
		for _, e := range entries {
			if !z.addEntry(dir, e, abs, zipName, depth) {
				return false
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				slog.Warn("filebrowse: zip readdir failed", "path", logsafe.Field(abs), "error", logsafe.Field(err.Error()))
			}
			return true
		}
		if len(entries) == 0 {
			return true
		}
	}
}

// A symlink is skipped rather than followed, and so is a FIFO, device or socket.
func (z *zipStream) addEntry(dir *os.File, e fs.DirEntry, parentAbs, parentZip string, depth int) bool {
	name := e.Name()
	abs := filepath.Join(parentAbs, name)
	zipName := filepath.Join(parentZip, name)
	if z.sensitive.Blocks(abs) {
		return true
	}
	switch {
	case e.IsDir():
		if depth+1 > maxSearchDepth {
			slog.Warn("filebrowse: zip depth cap reached", "path", logsafe.Field(abs))
			return true
		}
		child, err := openChild(dir, name, abs, pinnedDirFlags)
		if err != nil {
			warnZipOpen(abs, err)
			return true
		}
		defer child.Close()
		return z.addDir(child, abs, zipName, depth+1)
	case e.Type().IsRegular():
		f, err := openChild(dir, name, abs, pinnedFileFlags)
		if err != nil {
			warnZipOpen(abs, err)
			return true
		}
		defer f.Close()
		if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
			return true
		}
		return z.writeFile(f, zipName)
	default:
		return true
	}
}

// warnZipOpen logs an entry the walk could not open, except the two ordinary
// shapes on a tree being written to: gone, or swapped since the listing.
func warnZipOpen(abs string, err error) {
	if errors.Is(err, fs.ErrNotExist) || isSwapRefusal(err) {
		return
	}
	slog.Warn("filebrowse: zip open failed", "path", logsafe.Field(abs), "error", logsafe.Field(err.Error()))
}

// writeFile copies one regular file into the archive, updates the
// running totals, flushes, and stops the stream once a cap is reached.
func (z *zipStream) writeFile(f *os.File, zipName string) bool {
	fw, err := z.zw.Create(zipName)
	if err != nil {
		return false
	}
	n, _ := io.Copy(fw, f)
	z.totalBytes += n
	z.fileCount++
	if z.flusher != nil {
		z.flusher.Flush()
	}
	if z.totalBytes >= maxZipBytes {
		slog.Warn("filebrowse: zip size cap reached", "bytes", z.totalBytes)
		return false
	}
	if z.fileCount >= maxZipFiles {
		slog.Warn("filebrowse: zip file cap reached", "count", z.fileCount)
		return false
	}
	return true
}
