package translate

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// infoKindModelFallback is KAS's fallback decision for a refused or unavailable model, live and
// replayed. Its fields sit flat beside the kind, so it dispatches on the kind STRING. KAS flushes
// the refused attempt's say before sending it, so the frame is a boundary.
const infoKindModelFallback = "model_fallback"

// infoKindModelFallbackPromoted says a refused model's fallback now serves the session. The
// kiro-cli 2.30 TUI reads it with model_fallback's fields; no engine through 2.31 emits it.
const infoKindModelFallbackPromoted = "model_fallback_promoted"

func isModelFallbackKind(kind string) bool {
	return kind == infoKindModelFallback || kind == infoKindModelFallbackPromoted
}

// modelFallbackFields are both fallback kinds' members, FLAT beside the kind in the live and the
// replayed frame alike. AgentSubtaskID stays raw: a sub-agent's fallback is one carrying it as any
// string, "" included, which a decoded string cannot tell from an absent member.
type modelFallbackFields struct {
	From           string          `json:"from"`
	To             string          `json:"to"`
	Scope          string          `json:"scope"`
	Trigger        string          `json:"trigger"`
	Reason         string          `json:"reason"`
	AgentSubtaskID json.RawMessage `json:"agentSubtaskId"`
}

// ofSubagent reports whether the frame carries a string agentSubtaskId, the kiro-cli TUI's test.
func (k *modelFallbackFields) ofSubagent() bool {
	return len(k.AgentSubtaskID) > 0 && k.AgentSubtaskID[0] == '"'
}

// modelFallbackPayload is the decode door for both fallback kinds, shared by the live and replay
// paths so the two payloads are byte-identical. It reports false for a frame the transcript does
// not show, as the TUI drops it: a sub-agent's fallback, which never moves the chat's model, and a
// promotion with no target or a trigger other than a refusal. A promotion reads as a session-wide
// fallback.
func modelFallbackPayload(kind string, k *modelFallbackFields) (marotte.EntryModelFallback, bool) {
	if k.ofSubagent() {
		return marotte.EntryModelFallback{}, false
	}
	p := marotte.EntryModelFallback{
		From:    displayText(k.From),
		To:      displayText(k.To),
		Scope:   displayText(k.Scope),
		Trigger: displayText(k.Trigger),
		Reason:  displayText(k.Reason),
	}
	if kind == infoKindModelFallbackPromoted {
		if p.To == "" || k.Trigger != "refusal" {
			return marotte.EntryModelFallback{}, false
		}
		p.Scope, p.Reason = marotte.ModelFallbackScopeSession, ""
	}
	return p, true
}

func (t *Translator) handleModelFallback(ctx context.Context, chatID marotte.ChatID, k *sessionInfoKiroBlock) {
	p, ok := modelFallbackPayload(k.Kind, &k.modelFallbackFields)
	if !ok {
		slog.Debug("model fallback notice not shown", "chat_id", chatID, "kind", k.Kind)
		return
	}
	t.appendLaneless(ctx, chatID, marotte.EntryKindModelFallback, "", p,
		func(ctx context.Context, turn *turnlog.Turn) ([]turnlog.Sealed, error) {
			return turn.ModelFallback(ctx, &p)
		})
}

// stepModelFallback files a step's model_fallback in the step's run turn and reports whether the
// frame was one. A path whose turn already closed drops it.
func (t *Translator) stepModelFallback(ctx context.Context, chatID marotte.ChatID, attr FrameAttribution, k *sessionInfoKiroBlock) bool {
	if !isModelFallbackKind(k.Kind) {
		return false
	}
	p, show := modelFallbackPayload(k.Kind, &k.modelFallbackFields)
	if !show {
		return true
	}
	turn, ok := t.runs.RunFoldTarget(ctx, runStepOf(&attr), chatID)
	if !ok {
		slog.Debug("run log: model_fallback for a step path with no open turn, dropped",
			"workflow_id", attr.RunID, "node_path", attr.NodePath)
		return true
	}
	sc := runScope(attr.RunID)
	sealed, err := turn.ModelFallback(ctx, &p)
	t.publishSealed(ctx, sc, sealed)
	if err != nil {
		appendFailed(sc, string(marotte.EntryKindModelFallback), err)
	}
	return true
}
