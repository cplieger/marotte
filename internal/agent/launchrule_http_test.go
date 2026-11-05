package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
)

// fakeEventRuleChecker answers every save-time forge check with err, counting the checks.
type fakeEventRuleChecker struct {
	err    error
	checks []launchrule.EventSpec
	mu     sync.Mutex
}

func (f *fakeEventRuleChecker) CheckEventRule(_ context.Context, ev *launchrule.EventSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks = append(f.checks, *ev)
	return f.err
}

func (f *fakeEventRuleChecker) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.checks)
}

// launchRuleHub is a runtime listing bundled://publish over a launch-rule store in dir, its forge checks
// answered by forge, with the run routes mounted.
func launchRuleHub(t *testing.T, forge *fakeEventRuleChecker) (dir string, st *launchrule.Store, mux *http.ServeMux) {
	t.Helper()
	return launchRuleHubListing(t, forge, "")
}

// launchRuleHubListing is launchRuleHub with the recipe listing replaced by listing when it is not empty.
func launchRuleHubListing(t *testing.T, forge *fakeEventRuleChecker, listing string) (dir string, st *launchrule.Store, mux *http.ServeMux) {
	t.Helper()
	h, _, br := newTestHub()
	launchableRecipe(br, "wf_rule")
	if listing != "" {
		br.setCallResult(methodKiroWorkflowListRecipes, json.RawMessage(listing))
	}
	dir = t.TempDir()
	st, err := launchrule.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h.runs.launchRules = st
	h.SetEventRuleChecker(forge)
	mux = http.NewServeMux()
	(&runRoutes{runs: h.runs, epoch: h.Epoch}).register(mux)
	return dir, st, mux
}

func sendRoute(t *testing.T, mux *http.ServeMux, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader(body)))
	return rec
}

func rulesFile(t *testing.T, dir string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "launch-rules.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read launch-rules.json: %v", err)
	}
	return raw
}

func ruleState(t *testing.T, dir, id string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "launch-state", id+".json"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read the state of %s: %v", id, err)
	}
	return raw
}

const runRuleBody = `{"source":"bundled://publish","enabled":true,"when":{"event":{"connection":"github:github.com",` +
	`"repo":"cplieger/animap-source","kind":"run_concluded","filter":{"result":["success"]}}}}`

func eventRuleOf(repo string) *launchrule.Rule {
	return &launchrule.Rule{Source: "bundled://publish", Name: "publish", Enabled: true, When: launchrule.When{
		Event: &launchrule.EventSpec{
			Connection: "github:github.com", Repo: repo, Kind: launchrule.KindRunConcluded,
			Filter: launchrule.Filter{Result: []string{"success"}},
		},
	}}
}

