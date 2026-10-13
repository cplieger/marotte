package workflow

// The fixtures under testdata/inspect are real kiro-cli 2.29.1 replies (workspacePath scrubbed),
// recorded around one park of a one-step recipe whose step calls send_message warning: the park,
// the first read after an admitted answer (run running, step still paused), the answered step
// running, the parent session's send_message resuming it, its re-park, and a cancel. The parallel,
// repeat and non-step cases are those replies' node shapes recombined.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/keyenc"
)

const (
	fixtureRun     = "wf_66658d5c69b1f284"
	fixtureSession = "sess_c8ebb89e-a1ac-41da-aeaa-1aaa24762563"
)

func fixture(t *testing.T, name string) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "inspect", name+".json"))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return raw
}

func decoded(t *testing.T, raw json.RawMessage) Inspect {
	t.Helper()
	in, err := DecodeInspect(fixtureRun, raw)
	if err != nil {
		t.Fatalf("DecodeInspect: %v", err)
	}
	return in
}

// recombined is the parked fixture with its root's children replaced, so a derived shape keeps
// every field KAS sends beside the ones under test.
func recombined(t *testing.T, runStatus string, children ...map[string]any) Inspect {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(fixture(t, "parked"), &doc); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	state := doc["state"].(map[string]any)
	state["status"] = runStatus
	root := state["root"].(map[string]any)
	kids := make([]any, len(children))
	for i, c := range children {
		kids[i] = c
	}
	root["children"] = kids
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return decoded(t, raw)
}

func stepNode(id, status, session, signal string, awaiting any) map[string]any {
	n := map[string]any{"nodeId": id, "type": "step", "status": status, "agentName": "asker"}
	if session != "" {
		n["sessionId"] = session
	}
	if signal != "" {
		n["completionSignal"] = signal
		n["completionSignalSource"] = "send_message"
	}
	if awaiting != nil {
		n["awaitingUserInput"] = awaiting
	}
	return n
}

func position(path ...string) string {
	return keyenc.Join(fixtureRun, PathKey(path), fixtureSession)
}

func TestOpenAsks_RecordedReplies(t *testing.T) {
	t.Parallel()
	parkedPath := []string{fixtureRun, "ask"}
	cases := []struct {
		name     string
		fixture  string
		log      RunLog
		wantN    int
		wantWait Blocker
		wantHold Blocker
	}{
		{name: "a parked step is one ask", fixture: "parked", wantN: 1},
		{name: "a step not started asks nothing", fixture: "after-invoke"},
		{
			name: "the first read after an admitted answer is settling", fixture: "first-after-reply",
			wantN: 1, wantWait: BlockSettling,
		},
		{
			name: "a settling run nothing moved on is unsettled", fixture: "first-after-reply",
			log: RunLog{Quiet: true}, wantN: 1, wantWait: BlockUnsettled,
		},
		{
			name: "a run whose host died is recovering", fixture: "first-after-reply",
			log: RunLog{HostLost: true, Quiet: true}, wantN: 1, wantWait: BlockRecovering,
		},
		{
			name: "marotte's own pending holds without masking the wait", fixture: "first-after-reply",
			log: RunLog{Pending: true}, wantN: 1, wantWait: BlockSettling, wantHold: BlockPending,
		},
		{
			name: "a stop outranks a pending answer", fixture: "parked",
			log: RunLog{Pending: true, Stopping: true}, wantN: 1, wantHold: BlockStopping,
		},
		// awaitingUserInput stays true on a running step, so W1's second arm needs `paused`.
		{name: "an answered step running asks nothing", fixture: "running-after-answer"},
		// The user's chat: the parent agent answered by send_message, and pauseReason still says waiting.
		{name: "a step the parent resumed asks nothing", fixture: "running-after-parent-send"},
		{name: "a step re-parked after the parent's message asks again", fixture: "reparked-after-parent-send", wantN: 1},
		// The cancelled step keeps completionSignal need_input on an aborted node of a terminal run.
		{name: "a cancelled run asks nothing", fixture: "after-cancel"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := OpenAsks(decoded(t, fixture(t, c.fixture)), c.log)
			if len(got) != c.wantN {
				t.Fatalf("OpenAsks = %d asks %+v, want %d", len(got), got, c.wantN)
			}
			if c.wantN == 0 {
				return
			}
			a := got[0]
			if a.Wait != c.wantWait {
				t.Errorf("Wait = %q, want %q", a.Wait, c.wantWait)
			}
			if a.Hold != c.wantHold {
				t.Errorf("Hold = %q, want %q", a.Hold, c.wantHold)
			}
			if a.StepSessionID != fixtureSession || a.NodeID != "ask" || a.AgentName != "asker" {
				t.Errorf("ask addresses %q %q %q, want the step's session, node and agent", a.StepSessionID, a.NodeID, a.AgentName)
			}
			if !slices.Equal(a.NodePath, parkedPath) {
				t.Errorf("NodePath = %v, want %v", a.NodePath, parkedPath)
			}
			if a.Position != position(parkedPath...) {
				t.Errorf("Position = %q, want the run, path and session", a.Position)
			}
		})
	}
}

