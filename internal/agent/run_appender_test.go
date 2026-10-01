package agent

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

// runLoggingHub is a runtime whose Runs has a run log. The wiring gates the log on a
// config dir, and the note under test is the run log's, not the wiring's, so the field
// is set directly rather than rebuilding the order-sensitive construction sequence with
// one more option.
func runLoggingHub(t *testing.T) (*Runtime, *testChatStore) {
	t.Helper()
	h, cs, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	h.runs.log = newRunLog(t.TempDir())
	return h, cs
}

// refuseStep opens the step's turn, latches the refusal metadata a tagged chunk marks
// the turn with, and records the turn_end that says the model declined.
func refuseStep(t *testing.T, rs *Runs, runID, nodePath string, r *marotte.RefusalInfo) {
	t.Helper()
	rs.RunNodeStart(t.Context(), runID, nodePath, "step-session-1", "")
	turn := rs.log.Turn(runID, nodePath)
	if turn == nil {
		t.Fatalf("no open turn for %s/%s", runID, nodePath)
	}
	if r != nil {
		turn.SetRefusal(r)
	}
	if !rs.log.StopReason(runID, nodePath, marotte.StopReasonRefusal) {
		t.Fatalf("StopReason refused on the open turn %s/%s", runID, nodePath)
	}
}

// chatSteers is every steer entry the chat's log holds, decoded.
func chatSteers(t *testing.T, cs *testChatStore, chatID marotte.ChatID) []marotte.EntrySteer {
	t.Helper()
	entries, err := cs.All(t.Context(), chatID)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	var out []marotte.EntrySteer
	for _, e := range entries {
		if e.Kind != marotte.EntryKindSteer {
			continue
		}
		var s marotte.EntrySteer
		if err := json.Unmarshal(e.Payload, &s); err != nil {
			t.Fatalf("decode the steer %s: %v", e.ID, err)
		}
		out = append(out, s)
	}
	return out
}

// TestRunNodeComplete_ARefusedStepTellsTheLaunchingChat: KAS grades a refused step
// `completed`, so the launching chat otherwise shows a run that finished and says
// nothing about a step the model declined.
func TestRunNodeComplete_ARefusedStepTellsTheLaunchingChat(t *testing.T) {
	const (
		id       = "wf_1"
		chatID   = marotte.ChatID("c1")
		nodePath = "wf_1/loop#0/iter-1/build"
	)
	h, cs := runLoggingHub(t)
	seedChat(t, cs, chatID)
	h.runs.grantLease(t.Context(), id, "nightly",
		launchOrigin{origin: runlease.OriginAgent, chatID: string(chatID)})
	refuseStep(t, h.runs, id, nodePath, &marotte.RefusalInfo{
		Category:    "policy",
		Explanation: "I will not continue with this request.",
	})

	h.runs.RunNodeComplete(t.Context(), id, nodePath, "completed", "")

	entries, err := cs.All(t.Context(), chatID)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	i := slices.IndexFunc(entries, func(e marotte.Entry) bool { return e.Kind == marotte.EntryKindSteer })
	if i < 0 {
		t.Fatalf("no steer entry reached the launching chat; entries: %+v", entries)
	}
	if want := stepRefusalNoteID(id, nodePath); entries[i].ID != want {
		t.Errorf("note id = %q, want %q (the step's own path, so each refused step gets its own row)",
			entries[i].ID, want)
	}
	var steer marotte.EntrySteer
	if err := json.Unmarshal(entries[i].Payload, &steer); err != nil {
		t.Fatalf("decode the steer: %v", err)
	}
	if steer.Origin != marotte.SteerOriginAgent || steer.State != marotte.SteerStateRead {
		t.Errorf("note origin/state = %q/%q, want agent/read", steer.Origin, steer.State)
	}
	if steer.Severity != "warning" {
		t.Errorf("note severity = %q, want warning", steer.Severity)
	}
	if steer.OriginRun != id {
		t.Errorf("note provenance = run %q, want %q", steer.OriginRun, id)
	}
	for _, want := range []string{"nightly", nodePath, "policy", "I will not continue with this request."} {
		if !strings.Contains(steer.Text, want) {
			t.Errorf("note text = %q, want it to carry %q", steer.Text, want)
		}
	}
	// A refusal is deterministic, so a note reading "try again" sends the reader in a
	// circle; it has to say the step or its model must change.
	if !strings.Contains(steer.Text, "declined again") {
		t.Errorf("note text = %q, want it to say a re-run is declined again", steer.Text)
	}
}