// TestRegisterLaunchRules_MountsTheSurfaceOnlyWithAStoreBehindIt pins that without a store the handlers would
// panic; with one, skipping them would 404 a working feature.
func TestRegisterLaunchRules_MountsTheSurfaceOnlyWithAStoreBehindIt(t *testing.T) {
	t.Run("a configured store gets the routes", func(t *testing.T) {
		st, err := launchrule.NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("launchrule.NewStore: %v", err)
		}
		mux := http.NewServeMux()
		(&runRoutes{runs: &Runs{launchRules: st}}).register(mux)
		if rec := sendRoute(t, mux, http.MethodGet, "/api/launch-rules", ""); rec.Code != http.StatusOK {
			t.Errorf("GET /api/launch-rules = %d, want 200: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("no store means no routes, not broken ones", func(t *testing.T) {
		mux := http.NewServeMux()
		(&runRoutes{runs: &Runs{}}).register(mux)
		if rec := sendRoute(t, mux, http.MethodGet, "/api/launch-rules", ""); rec.Code != http.StatusNotFound {
			t.Errorf("GET /api/launch-rules = %d with no store, want 404: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("the schedule routes are gone", func(t *testing.T) {
		_, _, mux := launchRuleHub(t, &fakeEventRuleChecker{})
		if rec := sendRoute(t, mux, http.MethodGet, "/api/schedules", ""); rec.Code != http.StatusNotFound {
			t.Errorf("GET /api/schedules = %d, want 404", rec.Code)
		}
	})
}

type ruleListView struct {
	Rules []struct {
		NextRunAt *time.Time         `json:"next_run_at"`
		Status    *launchrule.Status `json:"status"`
		launchrule.Rule
		Error string `json:"error"`
		Held  int    `json:"held"`
	} `json:"rules"`
}

func listRules(t *testing.T, mux *http.ServeMux) ruleListView {
	t.Helper()
	rec := sendRoute(t, mux, http.MethodGet, "/api/launch-rules", "")
	var out ruleListView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("GET /api/launch-rules = %d %s", rec.Code, rec.Body.String())
	}
	return out
}

func TestLaunchRulesRoute(t *testing.T) {
	forge := &fakeEventRuleChecker{}
	dir, st, mux := launchRuleHub(t, forge)
	const clock = `{"source":"bundled://publish","enabled":true,"when":{"clock":{"freq":"daily","hour":3,"minute":0}}}`
	rec := sendRoute(t, mux, http.MethodPost, "/api/launch-rules", clock)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/launch-rules = %d %s, want 200", rec.Code, rec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("POST answered %s, want the created rule with its minted id", rec.Body.String())
	}
	t.Run("a second post adds a second rule for the recipe", func(t *testing.T) {
		if rec := sendRoute(t, mux, http.MethodPost, "/api/launch-rules", runRuleBody); rec.Code != http.StatusOK {
			t.Fatalf("POST event rule = %d %s, want 200", rec.Code, rec.Body.String())
		}
		if got := st.List(); len(got) != 2 || got[0].ID != created.ID || got[1].When.Event == nil {
			t.Errorf("rules = %+v, want the clock rule then the event rule", got)
		}
		if forge.count() != 1 {
			t.Errorf("forge checks = %d, want 1 for the event rule and none for the clock rule", forge.count())
		}
	})
	t.Run("a put replaces the rule in place", func(t *testing.T) {
		edit := strings.Replace(clock, `"hour":3`, `"hour":5`, 1)
		if rec := sendRoute(t, mux, http.MethodPut, "/api/launch-rules/"+created.ID, edit); rec.Code != http.StatusOK {
			t.Fatalf("PUT = %d %s, want 200", rec.Code, rec.Body.String())
		}
		if got, _ := st.Get(created.ID); got.When.Clock == nil || got.When.Clock.Hour != 5 {
			t.Errorf("rule after PUT = %+v, want 05:00", got)
		}
	})
	t.Run("a get lists every rule with its row", func(t *testing.T) {
		got := listRules(t, mux).Rules
		if len(got) != 2 || got[0].ID != created.ID || got[0].Name != "publish" || got[0].NextRunAt == nil ||
			got[0].Status == nil || got[0].Status.Anchor.IsZero() {
			t.Fatalf("GET = %+v, want the clock rule first with its next run and anchor", got)
		}
		if got[1].When.Event == nil || got[1].When.Event.IntervalMin != 5 || got[1].NextRunAt != nil || got[1].Held != 0 {
			t.Errorf("event rule view = %+v, want its spec with defaults, no next run, nothing held", got[1])
		}
	})
	t.Run("a put of an unknown id answers 404", func(t *testing.T) {
		if rec := sendRoute(t, mux, http.MethodPut, "/api/launch-rules/missing", clock); rec.Code != http.StatusNotFound {
			t.Errorf("PUT missing = %d %s, want 404", rec.Code, rec.Body.String())
		}
	})
	t.Run("a put cannot move a rule to another recipe", func(t *testing.T) {
		before := rulesFile(t, dir)
		moved := strings.Replace(clock, "bundled://publish", "bundled://other", 1)
		if rec := sendRoute(t, mux, http.MethodPut, "/api/launch-rules/"+created.ID, moved); rec.Code != http.StatusBadRequest {
			t.Errorf("PUT with another source = %d %s, want 400", rec.Code, rec.Body.String())
		}
		if !bytes.Equal(before, rulesFile(t, dir)) {
			t.Errorf("a refused PUT rewrote launch-rules.json")
		}
	})
	t.Run("a delete removes it and a second answers 404", func(t *testing.T) {
		if rec := sendRoute(t, mux, http.MethodDelete, "/api/launch-rules/"+created.ID, ""); rec.Code != http.StatusOK {
			t.Fatalf("DELETE = %d %s, want 200", rec.Code, rec.Body.String())
		}
		if _, ok := st.Get(created.ID); ok {
			t.Errorf("the rule survived its delete")
		}
		if rec := sendRoute(t, mux, http.MethodDelete, "/api/launch-rules/"+created.ID, ""); rec.Code != http.StatusNotFound {
			t.Errorf("second DELETE = %d %s, want 404", rec.Code, rec.Body.String())
		}
	})
}

func TestLaunchRuleWritesNeedWriteGuard(t *testing.T) {
	h := newDrainGateHub(t)
	h.SetEventRuleChecker(&fakeEventRuleChecker{})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	h.lifecycle.draining.Store(true)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/launch-rules", runRuleBody},
		{http.MethodPut, "/api/launch-rules/r1", runRuleBody},
		{http.MethodDelete, "/api/launch-rules/r1", ""},
	} {
		t.Run(tc.method, func(t *testing.T) {
			rec := sendRoute(t, mux, tc.method, tc.path, tc.body)
			if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "shutting down") {
				t.Errorf("%s %s while draining = %d %s, want the drain gate's 503", tc.method, tc.path, rec.Code, rec.Body.String())
			}
		})
	}
	t.Run("the list still answers", func(t *testing.T) {
		if rec := sendRoute(t, mux, http.MethodGet, "/api/launch-rules", ""); rec.Code != http.StatusOK {
			t.Errorf("GET /api/launch-rules while draining = %d, want 200", rec.Code)
		}
	})
}

