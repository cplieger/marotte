// KAS's own filesystem verbs `_kiro/fs/{stat,read_directory,delete}`, gated on
// `clientCapabilities.fs._meta.kiro.<name>`. Undeclared, KAS serves them itself, so
// declaring one confines an existing capability: stat and read_directory to
// confineReadable, delete to confineInWorkDir. No second gate:
// KAS restores a rejected delete through `fs/write_text_file`. Read and write stay
// undeclared so `fs/{read,write}_text_file` keeps every write guardrail.

package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"syscall"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/spec"
)

// File-type strings KAS's NodeFileSystem returns, the only values its consumers read.
const (
	fsTypeFile      = "file"
	fsTypeDirectory = "directory"
	fsTypeSymlink   = "symlink"
)

// errRefusedWorkDirRoot rejects a delete aimed at the workspace root.
var errRefusedWorkDirRoot = errors.New("refusing to delete the workspace root")

// kiroFSParams is `{sessionId, path}`, shared by all three verbs. KAS sends the path already
// absolute, so the confinement re-checks a claim.
type kiroFSParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
}

// kiroStatBody answers `_kiro/fs/stat`. `size` is required by KAS's
// isFSStatCapabilityResponse even though no consumer reads it.
type kiroStatBody struct {
	Type string `json:"type"`
	Size int64  `json:"size"`
}

// kiroDirEntry is one `_kiro/fs/read_directory` entry.
type kiroDirEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// kiroReadDirBody answers `_kiro/fs/read_directory`. Entries is non-nil: KAS calls `.map` on it.
type kiroReadDirBody struct {
	Entries []kiroDirEntry `json:"entries"`
}

// handleKiroFSRequest dispatches the three `_kiro/fs/*` verbs, reporting whether msg was
// one. Async under inflight with a fresh runtime context: Respond drops a write on the
// cancelled per-event ctx, and KAS's `extMethod` has no timeout.
func (in *inbound) handleKiroFSRequest(_ context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) bool {
	var handler func(context.Context, marotte.ChatID, *marotte.RPCResponse)
	switch msg.Method {
	case methodKiroFSStat:
		handler = in.respondKiroFSStat
	case methodKiroFSReadDirectory:
		handler = in.respondKiroFSReadDirectory
	case methodKiroFSDelete:
		handler = in.respondKiroFSDelete
	default:
		return false
	}
	in.lifetime.inflight.Go(func() {
		ctx, cancel := in.lifetime.derivedContext()
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("kiro fs handler panic",
					"chat_id", chatID, "method", msg.Method, "panic", r)
				in.respondBridge(ctx, chatID, msg, nil, errors.New("internal error"))
			}
		}()
		handler(ctx, chatID, msg)
	})
	return true
}

// kiroFSPath decodes the request's path. Each verb confines it to a root and a root-relative
// name, never an absolute path, so the verdict and the operation share a handle.
func kiroFSPath(msg *marotte.RPCResponse) (string, error) {
	var p kiroFSParams
	if err := parseRequest(msg, &p); err != nil {
		return "", fmt.Errorf("decode %s params: %w", msg.Method, err)
	}
	return p.Path, nil
}

// kiroFSReadablePath confines a stat or read_directory path (lifetime.confineReadable).
func (in *inbound) kiroFSReadablePath(msg *marotte.RPCResponse) (root *os.Root, rel string, release func(), err error) {
	p, err := kiroFSPath(msg)
	if err != nil {
		return nil, "", nil, err
	}
	return in.lifetime.confineReadable(p)
}

// respondKiroFSStat answers `_kiro/fs/stat` with `{type, size}`.
func (in *inbound) respondKiroFSStat(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	root, rel, release, err := in.kiroFSReadablePath(msg)
	if err != nil {
		in.respondFSError(ctx, chatID, msg, err)
		return
	}
	defer release()
	// Root.Stat follows symlinks, like KAS's fs.stat; the symlink type is still reachable via read_directory.
	info, err := root.Stat(rel)
	if err != nil {
		in.respondFSError(ctx, chatID, msg, err)
		return
	}
	body := kiroStatBody{Type: fsTypeFile, Size: info.Size()}
	if info.IsDir() {
		body.Type = fsTypeDirectory
	}
	in.respondBridge(ctx, chatID, msg, body, nil)
}

