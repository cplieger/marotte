package filebrowse

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"unicode/utf8"

	"github.com/cplieger/go/filesafe"
	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/filemode"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
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

// writeFile writes the body to l, through owner when l is an owned file (owner nil otherwise).
func writeFile(w http.ResponseWriter, r *http.Request, l loc, owner Owner) {
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
	parent, base, err := filesafe.OpenParentInRoot(l.m.root, l.rel())
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
	if owner != nil {
		writeOwned(w, r, l, owner, body)
		return
	}
	if body.FileID != nil && !staleWriteAllowed(w, r, l, parent, base, *body.FileID) {
		return
	}
	// A rename publishes a new inode, so an existing regular file's bits are
	// carried across or a save would flatten a 0o755 script; a new file passes
	// no mode and the directory decides.
	var opts []filesafe.WriteOption
	var restore os.FileMode
	var chmod bool
	if statErr == nil && existing.Mode().IsRegular() {
		opts, restore, chmod = filemode.RewriteOptions(existing.Mode().Perm())
	}
	data := []byte(body.Content)
	if _, err := filesafe.WriteFileInRoot(r.Context(), parent, base, data, opts...); err != nil {
		writeFileError(w, l, err)
		return
	}
	if chmod {
		if err := chmodInRoot(parent, base, restore); err != nil {
			slog.Debug("filebrowse: could not restore the file's mode after a write",
				"path", logsafe.Field(l.abs), "mode", restore, "error", logsafe.Field(err.Error()))
		}
	}
	slog.Info("filebrowse: file written", "path", logsafe.Field(l.abs), "bytes", len(data))
	httpserver.WriteJSON(w, FileWriteResult{OK: true, FileID: fileIDOf(data), Size: int64(len(data))})
}

// decodeWriteBody reads and validates the PUT body before anything on disk is opened.
func decodeWriteBody(w http.ResponseWriter, r *http.Request, l loc) (writeBody, bool) {
	httpserver.LimitBody(w, r, writeBodyLimit)
	var body writeBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		if maxErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
			slog.Warn("filebrowse: write body too large",
				"path", logsafe.Field(l.abs), "limit", writeBodyLimit, "error", logsafe.Field(maxErr.Error()))
			httpserver.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				FileRefusal{Error: "file too large", Code: refusalTooLarge})
			return writeBody{}, false
		}
		httpreply.BadRequest(w, "invalid json")
		return writeBody{}, false
	}
	if body.FileID != nil {
		if _, err := parseFileID(*body.FileID); err != nil {
			httpserver.WriteJSONStatus(w, http.StatusBadRequest,
				FileRefusal{Error: errInvalidFileID.Error(), Code: refusalInvalidFileID})
			return writeBody{}, false
		}
	}
	if len(body.Content) > WholeFileMax {
		size := int64(len(body.Content))
		httpserver.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
			FileRefusal{Error: "file too large", Code: refusalTooLarge, Size: &size})
		return writeBody{}, false
	}
	return body, true
}

// staleError is an owner's check refusing the bytes on disk, which differ from the editor's.
type staleError struct{ current []byte }

func (*staleError) Error() string { return errStaleWrite }

// writeOwned hands the save to l's owner, which reads, checks and writes under its own lock. The
// answer carries the written bytes when the owner wrote other bytes than were sent.
func writeOwned(w http.ResponseWriter, r *http.Request, l loc, owner Owner, body writeBody) {
	var check func([]byte) error
	if body.FileID != nil {
		want := *body.FileID
		check = func(current []byte) error {
			if current == nil || fileIDOf(current) == want {
				return nil
			}
			return &staleError{current: current}
		}
	}
	content := []byte(body.Content)
	written, err := owner.Replace(r.Context(), content, check)
	if stale, ok := errors.AsType[*staleError](err); ok {
		writeOwnedStale(w, l, stale.current, *body.FileID)
		return
	}
	if refusal, ok := errors.AsType[SaveRefusalError](err); ok {
		slog.Info("filebrowse: save refused by its owner",
			"path", logsafe.Field(l.abs), "reason", logsafe.Field(refusal.Error()))
		httpreply.BadRequest(w, refusal.Error())
		return
	}
	if err != nil {
		writeFileError(w, l, err)
		return
	}
	slog.Info("filebrowse: file written", "path", logsafe.Field(l.abs), "bytes", len(written))
	out := FileWriteResult{OK: true, FileID: fileIDOf(written), Size: int64(len(written))}
	if !bytes.Equal(written, content) {
		text := string(written)
		out.Content = &text
	}
	httpserver.WriteJSON(w, out)
}

