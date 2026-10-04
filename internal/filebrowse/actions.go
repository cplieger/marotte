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

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/webhttp/v3"
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
	webhttp.LimitBody(w, r, webhttp.MaxJSONBody)

	var body fileAction
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		if maxErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
			slog.Warn("filebrowse: action body too large",
				"limit", webhttp.MaxJSONBody, "error", maxErr)
			webhttp.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
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
	if err := fn(r.Context(), w, body, l, h); err != nil {
		// fn may have already written a response (for synchronous bad-
		// input errors); sentinel errHandled signals that case so we
		// don't double-write.
		if errors.Is(err, errHandled) {
			return
		}
		slog.Warn("filebrowse: action failed",
			"action", logsafe.Field(body.Action), "path", logsafe.Field(l.abs), "error", logsafe.Field(err.Error()))
		webhttp.WriteJSONStatus(w, http.StatusInternalServerError,
			httpreply.ErrorJSON(body.Action+" failed"))
		return
	}
	webhttp.Ok(w)
}

// refuseMountPoint writes the shared 403 for create/delete/rename/move
// aimed at a granted root itself. Mounts are boot-time configuration;
// the UI must not be able to remove or shadow one.
func refuseMountPoint(w http.ResponseWriter, action string, l loc) error {
	slog.Warn("filebrowse: "+action+" blocked on granted root", "path", l.abs)
	httpreply.Forbidden(w, "refusing to "+action+" a granted root")
	return errHandled
}

func actionMkdir(_ context.Context, w http.ResponseWriter, _ fileAction, l loc, h *Handler) error {
	// Symmetric with the actionDelete/actionRename destination guards: a
	// cold-boot mkdir on a sensitive dir must not pre-empt it.
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
	if err := l.m.root.MkdirAll(l.rel(), 0o755); err != nil {
		return err
	}
	slog.Info("filebrowse: mkdir", "path", l.abs)
	return nil
}

func actionTouch(_ context.Context, w http.ResponseWriter, _ fileAction, l loc, h *Handler) error {
	// Mirror of actionMkdir; also checks Sensitive.Blocks so creating an
	// exact-match sensitive file is refused before it hits the filesystem.
	if h.sensitive.Blocks(l.abs) || h.sensitive.protectedDir(l.abs) {
		slog.Warn("filebrowse: touch blocked on protected path", "path", l.abs)
		httpreply.Forbidden(w, "refusing to touch protected path")
		return errHandled
	}
	if l.isMountPoint() {
		return refuseMountPoint(w, "touch", l)
	}
	// O_EXCL rather than syscall.O_NOFOLLOW, which is INERT here:
	// os.Root.OpenFile ORs O_NOFOLLOW in itself and re-resolves the link on
	// the resulting ELOOP (go1.27.0, src/os/root_unix.go:85-101), so a
	// caller-supplied one is silently ignored. O_EXCL is a refusal the
	// kernel does honour: anything already at the name — including a
	// symlink planted after resolvePath accepted it — makes the create
	// fail instead of opening whatever it points at. An existing entry is
	// touch's no-op case, so EEXIST is success.
	f, err := l.m.root.OpenFile(l.rel(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			slog.Info("filebrowse: touch (already present)", "path", l.abs)
			return nil
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
	// Refuse to delete a granted root itself; everything INSIDE a mount is
	// deletable.
	if l.isMountPoint() {
		return refuseMountPoint(w, "delete", l)
	}
	// Layered guard: the mount-point check stops `/config` but would let
	// `/config/chats` through, since Sensitive.Blocks only matches the files
	// inside. protectedDir closes that gap.
	if h.sensitive.protectedDir(l.abs) {
		slog.Warn("filebrowse: delete blocked on protected dir", "path", l.abs)
		httpreply.Forbidden(w, "refusing to delete protected directory")
		return errHandled
	}
	// The mount's os.Root confines this unlink but does not PIN it: it
	// deliberately follows an in-root symlink, so a multi-component rel can
	// resolve to a different file than the one protectedDir judged:
	// reachable through the sensitive-path check because that check is
	// exact-prefix over the resolved path. OpenParentInRoot descends
	// component by component, Lstat-ing each one and refusing a symlink
	// rather than following it, so naming only the final element through
	// the pinned parent removes every ancestor from the unlink's path.
	//
	// The parent's own RemoveAll, never atomicfile.RemoveFileInRoot: that
	// refuses anything non-regular with ErrNotRegular, which would make a
	// symlinked entry undeletable from the browser.
	parent, base, err := atomicfile.OpenParentInRoot(l.m.root, l.rel())
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
	// Source-side guards. Renaming a granted root would shadow the mount;
	// renaming a protected container could orphan the server's view of
	// its state.
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
	// Route the destination through resolvePath so the allow-list +
	// real-path checks fire against the rename target too, not just the
	// source.
	destLoc, err := h.resolvePath(dest)
	if err != nil {
		slog.Warn("filebrowse: rename dest rejected",
			"from", l.abs, "to", dest, "reason", err.Error())
		httpreply.Forbidden(w, err.Error())
		return errHandled
	}
	// Confirm the resolved destination is still a direct child of the
	// original parent (defense in depth against separator surprises on
	// non-Linux filesystems). Same parent implies same mount.
	if filepath.Dir(destLoc.abs) != filepath.Dir(l.abs) {
		httpreply.Forbidden(w, "rename escapes parent directory")
		return errHandled
	}
	// Sensitive-path check on the DESTINATION: without this a
	// touch→write→rename sequence could overwrite sensitive files.
	// protectedDir layers on top for a decoy directory landing at a
	// bare-directory sensitive prefix name.
	if h.sensitive.Blocks(destLoc.abs) || h.sensitive.protectedDir(destLoc.abs) || destLoc.isMountPoint() {
		slog.Warn("filebrowse: rename blocked on sensitive dest",
			"from", l.abs, "to", destLoc.abs)
		httpreply.Forbidden(w, "rename target is protected")
		return errHandled
	}
	// One pinned parent addresses both ends here, since the same-parent
	// assertion above already established they share one directory. See
	// actionDelete for why the mount's os.Root is not enough on its own.
	parent, base, err := atomicfile.OpenParentInRoot(l.m.root, l.rel())
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

// availableBytes reports the bytes an unprivileged writer may still consume on
// the filesystem holding mountRoot. A package-level var so a test can drive the
// precheck against a tiny value instead of filling a real filesystem.
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
	// Statfs the destination MOUNT ROOT, not the client-supplied destination
	// path: the mount root is server-owned so nothing a client sends can steer
	// it, and it is on the same filesystem as the destination. A nested mount
	// INSIDE the root is an accepted edge case where the answer can be wrong.
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

// ctxReader wraps an io.Reader with a context. Every Read first checks
// ctx.Err() so a cancelled/disconnected request aborts on the next chunk
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