// respondKiroFSReadDirectory answers `_kiro/fs/read_directory` with `{entries}`. A missing
// directory answers an empty list, matching KAS's NodeFileSystem.
func (in *inbound) respondKiroFSReadDirectory(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	root, rel, release, err := in.kiroFSReadablePath(msg)
	if err != nil {
		in.respondFSError(ctx, chatID, msg, err)
		return
	}
	defer release()
	dirEntries, err := readDirInRoot(root, rel)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			in.respondBridge(ctx, chatID, msg, kiroReadDirBody{Entries: []kiroDirEntry{}}, nil)
			return
		}
		in.respondFSError(ctx, chatID, msg, err)
		return
	}
	in.respondBridge(ctx, chatID, msg, kiroReadDirBody{
		Entries: dirEntriesToWire(dirEntries),
	}, nil)
}

// readDirInRoot lists the directory at rel (os.Root has no ReadDir). O_DIRECTORY refuses a
// non-directory; O_NONBLOCK keeps a reader-less FIFO from blocking open(2).
func readDirInRoot(root *os.Root, rel string) ([]os.DirEntry, error) {
	f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return f.ReadDir(-1)
}

// dirEntriesToWire maps os.DirEntry values onto the wire shape. Every entry travels: KAS's
// ignore evaluators cover the listed directory, not its entry names.
func dirEntriesToWire(dirEntries []os.DirEntry) []kiroDirEntry {
	out := make([]kiroDirEntry, 0, len(dirEntries))
	for _, e := range dirEntries {
		entryType := fsTypeFile
		switch {
		case e.IsDir():
			entryType = fsTypeDirectory
		case e.Type()&os.ModeSymlink != 0:
			// File.ReadDir does not follow symlinks, so this branch is live.
			entryType = fsTypeSymlink
		}
		out = append(out, kiroDirEntry{Name: e.Name(), Type: entryType})
	}
	return out
}

// respondKiroFSDelete answers `_kiro/fs/delete` with `{}`, recursive for a directory like
// KAS's NodeFileSystem; it refuses only the workspace root. Not gated: KAS checkpoints and reviews.
func (in *inbound) respondKiroFSDelete(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	p, err := kiroFSPath(msg)
	if err != nil {
		in.respondFSError(ctx, chatID, msg, err)
		return
	}
	root, rel, err := in.lifetime.confineInWorkDir(p)
	if err != nil {
		in.respondFSError(ctx, chatID, msg, err)
		return
	}
	// The workspace root, which has exactly one root-relative spelling.
	if rel == "." {
		in.respondFSError(ctx, chatID, msg, errRefusedWorkDirRoot)
		return
	}
	// A lost delete race is unrecoverable, so the parent is pinned with
	// atomicfile.OpenParentInRoot (Lstat each component, refuse symlinks, os.SameFile), and only
	// the final element is unlinked; root.Remove would follow an in-root link.
	parent, base, err := atomicfile.OpenParentInRoot(root, rel)
	if err != nil {
		in.respondFSError(ctx, chatID, msg, err)
		return
	}
	defer func() { _ = parent.Close() }()

	info, err := parent.Lstat(base)
	if err != nil {
		in.respondFSError(ctx, chatID, msg, err)
		return
	}
	// Remove unlinks a symlink rather than following it, matching KAS's fs.rm; RemoveFileInRoot
	// would refuse it (ErrNotRegular), a behaviour change.
	if info.IsDir() {
		err = parent.RemoveAll(base)
	} else {
		err = parent.Remove(base)
	}
	if err != nil {
		in.respondFSError(ctx, chatID, msg, err)
		return
	}
	slog.Info("agent deleted a path", "chat_id", chatID, "dir", info.IsDir())
	if specDir, ok := spec.DirOf(rel); ok {
		in.specs.Mark(specDir)
	}
	// KAS's isFSDeleteCapabilityResponse throws on a non-empty `message`, so success is an empty object.
	in.respondBridge(ctx, chatID, msg, struct{}{}, nil)
}
