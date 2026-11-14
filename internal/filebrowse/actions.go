package filebrowse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/cplieger/go/filesafe"
	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
)

// --- /api/files/action (POST: mkdir, touch, delete, rename) ---

type fileAction struct {
	Action string `json:"action"`
	Path   string `json:"path"`
	// `name` is optional at the wire level and enforced per-action in the
	// handlers: actionRename rejects an empty one.
	Name string `json:"name"`
}

// actionFunc is the signature every file-action handler matches. The
// request context is threaded through so a handler can respect client
// cancellation; ctx wraps r.Context() at the caller.
type actionFunc func(ctx context.Context, w http.ResponseWriter, body fileAction, l loc, h *Handler) error

var fileActions = map[string]actionFunc{
	"mkdir":  actionMkdir,
	"touch":  actionTouch,
	"delete": actionDelete,
	"rename": actionRename,
}

func (h *Handler) handleFilesAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	httpserver.LimitBody(w, r, httpserver.MaxJSONBody)

	var body fileAction
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		if maxErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
			slog.Warn("filebrowse: action body too large",
				"limit", httpserver.MaxJSONBody, "error", maxErr)
			httpserver.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				httpreply.ErrorJSON("request body too large"))
			return
		}
		httpreply.BadRequest(w, "invalid json")
		return
	}
	l, ok := h.resolveOrForbid(w, body.Path)
	if !ok {
		return
	}

	fn, exists := fileActions[body.Action]
	if !exists {
		httpreply.BadRequest(w, "unknown action")
		return
	}
	if owned := h.ownedActionTarget(body.Action, l); owned != "" {
		_ = refuseOwned(w, body.Action, l, owned)
		return
	}
	if err := fn(r.Context(), w, body, l, h); err != nil {
		// fn may already have written a response; errHandled says so, so nothing is double-written.
		if errors.Is(err, errHandled) {
			return
		}
		slog.Warn("filebrowse: action failed",
			"action", logsafe.Field(body.Action), "path", logsafe.Field(l.abs), "error", logsafe.Field(err.Error()))
		httpserver.WriteJSONStatus(w, http.StatusInternalServerError,
			httpreply.ErrorJSON(body.Action+" failed"))
		return
	}
	httpserver.OK(w)
}

// ownedActionTarget answers the owned file the action at l would create, delete or rename, or "".
func (h *Handler) ownedActionTarget(action string, l loc) string {
	switch action {
	case "mkdir", "touch":
		return h.ownedAtOrAbove(l.abs)
	case "delete", "rename":
		if h.ownerFor(l) != nil {
			return l.abs
		}
	}
	return ""
}

// refuseOwned writes the 403 for a file action on an owned file, which only its owner writes.
func refuseOwned(w http.ResponseWriter, action string, l loc, owned string) error {
	slog.Warn("filebrowse: file action blocked on an owned file",
		"action", logsafe.Field(action), "path", logsafe.Field(l.abs), "owned", logsafe.Field(owned))
	httpreply.Forbidden(w, filepath.Base(owned)+" is written through its editor; edit it instead")
	return errHandled
}

// refuseMountPoint writes the shared 403 for create/rename/move
// aimed at a granted root itself. Mounts are boot-time configuration;
// the UI must not be able to remove or shadow one.
func refuseMountPoint(w http.ResponseWriter, action string, l loc) error {
	slog.Warn("filebrowse: "+action+" blocked on granted root", "path", l.abs)
	httpreply.Forbidden(w, "refusing to "+action+" a granted root")
	return errHandled
}

// codeNameTaken tags refuseTaken's 409, which the client tells apart from the idempotency layer's
// in-progress 409 by it.
const codeNameTaken = "name_taken"

// refuseTaken writes the 409 for a create on a name already taken: the client created a name it
// shows the user, so succeeding on someone else's entry would hand them that entry instead.
func refuseTaken(w http.ResponseWriter, action string, l loc) error {
	slog.Info("filebrowse: "+action+" on a taken name", "path", logsafe.Field(l.abs))
	httpserver.WriteJSONStatus(w, http.StatusConflict,
		httpreply.ErrorJSONWithCode(filepath.Base(l.abs)+" already exists", codeNameTaken))
	return errHandled
}

func actionMkdir(_ context.Context, w http.ResponseWriter, _ fileAction, l loc, h *Handler) error {
	if h.sensitive.protectedDir(l.abs) {
		slog.Warn("filebrowse: mkdir blocked on protected dir", "path", l.abs)
		httpreply.Forbidden(w, "refusing to mkdir protected directory")
		return errHandled
	}
	// A granted root always exists; "creating" it is a no-op or an attempt
	// to shadow a mount. Refuse, matching actionDelete.
	if l.isMountPoint() {
		return refuseMountPoint(w, "mkdir", l)
	}
	if err := l.m.root.Mkdir(l.rel(), 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return refuseTaken(w, "mkdir", l)
		}
		return err
	}
	slog.Info("filebrowse: mkdir", "path", l.abs)
	return nil
}

