package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workspace"
)

// fsReadCap caps text reads at 8 MiB: every KAS read_file and str_replace pre-read.
const fsReadCap = 8 << 20

// fsWriteCap caps one write at 4 MiB; KAS composes every structured edit into a whole-file
// fs/write_text_file here.
const fsWriteCap = 4 << 20

// Routine fs rejections, logged at Debug.
var (
	errCapExceeded    = errors.New("file exceeds byte cap")
	errRejectedByUser = errors.New("change rejected by user")
)

// errNoWorkRoot means the workspace could not be opened as a confined root. Not routine.
var errNoWorkRoot = errors.New("workspace is not open for confined access")

// Follows symlinks on the target and its parent, and is a verdict only: filesystem operations use
// confineInWorkDir, which pairs the verdict with a handle.
func (lt *lifetime) resolveInsideWorkDir(p string) (string, error) {
	return workspace.ResolveInsideAbs(lt.workDir, p)
}

// confineInWorkDir returns the workspace root and p's root-relative name, so every component
// is re-resolved per operation: an agent can swap an ancestor for a symlink between
// verdict and operation. A symlink staying inside the workspace is still followed; delete
// descends with atomicfile.OpenParentInRoot instead.
func (lt *lifetime) confineInWorkDir(p string) (*os.Root, string, error) {
	if lt.workRoot == nil {
		return nil, "", errNoWorkRoot
	}
	abs, err := lt.resolveInsideWorkDir(p)
	if err != nil {
		return nil, "", err
	}
	rel, err := workspace.RelPath(lt.workDir, abs)
	if err != nil {
		return nil, "", fmt.Errorf("workspace-relative path for %q: %w", p, err)
	}
	return lt.workRoot, rel, nil
}

// confineReadable is confineInWorkDir widened, for the READ verbs only, to an absolute path
// under uploadsDir: a prompt names a composer upload as `Attached file: <path>` and the agent
// is told to read it with its file tools. That root is opened per call; the caller runs
// release. A path neither root holds reports the workspace's error.
func (lt *lifetime) confineReadable(p string) (root *os.Root, rel string, release func(), err error) {
	root, rel, err = lt.confineInWorkDir(p)
	if err == nil {
		return root, rel, func() {}, nil
	}
	if lt.uploadsDir == "" || !filepath.IsAbs(p) {
		return nil, "", nil, err
	}
	if _, rel, upErr := workspace.ConfineAnyAbs([]string{lt.uploadsDir}, p); upErr == nil {
		up, openErr := os.OpenRoot(lt.uploadsDir)
		if openErr != nil {
			return nil, "", nil, fmt.Errorf("open uploads folder: %w", openErr)
		}
		return up, rel, func() { _ = up.Close() }, nil
	}
	return nil, "", nil, err
}

// respondFSError answers an fs request with a JSON-RPC error and logs it: routine
// rejections at Debug, real failures at Warn.
func (in *inbound) respondFSError(ctx context.Context, chatID marotte.ChatID, origin acpResponder, msg *marotte.RPCResponse, err error) {
	safe := logsafe.Field(err.Error())
	if fsErrorIsRoutine(err) {
		slog.Debug("fs request denied", "chat_id", chatID, "method", msg.Method, "error", safe)
	} else {
		slog.Warn("fs request failed", "chat_id", chatID, "method", msg.Method, "error", safe)
	}
	in.respondBridge(ctx, chatID, origin, msg, nil, err)
}

func fsErrorIsRoutine(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, errCapExceeded) ||
		errors.Is(err, errRejectedByUser)
}

func (in *inbound) respondBridge(ctx context.Context, chatID marotte.ChatID, origin acpResponder, msg *marotte.RPCResponse, result any, err error) {
	if msg.ID == nil {
		slog.Warn("fs request missing id", "chat_id", chatID, "method", msg.Method)
		return
	}
	if wErr := respondOn(ctx, in.coord.bridge.mgr, chatID, origin, msg, result, err); wErr != nil {
		slog.Error("fs response write failed", "chat_id", chatID, "method", msg.Method, "error", wErr)
	}
}

func parseRequest(msg *marotte.RPCResponse, v any) error {
	if msg.Params == nil {
		return io.ErrUnexpectedEOF
	}
	return json.Unmarshal(msg.Params, v)
}

// respondOn answers msg on origin, the bridge it arrived on, never on whatever bridge chatID maps to
// at answer time: KAS settles a response by its per-connection request id, so an answer redirected to
// a successor resolves an unrelated request pending there. A write that fails because origin exited
// or is no longer chatID's bridge is the expected loss at its end and drops at Debug; other failures
// return.
func respondOn(ctx context.Context, bridges *bridgeManager, chatID marotte.ChatID, origin acpResponder,
	msg *marotte.RPCResponse, result any, err error,
) error {
	if msg.ID == nil || origin == nil {
		return nil
	}
	wErr := origin.Respond(ctx, *msg.ID, result, err)
	if wErr != nil && (errors.Is(wErr, marotte.ErrBridgeExited) || !bridges.carries(chatID, origin)) {
		slog.Debug("answer dropped: the bridge it arrived on has ended",
			"chat_id", chatID, "method", msg.Method, "error", wErr)
		return nil
	}
	return wErr
}
