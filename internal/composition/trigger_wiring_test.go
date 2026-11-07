package composition

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/trigger"
)

// buildCalls is every call Build makes in its own statements, closures skipped, by callee name ("agent.New" for a
// selector), in source order.
type buildCalls struct {
	at   map[string][]token.Pos
	args map[string][][]ast.Expr
}

func readBuildCalls(t *testing.T) buildCalls {
	t.Helper()
	src, err := os.ReadFile("composition.go")
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), "composition.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var body *ast.BlockStmt
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "Build" && fn.Recv == nil {
			body = fn.Body
		}
	}
	if body == nil {
		t.Fatal("composition.go has no func Build")
	}
	bc := buildCalls{at: map[string][]token.Pos{}, args: map[string][][]ast.Expr{}}
	ast.Inspect(body, func(n ast.Node) bool {
		if _, isClosure := n.(*ast.FuncLit); isClosure {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := calleeName(call.Fun)
		bc.at[name] = append(bc.at[name], call.Pos())
		bc.args[name] = append(bc.args[name], call.Args)
		return true
	})
	return bc
}

func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		if x, ok := f.X.(*ast.Ident); ok {
			return x.Name + "." + f.Sel.Name
		}
	}
	return ""
}

// first is the position of the one call to name, failing the test when Build makes none or several.
func (bc buildCalls) first(t *testing.T, name string) token.Pos {
	t.Helper()
	if n := len(bc.at[name]); n != 1 {
		t.Fatalf("Build calls %s %d times, want once", name, n)
	}
	return bc.at[name][0]
}

// The confirmer exists, its records loaded, before agent.New returns, so a run_start that arrives before the trigger
// runner is built still confirms; its boot and effects wait for the agent's engine, so they start once h exists.
func TestConfirmerBeforeAgentAndRunnerAfterForges(t *testing.T) {
	bc := readBuildCalls(t)
	t.Run("the confirmer is built after the lease store and handed to agent.New", func(t *testing.T) {
		leases, conf, agentNew := bc.first(t, "openRunLeaseStore"), bc.first(t, "openTriggerConfirmer"),
			bc.first(t, "agent.New")
		if leases >= conf || conf >= agentNew {
			t.Errorf("order openRunLeaseStore %d, openTriggerConfirmer %d, agent.New %d; want them in that order",
				leases, conf, agentNew)
		}
		wired := false
		for _, arg := range bc.args["agent.New"][0] {
			if call, ok := arg.(*ast.CallExpr); ok && calleeName(call.Fun) == "withTriggerConfirmer" {
				wired = true
			}
		}
		if !wired {
			t.Error("agent.New is not given withTriggerConfirmer")
		}
		if start := bc.first(t, "startTriggerConfirmer"); start <= agentNew {
			t.Errorf("startTriggerConfirmer at %d, want it after agent.New at %d", start, agentNew)
		}
	})
	t.Run("the trigger runner is built after the forges manager", func(t *testing.T) {
		manager, runner := bc.first(t, "forges.NewManager"), bc.first(t, "newTriggerRunner")
		if manager >= runner {
			t.Errorf("newTriggerRunner at %d, want it after forges.NewManager at %d", runner, manager)
		}
		args := bc.args["newTriggerRunner"][0]
		if len(args) != 5 {
			t.Fatalf("newTriggerRunner takes %d arguments, want the confirmer, the launcher, the feeds, the budget and "+
				"the config dir", len(args))
		}
		if call, ok := args[2].(*ast.CallExpr); !ok || calleeName(call.Fun) != "forges.NewFeedSource" {
			t.Error("newTriggerRunner's feeds are not forges.NewFeedSource")
		}
		budget, _ := args[3].(*ast.Ident)
		observed, _ := bc.args["forgesManager.ObserveAttempts"][0][0].(*ast.SelectorExpr)
		if budget == nil || observed == nil || calleeName(observed) != budget.Name+".Observe" {
			t.Error("the forge connections' attempt observer is not the runner's budget's Observe")
		}
		if obs, load := bc.first(t, "forgesManager.ObserveAttempts"), bc.first(t, "forgesManager.Load"); obs >= load {
			t.Errorf("ObserveAttempts at %d, want it before forgesManager.Load at %d opens the connections", obs, load)
		}
		priced, _ := bc.args["h.SetTriggerBudget"][0][0].(*ast.Ident)
		if budget == nil || priced == nil || priced.Name != budget.Name {
			t.Error("the save estimate does not price against the runner's budget")
		}
		run := bc.first(t, "startTriggerLauncher")
		if run <= runner {
			t.Errorf("startTriggerLauncher at %d, want it after newTriggerRunner at %d installed the caps", run, runner)
		}
		launcher, ok := args[1].(*ast.Ident)
		started, _ := bc.args["startTriggerLauncher"][0][1].(*ast.Ident)
		if !ok || started == nil || launcher.Name != started.Name {
			t.Error("newTriggerRunner is not given the launcher composition starts")
		}
		start := bc.first(t, "startTriggerRunner")
		if start <= runner {
			t.Errorf("startTriggerRunner at %d, want it after newTriggerRunner at %d", start, runner)
		}
		for _, arg := range bc.args["startTriggerRunner"][0] {
			if call, ok := arg.(*ast.CallExpr); ok && calleeName(call.Fun) == "prPollGate" {
				t.Error("the trigger runner is gated on presence, want it polling with nobody present")
			}
		}
	})
}