func actionTouch(_ context.Context, w http.ResponseWriter, _ fileAction, l loc, h *Handler) error {
	if h.sensitive.Blocks(l.abs) || h.sensitive.protectedDir(l.abs) {
		slog.Warn("filebrowse: touch blocked on protected path", "path", l.abs)
		httpreply.Forbidden(w, "refusing to touch protected path")
		return errHandled
	}
	if l.isMountPoint() {
		return refuseMountPoint(w, "touch", l)
	}
	// O_EXCL, because O_NOFOLLOW is inert here: os.Root.OpenFile adds it itself and re-resolves on
	// ELOOP (go1.27.0 src/os/root_unix.go:85-101). O_EXCL makes anything already at the name, a
	// symlink planted after resolvePath included, fail the create.
	f, err := l.m.root.OpenFile(l.rel(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return refuseTaken(w, "touch", l)
		}
		return err
	}
	if closeErr := f.Close(); closeErr != nil {
		return closeErr
	}
	slog.Info("filebrowse: touch", "path", l.abs)
	return nil
}

func actionDelete(_ context.Context, w http.ResponseWriter, _ fileAction, l loc, h *Handler) error {
	err := h.remove(l)
	if refusal, ok := errors.AsType[DeleteRefusedError](err); ok {
		httpreply.Forbidden(w, refusal.Reason)
		return errHandled
	}
	return err
}

// DeleteRefusedError is a delete the file browser never performs, Reason worded for the person
// deleting.
type DeleteRefusedError struct{ Reason string }

func (e DeleteRefusedError) Error() string { return e.Reason }

// Delete removes reqPath and everything under it behind the guards of POST /api/files/action's
// delete, answering a DeleteRefusedError for a path those refuse. An absent path is success.
func (h *Handler) Delete(reqPath string) error {
	l, err := h.deletable(reqPath)
	if err != nil {
		return err
	}
	return h.unlink(l)
}

// CheckDelete answers the DeleteRefusedError Delete would for reqPath and removes nothing, so a
// caller removing several paths can refuse the lot before touching the first.
func (h *Handler) CheckDelete(reqPath string) error {
	_, err := h.deletable(reqPath)
	return err
}

// DeleteFile removes the file reqPath resolves to, behind Delete's guards, only when it is a
// regular file: anything else answers an error matching filesafe.ErrNotRegular and is left as
// found. An absent path is success.
func (h *Handler) DeleteFile(reqPath string) error {
	l, err := h.deletable(reqPath)
	if err != nil {
		return err
	}
	err = filesafe.RemoveFileInRoot(l.m.root, l.rel())
	if errors.Is(err, fs.ErrNotExist) {
		slog.Info("filebrowse: delete (already absent)", "path", l.abs)
		return nil
	}
	if err != nil {
		return err
	}
	slog.Info("filebrowse: delete", "path", l.abs)
	return nil
}

func (h *Handler) deletable(reqPath string) (loc, error) {
	l, err := h.resolvePath(filepath.Clean("/" + reqPath))
	if err != nil {
		slog.Warn("filebrowse: delete path rejected",
			"path", logsafe.Field(reqPath), "reason", logsafe.Field(err.Error()))
		return loc{}, DeleteRefusedError{Reason: err.Error()}
	}
	if h.ownerFor(l) != nil {
		return loc{}, DeleteRefusedError{Reason: filepath.Base(l.abs) + " is written through its editor; edit it instead"}
	}
	return l, h.refuseRemove(l)
}

func (h *Handler) remove(l loc) error {
	if err := h.refuseRemove(l); err != nil {
		return err
	}
	return h.unlink(l)
}

func (h *Handler) refuseRemove(l loc) error {
	// Refuse to delete a granted root itself; everything INSIDE a mount is
	// deletable.
	if l.isMountPoint() {
		slog.Warn("filebrowse: delete blocked on granted root", "path", l.abs)
		return DeleteRefusedError{Reason: "refusing to delete a granted root"}
	}
	// The mount-point check stops `/config` but not `/config/chats`, which protectedDir closes.
	if h.sensitive.protectedDir(l.abs) {
		slog.Warn("filebrowse: delete blocked on protected dir", "path", l.abs)
		return DeleteRefusedError{Reason: "refusing to delete protected directory"}
	}
	return nil
}

func (h *Handler) unlink(l loc) error {
	// The mount's os.Root confines the unlink but follows an in-root symlink, so a multi-component
	// rel could reach a different file than protectedDir judged. OpenParentInRoot descends refusing
	// symlinks, so only the final element is named through the pinned parent.
	// The parent's own RemoveAll, not filesafe.RemoveFileInRoot, which refuses a non-regular
	// entry and would make a symlink undeletable.
	parent, base, err := filesafe.OpenParentInRoot(l.m.root, l.rel())
	if err != nil {
		// os.Root.RemoveAll reports an already-absent path as success; a
		// parent directory that is gone is the same answer to the caller.
		// Only ErrNotExist: a component refused for being a symlink or a
		// non-directory is a real failure and must surface.
		if errors.Is(err, fs.ErrNotExist) {
			slog.Info("filebrowse: delete (already absent)", "path", l.abs)
			return nil
		}
		return err
	}
	defer func() { _ = parent.Close() }()
	if err := parent.RemoveAll(base); err != nil {
		return err
	}
	slog.Info("filebrowse: delete", "path", l.abs)
	return nil
}

