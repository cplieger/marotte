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
)

// transitionState is one state a transition's read of the run can find, as KAS answers inspect.
type transitionState struct {
	set  func(t *testing.T, br *fakeBridge)
	name string
	seen runSeen
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
		br.setCallRPCErr(methodKiroWorkflowInspect, &marotte.RPCError{
			Code: -32603, Message: "Internal error", Data: json.RawMessage(`{"details":"workflow not found"}`),
		})
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
	{name: "another pause", seen: seenOtherPause, set: inspectAs(func(t *testing.T) json.RawMessage {
		return inspectPaused(t, gateRunID, branchWaitReason)
	})},
	{name: "ended", seen: seenEnded, set: inspectAs(func(t *testing.T) json.RawMessage {
		return inspectReply(t, gateRunID, marotte.RunStatusCancelled, "")
	})},
}

// transitionAsk is one request, through the entry point its actor calls, with what it writes and answers by state.
type transitionAsk struct {
	do         func(t *testing.T, h *Runtime) string
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
		seenInvoluntary: resumeWrote, seenOtherPause: resumeWrote,
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
			return personAnswer(h.runs.pause(t.Context(), gateRunID))
		},
		want: withRows(personRefusals, map[runSeen]transitionWant{
			seenRunning: pauseWrote, seenPausing: pauseWrote, seenGatePaused: pauseWrote,
			seenRestartPaused: pauseWrote, seenInvoluntary: pauseWrote,
			seenPersonPaused: {answer: "already_paused"}, seenOtherPause: {answer: "already_paused"},
		}),
	},
	{
		name: "a person's resume",
		do: func(t *testing.T, h *Runtime) string {
			return personAnswer(h.runs.resume(t.Context(), gateRunID))
		},
		want: personResumes,
	},
	{
		name: "a person's repeat extension",
		do: func(t *testing.T, h *Runtime) string {
			return personAnswer(h.runs.extendRepeat(t.Context(), gateRunID, "loop", 2))
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
			seenInvoluntary: finishWrote, seenOtherPause: finishWrote,
		}),
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
			if h.runs.clearOrphaned(t.Context(), &l) {
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
	person := func(ctx context.Context, rs *Runs) string { return personAnswer(rs.resume(ctx, gateRunID)) }
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
	person := func(ctx context.Context, rs *Runs) error { return rs.pause(ctx, gateRunID) }
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
		"methodKiroWorkflowUpdate": "Runs.finishAsked", strconv.Quote(methodKiroWorkflowUpdate): "Runs.finishAsked",
	}
	transitionActions = []string{
		"writePause", "sendResume", "resumeAsked", "holdPause", "heal", "parkForUser", "writeHealResume", "finishAsked",
		"keepHold",
	}
	transitionOwned = []string{
		"Runs.transition", "Runs.carryOut", "Runs.writePause", "Runs.sendResume", "Runs.resumeAsked", "Runs.holdPause",
		"Runs.heal", "Runs.parkForUser", "Runs.writeHealResume", "Runs.finishAsked", "Runs.keepHold",
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
