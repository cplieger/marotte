package translate

// A steer's DURABLE entry: what a page reload rebuilds the note from.
//
// The dock rows and the transcript marks both die with the page, and a plain F5 on
// a live bridge triggers no session/load, so before these the only writer of a
// steer row was the REPLAY projection — a landed steer survived a container restart
// and not a refresh.

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// steerRow is one steer entry: its id and its decoded payload.
type steerRow struct {
	marotte.EntrySteer
	ID string
}

// steerRows returns the chat's steer entries in append order: sealed in its open
// turn, then filed after its newest close.
func steerRows(t *testing.T, deps *baseDeps, chatID marotte.ChatID) []steerRow {
	t.Helper()
	var out []steerRow
	for _, entries := range [][]marotte.Entry{deps.chatEntries(chatID), deps.between[chatID]} {
		for i := range entries {
			if entries[i].Kind == marotte.EntryKindSteer {
				out = append(out, steerRow{ID: entries[i].ID, EntrySteer: decodePayload[marotte.EntrySteer](t, &entries[i])})
			}
		}
	}
	return out
}

func TestSteeringInjected_PersistsAReadSteerRow(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	deps.userSteers = map[string]bool{"steer-1": true}
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_injected", map[string]any{
			"messageId": "steer-1",
			"content":   "use tabs",
		}), FrameAttribution{})

	rows := steerRows(t, deps, "c1")
	if len(rows) != 1 {
		t.Fatalf("persisted %d steer entries, want 1 — a read steer must survive a reload", len(rows))
	}
	row := rows[0]
	// The id is KAS's own steer id, which is also the id the replay projection
	// stamps, so a later session/load pairs on it rather than doubling the note.
	if row.ID != "steer-1" {
		t.Errorf("ID = %q, want the steer id", row.ID)
	}
	if row.Text != "use tabs" {
		t.Errorf("Text = %q, want the steer's text", row.Text)
	}
	if row.State != marotte.SteerStateRead {
		t.Errorf("State = %q, want %q", row.State, marotte.SteerStateRead)
	}
	if row.Origin != marotte.SteerOriginUser {
		t.Errorf("Origin = %q, want %q", row.Origin, marotte.SteerOriginUser)
	}
}

// The half that matters most: an agent note nothing READ. Rendering it after a
// reload as though it had landed is a false statement, which is worse than the
// note being absent. A user row's undelivered entry is the host's, written at
// the row's own terminal transition.
func TestSteeringCleared_PersistsAnUndeliveredAgentNote(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	// The queued frame is what puts the text in reach: the cleared frame carries
	// ids and nothing else.
	tr.HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_queued", map[string]any{
			"messageId": "notify-wf-1",
			"content":   "a run finished",
		}), FrameAttribution{})
	tr.HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_cleared", map[string]any{
			"messageIds": []string{"notify-wf-1"},
		}), FrameAttribution{})

	rows := steerRows(t, deps, "c1")
	if len(rows) != 1 {
		t.Fatalf("persisted %d steer entries, want 1", len(rows))
	}
	if rows[0].Text != "a run finished" {
		t.Errorf("Text = %q, want the note's text", rows[0].Text)
	}
	if rows[0].State != marotte.SteerStateDropped {
		t.Errorf("State = %q, want %q", rows[0].State, marotte.SteerStateDropped)
	}
}

// KAS clears its buffer at EVERY turn boundary, so the cleared frame names ids the
// model already read. That arrival is housekeeping, and reading it as a drop would
// overwrite a delivered steer with "never read".
func TestSteeringCleared_DoesNotOverwriteAReadSteer(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	for _, f := range []struct {
		kind   string
		fields map[string]any
	}{
		{"steering_queued", map[string]any{"messageId": "steer-1", "content": "use tabs"}},
		{"steering_injected", map[string]any{"messageId": "steer-1", "content": "use tabs"}},
		{"steering_cleared", map[string]any{"messageIds": []string{"steer-1"}}},
	} {
		tr.HandleSessionInfoUpdate(t.Context(), "c1", steerFrame(t, f.kind, f.fields), FrameAttribution{})
	}

	rows := steerRows(t, deps, "c1")
	if len(rows) != 1 {
		t.Fatalf("persisted %d steer entries, want 1 — the clear is housekeeping", len(rows))
	}
	if rows[0].State != marotte.SteerStateRead {
		t.Errorf("State = %q, want %q", rows[0].State, marotte.SteerStateRead)
	}
}

