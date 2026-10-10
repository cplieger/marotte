package translate

// A step is neither the chat nor a subagent, so the drop and subagent questions have
// different answers for one.

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/slogx/capture"
)

const (
	testChat   = marotte.ChatID("chat-1")
	testParent = "sess_parent"
	testStep   = "sess_step"
	testSub    = "sess_subagent"
)

func notif(method string, params map[string]any) *marotte.RPCResponse {
	raw, err := json.Marshal(params)
	if err != nil {
		panic(err)
	}
	return &marotte.RPCResponse{Method: method, Params: raw}
}

func capturing(events *[]marotte.ServerEvent) *baseDeps {
	d := newBaseDeps()
	d.parent = testParent
	d.onBroadcast = func(_ context.Context, evt marotte.ServerEvent) {
		*events = append(*events, evt)
	}
	return d
}

func TestClassifyFrame(t *testing.T) {
	t.Parallel()
	var events []marotte.ServerEvent
	tr := New(rolesOf(capturing(&events)))
	tr.RecordStepSession(testStep, "wf_1", "s1", "s1")

	cases := []struct {
		name    string
		session string
		marked  bool
		want    FrameOwner
	}{
		{"no session id is the chat", "", false, OwnerChat},
		{"the chat's own session", testParent, false, OwnerChat},
		{"a registered step session", testStep, false, OwnerStep},
		// The recovery path: with a cold registry, the frame's own _meta.kiro.workflow classifies.
		{"an unregistered session the FRAME marks as a step", "sess_unknown", true, OwnerStep},
		{"an unregistered, unmarked session is a subagent", testSub, false, OwnerSubagent},
		// The session id is the discriminator; a marker cannot promote the parent.
		{"the marker does not override the chat's own session", testParent, true, OwnerChat},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := tr.ClassifyFrame(testChat, c.session, c.marked); got != c.want {
				t.Errorf("ClassifyFrame(%q, marked=%v) = %d, want %d", c.session, c.marked, got, c.want)
			}
		})
	}
}

// TestDeriveSubSession_StepIsNotASubagent pins that a step answers "", or its permission
// ask names a subagent that does not exist.
func TestDeriveSubSession_StepIsNotASubagent(t *testing.T) {
	t.Parallel()
	var events []marotte.ServerEvent
	tr := New(rolesOf(capturing(&events)))
	tr.RecordStepSession(testStep, "wf_1", "s1", "s1")

	if got := tr.deriveSubSession(testChat, testStep); got != "" {
		t.Errorf("deriveSubSession(step) = %q, want \"\" (a step is not a subagent)", got)
	}
	if got := tr.deriveSubSession(testChat, testSub); got != testSub {
		t.Errorf("deriveSubSession(subagent) = %q, want %q", got, testSub)
	}
	if got := tr.deriveSubSession(testChat, testParent); got != "" {
		t.Errorf("deriveSubSession(parent) = %q, want \"\"", got)
	}
}

