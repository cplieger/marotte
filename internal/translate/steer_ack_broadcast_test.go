package translate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// steerAcksOf decodes every steer_ack entry in entries, in seal order.
func steerAcksOf(t *testing.T, entries []marotte.Entry) []marotte.EntrySteerAck {
	t.Helper()
	var out []marotte.EntrySteerAck
	for i := range entries {
		if entries[i].Kind != marotte.EntryKindSteerAck {
			continue
		}
		var a marotte.EntrySteerAck
		if err := json.Unmarshal(entries[i].Payload, &a); err != nil {
			t.Fatalf("decode steer_ack %q: %v", entries[i].ID, err)
		}
		out = append(out, a)
	}
	return out
}

// feedChunk streams one text delta through the live handler.
func feedChunk(t *testing.T, tr *Translator, chatID marotte.ChatID, text string) {
	t.Helper()
	feedLaneChunk(t, tr, chatID, "", text)
}

// feedLaneChunk streams one text delta attributed to lane, a delegate's subtask
// id; an empty lane is the agent's own stream.
func feedLaneChunk(t *testing.T, tr *Translator, chatID marotte.ChatID, lane, text string) {
	t.Helper()
	body := map[string]any{
		"content": map[string]any{"type": "text", "text": text},
	}
	if lane != "" {
		body["_meta"] = map[string]any{"kiro": map[string]any{"agentSubtaskId": lane}}
	}
	tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, body), false, FrameAttribution{})
}

// The agent's own statement about a steer reaches the record instead of being
// discarded with the marker: a steer_ack entry naming the steer, carrying the
// sentence and nothing of the reader's words, announced as entry_appended.
func TestHandleAssistantChunk_RecordsTheAgentsAcknowledgement(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	chatID := marotte.ChatID("c1")

	feedChunk(t, tr, chatID, "Done. [STEERING steer-abc: rebased onto main instead]")

	acks := steerAcksOf(t, deps.chatEntries(chatID))
	if len(acks) != 1 {
		t.Fatalf("got %d steer_ack entries, want 1: %v", len(acks), eventTypes(*events))
	}
	want := marotte.EntrySteerAck{SteerID: "steer-abc", Text: "rebased onto main instead"}
	if acks[0] != want {
		t.Errorf("steer_ack = %+v, want %+v", acks[0], want)
	}
	if !hasEntryAppended(events, marotte.EntryKindSteerAck) {
		t.Errorf("no entry_appended{steer_ack} frame: got %v", eventTypes(*events))
	}
	if id := marotte.SteerAckID("steer-abc"); !hasEntryID(deps.chatEntries(chatID), id) {
		t.Errorf("steer_ack entry id is not %q: %v", id, entryIDs(deps.chatEntries(chatID)))
	}
}

// The marker closing a response usually arrives as its OWN delta, and that delta
// emits no text — so the handler returns early. This is the case an append placed
// after that return would silently never serve. The ack seals the text it
// followed, so the record reads text then steer_ack, and the marker itself
// reaches neither.
func TestHandleAssistantChunk_AcknowledgementSurvivesAMarkerOnlyDelta(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	chatID := marotte.ChatID("c1")

	feedChunk(t, tr, chatID, "All set.")
	feedChunk(t, tr, chatID, "[STEERING steer-solo: switched to the new API]")

	entries := deps.chatEntries(chatID)
	kinds := entryKinds(entries)
	if want := []marotte.EntryKind{marotte.EntryKindText, marotte.EntryKindSteerAck}; !equalKinds(kinds, want) {
		t.Fatalf("sealed kinds = %v, want %v (events %v)", kinds, want, eventTypes(*events))
	}
	if got := textOf(t, entries[0]); got != "All set." {
		t.Errorf("sealed text = %q, want %q with the marker stripped", got, "All set.")
	}
	if acks := steerAcksOf(t, entries); acks[0].Text != "switched to the new API" {
		t.Errorf("ack text = %q", acks[0].Text)
	}
	if open := deps.turns.chats[chatID].OpenEntries(); len(open) != 0 {
		t.Errorf("open entries after the ack = %+v, want none", open)
	}
}

// Split across chunk boundaries the ack lands exactly once, on the delta that
// closes the marker, and the prose on either side of it stays prose: the text
// before it is sealed by the ack, the text after it opens a new entry.
func TestHandleAssistantChunk_AcknowledgementLandsOnceWhenSplit(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	chatID := marotte.ChatID("c1")

	for _, part := range []string{"ok ", "[STEERING ste", "er-split: kept the ", "existing shape]", " bye"} {
		feedChunk(t, tr, chatID, part)
	}

	entries := deps.chatEntries(chatID)
	acks := steerAcksOf(t, entries)
	if len(acks) != 1 {
		t.Fatalf("got %d steer_ack entries, want exactly 1: %+v", len(acks), acks)
	}
	want := marotte.EntrySteerAck{SteerID: "steer-split", Text: "kept the existing shape"}
	if acks[0] != want {
		t.Errorf("steer_ack = %+v, want %+v", acks[0], want)
	}
	if kinds := entryKinds(entries); !equalKinds(kinds, []marotte.EntryKind{marotte.EntryKindText, marotte.EntryKindSteerAck}) {
		t.Errorf("sealed kinds = %v, want [text steer_ack]", kinds)
	}
	if got := textOf(t, entries[0]); got != "ok " {
		t.Errorf("text before the ack = %q, want %q", got, "ok ")
	}
	open := deps.turns.chats[chatID].OpenEntries()
	if len(open) != 1 || open[0].Kind != marotte.EntryKindText || open[0].Text != " bye" {
		t.Errorf("open after the ack = %+v, want one text entry %q", open, " bye")
	}
}