// writeOwnedStale answers a stale owned save as staleWriteAllowed answers an unowned one.
func writeOwnedStale(w http.ResponseWriter, l loc, current []byte, want string) {
	got := fileIDOf(current)
	slog.Info("filebrowse: refused a stale write",
		"path", logsafe.Field(l.abs), "expected", logsafe.Field(want), "actual", got)
	if len(current) > WholeFileMax {
		size := int64(len(current))
		httpserver.WriteJSONStatus(w, http.StatusConflict, FileRefusal{
			Error: errStaleWrite, Code: refusalChanged, ContentKind: contentTooLarge, Size: &size,
		})
		return
	}
	httpserver.WriteJSONStatus(w, http.StatusConflict, staleRefusal(current, got))
}

// A full volume gets its own status and message.
func writeFileError(w http.ResponseWriter, l loc, err error) {
	switch {
	case errors.Is(err, filesafe.ErrSymlinkTarget), errors.Is(err, filesafe.ErrNotRegular):
		slog.Warn("filebrowse: refused a write onto a non-regular target",
			"path", logsafe.Field(l.abs), "error", logsafe.Field(err.Error()))
		httpreply.BadRequest(w, "not a regular file")
	case isOutOfSpace(err):
		slog.Warn("filebrowse: write failed, out of space",
			"path", logsafe.Field(l.abs), "error", logsafe.Field(err.Error()))
		httpserver.WriteJSONStatus(w, http.StatusInsufficientStorage,
			httpreply.ErrorJSON(errNoSpaceLeft))
	default:
		slog.Warn("filebrowse: write failed", "path", logsafe.Field(l.abs), "error", logsafe.Field(err.Error()))
		httpserver.WriteJSONStatus(w, http.StatusInternalServerError,
			httpreply.ErrorJSON("write failed"))
	}
}

// staleWriteAllowed is the stale-write guard: it reports whether the write may proceed, having
// written the refusal itself when not. It compares a fresh read's identity, never a stored one.
// Not locked: the compare-then-write race needs cross-process locking, which the single atomic
// writer does not warrant. An absent file is not stale.
func staleWriteAllowed(w http.ResponseWriter, r *http.Request, l loc, parent *os.Root, base, want string) bool {
	f, _, err := filesafe.OpenRegularInRootNoFollow(parent, base)
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if err != nil {
		if errors.Is(err, filesafe.ErrRaced) {
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
			httpserver.WriteJSONStatus(w, http.StatusConflict, FileRefusal{
				Error: errStaleWrite, Code: refusalChanged, ContentKind: contentTooLarge, Size: &size,
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
	httpserver.WriteJSONStatus(w, http.StatusConflict, staleRefusal(current, got))
	return false
}

// staleRefusal carries the disk text only when JSON can carry it exactly: a NUL or invalid
// UTF-8 would arrive as U+FFFD, which the client could then save over the real bytes.
func staleRefusal(current []byte, id string) FileRefusal {
	size := int64(len(current))
	out := FileRefusal{Error: errStaleWrite, Code: refusalChanged, FileID: id, Size: &size}
	switch {
	case looksBinary(current):
		out.ContentKind = contentBinary
	case !utf8.Valid(current):
		out.ContentKind = contentNotUTF8
	default:
		text := string(current)
		out.ContentKind, out.Content = contentText, &text
	}
	return out
}
