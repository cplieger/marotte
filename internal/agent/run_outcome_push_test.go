package agent

import (
	"context"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

// runOutcomePush records the push kind too: it must reach the registry as run_outcome.
type runOutcomePush struct {
	sent    chan runPushSent
	subject marotte.PushSubject
}

type runPushSent struct {
	title   string
	body    string
	kind    marotte.PushKind
	subject marotte.PushSubject
}

func newRunOutcomePush() *runOutcomePush {
	return &runOutcomePush{sent: make(chan runPushSent, 4)}
}

func (*runOutcomePush) HasSubscribers() bool                     { return true }
func (*runOutcomePush) SetPreferences(map[marotte.PushKind]bool) {}
func (*runOutcomePush) Preferences() map[marotte.PushKind]bool   { return nil }
func (*runOutcomePush) Close()                                   {}
func (*runOutcomePush) Retract(marotte.PushSubject)              {}
func (p *runOutcomePush) Send(_ context.Context, n *marotte.NotificationPayload) {
	p.subject = n.PushSubject
	select {
	case p.sent <- runPushSent{title: n.Title, body: n.Body, kind: n.Kind, subject: n.PushSubject}:
	default:
	}
}

func newRunPushHub(t *testing.T) (*Runtime, *runOutcomePush) {
	t.Helper()
	cs := newTestChatStore()
	fp := newRunOutcomePush()
	h := New(context.Background(), t.TempDir(), func() ACPBridge { return newFakeBridge() }, cs, WithPush(fp))
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	return h, fp
}

func awaitRunPush(t *testing.T, fp *runOutcomePush) runPushSent {
	t.Helper()
	select {
	case got := <-fp.sent:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("no push sent for a terminal run; a run outlives the turn that launched it," +
			" so this is the only channel that reaches a reader who is not looking at the page")
		return runPushSent{}
	}
}

// A run's outcome is notified under the run, titled by its name, in kiro-cli's workflow words.
func TestObserveComplete_PushesTheRunsOutcome(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   string
	}{
		{"completed", "Workflow completed"},
		{"failed", "Workflow failed"},
		{"aborted", "Workflow aborted"},
		{"cancelled", "Workflow cancelled"},
	} {
		t.Run(tc.status, func(t *testing.T) {
			h, fp := newRunPushHub(t)

			h.runs.observeComplete(t.Context(), "", runNotif(methodWFRunComplete, map[string]any{
				"workflowId": "wf_1", "workflowName": "Nightly review", "status": tc.status,
			}))

			got := awaitRunPush(t, fp)
			if got.title != "Nightly review" || got.body != tc.want {
				t.Errorf("push = %q / %q, want %q / %q", got.title, got.body, "Nightly review", tc.want)
			}
			if got.kind != marotte.PushKindRunOutcome {
				t.Errorf("push kind = %q, want %q; the settings key governs this kind alone",
					got.kind, marotte.PushKindRunOutcome)
			}
			if got.subject != marotte.RunSubject("wf_1") {
				t.Errorf("push subject = %+v, want the run's; the worker routes on its prefix", got.subject)
			}
		})
	}
}

// A run's failure detail is KAS's stop reason; a run launched from a chat names that chat.
func TestObserveComplete_DetailIsTheStopReasonElseTheParent(t *testing.T) {
	h, fp := newRunPushHub(t)
	ctx := t.Context()
	h.chatStore.(*testChatStore).seed(t, "c1", func(c *marotte.Chat) { c.Name = "Ship it" })
	h.runs.grantLease(ctx, "wf_1", "deploy", launchOrigin{origin: runlease.OriginAgent, chatID: "c1"})
	h.runs.grantLease(ctx, "wf_2", "deploy", launchOrigin{origin: runlease.OriginAgent, chatID: "c1"})

	h.runs.observeComplete(ctx, "c1", runNotif(methodWFRunComplete, map[string]any{
		"workflowId": "wf_1", "status": "completed",
	}))
	if got := awaitRunPush(t, fp); got.title != "deploy" || got.body != "Workflow completed · from Ship it" {
		t.Errorf("completed child run pushed %q / %q, want deploy / %q", got.title, got.body, "Workflow completed · from Ship it")
	}
	h.runs.observeComplete(ctx, "c1", runNotif(methodWFRunComplete, map[string]any{
		"workflowId": "wf_2", "status": "aborted",
		"finalState": map[string]any{"stopInitiator": "agent", "stopReason": "budget exhausted"},
	}))
	if got := awaitRunPush(t, fp); got.body != "Workflow aborted · budget exhausted" {
		t.Errorf("aborted run pushed %q, want its stop reason", got.body)
	}
}

