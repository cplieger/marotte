package filebrowse

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"path/filepath"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/webhttp/v3"
)

// --- /api/file/upload (POST multipart into a target directory) ---

func (h *Handler) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	// gosec:G120 is a false positive: r.Body is capped by MaxBytesReader
	// above, so ParseMultipartForm can't cause memory exhaustion here.
	if err := r.ParseMultipartForm(multipartMaxMemory); err != nil { //nolint:gosec // G120: size bounded by nginx proxy
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			slog.Warn("filebrowse: upload too large",
				"limit", maxUploadSize, "error", err)
			webhttp.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				httpreply.ErrorJSON("upload too large"))
		} else if errors.Is(err, context.Canceled) {
			slog.Debug("filebrowse: upload cancelled by client")
		} else {
			slog.Warn("filebrowse: upload form parse failed", "error", err)
			httpreply.BadRequest(w, "invalid multipart form")
		}
		return
	}
	dir := cmp.Or(r.FormValue("dir"), marotte.DefaultUploadDir)
	dirLoc, ok := h.resolveOrForbid(w, dir)
	if !ok {
		return
	}
	// Without this gate an upload with dir=/config would land inside the sensitive container;
	// writeUploads checks each final path too.
	if h.sensitive.protectedDir(dirLoc.abs) {
		slog.Warn("filebrowse: upload blocked on protected dir", "dir", dirLoc.abs)
		httpreply.Forbidden(w, "upload target is protected")
		return
	}
	// A nested target is created inside the mount's os.Root; the default target is the uploads
	// mount itself (rel "."), created at boot by ensureUploadDir.
	if err := dirLoc.m.root.MkdirAll(dirLoc.rel(), 0o755); err != nil {
		slog.Warn("filebrowse: upload mkdir failed", "path", dirLoc.abs, "error", err)
		webhttp.WriteJSONStatus(w, http.StatusInternalServerError,
			httpreply.ErrorJSON("upload failed"))
		return
	}
	formFiles := r.MultipartForm.File["files"]
	if len(formFiles) == 0 {
		httpreply.BadRequest(w, "no files")
		return
	}
	// Refuse the WHOLE batch before writing any of it: the batch is not atomic. Not checked against
	// os.TempDir(), the overlay, where the spill lands.
	var batchBytes int64
	for _, fh := range formFiles {
		batchBytes += fh.Size
	}
	if err := refuseIfCannotFit(batchBytes, dirLoc); err != nil {
		slog.Warn("filebrowse: upload refused, not enough space",
			"dir", dirLoc.abs, "batch_bytes", batchBytes, "error", err)
		webhttp.WriteJSONStatus(w, http.StatusInsufficientStorage,
			uploadErrorJSON(errNoSpaceLeft, nil))
		return
	}
	uploaded, totalBytes, err := writeUploads(r.Context(), dirLoc, formFiles, h.sensitive)
	if err != nil {
		respondUploadError(w, dirLoc.abs, uploaded, err)
		return
	}
	slog.Info("filebrowse: upload",
		"dir", dirLoc.abs, "count", len(uploaded), "bytes", totalBytes)
	webhttp.WriteJSON(w, map[string]any{"ok": true, "uploaded": uploaded})
}

// respondUploadError maps a writeUploads failure: invalid filename 400, an over-cap file 413 (never
// truncated), a full volume 507, else 500. Every body names the files that DID land, because a
// batch is not rolled back (each file is whole or absent).
func respondUploadError(w http.ResponseWriter, dir string, uploaded []string, err error) {
	if errors.Is(err, errInvalidFilename) {
		webhttp.WriteJSONStatus(w, http.StatusBadRequest,
			uploadErrorJSON(err.Error(), uploaded))
		return
	}
	if errors.Is(err, atomicfile.ErrFileTooLarge) {
		slog.Warn("filebrowse: upload too large",
			"limit", maxUploadSize, "uploaded", len(uploaded), "error", logsafe.Field(err.Error()))
		webhttp.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
			uploadErrorJSON("upload too large", uploaded))
		return
	}
	if isOutOfSpace(err) {
		slog.Warn("filebrowse: upload failed, out of space",
			"dir", dir, "uploaded", len(uploaded), "error", logsafe.Field(err.Error()))
		webhttp.WriteJSONStatus(w, http.StatusInsufficientStorage,
			uploadErrorJSON(errNoSpaceLeft, uploaded))
		return
	}
	slog.Warn("filebrowse: upload write failed",
		"dir", dir, "uploaded", len(uploaded), "error", logsafe.Field(err.Error()))
	webhttp.WriteJSONStatus(w, http.StatusInternalServerError,
		uploadErrorJSON("upload failed", uploaded))
}

// uploadErrorJSON is the error body every upload failure shares: the reason
// plus the files already written. uploaded is always a non-nil slice so the
// key encodes as [] rather than null and the client needs no null check.
func uploadErrorJSON(msg string, uploaded []string) map[string]any {
	if uploaded == nil {
		uploaded = []string{}
	}
	return map[string]any{"error": msg, "uploaded": uploaded}
}

// errInvalidFilename is surfaced as a 400 to the client. Raised on
// "." / ".." / empty-after-Base filenames so silent-skip doesn't
// produce a confusing `uploaded: []` subset response.
var errInvalidFilename = errors.New("invalid filename")

// A failed file leaves no temp; earlier files stay and are named in the error response. ctx aborts
// the rest of the batch.
func writeUploads(ctx context.Context, dirLoc loc, files []*multipart.FileHeader, sensitive Sensitive) (uploaded []string, total int64, err error) {
	uploaded = make([]string, 0, len(files))
	for _, fh := range files {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return uploaded, total, ctxErr
		}
		name := filepath.Base(fh.Filename)
		if name == "" || name == "." || name == ".." {
			slog.Warn("filebrowse: upload rejected: invalid filename",
				"raw_name", logsafe.Field(fh.Filename), "base", logsafe.Field(name), "target_dir", logsafe.Field(dirLoc.abs))
			return uploaded, total, fmt.Errorf("%w: %q", errInvalidFilename, fh.Filename)
		}
		dest := filepath.Join(dirLoc.abs, name)
		// Per-file gate: protectedDir caught a sensitive target directory; this blocks overwriting
		// a sensitive file in an ordinary one.
		if sensitive.Blocks(dest) {
			slog.Warn("filebrowse: upload rejected: sensitive dest",
				"raw_name", logsafe.Field(fh.Filename), "dest", logsafe.Field(dest))
			return uploaded, total, fmt.Errorf("%w: %q (protected)", errInvalidFilename, fh.Filename)
		}
		n, wErr := writeOneUpload(ctx, loc{m: dirLoc.m, abs: dest}, fh)
		if wErr != nil {
			return uploaded, total, wErr
		}
		uploaded = append(uploaded, name)
		total += n
	}
	return uploaded, total, nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func writeOneUpload(ctx context.Context, dest loc, fh *multipart.FileHeader) (n int64, err error) {
	src, err := fh.Open()
	if err != nil {
		return 0, err
	}
	defer func() { _ = src.Close() }()

	// The per-file cap is WithMaxBytes's below, which rejects rather than truncates.
	cr := &countingReader{r: &ctxReader{ctx: ctx, r: src}}
	if _, werr := atomicfile.WriteReaderInRoot(ctx, dest.m.root, dest.rel(), cr,
		atomicfile.WithMaxBytes(maxUploadSize)); werr != nil {
		return cr.n, werr
	}
	return cr.n, nil
}
