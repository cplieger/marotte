package workflow

// The table runs workflow.Health over live kiro-cli replies (testdata/inspect). branch-engine-paused,
// parallel-settled-paused, reparked-silently and at-cap are kiro-cli 3.5.0 replies (scrubbed: workspace, model ids,
// agent names, captured text); the owner-state and pause-record cases overlay a reply with the run-level fields
// kiro-cli 3.5.0's state schema declares (pauseDetail {class, code, occurredAt}, pausePending {initiator, reason,
// requestedAt}, stopInitiator "user", stopReason, pause {kind: maxIterations}), as asks_test.go recombines node shapes.

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

var healthNow = time.Date(2026, 11, 20, 6, 0, 0, 0, time.UTC)

// overlaid decodes fixture name for workflowID with edit applied to its raw document first.
func overlaid(t *testing.T, name string, edit func(doc map[string]any)) Inspect {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(fixture(t, name), &doc); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
	if edit != nil {
		edit(doc)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	id, _ := doc["workflowId"].(string)
	in, err := DecodeInspect(id, raw)
	if err != nil {
		t.Fatalf("DecodeInspect: %v", err)
	}
	return in
}

func stateOf(doc map[string]any) map[string]any { return doc["state"].(map[string]any) }

// firstStep is the first step node of the document's tree in pre-order.
func firstStep(doc map[string]any) map[string]any {
	var find func(n map[string]any) map[string]any
	find = func(n map[string]any) map[string]any {
		if n["type"] == "step" {
			return n
		}
		kids, _ := n["children"].([]any)
		for _, k := range kids {
			if got := find(k.(map[string]any)); got != nil {
				return got
			}
		}
		return nil
	}
	return find(stateOf(doc)["root"].(map[string]any))
}

// pausedStep is the first paused step node with id nodeID.
func pausedStep(doc map[string]any, nodeID string) map[string]any {
	var find func(n map[string]any) map[string]any
	find = func(n map[string]any) map[string]any {
		if n["nodeId"] == nodeID && n["status"] == "paused" {
			return n
		}
		kids, _ := n["children"].([]any)
		for _, k := range kids {
			if got := find(k.(map[string]any)); got != nil {
				return got
			}
		}
		return nil
	}
	return find(stateOf(doc)["root"].(map[string]any))
}

func healthOf(in Inspect, f HealthFacts) RunHealth {
	if f.Asks == nil && in.OK {
		f.Asks = OpenAsks(in, RunLog{})
	}
	return Health(in, &f, healthNow)
}

func stepState(h RunHealth, nodeID string) (StepHealth, bool) {
	for _, s := range h.Steps {
		if s.NodeID == nodeID {
			return s, true
		}
	}
	return StepHealth{}, false
}

func TestHealth_ParkedLeaves(t *testing.T) {
	t.Parallel()
	launched := HealthFacts{Reachable: true, Launched: true}
	cases := []struct {
		name    string
		fixture string
		edit    func(map[string]any)
		facts   HealthFacts
		want    HealthState
	}{
		// A step that asked, was answered, and ended its next turn on plain text (kiro-cli 3.5.0, live).
		{name: "a plain-text re-park is parked silently", fixture: "reparked-silently", facts: launched, want: HealthParkedSilently},
		{name: "a gate park is parked silently", fixture: "gated-park", facts: launched, want: HealthParkedSilently},
		{name: "a lost-signal park is parked silently", fixture: "unsaved-signal-park", facts: launched, want: HealthParkedSilently},
		{name: "a question waits on the launching chat", fixture: "parked", facts: launched, want: HealthWaitingOnParent},
		{name: "a re-asked question waits on the launching chat", fixture: "reparked-after-parent-send", facts: launched, want: HealthWaitingOnParent},
		{
			name: "a parentless run's question waits on a person", fixture: "parked",
			facts: HealthFacts{Reachable: true}, want: HealthWaitingOnPerson,
		},
		{
			// An older record whose node signal was cleared while the run's sentence still names the question.
			name: "the sole ask under the question sentence is a question", fixture: "parked",
			edit: func(doc map[string]any) {
				s := firstStep(doc)
				delete(s, "completionSignal")
				delete(s, "completionSignalSource")
			},
			facts: launched, want: HealthWaitingOnParent,
		},
		{
			name: "a step paused through update_status is paused", fixture: "parked",
			edit:  func(doc map[string]any) { firstStep(doc)["completionSignalSource"] = "status_update" },
			facts: launched, want: HealthPaused,
		},
		{
			name: "a permission request waits on a person", fixture: "parked",
			edit: func(doc map[string]any) {
				firstStep(doc)["attention"] = []any{map[string]any{"kind": "permission", "since": "2026-11-01T09:58:33.463Z", "toolCallId": "tc-1"}}
			},
			facts: launched, want: HealthWaitingOnPerson,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			in := overlaid(t, c.fixture, c.edit)
			h := healthOf(in, c.facts)
			if h.State != c.want {
				t.Fatalf("run state = %q (%s), want %q", h.State, h.Why, c.want)
			}
			if h.Lead < 0 || h.Steps[h.Lead].State != c.want {
				t.Fatalf("lead step = %d of %+v, want a %q step", h.Lead, h.Steps, c.want)
			}
		})
	}
}

