package trigger

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/settings"
)

// startCapped runs tc's launcher over a new fake with the fleet caps of configDir's config.json, which the runner
// installs on it, until the test ends; the runner itself polls nothing. It runs inside synctest.Test only.
func (tc *testConfirmer) startCapped(configDir string, answer func(n int, call launchCall) (string, error)) *fakeLauncher {
	f := &fakeLauncher{tc: tc, answer: answer}
	tc.run()
	ll := NewLauncher(tc.Confirmer, f)
	NewRunner(tc.Confirmer, ll, &fakeSource{}, configDir)
	ctx, cancel := context.WithCancel(context.WithoutCancel(tc.t.Context()))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = ll.Run(ctx, tc.ready)
	}()
	tc.t.Cleanup(func() { cancel(); <-done })
	return f
}

// startsRuns answers each call as a run that starts and stays live: named, leased as a triggered run's grant
// leases it, and confirmed by its run_start.
func (tc *testConfirmer) startsRuns(n int, call launchCall) (string, error) {
	wf := fmt.Sprintf("wf_%d", n)
	err := tc.NameAttempt(call.ruleID, call.key, wf)
	tc.lease(call.ruleID, call.key, wf)
	tc.ConfirmEvent(call.ruleID, call.key, wf)
	return wf, err
}

// startsAndEnds answers each call as a run that starts and has ended by the next call: named and confirmed, its
// lease already released.
func (tc *testConfirmer) startsAndEnds(n int, call launchCall) (string, error) {
	wf := fmt.Sprintf("wf_%d", n)
	err := tc.NameAttempt(call.ruleID, call.key, wf)
	tc.ConfirmEvent(call.ruleID, call.key, wf)
	return wf, err
}

// queuedRules creates n event rules, each holding one queued event.
func (tc *testConfirmer) queuedRules(n int) []launchrule.Rule {
	rules := make([]launchrule.Rule, n)
	for i := range rules {
		rules[i] = tc.createRule(runSpec("success"))
		tc.enqueue(rules[i].ID, held("k1", "c1"))
	}
	return rules
}

// unlaunched are the rules no call was taken for.
func unlaunched(rules []launchrule.Rule, calls []launchCall) []launchrule.Rule {
	var out []launchrule.Rule
	for i := range rules {
		if !slices.ContainsFunc(calls, func(c launchCall) bool { return c.ruleID == rules[i].ID }) {
			out = append(out, rules[i])
		}
	}
	return out
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, settings.Filename), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestFleetLiveCapHolds(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		for _, wf := range []string{"wf_manual_1", "wf_manual_2"} {
			l := runlease.Lease{WorkflowID: wf, Recipe: "weekly", Origin: runlease.OriginManual, StartedAt: time.Now()}
			if err := tc.leases.Put(t.Context(), &l); err != nil {
				t.Fatal(err)
			}
		}
		rules := tc.queuedRules(4)
		f := tc.startCapped(t.TempDir(), tc.startsRuns)
		tc.letLaunch(nil)
		calls := f.taken()
		if len(calls) != 3 {
			t.Fatalf("%d calls with four rules queued, want 3, the default live cap", len(calls))
		}
		waiting := unlaunched(rules, calls)
		if len(waiting) != 1 {
			t.Fatalf("rules with no call = %d, want 1", len(waiting))
		}
		w := waiting[0].ID
		if _, st := tc.memory(w); st.RowText != "waiting: 3 triggered runs are live (the limit)" {
			t.Errorf("row text of the held rule = %q, want the live cap's", st.RowText)
		}
		if !tc.relaunchable(w, "k1") {
			t.Error("the held rule's event is no longer queued, want it held, never dropped")
		}
		advance(time.Hour)
		if n := len(f.taken()); n != 3 {
			t.Errorf("%d calls an hour on with three runs still live, want 3", n)
		}
		if err := tc.leases.Release(t.Context(), calls[0].leaseID()); err != nil {
			t.Fatal(err)
		}
		advance(capRecheck)
		if n := len(f.taken()); n != 4 {
			t.Errorf("%d calls once a live run ended, want 4", n)
		}
		if got := f.taken(); got[len(got)-1].ruleID != w {
			t.Errorf("the fourth call is for rule %s, want the held rule %s", got[len(got)-1].ruleID, w)
		}
	})
}

