package translate

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// replayFixture is one hand-written session/load frame stream on disk. The extra
// fields are the fixture's own statement of what the assertions must derive from the
// same bytes, so a fixture edit cannot silently invalidate a test.
type replayFixture struct {
	Note           string               `json:"note"`
	Summary        string               `json:"summary"`
	SteerID        string               `json:"steer_id"`
	ActionID       string               `json:"action_id"`
	ReplayedCallID string               `json:"replayed_call_id"`
	DelegateID     string               `json:"delegate_id"`
	Frames         []replayFixtureFrame `json:"frames"`
}

type replayFixtureFrame struct {
	Kind   marotte.ACPUpdateKind `json:"kind"`
	Update json.RawMessage       `json:"update"`
}

// loadReplayFixture reads one frame stream out of testdata.
func loadReplayFixture(t *testing.T, name string) replayFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read replay fixture %s: %v", name, err)
	}
	var fx replayFixture
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatalf("parse replay fixture %s: %v", name, err)
	}
	if len(fx.Frames) == 0 {
		t.Fatalf("replay fixture %s carries no frames", name)
	}
	return fx
}

// projectFixture folds a fixture's whole frame stream into a fresh EntryProjection.
func projectFixture(t *testing.T, fx replayFixture) []ProjectedTurn {
	t.Helper()
	p := NewEntryProjection(seqIDs(), "")
	for _, f := range fx.Frames {
		p.Ingest(f.Kind, f.Update)
	}
	return p.Turns()
}

// entryProject folds a frame list built by the helpers in projection_entries_helpers_test.go.
func entryProject(frames [][2]any) []ProjectedTurn {
	p := NewEntryProjection(seqIDs(), "")
	for _, f := range frames {
		p.Ingest(f[0].(marotte.ACPUpdateKind), f[1].(json.RawMessage))
	}
	return p.Turns()
}

// dumpTurns renders projected turns as `kind[lane] id` lines, so a failure names the
// shape rather than a byte count.
func dumpTurns(turns []ProjectedTurn) string {
	var b strings.Builder
	for i := range turns {
		fmt.Fprintf(&b, "turn %d:\n", i)
		for _, e := range turns[i].Entries {
			fmt.Fprintf(&b, "  %s[%s] %s %s\n", e.Kind, e.Lane, e.ID, e.Payload)
		}
	}
	return b.String()
}

// kindsOf is one turn's entry kinds in order.
func kindsOf(turn ProjectedTurn) []marotte.EntryKind {
	kinds := make([]marotte.EntryKind, 0, len(turn.Entries))
	for _, e := range turn.Entries {
		kinds = append(kinds, e.Kind)
	}
	return kinds
}

// saysOf is the text of every text entry of one turn, in order.
func saysOf(t *testing.T, turn ProjectedTurn) []string {
	t.Helper()
	texts := make([]string, 0, len(turn.Entries))
	for _, e := range turn.Entries {
		if e.Kind != marotte.EntryKindText {
			continue
		}
		var payload marotte.EntryText
		payloadOf(t, e, &payload)
		texts = append(texts, payload.Text)
	}
	return texts
}

// entryOfKind is a turn's first entry of one kind.
func entryOfKind(t *testing.T, turn ProjectedTurn, kind marotte.EntryKind) marotte.Entry {
	t.Helper()
	for _, e := range turn.Entries {
		if e.Kind == kind {
			return e
		}
	}
	t.Fatalf("turn holds no %s entry:\n%s", kind, dumpTurns([]ProjectedTurn{turn}))
	return marotte.Entry{}
}

// payloadOf decodes one entry's payload into v.
func payloadOf(t *testing.T, e marotte.Entry, v any) {
	t.Helper()
	if err := json.Unmarshal(e.Payload, v); err != nil {
		t.Fatalf("parse %s payload %s: %v", e.Kind, e.Payload, err)
	}
}

// TestEntryProjection_ACompactionMidBracketKeepsOneTurnAndPairsByItsSummary pins the projected
// compaction id equal to marotte.CompactionEntryID over the same summary (one entry to the
// merge), and that the separator closes NO turn.
func TestEntryProjection_ACompactionMidBracketKeepsOneTurnAndPairsByItsSummary(t *testing.T) {
	fx := loadReplayFixture(t, "replay_compaction.json")
	turns := projectFixture(t, fx)

	if len(turns) != 1 {
		t.Fatalf("projected %d turns, want 1 — the separator must not close the bracket:\n%s",
			len(turns), dumpTurns(turns))
	}
	want := []marotte.EntryKind{
		marotte.EntryKindTurnOpen,
		marotte.EntryKindText,
		marotte.EntryKindCompaction,
		marotte.EntryKindTurnClose,
	}
	if got := kindsOf(turns[0]); !slices.Equal(got, want) {
		t.Fatalf("entry kinds = %v, want %v:\n%s", got, want, dumpTurns(turns))
	}
	compaction := entryOfKind(t, turns[0], marotte.EntryKindCompaction)
	wantID := marotte.CompactionEntryID([]byte(fx.Summary), 1)
	if compaction.ID != wantID {
		t.Errorf("compaction id = %q, want CompactionEntryID over the same summary bytes (%q)",
			compaction.ID, wantID)
	}
	var payload marotte.EntryCompaction
	payloadOf(t, compaction, &payload)
	if payload.Summary != fx.Summary {
		t.Errorf("compaction summary = %q, want the fixture's summary verbatim", payload.Summary)
	}
	// The post-summary frames continue the SAME say: the compaction sits at the separator.
	var say marotte.EntryText
	payloadOf(t, entryOfKind(t, turns[0], marotte.EntryKindText), &say)
	if !strings.Contains(say.Text, "reading the design") || !strings.Contains(say.Text, "continuing after the summary") {
		t.Errorf("text = %q, want both halves of the one say", say.Text)
	}
}

