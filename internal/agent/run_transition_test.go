package agent

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/token"
	"maps"
	"slices"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/signin"
	"github.com/cplieger/marotte/internal/workflow"
)

// transitionState is one state a transition's read of the run can find, as KAS answers inspect.
type transitionState struct {
	set  func(t *testing.T, br *fakeBridge)
	name string
	seen runSeen
}

// askPausedInspect is a paused run whose one step KAS parked waiting on a person, for reason.
func askPausedInspect(t *testing.T, reason string) json.RawMessage {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(inspectAsk(t, gateRunID, marotte.RunStatusPaused, parkedStep("ask", "sess_step")), &doc); err != nil {
		t.Fatalf("Setup: decoding the inspect reply: %v", err)
	}
	doc["state"].(map[string]any)["pauseReason"] = reason
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("Setup: marshalling the inspect reply: %v", err)
	}
	return raw
}

// withAttentionDrift is raw with every step's attention a string, a type workflow.DecodeInspect refuses.
func withAttentionDrift(t *testing.T, raw json.RawMessage) json.RawMessage {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("Setup: decoding the inspect reply: %v", err)
	}
	root := doc["state"].(map[string]any)["root"].(map[string]any)
	for _, kid := range root["children"].([]any) {
		kid.(map[string]any)["attention"] = "yes"
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("Setup: marshalling the inspect reply: %v", err)
	}
	return out
}

// askPath is the path of the step askPausedInspect and withPausedStep put under the root.
var askPath = workflow.PathKey([]string{"root", "ask"})

// withPausedStep is raw with a paused step at askPath that waits on nobody, unchanged when it is no reply or has a
// tree, so the run's classification stays the reply's own.
func withPausedStep(t *testing.T, raw json.RawMessage) json.RawMessage {
	t.Helper()
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return raw
	}
	st, ok := doc["state"].(map[string]any)
	if !ok || st["root"] != nil {
		return raw
	}
	st["root"] = map[string]any{"nodeId": "root", "type": "sequence", "status": st["status"], "children": []any{
		map[string]any{"nodeId": "ask", "type": stepNodeType, "status": "paused", "sessionId": "sess_step"},
	}}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("Setup: marshalling the inspect reply: %v", err)
	}
	return out
}

func inspectAs(raw func(t *testing.T) json.RawMessage) func(*testing.T, *fakeBridge) {
	return func(t *testing.T, br *fakeBridge) { br.setCallResult(methodKiroWorkflowInspect, raw(t)) }
}

var transitionStates = []transitionState{
	{name: "a failed read", seen: seenUnreadable, set: func(_ *testing.T, br *fakeBridge) {
		br.setCallRPCErr(methodKiroWorkflowInspect, &marotte.RPCError{Code: -32603, Message: "Internal error"})
	}},
	{name: "an undecodable reply", seen: seenUnreadable, set: func(_ *testing.T, br *fakeBridge) {
		br.setCallResult(methodKiroWorkflowInspect, json.RawMessage(`{"state":`))
	}},
	{name: "another run's reply", seen: seenUnreadable, set: inspectAs(func(t *testing.T) json.RawMessage {
		return inspectReply(t, "wf_other", marotte.RunStatusPaused, stalePauseReason)
	})},
	{name: "a run KAS no longer knows", seen: seenGone, set: func(_ *testing.T, br *fakeBridge) {
		br.setCallRPCErr(methodKiroWorkflowInspect, workflowNotFoundErr())
	}},
	{name: "running", seen: seenRunning, set: inspectAs(func(t *testing.T) json.RawMessage {
		return inspectReply(t, gateRunID, marotte.RunStatusRunning, "")
	})},
	{name: "running with a pause pending", seen: seenPausing, set: inspectAs(pendingInspect)},
	{name: "the gate's pause", seen: seenGatePaused, set: inspectAs(func(t *testing.T) json.RawMessage {
		return heldInspect(t, signInHoldReason)
	})},
	{name: "a person's pause", seen: seenPersonPaused, set: inspectAs(func(t *testing.T) json.RawMessage {
		return heldInspect(t, "")
	})},
	{name: "marotte's park for the user", seen: seenPersonPaused, set: inspectAs(func(t *testing.T) json.RawMessage {
		return heldInspect(t, manualResumeReason)
	})},
	{name: "a restart's pause", seen: seenRestartPaused, set: inspectAs(func(t *testing.T) json.RawMessage {
		return inspectPaused(t, gateRunID, stalePauseReason)
	})},
	{name: "an interruption's pause", seen: seenInvoluntary, set: inspectAs(func(t *testing.T) json.RawMessage {
		return inspectPaused(t, gateRunID, interruptedReason)
	})},
	{name: "a step's ask", seen: seenAskPaused, set: inspectAs(func(t *testing.T) json.RawMessage {
		return askPausedInspect(t, "Step requested user input via send_message.")
	})},
	{name: "a step's ask with an interruption's reason", seen: seenAskPaused, set: inspectAs(func(t *testing.T) json.RawMessage {
		return askPausedInspect(t, interruptedReason)
	})},
	{name: "a step's ask whose tree does not decode", seen: seenUnreadable, set: inspectAs(func(t *testing.T) json.RawMessage {
		return withAttentionDrift(t, askPausedInspect(t, "Step requested user input via send_message."))
	})},
	{name: "another pause", seen: seenOtherPause, set: inspectAs(func(t *testing.T) json.RawMessage {
		return inspectPaused(t, gateRunID, branchWaitReason)
	})},
	{name: "ended", seen: seenEnded, set: inspectAs(func(t *testing.T) json.RawMessage {
		return inspectReply(t, gateRunID, marotte.RunStatusCancelled, "")
	})},
}

