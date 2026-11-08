package trigger

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/schedule"
)

// The two example rules: a Weekly success on animap-source launching the weekly questions, and an animap issue
// gaining report:fast launching the report run.
const (
	weeklyRepo   = "cplieger/animap-source"
	weeklyRecipe = "animap-weekly-questions"
	reportRecipe = "animap-report"
	validLabel   = "report:valid"
	ciWorkflow   = ".github/workflows/ci.yaml"
)

func weeklySpec() *launchrule.EventSpec {
	return &launchrule.EventSpec{
		Connection: testConn, Repo: weeklyRepo, Kind: launchrule.KindRunConcluded, IntervalMin: 5,
		Filter: launchrule.Filter{Workflow: testWorkflow, Branch: "main", Result: []string{"success"}},
		Hold:   &launchrule.Hold{Policy: launchrule.HoldNewest, Cap: 10},
	}
}

func reportSpec() *launchrule.EventSpec {
	return &launchrule.EventSpec{
		Connection: testConn, Repo: testRepo, Kind: launchrule.KindIssueLabeled, IntervalMin: 5,
		Filter: launchrule.Filter{Label: testLabel},
		Hold:   &launchrule.Hold{Policy: launchrule.HoldFIFO, Cap: 20},
	}
}

// weekly is a run of the Weekly workflow on main, started by its schedule.
func weekly(id int64, state forgeapi.CheckState, created time.Time) forgeapi.Run {
	r := run(id, state, created)
	r.Trigger = forgeapi.TriggerSchedule
	return r
}

// report is an open animap issue whose title and body are canaries no input may carry.
func report(number int, labels ...string) forgeapi.Issue {
	is := issue(number, "alice", labels...)
	is.Title, is.Body = "canary-title", "canary-body"
	return is
}

// acceptance runs one marotte process at a time over a fake forge and a fake engine, wired as composition wires
// them: the Runner polls, the launcher launches and the Confirmer's Run cancels and releases. kill ends a process
// as an exit does; restart loads the next one from the files and leases the last one left.
type acceptance struct {
	t         *testing.T
	tc        *testConfirmer
	src       *fakeSource
	eng       *engine
	proc      *process
	configDir string
	// runs and issues are the forge's, read by every poll under src.mu.
	runs   []forgeapi.Run
	issues []forgeapi.Issue
}

type process struct {
	cancel context.CancelFunc
	ready  chan struct{}
	wg     sync.WaitGroup
}

// inAcceptance runs f inside a synctest bubble, so every bound, backoff and poll interval runs on its clock.
func inAcceptance(t *testing.T, f func(t *testing.T, a *acceptance)) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		tc := newTestConfirmer(t)
		a := &acceptance{t: t, tc: tc, src: &fakeSource{}, configDir: t.TempDir()}
		a.src.forge, a.src.open = &a.runs, &a.issues
		a.eng = &engine{tc: tc, runs: map[string]*engineRun{}, live: map[string]string{}}
		t.Cleanup(func() {
			if a.proc != nil {
				a.kill()
			}
		})
		f(t, a)
	})
}

func (a *acceptance) rule(name string, inputs map[string]string, ev *launchrule.EventSpec) launchrule.Rule {
	a.t.Helper()
	r, err := a.tc.store.Create(a.t.Context(), &launchrule.Rule{
		Source: "bundled://" + name, Name: name, Enabled: true, Inputs: inputs, When: launchrule.When{Event: ev},
	})
	if err != nil {
		a.t.Fatalf("Setup: Create: %v", err)
	}
	return r
}

func (a *acceptance) weeklyRule() launchrule.Rule {
	return a.rule(weeklyRecipe, map[string]string{"wait": "off"}, weeklySpec())
}

func (a *acceptance) reportRule() launchrule.Rule { return a.rule(reportRecipe, nil, reportSpec()) }

// boot starts a process whose engine is ready at once; its first poll runs before boot returns.
func (a *acceptance) boot() {
	a.start()
	a.engineReady()
}

// start starts a process over the Confirmer the last restart loaded, its engine not ready until engineReady.
func (a *acceptance) start() {
	ctx, cancel := context.WithCancel(context.WithoutCancel(a.t.Context()))
	p := &process{cancel: cancel, ready: make(chan struct{})}
	c := a.tc.Confirmer
	ll := NewLauncher(c, a.eng)
	rn := NewRunner(c, ll, a.src, NewBudget(), a.configDir)
	p.wg.Go(func() { c.Run(ctx, p.ready, a.eng) })
	p.wg.Go(func() { _ = ll.Run(ctx, p.ready) })
	p.wg.Go(func() { rn.Run(ctx) })
	a.proc = p
	synctest.Wait()
}