// TestEntryProjection_AnEmptySummaryTakesTheOrdinalID: an empty summary has no bytes to
// hash, so the id counts this projection's empty compactions from 1.
func TestEntryProjection_AnEmptySummaryTakesTheOrdinalID(t *testing.T) {
	frames := [][2]any{turnStartFrame(t), agentChunkFrame(t, "one")}
	for range 2 {
		frames = append(frames,
			pair(replayFrame(t, marotte.ACPUpdateSessionInfo, "", "summarization_separator", nil)),
			pair(replayFrame(t, marotte.ACPUpdateSessionInfo, "", "summary_message", map[string]any{
				"summaryMessage": map[string]any{"content": ""},
			})))
	}
	frames = append(frames, turnEndFrame(t, "end_turn"))

	turns := entryProject(frames)
	var got []string
	for _, e := range turns[0].Entries {
		if e.Kind == marotte.EntryKindCompaction {
			got = append(got, e.ID)
		}
	}
	want := []string{
		marotte.CompactionEntryID(nil, 1),
		marotte.CompactionEntryID(nil, 2),
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("compaction ids = %v, want %v:\n%s", got, want, dumpTurns(turns))
	}
}

// TestEntryProjection_AReplayedSayHoldsNoSteeringMarker pins that a replayed ack split across
// chunks leaves no marker in the projected text.
func TestEntryProjection_AReplayedSayHoldsNoSteeringMarker(t *testing.T) {
	fx := loadReplayFixture(t, "replay_steer_marker.json")
	turns := projectFixture(t, fx)

	if len(turns) != 1 {
		t.Fatalf("projected %d turns, want 1:\n%s", len(turns), dumpTurns(turns))
	}
	var say marotte.EntryText
	payloadOf(t, entryOfKind(t, turns[0], marotte.EntryKindText), &say)
	if strings.Contains(say.Text, "[STEERING") {
		t.Errorf("text = %q, want no acknowledgement marker", say.Text)
	}
	if say.Text != "Reindented the file. " {
		t.Errorf("text = %q, want the prose either side of the marker and nothing else", say.Text)
	}
	// The steer is a lane-less entry at its queue-time position with NO state.
	steer := entryOfKind(t, turns[0], marotte.EntryKindSteer)
	if steer.ID != fx.SteerID {
		t.Errorf("steer id = %q, want KAS's own %q", steer.ID, fx.SteerID)
	}
	var payload marotte.EntrySteer
	payloadOf(t, steer, &payload)
	if payload.State != "" {
		t.Errorf("steer state = %q, want empty — the replay cannot say whether the model read it", payload.State)
	}
	if payload.Origin != marotte.SteerOriginUser {
		t.Errorf("steer origin = %q, want %q for a `steer-` id", payload.Origin, marotte.SteerOriginUser)
	}
	// No steer_ack entry: the record has them.
	for _, e := range turns[0].Entries {
		if e.Kind == marotte.EntryKindSteerAck {
			t.Errorf("projected a steer_ack entry, want none:\n%s", dumpTurns(turns))
		}
	}
}

// TestEntryProjection_AnUnclosedMarkerIsDroppedAtTheClose pins that a never-closed marker
// carry is dropped.
func TestEntryProjection_AnUnclosedMarkerIsDroppedAtTheClose(t *testing.T) {
	turns := entryProject([][2]any{
		turnStartFrame(t),
		agentChunkFrame(t, "done. [STEERING steer-x: what I did"),
		turnEndFrame(t, "end_turn"),
	})
	var say marotte.EntryText
	payloadOf(t, entryOfKind(t, turns[0], marotte.EntryKindText), &say)
	if say.Text != "done. " {
		t.Errorf("text = %q, want the prose with the unclosed marker dropped", say.Text)
	}
}

// TestEntryProjection_AHeldBracketThatIsNotAMarkerGoesBackIntoTheSay pins the other arm.
func TestEntryProjection_AHeldBracketThatIsNotAMarkerGoesBackIntoTheSay(t *testing.T) {
	turns := entryProject([][2]any{
		turnStartFrame(t),
		agentChunkFrame(t, "see the note ["),
		turnEndFrame(t, "end_turn"),
	})
	var say marotte.EntryText
	payloadOf(t, entryOfKind(t, turns[0], marotte.EntryKindText), &say)
	if say.Text != "see the note [" {
		t.Errorf("text = %q, want the trailing bracket back in the say", say.Text)
	}
}

// TestEntryProjection_AnInvocationIsFiledInTheIssuersLane pins the invocation's lane and the
// delegate uuid in its payload (the pairing is asserted in internal/agent).
func TestEntryProjection_AnInvocationIsFiledInTheIssuersLane(t *testing.T) {
	fx := loadReplayFixture(t, "replay_invocation.json")
	turns := projectFixture(t, fx)

	if len(turns) != 1 {
		t.Fatalf("projected %d turns, want 1:\n%s", len(turns), dumpTurns(turns))
	}
	call := entryOfKind(t, turns[0], marotte.EntryKindToolCall)
	if call.ID != fx.ReplayedCallID {
		t.Errorf("invocation id = %q, want the replay's own %q", call.ID, fx.ReplayedCallID)
	}
	if call.Lane != "" {
		t.Errorf("invocation lane = %q, want the ISSUER's lane (empty here)", call.Lane)
	}
	var payload marotte.EntryToolCall
	payloadOf(t, call, &payload)
	if payload.AgentSubtaskID != fx.DelegateID {
		t.Errorf("invocation agent_subtask_id = %q, want the delegate uuid %q",
			payload.AgentSubtaskID, fx.DelegateID)
	}
	result := entryOfKind(t, turns[0], marotte.EntryKindToolResult)
	if result.ID != marotte.ToolResultID(fx.ReplayedCallID) {
		t.Errorf("result id = %q, want %q", result.ID, marotte.ToolResultID(fx.ReplayedCallID))
	}
	if result.Lane != "" {
		t.Errorf("result lane = %q, want the CALL's lane whatever the update carried", result.Lane)
	}
	// The delegate's prose is in its own lane, so the card sits between the issuer's two runs.
	var laned []string
	for _, e := range turns[0].Entries {
		if e.Lane != "" {
			laned = append(laned, string(e.Kind))
		}
	}
	if len(laned) != 1 || laned[0] != string(marotte.EntryKindText) {
		t.Errorf("laned entries = %v, want exactly the delegate's text:\n%s", laned, dumpTurns(turns))
	}
}

