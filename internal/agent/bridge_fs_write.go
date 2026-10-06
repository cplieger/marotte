// ACP fs write handlers: https://agentclientprotocol.com/protocol/file-system

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/filemode"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/spec"
)

// chmodInRoot is a test seam over os.Root.Chmod.
var chmodInRoot = (*os.Root).Chmod

// respondFSWrite handles fs/write_text_file:
//
//	{ sessionId, path, content: "..." }
//
// answering {} on success, capped at fsWriteCap, creating missing parents as 0o755.
// A write here is already authorized by KAS, including the revert of a rejected action:
// never gate, stage or attribute it, or the changed-files ledger double-counts.
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

	// Preserve an existing regular file's permission bits; a new file takes the directory's default.
	var opts []atomicfile.Option
	var restore os.FileMode
	if info, statErr := root.Lstat(rel); statErr == nil && info.Mode().IsRegular() {
		opts, restore = filemode.RewriteOptions(info.Mode().Perm())
	}
	// Missing parents are created through the root, so they stay confined.
	if dir := filepath.Dir(rel); dir != "." {
		if mkErr := root.MkdirAll(dir, 0o755); mkErr != nil {
			in.respondFSError(ctx, chatID, msg, mkErr)
			return
		}
	}

	// One confined atomic write: either the old bytes or all the new ones. A directory, FIFO,
	// device or socket at the target is refused up front.
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
	// A dirty bit keyed on the path, not attribution.
	if dir, ok := spec.DirOf(rel); ok {
		in.specs.Mark(dir)
	}
	in.respondBridge(ctx, chatID, msg, map[string]any{}, nil)
}

// There is no write gate: KAS reviews the whole turn and restores a rejected action from
// its own snapshot, so a marotte hold would desynchronise that restore.
