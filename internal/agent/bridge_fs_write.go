// ACP fs write handlers: https://agentclientprotocol.com/protocol/file-system

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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

// WriteHook governs the agent's fs/write_text_file writes of one file. Either func may be nil.
type WriteHook struct {
	// Check runs before anything is written; a non-nil error refuses the write, and its text is
	// the answer the agent reads. A restore KAS makes after a turn approval or during a rewind is
	// written even so (turnRegistry.expectRestores): refusing it would keep a change the user rejected.
	Check func(content []byte) error
	// Saved runs once a write is on disk, a restore Check refused included, before the agent is answered.
	Saved func()
}

// SetWriteHook runs hook on every agent write whose target is the file at path, an absolute
// path; a later hook for the same path replaces this one.
func (rt *Runtime) SetWriteHook(path string, hook WriteHook) {
	in := rt.inbound
	in.writeHooksMu.Lock()
	defer in.writeHooksMu.Unlock()
	if in.writeHooks == nil {
		in.writeHooks = make(map[string]WriteHook)
	}
	in.writeHooks[filepath.Clean(path)] = hook
}

// respondFSWrite handles fs/write_text_file:
//
//	{ sessionId, path, content: "..." }
//
// answering {} on success, capped at fsWriteCap, creating missing parents as 0o755.
// A write here is already authorized by KAS, including the revert of a rejected action:
// never stage or attribute it, or the changed-files ledger double-counts. The one refusal
// is a write hook's Check, which lets a restore through (WriteHook.Check).
func (in *inbound) respondFSWrite(ctx context.Context, chatID marotte.ChatID, origin acpResponder, msg *marotte.RPCResponse) {
	p, err := parseFSWriteParams(msg)
	if err != nil {
		in.respondFSError(ctx, chatID, origin, msg, err)
		return
	}
	root, rel, err := in.lifetime.confineInWorkDir(p.Path)
	if err != nil {
		in.respondFSError(ctx, chatID, origin, msg, err)
		return
	}
	hook := in.writeHookFor(root, rel)
	if in.refusedByWriteHook(ctx, chatID, origin, msg, hook, &p, rel) {
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
			in.respondFSError(ctx, chatID, origin, msg, mkErr)
			return
		}
	}

	// One confined atomic write: either the old bytes or all the new ones. A directory, FIFO,
	// device or socket at the target is refused up front.
	if _, wErr := atomicfile.WriteFileInRoot(ctx, root, rel, []byte(p.Content), opts...); wErr != nil {
		in.respondFSError(ctx, chatID, origin, msg, wErr)
		return
	}
	if restore != 0 {
		if chErr := chmodInRoot(root, rel, restore); chErr != nil {
			slog.Debug("fs/write_text_file: could not restore the file's mode",
				"chat_id", chatID, "path", logsafe.Field(rel), "mode", restore, "error", logsafe.Field(chErr.Error()))
		}
	}
	if hook.Saved != nil {
		hook.Saved()
	}
	// A dirty bit keyed on the path, not attribution.
	if dir, ok := spec.DirOf(rel); ok {
		in.specs.Mark(dir)
	}
	in.respondBridge(ctx, chatID, origin, msg, map[string]any{}, nil)
}

type fsWriteParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Content   string `json:"content"`
}

func parseFSWriteParams(msg *marotte.RPCResponse) (fsWriteParams, error) {
	var p fsWriteParams
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		return p, fmt.Errorf("parse params: %w", err)
	}
	if p.Path == "" {
		return p, errors.New("path is required")
	}
	if len(p.Content) > fsWriteCap {
		return p, fmt.Errorf("%w: %d", errCapExceeded, fsWriteCap)
	}
	return p, nil
}

func (in *inbound) refusedByWriteHook(ctx context.Context, chatID marotte.ChatID, origin acpResponder, msg *marotte.RPCResponse,
	hook WriteHook, p *fsWriteParams, rel string,
) bool {
	if hook.Check == nil {
		return false
	}
	err := hook.Check([]byte(p.Content))
	if err == nil {
		return false
	}
	if in.coord.turns.restoring(chatID, p.SessionID) {
		slog.Warn("fs/write_text_file: a KAS restore puts back content its write hook refuses",
			"chat_id", chatID, "path", logsafe.Field(rel), "reason", logsafe.Field(err.Error()))
		return false
	}
	slog.Info("fs/write_text_file refused by its write hook",
		"chat_id", chatID, "path", logsafe.Field(rel), "reason", logsafe.Field(err.Error()))
	in.respondBridge(ctx, chatID, origin, msg, nil, err)
	return true
}

// writeHookFor matches rel's directory by identity rather than by name, so a hook path spelled
// through a symlink, or an ancestor swapped after confinement, still matches; the leaf needs no
// resolving, because the atomic write refuses a symlink there. Directories missing on either side
// are compared by name below their nearest existing ancestor: the write recreates them, so a
// deleted config directory must not unhook its tools.json.
func (in *inbound) writeHookFor(root *os.Root, rel string) WriteHook {
	in.writeHooksMu.Lock()
	defer in.writeHooksMu.Unlock()
	if len(in.writeHooks) == 0 {
		return WriteHook{}
	}
	dir, missing, ok := existingAncestor(root.Stat, filepath.Dir(rel))
	if !ok {
		return WriteHook{}
	}
	for path, hook := range in.writeHooks {
		if filepath.Base(path) != filepath.Base(rel) {
			continue
		}
		hookDir, hookMissing, hookOK := existingAncestor(os.Stat, filepath.Dir(path))
		if hookOK && hookMissing == missing && os.SameFile(dir, hookDir) {
			return hook
		}
	}
	return WriteHook{}
}

// existingAncestor stats dir's nearest existing ancestor and returns the path below it that does
// not exist yet; ok is false when a stat fails for any reason but absence.
func existingAncestor(stat func(string) (os.FileInfo, error), dir string) (info os.FileInfo, missing string, ok bool) {
	for {
		info, err := stat(dir)
		if err == nil {
			return info, missing, true
		}
		parent := filepath.Dir(dir)
		if !errors.Is(err, fs.ErrNotExist) || parent == dir {
			return nil, "", false
		}
		missing = filepath.Join(filepath.Base(dir), missing)
		dir = parent
	}
}

// There is no approval gate: KAS reviews the whole turn and restores a rejected action from
// its own snapshot, so a marotte hold would desynchronise that restore.
