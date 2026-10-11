package translate

// The steering sub-kinds dispatch on the KIND STRING, so these pin the wire shape: flat
// fields beside `kind`.

import (
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// steerFrame builds a session_info_update whose steering fields sit FLAT beside
// `kind`, which is how KAS sends them.
func steerFrame(t *testing.T, kind string, fields map[string]any) []byte {
	t.Helper()
	kiro := map[string]any{"kind": kind}
	maps.Copy(kiro, fields)
	return mustJSON(t, map[string]any{
		"sessionUpdate": "session_info_update",
		"_meta":         map[string]any{"kiro": kiro},
	})
}

func TestSteeringQueued_BroadcastsTheWaitingSteer(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_queued", map[string]any{
			"messageId": "steer-1",
			"content":   "use tabs",
		}), FrameAttribution{})

	if len(*events) != 1 {
		t.Fatalf("broadcast %d events, want 1", len(*events))
	}
	e := (*events)[0]
	if e.Type != marotte.EventSteerQueued {
		t.Fatalf("type = %q, want %q", e.Type, marotte.EventSteerQueued)
	}
	p, ok := e.Payload.(marotte.SteerQueuedPayload)
	if !ok {
		t.Fatalf("payload type = %T", e.Payload)
	}
	if p.SteerID != "steer-1" || p.Text != "use tabs" {
		t.Errorf("payload = %+v, want steer-1 / use tabs", p)
	}
}

// The broadcast is the frame the record answered, so a row the server re-sent under
// a fresh id reaches the client as the batch frame naming the rows it carries.
func TestSteeringQueued_BroadcastsWhatTheBufferRecorded(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	deps.queuedAnswer = func(p marotte.SteerQueuedPayload) (marotte.SteerQueuedPayload, bool) {
		p.Replaces = []string{"steer-a", "steer-b"}
		return p, true
	}
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_queued", map[string]any{"messageId": "steer-new", "content": "use tabs"}),
		FrameAttribution{})

	if len(*events) != 1 {
		t.Fatalf("broadcast %d events, want 1", len(*events))
	}
	p, ok := (*events)[0].Payload.(marotte.SteerQueuedPayload)
	if !ok || !slices.Equal(p.Replaces, []string{"steer-a", "steer-b"}) {
		t.Errorf("payload = %+v, want Replaces [steer-a steer-b]", (*events)[0].Payload)
	}
}

// A chat whose teardown began answers no frame, so nothing is broadcast for it.
func TestSteeringQueued_AChatGoingAwayBroadcastsNothing(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	deps.queuedAnswer = func(p marotte.SteerQueuedPayload) (marotte.SteerQueuedPayload, bool) { return p, false }
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_queued", map[string]any{"messageId": "notify-1", "content": "a run finished"}),
		FrameAttribution{})

	if len(*events) != 0 {
		t.Errorf("events = %v, want none for a chat going away", eventTypes(*events))
	}
}

// TestSteeringQueued_AgentNoticeLeavesAsItsOwnEvent pins the notice split on severity, never
// a steer on the composer's chip row.
func TestSteeringQueued_AgentNoticeLeavesAsItsOwnEvent(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_queued", map[string]any{
			"messageId":            "notify-1",
			"content":              "[notification/error] a step failed",
			"notificationSeverity": "error",
		}), FrameAttribution{})

	if len(*events) != 1 {
		t.Fatalf("broadcast %d events, want 1", len(*events))
	}
	e := (*events)[0]
	if e.Type != marotte.EventAgentNotice {
		t.Fatalf("type = %q, want %q", e.Type, marotte.EventAgentNotice)
	}
	p, ok := e.Payload.(marotte.AgentNoticePayload)
	if !ok {
		t.Fatalf("payload type = %T", e.Payload)
	}
	if p.Severity != "error" {
		t.Errorf("severity = %q, want error", p.Severity)
	}
	if p.Text != "[notification/error] a step failed" {
		t.Errorf("text = %q, want the notice verbatim", p.Text)
	}
}

