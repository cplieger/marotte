package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// allRunStatuses is KAS's WorkflowStatusSchema, exhaustively.
var allRunStatuses = []string{"running", "paused", "completed", "failed", "aborted"}

// TestAffordance_VerbsByStatus pins each status's verbs, mirroring KAS; every live run can be cancelled.
func TestAffordance_VerbsByStatus(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   []string
	}{
		{"running", []string{verbPause, verbCancel}},
		{string(marotte.RunStatusPaused), []string{verbResume, verbCancel}},
		{"completed", []string{}},
		{"failed", []string{verbRetry}},
		{"aborted", []string{verbRetry}},
	} {
		t.Run(tc.status, func(t *testing.T) {
			got := affordanceOf(&runFacts{status: tc.status})
			if !slices.Equal(got.Verbs, tc.want) {
				t.Errorf("affordanceOf(%q) verbs = %v, want %v", tc.status, got.Verbs, tc.want)
			}
			if len(got.Refused) != 0 {
				t.Errorf("affordanceOf(%q) refused %v; with no pause pending the status is the only gate",
					tc.status, got.Refused)
			}
		})
	}
}

// TestAffordance_ARetryableRunOffersRetryWhoeverLaunchedIt pins that kiro-cli's restore pass skips aborted runs.
func TestAffordance_ARetryableRunOffersRetryWhoeverLaunchedIt(t *testing.T) {
	for _, status := range []string{"failed", "aborted"} {
		t.Run(status, func(t *testing.T) {
			{
				got := affordanceOf(&runFacts{status: status})
				if !got.permits(verbRetry) {
					t.Errorf("a %s run does not offer retry (%v); nothing else recovers this run, "+
						"so withholding it leaves the run unreachable", status, got.Verbs)
				}
				if sentence := got.refusal(verbRetry); sentence != "" {
					t.Errorf("retry carries a refusal %q while also being offered", sentence)
				}
			}
		})
	}
}

// TestAffordance_ResumeIsOfferedOnAPausedNonResidentRun pins that resume makes the launching session resident first,
// so its affordance depends on nothing resident.
func TestAffordance_ResumeIsOfferedOnAPausedNonResidentRun(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowList: parentlessRunList("wf_1")}
	got := h.runs.affordance(t.Context(), "wf_1", string(marotte.RunStatusPaused))
	if !got.permits(verbResume) {
		t.Errorf("affordance verbs = %v, want resume offered", got.Verbs)
	}
	if sentence := got.refusal(verbResume); sentence != "" {
		t.Errorf("resume carries a refusal %q on a run it can reach", sentence)
	}
	if h.bridge.mgr.get(runChatID("wf_1")) != nil {
		t.Error("deciding the affordance made a host session resident")
	}
}

// TestAffordance_AnUnknownStatusDegradesToReadOnly pins that no control and no refusal for an uninterpretable state.
func TestAffordance_AnUnknownStatusDegradesToReadOnly(t *testing.T) {
	for _, status := range []string{"", "cancelled", "some_future_status"} {
		t.Run("status="+status, func(t *testing.T) {
			got := affordanceOf(&runFacts{status: status})
			if len(got.Verbs) != 0 || len(got.Refused) != 0 {
				t.Errorf("affordanceOf(%q) = %+v, want nothing offered and nothing refused", status, got)
			}
		})
	}
}

// TestAffordance_PauseAndResumeAreNeverOfferedTogether pins that they contradict each other.
func TestAffordance_PauseAndResumeAreNeverOfferedTogether(t *testing.T) {
	for _, status := range allRunStatuses {
		for _, pending := range []bool{true, false} {
			got := affordanceOf(&runFacts{status: status, pausePending: pending})
			if got.permits(verbPause) && got.permits(verbResume) {
				t.Errorf("status %q (pause pending=%v) offers both pause and resume: %v", status, pending, got.Verbs)
			}
		}
	}
}

// TestAffordance_EveryOfferedVerbHasARoute pins that an offered verb with no route answers 200 and does
// nothing. Retry has its own handler (its reply carries the outcome); cancel is ungated because it doubles as tab close.
func TestAffordance_EveryOfferedVerbHasARoute(t *testing.T) {
	routes := map[string]runVerb{
		runVerbCancel.name: runVerbCancel,
		runVerbPause.name:  runVerbPause,
		runVerbResume.name: runVerbResume,
	}
	for _, verbs := range runStatusVerbs {
		for _, verb := range verbs {
			if verb == verbRetry {
				continue
			}
			v, ok := routes[verb]
			if !ok {
				t.Errorf("the table offers %q and no route issues it", verb)
				continue
			}
			if v.issue == nil {
				t.Errorf("run verb %q has no issuer: the route would answer ok without calling KAS", verb)
			}
			// A verb the table can explain must have its route consult it.
			if len(v.from) == 0 && refusableVerb(verb) {
				t.Errorf("run verb %q can be refused by the table but its route does not consult "+
					"it, so the sentence would contradict what the server accepts", verb)
			}
		}
	}
}