// TestEntryProjection_AnOrdinaryCallTakesTheFramesOwnLane: only an invocation is filed
// in the issuer's lane; a call the delegate itself makes belongs in the delegate's.
func TestEntryProjection_AnOrdinaryCallTakesTheFramesOwnLane(t *testing.T) {
	turns := entryProject([][2]any{
		turnStartFrame(t),
		agentChunkFrame(t, "delegating"),
		toolCallFrame(t, "call-1", map[string]any{"agentSubtaskId": "d-9"}),
		turnEndFrame(t, "end_turn"),
	})
	call := entryOfKind(t, turns[0], marotte.EntryKindToolCall)
	if call.Lane != "d-9" {
		t.Errorf("lane = %q, want the frame's own agent_subtask_id", call.Lane)
	}
	var payload marotte.EntryToolCall
	payloadOf(t, call, &payload)
	if payload.AgentSubtaskID != "" {
		t.Errorf("payload agent_subtask_id = %q, want empty — the lane carries it for an ordinary call",
			payload.AgentSubtaskID)
	}
}

// TestEntryProjection_AnUnsettledCallIsAbortedAtTheClose pins the abort in the call's own lane.
func TestEntryProjection_AnUnsettledCallIsAbortedAtTheClose(t *testing.T) {
	turns := entryProject([][2]any{
		turnStartFrame(t),
		toolCallFrame(t, "call-1", map[string]any{"agentSubtaskId": "d-9"}),
		turnEndFrame(t, "cancelled"),
	})
	result := entryOfKind(t, turns[0], marotte.EntryKindToolResult)
	if result.ID != marotte.ToolResultID("call-1") {
		t.Errorf("result id = %q, want %q", result.ID, marotte.ToolResultID("call-1"))
	}
	if result.Lane != "d-9" {
		t.Errorf("result lane = %q, want the call's own lane", result.Lane)
	}
	var payload marotte.EntryToolResult
	payloadOf(t, result, &payload)
	if payload.Status != marotte.ToolAborted {
		t.Errorf("status = %q, want %q", payload.Status, marotte.ToolAborted)
	}
	// The abort precedes the one turn_close.
	kinds := kindsOf(turns[0])
	if kinds[len(kinds)-1] != marotte.EntryKindTurnClose {
		t.Errorf("entry kinds = %v, want turn_close last", kinds)
	}
}

// TestEntryProjection_ASettledCallIsNotAborted: KAS's own synthetic result settles the
// call, so the close adds nothing.
func TestEntryProjection_ASettledCallIsNotAborted(t *testing.T) {
	turns := entryProject([][2]any{
		turnStartFrame(t),
		toolCallFrame(t, "call-1", nil),
		toolUpdateFrame(t, "call-1", "failed", nil),
		turnEndFrame(t, "cancelled"),
	})
	results := 0
	for _, e := range turns[0].Entries {
		if e.Kind == marotte.EntryKindToolResult {
			results++
		}
	}
	if results != 1 {
		t.Errorf("projected %d tool_result entries, want 1:\n%s", results, dumpTurns(turns))
	}
}

// TestEntryProjection_AnInternalToolIsDropped pins the live path's suppression on replay.
func TestEntryProjection_AnInternalToolIsDropped(t *testing.T) {
	turns := entryProject([][2]any{
		turnStartFrame(t),
		agentChunkFrame(t, "hello"),
		toolCallFrame(t, "call-1", map[string]any{"toolId": "fetch_cloud_config"}),
		turnEndFrame(t, "end_turn"),
	})
	for _, e := range turns[0].Entries {
		if e.Kind == marotte.EntryKindToolCall {
			t.Errorf("projected an internal tool's card, want none:\n%s", dumpTurns(turns))
		}
	}
}

// TestEntryProjection_APromptOpensItsOwnTurnCarryingTheMergesRuleOneKey pins the user row as
// the next turn's prompt, with KAS's record id.
func TestEntryProjection_APromptOpensItsOwnTurnCarryingTheMergesRuleOneKey(t *testing.T) {
	turns := entryProject([][2]any{
		replayUserRow{id: "kas-rec-1", ts: "2026-09-15T09:00:00.000Z", text: "add a test"}.frame(t),
		turnStartFrame(t),
		agentChunkFrame(t, "added"),
		turnEndFrame(t, "end_turn"),
	})
	if len(turns) != 1 {
		t.Fatalf("projected %d turns, want 1:\n%s", len(turns), dumpTurns(turns))
	}
	open := entryOfKind(t, turns[0], marotte.EntryKindTurnOpen)
	var payload marotte.EntryTurnOpen
	payloadOf(t, open, &payload)
	if payload.Source != marotte.TurnOpenNamePrompt {
		t.Errorf("source = %q, want %q", payload.Source, marotte.TurnOpenNamePrompt)
	}
	if payload.Prompt == nil {
		t.Fatalf("turn_open carries no prompt:\n%s", dumpTurns(turns))
	}
	if payload.Prompt.ID != "kas-rec-1" {
		t.Errorf("prompt id = %q, want KAS's own record id", payload.Prompt.ID)
	}
	if payload.Prompt.Text != "add a test" {
		t.Errorf("prompt text = %q, want the row verbatim", payload.Prompt.Text)
	}
	if payload.N != 0 {
		t.Errorf("n = %d, want 0 — the transcript ordinal belongs to the merged log", payload.N)
	}
}

