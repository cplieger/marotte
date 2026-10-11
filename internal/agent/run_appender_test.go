package agent

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
)

// runLoggingHub is a runtime whose Runs has a run log, set directly rather than through the order-sensitive wiring.
func runLoggingHub(t *testing.T) (*Runtime, *testChatStore) {
	t.Helper()
	h, cs, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	h.runs.log = newRunLog(t.TempDir())
	return h, cs
}

// A content frame can open a step's turn while the registry is cold; a later fold that knows the id names it.
func TestRunFoldTarget_ALaterFoldNamesATurnOpenedWithNoNodeID(t *testing.T) {
	h, _ := runLoggingHub(t)
	step := &translate.RunStep{RunID: "wf_1", NodePath: workflow.PathKey([]string{"wf_1", "a"}), SessionID: "step-session-a"}
	if _, ok := h.runs.RunFoldTarget(t.Context(), step, ""); !ok {
		t.Fatal("Setup: RunFoldTarget(no id) opened no turn")
	}
	step.NodeID = "a"
	if _, ok := h.runs.RunFoldTarget(t.Context(), step, ""); !ok {
		t.Fatal("RunFoldTarget(with id) found no open turn")
	}
	if got := h.runs.log.openNodeIDs("wf_1"); !maps.Equal(got, map[string]struct{}{"a": {}}) {
		t.Errorf("OpenNodeIDs() after a fold naming the node = %v, want [a]: awaitingAnswer reads the step as not waiting", got)
	}
}

func refuseStep(t *testing.T, rs *Runs, runID string, path []string, r *marotte.RefusalInfo) {
	t.Helper()
	nodePath := workflow.PathKey(path)
	rs.RunNodeStart(t.Context(), &translate.RunStep{RunID: runID, NodePath: nodePath, NodeID: path[len(path)-1], SessionID: "step-session-1"}, "")
	turn := rs.log.turn(runID, nodePath)
	if turn == nil {
		t.Fatalf("no open turn for %s/%s", runID, nodePath)
	}
	if r != nil {
		turn.SetRefusal(r)
	}
	if !rs.log.stopReason(runID, nodePath, marotte.StopReasonRefusal) {
		t.Fatalf("StopReason refused on the open turn %s/%s", runID, nodePath)
	}
}

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