func appendedSteers(t *testing.T, events []marotte.ServerEvent) []steerRow {
	t.Helper()
	var out []steerRow
	for _, e := range events {
		if e.Type != marotte.EventEntryAppended {
			continue
		}
		p, ok := e.Payload.(marotte.EntryAppendedPayload)
		if !ok || p.Entry.Kind != marotte.EntryKindSteer {
			continue
		}
		out = append(out, steerRow{ID: p.Entry.ID, EntrySteer: decodePayload[marotte.EntrySteer](t, &p.Entry)})
	}
	return out
}

func TestSteeringInjected_AnnouncesTheReadSteerEntry(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_injected", map[string]any{
			"messageId": "steer-1",
			"content":   "use tabs",
		}), FrameAttribution{})

	if len(*events) != 1 {
		t.Fatalf("events = %v, want the one entry_appended", eventTypes(*events))
	}
	rows := appendedSteers(t, *events)
	if len(rows) != 1 {
		t.Fatalf("entry_appended{steer} frames = %d, want 1: %v", len(rows), eventTypes(*events))
	}
	if rows[0].ID != "steer-1" || rows[0].State != marotte.SteerStateRead || rows[0].Text != "use tabs" {
		t.Errorf("announced steer = %+v, want id steer-1, state read, text %q", rows[0], "use tabs")
	}
}

// TestSteeringCleared_RecordsEachDroppedAgentNoteAndNoUserRow pins one dropped entry per agent
// note with the queued text, and none for a user row (the host writes it).
func TestSteeringCleared_RecordsEachDroppedAgentNoteAndNoUserRow(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	deps.userSteers = map[string]bool{"steer-1": true}
	tr := New(rolesOf(deps))
	for id, text := range map[string]string{"steer-1": "use tabs", "notify-wf-2": "a run finished"} {
		tr.HandleSessionInfoUpdate(t.Context(), "c1",
			steerFrame(t, "steering_queued", map[string]any{"messageId": id, "content": text}), FrameAttribution{})
	}
	*events = nil

	tr.HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_cleared", map[string]any{
			"messageIds": []string{"steer-1", "notify-wf-2"},
		}), FrameAttribution{})

	rows := steerRows(t, deps, "c1")
	want := steerRow{
		ID: "notify-wf-2", Text: "a run finished", Origin: marotte.SteerOriginAgent,
		State: marotte.SteerStateDropped, Reason: marotte.SteerReasonBoundary,
	}
	if len(rows) != 1 || !reflect.DeepEqual(rows[0], want) {
		t.Errorf("steer entries = %+v, want only the agent note %+v", rows, want)
	}
	if got := appendedSteers(t, *events); len(got) != 1 {
		t.Errorf("entry_appended{steer} frames = %d, want 1: %v", len(got), eventTypes(*events))
	}
	if left := waitingOf(t, deps, "c1"); len(left) != 0 {
		t.Errorf("waiting after the clear = %v, want empty", left)
	}
}

// TestSteeringCleared_ANoteNeverHeldIsDroppedTextless pins a text-less drop stamped with its run;
// a never-queued user steer writes nothing.
func TestSteeringCleared_ANoteNeverHeldIsDroppedTextless(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	deps.userSteers = map[string]bool{"steer-mine": true}
	deps.runNotices = map[marotte.ChatID][]stagedRunNotice{"c1": {{workflowID: "wf_9", producedTs: 1_700_000_000_000}}}
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_cleared", map[string]any{
			"messageIds": []string{"steer-mine", "notify-wf-9"},
		}), FrameAttribution{})

	rows := steerRows(t, deps, "c1")
	want := []steerRow{{
		ID:     "notify-wf-9",
		Origin: marotte.SteerOriginAgent, State: marotte.SteerStateDropped,
		Reason: marotte.SteerReasonBoundary, OriginRun: "wf_9", ProducedTs: 1_700_000_000_000,
	}}
	if len(rows) != 1 || !reflect.DeepEqual(rows[0], want[0]) {
		t.Errorf("steer entries = %+v, want only the text-less agent drop carrying its run's provenance %+v", rows, want[0])
	}
	if got := appendedSteers(t, *events); len(got) != 1 {
		t.Errorf("entry_appended{steer} frames = %d, want 1: %v", len(got), eventTypes(*events))
	}
	// The text-less note IS the merge's signal.
	if rows[0].Text != "" {
		t.Errorf("the dropped agent note carries text %q, want none: the absent words ARE the signal", rows[0].Text)
	}
}

