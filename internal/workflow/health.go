package workflow

import (
	"strconv"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// HealthState is how a run or one of its steps stands for the chat that launched it.
type HealthState string

// The states, run-level and step-level. A step state applies to a leaf; HealthAtCap and HealthHealing apply to a
// run only, and HealthPaused to either.
const (
	HealthHealthy         HealthState = "healthy"
	HealthParkedSilently  HealthState = "parked_silently"
	HealthEnginePaused    HealthState = "engine_paused"
	HealthHung            HealthState = "hung"
	HealthUnfinishedStep  HealthState = "unfinished_step"
	HealthWaitingOnParent HealthState = "waiting_on_parent"
	HealthWaitingOnPerson HealthState = "waiting_on_person"
	HealthPaused          HealthState = "paused"
	HealthAtCap           HealthState = "at_cap"
	HealthHealing         HealthState = "healing"
	HealthUnknown         HealthState = "unknown"
)

// stepPrecedence orders the step states a run reads when no run-level state takes it: first what the parent must
// act on and nobody else will.
var stepPrecedence = []HealthState{
	HealthParkedSilently, HealthEnginePaused, HealthHung, HealthUnfinishedStep,
	HealthWaitingOnParent, HealthWaitingOnPerson, HealthPaused,
}

// Precedence is s's rank in the step precedence, lower first; len(precedence) for a state outside it.
func (s HealthState) Precedence() int {
	for i, p := range stepPrecedence {
		if p == s {
			return i
		}
	}
	return len(stepPrecedence)
}

// UnfinishedKind is why a step KAS counted completed may have left its work unfinished.
type UnfinishedKind string

// The unfinished ends marotte grades (noteGradedStepEnd's three arms).
const (
	UnfinishedEmptyAnswer    UnfinishedKind = "empty_answer"
	UnfinishedModelCallLimit UnfinishedKind = "model_call_limit"
	UnfinishedRefused        UnfinishedKind = "refused"
)

// OpenCall is a step's oldest own-lane tool call still open.
type OpenCall struct {
	StartedAt time.Time
	Title     string
	Kind      string
}

// StepGrade is a step's newest unfinished grade. Told: a wake that carried it settled. Superseded: a newer turn
// opened on its path.
type StepGrade struct {
	At         time.Time
	Kind       UnfinishedKind
	TurnID     string
	Told       bool
	Superseded bool
}

// StepFacts is what marotte's run log holds of one step path's newest turn.
type StepFacts struct {
	// PausedAt is the step's node_paused, zero when none since its last turn_start.
	PausedAt time.Time
	// LastAt is the step's last activity, zero when the run log holds none.
	LastAt   time.Time
	OpenCall *OpenCall
	Grade    *StepGrade
	// TransientFailure is the engine error code of a turn that ended on a transient failure, "" for none.
	TransientFailure string
	// CountsTowardSilence: the step's open turn is model work, neither parked nor waiting on an ask.
	CountsTowardSilence bool
}

// HealthFacts is what marotte holds of a run beside KAS's tree, the second input of Health.
type HealthFacts struct {
	AutoResumeStoppedAt time.Time
	// HealingSince is when marotte's heal or restart sweep took the run, zero when neither is taking it.
	HealingSince time.Time
	// Steps is keyed by the step's PathKey.
	Steps map[string]StepFacts
	// Asks is the run's open asks from the same reply (OpenAsks).
	Asks    []Ask
	HealTry int
	// Reachable is false while the ask cell's newest read failed or it holds none; ReadFailed tells the first, and
	// Gone a read kiro-cli answered with no such run.
	Reachable  bool
	ReadFailed bool
	Gone       bool
	// Launched: the run has a launching chat.
	Launched bool
	// InterruptedPause: the run's pause reason is one a restart writes (marotte's resumablePause).
	InterruptedPause bool
}

// DefaultHungAfter is the agent's silence threshold. Shell calls run up to the shell tool's 30-minute ceiling and
// never past 31 minutes in 512,294 measured calls, while every longer gap inside a counted step turn was a hung
// search, another stuck call or a stalled model response, so 35 minutes flags no live shell call.
const DefaultHungAfter = 35 * time.Minute

// StepHealth is one step's state. Key is the discriminator of the state entry: it changes only with a new
// occurrence ("" when the entry has none and its first sight dates it).
type StepHealth struct {
	Since    time.Time
	LastAt   time.Time
	OpenCall *OpenCall
	State    HealthState
	// Why is one sentence after "step <node> "; Quote is the step's words a line may quote, raw and untrusted.
	Why     string
	Quote   string
	Key     string
	AskID   string
	PathKey string
	NodeID  string
	Agent   string
	Status  string
	Grade   UnfinishedKind
	Path    []string
}

// RunHealth is a run's state and its steps that are not healthy or are running. Lead indexes the step whose state
// the run reads (-1 when the run's state is its own); Owner is set for at_cap, healing and a run-level paused, whose
// owner is someone other than the launching chat, so no step state of the run calls on the chat.
type RunHealth struct {
	Since time.Time
	State HealthState
	Why   string
	Key   string
	// Status is KAS's run status, verbatim.
	Status string
	Steps  []StepHealth
	Lead   int
	Owner  bool
}

// parentPauseReason is KAS's stopReason for a pause the owning parent made through update_workflow.
const parentPauseReason = "Pause requested by owning parent/orchestrator; human intent not verified."

// questionPauseReason is KAS's pauseReason for a step that asked through send_message.
const questionPauseReason = "Step requested user input via send_message."

// continuationCap is kiro-cli's continuation budget plus one, the value a spent budget leaves on the node.
const continuationCap = 4

const (
	sourceStatusUpdate = "status_update"
	signalNeedInput    = "need_input"
)

// Health decides how a run stands, in one order: unknown when no successful read is held; for a paused run the
// owner states at_cap, healing and paused; then a paused run's own pause record; then the first step state by
// Precedence; then, for a paused run, engine_paused, so no paused run reads healthy. Pure: no clock, no I/O.
func Health(in Inspect, f *HealthFacts, now time.Time) RunHealth {
	if !in.OK || in.State == nil {
		why := "marotte has not read this run since it started"
		switch {
		case f.Gone:
			why = "kiro-cli reports no such run"
		case f.ReadFailed:
			why = "marotte cannot read this run"
		}
		return RunHealth{State: HealthUnknown, Lead: -1, Why: why}
	}
	s := in.State
	h := RunHealth{Status: s.Status, Lead: -1, Steps: stepsHealth(in, f, now)}
	if !f.Reachable {
		h.State, h.Why = HealthUnknown, "marotte cannot read this run"
		return h
	}
	paused := s.Status == statusPaused
	if paused && ownerState(s, f, &h) {
		return h
	}
	if paused && pauseRecordState(s, f, &h) {
		return h
	}
	best := len(stepPrecedence)
	for i := range h.Steps {
		if p := h.Steps[i].State.Precedence(); p < best {
			best, h.Lead = p, i
		}
	}
	if h.Lead >= 0 {
		st := &h.Steps[h.Lead]
		h.State, h.Since, h.Why, h.Key = st.State, st.Since, "step "+st.NodeID+" "+st.Why, st.Key
		return h
	}
	if paused {
		h.State, h.Why = HealthEnginePaused, "paused, and nothing marotte knows of will resume it"
		return h
	}
	h.State = HealthHealthy
	h.Since = parseTime(s.UpdatedAt())
	return h
}

// ownerState sets h to the paused run's owner state, reporting whether one applies.
func ownerState(s *State, f *HealthFacts, h *RunHealth) bool {
	p := &s.Pause
	switch {
	case p.CapKind == "maxIterations":
		h.State, h.Why = HealthAtCap, "a repeat reached its iteration cap"
	case !f.HealingSince.IsZero():
		h.State, h.Since = HealthHealing, f.HealingSince
		h.Why = "marotte is resuming it (try " + strconv.Itoa(max(f.HealTry, 1)) + " of 3)"
	case p.StopInitiator == "user" || p.PendingInitiator == "user":
		h.State, h.Why = HealthPaused, "paused by a person"
		if p.StopReason == parentPauseReason || p.PendingReason == parentPauseReason {
			h.Why = "paused by its parent"
		}
	default:
		return false
	}
	h.Owner = true
	return true
}

// pauseRecordState sets h to engine_paused for a paused run whose own record, or marotte's stop of its automatic
// resumes, explains the pause, reporting whether one does.
func pauseRecordState(s *State, f *HealthFacts, h *RunHealth) bool {
	p := &s.Pause
	switch {
	case p.Unreadable:
		h.Why = "kiro-cli's pause record could not be read"
	case p.HasDetail:
		h.Why, h.Key = detailWhy(p, f), p.DetailAt
		h.Since = parseTime(p.DetailAt)
	case !f.AutoResumeStoppedAt.IsZero():
		h.Why = "marotte stopped resuming it: it kept pausing with no progress between resumes"
	case f.InterruptedPause:
		h.Why = "a restart paused it and marotte's resume did not take it"
	default:
		return false
	}
	if h.Key == "" {
		h.Key = stamp(f.AutoResumeStoppedAt)
	}
	if h.Since.IsZero() {
		h.Since = f.AutoResumeStoppedAt
	}
	h.State = HealthEnginePaused
	return true
}

func detailWhy(p *RunPause, f *HealthFacts) string {
	switch p.DetailClass {
	case "transient-error":
		why := "a step failed on " + p.DetailCode + " after kiro-cli's own retries"
		if !f.AutoResumeStoppedAt.IsZero() {
			why += ", and marotte stopped resuming it: it kept pausing with no progress between resumes"
		}
		return why
	case "continuation-exhausted":
		return "kiro-cli stopped continuing a step after three turns with no signal"
	case "parent-authority":
		return "the parent session lost authority over the run"
	}
	return "kiro-cli paused it (" + p.DetailClass + ")"
}

// stepsHealth classifies every leaf, listing the ones that are not healthy or are running.
func stepsHealth(in Inspect, f *HealthFacts, now time.Time) []StepHealth {
	c := stepClassifier{s: in.State, f: f, now: now, sole: len(f.Asks) == 1}
	c.asks = make(map[string]*Ask, len(f.Asks))
	for i := range f.Asks {
		c.asks[PathKey(f.Asks[i].NodePath)] = &f.Asks[i]
	}
	var out []StepHealth
	for _, v := range nodes(in.State) {
		n := v.n
		if len(n.Children) > 0 || isContainer(n.Type) {
			continue
		}
		key := PathKey(v.path)
		facts := f.Steps[key]
		st := StepHealth{
			Path: v.path, PathKey: key, NodeID: n.NodeID, Agent: n.AgentName, Status: n.Status,
			LastAt: facts.LastAt, OpenCall: facts.OpenCall, State: HealthHealthy,
		}
		c.classify(&st, n, &facts, c.asks[key])
		if st.State != HealthHealthy || n.Status == statusRunning || st.Why != "" {
			out = append(out, st)
		}
	}
	return out
}

func isContainer(t string) bool {
	return t == "sequence" || t == "parallel" || t == nodeTypeRepeat
}

// stepClassifier holds what every leaf of one run is classified against.
type stepClassifier struct {
	now  time.Time
	s    *State
	f    *HealthFacts
	asks map[string]*Ask
	sole bool
}

// classify sets st's state. Question or re-park is read from the node's completion signal, which survives a
// restart, never from the ask's text source, which records only where marotte found the words this boot.
func (c *stepClassifier) classify(st *StepHealth, n *node, facts *StepFacts, ask *Ask) {
	runPaused := c.s.Status == statusPaused
	switch r := requestWait(n); {
	case r != nil:
		st.State, st.Since, st.Why = HealthWaitingOnPerson, parseTime(r.Since), "holds a permission or input request for a person"
	case n.Status == statusPaused && n.parkWait() != nil && ask != nil:
		c.parkedStep(st, n, ask, runPaused)
	case n.Status == statusPaused && n.parkWait() == nil && n.CompletionSignal == "" && !runPaused:
		st.State, st.Since, st.Why = HealthEnginePaused, facts.PausedAt, enginePausedWhy(n, facts)
		st.Key = stamp(facts.PausedAt)
	case facts.Grade != nil && !facts.Grade.Told && !facts.Grade.Superseded:
		g := facts.Grade
		st.State, st.Since, st.Key, st.Grade, st.Why = HealthUnfinishedStep, g.At, g.TurnID, g.Kind, unfinishedWhy(g.Kind)
	case c.hung(n, facts):
		st.State, st.Since, st.Key = HealthHung, facts.LastAt, stamp(facts.LastAt)
		st.Why = "has shown no activity for " + strconv.Itoa(int(c.now.Sub(facts.LastAt)/time.Minute)) + " minutes"
		if oc := facts.OpenCall; oc != nil {
			st.Why += "; its open call: " + oc.Title
		}
	}
}

func (c *stepClassifier) hung(n *node, facts *StepFacts) bool {
	return n.Status == statusRunning && facts.CountsTowardSilence && !facts.LastAt.IsZero() &&
		c.now.Sub(facts.LastAt) >= DefaultHungAfter
}

// stamp is t as a state entry's discriminator, "" for zero.
func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func (c *stepClassifier) parkedStep(st *StepHealth, n *node, ask *Ask, runPaused bool) {
	st.AskID, st.Key, st.Since = ask.ID, ask.ID, parseTime(n.parkWait().Since)
	switch {
	case n.CompletionSignal == signalSuccess || n.CompletionSignal == signalError:
		if !runPaused {
			st.Why = "finished (" + n.CompletionSignal + "); kiro-cli holds its branch until its siblings finish (kirodotdev/Kiro#12281)"
		}
		st.Key = ""
		return
	case n.CompletionSignal == signalNeedInput && n.signalSource() == sourceStatusUpdate:
		st.State, st.Why = HealthPaused, "was paused by its parent or a person"
	case n.CompletionSignal == signalNeedInput || (n.CompletionSignal == "" && c.sole && c.s.PauseReason == questionPauseReason):
		st.State, st.Why = HealthWaitingOnPerson, "asked a question"
		if c.f.Launched {
			st.State, st.Why = HealthWaitingOnParent, "asked its launching chat a question"
		}
		if ask.TextSource == marotte.RunAskTextAsked {
			st.Quote = ask.Text
		}
	default:
		st.State, st.Why, st.Quote = HealthParkedSilently, "ended a turn with no signal", ask.Text
	}
	if ask.Wait == marotte.RunAskBranchRunning {
		st.Why += "; it cannot be answered while a sibling runs; end it with update_workflow update_status, or wait"
	}
}

func enginePausedWhy(n *node, facts *StepFacts) string {
	cause := "kiro-cli paused it and recorded no reason"
	switch {
	case facts.TransientFailure != "":
		cause = "its turn failed on " + facts.TransientFailure + " after kiro-cli's own retries"
	case n.continuations() >= continuationCap:
		cause = "kiro-cli stopped continuing it after three turns with no signal"
	}
	return cause + "; kiro-cli holds the branch until its siblings finish"
}

func unfinishedWhy(k UnfinishedKind) string {
	switch k {
	case UnfinishedRefused:
		return "was declined by the model; change its prompt or model, then re-run it with update_workflow replace_remaining"
	case UnfinishedModelCallLimit:
		return "stopped at its model-call limit; re-run it with update_workflow replace_remaining"
	}
	return "ended on an empty model answer; re-run it with update_workflow replace_remaining"
}

// requestWait is a node's open permission or input request, the attention entry that carries a toolCallId.
func requestWait(n *node) *attentionEntry {
	for i := range n.Attention {
		if n.Attention[i].ToolCallID != "" {
			return &n.Attention[i]
		}
	}
	return nil
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