// transitionAsk is one request, through the entry point its actor calls, with what it writes and answers by state.
type transitionAsk struct {
	do func(t *testing.T, h *Runtime) string
	// prep, when set, edits the state's scripted replies before the request.
	prep       func(t *testing.T, br *fakeBridge)
	want       map[runSeen]transitionWant
	name       string
	gateClosed bool
}

type transitionWant struct {
	wrote  string
	answer string
}

// personAnswer names a person's answer by its HTTP status or reason.
func personAnswer(err error) string {
	if err == nil {
		return "done"
	}
	code, reason, ok := command.StatusOf(err)
	switch {
	case !ok:
		return "error: " + err.Error()
	case reason != "":
		return reason
	}
	return strconv.Itoa(code)
}

func healAnswer(o resumeOutcome) string {
	return map[resumeOutcome]string{
		resumeWritten: "written", resumeSkipped: "skipped", resumeHeld: "held", resumeFailed: "failed",
	}[o]
}

var (
	pauseWrote  = transitionWant{wrote: methodKiroWorkflowPause, answer: "done"}
	resumeWrote = transitionWant{wrote: methodKiroWorkflowResume, answer: "done"}
	// personRefusals are a person's answers for the states no person's request changes.
	personRefusals = map[runSeen]transitionWant{
		seenUnreadable: {answer: "503"}, seenGone: {answer: "404"}, seenEnded: {answer: "409"},
	}
	personResumes = withRows(personRefusals, map[runSeen]transitionWant{
		seenRunning: {answer: "already_running"}, seenPausing: {answer: "already_running"},
		seenGatePaused: resumeWrote, seenPersonPaused: resumeWrote, seenRestartPaused: resumeWrote,
		seenInvoluntary: resumeWrote, seenOtherPause: resumeWrote, seenAskPaused: {answer: "awaiting_answer"},
	})
	finishWrote = transitionWant{wrote: methodKiroWorkflowUpdate, answer: "done"}
)

func withRows(base, rows map[runSeen]transitionWant) map[runSeen]transitionWant {
	out := make(map[runSeen]transitionWant, len(base)+len(rows))
	maps.Copy(out, base)
	maps.Copy(out, rows)
	return out
}

// everyState is want for every state but the rows given.
func everyState(want transitionWant, rows map[runSeen]transitionWant) map[runSeen]transitionWant {
	out := map[runSeen]transitionWant{}
	for s := seenUnreadable; s <= seenEnded; s++ {
		out[s] = want
	}
	return withRows(out, rows)
}