func (a *acceptance) engineReady() {
	close(a.proc.ready)
	synctest.Wait()
}

// kill ends the process: its goroutines stop, and a launch it had in flight never returns to it.
func (a *acceptance) kill() {
	a.proc.cancel()
	a.proc.wg.Wait()
	a.proc = nil
}

// restart kills the process and loads the next one's Confirmer; boot or start runs it.
func (a *acceptance) restart() {
	a.kill()
	a.tc.restart()
}

// setRun puts r on the forge, replacing the run of its id.
func (a *acceptance) setRun(r forgeapi.Run) {
	a.src.mu.Lock()
	defer a.src.mu.Unlock()
	if i := slices.IndexFunc(a.runs, func(x forgeapi.Run) bool { return x.Ref.ID == r.Ref.ID }); i >= 0 {
		a.runs[i] = r
		return
	}
	a.runs = append(a.runs, r)
}

// setIssues makes items the forge's open issues.
func (a *acceptance) setIssues(items ...forgeapi.Issue) {
	a.src.mu.Lock()
	defer a.src.mu.Unlock()
	a.issues = slices.Clone(items)
}

// awaitLaunches moves the clock on a second at a time until n launches created a run, at most two poll intervals,
// and answers them.
func (a *acceptance) awaitLaunches(n int) []launch {
	a.t.Helper()
	for range 2 * 5 * 60 {
		if got := a.eng.launches(); len(got) >= n {
			return got
		}
		advance(time.Second)
	}
	got := a.eng.launches()
	a.t.Fatalf("launches = %+v, want %d within two poll intervals", got, n)
	return got
}

// advanceTo moves the bubble's clock on to at.
func advanceTo(at time.Time) { advance(time.Until(at)) }

// view is the rule's state as its row shows it.
func (a *acceptance) view(ruleID string) launchrule.State {
	a.t.Helper()
	st, err := a.tc.View(ruleID)
	if err != nil {
		a.t.Fatalf("View(%s): %v", ruleID, err)
	}
	return st
}

// launchStep is where a launch's process dies, the zero value running the launch to its end.
type launchStep int

const (
	runsToEnd launchStep = iota
	// diesAfterNew: workflow/new created the run and nothing named it.
	diesAfterNew
	// diesAfterName: the attempt is written and the lease is not.
	diesAfterName
	// diesAfterLease: the lease is written and the invoke never reached KAS.
	diesAfterLease
)

// launchPlan is how one launch that creates a run goes; the zero plan names, leases and invokes the run, which
// reports its run_start before the invoke's nil reply.
type launchPlan struct {
	reply     error
	dies      launchStep
	dropStart bool
	// leaseFails is a lease write that failed: no invoke, and ErrLeaseNotGranted.
	leaseFails bool
}

// engine stands in for KAS and the agent behind LaunchTriggered and the Confirmer's effects. One run holds a
// recipe at a time: a launch while one does answers BusyError naming it. Each launch hands over the run list first,
// as the recipe check does, then creates wf_<n> and goes as its plan says.
type engine struct {
	cancelErr error
	tc        *testConfirmer
	runs      map[string]*engineRun
	// live is the run holding each recipe, by source.
	live map[string]string
	// plans are the next launches' plans in order; rest is every later one's.
	plans   []launchPlan
	rest    launchPlan
	calls   []launchCall
	created []launch
	cancels []string
	n       int
	mu      sync.Mutex
}

type engineRun struct {
	ruleID, key, source string
	ended, started      bool
}

var (
	_ Launcher = (*engine)(nil)
	_ Effects  = (*engine)(nil)
)