// TestForeignSession_DropsBothNonChatOwners pins that the dedup guards drop a step's copy
// too: KAS fans one payload out to every live session.
func TestForeignSession_DropsBothNonChatOwners(t *testing.T) {
	t.Parallel()
	var events []marotte.ServerEvent
	tr := New(rolesOf(capturing(&events)))
	tr.RecordStepSession(testStep, "wf_1", "s1", "s1")

	for _, c := range []struct {
		name    string
		session string
		want    bool
	}{
		{"chat", testParent, false},
		{"unknown session id", "", false},
		{"step", testStep, true},
		{"subagent", testSub, true},
	} {
		if got := tr.foreignSession(testChat, c.session); got != c.want {
			t.Errorf("foreignSession(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestRunNotifications_NineBecomeThree pins the whole translation table: the two
// ends of a run are their own events and the seven kinds between them are one
// invalidation carrying the kind.
func TestRunNotifications_NineBecomeThree(t *testing.T) {
	t.Parallel()
	cases := []struct {
		method   string
		params   map[string]any
		wantType marotte.EventType
		wantKind marotte.RunProgressKind
	}{
		{"run_start", map[string]any{"workflowId": "wf_1", "workflowName": "publish"}, marotte.EventRunStarted, ""},
		{"run_complete", map[string]any{"workflowId": "wf_1", "status": "completed"}, marotte.EventRunFinished, ""},
		{"node_start", map[string]any{"workflowId": "wf_1", "nodeId": "a"}, marotte.EventRunProgress, marotte.RunProgressNodeStart},
		{"node_complete", map[string]any{"workflowId": "wf_1", "nodeId": "a"}, marotte.EventRunProgress, marotte.RunProgressNodeComplete},
		{"node_paused", map[string]any{"workflowId": "wf_1", "nodeId": "a"}, marotte.EventRunProgress, marotte.RunProgressNodePaused},
		{"paused", map[string]any{"workflowId": "wf_1"}, marotte.EventRunProgress, marotte.RunProgressPaused},
		{"watch_poll", map[string]any{"workflowId": "wf_1", "nodeId": "w"}, marotte.EventRunProgress, marotte.RunProgressWatchPoll},
		{"steps_queued", map[string]any{"workflowId": "wf_1"}, marotte.EventRunProgress, marotte.RunProgressStepsQueued},
		// loop_iteration names its node in `loopId`, not `nodeId`.
		{"loop_iteration", map[string]any{"workflowId": "wf_1", "loopId": "loop"}, marotte.EventRunProgress, marotte.RunProgressLoopIteration},
	}
	for _, c := range cases {
		t.Run(c.method, func(t *testing.T) {
			t.Parallel()
			var events []marotte.ServerEvent
			tr := New(rolesOf(capturing(&events)))
			msg := notif("_kiro/workflow/"+c.method, c.params)
			switch c.method {
			case "run_start":
				tr.HandleRunStart(t.Context(), testChat, msg)
			case "run_complete":
				tr.HandleRunComplete(t.Context(), testChat, msg)
			default:
				tr.RunProgressHandler(c.wantKind)(t.Context(), testChat, msg)
			}
			if len(events) != 1 {
				t.Fatalf("%s: got %d events, want 1", c.method, len(events))
			}
			if events[0].Type != c.wantType {
				t.Errorf("%s: event type = %q, want %q", c.method, events[0].Type, c.wantType)
			}
			// Every run event rides the LAUNCHING CHAT's topic: KAS parents a run on its session.
			if events[0].ChatID != testChat {
				t.Errorf("%s: chat_id = %q, want %q", c.method, events[0].ChatID, testChat)
			}
			if c.wantKind == "" {
				return
			}
			p, ok := events[0].Payload.(marotte.RunProgressPayload)
			if !ok {
				t.Fatalf("%s: payload type %T, want RunProgressPayload", c.method, events[0].Payload)
			}
			if p.Kind != c.wantKind {
				t.Errorf("%s: kind = %q, want %q", c.method, p.Kind, c.wantKind)
			}
			if c.method == "loop_iteration" && p.NodeID != "loop" {
				t.Errorf("loop_iteration: node_id = %q, want the loopId %q", p.NodeID, "loop")
			}
		})
	}
}

// TestRunStart_CarriesTheName pins that a client which has never fetched this run
// still has something to label the row with.
func TestRunStart_CarriesTheName(t *testing.T) {
	t.Parallel()
	var events []marotte.ServerEvent
	tr := New(rolesOf(capturing(&events)))
	tr.HandleRunStart(t.Context(), testChat,
		notif("_kiro/workflow/run_start", map[string]any{"workflowId": "wf_1", "workflowName": "publish-pr"}))
	p, ok := events[0].Payload.(marotte.RunStartedPayload)
	if !ok {
		t.Fatalf("payload type %T", events[0].Payload)
	}
	if p.WorkflowID != "wf_1" || p.Name != "publish-pr" {
		t.Errorf("payload = %+v, want {wf_1 publish-pr}", p)
	}
}

// TestRunStart_CarriesTheScheduledMark pins a flag only the launch path knows, keyed on
// the WORKFLOW id (chatID is "" for exactly these runs).
func TestRunStart_CarriesTheScheduledMark(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		scheduled map[string]bool
		wantFlag  bool
	}{
		{"a scheduled run is marked", map[string]bool{"wf_1": true}, true},
		{"a manual run is not", nil, false},
		// The mark belongs to one run, so another run's mark must not reach this one.
		{"another run's mark does not leak", map[string]bool{"wf_other": true}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var events []marotte.ServerEvent
			deps := capturing(&events)
			deps.scheduledRuns = c.scheduled
			tr := New(rolesOf(deps))

			// The real shape: dispatch passes "" for a parentless run's lifecycle frames.
			tr.HandleRunStart(t.Context(), "",
				notif("_kiro/workflow/run_start", map[string]any{"workflowId": "wf_1", "workflowName": "nightly"}))

			p, ok := events[0].Payload.(marotte.RunStartedPayload)
			if !ok {
				t.Fatalf("payload type %T, want RunStartedPayload", events[0].Payload)
			}
			if p.Scheduled != c.wantFlag {
				t.Errorf("scheduled = %v, want %v", p.Scheduled, c.wantFlag)
			}
			if p.WorkflowID != "wf_1" || p.Name != "nightly" {
				t.Errorf("payload = %+v, want the run's id and name intact", p)
			}
		})
	}
}

// TestRunComplete_CarriesTheRunsName pins the name read out of `finalState` (this frame has
// no top-level workflowName), for a client that never saw the start frame.
func TestRunComplete_CarriesTheRunsName(t *testing.T) {
	t.Parallel()
	var events []marotte.ServerEvent
	tr := New(rolesOf(capturing(&events)))

	tr.HandleRunComplete(t.Context(), "", notif("_kiro/workflow/run_complete", map[string]any{
		"workflowId": "wf_1",
		"status":     "completed",
		"finalState": map[string]any{"workflowName": "nightly-publish"},
	}))
	p, ok := events[0].Payload.(marotte.RunFinishedPayload)
	if !ok {
		t.Fatalf("payload type %T, want RunFinishedPayload", events[0].Payload)
	}
	if p.Name != "nightly-publish" {
		t.Errorf("name = %q, want the run's name from finalState", p.Name)
	}
	if p.Status != "completed" || p.WorkflowID != "wf_1" {
		t.Errorf("payload = %+v, want the id and status intact", p)
	}

	// A frame with no state carries no name; the client falls back to a generic label.
	events = nil
	tr.HandleRunComplete(t.Context(), "", notif("_kiro/workflow/run_complete", map[string]any{
		"workflowId": "wf_2", "status": "failed",
	}))
	if p, _ := events[0].Payload.(marotte.RunFinishedPayload); p.Name != "" {
		t.Errorf("name = %q for a frame with no finalState, want empty", p.Name)
	}
}

func TestRunLifecycle_NamesALabelledRunByItsLabel(t *testing.T) {
	t.Parallel()
	var events []marotte.ServerEvent
	deps := capturing(&events)
	deps.runLabels = map[string]string{"wf_1": "publish · scheduled"}
	tr := New(rolesOf(deps))

	tr.HandleRunStart(t.Context(), "",
		notif("_kiro/workflow/run_start", map[string]any{"workflowId": "wf_1", "workflowName": "publish"}))
	if p, _ := events[0].Payload.(marotte.RunStartedPayload); p.Name != "publish · scheduled" {
		t.Errorf("run_started name = %q, want the launch's label", p.Name)
	}
	tr.HandleRunComplete(t.Context(), "", notif("_kiro/workflow/run_complete", map[string]any{
		"workflowId": "wf_2", "status": "completed",
		"finalState": map[string]any{"workflowName": "publish", "runLabel": "publish-docs"},
	}))
	if p, _ := events[1].Payload.(marotte.RunFinishedPayload); p.Name != "publish-docs" {
		t.Errorf("run_finished name = %q, want finalState.runLabel", p.Name)
	}
}

// TestRunNotifications_IgnoreFramesWithNoWorkflowID pins that a malformed frame
// emits nothing rather than an event naming the empty run.
func TestRunNotifications_IgnoreFramesWithNoWorkflowID(t *testing.T) {
	t.Parallel()
	var events []marotte.ServerEvent
	tr := New(rolesOf(capturing(&events)))
	ctx := t.Context()
	tr.HandleRunStart(ctx, testChat, notif("_kiro/workflow/run_start", map[string]any{}))
	tr.HandleRunComplete(ctx, testChat, notif("_kiro/workflow/run_complete", map[string]any{}))
	tr.RunProgressHandler(marotte.RunProgressNodeStart)(ctx, testChat,
		notif("_kiro/workflow/node_start", map[string]any{"nodeId": "a"}))
	if len(events) != 0 {
		t.Errorf("got %d events for frames with no workflow id, want 0", len(events))
	}
}

// TestNodeStart_RecordsTheStepSession pins that node_start, the ONLY frame announcing a
// step's session id, records it.
func TestNodeStart_RecordsTheStepSession(t *testing.T) {
	t.Parallel()
	var events []marotte.ServerEvent
	tr := New(rolesOf(capturing(&events)))

	if _, ok := tr.steps.lookup("sess_new"); ok {
		t.Fatal("registry is not empty before node_start")
	}
	tr.RunProgressHandler(marotte.RunProgressNodeStart)(t.Context(), testChat,
		notif("_kiro/workflow/node_start", map[string]any{
			"workflowId": "wf_1", "nodeId": "build", "sessionId": "sess_new",
		}))
	ref, ok := tr.steps.lookup("sess_new")
	if !ok {
		t.Fatal("node_start did not record the step session")
	}
	if ref.WorkflowID != "wf_1" || ref.NodeID != "build" {
		t.Errorf("StepOf = %+v, want {wf_1 build}", ref)
	}
	// A node_start without a sessionId (resume) records nothing rather than a "" key.
	tr.RunProgressHandler(marotte.RunProgressNodeStart)(t.Context(), testChat,
		notif("_kiro/workflow/node_start", map[string]any{"workflowId": "wf_1", "nodeId": "next"}))
	if _, ok := tr.steps.lookup(""); ok {
		t.Error("a node_start with no sessionId recorded an empty-keyed entry")
	}
}

func TestNodeStart_SlashBearingNodeIDsKeyDistinctTurns(t *testing.T) {
	t.Parallel()
	var events []marotte.ServerEvent
	d := capturing(&events)
	tr := New(rolesOf(d))

	for _, f := range []struct {
		session string
		path    []string
	}{
		{session: "sess_ab_c", path: []string{"wf_1", "a/b", "c"}},
		{session: "sess_a_bc", path: []string{"wf_1", "a", "b/c"}},
	} {
		tr.RunProgressHandler(marotte.RunProgressNodeStart)(t.Context(), testChat,
			notif("_kiro/workflow/node_start", map[string]any{
				"workflowId": "wf_1", "nodeId": f.path[len(f.path)-1], "sessionId": f.session, "nodePath": f.path,
			}))
	}

	var opened []string
	for _, c := range d.runCalls {
		if c.kind == "node_start" {
			opened = append(opened, c.nodePath)
		}
	}
	if want := []string{`wf_1:a/b:c`, `wf_1:a:b/c`}; !slices.Equal(opened, want) {
		t.Errorf("node_start turn keys = %q, want %q", opened, want)
	}
	if ab, abc := tr.steps.refFor("sess_ab_c").NodePath, tr.steps.refFor("sess_a_bc").NodePath; ab == abc {
		t.Errorf("both step sessions recorded node path %q, want two", ab)
	}
}

// TestStepAttribution_SlashBearingNodeIDsKeyDistinctTurns pins the content frame's own meta to the same key.
func TestStepAttribution_SlashBearingNodeIDsKeyDistinctTurns(t *testing.T) {
	t.Parallel()
	var events []marotte.ServerEvent
	tr := New(rolesOf(capturing(&events)))

	ab := tr.StepAttribution("wf_1", "sess_ab_c",
		&ACPWorkflowMeta{WorkflowID: "wf_1", NodeID: "c", NodePath: []string{"wf_1", "a/b", "c"}})
	abc := tr.StepAttribution("wf_1", "sess_a_bc",
		&ACPWorkflowMeta{WorkflowID: "wf_1", NodeID: "b/c", NodePath: []string{"wf_1", "a", "b/c"}})
	if ab.NodePath != `wf_1:a/b:c` || abc.NodePath != `wf_1:a:b/c` {
		t.Errorf("StepAttribution node paths = %q and %q, want wf_1:a/b:c and wf_1:a:b/c", ab.NodePath, abc.NodePath)
	}
}

// TestRunStep_CarriesTheNodeIDBesideItsKey pins the id both turn openers hand the run log: a hashed
// path key cannot be split back into it.
func TestRunStep_CarriesTheNodeIDBesideItsKey(t *testing.T) {
	t.Parallel()
	var events []marotte.ServerEvent
	d := capturing(&events)
	tr := New(rolesOf(d))
	ctx := t.Context()

	tr.RunProgressHandler(marotte.RunProgressNodeStart)(ctx, testChat,
		notif("_kiro/workflow/node_start", map[string]any{
			"workflowId": "wf_1", "nodeId": "a/b", "sessionId": "sess_ab", "nodePath": []string{"wf_1", "a/b"},
		}))
	raw, err := json.Marshal(map[string]any{"content": map[string]any{"type": "text", "text": "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	tr.HandleAssistantChunk(ctx, testChat, raw, false, tr.StepAttribution("wf_1", "sess_ab", nil))

	got := map[string][]string{}
	for _, c := range d.runCalls {
		got[c.kind] = append(got[c.kind], c.nodeID)
	}
	if want := []string{"a/b"}; !slices.Equal(got["node_start"], want) || !slices.Equal(got["fold"], want) {
		t.Errorf("node ids: node_start %q, content fold %q; want %q for each", got["node_start"], got["fold"], want)
	}
}

// A cold registry (after a restart) knows no step; a content frame's own workflow _meta names its node.
func TestRunStep_TakesTheMetasNodeIDWithAColdRegistry(t *testing.T) {
	t.Parallel()
	var events []marotte.ServerEvent
	d := capturing(&events)
	tr := New(rolesOf(d))
	raw, err := json.Marshal(map[string]any{"content": map[string]any{"type": "text", "text": "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	wf := &ACPWorkflowMeta{WorkflowID: "wf_1", NodeID: "a/b", NodePath: []string{"wf_1", "a/b"}}

	tr.HandleAssistantChunk(t.Context(), testChat, raw, false, tr.StepAttribution("wf_1", "sess_cold", wf))

	var folds []string
	for _, c := range d.runCalls {
		if c.kind == "fold" {
			folds = append(folds, c.nodeID)
		}
	}
	if want := []string{"a/b"}; !slices.Equal(folds, want) {
		t.Errorf("content fold node ids = %q, want %q from the frame's meta", folds, want)
	}
}

// TestRunComplete_LeavesTheStepSessionsToItsCaller pins that this handler forgets nothing:
// the status can be `paused`, and the terminal gate is agent.observeComplete's.
func TestRunComplete_LeavesTheStepSessionsToItsCaller(t *testing.T) {
	t.Parallel()
	var events []marotte.ServerEvent
	tr := New(rolesOf(capturing(&events)))
	tr.RecordStepSession("sess_a", "wf_1", "a", "a")
	tr.RecordStepSession("sess_b", "wf_1", "b", "b")

	tr.HandleRunComplete(t.Context(), testChat,
		notif("_kiro/workflow/run_complete", map[string]any{"workflowId": "wf_1", "status": "completed"}))

	for _, id := range []string{"sess_a", "sess_b"} {
		if _, ok := tr.steps.lookup(id); !ok {
			t.Errorf("%s was forgotten by the frame rather than by the gated caller", id)
		}
	}
	if len(events) != 1 || events[0].Type != marotte.EventRunFinished {
		t.Errorf("events = %+v, want one run_finished", events)
	}
}

// TestForgetRunSteps_DropsOneRunsSessions pins the registry bound, scoped to the ended run.
func TestForgetRunSteps_DropsOneRunsSessions(t *testing.T) {
	t.Parallel()
	var events []marotte.ServerEvent
	tr := New(rolesOf(capturing(&events)))
	tr.RecordStepSession("sess_a", "wf_1", "a", "a")
	tr.RecordStepSession("sess_b", "wf_1", "b", "b")
	tr.RecordStepSession("sess_c", "wf_2", "c", "c")

	tr.ForgetRunSteps("wf_1")

	for _, id := range []string{"sess_a", "sess_b"} {
		if _, ok := tr.steps.lookup(id); ok {
			t.Errorf("%s survived its run's end", id)
		}
	}
	if _, ok := tr.steps.lookup("sess_c"); !ok {
		t.Error("another run's step session was forgotten too")
	}
}

func TestRecordStepSession_IgnoresIncompleteRefs(t *testing.T) {
	t.Parallel()
	var events []marotte.ServerEvent
	tr := New(rolesOf(capturing(&events)))
	tr.RecordStepSession("", "wf_1", "a", "a")
	tr.RecordStepSession("sess_x", "", "a", "a")
	if _, ok := tr.steps.lookup("sess_x"); ok {
		t.Error("recorded a step with no workflow id")
	}
}

func TestStepOf_EmptySessionIsNeverAStep(t *testing.T) {
	t.Parallel()
	var events []marotte.ServerEvent
	tr := New(rolesOf(capturing(&events)))
	if _, ok := tr.steps.lookup(""); ok {
		t.Error("StepOf(\"\") reported a step")
	}
}

// TestStepChunk_TwoIterationsDoNotShareATurn pins nodePath as the key: a repeat's
// iterations reuse the node id.
func TestStepChunk_TwoIterationsDoNotShareATurn(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	ctx := t.Context()

	for _, iter := range []string{"iter-0", "iter-1"} {
		raw, err := json.Marshal(map[string]any{
			"content": map[string]any{"type": "text", "text": "ran " + iter},
		})
		if err != nil {
			t.Fatal(err)
		}
		tr.HandleAssistantChunk(ctx, testChat, raw, false,
			FrameAttribution{Step: true, RunID: "wf_1", NodePath: "wf_1/loop/" + iter + "/step"})
	}
	if len(deps.turns.runs) != 2 {
		t.Fatalf("got %d run turns, want one per iteration", len(deps.turns.runs))
	}
	for _, iter := range []string{"iter-0", "iter-1"} {
		turn := deps.turns.runs[runPathKey("wf_1", "wf_1/loop/"+iter+"/step")]
		if turn == nil {
			t.Fatalf("no run turn for %s", iter)
		}
		open := turn.OpenEntries()
		if len(open) != 1 || open[0].Text != "ran "+iter {
			t.Errorf("%s: open entries = %+v, want the one text 'ran %s'", iter, open, iter)
		}
	}
	if deps.turns.chats[testChat] != nil {
		t.Error("a step's frames opened the launching chat's own turn; want the run's alone")
	}
}

// usageStore captures the chat-usage write persistTurnSummary makes.
type usageStore struct {
	recStore
	chat marotte.Chat
}

func (s *usageStore) Mutate(_ context.Context, _ marotte.ChatID, fn func(*marotte.Chat, bool) bool) (string, error) {
	s.mutateCalls++
	fn(&s.chat, true)
	return strconv.Itoa(s.mutateCalls), nil
}

// TestSessionInfoUpdate_StepMeteringCountsCreditsOnly pins the scoped allowance: a step's
// turn_completion feeds the RUN turn and bills credits to the launching chat, nothing else
// (LastTurnMs is the conversation's own last turn).
func TestSessionInfoUpdate_StepMeteringCountsCreditsOnly(t *testing.T) {
	t.Parallel()
	// No `workflow` block: KAS merges no promptMeta here, so a step's turn_completion is
	// byte-identical to the chat's and the step fact arrives only as ATTRIBUTION.
	infoFrame := func() json.RawMessage {
		kiro := map[string]any{
			"kind":                "turn_completion",
			"promptTurnSummaries": []map[string]any{{"unit": "credit", "usage": 0.25}},
			"elapsedTime":         1234.0,
		}
		raw, err := json.Marshal(map[string]any{"_meta": map[string]any{"kiro": kiro}})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	const runID, nodePath = "wf_1", "wf_1/build"

	for _, c := range []struct {
		name         string
		attr         FrameAttribution
		wantLastMs   float64
		wantRunMeter bool
	}{
		{"the chat's own turn moves both", FrameAttribution{}, 1234, false},
		{"a step's turn meters the run turn and bills the chat's credits only", FrameAttribution{Step: true, RunID: runID, NodePath: nodePath}, 0, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			store := &usageStore{}
			deps := newBaseDeps()
			deps.store = store
			deps.turns.runTurn(runID, nodePath)
			tr := New(rolesOf(deps))
			tr.HandleSessionInfoUpdate(t.Context(), testChat, infoFrame(), c.attr)

			if store.chat.Usage.Credits != 0.25 {
				t.Errorf("credits = %v, want 0.25 (real spend is the user's either way)", store.chat.Usage.Credits)
			}
			if !store.chat.Usage.HasRealData {
				t.Error("HasRealData not set, so the popup would keep showing placeholder zeros")
			}
			if store.chat.Usage.LastTurnMs != c.wantLastMs {
				t.Errorf("LastTurnMs = %v, want %v", store.chat.Usage.LastTurnMs, c.wantLastMs)
			}
			var metered []runCall
			for _, rc := range deps.runCalls {
				if rc.kind == "meter" {
					metered = append(metered, rc)
				}
			}
			if !c.wantRunMeter {
				if len(metered) != 0 {
					t.Errorf("the chat's own turn metered a run turn: %+v", metered)
				}
				return
			}
			if len(metered) != 1 || metered[0].runID != runID || metered[0].nodePath != nodePath ||
				metered[0].credits != 0.25 || metered[0].elapsedMs != 1234 {
				t.Errorf("run meter calls = %+v, want one for %s/%s with 0.25 credits over 1234 ms", metered, runID, nodePath)
			}
		})
	}
}

// TestSessionInfoUpdate_StepFramesWithoutMeteringStayDropped pins that a step's focus,
// compaction and context-usage frames never reach the chat.
func TestSessionInfoUpdate_StepFramesWithoutMeteringStayDropped(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(map[string]any{"_meta": map[string]any{"kiro": map[string]any{
		"kind":            "context_usage",
		"usagePercentage": 42.0,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	store := &usageStore{}
	deps := newBaseDeps()
	deps.store = store
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), testChat, raw, FrameAttribution{Step: true})
	if store.mutateCalls != 0 {
		t.Errorf("a step's context_usage reached the chat (%d mutations), want 0", store.mutateCalls)
	}
}

// TestRecordRunSteps_SeedsFromAnInspectRead pins the recovery path: after a restart an
// `inspect` read makes the frame classify as a step rather than a subagent.
func TestRecordRunSteps_SeedsFromAnInspectRead(t *testing.T) {
	t.Parallel()
	var events []marotte.ServerEvent
	tr := New(rolesOf(capturing(&events)))

	if got := tr.ClassifyFrame(testChat, "sess_build", false); got != OwnerSubagent {
		t.Fatalf("before the read, sess_build classified %d, want OwnerSubagent", got)
	}
	tr.RecordRunSteps(json.RawMessage(`{
	  "workflowId": "wf_1",
	  "state": {"workflowId": "wf_1", "root": {"nodeId": "wf_1", "type": "sequence", "children": [
	    {"nodeId": "build", "type": "step", "sessionId": "sess_build"},
	    {"nodeId": "test", "type": "step", "sessionId": "sess_test"},
	    {"nodeId": "later", "type": "step"}
	  ]}}
	}`))

	for _, id := range []string{"sess_build", "sess_test"} {
		if got := tr.ClassifyFrame(testChat, id, false); got != OwnerStep {
			t.Errorf("after the read, %s classified %d, want OwnerStep", id, got)
		}
	}
	ref, ok := tr.steps.lookup("sess_build")
	if !ok || ref.WorkflowID != "wf_1" || ref.NodeID != "build" {
		t.Errorf("lookup(sess_build) = %+v ok=%v, want {wf_1 build} true", ref, ok)
	}
	// An empty key would make every unattributed frame on this chat look like a step.
	if _, ok := tr.steps.lookup(""); ok {
		t.Error("a pending step seeded an empty-keyed entry")
	}
}

func TestRecordRunSteps_SlashBearingNodeIDsStayDistinct(t *testing.T) {
	t.Parallel()
	var events []marotte.ServerEvent
	tr := New(rolesOf(capturing(&events)))

	tr.RecordRunSteps(json.RawMessage(`{"state": {"workflowId": "wf_1", "root": {"nodeId": "wf_1", "type": "sequence", "children": [
	  {"nodeId": "a/b", "type": "sequence", "children": [{"nodeId": "c", "type": "step", "sessionId": "sess_ab_c"}]},
	  {"nodeId": "a", "type": "sequence", "children": [{"nodeId": "b/c", "type": "step", "sessionId": "sess_a_bc"}]}
	]}}}`))

	if got := tr.steps.refFor("sess_ab_c").NodePath; got != `wf_1:a/b:c` {
		t.Errorf("refFor(sess_ab_c).NodePath = %q, want %q", got, `wf_1:a/b:c`)
	}
	if got := tr.steps.refFor("sess_a_bc").NodePath; got != `wf_1:a:b/c` {
		t.Errorf("refFor(sess_a_bc).NodePath = %q, want %q", got, `wf_1:a:b/c`)
	}
}

// TestRecordRunSteps_ToleratesJunk pins that seeding cannot fail a read.
func TestRecordRunSteps_ToleratesJunk(t *testing.T) {
	t.Parallel()
	var events []marotte.ServerEvent
	tr := New(rolesOf(capturing(&events)))
	for _, raw := range []string{`{`, `null`, `[]`, `{"state":null}`, `{"state":{"root":null}}`, `"a string"`, ``} {
		tr.RecordRunSteps(json.RawMessage(raw))
	}
}

// TestStepToolCall_FoldsIntoTheStepsTurn pins that a step's TOOL and TEXT frames route by
// the same attribution, or one step's work fragments across two turns.
func TestStepToolCall_FoldsIntoTheStepsTurn(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	ctx := t.Context()
	attr := FrameAttribution{Step: true, RunID: "wf_1", NodePath: "wf_1/build"}

	text, err := json.Marshal(map[string]any{
		"content": map[string]any{"type": "text", "text": "building"},
	})
	if err != nil {
		t.Fatal(err)
	}
	tr.HandleAssistantChunk(ctx, testChat, text, false, attr)

	tool, err := json.Marshal(map[string]any{
		"toolCallId": "tc-1", "title": "write file", "kind": "edit", "status": "pending",
		"_meta": map[string]any{"kiro": map[string]any{
			"agentSubtaskId": "kas-own-subtask-uuid",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tr.HandleToolCall(ctx, testChat, tool, attr)

	if len(deps.turns.runs) != 1 {
		t.Fatalf("got %d run turns, want 1 (one step, one turn)", len(deps.turns.runs))
	}
	turn := deps.turns.runs[runPathKey("wf_1", "wf_1/build")]
	if turn == nil {
		t.Fatal("no run turn for wf_1/build")
	}
	if open := turn.OpenEntries(); len(open) != 1 || open[0].Text != "building" {
		t.Errorf("open entries = %+v, want the step's text still coalescing", open)
	}
	calls := toolCallsOf(t, deps.runEntries("wf_1", "wf_1/build"))
	if len(calls) != 1 || calls[0].ID != "tc-1" {
		t.Fatalf("run turn tool_call entries = %+v, want tc-1", calls)
	}
	if calls[0].AgentSubtaskID != "kas-own-subtask-uuid" {
		t.Errorf("tool call subtask = %q, want KAS's own (the create fixes the lane)", calls[0].AgentSubtaskID)
	}
	if deps.turns.chats[testChat] != nil {
		t.Error("a step's tool call opened the launching chat's own turn; want the run's alone")
	}
}

// TestAgentLaunchedRun_IsRecorded pins both directions of the two slog lines that are an
// agent-launched run's only durable trace. Serial: slog's default is process-global.
func TestAgentLaunchedRun_IsRecorded(t *testing.T) {
	const (
		startMsg = "agent-launched workflow run started"
		endMsg   = "agent-launched workflow run finished"
	)
	// The gate is the DELIVERY ADDRESS: a chat bridge stamps the chat's id, a run bridge's
	// lifecycle frames get an empty one.
	cases := []struct {
		name       string
		chatID     marotte.ChatID
		wantLogged bool
	}{
		{"a run launched from inside a chat is recorded", testChat, true},
		{"a run marotte launched is not", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := capture.Default(t)
			var events []marotte.ServerEvent
			tr := New(rolesOf(capturing(&events)))
			ctx := t.Context()

			// Both cases carry a parentSessionId, so only the address can separate them.
			start := map[string]any{
				"workflowId": "wf_7", "workflowName": "publish-pr", "parentSessionId": testParent,
			}
			done := map[string]any{
				"workflowId": "wf_7",
				"status":     "completed",
				"finalState": map[string]any{
					"workflowName": "publish-pr", "parentSessionId": testParent,
				},
			}
			tr.HandleRunStart(ctx, c.chatID, notif("_kiro/workflow/run_start", start))
			tr.HandleRunComplete(ctx, c.chatID, notif("_kiro/workflow/run_complete", done))

			// Only the log line is gated, so the gate cannot become "reads run_start" by accident.
			if len(events) != 2 {
				t.Fatalf("got %d SSE events, want 2 (started + finished) regardless of origin", len(events))
			}
			if !c.wantLogged {
				if n := rec.Count("agent-launched"); n != 0 {
					t.Errorf("a run marotte launched produced %d agent-origin log line(s), want 0", n)
				}
				return
			}
			for _, msg := range []string{startMsg, endMsg} {
				if rec.CountExact(msg) != 1 {
					t.Errorf("got %d %q lines, want 1", rec.CountExact(msg), msg)
				}
				for key, want := range map[string]string{
					"workflow_id": "wf_7",
					"origin":      "agent",
					"recipe":      "publish-pr",
				} {
					if !rec.HasAttr(msg, key, want) {
						got, _ := rec.AttrValue(msg, key)
						t.Errorf("%q: %s = %q, want %q", msg, key, got, want)
					}
				}
			}
			// Terminal covers success, failure, cancel and a policy stop, so the line carries the status.
			if !rec.HasAttr(endMsg, "status", "completed") {
				got, _ := rec.AttrValue(endMsg, "status")
				t.Errorf("%q: status = %q, want %q", endMsg, got, "completed")
			}
		})
	}
}

// TestAgentLaunchedRun_IgnoresTheParentSessionField pins that the origin gate ignores
// `parentSessionId`: `_kiro/workflow/new` requires one, so every marotte launch carries it.
// Serial: slog's default is process-global.
func TestAgentLaunchedRun_IgnoresTheParentSessionField(t *testing.T) {
	rec := capture.Default(t)
	var events []marotte.ServerEvent
	tr := New(rolesOf(capturing(&events)))
	ctx := t.Context()

	// The shape a marotte launch produces: a parent in both positions, on an empty chat id.
	tr.HandleRunStart(ctx, "", notif("_kiro/workflow/run_start", map[string]any{
		"workflowId": "wf_9", "workflowName": "nightly", "parentSessionId": testParent,
	}))
	tr.HandleRunComplete(ctx, "", notif("_kiro/workflow/run_complete", map[string]any{
		"workflowId":      "wf_9",
		"status":          "completed",
		"parentSessionId": testParent,
		"finalState": map[string]any{
			"workflowName": "nightly", "parentSessionId": testParent,
		},
	}))

	if n := rec.Count("agent-launched"); n != 0 {
		t.Errorf("a run marotte launched produced %d agent-origin log line(s), want 0; "+
			"the gate is reading parentSessionId, which every run now carries", n)
	}
	// The SSE events are what the client needs and they are never gated.
	if len(events) != 2 {
		t.Fatalf("got %d SSE events, want 2 (started + finished)", len(events))
	}
}