var transitionAsks = []transitionAsk{
	{
		name: "a person's pause",
		do: func(t *testing.T, h *Runtime) string {
			return personAnswer(h.runs.pause(t.Context(), gateRunID, callerPage))
		},
		want: withRows(personRefusals, map[runSeen]transitionWant{
			seenRunning: pauseWrote, seenPausing: pauseWrote, seenGatePaused: pauseWrote,
			seenRestartPaused: pauseWrote, seenInvoluntary: pauseWrote,
			seenPersonPaused: {answer: "already_paused"}, seenOtherPause: {answer: "already_paused"},
			seenAskPaused: {answer: "already_paused"},
		}),
	},
	{
		name: "a person's resume",
		do: func(t *testing.T, h *Runtime) string {
			return personAnswer(h.runs.resume(t.Context(), gateRunID, callerPage))
		},
		want: personResumes,
	},
	{
		name: "a person's repeat extension",
		do: func(t *testing.T, h *Runtime) string {
			return personAnswer(h.runs.extendRepeat(t.Context(), gateRunID, "loop", 2, callerPage))
		},
		want: personResumes,
	},
	{
		name: "a person's repeat finish",
		do: func(t *testing.T, h *Runtime) string {
			return personAnswer(h.runs.finishRepeat(t.Context(), gateRunID, "loop"))
		},
		want: withRows(personRefusals, map[runSeen]transitionWant{
			seenRunning: {answer: "already_running"}, seenPausing: {answer: "already_running"},
			seenGatePaused: finishWrote, seenPersonPaused: finishWrote, seenRestartPaused: finishWrote,
			seenInvoluntary: finishWrote, seenOtherPause: finishWrote, seenAskPaused: finishWrote,
		}),
	},
	{
		name: "a person's end of a paused step",
		prep: func(t *testing.T, br *fakeBridge) {
			br.mu.Lock()
			defer br.mu.Unlock()
			br.callResults[methodKiroWorkflowInspect] = withPausedStep(t, br.callResults[methodKiroWorkflowInspect])
		},
		do: func(t *testing.T, h *Runtime) string {
			return personAnswer(h.runs.endStep(t.Context(), gateRunID, askPath, runStepCompleted))
		},
		want: everyState(finishWrote, personRefusals),
	},
	{
		name: "the gate's pause", gateClosed: true,
		do: func(t *testing.T, h *Runtime) string {
			if err := h.runs.pauseHeld(t.Context(), gateRunID); err != nil {
				return "retry"
			}
			return "done"
		},
		want: everyState(transitionWant{answer: "done"}, map[runSeen]transitionWant{
			seenUnreadable: {answer: "retry"}, seenRunning: pauseWrote,
		}),
	},
	{
		name: "the gate's finder", gateClosed: true,
		do: func(t *testing.T, h *Runtime) string {
			if _, err := h.runs.findPass(t.Context(), []string{gateRunID}); err != nil {
				return "retry"
			}
			return "done"
		},
		want: everyState(transitionWant{answer: "done"}, map[runSeen]transitionWant{seenUnreadable: {answer: "retry"}}),
	},
	{
		name: "an automatic resume",
		do: func(t *testing.T, h *Runtime) string {
			return healAnswer(h.runs.resumeIfInterrupted(t.Context(), parkChat, gateRunID))
		},
		want: everyState(transitionWant{answer: "skipped"}, map[runSeen]transitionWant{
			seenUnreadable:    {answer: "failed"},
			seenGatePaused:    {wrote: methodKiroWorkflowResume, answer: "written"},
			seenRestartPaused: {wrote: methodKiroWorkflowResume, answer: "written"},
			seenInvoluntary:   {wrote: methodKiroWorkflowResume, answer: "written"},
		}),
	},
	{
		name: "the orphan sweep's cancel",
		do: func(t *testing.T, h *Runtime) string {
			l, held := h.runs.lease(gateRunID)
			if !held {
				t.Fatal("Setup: the run holds no lease")
			}
			if h.runs.clearOrphaned(t.Context(), &l, h.runs.lifeOf(l.WorkflowID)) {
				return "cancelled"
			}
			return "left"
		},
		want: everyState(transitionWant{answer: "left"}, map[runSeen]transitionWant{
			seenRestartPaused: {wrote: methodKiroWorkflowCancel, answer: "cancelled"},
		}),
	},
}

// stateWrites lists the pause, resume, finish and cancel writes in calls.
func stateWrites(calls []string) []string {
	return slices.DeleteFunc(slices.Clone(calls), func(m string) bool {
		switch m {
		case methodKiroWorkflowPause, methodKiroWorkflowResume, methodKiroWorkflowUpdate, methodKiroWorkflowCancel:
			return false
		}
		return true
	})
}

