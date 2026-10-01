package translate

// The three steering sub-kinds are the one place the session_info_update cascade
// dispatches on the KIND STRING rather than on which sub-block is present, so the
// wire shape is what these tests pin: flat fields beside `kind`, because KAS's
// buildSessionInfoUpdate spreads the update object straight into `_meta.kiro` and
// emits no legacy nested block for any of them.

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

// The agent's own notice leaves as its own event, never as a steer. KAS delivers
// it through the same buffer and the severity is the only thing distinguishing
// it, so the split has to happen here: forwarding it as a steer put the agent's
// words on the composer's chip row as though the user had typed them.
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

// appendedSteers decodes every entry_appended{steer} frame in events, in order:
// the one frame a read or dropped steer travels as, and what takes the dock row
// out and puts the note into the turn body in one client update.
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

// A boundary drop is recorded per steer, with the text and origin the queued frame
// carried: KAS's cleared frame names ids alone, so the waiting set is the one
// place the words survive.
func TestSteeringCleared_RecordsEachDroppedSteer(t *testing.T) {
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
	if len(rows) != 2 {
		t.Fatalf("steer entries = %+v, want one dropped row per cleared id", rows)
	}
	// Each drop carries the BOUNDARY reason: the turn ended before the agent read it,
	// which is the one thing this clear knows and the label's clause is worded from.
	want := map[string]steerRow{
		"steer-1": {ID: "steer-1", EntrySteer: marotte.EntrySteer{
			Text: "use tabs", Origin: marotte.SteerOriginUser, State: marotte.SteerStateDropped,
			Reason: marotte.SteerReasonBoundary,
		}},
		"notify-wf-2": {ID: "notify-wf-2", EntrySteer: marotte.EntrySteer{
			Text: "a run finished", Origin: marotte.SteerOriginAgent, State: marotte.SteerStateDropped,
			Reason: marotte.SteerReasonBoundary,
		}},
	}
	for _, row := range rows {
		if !reflect.DeepEqual(row, want[row.ID]) {
			t.Errorf("dropped row %q = %+v, want %+v", row.ID, row, want[row.ID])
		}
	}
	if got := appendedSteers(t, *events); len(got) != 2 {
		t.Errorf("entry_appended{steer} frames = %d, want 2: %v", len(got), eventTypes(*events))
	}
	if left := waitingOf(t, deps, "c1"); len(left) != 0 {
		t.Errorf("waiting after the clear = %v, want empty", left)
	}
}

// An agent note this process never queued is dropped text-less, stamped with the
// finished run it came from, and the absent words ARE the signal the resume's merge
// reads; a user steer it never queued was already read, so its drop is not a fact
// and nothing is written.
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
	// The text-less note IS the signal: nothing is written beside it, and the next
	// session/load's merge reads the entry's own empty text.
	if rows[0].Text != "" {
		t.Errorf("the dropped agent note carries text %q, want none: the absent words ARE the signal", rows[0].Text)
	}
}

// KAS clears its buffer at EVERY turn boundary, so an empty list is the normal
// case on the vast majority of turns. Broadcasting it would put one dead event on
// the wire per turn for every chat.
func TestSteeringCleared_EmptyListIsNotBroadcast(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_cleared", map[string]any{"messageIds": []string{}}), FrameAttribution{})

	if len(*events) != 0 {
		t.Errorf("broadcast %d events for an empty clear, want 0", len(*events))
	}
}

// A steer with no id is unaddressable: every later event keys on it, so a chip
// built from this could never be resolved or cleared. Dropped rather than
// forwarded — but still CONSUMED, so it does not fall through to the
// unknown-kind warning.
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

// The steering dispatch runs BEFORE the parent-only gate, and this is why: a
// steer belongs to the CHAT — the user typed it there — but it is consumed by
// whichever execution is running, which may be a subagent's. Gating on
// attribution would drop the injected signal exactly when the agent delegated,
// leaving a chip that says "waiting" over a message the model has read.
func TestSteering_SurvivesSubagentAttribution(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_injected", map[string]any{
			"messageId": "steer-1",
			"content":   "use tabs",
		}), FrameAttribution{SubSessionID: "sub-session-7"})

	rows := appendedSteers(t, *events)
	if len(rows) != 1 || rows[0].ID != "steer-1" || rows[0].State != marotte.SteerStateRead {
		t.Fatalf("events = %v — a steer consumed inside a subagent must still be recorded as read", eventTypes(*events))
	}
}