// An onMaxIterations pause notifies only a parentless run: a run with a parent agent is the
// parent's to resume.
func TestObserveComplete_APauseNotifiesOnlyAParentlessRun(t *testing.T) {
	pause := map[string]any{"pause": map[string]any{"kind": "maxIterations"}}
	t.Run("parentless", func(t *testing.T) {
		h, fp := newRunPushHub(t)
		h.runs.grantLease(t.Context(), "wf_1", "code-review", manualLaunch())
		h.runs.observeComplete(t.Context(), "", runNotif(methodWFRunComplete, map[string]any{
			"workflowId": "wf_1", "status": "paused", "finalState": pause,
		}))
		if got := awaitRunPush(t, fp); got.body != "Workflow paused · iteration limit reached" {
			t.Errorf("a parentless paused run pushed %q", got.body)
		}
	})
	t.Run("with a parent agent", func(t *testing.T) {
		h, fp := newRunPushHub(t)
		h.runs.grantLease(t.Context(), "wf_1", "code-review", launchOrigin{origin: runlease.OriginAgent, chatID: "c1"})
		h.runs.observeComplete(t.Context(), "c1", runNotif(methodWFRunComplete, map[string]any{
			"workflowId": "wf_1", "status": "paused", "finalState": pause,
		}))
		joinInflight(t, h)
		select {
		case got := <-fp.sent:
			t.Errorf("a paused run with a parent agent pushed %q; the parent intervenes", got.body)
		default:
		}
	})
	t.Run("parentless, paused for another reason", func(t *testing.T) {
		h, fp := newRunPushHub(t)
		h.runs.grantLease(t.Context(), "wf_1", "code-review", manualLaunch())
		h.runs.observeComplete(t.Context(), "", runNotif(methodWFRunComplete, map[string]any{
			"workflowId": "wf_1", "status": "paused", "finalState": map[string]any{"pause": map[string]any{"kind": "stepInput"}},
		}))
		joinInflight(t, h)
		select {
		case got := <-fp.sent:
			t.Errorf("a parentless run paused for a step's input pushed %q; only the iteration limit notifies", got.body)
		default:
		}
	})
}

// run_complete's top-level workflowName is always empty, so the label comes from the lease.
func TestObserveComplete_LabelFallsBackToTheLeasesRecipe(t *testing.T) {
	h, fp := newRunPushHub(t)
	ctx := t.Context()
	h.runs.grantLease(ctx, "wf_1", "code-review", manualLaunch())

	h.runs.observeComplete(ctx, "", runNotif(methodWFRunComplete, map[string]any{
		"workflowId": "wf_1", "status": "completed",
	}))

	if got := awaitRunPush(t, fp); got.title != "code-review" {
		t.Errorf("push title = %q, want %q; the frame carries no name, so the lease is the source",
			got.title, "code-review")
	}
}

// A step's question is notified under its run, naming the step: it parks the run on a person.
func TestHandleSessionNotify_NotifiesTheStepsQuestion(t *testing.T) {
	h, fp := newRunPushHub(t)
	h.runs.grantLease(t.Context(), "wf_1", "code-review", manualLaunch())
	h.runs.notifyAsk(t.Context(), &runAsk{chatID: "run:wf_1", payload: marotte.RunInputNeededPayload{
		WorkflowID: "wf_1", NodeID: "n1", AgentName: "reviewer", Question: "Merge it?",
	}})
	got := awaitRunPush(t, fp)
	want := runPushSent{
		title: "code-review", body: "Input required · reviewer: Merge it?",
		kind: marotte.PushKindPermission, subject: marotte.RunSubject("wf_1"),
	}
	if got != want {
		t.Errorf("step question pushed %+v, want %+v", got, want)
	}
}