func TestHealth_ParkedSilentlyCarriesTheAsk(t *testing.T) {
	t.Parallel()
	in := overlaid(t, "reparked-silently", nil)
	asks := OpenAsks(in, RunLog{Steps: map[string]StepLog{
		"sess_63ef0731-ce88-4a8f-b0b3-eaa68afd4027": {Said: "probe done"},
	}})
	h := Health(in, &HealthFacts{Reachable: true, Launched: true, Asks: asks}, healthNow)
	s := h.Steps[h.Lead]
	if s.AskID != asks[0].ID || s.Key != asks[0].ID {
		t.Errorf("AskID, Key = %q, %q, want the ask's id %q", s.AskID, s.Key, asks[0].ID)
	}
	if s.Quote != "probe done" {
		t.Errorf("Quote = %q, want the step's last words", s.Quote)
	}
	if want := time.Date(2026, 11, 20, 4, 35, 45, 557e6, time.UTC); !s.Since.Equal(want) {
		t.Errorf("Since = %v, want the park's since %v", s.Since, want)
	}
	if !strings.Contains(s.Why, "no signal") {
		t.Errorf("Why = %q, want it to say the turn ended with no signal", s.Why)
	}
}

func TestHealth_EnginePausedBranch(t *testing.T) {
	t.Parallel()
	const pathKey = "media-build"
	cases := []struct {
		name    string
		edit    func(map[string]any)
		facts   func(path string) map[string]StepFacts
		wantWhy string
		since   time.Time
	}{
		{name: "no cause recorded", wantWhy: "recorded no reason"},
		{
			name: "a throttled turn",
			facts: func(path string) map[string]StepFacts {
				return map[string]StepFacts{path: {TransientFailure: "ClientThrottleError", PausedAt: healthNow.Add(-time.Hour)}}
			},
			wantWhy: "ClientThrottleError", since: healthNow.Add(-time.Hour),
		},
		{
			name:    "a spent continuation budget",
			edit:    func(doc map[string]any) { pausedStep(doc, pathKey)["continuationAttempts"] = 4 },
			wantWhy: "three turns with no signal",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			in := overlaid(t, "branch-engine-paused", c.edit)
			path := ""
			for _, st := range Steps(in.State) {
				if st.NodeID == pathKey && st.Status == statusPaused {
					path = PathKey(st.Path)
				}
			}
			f := HealthFacts{Reachable: true, Launched: true}
			if c.facts != nil {
				f.Steps = c.facts(path)
			}
			h := healthOf(in, f)
			if h.State != HealthEnginePaused {
				t.Fatalf("run state = %q (%s), want engine_paused", h.State, h.Why)
			}
			s, ok := stepState(h, pathKey)
			if !ok || s.State != HealthEnginePaused {
				t.Fatalf("step %s = %+v, want engine_paused", pathKey, s)
			}
			if !strings.Contains(s.Why, c.wantWhy) || !strings.Contains(s.Why, "until its siblings finish") {
				t.Errorf("Why = %q, want %q and the sibling hold", s.Why, c.wantWhy)
			}
			if !s.Since.Equal(c.since) {
				t.Errorf("Since = %v, want %v", s.Since, c.since)
			}
			if running, ok := stepState(h, "search-build"); !ok || running.State != HealthHealthy {
				t.Errorf("the running sibling = %+v, want a healthy listed step", running)
			}
		})
	}
}