// TestSteeringCleared_EmptyListIsNotBroadcast pins silence on the normal empty clear.
func TestSteeringCleared_EmptyListIsNotBroadcast(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_cleared", map[string]any{"messageIds": []string{}}), FrameAttribution{})

	if len(*events) != 0 {
		t.Errorf("broadcast %d events for an empty clear, want 0", len(*events))
	}
}

// TestSteering_IDlessFramesAreDroppedNotForwarded pins that an id-less steer is consumed and
// dropped.
func TestSteering_IDlessFramesAreDroppedNotForwarded(t *testing.T) {
	for _, kind := range []string{"steering_queued", "steering_injected"} {
		t.Run(kind, func(t *testing.T) {
			deps, events, _ := depsWithStore(t, "c1")
			New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
				steerFrame(t, kind, map[string]any{"content": "orphan"}), FrameAttribution{})

			if len(*events) != 0 {
				t.Errorf("broadcast %d events for an id-less %s, want 0", len(*events), kind)
			}
		})
	}
}

// TestSteering_SurvivesSubagentAttribution pins steering dispatch BEFORE the parent-only gate:
// a steer is the chat's, consumed by whichever execution runs.
func TestSteering_SurvivesSubagentAttribution(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_injected", map[string]any{
			"messageId": "steer-1",
			"content":   "use tabs",
		}), FrameAttribution{Subagent: true, SessionID: "sub-session-7"})

	rows := appendedSteers(t, *events)
	if len(rows) != 1 || rows[0].ID != "steer-1" || rows[0].State != marotte.SteerStateRead {
		t.Fatalf("events = %v — a steer consumed inside a subagent must still be recorded as read", eventTypes(*events))
	}
}

// Origin is the one field not decoded from the frame: every producer persists identically,
// so the answer comes from the ledger of what THIS server sent.

func TestSteeringQueued_OriginIsUserForASteerThisServerSent(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	deps.userSteers = map[string]bool{"steer-m-1": true}
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_queued", map[string]any{
			"messageId": "steer-m-1",
			"content":   "use tabs",
		}), FrameAttribution{})

	p, ok := (*events)[0].Payload.(marotte.SteerQueuedPayload)
	if !ok {
		t.Fatalf("payload type = %T", (*events)[0].Payload)
	}
	if p.Origin != marotte.SteerOriginUser {
		t.Errorf("origin = %q, want %q for an id the ledger holds", p.Origin, marotte.SteerOriginUser)
	}
}

// An id the ledger does not hold is the agent's, and this is the reported
// defect: a workflow's report rendered as something the user had typed.
func TestSteeringQueued_OriginIsAgentForAnIDTheLedgerDoesNotHold(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_queued", map[string]any{
			"messageId": "notify-wf-9",
			"content":   "A workflow you launched completed.",
		}), FrameAttribution{})

	p, ok := (*events)[0].Payload.(marotte.SteerQueuedPayload)
	if !ok {
		t.Fatalf("payload type = %T", (*events)[0].Payload)
	}
	if p.Origin != marotte.SteerOriginAgent {
		t.Errorf("origin = %q, want %q", p.Origin, marotte.SteerOriginAgent)
	}
}

// TestSteeringInjected_TheEntryCarriesTheOrigin pins the origin on the read entry: for agent
// injections it is the ONLY record. A `steer-` id names the user whatever the ledger holds.
func TestSteeringInjected_TheEntryCarriesTheOrigin(t *testing.T) {
	for _, tc := range []struct {
		name  string
		id    string
		known bool
		want  marotte.SteerOrigin
	}{
		{"the user's own", "steer-1", true, marotte.SteerOriginUser},
		{"a workflow's report", "notify-wf-9", false, marotte.SteerOriginAgent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, events, _ := depsWithStore(t, "c1")
			if tc.known {
				deps.userSteers = map[string]bool{tc.id: true}
			}
			New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
				steerFrame(t, "steering_injected", map[string]any{
					"messageId": tc.id,
					"content":   "use tabs",
				}), FrameAttribution{})

			rows := appendedSteers(t, *events)
			if len(rows) != 1 {
				t.Fatalf("entry_appended{steer} frames = %d, want 1: %v", len(rows), eventTypes(*events))
			}
			if rows[0].Origin != tc.want {
				t.Errorf("origin = %q, want %q", rows[0].Origin, tc.want)
			}
		})
	}
}