// isSingleSegmentName reports whether name is one non-traversal path
// component, safe to Join onto a parent directory. filepath.Base alone
// isn't enough: a bare ".." passes Base untouched and Join then silently
// escapes to the parent directory.
func isSingleSegmentName(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.ContainsRune(name, '/') && !strings.ContainsRune(name, '\\') &&
		!strings.ContainsRune(name, 0)
}

func actionRename(_ context.Context, w http.ResponseWriter, body fileAction, l loc, h *Handler) error {
	if l.isMountPoint() {
		return refuseMountPoint(w, "rename", l)
	}
	if h.sensitive.protectedDir(l.abs) {
		slog.Warn("filebrowse: rename blocked on protected dir", "path", l.abs)
		httpreply.Forbidden(w, "refusing to rename protected directory")
		return errHandled
	}
	if !isSingleSegmentName(body.Name) {
		httpreply.BadRequest(w, "invalid name")
		return errHandled
	}
	dest := filepath.Join(filepath.Dir(l.abs), body.Name)
	destLoc, err := h.resolvePath(dest)
	if err != nil {
		slog.Warn("filebrowse: rename dest rejected",
			"from", l.abs, "to", dest, "reason", err.Error())
		httpreply.Forbidden(w, err.Error())
		return errHandled
	}
	// Defense in depth: the resolved destination must still be a direct child of the original
	// parent, which also implies the same mount.
	if filepath.Dir(destLoc.abs) != filepath.Dir(l.abs) {
		httpreply.Forbidden(w, "rename escapes parent directory")
		return errHandled
	}
	// Without the destination check a touch→write→rename could overwrite a sensitive file;
	// protectedDir covers a decoy directory at a bare-directory sensitive name.
	if h.sensitive.Blocks(destLoc.abs) || h.sensitive.protectedDir(destLoc.abs) || destLoc.isMountPoint() {
		slog.Warn("filebrowse: rename blocked on sensitive dest",
			"from", l.abs, "to", destLoc.abs)
		httpreply.Forbidden(w, "rename target is protected")
		return errHandled
	}
	if owned := h.ownedAtOrAbove(destLoc.abs); owned != "" {
		return refuseOwned(w, "rename", destLoc, owned)
	}
	// One pinned parent serves both ends, since they share one directory; see actionDelete for why
	// os.Root alone is not enough.
	parent, base, err := filesafe.OpenParentInRoot(l.m.root, l.rel())
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	if err := parent.Rename(base, filepath.Base(destLoc.abs)); err != nil {
		return err
	}
	slog.Info("filebrowse: rename", "from", l.abs, "to", destLoc.abs)
	return nil
}

// errNoSpace is returned by the free-space precheck when the bytes about to be
// written definitively will not fit at the destination.
var errNoSpace = errors.New("insufficient free space at the destination")

// A package-level var so a test can drive the precheck against a tiny value instead of filling a
// real filesystem.
var availableBytes = func(mountRoot string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(mountRoot, &st); err != nil {
		return 0, err
	}
	// Bavail, NOT Bfree: Bfree counts the blocks held back for root, which this
	// process cannot write into.
	blocks, bsize := st.Bavail, st.Bsize
	if bsize <= 0 {
		return 0, fmt.Errorf("statfs %q: nonsensical block size %d", mountRoot, bsize)
	}
	if blocks > uint64(math.MaxInt64)/uint64(bsize) {
		// A filesystem whose free space overflows an int64 fits anything.
		return math.MaxInt64, nil
	}
	return int64(blocks) * bsize, nil
}

// refuseIfCannotFit returns errNoSpace when size definitely exceeds the
// destination filesystem's free space. ADVISORY and racy: space can vanish
// between this answer and the write, so the write's own ENOSPC/EDQUOT mapping
// is authoritative.
func refuseIfCannotFit(size int64, dest loc) error {
	// Statfs the server-owned mount root, on the destination's filesystem, so no client input
	// steers it; a nested mount inside the root can answer wrong, accepted.
	avail, err := availableBytes(dest.m.root.Name())
	if err != nil {
		// A Statfs this process cannot answer must not refuse a write that would
		// have worked; let the write path be authoritative.
		slog.Debug("filebrowse: destination free space unknown, leaving it to the write",
			"dest", dest.abs, "error", err)
		return nil
	}
	if size > avail {
		return fmt.Errorf("%w: %d bytes wanted, %d available", errNoSpace, size, avail)
	}
	return nil
}

// Every Read first checks ctx.Err() so a cancelled/disconnected request aborts on the next chunk
// boundary.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