// TestRunNodeComplete_ARefusedStepTellsTheLaunchingChat pins that KAS grades a refused step `completed`.
func TestRunNodeComplete_ARefusedStepTellsTheLaunchingChat(t *testing.T) {
	const (
		id     = "wf_1"
		chatID = marotte.ChatID("c1")
	)
	path := []string{"wf_1", "loop", "iter-1", "build"}
	h, cs := runLoggingHub(t)
	seedChat(t, cs, chatID)
	h.runs.grantLease(t.Context(), id, "nightly",
		launchOrigin{origin: runlease.OriginAgent, chatID: string(chatID)})
	refuseStep(t, h.runs, id, path, &marotte.RefusalInfo{
		Category:    "policy",
		Explanation: "I will not continue with this request.",
	})

	h.runs.RunNodeComplete(t.Context(), id, path, "completed", "")

	entries, err := cs.All(t.Context(), chatID)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	i := slices.IndexFunc(entries, func(e marotte.Entry) bool { return e.Kind == marotte.EntryKindSteer })
	if i < 0 {
		t.Fatalf("no steer entry reached the launching chat; entries: %+v", entries)
	}
	if want := stepNoteID(id, stepNoteRefused, workflow.PathKey(path)); entries[i].ID != want {
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
	// The reader sees the step's segments joined with "/", never the key's own spelling.
	for _, want := range []string{"nightly", "step wf_1/loop/iter-1/build was declined", "policy", "I will not continue with this request."} {
		if !strings.Contains(steer.Text, want) {
			t.Errorf("note text = %q, want it to carry %q", steer.Text, want)
		}
	}
	// A deterministic refusal must not read "try again".
	if !strings.Contains(steer.Text, "declined again") {
		t.Errorf("note text = %q, want it to say a re-run is declined again", steer.Text)
	}
}

// KAS grades a step its iteration limit stopped `completed`, so only this note tells the launching chat it did not finish.
func TestRunNodeComplete_AStepStoppedAtTheModelCallLimitTellsTheLaunchingChat(t *testing.T) {
	const (
		id     = "wf_1"
		chatID = marotte.ChatID("c1")
	)
	path := []string{"wf_1", "build"}
	nodePath := workflow.PathKey(path)
	h, cs := runLoggingHub(t)
	seedChat(t, cs, chatID)
	h.runs.grantLease(t.Context(), id, "nightly",
		launchOrigin{origin: runlease.OriginAgent, chatID: string(chatID)})
	h.runs.RunNodeStart(t.Context(), &translate.RunStep{RunID: id, NodePath: nodePath, NodeID: "build", SessionID: "step-session-1"}, "")
	if !h.runs.log.stopReason(id, nodePath, marotte.StopReasonToolUse) {
		t.Fatal("StopReason(tool_use) refused on the open turn")
	}

	h.runs.RunNodeComplete(t.Context(), id, path, "completed", "")

	entries, err := cs.All(t.Context(), chatID)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	i := slices.IndexFunc(entries, func(e marotte.Entry) bool { return e.Kind == marotte.EntryKindSteer })
	if i < 0 {
		t.Fatalf("no steer entry reached the launching chat; entries: %+v", entries)
	}
	if want := stepNoteID(id, stepNoteModelCallLimit, nodePath); entries[i].ID != want {
		t.Errorf("note id = %q, want %q", entries[i].ID, want)
	}
	var steer marotte.EntrySteer
	if err := json.Unmarshal(entries[i].Payload, &steer); err != nil {
		t.Fatalf("decode the steer: %v", err)
	}
	if want := "nightly step wf_1/build stopped early. " + marotte.ModelCallLimitStepReason; steer.Text != want {
		t.Errorf("note text = %q, want %q", steer.Text, want)
	}
	if steer.Severity != "warning" || steer.OriginRun != id {
		t.Errorf("note severity/run = %q/%q, want warning/%s", steer.Severity, steer.OriginRun, id)
	}
}

// Every refusal field is optional, so absence is not evidence the model did not decline.
func TestRunNodeComplete_ARefusalWithNoMetadataStillLeavesANote(t *testing.T) {
	const (
		id     = "wf_1"
		chatID = marotte.ChatID("c1")
	)
	h, cs := runLoggingHub(t)
	seedChat(t, cs, chatID)
	h.runs.grantLease(t.Context(), id, "nightly",
		launchOrigin{origin: runlease.OriginAgent, chatID: string(chatID)})
	refuseStep(t, h.runs, id, []string{"build"}, nil)

	h.runs.RunNodeComplete(t.Context(), id, []string{"build"}, "completed", "")

	steers := chatSteers(t, cs, chatID)
	if len(steers) != 1 {
		t.Fatalf("got %d notes, want one", len(steers))
	}
	if !strings.Contains(steers[0].Text, "build was declined by the model.") {
		t.Errorf("note text = %q, want a sentence that stands with no category", steers[0].Text)
	}
}

// The run's own frames already say it finished.
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
			h.runs.RunNodeStart(t.Context(), &translate.RunStep{RunID: id, NodePath: "build", NodeID: "build", SessionID: "step-session-1"}, "")
			if !h.runs.log.stopReason(id, "build", tc.raw) {
				t.Fatalf("StopReason(%q) refused on the open turn", tc.raw)
			}

			h.runs.RunNodeComplete(t.Context(), id, []string{"build"}, tc.status, "")

			if steers := chatSteers(t, cs, chatID); len(steers) != 0 {
				t.Errorf("%s left %d notes, want none: %+v", tc.desc, len(steers), steers)
			}
		})
	}
}

// A parentless lease carries no chat. The store would refuse the append anyway, so the logged
// failure is what proves nothing was attempted. No t.Parallel: captureLogs swaps the slog default.
func TestRunNodeComplete_AParentlessRunLeavesNoNote(t *testing.T) {
	const id = "wf_1"
	h, cs := runLoggingHub(t)
	leased(t, h.runs, id)
	refuseStep(t, h.runs, id, []string{"build"}, &marotte.RefusalInfo{Category: "policy"})
	logs := captureLogs(t)

	h.runs.RunNodeComplete(t.Context(), id, []string{"build"}, "completed", "")

	if strings.Contains(logs.String(), "a steer was not recorded") {
		t.Errorf("a parentless run's refusal tried to write a note: %s", logs.String())
	}
	if chats := cs.List(t.Context()); len(chats) != 0 {
		t.Errorf("a parentless run's refusal created a chat: %+v", chats)
	}
}

// CloseNode closes a turn once, so a repeated node_complete seals no second note.
func TestRunNodeComplete_ARefusalIsNotedOnce(t *testing.T) {
	const (
		id     = "wf_1"
		chatID = marotte.ChatID("c1")
	)
	h, cs := runLoggingHub(t)
	seedChat(t, cs, chatID)
	h.runs.grantLease(t.Context(), id, "nightly",
		launchOrigin{origin: runlease.OriginAgent, chatID: string(chatID)})
	refuseStep(t, h.runs, id, []string{"build"}, &marotte.RefusalInfo{Category: "policy"})

	h.runs.RunNodeComplete(t.Context(), id, []string{"build"}, "completed", "")
	h.runs.RunNodeComplete(t.Context(), id, []string{"build"}, "completed", "")

	if steers := chatSteers(t, cs, chatID); len(steers) != 1 {
		t.Errorf("got %d notes for one refusal, want one", len(steers))
	}
}