// TestEntryProjection_NoUserRowMeansAHeaderlessTurn: content with no bracket and no user
// row before it, and no turn to continue, opens a turn with no prompt.
func TestEntryProjection_NoUserRowMeansAHeaderlessTurn(t *testing.T) {
	turns := entryProject([][2]any{agentChunkFrame(t, "waking up on my own")})
	open := entryOfKind(t, turns[0], marotte.EntryKindTurnOpen)
	var payload marotte.EntryTurnOpen
	payloadOf(t, open, &payload)
	if payload.Source != marotte.TurnOpenNameWireTurnStart {
		t.Errorf("source = %q, want %q", payload.Source, marotte.TurnOpenNameWireTurnStart)
	}
	if payload.Prompt != nil {
		t.Errorf("turn_open carries a prompt, want none")
	}
}

// TestEntryProjection_ContentAfterATurnEndContinuesThatTurn pins the continuation rule: no
// second turn and no second turn_close.
func TestEntryProjection_ContentAfterATurnEndContinuesThatTurn(t *testing.T) {
	turns := entryProject([][2]any{
		turnStartFrame(t),
		agentChunkFrame(t, "first"),
		turnEndFrame(t, "end_turn"),
		agentChunkFrame(t, "an agent-initiated afterthought"),
		turnEndFrame(t, "end_turn"),
	})
	if len(turns) != 1 {
		t.Fatalf("projected %d turns, want 1:\n%s", len(turns), dumpTurns(turns))
	}
	opens, closes := 0, 0
	for _, e := range turns[0].Entries {
		switch e.Kind {
		case marotte.EntryKindTurnOpen:
			opens++
		case marotte.EntryKindTurnClose:
			closes++
		}
	}
	if opens != 1 || closes != 1 {
		t.Errorf("turn holds %d turn_open and %d turn_close, want one of each:\n%s",
			opens, closes, dumpTurns(turns))
	}
	// The counts cannot see a second turn discarding the first's entries; both says must be here.
	wantSays := []string{"first", "an agent-initiated afterthought"}
	if got := saysOf(t, turns[0]); !slices.Equal(got, wantSays) {
		t.Errorf("turn holds says %q, want %q:\n%s", got, wantSays, dumpTurns(turns))
	}
}

// TestEntryProjection_ASteerLandsAtItsArrivalPosition pins a mid-turn steer between the prose
// either side of it.
func TestEntryProjection_ASteerLandsAtItsArrivalPosition(t *testing.T) {
	// Prose on BOTH sides, so only the agent text handler's flush can place the steer here.
	turns := entryProject([][2]any{
		turnStartFrame(t),
		agentChunkFrame(t, "starting"),
		pair(replaySteerFrame(t, "steer-m-1", "use tabs")),
		agentChunkFrame(t, "resuming"),
		turnEndFrame(t, "end_turn"),
	})
	want := []marotte.EntryKind{
		marotte.EntryKindTurnOpen,
		marotte.EntryKindText,
		marotte.EntryKindSteer,
		marotte.EntryKindText,
		marotte.EntryKindTurnClose,
	}
	if got := kindsOf(turns[0]); !slices.Equal(got, want) {
		t.Errorf("entry kinds = %v, want %v:\n%s", got, want, dumpTurns(turns))
	}
}

// TestEntryProjection_ANotifyRowIsAnAgentSteerCarryingItsSeverity pins a step's send_message
// note as an agent steer with its severity (from _meta.kiro.notification.status).
func TestEntryProjection_ANotifyRowIsAnAgentSteerCarryingItsSeverity(t *testing.T) {
	k, raw := replayFrame(t, replayUserChunkKind, "the step needs a decision", "", map[string]any{
		"messageId":    "notify-1",
		"timestamp":    "2026-09-15T13:00:00.000Z",
		"source":       "steer",
		"notification": map[string]any{"kind": "system-notification", "status": "warning", "sender": "step"},
	})
	turns := entryProject([][2]any{turnStartFrame(t), pair(k, raw), turnEndFrame(t, "end_turn")})

	steer := entryOfKind(t, turns[0], marotte.EntryKindSteer)
	var payload marotte.EntrySteer
	payloadOf(t, steer, &payload)
	if payload.Origin != marotte.SteerOriginAgent {
		t.Errorf("origin = %q, want %q for a `notify-` id", payload.Origin, marotte.SteerOriginAgent)
	}
	if payload.Severity != "warning" {
		t.Errorf("severity = %q, want the notification status", payload.Severity)
	}
	if payload.Text != "the step needs a decision" {
		t.Errorf("text = %q, want the bare text as replayed", payload.Text)
	}
}

// TestEntryProjection_AParentNoticeIsTheMainAgentsSteer pins the launching chat's send_message to
// a step, replayed off the step's session with KAS's `notification.sender: "parent"`, as a parent
// steer: an agent origin would label the main agent's words "Workflow result".
func TestEntryProjection_AParentNoticeIsTheMainAgentsSteer(t *testing.T) {
	k, raw := replayFrame(t, replayUserChunkKind, "weigh this design input", "", map[string]any{
		"messageId": "notify-b88f88cd-676f-472a-ad99-83e7ac49ddda",
		"timestamp": "2026-10-10T12:04:09.213Z",
		"source":    "steer",
		"notification": map[string]any{
			"kind": "system-notification", "status": "info", "sender": "parent",
			"notifyId": "notify-b88f88cd-676f-472a-ad99-83e7ac49ddda",
		},
	})
	turns := entryProject([][2]any{turnStartFrame(t), pair(k, raw), turnEndFrame(t, "end_turn")})

	steer := entryOfKind(t, turns[0], marotte.EntryKindSteer)
	var payload marotte.EntrySteer
	payloadOf(t, steer, &payload)
	if payload.Origin != marotte.SteerOriginParent {
		t.Errorf("origin = %q, want %q for a row KAS marks sender parent", payload.Origin, marotte.SteerOriginParent)
	}
	if payload.Severity != "info" {
		t.Errorf("severity = %q, want the notification status", payload.Severity)
	}
}