// --- Origin: whose words the steer carries -------------------------------
//
// The one field on these payloads that is not decoded from the frame. Nothing on
// the wire separates the user's own correction from a workflow reporting into the
// same buffer — measured on the live store, all three producers persist
// identically as `{"type":"user","source":"steer"}` — so the answer comes from
// the ledger of what THIS server sent.

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

// The read steer's ENTRY carries it too, and that is load-bearing rather than
// symmetric: both agent injection paths append straight to KAS's buffer and only
// `_session/steer` broadcasts a queued frame, so for the case Origin exists to
// name, the steer entry is the ONLY record the client ever sees.
//
// The agent case carries a `notify-` id because that is the id space an agent's
// own steering row lands in. A `steer-` id is one THIS server sent, so it names
// the user whatever the ledger holds — the case below pins that separately.
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

// The steer entry carries the resends the sender recorded, read or dropped: KAS's
// frames never name them, so the ledger is the one source.
func TestSteering_TheEntryCarriesTheRecordedResends(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kind  string
		frame map[string]any
	}{
		{"read", "steering_injected", map[string]any{"messageId": "steer-2", "content": "for decision 5 too"}},
		{"dropped", "steering_cleared", map[string]any{"messageIds": []string{"steer-2"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, events, _ := depsWithStore(t, "c1")
			deps.userSteers = map[string]bool{"steer-2": true}
			deps.steerResends = map[string][]string{"steer-2": {"steer-1"}}
			tr := New(rolesOf(deps))
			tr.HandleSessionInfoUpdate(t.Context(), "c1",
				steerFrame(t, "steering_queued", map[string]any{"messageId": "steer-2", "content": "for decision 5 too"}), FrameAttribution{})
			*events = nil

			tr.HandleSessionInfoUpdate(t.Context(), "c1", steerFrame(t, tc.kind, tc.frame), FrameAttribution{})

			rows := appendedSteers(t, *events)
			if len(rows) != 1 {
				t.Fatalf("entry_appended{steer} frames = %d, want 1: %v", len(rows), eventTypes(*events))
			}
			if want := []string{"steer-1"}; !slices.Equal(rows[0].Resends, want) {
				t.Errorf("%s steer resends = %v, want the ledger's %v", tc.name, rows[0].Resends, want)
			}
		})
	}
}

// A frame carrying a severity still leaves as an agent_notice and produces no
// steer at all, so Origin never has to answer for one. The severity gate and the
// ledger answer different questions: the gate catches the one shape KAS marks,
// and the ledger covers everything else the agent injects.
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

// --- The waiting SET: what a reconnect has to be able to re-offer ---
//
// KAS's steering buffer is the only place a waiting steer exists, and nothing
// client-callable reads it back (`_session/steer` and `_session/steer/clear` are
// the whole verb set — there is no list). So a client that loses a frame loses the
// row: its dock empties with the message still queued, and the reader watches
// their correction vanish and reasonably re-sends it. These three cases pin the
// projection the connect replay serves from, one per arm of the sub-kind cascade.

// waitingOf returns the buffer's entries for one chat, keyed by steer id.
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

// The AGENT's own notice is not a steer on any surface, so it must not enter the
// set either — replaying one would put a line the agent wrote in the composer's
// chip row, which is the defect the split exists to prevent.
//
// A real steer rides alongside it deliberately: asserting an EMPTY set would pass
// for a build that records nothing at all, which is the one mutant this case has
// to be able to see.
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

// The model READ it, so it is no longer waiting: replaying it afterwards would
// offer a delivered message back to the dock.
//
// A SIBLING steer stays behind, for the same reason as above: an empty-set
// assertion would pass for a build that never recorded either one.
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