// parallel-settled-paused is a kiro-cli 3.5.0 run read once its throttled branch's sibling settled: the parallel
// and the run read paused, and the run carries the branch's transient pauseDetail.
func TestHealth_SettledParallelPause(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		facts HealthFacts
		want  HealthState
		owner bool
	}{
		{name: "the heal declines it", facts: HealthFacts{Reachable: true, Launched: true}, want: HealthEnginePaused},
		{
			name:  "the heal takes it",
			facts: HealthFacts{Reachable: true, Launched: true, HealingSince: healthNow.Add(-time.Minute), HealTry: 1},
			want:  HealthHealing, owner: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			in := overlaid(t, "parallel-settled-paused", nil)
			if in.State.Status != statusPaused {
				t.Fatalf("Setup: the capture's run status = %q, want paused", in.State.Status)
			}
			h := healthOf(in, c.facts)
			if h.State != c.want {
				t.Fatalf("run state = %q (%s), want %q", h.State, h.Why, c.want)
			}
			if h.Owner != c.owner {
				t.Errorf("Owner = %v, want %v", h.Owner, c.owner)
			}
			if done, ok := stepState(h, "seat-a"); ok {
				t.Errorf("the settled sibling is listed as %+v, want it left out as healthy and finished", done)
			}
		})
	}
}

// pausedWith overlays a paused run's record on fixture name.
func pausedWith(fields map[string]any) func(map[string]any) {
	return func(doc map[string]any) {
		s := stateOf(doc)
		s["status"] = "paused"
		maps.Copy(s, fields)
	}
}

const parentStopReason = "Pause requested by owning parent/orchestrator; human intent not verified."