// A refusal with no category and no explanation still leaves a note: every field of the
// block is optional, so absence is not evidence the model did not decline.
func TestRunNodeComplete_ARefusalWithNoMetadataStillLeavesANote(t *testing.T) {
	const (
		id     = "wf_1"
		chatID = marotte.ChatID("c1")
	)
	h, cs := runLoggingHub(t)
	seedChat(t, cs, chatID)
	h.runs.grantLease(t.Context(), id, "nightly",
		launchOrigin{origin: runlease.OriginAgent, chatID: string(chatID)})
	refuseStep(t, h.runs, id, "build", nil)

	h.runs.RunNodeComplete(t.Context(), id, "build", "completed", "")

	steers := chatSteers(t, cs, chatID)
	if len(steers) != 1 {
		t.Fatalf("got %d notes, want one", len(steers))
	}
	if !strings.Contains(steers[0].Text, "build was declined by the model.") {
		t.Errorf("note text = %q, want a sentence that stands with no category", steers[0].Text)
	}
}

// A step that ran leaves the launching chat alone: the run's ordinary frames already
// say it finished.
func TestRunNodeComplete_AStepThatRanLeavesNoNote(t *testing.T) {
	const (
		id     = "wf_1"
		chatID = marotte.ChatID("c1")
	)
	for _, tc := range []struct {
		desc   string
		raw    marotte.StopReason
		status string
	}{
		{"a completed step", marotte.StopReasonEndTurn, "completed"},
		{"a failed step", marotte.StopReasonError, "failed"},
		{"a cancelled step", marotte.StopReasonCancelled, "cancelled"},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			h, cs := runLoggingHub(t)
			seedChat(t, cs, chatID)
			h.runs.grantLease(t.Context(), id, "nightly",
				launchOrigin{origin: runlease.OriginAgent, chatID: string(chatID)})
			h.runs.RunNodeStart(t.Context(), id, "build", "step-session-1", "")
			if !h.runs.log.StopReason(id, "build", tc.raw) {
				t.Fatalf("StopReason(%q) refused on the open turn", tc.raw)
			}

			h.runs.RunNodeComplete(t.Context(), id, "build", tc.status, "")

			if steers := chatSteers(t, cs, chatID); len(steers) != 0 {
				t.Errorf("%s left %d notes, want none: %+v", tc.desc, len(steers), steers)
			}
		})
	}
}

// A parentless run's lease carries no chat, so there is nobody to tell.
//
// The assertion is that nothing was ATTEMPTED, not merely that no chat appeared: the
// empty chat id the lease carries is not a valid one, so the store refuses the append on
// its own and a note written anyway leaves the record equally untouched. What separates
// the two is the failure the refused append logs — the guard's removal is invisible
// without it. No t.Parallel: captureLogs swaps the slog default.
func TestRunNodeComplete_AParentlessRunLeavesNoNote(t *testing.T) {
	const id = "wf_1"
	h, cs := runLoggingHub(t)
	leased(t, h.runs, id)
	refuseStep(t, h.runs, id, "build", &marotte.RefusalInfo{Category: "policy"})
	logs := captureLogs(t)

	h.runs.RunNodeComplete(t.Context(), id, "build", "completed", "")

	if strings.Contains(logs.String(), "a steer was not recorded") {
		t.Errorf("a parentless run's refusal tried to write a note: %s", logs.String())
	}
	if chats := cs.List(t.Context()); len(chats) != 0 {
		t.Errorf("a parentless run's refusal created a chat: %+v", chats)
	}
}

// One refusal is one note, and nothing is kept to make that true: CloseNode closes an
// open turn exactly once, so a repeated node_complete seals no second turn_close.
func TestRunNodeComplete_ARefusalIsNotedOnce(t *testing.T) {
	const (
		id     = "wf_1"
		chatID = marotte.ChatID("c1")
	)
	h, cs := runLoggingHub(t)
	seedChat(t, cs, chatID)
	h.runs.grantLease(t.Context(), id, "nightly",
		launchOrigin{origin: runlease.OriginAgent, chatID: string(chatID)})
	refuseStep(t, h.runs, id, "build", &marotte.RefusalInfo{Category: "policy"})

	h.runs.RunNodeComplete(t.Context(), id, "build", "completed", "")
	h.runs.RunNodeComplete(t.Context(), id, "build", "completed", "")

	if steers := chatSteers(t, cs, chatID); len(steers) != 1 {
		t.Errorf("got %d notes for one refusal, want one", len(steers))
	}
}