// leaseID is the run startsRuns leased for the call.
func (c launchCall) leaseID() string { return fmt.Sprintf("wf_%d", c.n) }

func TestFleetHourlyCapHolds(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		rules := tc.queuedRules(7)
		f := tc.startCapped(t.TempDir(), tc.startsAndEnds)
		tc.letLaunch(nil)
		calls := f.taken()
		if len(calls) != 6 {
			t.Fatalf("%d calls with seven rules queued and no run live, want 6, the default hourly cap", len(calls))
		}
		w := unlaunched(rules, calls)[0].ID
		if _, st := tc.memory(w); st.RowText != "waiting: 6 triggered runs started in the last hour (the limit)" {
			t.Errorf("row text of the held rule = %q, want the hourly cap's", st.RowText)
		}
		advance(time.Hour - time.Second)
		if n := len(f.taken()); n != 6 {
			t.Errorf("%d calls just under an hour after the six, want 6", n)
		}
		advance(time.Second)
		if n := len(f.taken()); n != 7 {
			t.Errorf("%d calls an hour after the six, want 7", n)
		}
	})
}

// The hourly cap counts runs started, each for an hour from its own start.
func TestFleetHourlyCapCountsStartedRuns(t *testing.T) {
	t.Run("a launch that created no run is not counted", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			tc.queuedRules(7)
			f := tc.startCapped(t.TempDir(), func(n int, call launchCall) (string, error) {
				if n <= 6 {
					return "", Classed(ErrLaunchRetryable, errFake)
				}
				return tc.startsAndEnds(n, call)
			})
			tc.letLaunch(nil)
			if n := len(f.taken()); n != 7 {
				t.Errorf("%d calls after six that started no run, want the seventh let through", n)
			}
		})
	})
	t.Run("a held launch goes once the oldest run is an hour old", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			tc.queuedRules(3)
			f := tc.startCapped(t.TempDir(), tc.startsAndEnds)
			tc.letLaunch(nil)
			advance(30 * time.Minute)
			for _, r := range tc.queuedRules(4) {
				tc.persist(r.ID)
			}
			synctest.Wait()
			if n := len(f.taken()); n != 6 {
				t.Fatalf("Setup: %d calls, want three at the start and three half an hour on", n)
			}
			advance(30*time.Minute - time.Second)
			if n := len(f.taken()); n != 6 {
				t.Errorf("%d calls just under an hour after the first three, want 6", n)
			}
			advance(time.Second)
			if n := len(f.taken()); n != 7 {
				t.Errorf("%d calls an hour after the first three, want the held seventh", n)
			}
		})
	})
}

func TestFleetCapsReadFromConfig(t *testing.T) {
	const configured = `{"trigger_max_live_runs": 2, "trigger_max_launches_per_hour": 4}`
	tests := []struct {
		name   string
		config string
		answer func(tc *testConfirmer) func(int, launchCall) (string, error)
		rules  int
		want   int
	}{
		{name: "a live cap of 2", config: configured, answer: liveRuns, rules: 3, want: 2},
		{name: "an hourly cap of 4", config: configured, answer: endedRuns, rules: 5, want: 4},
		{name: "absent keys hold at 3 live", config: `{"theme": "dark"}`, answer: liveRuns, rules: 4, want: 3},
		{name: "absent keys hold at 6 an hour", config: `{"theme": "dark"}`, answer: endedRuns, rules: 7, want: 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inBubble(t, func(t *testing.T, tc *testConfirmer) {
				tc.queuedRules(tt.rules)
				f := tc.startCapped(writeConfig(t, tt.config), tt.answer(tc))
				tc.letLaunch(nil)
				if n := len(f.taken()); n != tt.want {
					t.Errorf("%d calls with %d rules queued, want %d", n, tt.rules, tt.want)
				}
			})
		})
	}
}

func liveRuns(tc *testConfirmer) func(int, launchCall) (string, error)  { return tc.startsRuns }
func endedRuns(tc *testConfirmer) func(int, launchCall) (string, error) { return tc.startsAndEnds }
