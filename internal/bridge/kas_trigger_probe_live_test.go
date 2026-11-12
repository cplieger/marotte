package bridge_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/bridge"
	"github.com/cplieger/marotte/internal/marotte"
)

var kasTriggerProbe = flag.Bool("kas-trigger-probe", false,
	"run TestProbeTriggeredLaunch: drive the kiro-cli named by MAROTTE_LIVE_KIRO_CLI and rewrite internal/agent/testdata/kas-probe")

const (
	probeDir      = "../agent/testdata/kas-probe"
	probeRecipe   = "trigger-probe"
	probeInvokes  = 20
	probeFrameMax = time.Minute
	probeKASDown  = 150 * time.Second
	probeResumeBy = 3 * time.Minute
)

// probeRecipeJSON is a parallel over one command watch that prints "idle" and polls every ten minutes, so a run
// stays running, spends no model credits and never ends itself.
const probeRecipeJSON = `{
  "name": "trigger-probe",
  "description": "A run that parks on a command watch until it is cancelled.",
  "inputs": {"run_id": "The id of the run that triggered this one.", "wait": "on or off"},
  "steps": [{
    "type": "parallel", "id": "run", "joinPolicy": "allSettled",
    "branches": [{
      "type": "watch", "id": "coordinator", "handler": "command",
      "config": {"command": "cat park.json", "pollIntervalSec": 600, "commandTimeoutSec": 30}
    }]
  }]
}`

// TestProbeTriggeredLaunch records what the event-trigger launch path assumes of the pinned kiro-cli: a created,
// never-invoked run is listed active; invoke's reply precedes run_start by under a minute; an engine-lane cancel
// with no initiator ends a never-invoked run and a running one; and whether KAS resumes a watch-parked run after a
// restart. It spends no model credits but starts kiro-cli, so it needs MAROTTE_LIVE_KIRO_CLI and -kas-trigger-probe.
func TestProbeTriggeredLaunch(t *testing.T) {
	p, ctx := newProbe(t)
	p.newListsActive(ctx)
	p.invokeInterval(ctx)
	p.cancelUninvoked(ctx)
	p.autoResume(ctx)
	p.c.shutdown()
}

// TestProbeReleaseOrder records whether a cancelled run's terminal frame still reaches its lane when the lease and host
// session are released right after the cancel's answer. It is its own test because it needs no 150 s restart.
func TestProbeReleaseOrder(t *testing.T) {
	p, ctx := newProbe(t)
	p.releaseOrder(ctx)
	p.c.shutdown()
}

// TestProbeMissingInput records KAS's answer to a workflow/new that leaves a declared input out: marotte cannot see
// which inputs a recipe requires, so this answer decides how a triggered launch of such a rule is classed.
func TestProbeMissingInput(t *testing.T) {
	p, ctx := newProbe(t)
	p.missingInput(ctx)
	p.c.shutdown()
}

