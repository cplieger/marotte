package filebrowse

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"unicode/utf8"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/filemode"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/webhttp/v3"
)

// errStaleWrite is the sentence on a refused stale save.
const errStaleWrite = "file changed on disk since you opened it"

// writeBodyLimit bounds the JSON body: a WholeFileMax file of control bytes encodes each
// byte as a six-byte \u00XX escape.
const writeBodyLimit = 6*WholeFileMax + 1024

var chmodInRoot = (*os.Root).Chmod

type writeBody struct {
	// FileID is the identity the client's buffer was read under. Absent (or null) writes
	// unconditionally, for non-editor writers and the editor's explicit Overwrite.
	FileID  *string `json:"file_id"`
	Content string  `json:"content"`
}

func writeFile(w http.ResponseWriter, r *http.Request, l loc, hook SaveHook) {
	body, ok := decodeWriteBody(w, r, l)
	if !ok {
		return
	}
	if l.isMountPoint() {
		httpreply.BadRequest(w, "path is a directory")
		return
	}
	// Pinned once: the stale read, the mode carry-over, the write and the chmod all name
	// the basename through this root, so an ancestor swapped for a symlink after the policy
	// check cannot redirect any of them.
	parent, base, err := atomicfile.OpenParentInRoot(l.m.root, l.rel())
	if err != nil {
		writeFileError(w, l, err)
		return
	}
	defer parent.Close()
	existing, statErr := parent.Lstat(base)
	if statErr == nil && existing.IsDir() {
		httpreply.BadRequest(w, "path is a directory")
		return
	}
	if body.FileID != nil && !staleWriteAllowed(w, r, l, parent, base, *body.FileID) {
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
	if statErr == nil && existing.Mode().IsRegular() {
		opts, restore = filemode.RewriteOptions(existing.Mode().Perm())
	}
	data := []byte(body.Content)
	if _, err := atomicfile.WriteFileInRoot(r.Context(), parent, base, data, opts...); err != nil {
		writeFileError(w, l, err)
		return
	}
	if restore != 0 {
		if err := chmodInRoot(parent, base, restore); err != nil {
			slog.Debug("filebrowse: could not restore the file's mode after a write",
				"path", logsafe.Field(l.abs), "mode", restore, "error", logsafe.Field(err.Error()))
		}
	}
	slog.Info("filebrowse: file written", "path", logsafe.Field(l.abs), "bytes", len(data))
	if hook.Saved != nil {
		hook.Saved()
	}
	webhttp.WriteJSON(w, FileWriteResult{OK: true, FileID: fileIDOf(data), Size: int64(len(data))})
}

// decodeWriteBody reads and validates the PUT body before anything on disk is opened.
func decodeWriteBody(w http.ResponseWriter, r *http.Request, l loc) (writeBody, bool) {
	webhttp.LimitBody(w, r, writeBodyLimit)
	var body writeBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		if maxErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
			slog.Warn("filebrowse: write body too large",
				"path", logsafe.Field(l.abs), "limit", writeBodyLimit, "error", logsafe.Field(maxErr.Error()))
			webhttp.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				FileRefusal{Error: "file too large", Code: RefusalTooLarge})
			return writeBody{}, false
		}
		httpreply.BadRequest(w, "invalid json")
		return writeBody{}, false
	}
	if body.FileID != nil {
		if _, err := parseFileID(*body.FileID); err != nil {
			webhttp.WriteJSONStatus(w, http.StatusBadRequest,
				FileRefusal{Error: errInvalidFileID.Error(), Code: RefusalInvalidFileID})
			return writeBody{}, false
		}
	}
	if len(body.Content) > WholeFileMax {
		size := int64(len(body.Content))
		webhttp.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
			FileRefusal{Error: "file too large", Code: RefusalTooLarge, Size: &size})
		return writeBody{}, false
	}
	return body, true
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

// A full volume gets its own status and message.
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
// written the refusal itself when not. It compares a fresh read's identity, never a stored one.
// Not locked: the compare-then-write race needs cross-process locking, which the single atomic
// writer does not warrant. An absent file is not stale.
func staleWriteAllowed(w http.ResponseWriter, r *http.Request, l loc, parent *os.Root, base, want string) bool {
	f, _, err := atomicfile.OpenRegularInRootNoFollow(parent, base)
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if err != nil {
		if errors.Is(err, atomicfile.ErrRaced) {
			writeChanged(w, "")
			return false
		}
		writeFileError(w, l, err)
		return false
	}
	defer f.Close()
	current, _, err := readStable(r.Context(), f)
	if err != nil {
		// Not writeStableError: that is the viewer's mapping, and a save refused for size is a
		// stale save the editor offers Overwrite and Download for.
		if tl, ok := errors.AsType[*tooLargeError](err); ok {
			size := tl.size
			webhttp.WriteJSONStatus(w, http.StatusConflict, FileRefusal{
				Error: errStaleWrite, Code: RefusalChanged, ContentKind: ContentTooLarge, Size: &size,
			})
			return false
		}
		writeOpenError(w, l.abs, err)
		return false
	}
	got := fileIDOf(current)
	if got == want {
		return true
	}
	slog.Info("filebrowse: refused a stale write",
		"path", logsafe.Field(l.abs), "expected", logsafe.Field(want), "actual", got)
	webhttp.WriteJSONStatus(w, http.StatusConflict, staleRefusal(current, got))
	return false
}

// staleRefusal carries the disk text only when JSON can carry it exactly: a NUL or invalid
// UTF-8 would arrive as U+FFFD, which the client could then save over the real bytes.
func staleRefusal(current []byte, id string) FileRefusal {
	size := int64(len(current))
	out := FileRefusal{Error: errStaleWrite, Code: RefusalChanged, FileID: id, Size: &size}
	switch {
	case looksBinary(current):
		out.ContentKind = ContentBinary
	case !utf8.Valid(current):
		out.ContentKind = ContentNotUTF8
	default:
		text := string(current)
		out.ContentKind, out.Content = ContentText, &text
	}
	return out
}