// A steer this server sent whose ledger entry was missing at queue time is still
// the USER's, because the id says so — so the clear writes nothing for it: a user
// row's entry is the host's, and an agent verdict here would title the reader's own
// words "Workflow result not delivered".
//
// The ledger is left empty deliberately: that is every one of its loss modes at
// once (the queued-frame race, TTL expiry, cap eviction, chat teardown, restart).
func TestSteeringCleared_ADerivedIDTheLedgerLostIsStillTheUsers(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	const id = "steer-m-mtyaeheu-i481u605rb5m5u2c1y"

	tr.HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_queued", map[string]any{
			"messageId": id,
			"content":   "correction, the url does not work",
		}), FrameAttribution{})
	if p, ok := (*events)[0].Payload.(marotte.SteerQueuedPayload); !ok || p.Origin != marotte.SteerOriginUser {
		t.Errorf("queued payload = %+v, want origin %q — a %q id is one this server sent",
			(*events)[0].Payload, marotte.SteerOriginUser, marotte.SteerIDPrefix)
	}
	tr.HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_cleared", map[string]any{
			"messageIds": []string{id},
		}), FrameAttribution{})

	if rows := steerRows(t, deps, "c1"); len(rows) != 0 {
		t.Errorf("persisted %+v, want nothing: the user row's entry is the host's", rows)
	}
}

// An agent-origin steer keeps its own origin, or the note's title claims a
// workflow's report is something the reader typed — the defect SteerOrigin exists
// to prevent, which the durable row would otherwise reintroduce on every reload.
func TestSteeringInjected_PersistsTheAgentOrigin(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_injected", map[string]any{
			"messageId": "notify-wf-9",
			"content":   "A workflow you launched completed.",
		}), FrameAttribution{})

	rows := steerRows(t, deps, "c1")
	if len(rows) != 1 {
		t.Fatalf("persisted %d steer entries, want 1", len(rows))
	}
	if rows[0].Origin != marotte.SteerOriginAgent {
		t.Errorf("Origin = %q, want %q", rows[0].Origin, marotte.SteerOriginAgent)
	}
}

// A cleared id the buffer never held has no text anywhere, so there is nothing to
// write. Two shapes reach it: the housekeeping clear above, and a clear for a
// steer queued before this process started.
func TestSteeringCleared_UnknownIDPersistsNothing(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_cleared", map[string]any{
			"messageIds": []string{"steer-never-seen"},
		}), FrameAttribution{})

	if rows := steerRows(t, deps, "c1"); len(rows) != 0 {
		t.Errorf("persisted %d steer entries, want 0", len(rows))
	}
}

// Keyed by id: every write of a steer takes KAS's steer id, the identity the replay
// merge pairs on, so a frame delivered twice never mints a second one.
func TestSteeringInjected_WritesUnderTheSteerIDEveryTime(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	frame := steerFrame(t, "steering_injected", map[string]any{
		"messageId": "steer-1",
		"content":   "use tabs",
	})

	tr.HandleSessionInfoUpdate(t.Context(), "c1", frame, FrameAttribution{})
	tr.HandleSessionInfoUpdate(t.Context(), "c1", frame, FrameAttribution{})

	rows := steerRows(t, deps, "c1")
	if len(rows) == 0 {
		t.Fatal("persisted no steer entry")
	}
	for i, row := range rows {
		if row.ID != "steer-1" {
			t.Errorf("entry %d ID = %q, want the steer id every time", i, row.ID)
		}
	}
}

// ADDENDUM 8 (a): a finished run's notice is read in whatever turn the reader
// prompts next, and the entry must say WHICH run and WHEN it finished, or an
// hours-old result reads as news. A step's mid-run note and a user steer carry
// neither, and a notice with no recorded run is written without a guess.
func TestSteeringInjected_ARunNoticeCarriesItsRunsProvenance(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	deps.userSteers = map[string]bool{"steer-1": true}
	deps.runNotices = map[marotte.ChatID][]stagedRunNotice{"c1": {{workflowID: "wf_1", producedTs: 1_700_000_000_000}}}
	tr := New(rolesOf(deps))

	for _, f := range []struct {
		id, text string
	}{
		{"notify-wf-7d2c", `A workflow you launched ("nightly") completed.`},
		{"notify-step-9", "[notification/warning] a step is waiting"},
		{"steer-1", "use tabs"},
		{"notify-wf-e01a", `A workflow you launched ("weekly") failed.`},
	} {
		tr.HandleSessionInfoUpdate(t.Context(), "c1",
			steerFrame(t, "steering_injected", map[string]any{"messageId": f.id, "content": f.text}), FrameAttribution{})
	}

	rows := steerRows(t, deps, "c1")
	if len(rows) != 4 {
		t.Fatalf("persisted %d steer entries, want 4", len(rows))
	}
	if rows[0].OriginRun != "wf_1" || rows[0].ProducedTs != 1_700_000_000_000 {
		t.Errorf("run notice provenance = (%q, %d), want (wf_1, 1700000000000)", rows[0].OriginRun, rows[0].ProducedTs)
	}
	for _, i := range []int{1, 2} {
		if rows[i].OriginRun != "" || rows[i].ProducedTs != 0 {
			t.Errorf("%s carries provenance (%q, %d), want none: only a run's notice is late-able", rows[i].ID, rows[i].OriginRun, rows[i].ProducedTs)
		}
	}
	if rows[3].OriginRun != "" || rows[3].ProducedTs != 0 {
		t.Errorf("a notice with no recorded run carries (%q, %d), want none rather than a guess", rows[3].OriginRun, rows[3].ProducedTs)
	}
}