func newProbe(t *testing.T) (*probe, context.Context) {
	t.Helper()
	if !*kasTriggerProbe {
		t.Skip("-kas-trigger-probe not set: the probe rewrites testdata")
	}
	bin, pin := liveKiroCLI(t)
	c := newCapture(t, &captureEnv{cli: bin, kiroCLI: pin})
	p := &probe{c: c, t: t, owner: map[string]marotte.SessionID{}}
	if err := os.MkdirAll(filepath.Join(c.work, ".kiro", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.work, ".kiro", "workflows", probeRecipe+".workflow.json"), []byte(probeRecipeJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.work, "park.json"), []byte(`{"outcome":"idle","cursor":0}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Minute)
	t.Cleanup(cancel)
	p.start(ctx)
	p.kas = kasVersion(t, pin)
	p.source = p.recipeSource(ctx)
	return p, ctx
}

type probeFrame struct {
	at         time.Time
	Lane       string          `json:"lane"`
	Method     string          `json:"method"`
	WorkflowID string          `json:"workflowId,omitempty"`
	Status     string          `json:"status,omitempty"`
	Params     json.RawMessage `json:"params,omitempty"`
}

type probe struct {
	t      *testing.T
	c      *capture
	e      *bridge.Engine
	cond   *sync.Cond
	frames []probeFrame
	// owner places a run's frames on its host session's lane, as dev's engine router does once the launch
	// registered the run's owner; a run with no owner row stays on the engine lane.
	owner  map[string]marotte.SessionID
	kas    string
	source string
	mu     sync.Mutex
}

func (p *probe) Route(h *bridge.FrameHead) marotte.SessionID {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.owner[h.WorkflowID]
}

func (p *probe) own(id string, s *bridge.Session) {
	p.mu.Lock()
	p.owner[id] = s.SessionID()
	p.mu.Unlock()
}

// start starts an engine whose engine lane, and every host lane opened later, feed p.frames.
func (p *probe) start(ctx context.Context) {
	p.t.Helper()
	if p.cond == nil {
		p.cond = sync.NewCond(&p.mu)
	}
	e := bridge.NewEngine(p.c.env.cli, p.c.work, bridge.WithRouter(p))
	lifetime, cancel := context.WithCancel(context.Background())
	p.c.engines = append(p.c.engines, e)
	p.c.cancels = append(p.c.cancels, cancel)
	go func() {
		for n := range e.EngineNotifCh() {
			p.note("engine", n.Msg)
			if n.Msg.ID != nil && n.Msg.Method != "" {
				_ = e.Respond(context.Background(), *n.Msg.ID, nil, &marotte.RPCError{Code: -32601, Message: "method not found"})
			}
		}
	}()
	p.c.onFrame = func(_ *bridge.Session, msg *marotte.RPCResponse) { p.note("host", msg) }
	if err := e.Start(ctx, &marotte.EngineOpts{Lifetime: lifetime, AuthArgs: []string{"--auth-method", "cli"}}); err != nil {
		p.t.Fatalf("engine start: %v", err)
	}
	p.e = e
}

func (p *probe) note(lane string, msg *marotte.RPCResponse) {
	if msg.Method == "" {
		return
	}
	var head struct {
		WorkflowID string `json:"workflowId"`
		Status     string `json:"status"`
	}
	_ = json.Unmarshal(msg.Params, &head)
	f := probeFrame{at: time.Now(), Lane: lane, Method: msg.Method, WorkflowID: head.WorkflowID, Status: head.Status}
	if strings.HasPrefix(msg.Method, "_kiro/workflow/run_") {
		f.Params = msg.Params
	}
	p.mu.Lock()
	p.frames = append(p.frames, f)
	p.cond.Broadcast()
	p.mu.Unlock()
}

// await returns the first frame from index from on that names method and id, or false after budget.
func (p *probe) await(from int, method, id string, budget time.Duration) (probeFrame, bool) {
	stop := time.AfterFunc(budget, func() {
		p.mu.Lock()
		p.cond.Broadcast()
		p.mu.Unlock()
	})
	defer stop.Stop()
	deadline := time.Now().Add(budget)
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := from; ; {
		for ; i < len(p.frames); i++ {
			if f := p.frames[i]; f.Method == method && f.WorkflowID == id {
				return f, true
			}
		}
		if time.Now().After(deadline) {
			return probeFrame{}, false
		}
		p.cond.Wait()
	}
}

func (p *probe) mark() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.frames)
}

func (p *probe) since(from int, id string) []probeFrame {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []probeFrame
	for _, f := range p.frames[min(from, len(p.frames)):] {
		if f.WorkflowID == id {
			out = append(out, f)
		}
	}
	return out
}

func (p *probe) engineCall(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	resp, err := p.e.Call(cctx, method, params)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

func (p *probe) recipeSource(ctx context.Context) string {
	p.t.Helper()
	raw, err := p.engineCall(ctx, "_kiro/workflow/listRecipes", map[string]any{"workspacePaths": []string{p.c.work}})
	if err != nil {
		p.t.Fatalf("listRecipes: %v", err)
	}
	var list struct {
		Recipes []struct {
			Name            string `json:"name"`
			Source          string `json:"source"`
			ValidationError string `json:"validationError"`
		} `json:"recipes"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		p.t.Fatalf("listRecipes reply: %v", err)
	}
	for _, r := range list.Recipes {
		if r.Name == probeRecipe {
			if r.ValidationError != "" {
				p.t.Fatalf("the probe recipe does not validate: %s", r.ValidationError)
			}
			return r.Source
		}
	}
	p.t.Fatalf("listRecipes names no %s: %s", probeRecipe, raw)
	return ""
}

// host opens a host session as dev's launchAs does.
func (p *probe) host(ctx context.Context) *bridge.Session {
	p.t.Helper()
	_, attach := p.c.attach()
	s, err := p.e.NewSession(ctx, &marotte.SessionOpts{Role: marotte.SessionRoleHost}, attach)
	if err != nil {
		p.t.Fatalf("host session: %v", err)
	}
	return s
}

// create is dev's workflowNew on a fresh host session.
func (p *probe) create(ctx context.Context) (*bridge.Session, string) {
	p.t.Helper()
	s := p.host(ctx)
	resp, err := s.Call(ctx, "_kiro/workflow/new", map[string]any{
		"workflowPath":    p.source,
		"workspacePaths":  []string{p.c.work},
		"inputs":          map[string]any{"run_id": "probe", "wait": "off"},
		"parentSessionId": string(s.SessionID()),
	})
	if err == nil && resp.Error != nil {
		err = resp.Error
	}
	if err != nil {
		p.t.Fatalf("workflow/new: %v", err)
	}
	var res struct {
		WorkflowID string `json:"workflowId"`
	}
	if json.Unmarshal(resp.Result, &res) != nil || res.WorkflowID == "" {
		p.t.Fatalf("workflow/new answered no workflowId: %s", resp.Result)
	}
	return s, res.WorkflowID
}

type listedRun struct {
	WorkflowID   string `json:"workflowId"`
	WorkflowName string `json:"workflowName"`
	Status       string `json:"status"`
}

// listed returns the run's row in workflow/list, or nil when the list does not name it.
func (p *probe) listed(ctx context.Context, id string) *listedRun {
	p.t.Helper()
	raw, err := p.engineCall(ctx, "_kiro/workflow/list", map[string]any{"workspacePaths": []string{p.c.work}})
	if err != nil {
		p.t.Fatalf("workflow/list: %v", err)
	}
	var list struct {
		Runs []listedRun `json:"runs"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		p.t.Fatalf("workflow/list reply: %v", err)
	}
	for i := range list.Runs {
		if list.Runs[i].WorkflowID == id {
			return &list.Runs[i]
		}
	}
	return nil
}

func active(r *listedRun) bool { return r != nil && marotte.RunStatus(r.Status).Active() }

func invokeRun(ctx context.Context, s *bridge.Session, id string) (time.Time, error) {
	resp, err := s.Call(ctx, "_kiro/workflow/invoke", map[string]any{"workflowId": id})
	at := time.Now()
	if err == nil && resp.Error != nil {
		err = resp.Error
	}
	return at, err
}

// cancelEngineLane is an abandoned run's cancel: engine lane, no initiator.
func (p *probe) cancelEngineLane(ctx context.Context, id string) (json.RawMessage, error) {
	return p.engineCall(ctx, "_kiro/workflow/cancel", map[string]any{"workflowId": id})
}

func (p *probe) newListsActive(ctx context.Context) {
	s, id := p.create(ctx)
	row := p.listed(ctx, id)
	pass := active(row)
	p.write("new-lists-active", map[string]any{
		"probe":     "(a) workflow/new of a recipe of example 1's shape, then workflow/list before invoke",
		"criterion": "the list names the created run with an Active() status",
		"pass":      pass,
		"listed":    row,
	})
	if !pass {
		p.t.Errorf("(a) a created, never-invoked run is not listed active: %+v", row)
	}
	_, _ = p.cancelEngineLane(ctx, id)
	_ = s.Close(ctx)
}

func (p *probe) invokeInterval(ctx context.Context) {
	type sample struct {
		WorkflowID string  `json:"workflowId"`
		Seconds    float64 `json:"seconds"`
		Lane       string  `json:"lane"`
		Error      string  `json:"error,omitempty"`
	}
	samples := make([]sample, 0, probeInvokes)
	var worst time.Duration
	pass := true
	for range probeInvokes {
		s, id := p.create(ctx)
		from := p.mark()
		replied, err := invokeRun(ctx, s, id)
		sm := sample{WorkflowID: id}
		if err != nil {
			sm.Error = err.Error()
			pass = false
		} else if f, ok := p.await(from, "_kiro/workflow/run_start", id, probeFrameMax); !ok {
			sm.Error = "no run_start within " + probeFrameMax.String()
			pass = false
		} else {
			// A run_start before the reply is a zero interval: the confirmation came first.
			d := max(f.at.Sub(replied), 0)
			sm.Seconds, sm.Lane = d.Seconds(), f.Lane
			worst = max(worst, d)
		}
		samples = append(samples, sm)
		if _, cErr := p.cancelEngineLane(ctx, id); cErr != nil {
			p.t.Logf("cancel %s: %v", id, cErr)
		}
		_ = s.Close(ctx)
	}
	pass = pass && worst <= probeFrameMax
	p.write("invoke-interval", map[string]any{
		"probe":         "(b) 20 invokes: the interval from the invoke reply to run_start",
		"criterion":     "every interval is at most one minute (launchConfirmBound, 5 minutes, rests on it)",
		"pass":          pass,
		"worst_seconds": worst.Seconds(),
		"samples":       samples,
	})
	if !pass {
		p.t.Errorf("(b) an invoke's run_start took longer than a minute or never came: worst %s", worst)
	}
}

func (p *probe) cancelOne(ctx context.Context, id string) map[string]any {
	from := p.mark()
	before := p.listed(ctx, id)
	result, err := p.cancelEngineLane(ctx, id)
	out := map[string]any{"listed_before": before}
	if err != nil {
		out["cancel_error"] = err.Error()
	} else {
		out["cancel_result"] = result
	}
	if f, ok := p.await(from, "_kiro/workflow/run_complete", id, probeFrameMax); ok {
		out["run_complete_status"], out["run_complete_lane"] = f.Status, f.Lane
	} else {
		out["run_complete_status"] = "none within " + probeFrameMax.String()
	}
	after := p.listed(ctx, id)
	out["listed_after"] = after
	out["active_after"] = active(after)
	return out
}

func (p *probe) cancelUninvoked(ctx context.Context) {
	s, id := p.create(ctx)
	if err := s.Close(ctx); err != nil {
		p.t.Fatalf("close the host session: %v", err)
	}
	uninvoked := p.cancelOne(ctx, id)

	rs, rid := p.create(ctx)
	from := p.mark()
	if _, err := invokeRun(ctx, rs, rid); err != nil {
		p.t.Fatalf("invoke: %v", err)
	}
	if _, ok := p.await(from, "_kiro/workflow/run_start", rid, probeFrameMax); !ok {
		p.t.Fatalf("no run_start for %s", rid)
	}
	running := p.cancelOne(ctx, rid)
	_ = rs.Close(ctx)
	pass := uninvoked["active_after"] == false && running["active_after"] == false
	p.write("cancel-uninvoked", map[string]any{
		"probe":     "(d) _kiro/workflow/cancel with no initiator on the engine lane: a created, never-invoked run whose host session is closed, and a running run",
		"criterion": "the answer, the terminal status, and that the run leaves the list's Active() set",
		"pass":      pass,
		"uninvoked": uninvoked,
		"running":   running,
	})
	if !pass {
		p.t.Errorf("(d) a cancelled run stays active: uninvoked %v, running %v", uninvoked, running)
	}
}

func (p *probe) releaseOrder(ctx context.Context) {
	s, id := p.create(ctx)
	p.own(id, s)
	from := p.mark()
	if _, err := invokeRun(ctx, s, id); err != nil {
		p.t.Fatalf("invoke: %v", err)
	}
	start, ok := p.await(from, "_kiro/workflow/run_start", id, probeFrameMax)
	if !ok {
		p.t.Fatalf("no run_start for %s", id)
	}
	from = p.mark()
	result, err := p.cancelEngineLane(ctx, id)
	if err != nil {
		p.t.Fatalf("cancel: %v", err)
	}
	answered := time.Now()
	// The lease release touches only marotte's state; on the wire the two steps are this close.
	early := p.since(from, id)
	closeErr := s.Close(ctx)
	closed := time.Now()
	out := map[string]any{
		"probe": "(c) cancel a running run, and on the cancel's answer release it as ReleaseTriggered does " +
			"(releaseLease, then closeBridge, which closes the run's host session) before the cancel's terminal frame",
		"criterion":             "the cancel's run_complete still arrives and the run ends aborted",
		"run_start_lane":        start.Lane,
		"cancel_result":         result,
		"frames_before_close":   early,
		"close_ms_after_answer": closed.Sub(answered).Milliseconds(),
		"terminal_frame_before_close": slices.ContainsFunc(early, func(f probeFrame) bool {
			return f.Method == "_kiro/workflow/run_complete"
		}),
	}
	if closeErr != nil {
		out["close_error"] = closeErr.Error()
	}
	f, got := p.await(from, "_kiro/workflow/run_complete", id, probeFrameMax)
	if got {
		out["run_complete_status"], out["run_complete_lane"] = f.Status, f.Lane
		out["run_complete_ms_after_answer"] = f.at.Sub(answered).Milliseconds()
	} else {
		out["run_complete_status"] = "none within " + probeFrameMax.String()
	}
	row := p.listed(ctx, id)
	out["listed_after"] = row
	pass := got && f.Status == string(marotte.RunStatusAborted) && row != nil && row.Status == string(marotte.RunStatusAborted)
	out["pass"] = pass
	p.write("release-order", out)
	if !pass {
		p.t.Errorf("(c) the release before the terminal frame lost it or the run did not end aborted: %v", out)
	}
}

func (p *probe) autoResume(ctx context.Context) {
	s, id := p.create(ctx)
	from := p.mark()
	if _, err := invokeRun(ctx, s, id); err != nil {
		p.t.Fatalf("invoke: %v", err)
	}
	if _, ok := p.await(from, "_kiro/workflow/run_start", id, probeFrameMax); !ok {
		p.t.Fatalf("no run_start for %s", id)
	}
	p.e.Stop()
	<-p.e.Done()
	stopped := time.Now()
	timer := time.NewTimer(probeKASDown)
	select {
	case <-ctx.Done():
		timer.Stop()
		p.t.Fatal(ctx.Err())
	case <-timer.C:
	}
	restartFrom := p.mark()
	p.start(ctx)
	restarted := time.Now()
	out := map[string]any{
		"probe":        "(e) a watch-parked run, KAS stopped and started again after 150 s: does auto-resume send run_start, and how long after the restart",
		"down_seconds": restarted.Sub(stopped).Seconds(),
	}
	if f, ok := p.await(restartFrom, "_kiro/workflow/run_start", id, probeResumeBy); ok {
		out["run_start"], out["run_start_lane"] = true, f.Lane
		out["seconds_after_restart"] = f.at.Sub(restarted).Seconds()
	} else {
		out["run_start"] = false
		out["waited_seconds"] = probeResumeBy.Seconds()
	}
	out["frames_after_restart"] = p.since(restartFrom, id)
	out["listed_after_restart"] = p.listed(ctx, id)
	p.write("auto-resume", out)
	if _, err := p.cancelEngineLane(ctx, id); err != nil {
		p.t.Logf("cancel %s: %v", id, err)
	}
}

func (p *probe) missingInput(ctx context.Context) {
	s := p.host(ctx)
	defer func() { _ = s.Close(ctx) }()
	resp, err := s.Call(ctx, "_kiro/workflow/new", map[string]any{
		"workflowPath":    p.source,
		"workspacePaths":  []string{p.c.work},
		"inputs":          map[string]any{"wait": "off"},
		"parentSessionId": string(s.SessionID()),
	})
	out := map[string]any{
		"probe":     "workflow/new of a recipe declaring run_id and wait, with only wait given",
		"criterion": "how a triggered launch is classed: a -32602 *marotte.RPCError blocks the rule, another error counts on the 24-hour clock, an accepted run launches",
	}
	switch {
	case err != nil:
		out["transport_error"] = err.Error()
	case resp.Error != nil:
		out["error"] = resp.Error
		out["invalid_params"] = resp.Error.Code == -32602
	default:
		var res struct {
			WorkflowID string `json:"workflowId"`
		}
		if json.Unmarshal(resp.Result, &res) != nil || res.WorkflowID == "" {
			out["result"] = resp.Result
			break
		}
		out["created"] = true
		from := p.mark()
		if _, iErr := invokeRun(ctx, s, res.WorkflowID); iErr != nil {
			out["invoke_error"] = iErr.Error()
		} else {
			_, started := p.await(from, "_kiro/workflow/run_start", res.WorkflowID, probeFrameMax)
			out["invoke_ok"], out["run_start"] = true, started
		}
		_, _ = p.cancelEngineLane(ctx, res.WorkflowID)
	}
	p.write("missing-input", out)
}

func (p *probe) write(name string, body map[string]any) {
	p.t.Helper()
	body["_meta"] = map[string]any{
		"recorded": "live", "kiroCli": p.c.env.kiroCLI, "kas": p.kas,
		"date": time.Now().UTC().Format(time.DateOnly), "regenerate": "go test ./internal/bridge/ -run " + p.t.Name() + " -kas-trigger-probe",
	}
	raw, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		p.t.Fatal(err)
	}
	out := p.c.scrub(raw)
	for _, bad := range forbiddenInCapture {
		if i := bytes.Index(out, []byte(bad)); i >= 0 {
			p.t.Fatalf("%s holds %q near %q", name, bad, excerpt(out, i))
		}
	}
	if err := os.MkdirAll(probeDir, 0o755); err != nil {
		p.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(probeDir, name+".json"), append(out, '\n'), 0o644); err != nil {
		p.t.Fatal(err)
	}
	p.t.Logf("%s: %s", name, out)
}