// TestEntryProjection_AWorkflowProgressRowIsDropped pins the drop of the run card's JSON row.
func TestEntryProjection_AWorkflowProgressRowIsDropped(t *testing.T) {
	k, raw := replayFrame(t, replayUserChunkKind, `{"kind":"node_start"}`, "", map[string]any{
		"messageId": "wf-progress-1",
		"timestamp": "2026-09-15T13:00:00.000Z",
		"source":    "steer",
	})
	turns := entryProject([][2]any{turnStartFrame(t), pair(k, raw), turnEndFrame(t, "end_turn")})
	for _, e := range turns[0].Entries {
		if e.Kind == marotte.EntryKindSteer {
			t.Errorf("projected a workflow-progress row as a steer:\n%s", dumpTurns(turns))
		}
	}
}

// TestEntryProjection_TheEmptyBoundaryRowDoesNotHijackTheNextPrompt pins the id-change flush.
func TestEntryProjection_TheEmptyBoundaryRowDoesNotHijackTheNextPrompt(t *testing.T) {
	turns := entryProject([][2]any{
		pair(replaySteerFrame(t, "steering_boundary_65cbb64e", "")),
		replayUserRow{id: "kas-rec-1", ts: "2026-09-15T09:00:00.000Z", text: "a real question"}.frame(t),
		turnStartFrame(t),
		agentChunkFrame(t, "answering"),
		turnEndFrame(t, "end_turn"),
	})
	if len(turns) != 1 {
		t.Fatalf("projected %d turns, want 1:\n%s", len(turns), dumpTurns(turns))
	}
	for _, e := range turns[0].Entries {
		if e.Kind == marotte.EntryKindSteer {
			t.Fatalf("projected the boundary row as a steer:\n%s", dumpTurns(turns))
		}
	}
	var payload marotte.EntryTurnOpen
	payloadOf(t, entryOfKind(t, turns[0], marotte.EntryKindTurnOpen), &payload)
	if payload.Prompt == nil || payload.Prompt.ID != "kas-rec-1" {
		t.Errorf("turn_open prompt = %+v, want the reader's own row", payload.Prompt)
	}
}

// TestEntryProjection_TurnsIsIdempotent: the swap may read the projection more than
// once, so a second read must answer the same turns rather than closing another one.
func TestEntryProjection_TurnsIsIdempotent(t *testing.T) {
	p := NewEntryProjection(seqIDs(), "")
	for _, f := range [][2]any{turnStartFrame(t), agentChunkFrame(t, "hi"), turnEndFrame(t, "end_turn")} {
		p.Ingest(f[0].(marotte.ACPUpdateKind), f[1].(json.RawMessage))
	}
	first := p.Turns()
	second := p.Turns()
	if dumpTurns(first) != dumpTurns(second) {
		t.Errorf("second read differs:\nfirst:\n%s\nsecond:\n%s", dumpTurns(first), dumpTurns(second))
	}
}

// TestEntryProjection_TwoLoadsOfOneReplayAgree: the projection is deterministic given
// its id generator, which is what makes the merge's idempotence reachable at all.
func TestEntryProjection_TwoLoadsOfOneReplayAgree(t *testing.T) {
	fx := loadReplayFixture(t, "replay_invocation.json")
	if a, b := dumpTurns(projectFixture(t, fx)), dumpTurns(projectFixture(t, fx)); a != b {
		t.Errorf("two loads of one replay differ:\nfirst:\n%s\nsecond:\n%s", a, b)
	}
}

// TestEntryProjection_AThinkingDeltaIsItsOwnEntry: a reasoning stream is its own action
// with its own say id, so it is a thinking entry rather than text.
func TestEntryProjection_AThinkingDeltaIsItsOwnEntry(t *testing.T) {
	turns := entryProject([][2]any{
		turnStartFrame(t),
		pair(replayFrame(t, marotte.ACPUpdateThoughtChunk, "weighing two shapes", "", map[string]any{
			"messageId": "reason-1-say",
			"timestamp": "2026-09-15T14:00:00.000Z",
		})),
		agentChunkFrame(t, "picked the second"),
		turnEndFrame(t, "end_turn"),
	})
	want := []marotte.EntryKind{
		marotte.EntryKindTurnOpen,
		marotte.EntryKindThinking,
		marotte.EntryKindText,
		marotte.EntryKindTurnClose,
	}
	if got := kindsOf(turns[0]); !slices.Equal(got, want) {
		t.Errorf("entry kinds = %v, want %v:\n%s", got, want, dumpTurns(turns))
	}
}

