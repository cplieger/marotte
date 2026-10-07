package filebrowse

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/filemode"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/webhttp/v3"
)

// errStaleWrite is the client sentinel for a refused stale write. Named so the
// editor can branch on it rather than matching prose.
const errStaleWrite = "file changed on disk since you opened it"

// chmodInRoot is a test seam over os.Root.Chmod.
var chmodInRoot = (*os.Root).Chmod

// writeBody is the PUT /api/file payload.
type writeBody struct {
	Content string `json:"content"`
	// ExpectedHash is the content_hash the client received when it loaded the file. Optional:
	// omitting it writes unconditionally, for non-editor writers.
	ExpectedHash string `json:"expected_hash"`
}

func writeFile(w http.ResponseWriter, r *http.Request, l loc, hook SaveHook) {
	webhttp.LimitBody(w, r, MaxFileSize)
	var body writeBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		if maxErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
			slog.Warn("filebrowse: write body too large",
				"path", logsafe.Field(l.abs), "limit", MaxFileSize, "error", logsafe.Field(maxErr.Error()))
			webhttp.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				httpreply.ErrorJSON(errFileTooLarge))
			return
		}
		httpreply.BadRequest(w, "invalid json")
		return
	}
	// A clean 400 for a directory target rather than a 500 whose EISDIR text leaks the resolved
	// path.
	if info, err := l.m.root.Stat(l.rel()); err == nil && info.IsDir() {
		httpreply.BadRequest(w, "path is a directory")
		return
	}
	if !staleWriteAllowed(w, r, l, body) {
		return
	}
	if refusedBySaveHook(w, l, hook, []byte(body.Content)) {
		return
	}
	// A rename publishes a new inode, so an existing regular file's bits are
	// carried across or a save would flatten a 0o755 script; a new file passes
	// no mode and the directory decides.
	var opts []atomicfile.Option
	var restore os.FileMode
	if info, err := l.m.root.Lstat(l.rel()); err == nil && info.Mode().IsRegular() {
		opts, restore = filemode.RewriteOptions(info.Mode().Perm())
	}
	// One confined atomic write. O_NOFOLLOW would be inert here (os.Root.OpenFile adds it and
	// re-resolves on ELOOP, go1.27.0 src/os/root_unix.go:85-101), and the sensitive paths live
	// inside the /config mount. Temp-then-rename refuses a symlink target up front, and a lost race
	// replaces only the link.
	if _, err := atomicfile.WriteFileInRoot(r.Context(), l.m.root, l.rel(),
		[]byte(body.Content), opts...); err != nil {
		writeFileError(w, l, err)
		return
	}
	if restore != 0 {
		if err := chmodInRoot(l.m.root, l.rel(), restore); err != nil {
			slog.Debug("filebrowse: could not restore the file's mode after a write",
				"path", logsafe.Field(l.abs), "mode", restore, "error", logsafe.Field(err.Error()))
		}
	}
	slog.Info("filebrowse: file written", "path", logsafe.Field(l.abs), "bytes", len(body.Content))
	if hook.Saved != nil {
		hook.Saved()
	}
	webhttp.Ok(w)
}

func refusedBySaveHook(w http.ResponseWriter, l loc, hook SaveHook, content []byte) (answered bool) {
	if hook.Check == nil {
		return false
	}
	err := hook.Check(content)
	if err == nil {
		return false
	}
	slog.Info("filebrowse: save refused by its hook",
		"path", logsafe.Field(l.abs), "reason", logsafe.Field(err.Error()))
	httpreply.BadRequest(w, err.Error())
	return true
}

// writeFileError maps a confined-write failure onto the HTTP status the client needs, off
// atomicfile's sentinels and the errno beneath; a full volume gets its own status and message.
func writeFileError(w http.ResponseWriter, l loc, err error) {
	switch {
	case errors.Is(err, atomicfile.ErrSymlinkTarget), errors.Is(err, atomicfile.ErrNotRegular):
		slog.Warn("filebrowse: refused a write onto a non-regular target",
			"path", logsafe.Field(l.abs), "error", logsafe.Field(err.Error()))
		httpreply.BadRequest(w, "not a regular file")
	case isOutOfSpace(err):
		slog.Warn("filebrowse: write failed, out of space",
			"path", logsafe.Field(l.abs), "error", logsafe.Field(err.Error()))
		webhttp.WriteJSONStatus(w, http.StatusInsufficientStorage,
			httpreply.ErrorJSON(errNoSpaceLeft))
	default:
		slog.Warn("filebrowse: write failed", "path", logsafe.Field(l.abs), "error", logsafe.Field(err.Error()))
		webhttp.WriteJSONStatus(w, http.StatusInternalServerError,
			httpreply.ErrorJSON("write failed"))
	}
}

// staleWriteAllowed is the stale-write guard: it reports whether the write may proceed, having
// written the 409 (or 500) itself when not. Not locked: the compare-then-write race needs
// cross-process locking, which the single atomic writer does not warrant. An absent file is not
// stale.
func staleWriteAllowed(w http.ResponseWriter, r *http.Request, l loc, body writeBody) bool {
	if body.ExpectedHash == "" {
		return true
	}
	current, err := atomicfile.ReadBoundedInRoot(r.Context(), l.m.root, l.rel(), MaxFileSize)
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if err != nil {
		slog.Warn("filebrowse: stale-check read failed", "path", logsafe.Field(l.abs), "error", logsafe.Field(err.Error()))
		webhttp.WriteJSONStatus(w, http.StatusInternalServerError, httpreply.ErrorJSON("write failed"))
		return false
	}
	got := contentHash(current)
	if got == body.ExpectedHash {
		return true
	}
	slog.Info("filebrowse: refused a stale write",
		"path", logsafe.Field(l.abs), "expected", logsafe.Field(body.ExpectedHash), "actual", got)
	// The current content rides the 409 so the client can show what changed
	// instead of asking the user to reload and compare by eye.
	webhttp.WriteJSONStatus(w, http.StatusConflict, map[string]string{
		"error":         errStaleWrite,
		"content":       string(current),
		"content_hash":  got,
		"expected_hash": body.ExpectedHash,
	})
	return false
}