func TestHealth_RunOrder(t *testing.T) {
	t.Parallel()
	detail := func(class string) map[string]any {
		return map[string]any{"class": class, "code": "X", "occurredAt": "2026-11-20T05:00:00.000Z"}
	}
	cases := []struct {
		name     string
		fixture  string
		edit     func(map[string]any)
		facts    HealthFacts
		want     HealthState
		wantWhy  string
		wantStep HealthState
		owner    bool
	}{
		{name: "a repeat at its cap is at_cap", fixture: "at-cap", want: HealthAtCap, owner: true},
		{
			name: "a paused run nothing explains is engine paused", fixture: "at-cap",
			edit: func(doc map[string]any) { delete(stateOf(doc), "pause") },
			want: HealthEnginePaused, wantWhy: "nothing marotte knows of will resume it",
		},
		{
			name: "a person's pause is paused", fixture: "at-cap",
			edit: func(doc map[string]any) {
				delete(stateOf(doc), "pause")
				stateOf(doc)["stopInitiator"] = "user"
			},
			want: HealthPaused, wantWhy: "paused by a person", owner: true,
		},
		{
			name: "a parent's pause is paused by its parent", fixture: "at-cap",
			edit: func(doc map[string]any) {
				delete(stateOf(doc), "pause")
				stateOf(doc)["stopInitiator"] = "user"
				stateOf(doc)["stopReason"] = parentStopReason
			},
			want: HealthPaused, wantWhy: "paused by its parent", owner: true,
		},
		{
			name: "a queued person's pause is paused", fixture: "parked",
			edit:  pausedWith(map[string]any{"pausePending": map[string]any{"initiator": "user", "requestedAt": "2026-11-20T05:00:00.000Z"}}),
			facts: HealthFacts{Launched: true}, want: HealthPaused, owner: true,
		},
		{
			name: "a re-park under a parent pause reads paused and keeps its step state", fixture: "reparked-silently",
			edit:  pausedWith(map[string]any{"stopInitiator": "user", "stopReason": parentStopReason}),
			facts: HealthFacts{Launched: true}, want: HealthPaused, wantStep: HealthParkedSilently, owner: true,
		},
		{
			name: "a transient pause the heal takes is healing", fixture: "reparked-silently",
			edit:  pausedWith(map[string]any{"pauseDetail": detail("transient-error")}),
			facts: HealthFacts{Launched: true, HealingSince: healthNow.Add(-time.Minute), HealTry: 1},
			want:  HealthHealing, wantStep: HealthParkedSilently, owner: true,
		},
		{
			name: "a transient pause the heal declines is engine paused", fixture: "at-cap",
			edit: func(doc map[string]any) {
				delete(stateOf(doc), "pause")
				stateOf(doc)["pauseDetail"] = detail("transient-error")
			},
			want: HealthEnginePaused,
		},
		{
			name: "a pause marotte stopped resuming says so", fixture: "at-cap",
			edit: func(doc map[string]any) {
				delete(stateOf(doc), "pause")
				stateOf(doc)["pauseDetail"] = detail("transient-error")
			},
			facts: HealthFacts{AutoResumeStoppedAt: healthNow.Add(-time.Minute)},
			want:  HealthEnginePaused, wantWhy: "marotte stopped resuming it",
		},
		{
			name: "a question pause marotte stopped resuming reads the stop, not the question", fixture: "parked",
			facts: HealthFacts{Launched: true, AutoResumeStoppedAt: healthNow.Add(-time.Minute)},
			want:  HealthEnginePaused, wantWhy: "marotte stopped resuming it",
		},
		{
			name: "a spent continuation budget is engine paused", fixture: "at-cap",
			edit: func(doc map[string]any) {
				delete(stateOf(doc), "pause")
				stateOf(doc)["pauseDetail"] = detail("continuation-exhausted")
			},
			want: HealthEnginePaused, wantWhy: "three turns with no signal",
		},
		{
			name: "a lost parent authority is engine paused", fixture: "at-cap",
			edit: func(doc map[string]any) {
				delete(stateOf(doc), "pause")
				stateOf(doc)["pauseDetail"] = detail("parent-authority")
			},
			want: HealthEnginePaused, wantWhy: "lost authority over the run",
		},
		{
			name: "an unknown pause class is engine paused", fixture: "at-cap",
			edit: func(doc map[string]any) {
				delete(stateOf(doc), "pause")
				stateOf(doc)["pauseDetail"] = detail("solar-flare")
			},
			want: HealthEnginePaused,
		},
		{
			name: "a pause record that does not parse is engine paused", fixture: "at-cap",
			edit: func(doc map[string]any) {
				delete(stateOf(doc), "pause")
				stateOf(doc)["pauseDetail"] = 7
			},
			want: HealthEnginePaused, wantWhy: "could not be read",
		},
		{
			name: "a restart pause marotte did not resume is engine paused", fixture: "at-cap",
			edit:  func(doc map[string]any) { delete(stateOf(doc), "pause") },
			facts: HealthFacts{InterruptedPause: true},
			want:  HealthEnginePaused, wantWhy: "a restart paused it",
		},
		{
			name: "a pause record outranks a step question", fixture: "parked",
			edit:  pausedWith(map[string]any{"pauseDetail": detail("continuation-exhausted")}),
			facts: HealthFacts{Launched: true}, want: HealthEnginePaused, wantStep: HealthWaitingOnParent,
		},
		{
			name: "a failed read is unknown", fixture: "parked",
			facts: HealthFacts{Reachable: false, Launched: true, Asks: []Ask{}}, want: HealthUnknown, wantWhy: "cannot read",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := c.facts
			if c.want != HealthUnknown {
				f.Reachable = true
			}
			h := healthOf(overlaid(t, c.fixture, c.edit), f)
			if h.State != c.want {
				t.Fatalf("run state = %q (%s), want %q", h.State, h.Why, c.want)
			}
			if h.Owner != c.owner {
				t.Errorf("Owner = %v, want %v", h.Owner, c.owner)
			}
			if c.wantWhy != "" && !strings.Contains(h.Why, c.wantWhy) {
				t.Errorf("Why = %q, want it to contain %q", h.Why, c.wantWhy)
			}
			if c.wantStep != "" {
				found := false
				for _, s := range h.Steps {
					found = found || s.State == c.wantStep
				}
				if !found {
					t.Errorf("steps %+v, want one listed %q", h.Steps, c.wantStep)
				}
			}
		})
	}
}

