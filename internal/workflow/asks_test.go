package workflow

// The fixtures under testdata/inspect are real kiro-cli 2.37.0 replies (workspacePath and model ids scrubbed),
// recorded around one park of a one-step recipe whose step calls send_message warning: the park,
// the first read after an admitted answer (run running, step still paused), the answered step
// running, the parent session's send_message resuming it, its re-park, and a cancel. gated-park is
// a step with a `completion` gate that ended its turn with no signal. The parallel, repeat and
// non-step cases are those replies' node shapes recombined. unsaved-signal-park is a kiro-cli 2.31.0
// park reply with finalizeStepAfterTurn's lost-signal arm applied (signal cleared, its pauseReason)
// and the park record parkAwaitingUser writes added by hand, since that arm has not been triggered live.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/go/keyenc"
	"github.com/cplieger/marotte/internal/marotte"
)

const (
	fixtureRun     = "wf_a9239a70a27fce6e"
	fixtureSession = "sess_4cd99833-1963-491f-a2b0-a3b26b5b6e85"
)

// parked is the attention KAS's park writes on a step (markWaitingForUser).
var parked = []any{map[string]any{"kind": "question", "since": "2026-11-01T09:58:33.463Z"}}

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

func stepNode(id, status, session, signal string, attention []any) map[string]any {
	n := map[string]any{"nodeId": id, "type": "step", "status": status, "agentName": "asker"}
	if session != "" {
		n["sessionId"] = session
	}
	if signal != "" {
		n["completionSignal"] = signal
		n["completionSignalSource"] = "send_message"
	}
	if attention != nil {
		n["attention"] = attention
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
		wantWait marotte.RunAskBlocker
		wantHold marotte.RunAskBlocker
	}{
		{name: "a parked step is one ask", fixture: "parked", wantN: 1},
		{name: "a step not started asks nothing", fixture: "after-invoke"},
		{
			name: "the first read after an admitted answer is settling", fixture: "first-after-reply",
			wantN: 1, wantWait: marotte.RunAskSettling,
		},
		{
			name: "a settling run nothing moved on is unsettled", fixture: "first-after-reply",
			log: RunLog{Quiet: true}, wantN: 1, wantWait: marotte.RunAskUnsettled,
		},
		{
			name: "marotte's own pending holds without masking the wait", fixture: "first-after-reply",
			log: RunLog{Pending: true}, wantN: 1, wantWait: marotte.RunAskSettling, wantHold: marotte.RunAskPending,
		},
		{
			name: "a stop outranks a pending answer", fixture: "parked",
			log: RunLog{Pending: true, Stopping: true}, wantN: 1, wantHold: marotte.RunAskStopping,
		},
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
	if len(parked) != 1 || parked[0].TextSource != marotte.RunAskTextAsked || parked[0].Text != "Which branch?" {
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

// KAS can keep a step paused while its session runs a turn: an answer's wake turn beside a running
// sibling, or the parking turn before it ends. The step is working, so it asks nothing until that
// turn ends.
func TestOpenAsks_AStepWhoseTurnRunsAsksNothing(t *testing.T) {
	t.Parallel()
	in := decoded(t, fixture(t, "parked"))
	for _, step := range []StepLog{
		{Marker: "m2", Open: true, Said: "words"},
		{Marker: "m2", Open: true, Asked: "Which branch?", Said: "words"},
	} {
		if got := OpenAsks(in, RunLog{Epoch: "e1", Steps: map[string]StepLog{fixtureSession: step}}); len(got) != 0 {
			t.Errorf("with the turn open (%+v) the asks = %+v, want none", step, got)
		}
	}
	ended := OpenAsks(in, RunLog{Epoch: "e1", Steps: map[string]StepLog{fixtureSession: {Marker: "m2", Said: "words"}}})
	if len(ended) != 1 || ended[0].ID != keyenc.Join(ended[0].Position, "m2") {
		t.Errorf("once the turn ended the asks = %+v, want one keyed on its turn's marker", ended)
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
		source marotte.RunAskText
	}{
		{"the parking turn's warning", StepLog{Asked: "Q?", Said: "words"}, "Q?", marotte.RunAskTextAsked},
		{"no warning falls back to the step's last words", StepLog{Said: "words"}, "words", marotte.RunAskTextLastMessage},
		{"nothing seen", StepLog{}, "", marotte.RunAskTextUnseen},
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
		{"paused with the signal and the park record", "paused", stepNode("ask", "paused", fixtureSession, "need_input", parked), 1},
		{"a gated park: no signal, the park record", "paused", stepNode("ask", "paused", fixtureSession, "", parked), 1},
		{"paused with the signal and no park record", "paused", stepNode("ask", "paused", fixtureSession, "need_input", nil), 0},
		{"paused with only the step session's permission request", "paused", stepNode("ask", "paused", fixtureSession, "", []any{
			map[string]any{"kind": "permission", "since": "2026-11-01T09:58:33.463Z", "toolCallId": "tooluse_1"},
		}), 0},
		{"paused with only the step session's input request", "paused", stepNode("ask", "paused", fixtureSession, "need_input", []any{
			map[string]any{"kind": "question", "since": "2026-11-01T09:58:33.463Z", "toolCallId": "tooluse_1"},
		}), 0},
		{"paused with no session to address", "paused", stepNode("ask", "paused", "", "need_input", parked), 0},
		{"running with the park record is a turn in progress", "running", stepNode("ask", "running", fixtureSession, "need_input", parked), 0},
		{"a terminal run asks nothing", "failed", stepNode("ask", "paused", fixtureSession, "need_input", parked), 0},
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
			stepNode("a", "paused", fixtureSession, "need_input", parked),
			stepNode("b", "running", other, "", nil),
		))
		got := OpenAsks(in, RunLog{Quiet: true})
		if len(got) != 1 || got[0].Wait != marotte.RunAskBranchRunning {
			t.Fatalf("OpenAsks = %+v, want one ask blocked by the running branch even after the settle window", got)
		}
	})
	t.Run("a running command leaf blocks too", func(t *testing.T) {
		t.Parallel()
		in := recombined(t, "running", parallel(
			stepNode("a", "paused", fixtureSession, "need_input", parked),
			map[string]any{"nodeId": "w", "type": "command", "status": "running"},
		))
		if got := OpenAsks(in, RunLog{}); got[0].Wait != marotte.RunAskBranchRunning {
			t.Errorf("Wait = %q, want branch_running", got[0].Wait)
		}
	})
	t.Run("two parked branches are two asks", func(t *testing.T) {
		t.Parallel()
		in := recombined(t, "paused", parallel(
			stepNode("a", "paused", fixtureSession, "need_input", parked),
			stepNode("b", "paused", other, "need_input", parked),
		))
		got := OpenAsks(in, RunLog{Pending: true})
		if len(got) != 2 {
			t.Fatalf("OpenAsks = %d asks, want 2", len(got))
		}
		if got[0].ID == got[1].ID || got[0].Position == got[1].Position {
			t.Error("two branches share an identity, so one settle retires both")
		}
		if got[0].Wait != "" || got[0].Hold != marotte.RunAskPending {
			t.Errorf("a paused run's ask = wait %q hold %q, want no wait and pending held", got[0].Wait, got[0].Hold)
		}
	})
	t.Run("a fan-out sharing one path is two asks", func(t *testing.T) {
		t.Parallel()
		in := recombined(t, "paused", parallel(
			stepNode("a", "paused", fixtureSession, "need_input", parked),
			stepNode("a", "paused", other, "need_input", parked),
		))
		got := OpenAsks(in, RunLog{})
		if len(got) != 2 || PathKey(got[0].NodePath) != PathKey(got[1].NodePath) {
			t.Fatalf("OpenAsks = %+v, want two asks at one path", got)
		}
	})
	t.Run("repeat iterations sharing a node id are distinct asks", func(t *testing.T) {
		t.Parallel()
		iter := func(n int, session string) map[string]any {
			return map[string]any{
				"nodeId": "loop#" + string(rune('0'+n)), "type": "sequence", "status": "paused",
				"iteration": n, "children": []any{stepNode("review", "paused", session, "need_input", parked)},
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
		"a type drift":     `{"state":{"workflowId":"` + fixtureRun + `","status":"paused","root":{"nodeId":"r","attention":"yes"}}}`,
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
	if _, err := DecodeInspect(fixtureRun, json.RawMessage(cases["another run"])); !errors.Is(err, errOtherRun) {
		t.Errorf("another run's reply = %v, want errOtherRun", err)
	}
}

func TestDecodeInspect_ARunNamedOnlyOnTheReplyKeysItsAsks(t *testing.T) {
	t.Parallel()
	raw := `{"workflowId":"` + fixtureRun + `","state":{"status":"paused","root":{"nodeId":"s","type":"step",` +
		`"status":"paused","sessionId":"` + fixtureSession + `","completionSignal":"need_input",` +
		`"attention":[{"kind":"question","since":"2026-11-01T09:58:33.463Z"}]}}}`
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
		stepNode("a", "running", "sess_a", "", nil), stepNode("b", "paused", fixtureSession, "need_input", parked))
	if got := ParkedSteps(mixed); len(got) != 1 || got[0].NodeID != "b" {
		t.Errorf("ParkedSteps = %+v, want only the waiting step, not the running one beside it", got)
	}
}

// A send_message signal KAS could not save parks the step with no signal: it is still one ask,
// and the card carries KAS's reason rather than reading as a question.
func TestOpenAsks_UnsavedSignalParkCarriesItsReason(t *testing.T) {
	t.Parallel()
	const (
		run     = "wf_7e891a7469b82ed3"
		session = "sess_2336f73e-7b71-4acd-bbdf-7ff92c0ac98f"
		reason  = "Step 'ask' sent a send_message success signal that could not be saved, so it is paused awaiting user input instead of finishing."
	)
	in, err := DecodeInspect(run, fixture(t, "unsaved-signal-park"))
	if err != nil {
		t.Fatalf("DecodeInspect: %v", err)
	}
	log := RunLog{Steps: map[string]StepLog{session: {Marker: "m1", Said: "All three files are updated."}}}
	got := OpenAsks(in, log)
	if len(got) != 1 {
		t.Fatalf("OpenAsks = %d asks %+v, want 1", len(got), got)
	}
	a := got[0]
	if a.Reason != reason {
		t.Errorf("Reason = %q, want %q", a.Reason, reason)
	}
	if a.Blocked() != "" {
		t.Errorf("Blocked = %q, want nothing blocking", a.Blocked())
	}
	if a.TextSource != marotte.RunAskTextLastMessage || a.Text != "All three files are updated." {
		t.Errorf("text = %q (%s), want the step's last message", a.Text, a.TextSource)
	}
}

// A step that signalled need_input asks its own question, and with two steps waiting the one
// run-level reason names either, so neither carries it.
func TestOpenAsks_ReasonOnlyWhereItNamesThisPark(t *testing.T) {
	t.Parallel()
	if got := OpenAsks(decoded(t, fixture(t, "parked")), RunLog{}); len(got) != 1 || got[0].Reason != "" {
		t.Errorf("need_input park: asks %+v, want one with no reason", got)
	}
	two := recombined(t, statusPaused,
		stepNode("a", statusPaused, fixtureSession, "", parked),
		stepNode("b", statusPaused, "sess_other", "", parked))
	two.State.PauseReason = "Step 'b' is awaiting user input."
	got := OpenAsks(two, RunLog{})
	if len(got) != 2 {
		t.Fatalf("OpenAsks = %d asks %+v, want 2", len(got), got)
	}
	for _, a := range got {
		if a.Reason != "" {
			t.Errorf("%s: Reason = %q, want none while two steps wait", a.NodeID, a.Reason)
		}
	}
}

// PausedOnAsks is the fact that withholds Resume: only a paused run whose every paused leaf waits on
// a person qualifies, since one ordinary pause beside an ask is a pause Resume undoes.
func TestPausedOnAsks(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		in   Inspect
		want bool
	}{
		"one step asks": {
			in:   recombined(t, statusPaused, stepNode("ask", statusPaused, fixtureSession, "need_input", parked)),
			want: true,
		},
		"two steps ask": {
			in: recombined(t, statusPaused,
				stepNode("a", statusPaused, fixtureSession, "need_input", parked),
				stepNode("b", statusPaused, "sess_other", "need_input", parked)),
			want: true,
		},
		"an ask beside an ordinary paused step": {
			in: recombined(t, statusPaused,
				stepNode("a", statusPaused, fixtureSession, "need_input", parked),
				stepNode("b", statusPaused, "sess_other", "", nil)),
		},
		"a paused step that asks nothing": {
			in: recombined(t, statusPaused, stepNode("a", statusPaused, fixtureSession, "", nil)),
		},
		"a gated park alone": {
			in:   recombined(t, statusPaused, stepNode("gated", statusPaused, fixtureSession, "", parked)),
			want: true,
		},
		"a running run holding a parked step": {
			in: recombined(t, statusRunning, stepNode("a", statusPaused, fixtureSession, "need_input", parked)),
		},
		"an unreadable reply": {in: Inspect{}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := PausedOnAsks(tc.in); got != tc.want {
				t.Errorf("PausedOnAsks = %v, want %v", got, tc.want)
			}
		})
	}
}

// A step with a `completion` gate that ended its turn with no signal is parked by KAS waiting for the
// next user message: it is an ask carrying KAS's reason, and a run paused on it alone is paused on asks.
func TestOpenAsks_AGatedParkIsAnAsk(t *testing.T) {
	t.Parallel()
	const (
		run     = "wf_af70a38d2a7862f0"
		session = "sess_746a39f5-fb36-487e-a2a3-5335af805af4"
	)
	in, err := DecodeInspect(run, fixture(t, "gated-park"))
	if err != nil {
		t.Fatalf("DecodeInspect: %v", err)
	}
	got := OpenAsks(in, RunLog{})
	if len(got) != 1 {
		t.Fatalf("OpenAsks = %d asks %+v, want 1", len(got), got)
	}
	if a := got[0]; a.NodeID != "gated" || a.StepSessionID != session || a.Reason != "Step 'gated' is waiting for the next user message." {
		t.Errorf("ask = node %q session %q reason %q, want the gated step with KAS's reason", a.NodeID, a.StepSessionID, a.Reason)
	}
	if !PausedOnAsks(in) {
		t.Error("PausedOnAsks = false, want true: false offers a Resume that KAS answers by re-parking the step")
	}
}

// AskedAt is the park record's own start, which a restart keeps, not anything marotte observed.
func TestOpenAsks_AskedAtIsTheWaitStart(t *testing.T) {
	t.Parallel()
	got := OpenAsks(decoded(t, fixture(t, "parked")), RunLog{})
	if len(got) != 1 || got[0].AskedAt != "2026-11-01T09:58:33.463Z" {
		t.Errorf("asks = %+v, want one asked at the park record's since", got)
	}
}