// TestEntryProjection_TheCloserCarriesTheWiresOwnConclusion pins the outcome from
// ConcludeStopReason and the metering from the preceding turn_completion.
func TestEntryProjection_TheCloserCarriesTheWiresOwnConclusion(t *testing.T) {
	turns := entryProject([][2]any{
		turnStartFrame(t),
		agentChunkFrame(t, "working"),
		pair(replayFrame(t, marotte.ACPUpdateSessionInfo, "", "turn_completion", map[string]any{
			"promptTurnSummaries": []any{map[string]any{"unit": "credit", "usage": 0.5}},
			"elapsedTime":         1200.0,
		})),
		turnEndFrame(t, "cancelled"),
	})
	var payload marotte.EntryTurnClose
	payloadOf(t, entryOfKind(t, turns[0], marotte.EntryKindTurnClose), &payload)
	if payload.Outcome != marotte.TurnOutcomeCancelled {
		t.Errorf("outcome = %q, want %q", payload.Outcome, marotte.TurnOutcomeCancelled)
	}
	if payload.StopReasonRaw != "cancelled" {
		t.Errorf("stop_reason_raw = %q, want the wire's own value", payload.StopReasonRaw)
	}
	if payload.Credits != 0.5 {
		t.Errorf("credits = %v, want 0.5", payload.Credits)
	}
	if payload.ElapsedMs != 1200 {
		t.Errorf("elapsed_ms = %v, want 1200", payload.ElapsedMs)
	}
}

// toolCallFrame builds a replayed tool_call. extra merges into _meta.kiro, so a test
// names only the discriminator it is about.
func toolCallFrame(t *testing.T, id string, extra map[string]any) [2]any {
	t.Helper()
	kiro := map[string]any{
		"replay":    true,
		"messageId": id + "-call",
		"timestamp": "2026-09-15T12:00:02.000Z",
	}
	maps.Copy(kiro, extra)
	return pair(marotte.ACPUpdateToolCall, mustJSON(t, map[string]any{
		"sessionUpdate": string(marotte.ACPUpdateToolCall),
		"toolCallId":    id,
		"title":         "Run Command",
		"kind":          "execute",
		"status":        "pending",
		"_meta":         map[string]any{"kiro": kiro},
	}))
}

// toolUpdateFrame builds a replayed tool_call_update.
func toolUpdateFrame(t *testing.T, id, status string, extra map[string]any) [2]any {
	t.Helper()
	kiro := map[string]any{
		"replay":    true,
		"messageId": id + "-result",
		"timestamp": "2026-09-15T12:00:07.000Z",
	}
	maps.Copy(kiro, extra)
	return pair(marotte.ACPUpdateToolUpdate, mustJSON(t, map[string]any{
		"sessionUpdate": string(marotte.ACPUpdateToolUpdate),
		"toolCallId":    id,
		"status":        status,
		"_meta":         map[string]any{"kiro": kiro},
	}))
}

// TestEntryProjection_ATurnWithNoTurnEndEmitsNoCloser pins that a replay ending inside a turn
// settles its calls and emits NO turn_close (an empty outcome would be rejected).
func TestEntryProjection_ATurnWithNoTurnEndEmitsNoCloser(t *testing.T) {
	turns := entryProject([][2]any{
		turnStartFrame(t),
		agentChunkFrame(t, "half an answer"),
		toolCallFrame(t, "call-1", nil),
	})
	if len(turns) != 1 {
		t.Fatalf("projected %d turns, want 1:\n%s", len(turns), dumpTurns(turns))
	}
	kinds := kindsOf(turns[0])
	if slices.Contains(kinds, marotte.EntryKindTurnClose) {
		t.Errorf("entry kinds = %v, want no turn_close for a turn KAS never ended:\n%s",
			kinds, dumpTurns(turns))
	}
	result := entryOfKind(t, turns[0], marotte.EntryKindToolResult)
	var payload marotte.EntryToolResult
	payloadOf(t, result, &payload)
	if payload.Status != marotte.ToolAborted {
		t.Errorf("status = %q, want %q — the replay never settled the call", payload.Status, marotte.ToolAborted)
	}
	for _, e := range turns[0].Entries {
		if e.Kind != marotte.EntryKindTurnClose {
			continue
		}
		var c marotte.EntryTurnClose
		payloadOf(t, e, &c)
		if c.Outcome == "" {
			t.Errorf("turn_close carries an empty outcome, which the wire refuses:\n%s", e.Payload)
		}
	}
}

// TestEntryProjection_APayloadlessTurnEndClosesAsUnknown: a bracket with no turnEnd
// block still closes the turn, and the outcome it stamps is ConcludeStopReason's own
// answer for an unstated reason rather than an empty string the wire refuses.
func TestEntryProjection_APayloadlessTurnEndClosesAsUnknown(t *testing.T) {
	turns := entryProject([][2]any{
		turnStartFrame(t),
		agentChunkFrame(t, "done"),
		pair(replayFrame(t, marotte.ACPUpdateSessionInfo, "", infoKindTurnEnd, nil)),
	})
	closer := entryOfKind(t, turns[0], marotte.EntryKindTurnClose)
	var c marotte.EntryTurnClose
	payloadOf(t, closer, &c)
	if c.Outcome != marotte.TurnOutcomeUnknown {
		t.Errorf("outcome = %q, want %q for a bracket carrying no stop reason", c.Outcome, marotte.TurnOutcomeUnknown)
	}
}

// TestEntryProjection_AnUnsettlingUpdateLeavesTheCallForTheClose pins that a status-less or
// non-terminal update projects no tool_result (an empty status costs the client the window).
func TestEntryProjection_AnUnsettlingUpdateLeavesTheCallForTheClose(t *testing.T) {
	for name, status := range map[string]string{"statusless": "", "in_progress": "in_progress"} {
		t.Run(name, func(t *testing.T) {
			turns := entryProject([][2]any{
				turnStartFrame(t),
				toolCallFrame(t, "call-1", nil),
				toolUpdateFrame(t, "call-1", status, nil),
				turnEndFrame(t, "end_turn"),
			})
			var results []marotte.EntryToolResult
			for _, e := range turns[0].Entries {
				if e.Kind != marotte.EntryKindToolResult {
					continue
				}
				var payload marotte.EntryToolResult
				payloadOf(t, e, &payload)
				results = append(results, payload)
			}
			if len(results) != 1 {
				t.Fatalf("projected %d tool_result entries, want exactly the close's abort:\n%s",
					len(results), dumpTurns(turns))
			}
			if results[0].Status != marotte.ToolAborted {
				t.Errorf("tool_result status = %q, want %q — the update settled nothing",
					results[0].Status, marotte.ToolAborted)
			}
		})
	}
}