// transitionReads are the tests' transition hooks, by the Runs each watches, so runtimes in concurrent subtests each
// keep their own.
var (
	transitionReads   sync.Map
	transitionsHooked sync.Once
)

// onTransitionRead installs fn as h's transition hook for the test.
func onTransitionRead(t *testing.T, h *Runtime, fn func(want runWant, seen runSeen)) {
	t.Helper()
	onTransitionReadErr(t, h, func(want runWant, seen runSeen) error {
		fn(want, seen)
		return nil
	})
}

// onTransitionReadErr installs fn as h's transition hook for the test, an error it returns failing the read, and
// returns its uninstall for a caller that ends it before the test does.
func onTransitionReadErr(t *testing.T, h *Runtime, fn func(want runWant, seen runSeen) error) (unhook func()) {
	t.Helper()
	transitionsHooked.Do(func() {
		hook := func(rs *Runs, _ string, want runWant, seen runSeen) error {
			if fn, ok := transitionReads.Load(rs); ok {
				return fn.(func(runWant, runSeen) error)(want, seen)
			}
			return nil
		}
		transitionHook.Store(&hook)
	})
	runs := h.runs
	transitionReads.Store(runs, fn)
	unhook = func() { transitionReads.Delete(runs) }
	t.Cleanup(unhook)
	return unhook
}

// Every request, through the entry point its actor calls, against every state its read can find: what it writes,
// what it answers, and the state it read.
func TestRunTransitions_EveryRequestInEveryState(t *testing.T) {
	for _, ask := range transitionAsks {
		for _, st := range transitionStates {
			t.Run(ask.name+" of "+st.name, func(t *testing.T) {
				h, br := pausedRunOnChat(t)
				if ask.gateClosed {
					wireGate(h, signin.NeedsSignIn)
				}
				br.setCallResult(methodKiroWorkflowPause, json.RawMessage(`{"paused":true}`))
				br.setCallResult(methodKiroWorkflowResume, json.RawMessage(`{}`))
				br.setCallResult(methodKiroWorkflowUpdate, json.RawMessage(`{"updated":true}`))
				st.set(t, br)
				if ask.prep != nil {
					ask.prep(t, br)
				}
				var read []runSeen
				var mu sync.Mutex
				onTransitionRead(t, h, func(_ runWant, seen runSeen) {
					mu.Lock()
					defer mu.Unlock()
					read = append(read, seen)
				})

				answer := ask.do(t, h)
				want := ask.want[st.seen]
				if answer != want.answer {
					t.Errorf("answer = %q, want %q", answer, want.answer)
				}
				var wantWrites []string
				if want.wrote != "" {
					wantWrites = []string{want.wrote}
				}
				if got := stateWrites(br.callLog()); !slices.Equal(got, wantWrites) {
					t.Errorf("writes = %v, want %v", got, wantWrites)
				}
				mu.Lock()
				defer mu.Unlock()
				if !slices.Equal(read, []runSeen{st.seen}) {
					t.Errorf("states read = %v, want one read of %v", read, st.seen)
				}
			})
		}
	}
}

// heldTransition holds the first read of want in its hook until the returned release, and counts every read.
func heldTransition(t *testing.T, h *Runtime, first runWant) (release func(), reads func() int) {
	t.Helper()
	gate := make(chan struct{})
	var mu sync.Mutex
	n, held := 0, false
	onTransitionRead(t, h, func(want runWant, _ runSeen) {
		mu.Lock()
		n++
		hold := !held && want == first
		held = held || hold
		mu.Unlock()
		if hold {
			<-gate
		}
	})
	return func() { close(gate) }, func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}
}

