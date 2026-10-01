package agent

import (
	"context"
	"log/slog"

	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/spec"
	"github.com/cplieger/marotte/internal/workspace"
)

// handleSpecPhaseCheckpoint marks the spec directory a `_kiro/spec/phaseCheckpoint`
// notification names dirty. KAS emits one for every accepted in-process write of a
// spec document in a spec-mode session, so it is the second spec_changed producer
// beside the bridge's own fs handlers. artifactPath is the tool's raw input path,
// absolute or cwd-relative, and is confined to the workspace before spec.DirOf
// sees it; a path outside, or an undecodable payload, is dropped at Debug.
func (rt *Runtime) handleSpecPhaseCheckpoint(_ context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	var p struct {
		ArtifactPath string `json:"artifactPath"`
	}
	if err := parseRequest(msg, &p); err != nil {
		slog.Debug("spec checkpoint: unreadable payload", "chat_id", chatID, "error", logsafe.Field(err.Error()))
		return
	}
	if p.ArtifactPath == "" {
		slog.Debug("spec checkpoint: no artifactPath", "chat_id", chatID)
		return
	}
	abs, err := rt.lifecycle.resolveInsideWorkDir(p.ArtifactPath)
	if err != nil {
		slog.Debug("spec checkpoint: path outside the workspace", "chat_id", chatID,
			"path", logsafe.Field(p.ArtifactPath), "error", logsafe.Field(err.Error()))
		return
	}
	rel, err := workspace.RelPath(rt.lifecycle.workDir, abs)
	if err != nil {
		slog.Debug("spec checkpoint: path outside the workspace", "chat_id", chatID,
			"path", logsafe.Field(p.ArtifactPath), "error", logsafe.Field(err.Error()))
		return
	}
	if dir, ok := spec.DirOf(rel); ok {
		rt.specs.Mark(dir)
	}
}