// A delegate's chunk keeps its lane, so the ack sits in the delegate's own stream
// and the entry_appended frame names that lane: the client positions it inside
// the reply it interrupted, not the agent's.
func TestHandleAssistantChunk_AcknowledgementKeepsTheDelegatesLane(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	chatID := marotte.ChatID("c1")

	feedLaneChunk(t, tr, chatID, "sub-1", "on it [STEERING steer-d: reran the suite]")

	entries := deps.chatEntries(chatID)
	var ack *marotte.Entry
	for i := range entries {
		if entries[i].Kind == marotte.EntryKindSteerAck {
			ack = &entries[i]
		}
	}
	if ack == nil {
		t.Fatalf("no steer_ack entry: %v", entryKinds(entries))
	}
	if ack.Lane != "sub-1" {
		t.Errorf("steer_ack lane = %q, want sub-1", ack.Lane)
	}
	for _, e := range *events {
		p, ok := e.Payload.(marotte.EntryAppendedPayload)
		if !ok || p.Entry.Kind != marotte.EntryKindSteerAck {
			continue
		}
		if p.Entry.Lane != "sub-1" {
			t.Errorf("entry_appended lane = %q, want sub-1", p.Entry.Lane)
		}
		return
	}
	t.Errorf("no entry_appended{steer_ack} frame: %v", eventTypes(*events))
}

// Reasoning is not screened for markers at all (KAS's own recordSteeringAcks
// reads text entries only), so a marker-shaped string in a thought must not
// record an ack for a steer nothing answered, and the thought keeps its bytes.
func TestHandleAssistantChunk_ReasoningYieldsNoAcknowledgement(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	chatID := marotte.ChatID("c1")

	const thought = "[STEERING steer-x: a thought]"
	tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": thought},
	}), true, FrameAttribution{})

	if acks := steerAcksOf(t, deps.chatEntries(chatID)); len(acks) != 0 {
		t.Errorf("got %d steer_ack entries from reasoning, want none: %+v", len(acks), acks)
	}
	if hasEntryAppended(events, marotte.EntryKindSteerAck) {
		t.Errorf("an entry_appended{steer_ack} frame left a reasoning chunk: %v", eventTypes(*events))
	}
	open := deps.turns.chats[chatID].OpenEntries()
	if len(open) != 1 || open[0].Kind != marotte.EntryKindThinking || open[0].Text != thought {
		t.Errorf("open after the thought = %+v, want one thinking entry holding %q verbatim", open, thought)
	}
}

// An empty body is not an answer. The marker `[STEERING steer-1: ]` cannot match
// the pattern at all (it requires at least one body character), but a body of
// pure whitespace trims to nothing, and a steer_ack with no text is a row that
// says the agent said nothing.
func TestHandleAssistantChunk_EmptyAcknowledgementIsNotRecorded(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	chatID := marotte.ChatID("c1")

	feedChunk(t, tr, chatID, "done [STEERING steer-blank:    ]")

	if acks := steerAcksOf(t, deps.chatEntries(chatID)); len(acks) != 0 {
		t.Errorf("got %d steer_ack entries for a whitespace body, want none: %+v", len(acks), acks)
	}
	if hasEntryAppended(events, marotte.EntryKindSteerAck) {
		t.Errorf("an entry_appended{steer_ack} frame for a whitespace body: %v", eventTypes(*events))
	}
	open := deps.turns.chats[chatID].OpenEntries()
	if len(open) != 1 || strings.TrimSpace(open[0].Text) != "done" {
		t.Errorf("open after the stripped marker = %+v, want the one text entry %q", open, "done")
	}
}

// textOf decodes the text of one sealed text entry.
func textOf(t *testing.T, e marotte.Entry) string {
	t.Helper()
	var p marotte.EntryText
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatalf("decode text %q: %v", e.ID, err)
	}
	return p.Text
}

func hasEntryID(entries []marotte.Entry, id string) bool {
	for i := range entries {
		if entries[i].ID == id {
			return true
		}
	}
	return false
}

func entryIDs(entries []marotte.Entry) []string {
	out := make([]string, 0, len(entries))
	for i := range entries {
		out = append(out, entries[i].ID)
	}
	return out
}

func equalKinds(got, want []marotte.EntryKind) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