// TestEntryProjection_AContinuationIsSettledAtTurns pins that Turns() settles a continuation
// after the closer.
func TestEntryProjection_AContinuationIsSettledAtTurns(t *testing.T) {
	turns := entryProject([][2]any{
		turnStartFrame(t),
		agentChunkFrame(t, "first"),
		turnEndFrame(t, "end_turn"),
		toolCallFrame(t, "call-2", nil),
		agentChunkFrame(t, "see the note ["),
	})
	if len(turns) != 1 {
		t.Fatalf("projected %d turns, want 1:\n%s", len(turns), dumpTurns(turns))
	}
	result := entryOfKind(t, turns[0], marotte.EntryKindToolResult)
	if result.ID != marotte.ToolResultID("call-2") {
		t.Errorf("result id = %q, want the continuation's call aborted", result.ID)
	}
	var payload marotte.EntryToolResult
	payloadOf(t, result, &payload)
	if payload.Status != marotte.ToolAborted {
		t.Errorf("status = %q, want %q", payload.Status, marotte.ToolAborted)
	}
	wantSays := []string{"first", "see the note ["}
	if got := saysOf(t, turns[0]); !slices.Equal(got, wantSays) {
		t.Errorf("says = %q, want %q — the continuation's held tail was lost:\n%s", got, wantSays, dumpTurns(turns))
	}
}

// TestEntryProjection_ASayChangeSettlesTheLanesCarry pins that a carry withheld at the end of
// say S belongs to S, settled at the next say or a thinking delta.
func TestEntryProjection_ASayChangeSettlesTheLanesCarry(t *testing.T) {
	say := func(kind marotte.ACPUpdateKind, id, text string) [2]any {
		return pair(replayFrame(t, kind, text, "", map[string]any{"messageId": id}))
	}
	t.Run("another say", func(t *testing.T) {
		turns := entryProject([][2]any{
			turnStartFrame(t),
			say(marotte.ACPUpdateAgentChunk, "s1-say", "first part ["),
			say(marotte.ACPUpdateAgentChunk, "s2-say", "second part"),
			turnEndFrame(t, "end_turn"),
		})
		wantSays := []string{"first part [", "second part"}
		if got := saysOf(t, turns[0]); !slices.Equal(got, wantSays) {
			t.Errorf("says = %q, want %q — the held tail moved to the next say:\n%s", got, wantSays, dumpTurns(turns))
		}
	})
	t.Run("a thinking delta over a text say", func(t *testing.T) {
		// The text say is wholly withheld, so its release decides where the entry is created.
		turns := entryProject([][2]any{
			turnStartFrame(t),
			say(marotte.ACPUpdateAgentChunk, "s1-say", "["),
			say(marotte.ACPUpdateThoughtChunk, "r1-say", "weighing"),
			say(marotte.ACPUpdateAgentChunk, "s2-say", "more"),
			turnEndFrame(t, "end_turn"),
		})
		want := []marotte.EntryKind{
			marotte.EntryKindTurnOpen,
			marotte.EntryKindText,
			marotte.EntryKindThinking,
			marotte.EntryKindText,
			marotte.EntryKindTurnClose,
		}
		if got := kindsOf(turns[0]); !slices.Equal(got, want) {
			t.Fatalf("entry kinds = %v, want %v — the text say must be sealed at the thinking delta:\n%s",
				got, want, dumpTurns(turns))
		}
		if got := saysOf(t, turns[0]); !slices.Equal(got, []string{"[", "more"}) {
			t.Errorf("says = %q, want each say to hold its own text:\n%s", got, dumpTurns(turns))
		}
	})
}

// TestEntryProjection_TheCompactionLandsAfterTheSayItsSeparatorSealed pins the live order:
// seal, then compaction.
func TestEntryProjection_TheCompactionLandsAfterTheSayItsSeparatorSealed(t *testing.T) {
	turns := entryProject([][2]any{
		turnStartFrame(t),
		agentChunkFrame(t, "["),
		pair(replayFrame(t, marotte.ACPUpdateSessionInfo, "", infoKindSeparator, nil)),
		pair(replayFrame(t, marotte.ACPUpdateSessionInfo, "", infoKindSummary, map[string]any{
			"summaryMessage": map[string]any{"content": "the gist"},
		})),
		turnEndFrame(t, "end_turn"),
	})
	want := []marotte.EntryKind{
		marotte.EntryKindTurnOpen,
		marotte.EntryKindText,
		marotte.EntryKindCompaction,
		marotte.EntryKindTurnClose,
	}
	if got := kindsOf(turns[0]); !slices.Equal(got, want) {
		t.Errorf("entry kinds = %v, want %v — the released say must precede the compaction:\n%s",
			got, want, dumpTurns(turns))
	}
}

// TestEntryProjection_ReplayedRefusalChunkIsNotProse pins the refusal rule on replay.
func TestEntryProjection_ReplayedRefusalChunkIsNotProse(t *testing.T) {
	turns := entryProject([][2]any{
		turnStartFrame(t),
		agentChunkFrame(t, "partial"),
		pair(replayFrame(t, marotte.ACPUpdateAgentChunk, "I cannot help", "", map[string]any{
			"refusal": map[string]any{"category": "c", "explanation": "I cannot help"},
		})),
		turnEndFrame(t, "content_filtered"),
	})
	if got := saysOf(t, turns[0]); !slices.Equal(got, []string{"partial"}) {
		t.Errorf("text entries = %q, want only the prose before the refusal:\n%s", got, dumpTurns(turns))
	}
	var payload marotte.EntryTurnClose
	payloadOf(t, entryOfKind(t, turns[0], marotte.EntryKindTurnClose), &payload)
	want := marotte.RefusalInfo{Category: "c", Explanation: "I cannot help"}
	if payload.Refusal == nil || *payload.Refusal != want {
		t.Errorf("turn_close.refusal = %+v, want %+v", payload.Refusal, want)
	}
}

