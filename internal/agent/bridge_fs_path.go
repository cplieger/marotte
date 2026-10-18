package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

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
// confine, which pairs the verdict with a handle.
func (lt *lifetime) resolveInsideWorkDir(p string) (string, error) {
	return workspace.ResolveInsideAbs(lt.workDir, p)
}

// fsAccess is what a verb does at a path. Only a read reaches the uploads folder.
type fsAccess int

const (
	fsWrite fsAccess = iota
	fsRead
)

// fsTarget is a path confined to one root: every operation goes through root with rel, so each
// component is re-resolved per operation and an agent swapping an ancestor for a symlink between
// verdict and operation reaches nothing outside it. A symlink staying inside the root is still
// followed; delete descends with atomicfile.OpenParentInRoot instead. The caller runs release.
type fsTarget struct {
	root *os.Root
	rel  string
	// inWorkspace reports rel is workspace-relative, the one tree whose names marotte reads
	// (spec directories).
	inWorkspace bool
	opened      bool
}

func (t fsTarget) release() {
	if t.opened {
		_ = t.root.Close()
	}
}

// confine resolves p into the workspace, or, for an absolute p, into the first granted
// additional directory or (reads only) the uploads folder holding it; a prompt names a composer
// upload as `Attached file: <path>`. A relative p resolves against the workspace only. A root
// other than the workspace is opened for the call. A path no root holds reports the
// workspace's error.
func (lt *lifetime) confine(ctx context.Context, p string, access fsAccess) (fsTarget, error) {
	roots := append([]string{lt.workDir}, lt.grantedDirs(ctx)...)
	if access == fsRead && lt.uploadsDir != "" {
		roots = append(roots, lt.uploadsDir)
	}
	won, rel, err := workspace.ConfineAnyAbs(roots, p)
	if err != nil {
		return fsTarget{}, err
	}
	if won == lt.workDir {
		if lt.workRoot == nil {
			return fsTarget{}, errNoWorkRoot
		}
		return fsTarget{root: lt.workRoot, rel: rel, inWorkspace: true}, nil
	}
	root, err := os.OpenRoot(won)
	if err != nil {
		return fsTarget{}, fmt.Errorf("open %s: %w", won, err)
	}
	return fsTarget{root: root, rel: rel, opened: true}, nil
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

// respondOn answers msg on origin, the session it arrived on, never on whatever session chatID maps to
// at answer time: KAS settles a response by its request id on the connection that asked, and a restarted
// engine numbers its requests from zero, so an answer redirected across a restart resolves an unrelated
// request. A write that fails because origin exited
// or is no longer chatID's session is the expected loss at its end and drops at Debug; other failures
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