func TestLaunchRuleRouteRefusesInvalidSpec(t *testing.T) {
	const clockPrefix = `{"source":"bundled://publish","enabled":true,"when":{"clock":`
	const eventPrefix = `{"source":"bundled://publish","enabled":true,"when":{"event":{"connection":"github:github.com",` +
		`"repo":"cplieger/animap-source","kind":"run_concluded","filter":{"result":["success"]}`
	for _, tc := range []struct{ name, body, want string }{
		{"a clock hour out of range", clockPrefix + `{"freq":"daily","hour":25}}}`, "when.clock"},
		{"an interval out of range", eventPrefix + `,"interval_min":90}}}`, "interval_min must be 2 to 60"},
		{"a malformed repository", strings.Replace(eventPrefix, "cplieger/animap-source", "not a repo", 1) + `}}}`, "repo"},
		{
			"a label on a run rule", strings.Replace(eventPrefix, `"filter":{`, `"filter":{"label":"x",`, 1) + `}}}`,
			"filter.label is only used by issue_labeled and pr_labeled",
		},
		{"the reserved concurrency", eventPrefix + `,"concurrency":2}}}`, "concurrency is not supported yet"},
		{"the reserved cooldown", eventPrefix + `,"cooldown_min":10}}}`, "cooldown_min is not supported yet"},
		{"a server field", clockPrefix + `{"freq":"daily","hour":2}},"status":{}}`, "status is written by the server"},
		{"both a clock and an event", `{"source":"bundled://publish","when":{"clock":{"freq":"daily","hour":2},` +
			`"event":{"connection":"github:github.com"}}}`, "exactly one of clock and event"},
		{
			"a clock input the recipe does not declare", clockPrefix + `{"freq":"daily","hour":2}},"inputs":{"tag":"v1"}}`,
			"inputs.tag is not an input of publish",
		},
		{
			"an event input the recipe does not declare", eventPrefix + `}},"inputs":{"tag":"v1"}}`,
			"inputs.tag is not an input of publish",
		},
	} {
		for _, method := range []string{http.MethodPost, http.MethodPut} {
			t.Run(tc.name+" through "+method, func(t *testing.T) {
				forge := &fakeEventRuleChecker{}
				dir, st, mux := launchRuleHub(t, forge)
				target := "/api/launch-rules"
				if method == http.MethodPut {
					seed := eventRuleOf("cplieger/animap-source")
					if strings.HasPrefix(tc.body, clockPrefix) {
						seed = &launchrule.Rule{Source: "bundled://publish", When: launchrule.When{
							Clock: &launchrule.Spec{Freq: launchrule.FreqDaily, Hour: 2},
						}}
					}
					r, err := st.Create(t.Context(), seed)
					if err != nil {
						t.Fatalf("Create: %v", err)
					}
					target += "/" + r.ID
				}
				before := rulesFile(t, dir)
				rec := sendRoute(t, mux, method, target, tc.body)
				if rec.Code != http.StatusBadRequest || !strings.Contains(errorText(t, rec), tc.want) {
					t.Errorf("%s = %d %s, want 400 carrying %q", method, rec.Code, rec.Body.String(), tc.want)
				}
				if !bytes.Equal(before, rulesFile(t, dir)) {
					t.Errorf("a refused %s rewrote launch-rules.json", method)
				}
				if forge.count() != 0 {
					t.Errorf("an invalid rule reached the forge check")
				}
			})
		}
	}
}

