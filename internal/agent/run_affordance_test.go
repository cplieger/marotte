package agent

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// allRunStatuses is KAS's WorkflowStatusSchema, exhaustively.
var allRunStatuses = []string{"running", "paused", "completed", "failed", "aborted"}

// TestAffordance_VerbsByStatus pins each status's verbs on a hosted run, mirroring KAS; every live run can be cancelled.
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
			got := affordanceOf(&runFacts{status: tc.status, hosted: true})
			if !slices.Equal(got.Verbs, tc.want) {
				t.Errorf("affordanceOf(%q, hosted) verbs = %v, want %v", tc.status, got.Verbs, tc.want)
			}
			if len(got.Refused) != 0 {
				t.Errorf("affordanceOf(%q, hosted) refused %v; a hosted run's status is the only gate",
					tc.status, got.Refused)
			}
		})
	}
}

// TestAffordance_ARetryableRunOffersRetryWhoeverLaunchedIt pins that kiro-cli's restore pass skips aborted runs.
func TestAffordance_ARetryableRunOffersRetryWhoeverLaunchedIt(t *testing.T) {
	for _, status := range []string{"failed", "aborted"} {
		for name, f := range map[string]runFacts{
			"parentless, nothing hosting it":    {status: status},
			"chat-parented, chat closed":        {status: status, parentChat: "c1", parentName: "Nightly"},
			"chat-parented, chat's bridge live": {status: status, parentChat: "c1", parentName: "Nightly", hosted: true},
		} {
			t.Run(status+": "+name, func(t *testing.T) {
				got := affordanceOf(&f)
				if !got.permits(verbRetry) {
					t.Errorf("a %s run does not offer retry (%v); nothing else recovers this run, "+
						"so withholding it leaves the run unreachable", status, got.Verbs)
				}
				if sentence := got.refusal(verbRetry); sentence != "" {
					t.Errorf("retry carries a refusal %q while also being offered", sentence)
				}
			})
		}
	}
}

// TestAffordance_PauseIsWithheldFromARunNothingHosts pins that KAS's pause throws for an unregistered run.
func TestAffordance_PauseIsWithheldFromARunNothingHosts(t *testing.T) {
	got := affordanceOf(&runFacts{status: "running"})
	if got.permits(verbPause) {
		t.Errorf("pause is offered on a run nothing hosts; its only outcome is a refusal")
	}
	if got.refusal(verbPause) == "" {
		t.Errorf("pause was withheld with no sentence; an empty control row tells a reader " +
			"nothing about why the run cannot be driven")
	}
	// Cancel works through any connection.
	if !got.permits(verbCancel) {
		t.Errorf("cancel was withheld from a live run (%v), leaving it unstoppable", got.Verbs)
	}
}

// TestAffordance_ResumeIsOfferedOnAPausedRunNothingHosts pins that resume makes the launching session live first.
func TestAffordance_ResumeIsOfferedOnAPausedRunNothingHosts(t *testing.T) {
	for name, f := range map[string]runFacts{
		"parentless":    {status: string(marotte.RunStatusPaused)},
		"chat-parented": {status: string(marotte.RunStatusPaused), parentChat: "c1", parentName: "Nightly"},
	} {
		t.Run(name, func(t *testing.T) {
			got := affordanceOf(&f)
			if !got.permits(verbResume) {
				t.Errorf("affordanceOf(%+v) verbs = %v, want resume offered", f, got.Verbs)
			}
			if sentence := got.refusal(verbResume); sentence != "" {
				t.Errorf("resume carries a refusal %q on a run it can reach", sentence)
			}
		})
	}
}

// TestAffordance_ARefusalNamesTheChatToOpen pins that opening that chat respawns the parent's bridge.
func TestAffordance_ARefusalNamesTheChatToOpen(t *testing.T) {
	got := affordanceOf(&runFacts{status: "running", parentChat: "c-abc", parentName: "Nightly publish"})
	sentence := got.refusal(verbPause)
	if !strings.Contains(sentence, "Nightly publish") {
		t.Errorf("the refusal = %q, want it to name the chat to open", sentence)
	}

	// An empty name falls back to the id.
	unnamedAff := affordanceOf(&runFacts{status: "running", parentChat: "c-abc"})
	unnamed := unnamedAff.refusal(verbPause)
	if !strings.Contains(unnamed, "c-abc") {
		t.Errorf("the refusal for an unnamed chat = %q, want it to name the chat's id", unnamed)
	}

	// A parentless run's sentence invents no chat.
	parentlessAff := affordanceOf(&runFacts{status: "running"})
	parentless := parentlessAff.refusal(verbPause)
	if strings.Contains(parentless, "chat") && !strings.Contains(parentless, "Cancel") {
		t.Errorf("a parentless run's refusal = %q, want it to name no chat and to name the "+
			"verb that still works", parentless)
	}
}