// Two resumes of one pause, either first, write one resume: the second reads the run after the first's write, a
// person's answering that it is already running and an automatic one leaving it.
func TestRunTransitions_TwoResumesOfOnePauseWriteOne(t *testing.T) {
	person := func(ctx context.Context, rs *Runs) string { return personAnswer(rs.resume(ctx, gateRunID, callerPage)) }
	heal := func(ctx context.Context, rs *Runs) string {
		return healAnswer(rs.resumeIfInterrupted(ctx, parkChat, gateRunID))
	}
	for _, tc := range []struct {
		first, second         func(context.Context, *Runs) string
		name                  string
		wantFirst, wantSecond string
		firstWant             runWant
	}{
		{
			name: "the automatic resume first", first: heal, second: person, firstWant: healResume,
			wantFirst: "written", wantSecond: "already_running",
		},
		{
			name: "the person's resume first", first: person, second: heal, firstWant: personResume,
			wantFirst: "done", wantSecond: "skipped",
		},
		{
			name: "another person's resume first", first: person, second: person, firstWant: personResume,
			wantFirst: "done", wantSecond: "already_running",
		},
		{
			name: "two automatic resumes", first: heal, second: heal, firstWant: healResume,
			wantFirst: "written", wantSecond: "skipped",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, br := pausedRunOnChat(t)
				resumes := countResumes(t, br, gateRunID)
				release, reads := heldTransition(t, h, tc.firstWant)

				var first, second string
				firstDone, secondDone := make(chan struct{}), make(chan struct{})
				go func() { defer close(firstDone); first = tc.first(t.Context(), h.runs) }()
				synctest.Wait()
				go func() { defer close(secondDone); second = tc.second(t.Context(), h.runs) }()
				synctest.Wait()
				if n := reads(); n != 1 {
					t.Errorf("reads while the first is held between its read and its write = %d, want its own alone", n)
				}
				release()
				<-firstDone
				<-secondDone

				if first != tc.wantFirst || second != tc.wantSecond {
					t.Errorf("answers = %q then %q, want %q then %q", first, second, tc.wantFirst, tc.wantSecond)
				}
				if n := resumes.Load(); n != 1 {
					t.Errorf("resumes written = %d, want 1", n)
				}
			})
		})
	}
}

// A person's pause and the gate's, either first, leave the run paused by the person, so the gate's release resumes
// nothing: the gate pauses only a running run, and a person's pause replaces the gate's.
func TestRunTransitions_APersonsPauseAndTheGatesLeaveThePersonsPause(t *testing.T) {
	person := func(ctx context.Context, rs *Runs) error { return rs.pause(ctx, gateRunID, callerPage) }
	gate := func(ctx context.Context, rs *Runs) error { return rs.pauseHeld(ctx, gateRunID) }
	for _, tc := range []struct {
		first, second func(context.Context, *Runs) error
		name          string
		// wantReasons are the pauses written, by reason, in order.
		wantReasons []string
		firstWant   runWant
	}{
		{
			name: "the gate's pause first", first: gate, second: person, firstWant: gatePause,
			wantReasons: []string{signInHoldReason, ""},
		},
		{
			name: "the person's pause first", first: person, second: gate, firstWant: personPause,
			wantReasons: []string{""},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, br := pausedRunOnChat(t)
				answerability := wireGate(h, signin.NeedsSignIn)
				g := &gateRun{h: h, br: br}
				br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, gateRunID, marotte.RunStatusRunning, ""))
				verbs := g.watchVerbs(t)
				release, reads := heldTransition(t, h, tc.firstWant)

				var firstErr, secondErr error
				firstDone, secondDone := make(chan struct{}), make(chan struct{})
				go func() { defer close(firstDone); firstErr = tc.first(t.Context(), h.runs) }()
				synctest.Wait()
				go func() { defer close(secondDone); secondErr = tc.second(t.Context(), h.runs) }()
				synctest.Wait()
				if n := reads(); n != 1 {
					t.Errorf("reads while the first is held between its read and its write = %d, want its own alone", n)
				}
				release()
				<-firstDone
				<-secondDone
				if err := errors.Join(firstErr, secondErr); err != nil {
					t.Fatalf("pauses = %v", err)
				}

				var reasons []string
				for _, p := range verbs.pauseParams() {
					r, _ := p["reason"].(string)
					reasons = append(reasons, r)
				}
				if !slices.Equal(reasons, tc.wantReasons) {
					t.Errorf("pauses written, by reason = %q, want %q", reasons, tc.wantReasons)
				}
				answerability.set(h, signin.Answerable)
				synctest.Wait()
				if n := verbs.resumes.Load(); n != 0 {
					t.Errorf("resumes written at the sign-in = %d, want none of the person's pause", n)
				}
			})
		})
	}
}