// The user's chat in order: the step parks, the parent agent answers by send_message (the defer
// route takes the same path), the step re-parks. The answered ask leaves, and the re-park is a new
// ask, so a second answer typed against the first gets "stale" rather than landing.
func TestOpenAsks_TheParentsSendMessageEndsTheAskAndAReparkIsANewOne(t *testing.T) {
	t.Parallel()
	first := RunLog{Epoch: "e1", Steps: map[string]StepLog{fixtureSession: {Marker: "m1", Asked: "Which branch?"}}}
	parked := OpenAsks(decoded(t, fixture(t, "parked")), first)
	if len(parked) != 1 || parked[0].TextSource != TextAsked || parked[0].Text != "Which branch?" {
		t.Fatalf("the park = %+v, want one ask carrying the step's question", parked)
	}
	resumed := RunLog{Epoch: "e1", Steps: map[string]StepLog{fixtureSession: {Marker: "m2", Open: true, Asked: ""}}}
	if got := OpenAsks(decoded(t, fixture(t, "running-after-parent-send")), resumed); len(got) != 0 {
		t.Fatalf("after the parent's send_message = %+v, want no ask on any surface", got)
	}
	reparked := RunLog{Epoch: "e1", Steps: map[string]StepLog{fixtureSession: {Marker: "m2", Asked: "And the tag?"}}}
	again := OpenAsks(decoded(t, fixture(t, "reparked-after-parent-send")), reparked)
	if len(again) != 1 {
		t.Fatalf("the re-park = %+v, want one ask", again)
	}
	if again[0].ID == parked[0].ID {
		t.Error("the re-park kept the first ask's id, so an answer typed against the first question lands on the second")
	}
	if again[0].Position != parked[0].Position {
		t.Error("the re-park moved position, so its banner stacks instead of replacing the first")
	}
}

func TestOpenAsks_AMarkerlessAskKeysOnTheEpoch(t *testing.T) {
	t.Parallel()
	in := decoded(t, fixture(t, "parked"))
	a := OpenAsks(in, RunLog{Epoch: "e1"})
	b := OpenAsks(in, RunLog{Epoch: "e2"})
	if a[0].ID == b[0].ID {
		t.Error("two boots minted one id for a markerless ask, so a pre-restart answer passes the stale check")
	}
	if a[0].ID != keyenc.Join(a[0].Position, keyenc.Join("e1", "0")) {
		t.Errorf("ID = %q, want the position plus the epoch placeholder", a[0].ID)
	}
}

func TestOpenAsks_Text(t *testing.T) {
	t.Parallel()
	in := decoded(t, fixture(t, "parked"))
	cases := []struct {
		name   string
		step   StepLog
		want   string
		source TextSource
	}{
		{"the parking turn's warning", StepLog{Asked: "Q?", Said: "words"}, "Q?", TextAsked},
		{"no warning falls back to the step's last words", StepLog{Said: "words"}, "words", TextLastMessage},
		{"an open turn is never the question", StepLog{Asked: "Q?", Said: "words", Open: true}, "words", TextLastMessage},
		{"nothing seen", StepLog{}, "", TextUnseen},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := OpenAsks(in, RunLog{Steps: map[string]StepLog{fixtureSession: c.step}})
			if got[0].Text != c.want || got[0].TextSource != c.source {
				t.Errorf("text = %q (%s), want %q (%s)", got[0].Text, got[0].TextSource, c.want, c.source)
			}
		})
	}
}

func TestOpenAsks_TheWaitPredicate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		run   string
		node  map[string]any
		wantN int
	}{
		{"paused with the signal", "paused", stepNode("ask", "paused", fixtureSession, "need_input", nil), 1},
		{"paused with no signal and awaiting input", "paused", stepNode("ask", "paused", fixtureSession, "", true), 1},
		{"paused with no signal and not awaiting", "paused", stepNode("ask", "paused", fixtureSession, "", false), 0},
		{"paused with no signal and no awaiting field", "paused", stepNode("ask", "paused", fixtureSession, "", nil), 0},
		{"paused with another signal", "paused", stepNode("ask", "paused", fixtureSession, "done", true), 0},
		{"paused with no session to address", "paused", stepNode("ask", "paused", "", "need_input", true), 0},
		{"running with the signal is a turn in progress", "running", stepNode("ask", "running", fixtureSession, "need_input", true), 0},
		{"a terminal run asks nothing", "failed", stepNode("ask", "paused", fixtureSession, "need_input", true), 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := OpenAsks(recombined(t, c.run, c.node), RunLog{}); len(got) != c.wantN {
				t.Errorf("OpenAsks = %d asks, want %d", len(got), c.wantN)
			}
		})
	}
}