// The read steer's entry carries the row keys the steer buffer answers the read
// with: KAS's frames never name them, and a client matches them against its row
// keys to take every row the combined steer carried.
func TestSteeringInjected_TheEntryCarriesTheBuffersCarriedKeys(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	deps.userSteers = map[string]bool{"steer-2": true}
	deps.queuedAnswer = func(p marotte.SteerQueuedPayload) (marotte.SteerQueuedPayload, bool) {
		p.Replaces = []string{"steer-1"}
		return p, true
	}
	tr := New(rolesOf(deps))
	tr.HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_queued", map[string]any{"messageId": "steer-2", "content": "for decision 5 too"}), FrameAttribution{})
	*events = nil

	tr.HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_injected", map[string]any{"messageId": "steer-2", "content": "for decision 5 too"}),
		FrameAttribution{})

	rows := appendedSteers(t, *events)
	if len(rows) != 1 {
		t.Fatalf("entry_appended{steer} frames = %d, want 1: %v", len(rows), eventTypes(*events))
	}
	if want := []string{"steer-1"}; !slices.Equal(rows[0].Resends, want) {
		t.Errorf("read steer resends = %v, want the buffer's %v", rows[0].Resends, want)
	}
}

// TestSteeringQueued_ASeverityStillPreemptsTheSteerEntirely pins that a severity frame yields
// an agent_notice and no steer.
func TestSteeringQueued_ASeverityStillPreemptsTheSteerEntirely(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_queued", map[string]any{
			"messageId":            "notify-1",
			"content":              "[notification/warning] a step is waiting",
			"notificationSeverity": "warning",
		}), FrameAttribution{})

	if len(*events) != 1 || (*events)[0].Type != marotte.EventAgentNotice {
		t.Fatalf("events = %+v, want one agent_notice and no steer", *events)
	}
}

// The waiting SET, what a reconnect re-offers: nothing client-callable reads KAS's buffer back.

func waitingOf(t *testing.T, d *baseDeps, chatID marotte.ChatID) map[string]marotte.SteerQueuedPayload {
	t.Helper()
	out := map[string]marotte.SteerQueuedPayload{}
	for _, p := range d.waiting[chatID] {
		out[p.SteerID] = p
	}
	return out
}

func TestSteeringQueued_RecordsTheSteerAsWaiting(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	deps.userSteers = map[string]bool{"steer-1": true}
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_queued", map[string]any{
			"messageId": "steer-1",
			"content":   "use tabs",
		}), FrameAttribution{})

	got := waitingOf(t, deps, "c1")
	if len(got) != 1 {
		t.Fatalf("waiting = %+v, want one entry", got)
	}
	e := got["steer-1"]
	if e.Text != "use tabs" || e.Origin != marotte.SteerOriginUser {
		t.Errorf("entry = %+v, want the text and the resolved origin", e)
	}
}

// TestSteeringQueued_AnAgentNoticeIsNotRecordedAsWaiting pins a notice outside the set, beside
// a real steer so an empty-set build cannot pass.
func TestSteeringQueued_AnAgentNoticeIsNotRecordedAsWaiting(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	tr.HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_queued", map[string]any{
			"messageId": "steer-1",
			"content":   "use tabs",
		}), FrameAttribution{})
	tr.HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_queued", map[string]any{
			"messageId":            "notify-1",
			"content":              "[notification/warning] a step is waiting",
			"notificationSeverity": "warning",
		}), FrameAttribution{})

	got := waitingOf(t, deps, "c1")
	if len(got) != 1 {
		t.Fatalf("waiting = %+v, want the steer alone", got)
	}
	if _, ok := got["notify-1"]; ok {
		t.Errorf("waiting = %+v, want the notice absent", got)
	}
}

