package agent

import (
	"context"
	"log/slog"

	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/spec"
	"github.com/cplieger/marotte/internal/workspace"
)

// handleSpecPhaseCheckpoint marks the spec directory a `_kiro/spec/phaseCheckpoint` names dirty: KAS emits one
// per accepted in-process spec write, the second spec_changed producer. artifactPath is the raw tool path,
// confined to the workspace before spec.DirOf; outside or undecodable is dropped at Debug.
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