func TestOpenAsks_Branches(t *testing.T) {
	t.Parallel()
	const other = "sess_other"
	parallel := func(kids ...map[string]any) map[string]any {
		cs := make([]any, len(kids))
		for i, k := range kids {
			cs[i] = k
		}
		return map[string]any{"nodeId": "par", "type": "parallel", "status": "running", "children": cs}
	}
	t.Run("a sibling step running blocks the answer", func(t *testing.T) {
		t.Parallel()
		in := recombined(t, "running", parallel(
			stepNode("a", "paused", fixtureSession, "need_input", true),
			stepNode("b", "running", other, "", nil),
		))
		got := OpenAsks(in, RunLog{Quiet: true})
		if len(got) != 1 || got[0].Wait != BlockBranchRunning {
			t.Fatalf("OpenAsks = %+v, want one ask blocked by the running branch even after the settle window", got)
		}
	})
	t.Run("a running command leaf blocks too", func(t *testing.T) {
		t.Parallel()
		in := recombined(t, "running", parallel(
			stepNode("a", "paused", fixtureSession, "need_input", true),
			map[string]any{"nodeId": "w", "type": "command", "status": "running"},
		))
		if got := OpenAsks(in, RunLog{}); got[0].Wait != BlockBranchRunning {
			t.Errorf("Wait = %q, want branch_running", got[0].Wait)
		}
	})
	t.Run("two parked branches are two asks and Continue targets the first", func(t *testing.T) {
		t.Parallel()
		in := recombined(t, "paused", parallel(
			stepNode("a", "paused", fixtureSession, "need_input", true),
			stepNode("b", "paused", other, "need_input", true),
		))
		got := OpenAsks(in, RunLog{Pending: true})
		if len(got) != 2 {
			t.Fatalf("OpenAsks = %d asks, want 2", len(got))
		}
		if got[0].ID == got[1].ID || got[0].Position == got[1].Position {
			t.Error("two branches share an identity, so one settle retires both")
		}
		if !got[0].CanContinue || got[1].CanContinue {
			t.Errorf("CanContinue = %v, %v; want only KAS's update target, even with marotte's own pending set",
				got[0].CanContinue, got[1].CanContinue)
		}
		if got[0].Wait != "" || got[0].Hold != BlockPending {
			t.Errorf("a paused run's ask = wait %q hold %q, want no wait and pending held", got[0].Wait, got[0].Hold)
		}
	})
	t.Run("a fan-out sharing one path offers Continue on KAS's target session only", func(t *testing.T) {
		t.Parallel()
		in := recombined(t, "paused", parallel(
			stepNode("a", "paused", fixtureSession, "need_input", true),
			stepNode("a", "paused", other, "need_input", true),
		))
		got := OpenAsks(in, RunLog{})
		if len(got) != 2 || PathKey(got[0].NodePath) != PathKey(got[1].NodePath) {
			t.Fatalf("OpenAsks = %+v, want two asks at one path", got)
		}
		if !got[0].CanContinue || got[1].CanContinue {
			t.Errorf("CanContinue = %v, %v; want only the first session's, the one update targets",
				got[0].CanContinue, got[1].CanContinue)
		}
	})
	t.Run("a live run offers no Continue", func(t *testing.T) {
		t.Parallel()
		in := recombined(t, "running", stepNode("ask", "paused", fixtureSession, "need_input", true))
		if got := OpenAsks(in, RunLog{}); got[0].CanContinue {
			t.Error("CanContinue on a live loop, where update queues and can erase a sibling's signal")
		}
	})
	t.Run("repeat iterations sharing a node id are distinct asks", func(t *testing.T) {
		t.Parallel()
		iter := func(n int, session string) map[string]any {
			return map[string]any{
				"nodeId": "loop#" + string(rune('0'+n)), "type": "sequence", "status": "paused",
				"iteration": n, "children": []any{stepNode("review", "paused", session, "need_input", true)},
			}
		}
		in := recombined(t, "paused", map[string]any{
			"nodeId": "loop", "type": "repeat", "status": "paused",
			"children": []any{iter(0, fixtureSession), iter(1, other)},
		})
		got := OpenAsks(in, RunLog{})
		if len(got) != 2 {
			t.Fatalf("OpenAsks = %d asks, want 2", len(got))
		}
		if got[1].NodePath[2] != "iter-1" {
			t.Errorf("the second iteration's path = %v, want KAS's wire spelling iter-1", got[1].NodePath)
		}
		if got[0].ID == got[1].ID {
			t.Error("two iterations share an ask id")
		}
	})
}

