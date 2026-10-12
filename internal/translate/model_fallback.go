package translate

import (
	"context"
	"log/slog"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// infoKindModelFallback is KAS's fallback decision for a refused or unavailable model, live and
// replayed. Its fields sit flat beside the kind, so it dispatches on the kind STRING. KAS flushes
// the refused attempt's say before sending it, so the frame is a boundary.
const infoKindModelFallback = "model_fallback"

// modelFallbackFields are model_fallback's members, FLAT beside the kind in the live and the
// replayed frame alike. AgentSubtaskID is set on a sub-agent's fallback.
type modelFallbackFields struct {
	From           string `json:"from"`
	To             string `json:"to"`
	Scope          string `json:"scope"`
	Trigger        string `json:"trigger"`
	Reason         string `json:"reason"`
	AgentSubtaskID string `json:"agentSubtaskId"`
}

// modelFallbackPayload is the decode door for a model_fallback frame, shared by the live and
// replay paths so the two payloads are byte-identical.
func modelFallbackPayload(k *modelFallbackFields) marotte.EntryModelFallback {
	return marotte.EntryModelFallback{
		From:     displayText(k.From),
		To:       displayText(k.To),
		Scope:    displayText(k.Scope),
		Trigger:  displayText(k.Trigger),
		Reason:   displayText(k.Reason),
		Subagent: k.AgentSubtaskID != "",
	}
}

func (t *Translator) handleModelFallback(ctx context.Context, chatID marotte.ChatID, k *sessionInfoKiroBlock) {
	p := modelFallbackPayload(&k.modelFallbackFields)
	t.appendLaneless(ctx, chatID, marotte.EntryKindModelFallback, "", p,
		func(ctx context.Context, turn *turnlog.Turn) ([]turnlog.Sealed, error) {
			return turn.ModelFallback(ctx, &p)
		})
}

// stepModelFallback files a step's model_fallback in the step's run turn and reports whether the
// frame was one. A path whose turn already closed drops it.
func (t *Translator) stepModelFallback(ctx context.Context, chatID marotte.ChatID, attr FrameAttribution, k *sessionInfoKiroBlock) bool {
	if k.Kind != infoKindModelFallback {
		return false
	}
	turn, ok := t.runs.RunFoldTarget(ctx, runStepOf(&attr), chatID)
	if !ok {
		slog.Debug("run log: model_fallback for a step path with no open turn, dropped",
			"workflow_id", attr.RunID, "node_path", attr.NodePath)
		return true
	}
	sc := runScope(attr.RunID)
	p := modelFallbackPayload(&k.modelFallbackFields)
	sealed, err := turn.ModelFallback(ctx, &p)
	t.publishSealed(ctx, sc, sealed)
	if err != nil {
		appendFailed(sc, string(marotte.EntryKindModelFallback), err)
	}
	return true
}
