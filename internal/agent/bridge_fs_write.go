// File-system write request handlers for kiro-cli ACP bridges.
//
// Spec: https://agentclientprotocol.com/protocol/file-system

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/spec"
)

// chmodInRoot is a test seam over os.Root.Chmod.
var chmodInRoot = (*os.Root).Chmod

// respondFSWrite handles fs/write_text_file. Request params:
//
//	{ sessionId, path, content: "..." }
//
// Response: empty object on success. Caps content at fsWriteCap. Creates
// missing parent directories as ordinary 0o755 directories.
//
// A write reaching this handler is already authorized (KAS gates the whole
// turn) and applies immediately — including the REVERT of a rejected
// action, sent back as an ordinary fs/write_text_file. Do not gate, stage,
// snapshot or attribute that write as agent work: it would double-count the
// changed-files ledger, and under any surviving gate it would deadlock.
func (in *inbound) respondFSWrite(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		in.respondFSError(ctx, chatID, msg, fmt.Errorf("parse params: %w", err))
		return
	}
	if p.Path == "" {
		in.respondFSError(ctx, chatID, msg, errors.New("path is required"))
		return
	}
	if len(p.Content) > fsWriteCap {
		in.respondFSError(ctx, chatID, msg, fmt.Errorf("%w: %d", errCapExceeded, fsWriteCap))
		return
	}
	root, rel, err := in.lifetime.confineInWorkDir(p.Path)
	if err != nil {
		in.respondFSError(ctx, chatID, msg, err)
		return
	}

	// Preserve the existing file's permission bits so the agent can't silently
	// demote a 0o755 script or promote a 0o600 secret. An owner-only mode is
	// enforced on the write; a wider one is restored afterwards, best effort,
	// so a workspace whose ACL widens new files cannot refuse the save. A new
	// file passes no mode; the directory decides. Lstat, so only a REGULAR
	// file's bits are adopted; atomicfile refuses a symlink, FIFO or device
	// at the target (ErrSymlinkTarget / ErrNotRegular).
	var opts []atomicfile.Option
	var restore os.FileMode
	if info, statErr := root.Lstat(rel); statErr == nil && info.Mode().IsRegular() {
		if perm := info.Mode().Perm(); perm&0o077 == 0 {
			opts = append(opts, atomicfile.WithMode(perm))
		} else {
			restore = perm
		}
	}
	// Missing parents are ordinary directories of the user's tree, created
	// through the root so they stay confined.
	if dir := filepath.Dir(rel); dir != "." {
		if mkErr := root.MkdirAll(dir, 0o755); mkErr != nil {
			in.respondFSError(ctx, chatID, msg, mkErr)
			return
		}
	}

	// One confined atomic write. Every component of rel is re-resolved inside
	// the root on every operation, so a swapped ancestor cannot redirect the
	// write. Temp-then-rename means the target is either the old bytes or all
	// of the new ones, never a truncated partial. A target occupied by a
	// directory, FIFO, device node or socket is refused up front rather than
	// opened (a FIFO's open(2) would otherwise block this handler indefinitely
	// under lifetime.inflight, against a KAS Call with no timeout).
	if _, wErr := atomicfile.WriteFileInRoot(ctx, root, rel, []byte(p.Content), opts...); wErr != nil {
		in.respondFSError(ctx, chatID, msg, wErr)
		return
	}
	if restore != 0 {
		if chErr := chmodInRoot(root, rel, restore); chErr != nil {
			slog.Debug("fs/write_text_file: could not restore the file's mode",
				"chat_id", chatID, "path", logsafe.Field(rel), "mode", restore, "error", logsafe.Field(chErr.Error()))
		}
	}
	// A dirty bit keyed on the path, not attribution: it touches no ledger, no
	// turn and no card.
	if dir, ok := spec.DirOf(rel); ok {
		in.specs.Mark(dir)
	}
	in.respondBridge(ctx, chatID, msg, map[string]any{}, nil)
}

// THERE IS NO WRITE GATE. KAS reviews a whole TURN (`autopilot: false` → a
// turn_approval permission request), so a write arriving here is already
// authorized and goes to disk unconditionally. Do not add a second gate: KAS
// restores a rejected action from its own snapshot, and a marotte-side hold
// would make that restore operate on content KAS never wrote.