func TestDecodeInspect_Refuses(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"a syntax error": `{"state":`,
		"no state":       `{"workflowId":"` + fixtureRun + `"}`,
		"another run":    `{"state":{"workflowId":"wf_other","status":"paused"}}`,
		"no run named":   `{"state":{"status":"paused"}}`,
		"a reply naming another run than its state": `{"workflowId":"wf_other","state":{"workflowId":"` + fixtureRun + `","status":"paused"}}`,
		"a state naming another run than its reply": `{"workflowId":"` + fixtureRun + `","state":{"workflowId":"wf_other","status":"paused"}}`,
		"a type drift":     `{"state":{"workflowId":"` + fixtureRun + `","status":"paused","root":{"nodeId":"r","awaitingUserInput":"yes"}}}`,
		"a drifted signal": `{"state":{"workflowId":"` + fixtureRun + `","status":"paused","root":{"nodeId":"r","completionSignal":7}}}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			in, err := DecodeInspect(fixtureRun, json.RawMessage(raw))
			if err == nil || in.OK {
				t.Fatalf("DecodeInspect = %+v, %v; want not OK with a reason", in, err)
			}
			if got := OpenAsks(in, RunLog{}); got != nil {
				t.Errorf("OpenAsks of an unreadable reply = %+v, want none", got)
			}
		})
	}
	if _, err := DecodeInspect(fixtureRun, json.RawMessage(cases["another run"])); !errors.Is(err, ErrOtherRun) {
		t.Errorf("another run's reply = %v, want ErrOtherRun", err)
	}
}

func TestDecodeInspect_ARunNamedOnlyOnTheReplyKeysItsAsks(t *testing.T) {
	t.Parallel()
	raw := `{"workflowId":"` + fixtureRun + `","state":{"status":"paused","root":{"nodeId":"s","type":"step",` +
		`"status":"paused","sessionId":"` + fixtureSession + `","completionSignal":"need_input"}}}`
	in, err := DecodeInspect(fixtureRun, json.RawMessage(raw))
	if err != nil || !in.OK {
		t.Fatalf("DecodeInspect = %+v, %v; want OK", in, err)
	}
	if in.State.WorkflowID != fixtureRun {
		t.Errorf("State.WorkflowID = %q, want %q", in.State.WorkflowID, fixtureRun)
	}
	asks := OpenAsks(in, RunLog{})
	if len(asks) != 1 {
		t.Fatalf("OpenAsks = %+v, want one ask", asks)
	}
	parts, err := keyenc.Split(asks[0].Position)
	if err != nil || len(parts) == 0 || parts[0] != fixtureRun {
		t.Errorf("ask position %q = %v, %v; want it to name %q", asks[0].Position, parts, err, fixtureRun)
	}
}

func TestStatusUpdateTarget(t *testing.T) {
	t.Parallel()
	in := recombined(t, "running",
		stepNode("a", "paused", fixtureSession, "need_input", true),
		stepNode("b", "running", "sess_b", "", nil))
	got, ok := StatusUpdateTarget(in.State)
	if !ok || got.NodeID != "b" {
		t.Errorf("StatusUpdateTarget = %+v, %v; want the running step ahead of the earlier paused one", got, ok)
	}
	none := recombined(t, "running", stepNode("a", "completed", fixtureSession, "", nil))
	if _, ok := StatusUpdateTarget(none.State); ok {
		t.Error("StatusUpdateTarget found a target in a run with no running or paused step")
	}
}

func TestParkedSteps_MatchesOpenAsks(t *testing.T) {
	t.Parallel()
	in := decoded(t, fixture(t, "parked"))
	steps := ParkedSteps(in)
	asks := OpenAsks(in, RunLog{})
	if len(steps) != len(asks) || steps[0].SessionID != asks[0].StepSessionID {
		t.Errorf("ParkedSteps = %+v, want the steps OpenAsks answers", steps)
	}
	if ParkedSteps(decoded(t, fixture(t, "after-cancel"))) != nil {
		t.Error("ParkedSteps of a cancelled run named a step")
	}
	mixed := recombined(t, "running",
		stepNode("a", "running", "sess_a", "", nil), stepNode("b", "paused", fixtureSession, "need_input", true))
	if got := ParkedSteps(mixed); len(got) != 1 || got[0].NodeID != "b" {
		t.Errorf("ParkedSteps = %+v, want only the waiting step, not the running one beside it", got)
	}
}