// TestSteeringInjected_RemovesTheSteerFromTheWaitingSet pins the removal, with a sibling left
// behind.
func TestSteeringInjected_RemovesTheSteerFromTheWaitingSet(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	for _, id := range []string{"steer-1", "steer-2"} {
		tr.HandleSessionInfoUpdate(t.Context(), "c1",
			steerFrame(t, "steering_queued", map[string]any{
				"messageId": id,
				"content":   "text of " + id,
			}), FrameAttribution{})
	}
	tr.HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_injected", map[string]any{
			"messageId": "steer-1",
			"content":   "text of steer-1",
		}), FrameAttribution{})

	got := waitingOf(t, deps, "c1")
	if len(got) != 1 {
		t.Fatalf("waiting = %+v, want only steer-2", got)
	}
	if _, ok := got["steer-2"]; !ok {
		t.Errorf("waiting = %+v, want steer-2 kept", got)
	}
}

// A boundary cleared KAS's buffer, so every id it names is gone server-side
// whether the model read it or not. Named ids only, matching the frame.
func TestSteeringCleared_RemovesEachNamedSteerFromTheWaitingSet(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	for _, id := range []string{"steer-1", "steer-2", "steer-3"} {
		tr.HandleSessionInfoUpdate(t.Context(), "c1",
			steerFrame(t, "steering_queued", map[string]any{
				"messageId": id,
				"content":   "text of " + id,
			}), FrameAttribution{})
	}
	tr.HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_cleared", map[string]any{
			"messageIds": []string{"steer-1", "steer-3"},
		}), FrameAttribution{})

	got := waitingOf(t, deps, "c1")
	if len(got) != 1 {
		t.Fatalf("waiting = %+v, want only steer-2", got)
	}
	if _, ok := got["steer-2"]; !ok {
		t.Errorf("waiting = %+v, want steer-2 kept", got)
	}
}

// stepAttr is the attribution a step session's frame carries once its node_start registered it.
var stepAttr = FrameAttribution{Step: true, SessionID: "sess_s", RunID: "wf_1", NodePath: "wf_1/review"}

// A step session's buffer is the step's: its row folds under the step's key and its frame names the
// run and step, never the launching chat.
func TestSteeringQueued_AStepSessionsRowIsTheSteps(t *testing.T) {
	for _, door := range []struct {
		name   string
		handle func(*Translator, []byte)
	}{
		{"the launching chat's bridge", func(tr *Translator, raw []byte) {
			tr.HandleSessionInfoUpdate(t.Context(), "c1", raw, stepAttr)
		}},
		{"the run's own bridge", func(tr *Translator, raw []byte) {
			tr.HandleStepInfoUpdate(t.Context(), "run:wf_1", raw, stepAttr)
		}},
	} {
		t.Run(door.name, func(t *testing.T) {
			deps, events, _ := depsWithStore(t, "c1")
			door.handle(New(rolesOf(deps)), steerFrame(t, "steering_queued", map[string]any{
				"messageId": "steer-1", "content": "use tabs",
			}))

			if len(*events) != 1 {
				t.Fatalf("broadcast %d events, want 1", len(*events))
			}
			e := (*events)[0]
			p, _ := e.Payload.(marotte.SteerQueuedPayload)
			if e.ChatID != "" || p.WorkflowID != "wf_1" || p.NodePath != "wf_1/review" {
				t.Errorf("event = chat %q payload %+v, want no chat and the step's run and path", e.ChatID, p)
			}
			if len(deps.waiting["c1"]) != 0 || len(deps.waiting[marotte.StepSteerKey("sess_s")]) != 1 {
				t.Errorf("rows = %v, want the step's key holding the row and the chat holding none", deps.waiting)
			}
		})
	}
}

// A step's read steer is the run's entry, filed in the step's turn rather than the chat's.
func TestSteeringInjected_AStepSessionsReadIsTheRunsEntry(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	New(rolesOf(deps)).HandleStepInfoUpdate(t.Context(), "run:wf_1",
		steerFrame(t, "steering_injected", map[string]any{"messageId": "steer-1", "content": "use tabs"}), stepAttr)

	want := runCall{kind: "steer", runID: "wf_1", nodePath: "wf_1/review", steerID: "steer-1"}
	if !slices.Contains(deps.runCalls, want) {
		t.Errorf("run calls = %+v, want %+v", deps.runCalls, want)
	}
}