// transitionVerbWriters maps each of the pause, resume and update verbs, by its constant or its value, to the one
// function that may name it, and transitionOwned lists the only functions that may call a transition's actions:
// every pause, resume and repeat finish of a run, and every gate pause a finder reads, goes through Runs.transition.
// holdWriters maps each name that adds or drops a hold of the gate's to the only functions that may name it, so a
// hold changes only inside the transition: added by a heal's admission, dropped by the transition itself.
var (
	transitionVerbWriters = map[string]string{
		"methodKiroWorkflowPause": "Runs.writePause", strconv.Quote(methodKiroWorkflowPause): "Runs.writePause",
		"methodKiroWorkflowResume": "Runs.sendResume", strconv.Quote(methodKiroWorkflowResume): "Runs.sendResume",
		"methodKiroWorkflowUpdate": "Runs.updateNodeStatus", strconv.Quote(methodKiroWorkflowUpdate): "Runs.updateNodeStatus",
	}
	transitionActions = []string{
		"writePause", "sendResume", "resumeAsked", "holdPause", "heal", "parkForUser", "writeHealResume", "finishAsked",
		"keepHold", "endStepAsked", "updateNodeStatus",
	}
	transitionOwned = []string{
		"Runs.transition", "Runs.carryOut", "Runs.writePause", "Runs.sendResume", "Runs.resumeAsked", "Runs.holdPause",
		"Runs.heal", "Runs.healSpent", "Runs.parkForUser", "Runs.writeHealResume", "Runs.finishAsked", "Runs.keepHold",
		"Runs.endStepAsked",
	}
	holdWriters = map[string][]string{
		"heldHeals":   {"runGate.admitOrHold", "runGate.dropHold", "runGate.refreshLocked"},
		"admitOrHold": {"Runs.admitRunWork"},
		"holdClosed":  {"Runs.admitRunWork", "Runs.heal"},
		"dropHold":    {"Runs.transition"},
	}
)

// A pause, resume or update written outside the transition, by the verb's name, its value or a call to one of the
// transition's actions, fails here, as does a hold of the gate's added or dropped outside it. The verbs' own
// declarations and the run-work set name them too.
func TestRunTransitions_EveryPauseAndResumeGoesThroughTheTransition(t *testing.T) {
	fset := token.NewFileSet()
	for _, f := range parseAgentSources(t, fset) {
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				checkVerbDeclarations(t, fset, d)
			case *ast.FuncDecl:
				checkTransitionRoute(t, fset, d)
			}
		}
	}
}

// transitionVerbNamed is the transition verb n names and its one writer, ok false when it names none.
func transitionVerbNamed(n ast.Node) (verb, writer string, ok bool) {
	switch x := n.(type) {
	case *ast.Ident:
		verb = x.Name
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", "", false
		}
		verb = x.Value
	default:
		return "", "", false
	}
	writer, ok = transitionVerbWriters[verb]
	return verb, writer, ok
}

func checkVerbDeclarations(t *testing.T, fset *token.FileSet, d *ast.GenDecl) {
	t.Helper()
	for _, spec := range d.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 {
			continue
		}
		switch vs.Names[0].Name {
		case "methodKiroWorkflowPause", "methodKiroWorkflowResume", "methodKiroWorkflowUpdate", "runWorkVerbs":
			continue
		}
		ast.Inspect(vs, func(n ast.Node) bool {
			if verb, _, ok := transitionVerbNamed(n); ok {
				t.Errorf("%s: %s names %s outside the transition", fset.Position(n.Pos()), vs.Names[0].Name, verb)
			}
			return true
		})
	}
}

func checkTransitionRoute(t *testing.T, fset *token.FileSet, fn *ast.FuncDecl) {
	t.Helper()
	if fn.Body == nil {
		return
	}
	name := qualifiedName(fn)
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if verb, writer, ok := transitionVerbNamed(n); ok && name != writer {
			t.Errorf("%s: %s names %s; only %s may", fset.Position(n.Pos()), name, verb, writer)
		}
		if sel, ok := n.(*ast.SelectorExpr); ok && slices.Contains(transitionActions, sel.Sel.Name) &&
			!slices.Contains(transitionOwned, name) {
			t.Errorf("%s: %s calls the transition's %s; only Runs.transition decides a pause or resume",
				fset.Position(sel.Pos()), name, sel.Sel.Name)
		}
		if id, ok := n.(*ast.Ident); ok {
			if owners, guarded := holdWriters[id.Name]; guarded && !slices.Contains(owners, name) {
				t.Errorf("%s: %s names %s; only %v may, so a hold changes only inside the transition",
					fset.Position(id.Pos()), name, id.Name, owners)
			}
		}
		return true
	})
}
