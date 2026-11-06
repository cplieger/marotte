package translate

import (
	"context"
	"path"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// steeringTitlePrefix opens a steering box's title; the client draws the same words.
const steeringTitlePrefix = "Steering loaded: "

// steeringDocName is how a box names one document: a file URI's percent-decoded base name,
// any other id as KAS wrote it.
func steeringDocName(id string) string {
	p := localPath(id)
	if p == id {
		return id
	}
	if base := path.Base(p); base != "/" && base != "." {
		return base
	}
	return id
}

// steeringToolCall is the box for one steering_inclusion, shared by the live and replay paths so
// the two payloads are byte-identical. The caller mints the id.
func steeringToolCall(docs []string, ts int64) marotte.EntryToolCall {
	names := make([]string, len(docs))
	for i, id := range docs {
		names[i] = steeringDocName(id)
	}
	return marotte.EntryToolCall{
		Title:        displayText(steeringTitlePrefix + strings.Join(names, ", ")),
		Kind:         marotte.ToolKindSteering,
		Status:       marotte.ToolCompleted,
		SteeringDocs: docs,
		Ts:           ts,
	}
}

// handleSteeringInclusion records the box where the frame falls in the stream, in its subagent's
// lane, and feeds the footer list of the turn that holds it (a step's run turn, else the chat's).
func (t *Translator) handleSteeringInclusion(ctx context.Context, chatID marotte.ChatID, attr FrameAttribution, k *sessionInfoKiroBlock) {
	docs := steeringDocIDs(k.SteeringDocuments)
	if len(docs) == 0 {
		return
	}
	turn, sc, ok := t.foldTarget(ctx, chatID, attr)
	if !ok {
		return
	}
	turn.NoteSteering(docs)
	call := steeringToolCall(docs, time.Now().UnixMilli())
	sealed, err := turn.SteeringLoaded(ctx, k.subtask(), &call)
	t.publishSealed(ctx, sc, sealed)
	if err != nil {
		appendFailed(sc, "steering tool_call", err)
	}
}

// ingestSteering is handleSteeringInclusion's replay twin. A set KAS persisted ahead of its
// prompt (the session-start set, a turn's new documents) waits for the turn that prompt opens,
// as live it lands right after that turn_open.
func (p *EntryProjection) ingestSteering(k *replayInfoKiro) {
	docs := steeringDocIDs(k.SteeringDocuments)
	if len(docs) == 0 {
		return
	}
	box := heldSteering{lane: k.subtask(), docs: docs, stamp: k.Timestamp}
	if p.betweenTurns() {
		p.heldSteering = append(p.heldSteering, box)
		return
	}
	p.flushUser()
	p.appendSteering(box)
}

// betweenTurns reports that no running turn can take an entry yet: none is open, the newest one
// ended, or a prompt row is waiting to open the next.
func (p *EntryProjection) betweenTurns() bool {
	return p.cur == nil || p.closed || p.prompt != nil || (p.userPending && !p.userSteer)
}

type heldSteering struct {
	lane  string
	stamp string
	docs  []string
}

func (p *EntryProjection) appendSteering(box heldSteering) {
	p.sealLane(box.lane)
	p.facts.footer.NoteSteering(box.docs)
	id := marotte.SteeringCallIDPrefix + p.newID()
	call := steeringToolCall(box.docs, p.frameTS(box.stamp))
	call.ID = id
	p.appendEntry(marotte.EntryKindToolCall, id, box.lane, call.Ts, call)
}

// drainSteering lands the held sets in the newest turn, in arrival order.
func (p *EntryProjection) drainSteering() {
	held := p.heldSteering
	p.heldSteering = nil
	for _, box := range held {
		p.appendSteering(box)
	}
}