func TestHealth_NoReadYetIsUnknown(t *testing.T) {
	t.Parallel()
	h := Health(Inspect{}, &HealthFacts{}, healthNow)
	if h.State != HealthUnknown || !strings.Contains(h.Why, "has not read") {
		t.Errorf("Health of no read = %q (%s), want unknown, not read", h.State, h.Why)
	}
	h = Health(Inspect{}, &HealthFacts{ReadFailed: true}, healthNow)
	if h.State != HealthUnknown || !strings.Contains(h.Why, "cannot read") {
		t.Errorf("Health of a failed first read = %q (%s), want unknown, cannot read", h.State, h.Why)
	}
}

// heldBranch is a parallel with a branch that sent success and is held open beside a running sibling.
func heldBranch(t *testing.T, signal string) Inspect {
	t.Helper()
	held := stepNode("ask", "paused", fixtureSession, signal, parked)
	sibling := stepNode("other", "running", "sess_other", "", nil)
	return recombined(t, "running", map[string]any{
		"nodeId": "fan", "type": "parallel", "status": "running", "children": []any{held, sibling},
	})
}

func TestHealth_HeldSignalIsHealthy(t *testing.T) {
	t.Parallel()
	for _, signal := range []string{"success", "error"} {
		t.Run(signal, func(t *testing.T) {
			t.Parallel()
			h := healthOf(heldBranch(t, signal), HealthFacts{Reachable: true, Launched: true})
			if h.State != HealthHealthy {
				t.Fatalf("run state = %q (%s), want healthy", h.State, h.Why)
			}
			s, ok := stepState(h, "ask")
			if !ok || s.State != HealthHealthy || !strings.Contains(s.Why, "kirodotdev/Kiro#12281") {
				t.Errorf("held step = %+v, want healthy with the hold in its why", s)
			}
		})
	}
}

func TestHealth_BlockedAskSaysWhy(t *testing.T) {
	t.Parallel()
	asker := stepNode("ask", "paused", fixtureSession, "", parked)
	sibling := stepNode("other", "running", "sess_other", "", nil)
	in := recombined(t, "running", map[string]any{
		"nodeId": "fan", "type": "parallel", "status": "running", "children": []any{asker, sibling},
	})
	// The recombined run keeps the parked fixture's question sentence, which would make this sole ask a question.
	in.State.PauseReason = ""
	h := healthOf(in, HealthFacts{Reachable: true, Launched: true})
	s, ok := stepState(h, "ask")
	if !ok || s.State != HealthParkedSilently {
		t.Fatalf("step = %+v, want parked_silently", s)
	}
	if !strings.Contains(s.Why, "cannot be answered while a sibling runs") {
		t.Errorf("Why = %q, want the sibling block named", s.Why)
	}
}

func TestHealth_Hung(t *testing.T) {
	t.Parallel()
	in := heldBranch(t, "success")
	var otherPath string
	for _, st := range Steps(in.State) {
		if st.NodeID == "other" {
			otherPath = PathKey(st.Path)
		}
	}
	call := &OpenCall{Title: "go test ./...", Kind: "execute", StartedAt: healthNow.Add(-40 * time.Minute)}
	cases := []struct {
		name   string
		facts  StepFacts
		want   HealthState
		listed bool
	}{
		{name: "silent 35 minutes", facts: StepFacts{CountsTowardSilence: true, LastAt: healthNow.Add(-35 * time.Minute), OpenCall: call}, want: HealthHung},
		{name: "silent 34 minutes", facts: StepFacts{CountsTowardSilence: true, LastAt: healthNow.Add(-34 * time.Minute)}, want: HealthHealthy},
		{name: "a turn that does not count", facts: StepFacts{LastAt: healthNow.Add(-2 * time.Hour)}, want: HealthHealthy},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := healthOf(in, HealthFacts{Reachable: true, Launched: true, Steps: map[string]StepFacts{otherPath: c.facts}})
			if h.State != c.want {
				t.Fatalf("run state = %q (%s), want %q", h.State, h.Why, c.want)
			}
			s, ok := stepState(h, "other")
			if !ok {
				t.Fatalf("the running step is not listed: %+v", h.Steps)
			}
			if s.State != c.want || !s.LastAt.Equal(c.facts.LastAt) {
				t.Errorf("step = %+v, want %q with its last activity", s, c.want)
			}
			if c.want == HealthHung && (s.OpenCall == nil || s.OpenCall.Title != call.Title || !s.Since.Equal(c.facts.LastAt)) {
				t.Errorf("hung step = %+v, want its open call and since its last activity", s)
			}
		})
	}
}