func (e *engine) LaunchTriggered(
	ctx context.Context, source, ruleID, eventKey, coalesceKey string, inputs map[string]string, _ marotte.RunDefaults,
) (string, error) {
	e.tc.PruneAbandoned(ruleID, e.statuses())
	e.mu.Lock()
	e.calls = append(e.calls, launchCall{
		at: time.Now(), ruleID: ruleID, key: eventKey, coalesce: coalesceKey, inputs: maps.Clone(inputs),
		n: len(e.calls) + 1,
	})
	if wf := e.live[source]; wf != "" {
		e.mu.Unlock()
		return "", &schedule.BusyError{WorkflowID: wf}
	}
	e.n++
	wf := fmt.Sprintf("wf_%d", e.n)
	e.runs[wf], e.live[source] = &engineRun{ruleID: ruleID, key: eventKey, source: source}, wf
	e.created = append(e.created, launch{launchCall: e.calls[len(e.calls)-1], wf: wf})
	plan := e.rest
	if len(e.plans) > 0 {
		plan, e.plans = e.plans[0], e.plans[1:]
	}
	e.mu.Unlock()
	if plan.dies == diesAfterNew {
		<-ctx.Done()
		return "", ctx.Err()
	}
	if err := e.tc.NameAttempt(ruleID, eventKey, wf); err != nil {
		return wf, err
	}
	if plan.dies == diesAfterName {
		<-ctx.Done()
		return "", ctx.Err()
	}
	if plan.leaseFails {
		return wf, ErrLeaseNotGranted
	}
	e.tc.lease(ruleID, eventKey, wf)
	if plan.dies == diesAfterLease {
		<-ctx.Done()
		return "", ctx.Err()
	}
	if !plan.dropStart {
		e.start(wf)
	}
	return wf, plan.reply
}

// CancelAbandoned ends the run unless cancelErr refuses it, and the run's terminal frame releases its lease.
func (e *engine) CancelAbandoned(ctx context.Context, workflowID string) error {
	e.mu.Lock()
	e.cancels = append(e.cancels, workflowID)
	if err := e.cancelErr; err != nil {
		e.mu.Unlock()
		return err
	}
	e.endLocked(workflowID)
	e.mu.Unlock()
	_ = e.tc.leases.Release(ctx, workflowID)
	return nil
}

func (e *engine) ReleaseTriggered(ctx context.Context, workflowID string) {
	_ = e.tc.leases.Release(ctx, workflowID)
}

func (e *engine) endLocked(workflowID string) {
	r := e.runs[workflowID]
	if r == nil {
		return
	}
	r.ended = true
	if e.live[r.source] == workflowID {
		delete(e.live, r.source)
	}
}

// finish ends the run on its own terms: its lease goes, and its ending reaches its rule's row.
func (e *engine) finish(workflowID string) {
	e.mu.Lock()
	r := e.runs[workflowID]
	e.endLocked(workflowID)
	e.mu.Unlock()
	_ = e.tc.leases.Release(e.tc.t.Context(), workflowID)
	if r != nil && r.ruleID != "" {
		e.tc.RecordEnding(r.ruleID, workflowID, launchrule.Outcome{Status: launchrule.StatusCompleted})
	}
}

// start delivers the run's run_start.
func (e *engine) start(workflowID string) {
	e.mu.Lock()
	r := e.runs[workflowID]
	r.started = true
	e.mu.Unlock()
	e.tc.ConfirmEvent(r.ruleID, r.key, workflowID)
}

func (e *engine) started(workflowID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	r := e.runs[workflowID]
	return r != nil && r.started
}

// handRun is a person's run of the recipe, live with a manual lease.
func (e *engine) handRun(recipe, workflowID string) {
	source := "bundled://" + recipe
	e.mu.Lock()
	e.runs[workflowID], e.live[source] = &engineRun{source: source}, workflowID
	e.mu.Unlock()
	e.tc.manualLease(workflowID)
}

// statuses is the run list: every run not yet ended, running.
func (e *engine) statuses() map[string]marotte.RunStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[string]marotte.RunStatus{}
	for wf, r := range e.runs {
		if !r.ended {
			out[wf] = marotte.RunStatusRunning
		}
	}
	return out
}

func (e *engine) plan(plans ...launchPlan) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.plans = append(e.plans, plans...)
}

func (e *engine) planRest(p launchPlan) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rest = p
}

func (e *engine) refuseCancels(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cancelErr = err
}

// launch is a call that created run wf.
type launch struct {
	wf string
	launchCall
}

// launches are the calls that created a run, in order.
func (e *engine) launches() []launch {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.created)
}

// taken is every call, a busy recipe's refusals included.
func (e *engine) taken() []launchCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.calls)
}

// vanish ends the run with nothing reported, as a run KAS ended on its own leaves the run list.
func (e *engine) vanish(workflowID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.endLocked(workflowID)
}

// liveRun is the run holding the recipe, empty when none does.
func (e *engine) liveRun(recipe string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.live["bundled://"+recipe]
}

func (e *engine) cancelled() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.cancels)
}