func TestLaunchRuleSaveChecksInputsAgainstTheRecipe(t *testing.T) {
	const listing = `{"recipes":[{"name":"publish","source":"bundled://publish","inputs":{"tag":"string","notes":"prompt"}}]}`
	const prefix = `{"source":"bundled://publish","enabled":true,"when":{"clock":{"freq":"daily","hour":2}},"inputs":`
	t.Run("declared inputs are kept", func(t *testing.T) {
		_, st, mux := launchRuleHubListing(t, &fakeEventRuleChecker{}, listing)
		rec := sendRoute(t, mux, http.MethodPost, "/api/launch-rules", prefix+`{"tag":"v1","notes":"ship it"}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST = %d %s, want 200", rec.Code, rec.Body.String())
		}
		rules := st.List()
		if len(rules) != 1 || rules[0].Inputs["tag"] != "v1" || rules[0].Inputs["notes"] != "ship it" {
			t.Errorf("stored rules = %+v, want one carrying tag=v1 and notes", rules)
		}
	})
	t.Run("the first undeclared name in order is named", func(t *testing.T) {
		dir, _, mux := launchRuleHubListing(t, &fakeEventRuleChecker{}, listing)
		rec := sendRoute(t, mux, http.MethodPost, "/api/launch-rules", prefix+`{"zeta":"x","tag":"v1","alpha":"y"}}`)
		if rec.Code != http.StatusBadRequest || errorText(t, rec) != "inputs.alpha is not an input of publish" {
			t.Errorf("POST = %d %s, want 400 naming inputs.alpha", rec.Code, rec.Body.String())
		}
		if rulesFile(t, dir) != nil {
			t.Errorf("a refused save wrote launch-rules.json")
		}
	})
}

func TestEventRuleSaveAnswersTheForgeCheck(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code int
	}{
		{"a refusal answers 400", &launchrule.ForgeCheckError{Text: "github.com can't read cplieger/animap-source"}, http.StatusBadRequest},
		{"an unavailable forge answers 503", &launchrule.ForgeCheckError{Text: "github.com did not answer; try again", Unavailable: true}, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, _, mux := launchRuleHub(t, &fakeEventRuleChecker{err: tc.err})
			rec := sendRoute(t, mux, http.MethodPost, "/api/launch-rules", runRuleBody)
			if rec.Code != tc.code || errorText(t, rec) != tc.err.Error() {
				t.Errorf("POST = %d %s, want %d with %q", rec.Code, rec.Body.String(), tc.code, tc.err.Error())
			}
			if rulesFile(t, dir) != nil {
				t.Errorf("a refused save wrote launch-rules.json")
			}
		})
	}
	t.Run("a put that leaves the event alone reads no forge", func(t *testing.T) {
		forge := &fakeEventRuleChecker{}
		_, st, mux := launchRuleHub(t, forge)
		r, err := st.Create(t.Context(), eventRuleOf("cplieger/animap-source"))
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		forge.err = &launchrule.ForgeCheckError{Text: "github.com did not answer; try again", Unavailable: true}
		paused := strings.Replace(runRuleBody, `"enabled":true`, `"enabled":false`, 1)
		if rec := sendRoute(t, mux, http.MethodPut, "/api/launch-rules/"+r.ID, paused); rec.Code != http.StatusOK {
			t.Errorf("pause PUT = %d %s, want 200 with no forge read", rec.Code, rec.Body.String())
		}
		if forge.count() != 0 {
			t.Errorf("forge checks = %d for a pause, want 0", forge.count())
		}
	})
}

// heldQueue is a state holding n events, a cursor, a launched and a superseded slot, and a confirmed run.
func heldQueue(n int) *launchrule.State {
	st := &launchrule.State{
		Status:     launchrule.Status{LastRunID: "wf_confirmed", LastMinted: time.Date(2026, time.November, 2, 9, 0, 0, 0, time.UTC)},
		Cursor:     json.RawMessage(`{"baseline_at":"2026-11-01T00:00:00Z"}`),
		Launched:   json.RawMessage(`[{"key":"run:1"}]`),
		Superseded: json.RawMessage(`[{"key":"run:0"}]`),
	}
	for i := range n {
		st.Queue = append(st.Queue, json.RawMessage(`{"key":"run:`+string(rune('a'+i))+`"}`))
	}
	return st
}

func TestRebaselineEditDropsHeldEvents(t *testing.T) {
	dir, st, mux := launchRuleHub(t, &fakeEventRuleChecker{})
	r, err := st.Create(t.Context(), eventRuleOf("cplieger/animap-source"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fresh, err := st.State(r.ID)
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	held := heldQueue(3)
	held.Population = fresh.Population
	if err := st.WriteState(t.Context(), r.ID, held); err != nil {
		t.Fatalf("WriteState: %v", err)
	}
	moved := strings.Replace(runRuleBody, "cplieger/animap-source", "cplieger/animap", 1)
	t.Run("a repo change with held events answers 409 naming them", func(t *testing.T) {
		before := ruleState(t, dir, r.ID)
		rec := sendRoute(t, mux, http.MethodPut, "/api/launch-rules/"+r.ID, moved)
		want := "This change drops 3 held events. Send it again with `confirm_drop: 3`"
		if rec.Code != http.StatusConflict || errorText(t, rec) != want {
			t.Errorf("PUT = %d %s, want 409 %q", rec.Code, rec.Body.String(), want)
		}
		if got, _ := st.Get(r.ID); got.When.Event.Repo != "cplieger/animap-source" {
			t.Errorf("the refused edit landed: repo %q", got.When.Event.Repo)
		}
		if !bytes.Equal(before, ruleState(t, dir, r.ID)) {
			t.Errorf("the refused edit rewrote the rule's state file")
		}
	})
	t.Run("a wrong count is refused again", func(t *testing.T) {
		body := strings.TrimSuffix(moved, "}") + `,"confirm_drop":2}`
		if rec := sendRoute(t, mux, http.MethodPut, "/api/launch-rules/"+r.ID, body); rec.Code != http.StatusConflict {
			t.Errorf("PUT with confirm_drop 2 = %d %s, want 409", rec.Code, rec.Body.String())
		}
	})
	t.Run("a filter edit keeps the held events", func(t *testing.T) {
		edit := strings.Replace(runRuleBody, `"result":["success"]`, `"result":["success","failure"]`, 1)
		if rec := sendRoute(t, mux, http.MethodPut, "/api/launch-rules/"+r.ID, edit); rec.Code != http.StatusOK {
			t.Fatalf("filter PUT = %d %s, want 200", rec.Code, rec.Body.String())
		}
		if s, _ := st.State(r.ID); s.Held() != 3 || len(s.Cursor) == 0 {
			t.Errorf("state after a filter edit = %+v, want the queue and cursor kept", s)
		}
	})
	t.Run("confirm_drop resets the population and keeps the row", func(t *testing.T) {
		body := strings.TrimSuffix(moved, "}") + `,"confirm_drop":3}`
		if rec := sendRoute(t, mux, http.MethodPut, "/api/launch-rules/"+r.ID, body); rec.Code != http.StatusOK {
			t.Fatalf("PUT with confirm_drop 3 = %d %s, want 200", rec.Code, rec.Body.String())
		}
		reloaded, err := launchrule.NewStore(dir)
		if err != nil {
			t.Fatalf("reload: %v", err)
		}
		s, err := reloaded.State(r.ID)
		if err != nil {
			t.Fatalf("State: %v", err)
		}
		if s.Held() != 0 || s.Cursor != nil || s.Launched != nil || s.Superseded != nil {
			t.Errorf("state = %+v, want the queue, cursor, launched and superseded reset", s)
		}
		if s.Population == "" || s.Population == fresh.Population {
			t.Errorf("population = %q, want the new repository's, not %q", s.Population, fresh.Population)
		}
		if s.Status.LastRunID != held.Status.LastRunID || !s.Status.LastMinted.Equal(held.Status.LastMinted) {
			t.Errorf("status = %+v, want last_run_id and last_minted kept", s.Status)
		}
	})
	t.Run("the attempt in flight is abandoned with cause=edit", func(t *testing.T) {
		t.Skip("needs 5.4")
	})
}

func TestPutRefusesWhenBranchSwitch(t *testing.T) {
	const clockBody = `{"source":"bundled://publish","enabled":true,"when":{"clock":{"freq":"daily","hour":3}}}`
	clock := &launchrule.Rule{Source: "bundled://publish", Enabled: true, When: launchrule.When{
		Clock: &launchrule.Spec{Freq: launchrule.FreqDaily, Hour: 3},
	}}
	for _, tc := range []struct {
		name, body string
		seed       *launchrule.Rule
	}{
		{"clock to event", runRuleBody, clock},
		{"event to clock", clockBody, eventRuleOf("cplieger/animap-source")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forge := &fakeEventRuleChecker{}
			dir, st, mux := launchRuleHub(t, forge)
			r, err := st.Create(t.Context(), tc.seed)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			rules, state := rulesFile(t, dir), ruleState(t, dir, r.ID)
			rec := sendRoute(t, mux, http.MethodPut, "/api/launch-rules/"+r.ID, tc.body)
			const want = "A rule cannot change between a schedule and a forge event. Add a new rule and delete this one"
			if rec.Code != http.StatusBadRequest || errorText(t, rec) != want {
				t.Errorf("PUT = %d %s, want 400 %q", rec.Code, rec.Body.String(), want)
			}
			if !bytes.Equal(rules, rulesFile(t, dir)) || !bytes.Equal(state, ruleState(t, dir, r.ID)) {
				t.Errorf("a refused switch rewrote launch-rules.json or the rule's state file")
			}
			if forge.count() != 0 {
				t.Errorf("a refused switch read the forge %d times", forge.count())
			}
			t.Run("no RuleChanged is delivered", func(t *testing.T) {
				t.Skip("needs 5.4")
			})
		})
	}
}

// anchorClockRule moves a clock rule's anchor off its creation time.
func anchorClockRule(t *testing.T, st *launchrule.Store, row launchrule.Row, anchor time.Time) {
	t.Helper()
	if err := st.UpdateClock(t.Context(), row, func(s *launchrule.Status) bool { s.Anchor = anchor; return true }); err != nil {
		t.Fatalf("UpdateClock: %v", err)
	}
}

// putClockRule stores a clock rule for source, anchored at its creation.
func putClockRule(t *testing.T, st *launchrule.Store, source string, spec launchrule.Spec, enabled bool) launchrule.Rule {
	t.Helper()
	r, err := st.Create(t.Context(), &launchrule.Rule{Source: source, Enabled: enabled, When: launchrule.When{Clock: &spec}})
	if err != nil {
		t.Fatalf("Create(%s): %v", source, err)
	}
	return r
}