// TestAffordance_AnUnknownStatusDegradesToReadOnly pins that no control and no refusal for an uninterpretable state.
func TestAffordance_AnUnknownStatusDegradesToReadOnly(t *testing.T) {
	for _, status := range []string{"", "cancelled", "some_future_status"} {
		t.Run("status="+status, func(t *testing.T) {
			got := affordanceOf(&runFacts{status: status, hosted: true})
			if len(got.Verbs) != 0 || len(got.Refused) != 0 {
				t.Errorf("affordanceOf(%q) = %+v, want nothing offered and nothing refused", status, got)
			}
		})
	}
}

// TestAffordance_PauseAndResumeAreNeverOfferedTogether pins that they contradict each other.
func TestAffordance_PauseAndResumeAreNeverOfferedTogether(t *testing.T) {
	for _, status := range allRunStatuses {
		for _, hosted := range []bool{true, false} {
			got := affordanceOf(&runFacts{status: status, hosted: hosted})
			if got.permits(verbPause) && got.permits(verbResume) {
				t.Errorf("status %q (hosted=%v) offers both pause and resume: %v", status, hosted, got.Verbs)
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
		for _, hosted := range []bool{true, false} {
			aff := affordanceOf(&runFacts{status: status, hosted: hosted})
			if aff.refusal(verb) != "" {
				return true
			}
		}
	}
	return false
}

// TestChatForSession_ResolvesARunsParentWithoutALiveBridge pins that hostBridgeChat needs a live bridge, wrong for a refusal.
func TestChatForSession_ResolvesARunsParentWithoutALiveBridge(t *testing.T) {
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

	t.Run("the chat's current session, with no bridge open", func(t *testing.T) {
		h := seed(t, "sess_owned")
		id, name, complete := h.runs.chatForSession(t.Context(), "sess_owned")
		if id != "c1" || name != "Nightly publish" || !complete {
			t.Errorf("chatForSession = (%q, %q, %v), want (c1, Nightly publish, true)", id, name, complete)
		}
	})

	// A run launched before a session change is parented on a retired id in the chain.
	t.Run("a RETIRED session in the chain still resolves", func(t *testing.T) {
		h := seed(t, "sess_old", "sess_current")
		if id, _, _ := h.runs.chatForSession(t.Context(), "sess_old"); id != "c1" {
			t.Errorf("chatForSession(a retired session) = %q, want c1", id)
		}
	})

	t.Run("a parentless run and a stranger session resolve to nothing", func(t *testing.T) {
		h := seed(t, "sess_owned")
		for _, session := range []string{"", "sess_stranger"} {
			if id, name, _ := h.runs.chatForSession(t.Context(), session); id != "" || name != "" {
				t.Errorf("chatForSession(%q) = (%q, %q), want empty", session, id, name)
			}
		}
	})
}

// TestAffordance_ChatParentedRunIsHostedByItsChatsBridge pins that an open launching chat hosts the run with nothing under `run:<id>`.
func TestAffordance_ChatParentedRunIsHostedByItsChatsBridge(t *testing.T) {
	h, cs, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: kasRuns(t, map[string]any{
			"workflowId": "wf_1", "status": "running", "parentSessionId": "sess_owned",
		}),
	}
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "Nightly publish"
		c.RecordSession("sess_owned")
		return true
	}); err != nil {
		t.Fatalf("Setup: seeding the chat: %s", err)
	}

	closed := h.runs.affordance(t.Context(), "wf_1", "running")
	if closed.permits(verbPause) {
		t.Error("pause offered while the launching chat has no bridge; nothing holds the run")
	}
	if !strings.Contains(closed.refusal(verbPause), "Nightly publish") {
		t.Errorf("the refusal = %q, want it to name the launching chat", closed.refusal(verbPause))
	}

	if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("Setup: opening the chat's bridge: %s", err)
	}
	open := h.runs.affordance(t.Context(), "wf_1", "running")
	if !open.permits(verbPause) {
		t.Errorf("pause withheld from a run whose launching chat is live (%v, %v); that process "+
			"holds the run's registry entry", open.Verbs, open.Refused)
	}
}