func TestHealth_UnfinishedStep(t *testing.T) {
	t.Parallel()
	in := overlaid(t, "running-after-answer", nil)
	path := PathKey([]string{fixtureRun, "ask"})
	at := healthNow.Add(-time.Minute)
	cases := []struct {
		name  string
		grade StepGrade
		want  HealthState
	}{
		{name: "an empty answer", grade: StepGrade{Kind: UnfinishedEmptyAnswer, At: at, TurnID: "t-1"}, want: HealthUnfinishedStep},
		{name: "a refusal", grade: StepGrade{Kind: UnfinishedRefused, At: at, TurnID: "t-1"}, want: HealthUnfinishedStep},
		{name: "a model-call limit", grade: StepGrade{Kind: UnfinishedModelCallLimit, At: at, TurnID: "t-1"}, want: HealthUnfinishedStep},
		{name: "told", grade: StepGrade{Kind: UnfinishedEmptyAnswer, At: at, TurnID: "t-1", Told: true}, want: HealthHealthy},
		{name: "a newer turn opened", grade: StepGrade{Kind: UnfinishedEmptyAnswer, At: at, TurnID: "t-1", Superseded: true}, want: HealthHealthy},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			g := c.grade
			h := healthOf(in, HealthFacts{Reachable: true, Launched: true, Steps: map[string]StepFacts{path: {Grade: &g}}})
			if h.State != c.want {
				t.Fatalf("run state = %q (%s), want %q", h.State, h.Why, c.want)
			}
			if c.want != HealthUnfinishedStep {
				return
			}
			s := h.Steps[h.Lead]
			if s.Key != "t-1" || !s.Since.Equal(at) || s.Grade != c.grade.Kind {
				t.Errorf("step = %+v, want key t-1, since the grade, kind %q", s, c.grade.Kind)
			}
		})
	}
}

func TestHealth_StepPrecedence(t *testing.T) {
	t.Parallel()
	reparked := stepNode("silent", "paused", "sess_silent", "", parked)
	question := stepNode("asker", "paused", fixtureSession, "need_input", parked)
	in := recombined(t, "paused", reparked, question)
	h := healthOf(in, HealthFacts{Reachable: true, Launched: true})
	if h.State != HealthParkedSilently || h.Steps[h.Lead].NodeID != "silent" {
		t.Fatalf("run = %q led by %+v, want the re-park ahead of the question", h.State, h.Steps)
	}
	if s, ok := stepState(h, "asker"); !ok || s.State != HealthWaitingOnParent {
		t.Errorf("question step = %+v, want waiting_on_parent", s)
	}
}

func TestHealth_RunningRunIsHealthy(t *testing.T) {
	t.Parallel()
	h := healthOf(overlaid(t, "running-after-answer", nil), HealthFacts{Reachable: true, Launched: true})
	if h.State != HealthHealthy || h.Lead != -1 {
		t.Errorf("run = %q lead %d, want healthy with no lead", h.State, h.Lead)
	}
	if h.Since.IsZero() {
		t.Error("a healthy run's since is its last state change, got zero")
	}
}

func TestDecodeInspect_LenientPauseFields(t *testing.T) {
	t.Parallel()
	in := overlaid(t, "parked", func(doc map[string]any) {
		stateOf(doc)["pauseDetail"] = "not an object"
		firstStep(doc)["continuationAttempts"] = "four"
	})
	if !in.OK {
		t.Fatal("a drifted pause field failed the whole reply")
	}
	if got := Unreadable(in.State); len(got) != 2 {
		t.Errorf("Unreadable = %q, want the two drifted fields", got)
	}
	if in.State.Pause.HasDetail {
		t.Error("an unparsed pauseDetail reads as present")
	}
	if asks := OpenAsks(in, RunLog{}); len(asks) != 1 || asks[0].TextSource != marotte.RunAskTextUnseen {
		t.Errorf("OpenAsks after a drifted field = %+v, want the one ask", asks)
	}
}