// TestEntryProjection_TurnEndStopDetailsRefusalFillsTurnClose: a replayed turn whose
// refusal reached no chunk still carries it on the turn_end, and that is the closer's
// refusal.
func TestEntryProjection_TurnEndStopDetailsRefusalFillsTurnClose(t *testing.T) {
	turns := entryProject([][2]any{
		turnStartFrame(t),
		agentChunkFrame(t, "working"),
		pair(replayFrame(t, marotte.ACPUpdateSessionInfo, "", "turn_end", map[string]any{
			"turnEnd": map[string]any{
				"stopReason":  "refusal",
				"stopDetails": map[string]any{"refusal": map[string]any{"category": "c", "recommendedModel": "m"}},
			},
		})),
	})
	var payload marotte.EntryTurnClose
	payloadOf(t, entryOfKind(t, turns[0], marotte.EntryKindTurnClose), &payload)
	want := marotte.RefusalInfo{Category: "c", RecommendedModel: "m"}
	if payload.Refusal == nil || *payload.Refusal != want {
		t.Errorf("turn_close.refusal = %+v, want %+v", payload.Refusal, want)
	}
}

// A replayed turn kiro-cli cut at its model-call limit projects the same close a live one records.
func TestEntryProjection_ATurnEndAtTheModelCallLimitKeepsItsKind(t *testing.T) {
	turns := entryProject([][2]any{
		turnStartFrame(t),
		agentChunkFrame(t, "working"),
		turnEndFrame(t, "tool_use"),
	})
	var c marotte.EntryTurnClose
	payloadOf(t, entryOfKind(t, turns[0], marotte.EntryKindTurnClose), &c)
	if c.Outcome != marotte.TurnOutcomeFailed || c.FailureKind != marotte.FailureKindModelCallLimit || c.FailureReason != marotte.ModelCallLimitTurnReason {
		t.Errorf("turn_close = {%s %q %q}, want {failed %q %q}", c.Outcome, c.FailureKind, c.FailureReason,
			marotte.FailureKindModelCallLimit, marotte.ModelCallLimitTurnReason)
	}
}

// A replayed failed turn carries the engine's own account: the display_error row
// replays before its turn_end, and turn_end itself carries no failure sentence.
func TestEntryProjection_ReplayedDisplayErrorIsTheFailedTurnsReason(t *testing.T) {
	turns := entryProject([][2]any{
		turnStartFrame(t),
		agentChunkFrame(t, "working"),
		pair(replayFrame(t, marotte.ACPUpdateSessionInfo, "", "display_error", map[string]any{
			"displayError": map[string]any{"message": "Your connection was interrupted.", "errorType": "ge"},
		})),
		pair(replayFrame(t, marotte.ACPUpdateSessionInfo, "", "turn_end", map[string]any{
			"turnEnd": map[string]any{"stopReason": "error"},
		})),
	})
	var payload marotte.EntryTurnClose
	payloadOf(t, entryOfKind(t, turns[0], marotte.EntryKindTurnClose), &payload)
	if payload.FailureReason != "Your connection was interrupted." {
		t.Errorf("turn_close.failure_reason = %q, want the replayed engine sentence", payload.FailureReason)
	}
}

// A display_error on a turn that then ended cleanly is not its verdict.
func TestEntryProjection_ReplayedDisplayErrorLeavesACleanTurnClean(t *testing.T) {
	turns := entryProject([][2]any{
		turnStartFrame(t),
		pair(replayFrame(t, marotte.ACPUpdateSessionInfo, "", "display_error", map[string]any{
			"displayError": map[string]any{"message": "Sub-agent stalled.", "errorType": "ge"},
		})),
		agentChunkFrame(t, "recovered"),
		pair(replayFrame(t, marotte.ACPUpdateSessionInfo, "", "turn_end", map[string]any{
			"turnEnd": map[string]any{"stopReason": "end_turn"},
		})),
	})
	var payload marotte.EntryTurnClose
	payloadOf(t, entryOfKind(t, turns[0], marotte.EntryKindTurnClose), &payload)
	if payload.FailureReason != "" {
		t.Errorf("turn_close.failure_reason = %q, want empty on a clean turn", payload.FailureReason)
	}
}

func TestEntryProjection_AReplayedOffloadReachesTheResult(t *testing.T) {
	path := "/config/home/.kiro/sessions/ab12/sess_1/tool-outputs/execute_bash-0a1b2c3d.txt"
	turns := entryProject([][2]any{
		turnStartFrame(t),
		toolCallFrame(t, "call-1", nil),
		toolUpdateFrame(t, "call-1", "completed", map[string]any{
			"outputTransformation": map[string]any{"kind": "offloaded", "absFilePath": path, "totalChars": 31000},
		}),
	})
	if len(turns) != 1 {
		t.Fatalf("projected %d turns, want 1:\n%s", len(turns), dumpTurns(turns))
	}
	var payload marotte.EntryToolResult
	payloadOf(t, entryOfKind(t, turns[0], marotte.EntryKindToolResult), &payload)
	if payload.Offload == nil || payload.Offload.Path != path || payload.Offload.TotalChars != 31000 {
		t.Errorf("tool_result offload = %+v, want path %q and 31000 chars", payload.Offload, path)
	}
}
