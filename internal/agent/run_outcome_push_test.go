package agent

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// runOutcomePush records the push kind too: it must reach the registry as run_outcome.
type runOutcomePush struct {
	sent    chan runPushSent
	subject marotte.PushSubject
}

type runPushSent struct {
	body    string
	kind    marotte.PushKind
	subject marotte.PushSubject
}

func newRunOutcomePush() *runOutcomePush {
	return &runOutcomePush{sent: make(chan runPushSent, 4)}
}

func (p *runOutcomePush) RegisterRoutes(*http.ServeMux)            {}
func (p *runOutcomePush) Subscribe(marotte.PushSubscription)       {}
func (p *runOutcomePush) Unsubscribe(string)                       {}
func (p *runOutcomePush) HasSubscribers() bool                     { return true }
func (p *runOutcomePush) SetPreferences(map[marotte.PushKind]bool) {}
func (p *runOutcomePush) ReloadPreferences(context.Context)        {}
func (p *runOutcomePush) Close()                                   {}
func (p *runOutcomePush) Retract(marotte.PushSubject)              {}
func (p *runOutcomePush) Send(
	_ context.Context, _, body string, kind marotte.PushKind, subject marotte.PushSubject, _ string,
) {
	p.subject = subject
	select {
	case p.sent <- runPushSent{body: body, kind: kind, subject: subject}:
	default:
	}
}

// newRunPushHub is newTestHub with a push recorder that keeps the kind.
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

// A run's outcome push is keyed on the run. Bodies are spelled out: computed ones pass any mapping, and
// this vocabulary is shared with handlers/run.ts toastCompletion.
func TestObserveComplete_PushesTheRunsOutcome(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   string
	}{
		{"completed", "Nightly review finished"},
		{"failed", "Nightly review failed"},
		{"aborted", "Nightly review was aborted"},
		{"cancelled", "Nightly review was cancelled"},
	} {
		t.Run(tc.status, func(t *testing.T) {
			h, fp := newRunPushHub(t)

			h.runs.observeComplete(t.Context(), "", runNotif(methodWFRunComplete, map[string]any{
				"workflowId": "wf_1", "workflowName": "Nightly review", "status": tc.status,
			}))

			got := awaitRunPush(t, fp)
			if got.body != tc.want {
				t.Errorf("push body = %q, want %q", got.body, tc.want)
			}
			if got.kind != marotte.PushKindRunOutcome {
				t.Errorf("push kind = %q, want %q; the settings key governs this kind alone",
					got.kind, marotte.PushKindRunOutcome)
			}
			if got.subject.Key != marotte.RunSubjectPrefix+"wf_1" {
				t.Errorf("push subject key = %q, want %q; the worker routes on that prefix",
					got.subject.Key, marotte.RunSubjectPrefix+"wf_1")
			}
			if got.subject.ChatID != "" {
				t.Errorf("push subject carries chat id %q; a run's notification is about the RUN",
					got.subject.ChatID)
			}
		})
	}
}

// An onMaxIterations pause arrives on the same frame and must not push "finished".
func TestObserveComplete_PushesNothingForANonTerminalRun(t *testing.T) {
	h, fp := newRunPushHub(t)

	h.runs.observeComplete(t.Context(), "", runNotif(methodWFRunComplete, map[string]any{
		"workflowId": "wf_1", "workflowName": "Nightly review", "status": "paused",
	}))

	joinInflight(t, h)
	select {
	case got := <-fp.sent:
		t.Errorf("a paused run pushed %q; the run has not ended", got.body)
	default:
	}
}

// run_complete's top-level workflowName is always empty, so the label comes from the lease.
func TestObserveComplete_LabelFallsBackToTheLeasesRecipe(t *testing.T) {
	h, fp := newRunPushHub(t)
	ctx := t.Context()
	h.runs.grantLease(ctx, "wf_1", "code-review", manualLaunch())

	h.runs.observeComplete(ctx, "", runNotif(methodWFRunComplete, map[string]any{
		"workflowId": "wf_1", "status": "completed",
	}))

	if got := awaitRunPush(t, fp); got.body != "code-review finished" {
		t.Errorf("push body = %q, want %q; the frame carries no name, so the lease is the source",
			got.body, "code-review finished")
	}
}

// With neither a frame name nor a recipe, the floor keeps the body from being a bare verb.
func TestRunOutcomeBody_FloorsTheLabel(t *testing.T) {
	t.Parallel()
	rs := &Runs{}
	label := rs.runOutcomeLabel(lifecycleFrame{WorkflowID: "wf_1", Status: marotte.RunStatusFailed})
	if got := runOutcomeBody(marotte.RunStatusFailed, label); got != "Workflow run failed" {
		t.Errorf("body with no name anywhere = %q, want %q", got, "Workflow run failed")
	}
}