// countingLauncher counts LaunchTriggered calls, failing each before any run exists.
type countingLauncher struct {
	calls int
	mu    sync.Mutex
}

func (l *countingLauncher) LaunchTriggered(
	context.Context, string, string, string, string, map[string]string, marotte.RunDefaults,
) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	return "", errors.New("listRecipes timed out")
}

func (l *countingLauncher) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

// noEffects answers every cancel and release at once.
type noEffects struct{}

func (noEffects) CancelAbandoned(context.Context, string) error { return nil }
func (noEffects) ReleaseTriggered(context.Context, string)      {}

func TestLauncherStartsAfterEngineReady(t *testing.T) {
	t.Run("the launcher is built over the agent's runs after the confirmer starts", func(t *testing.T) {
		bc := readBuildCalls(t)
		start, build := bc.first(t, "startTriggerConfirmer"), bc.first(t, "newTriggerLauncher")
		if run := bc.first(t, "startTriggerLauncher"); start >= build || build >= run {
			t.Errorf("order startTriggerConfirmer %d, newTriggerLauncher %d, startTriggerLauncher %d; want that order",
				start, build, run)
		}
		args := bc.args["newTriggerLauncher"][0]
		if len(args) != 2 {
			t.Fatalf("newTriggerLauncher takes %d arguments, want the confirmer and the agent's runs", len(args))
		}
		if call, ok := args[1].(*ast.CallExpr); !ok || calleeName(call.Fun) != "h.Runs" {
			t.Error("newTriggerLauncher's launcher is not h.Runs()")
		}
	})
	t.Run("a queued entry launches only once the engine is ready", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			store, w, err := launchrule.NewStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			r, err := store.Create(t.Context(), &launchrule.Rule{
				Source: "bundled://weekly", Name: "weekly", Enabled: true, When: launchrule.When{Event: &launchrule.EventSpec{
					Connection: "github:github.com", Repo: "cplieger/animap", Kind: launchrule.KindRunConcluded,
					Filter: launchrule.Filter{Workflow: ".github/workflows/weekly.yaml", Branch: "main", Result: []string{"success"}},
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			entry, _ := json.Marshal(map[string]any{"key": "k1", "coalesce_key": "c1", "inputs": map[string]string{}})
			if err := w.WriteState(t.Context(), r.ID, &launchrule.State{Queue: []json.RawMessage{entry}}); err != nil {
				t.Fatal(err)
			}
			conf := trigger.NewConfirmer(store, w, runlease.NewMemory())
			ready := make(chan struct{})
			l := &countingLauncher{}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stopConf := startTriggerConfirmer(ctx, conf, ready, noEffects{})
			stopLaunch := startTriggerLauncher(ctx, newTriggerLauncher(conf, l), ready)
			synctest.Wait()
			if n := l.count(); n != 0 {
				t.Errorf("%d LaunchTriggered calls before engine ready, want none", n)
			}
			close(ready)
			synctest.Wait()
			if n := l.count(); n != 1 {
				t.Errorf("%d LaunchTriggered calls after engine ready, want one", n)
			}
			stopLaunch()
			stopConf()
		})
	})
}

// emptyFeeds answers every list with an empty, complete page, counting the calls.
type emptyFeeds struct{ calls atomic.Int32 }

func (f *emptyFeeds) OpenIssues(context.Context, trigger.Feed, forgeapi.Cursor) (forgeapi.Page[forgeapi.Issue], error) {
	f.calls.Add(1)
	return forgeapi.Page[forgeapi.Issue]{}, nil
}

func (f *emptyFeeds) OpenPRs(context.Context, trigger.Feed, forgeapi.Cursor) (forgeapi.Page[forgeapi.PullRequest], error) {
	f.calls.Add(1)
	return forgeapi.Page[forgeapi.PullRequest]{}, nil
}

func (f *emptyFeeds) Runs(context.Context, trigger.Feed, time.Time, forgeapi.Cursor) (forgeapi.Page[forgeapi.Run], error) {
	f.calls.Add(1)
	return forgeapi.Page[forgeapi.Run]{}, nil
}

func TestTriggerRunnerRunsWithNobodyPresent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store, w, err := launchrule.NewStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		r, err := store.Create(t.Context(), &launchrule.Rule{
			Source: "bundled://weekly", Name: "weekly", Enabled: true, When: launchrule.When{Event: &launchrule.EventSpec{
				Connection: "github:github.com", Repo: "cplieger/animap", Kind: launchrule.KindRunConcluded,
				Filter: launchrule.Filter{Workflow: ".github/workflows/weekly.yaml", Branch: "main", Result: []string{"success"}},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		entry, _ := json.Marshal(map[string]any{"key": "k1", "coalesce_key": "c1", "inputs": map[string]string{}})
		if err := w.WriteState(t.Context(), r.ID, &launchrule.State{Queue: []json.RawMessage{entry}}); err != nil {
			t.Fatal(err)
		}
		conf := trigger.NewConfirmer(store, w, runlease.NewMemory())
		ready := make(chan struct{})
		close(ready)
		l := &countingLauncher{}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		stopConf := startTriggerConfirmer(ctx, conf, ready, noEffects{})
		ll := newTriggerLauncher(conf, l)
		feeds := &emptyFeeds{}
		stopRunner := startTriggerRunner(ctx, newTriggerRunner(conf, ll, feeds, trigger.NewBudget(), t.TempDir()))
		stopLaunch := startTriggerLauncher(ctx, ll, ready)
		synctest.Wait()
		second := make(chan error, 1)
		go func() { second <- ll.Run(ctx, ready) }()
		synctest.Wait()
		select {
		case err := <-second:
			if err == nil {
				t.Error("a second Run of composition's launcher returned nil, want it refused while composition's runs")
			}
		default:
			t.Error("a second Run of composition's launcher is running, want composition's start the only one")
		}
		if n := l.count(); n != 1 {
			t.Errorf("%d LaunchTriggered calls for the one queued entry, with no browser session, want one", n)
		}
		if n := feeds.calls.Load(); n != 1 {
			t.Errorf("%d feed reads, with no browser session, want the rule's first poll", n)
		}
		stopLaunch()
		stopRunner()
		stopConf()
	})
}
