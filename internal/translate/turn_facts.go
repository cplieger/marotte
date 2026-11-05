package translate

import (
	"encoding/json"
	"math"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// pendingInteractionBlock is the kind=="pending_interaction" block: the ask a
// tool call raised, with the options it offered.
type pendingInteractionBlock struct {
	ToolCallID      string `json:"toolCallId"`
	InteractionType string `json:"interactionType"`
	Options         []struct {
		OptionID string `json:"optionId"`
		Name     string `json:"name"`
		Kind     string `json:"kind"`
	} `json:"options"`
}

// askOptions is the block's options in the turn log's vocabulary, each field
// bounded and single-line: the labels are model-authored.
func (b *pendingInteractionBlock) askOptions() []turnlog.AskOption {
	out := make([]turnlog.AskOption, 0, len(b.Options))
	for _, o := range b.Options {
		out = append(out, turnlog.AskOption{ID: o.OptionID, Kind: o.Kind, Name: displayText(o.Name)})
	}
	return out
}

// infoKindTurnTiming replays a turn's first-output stamps, and arrives live only when KAS could not persist them.
const infoKindTurnTiming = "turn_timing"

// acpTurnTiming is _meta.kiro.ttft: KAS's epoch-ms stamps for the turn's first displayed chunk.
// Its agentOutput interval list is not read: the turn's tool cards show that window.
type acpTurnTiming struct {
	PromptReceivedAt     *float64 `json:"promptReceivedAt"`
	ModelRequestSentAt   *float64 `json:"modelRequestSentAt"`
	FirstTokenReceivedAt *float64 `json:"firstTokenReceivedAt"`
	FirstChunkEmittedAt  *float64 `json:"firstChunkEmittedAt"`
}

// firstOutputFrom is the stamps as spans, nil unless the prompt's arrival and the first chunk
// are both stamped and ordered. A split is kept only when its own stamps fall in order inside
// that wait, so a skewed stamp withholds a span rather than inventing one.
func firstOutputFrom(t *acpTurnTiming) *marotte.TurnFirstOutput {
	if t == nil || !stamped(t.PromptReceivedAt) || !stamped(t.FirstChunkEmittedAt) {
		return nil
	}
	start, shown := *t.PromptReceivedAt, *t.FirstChunkEmittedAt
	if start > shown {
		return nil
	}
	fo := &marotte.TurnFirstOutput{TotalMs: int64(shown - start)}
	if !stamped(t.ModelRequestSentAt) {
		return fo
	}
	sent := *t.ModelRequestSentAt
	if start <= sent && sent <= shown {
		fo.SetupMs = int64(sent - start)
	}
	if stamped(t.FirstTokenReceivedAt) {
		if first := *t.FirstTokenReceivedAt; sent <= first && first <= shown {
			fo.ModelMs = int64(first - sent)
		}
	}
	return fo
}

func stamped(v *float64) bool {
	return v != nil && *v > 0 && !math.IsInf(*v, 0) && !math.IsNaN(*v)
}

// KAS sends each as its id string; an object form is read for its id.
func steeringDocIDs(raw []json.RawMessage) []string {
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		var id string
		if json.Unmarshal(r, &id) != nil {
			var doc struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(r, &doc) != nil {
				continue
			}
			id = doc.ID
		}
		if id = displayText(id); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// boundedIDs is a list of backend-supplied identifiers, each bounded and single
// line, empties dropped.
func boundedIDs(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = displayText(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// noteTurnFacts records the per-turn facts a session_info_update carries on the
// chat's own open turn: an ask, its answer, or the steering KAS added. Reports
// whether the frame was one of those.
func (t *Translator) noteTurnFacts(chatID marotte.ChatID, k *sessionInfoKiroBlock) bool {
	switch {
	case k.PendingInteraction != nil:
		if turn, ok := t.turns.OwnTurn(chatID); ok {
			p := k.PendingInteraction
			turn.NoteAsk(p.ToolCallID, p.InteractionType, p.askOptions())
		}
		return true
	case len(k.SteeringDocuments) > 0:
		if turn, ok := t.turns.OwnTurn(chatID); ok {
			turn.NoteSteering(steeringDocIDs(k.SteeringDocuments))
		}
		return true
	}
	return false
}

// A resolution always withdraws its pending ask; facts are recorded on the chat's own turn only (a
// workflow step's nowhere), and an answer to an unnoted ask records nothing.
func (t *Translator) handleAskInfo(chatID marotte.ChatID, k *sessionInfoKiroBlock, attr FrameAttribution) bool {
	if r := k.InteractionResolved; r != nil {
		t.handleInteractionResolved(chatID, r)
		t.noteAnswer(chatID, r)
		return true
	}
	return !attr.Step && t.noteTurnFacts(chatID, k)
}

func (t *Translator) noteAnswer(chatID marotte.ChatID, r *interactionResolvedBlock) {
	if turn, ok := t.turns.OwnTurn(chatID); ok {
		turn.NoteAnswer(r.ToolCallID, r.Outcome, displayText(r.SelectedOption))
	}
}