// refusableVerb reports whether any combination withholds verb with a sentence, derived so a newly refusable verb is covered.
func refusableVerb(verb string) bool {
	for _, status := range allRunStatuses {
		for _, pending := range []bool{true, false} {
			aff := affordanceOf(&runFacts{status: status, pausePending: pending})
			if aff.refusal(verb) != "" {
				return true
			}
		}
	}
	return false
}

// TestChatForSession_ResolvesARunsParentFromTheStoreAlone pins that a run's parent chat is found with no session
// resident.
func TestChatForSession_ResolvesARunsParentFromTheStoreAlone(t *testing.T) {
	seed := func(t *testing.T, sessions ...string) *Runtime {
		t.Helper()
		h, cs, _ := newTestHub()
		if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
			c.Name = "Nightly publish"
			for _, s := range sessions {
				c.RecordSession(s)
			}
			return true
		}); err != nil {
			t.Fatalf("Setup: seeding the chat: %s", err)
		}
		return h
	}

	t.Run("the chat's current session, with no session open", func(t *testing.T) {
		h := seed(t, "sess_owned")
		id, complete := h.runs.chatForSession(t.Context(), "sess_owned")
		if id != "c1" || !complete {
			t.Errorf("chatForSession = (%q, %v), want (c1, true)", id, complete)
		}
	})

	// A run launched before a session change is parented on a retired id in the chain.
	t.Run("a RETIRED session in the chain still resolves", func(t *testing.T) {
		h := seed(t, "sess_old", "sess_current")
		if id, _ := h.runs.chatForSession(t.Context(), "sess_old"); id != "c1" {
			t.Errorf("chatForSession(a retired session) = %q, want c1", id)
		}
	})

	t.Run("a parentless run and a stranger session resolve to nothing", func(t *testing.T) {
		h := seed(t, "sess_owned")
		for _, session := range []string{"", "sess_stranger"} {
			if id, _ := h.runs.chatForSession(t.Context(), session); id != "" {
				t.Errorf("chatForSession(%q) = %q, want empty", session, id)
			}
		}
	})
}

// TestAffordance_AskingWithholdsResume pins the paused row while every paused step asks: KAS's
// resume re-parks such a step at once, so Cancel alone is offered, with no sentence for Resume.
func TestAffordance_AskingWithholdsResume(t *testing.T) {
	for name, tc := range map[string]struct {
		facts runFacts
		want  []string
	}{
		"asking":                    {runFacts{status: "paused", asking: true}, []string{verbCancel}},
		"an ordinary pause":         {runFacts{status: "paused"}, []string{verbResume, verbCancel}},
		"running while a step asks": {runFacts{status: "running", asking: true}, []string{verbPause, verbCancel}},
		"a loop at its cap": {
			runFacts{status: "paused", asking: true, pauseKind: pauseKindMaxIterations, pauseNodeID: "loop"},
			maxIterationsVerbs,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := affordanceOf(&tc.facts)
			if !slices.Equal(got.Verbs, tc.want) {
				t.Errorf("verbs = %v, want %v", got.Verbs, tc.want)
			}
			if len(got.Refused) != 0 {
				t.Errorf("refused = %v, want none: the composer is the way on", got.Refused)
			}
		})
	}
}

// TestHandleControls_ReadsTheAskCell pins that the controls route takes `asking` from the run's ask cell.
func TestHandleControls_ReadsTheAskCell(t *testing.T) {
	controls := func(t *testing.T, h *Runtime) marotte.RunControlsResponse {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/runs/wf_1/controls", nil)
		req.SetPathValue("id", "wf_1")
		rec := httptest.NewRecorder()
		h.runRoutes.handleControls(rec, req)
		var got marotte.RunControlsResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("GET controls = %d %s: %v", rec.Code, rec.Body.String(), err)
		}
		return got
	}
	t.Run("every paused step asks", func(t *testing.T) {
		h, _, _ := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
		readNow(t, h, "wf_1")
		if got := controls(t, h); !slices.Equal(got.Verbs, []string{verbCancel}) {
			t.Errorf("verbs = %v, want Cancel alone", got.Verbs)
		}
	})
	t.Run("an ordinary paused step beside the ask", func(t *testing.T) {
		plain := askStepNode{id: "build", status: "paused", session: "sess_build"}
		h, _, _ := askCoreHub(t, "wf_1",
			inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step"), plain))
		readNow(t, h, "wf_1")
		if got := controls(t, h); !slices.Contains(got.Verbs, verbResume) {
			t.Errorf("verbs = %v, want Resume offered", got.Verbs)
		}
	})
}
