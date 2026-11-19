package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/keyedrate"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rule"
	"github.com/cplieger/marotte/internal/ruleengine"
)

// fieldErrorBody is one refused field of a 400's body.
type fieldErrorBody struct {
	Field string `json:"field"`
	Error string `json:"error"`
}

// fakeRuleView answers the engine's in-memory facts the setup route serves; its taps and commands do nothing.
type fakeRuleView struct {
	status map[marotte.ChatID]ruleengine.Status
}

func (f *fakeRuleView) Status(id marotte.ChatID) ruleengine.Status { return f.status[id] }

func (*fakeRuleView) TurnOpened(marotte.ChatID, string, marotte.TurnOpenSource, *marotte.Caller, bool) int {
	return 0
}

func (*fakeRuleView) TurnClosed(marotte.ChatID, string, *marotte.Caller) {}

func (*fakeRuleView) LaunchDepth(marotte.ChatID) int { return 0 }

func (*fakeRuleView) RunEnded(marotte.ChatID, string, int) {}

func (*fakeRuleView) SessionEnded(marotte.ChatID) {}

func (*fakeRuleView) Reindex(marotte.ChatID) {}

func (*fakeRuleView) RunNow(context.Context, marotte.ChatID, string, string) (command.RunNowAnswer, error) {
	return command.RunNowAnswer{}, nil
}

func postJSONRoute(t *testing.T, rt *Runtime, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	rt.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw)))
	return rec
}

const testRulePrompt = "Check the open pull requests."

func everyRule(id, triggerID string, minutes int) rule.Rule {
	return rule.Rule{ID: id, Spec: rule.Spec{
		Name: "Tidy", Prompt: testRulePrompt, Enabled: true,
		Triggers: []rule.Trigger{{ID: triggerID, Kind: rule.KindEvery, Every: &rule.EveryTrigger{Minutes: minutes}}},
	}, Status: rule.Status{Triggers: map[string]rule.TriggerState{triggerID: {ArmedAt: 1_700_000_000_000}}}}
}

// seedRulesBot writes the started Bot id with rules, at spec_version 3.
func seedRulesBot(t *testing.T, cs *testChatStore, id marotte.ChatID, rules ...rule.Rule) {
	t.Helper()
	if _, err := cs.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool {
		c.Name, c.NameSetByUser, c.CurrentModeID = "Tidier", true, marotte.BotRoleID
		c.Bot = &marotte.Bot{Goal: "Keep it tidy.", StartedAt: 1_700_000_000_000, SpecVersion: 3, Rules: rules}
		return true
	}); err != nil {
		t.Fatalf("Setup: seed the Bot: %v", err)
	}
}

func catalogSample(t *testing.T, kind rule.TriggerKind) map[string]any {
	t.Helper()
	for _, k := range rule.NewCatalog().Kinds {
		if k.Kind == kind {
			var sample map[string]any
			if err := json.Unmarshal(k.Sample, &sample); err != nil {
				t.Fatal(err)
			}
			return sample
		}
	}
	t.Fatalf("the catalogue has no %s kind", kind)
	return nil
}

// The setup carries each rule's spec and status and the engine's in-memory facts, and is never cached.
func TestBotSetupRoute_CarriesTheRulesAndTheEngine(t *testing.T) {
	t.Parallel()
	since := time.UnixMilli(1_700_000_600_000)
	ceiling := time.UnixMilli(1_700_000_900_000)
	view := &fakeRuleView{status: map[marotte.ChatID]ruleengine.Status{"c1": {WriteFailingSince: since, CeilingUntil: ceiling}}}
	h, cs, _ := newTestHub()
	h.SetRuleEngine(view)
	r := everyRule("R1", "T1", 15)
	r.Status.LastFire = &rule.Mark{Trigger: "T1", EventID: "every:T1:1", Summary: "every 15 min", At: 1_700_000_100_000}
	seedRulesBot(t, cs, "c1", r)
	seedRulesBot(t, cs, "c2", everyRule("R2", "T2", 30))

	rec := getRoute(t, h, "/api/bots/c1")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/bots/c1 = %d %s, want 200", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	got := decodeBody[marotte.BotSetup](t, rec)
	if got.Bot == nil || len(got.Bot.Rules) != 1 {
		t.Fatalf("bot = %+v, want the one rule", got.Bot)
	}
	if !reflect.DeepEqual(got.Bot.Rules[0], r) {
		t.Errorf("rule = %+v, want the record's spec and status %+v", got.Bot.Rules[0], r)
	}
	want := marotte.BotEngine{State: marotte.BotEngineWriteFailing, Since: since.UnixMilli(), CeilingUntil: ceiling.UnixMilli()}
	if got.Engine != want {
		t.Errorf("engine = %+v, want %+v", got.Engine, want)
	}

	t.Run("a Bot the engine has nothing on", func(t *testing.T) {
		rec := getRoute(t, h, "/api/bots/c2")
		body := decodeBody[map[string]json.RawMessage](t, rec)
		if string(body["engine"]) != "{}" {
			t.Errorf("engine = %s, want {}", body["engine"])
		}
	})
}

// The catalogue route answers the rule package's catalogue exactly, so the client mirrors no bound or phrase.
func TestRuleCatalogRoute_AnswersTheCatalogue(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestHub()

	rec := getRoute(t, h, "/api/bots/rule-catalog")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/bots/rule-catalog = %d %s, want 200", rec.Code, rec.Body)
	}
	raw, err := json.Marshal(rule.NewCatalog())
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("catalogue = %s, want %s", rec.Body, raw)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

// testBody is the dry run's request.
type testBody struct {
	Spec      rule.Spec      `json:"spec"`
	TriggerID string         `json:"trigger_id,omitempty"`
	Sample    map[string]any `json:"sample,omitempty"`
	UseLast   bool           `json:"use_last,omitempty"`
}

func testSpec(cond *rule.Cond, triggers ...rule.Trigger) rule.Spec {
	return rule.Spec{Name: "Tidy", Prompt: testRulePrompt, Enabled: true, Triggers: triggers, Condition: cond}
}

func newEvery(id string, minutes int) rule.Trigger {
	return rule.Trigger{ID: id, Kind: rule.KindEvery, Every: &rule.EveryTrigger{Minutes: minutes}}
}

func condLeaf(l rule.Leaf) rule.Cond { return rule.Cond{Leaf: &l} }

func runTest(t *testing.T, h *Runtime, chat string, body testBody) *httptest.ResponseRecorder {
	t.Helper()
	return postJSONRoute(t, h, "/api/bots/"+chat+"/rules/test", body)
}

// A dry run of a sample answers the outcome, each state item's value, the wait and the exact text, and changes
// nothing in the record.
func TestRuleTestRoute_DryRunsASampleAndWritesNothing(t *testing.T) {
	t.Parallel()
	h, cs, _ := newTestHub()
	saved := everyRule("R1", "T1", 15)
	seedRulesBot(t, cs, "c1", saved)
	before, _ := cs.Get(t.Context(), "c1")
	cond := rule.Cond{Op: rule.OpAll, Children: []rule.Cond{
		condLeaf(rule.Leaf{Kind: rule.LeafRuleLastRan, Rule: &rule.LastRanLeaf{Minutes: 60}}),
		condLeaf(rule.Leaf{Kind: rule.LeafBotWaiting}),
	}}
	spec := testSpec(&cond, newEvery("T1", 15))
	sample := catalogSample(t, rule.KindEvery)

	rec := runTest(t, h, "c1", testBody{Spec: spec, Sample: sample})

	if rec.Code != http.StatusOK {
		t.Fatalf("rules/test = %d %s, want 200", rec.Code, rec.Body)
	}
	got := decodeBody[marotte.RuleTestAnswer](t, rec)
	if got.Outcome != marotte.RuleTestSkip || got.Reason != "this Bot is not waiting on you" {
		t.Errorf("outcome %q reason %q, want skip for the false bot_waiting item", got.Outcome, got.Reason)
	}
	wantLeaves := []marotte.RuleTestLeaf{
		{Path: "condition.children[0]", Value: marotte.RuleTestTrue},
		{Path: "condition.children[1]", Value: marotte.RuleTestFalse},
	}
	if !slices.Equal(got.Leaves, wantLeaves) {
		t.Errorf("leaves = %+v, want %+v", got.Leaves, wantLeaves)
	}
	data, err := rule.NewEvent(sample)
	if err != nil {
		t.Fatal(err)
	}
	wantText, err := rule.Render(spec, &rule.QueuedEvent{Stage: rule.StageFired, Trigger: "T1", EventID: "test", Data: data, Depth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != wantText {
		t.Errorf("text = %q, want the rendered prompt %q", got.Text, wantText)
	}
	after, _ := cs.Get(t.Context(), "c1")
	if !reflect.DeepEqual(after.Bot, before.Bot) {
		t.Errorf("the record moved: %+v, want %+v", after.Bot, before.Bot)
	}
}

// A rule whose condition holds says it would send now, or why and until when it would wait.
func TestRuleTestRoute_SaysWhatWouldHappenNext(t *testing.T) {
	t.Parallel()
	spec := testSpec(nil, newEvery("T1", 15))
	t.Run("a free Bot", func(t *testing.T) {
		h, cs, _ := newTestHub()
		seedRulesBot(t, cs, "c1", everyRule("R1", "T1", 15))
		got := decodeBody[marotte.RuleTestAnswer](t, runTest(t, h, "c1", testBody{Spec: spec, Sample: catalogSample(t, rule.KindEvery)}))
		if got.Outcome != marotte.RuleTestFire || got.Wait != (marotte.RuleTestWait{}) {
			t.Errorf("outcome %q wait %+v, want fire with no wait", got.Outcome, got.Wait)
		}
	})
	t.Run("a working Bot", func(t *testing.T) {
		h, cs, _ := newTestHub()
		seedRulesBot(t, cs, "c1", everyRule("R1", "T1", 15))
		if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
			c.Run = []marotte.RunEntry{{ID: "e1", Kind: marotte.RunKindTurn, State: marotte.RunPending, Origin: marotte.RunFromWire}}
			return true
		}); err != nil {
			t.Fatal(err)
		}
		got := decodeBody[marotte.RuleTestAnswer](t, runTest(t, h, "c1", testBody{Spec: spec, Sample: catalogSample(t, rule.KindEvery)}))
		if got.Outcome != marotte.RuleTestFire || got.Wait != (marotte.RuleTestWait{Reason: marotte.RuleWaitBotBusy}) {
			t.Errorf("outcome %q wait %+v, want fire waiting on the turn", got.Outcome, got.Wait)
		}
	})
	t.Run("a Bot inside its 1-minute gap", func(t *testing.T) {
		h, cs, _ := newTestHub()
		r := everyRule("R1", "T1", 15)
		sent := time.Now().Add(-20 * time.Second).UnixMilli()
		r.Status.Sent = []int64{sent}
		seedRulesBot(t, cs, "c1", r)
		got := decodeBody[marotte.RuleTestAnswer](t, runTest(t, h, "c1", testBody{Spec: spec, Sample: catalogSample(t, rule.KindEvery)}))
		want := marotte.RuleTestWait{Reason: marotte.RuleWaitGap, Until: sent + rule.MinGap.Milliseconds()}
		if got.Wait != want {
			t.Errorf("wait %+v, want %+v", got.Wait, want)
		}
	})
}

// use_last evaluates the trigger's stored last real event.
func TestRuleTestRoute_UsesTheTriggersLastRealEvent(t *testing.T) {
	t.Parallel()
	h, cs, _ := newTestHub()
	r := everyRule("R1", "T1", 15)
	last, err := rule.NewEvent(map[string]any{"trigger": "every", "rule": "Tidy", "due_at": "2026-11-16T09:00:00+01:00", "late_by": "0s", "anchor": "last"})
	if err != nil {
		t.Fatal(err)
	}
	st := r.Status.Triggers["T1"]
	st.LastEvent = last
	r.Status.Triggers["T1"] = st
	seedRulesBot(t, cs, "c1", r)

	rec := runTest(t, h, "c1", testBody{Spec: testSpec(nil, newEvery("T1", 15)), TriggerID: "T1", UseLast: true})

	if rec.Code != http.StatusOK {
		t.Fatalf("rules/test = %d %s, want 200", rec.Code, rec.Body)
	}
	if got := decodeBody[marotte.RuleTestAnswer](t, rec); !strings.Contains(got.Text, "2026-11-16T09:00:00+01:00") {
		t.Errorf("text = %q, want the stored event's due_at", got.Text)
	}
}

// A body breaking the request's rules answers 400 naming the field, and an invalid spec its fields.
func TestRuleTestRoute_RefusesABadBody(t *testing.T) {
	t.Parallel()
	two := testSpec(nil, newEvery("T1", 15), newEvery("new:b", 30))
	sample := map[string]any{"trigger": "every", "rule": "Tidy", "due_at": "x", "late_by": "0s", "anchor": "last"}
	cases := []struct {
		name  string
		body  testBody
		field string
	}{
		{"both sample and use_last", testBody{Spec: testSpec(nil, newEvery("T1", 15)), Sample: sample, UseLast: true}, "sample"},
		{"neither sample nor use_last", testBody{Spec: testSpec(nil, newEvery("T1", 15))}, "sample"},
		{"no trigger_id on two triggers", testBody{Spec: two, Sample: sample}, "trigger_id"},
		{"a trigger_id the spec lacks", testBody{Spec: testSpec(nil, newEvery("T1", 15)), TriggerID: "T9", Sample: sample}, "trigger_id"},
		{"use_last with no last event", testBody{Spec: testSpec(nil, newEvery("T1", 15)), UseLast: true}, "use_last"},
		{"a sample that is no event", testBody{Spec: testSpec(nil, newEvery("T1", 15)), Sample: map[string]any{"rule": map[string]any{}}}, "sample"},
		{"a sample of another kind", testBody{Spec: testSpec(nil, newEvery("T1", 15)), Sample: map[string]any{"trigger": "at", "rule": "Tidy", "due_at": "x", "late_by": "0s"}}, "sample"},
		{"an invalid spec", testBody{Spec: testSpec(nil, newEvery("T1", 1)), Sample: sample}, "spec.triggers[0].every.minutes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, cs, _ := newTestHub()
			seedRulesBot(t, cs, "c1", everyRule("R1", "T1", 15))
			rec := runTest(t, h, "c1", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("rules/test = %d %s, want 400", rec.Code, rec.Body)
			}
			var body struct {
				Error  string           `json:"error"`
				Fields []fieldErrorBody `json:"fields"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error == "" || !slices.ContainsFunc(body.Fields, func(f fieldErrorBody) bool { return f.Field == tc.field && f.Error != "" }) {
				t.Errorf("body = %s, want an error naming %s with its sentence", rec.Body, tc.field)
			}
		})
	}
	t.Run("a chat that is no Bot", func(t *testing.T) {
		h, cs, _ := newTestHub()
		seedChain(t, cs, "c-plain", "sess_a")
		if rec := runTest(t, h, "c-plain", testBody{Spec: testSpec(nil, newEvery("new:a", 15)), Sample: sample}); rec.Code != http.StatusNotFound {
			t.Errorf("rules/test on a plain chat = %d, want 404", rec.Code)
		}
	})
}

// Each Bot has its own bucket of 30 tests a minute; past it the route answers 429 with the wait.
func TestRuleTestRoute_LimitsEachBotToThirtyAMinute(t *testing.T) {
	t.Parallel()
	h, cs, _ := newTestHub()
	now := time.Unix(1_700_000_000, 0)
	h.ruleTests = keyedrate.New(keyedrate.Config{Burst: 30, Every: 2 * time.Second, Now: func() time.Time { return now }})
	seedRulesBot(t, cs, "c1", everyRule("R1", "T1", 15))
	seedRulesBot(t, cs, "c2", everyRule("R2", "T2", 15))
	body := func(trigger string) testBody {
		return testBody{Spec: testSpec(nil, newEvery(trigger, 15)), Sample: catalogSample(t, rule.KindEvery)}
	}

	for i := range 30 {
		if rec := runTest(t, h, "c1", body("T1")); rec.Code != http.StatusOK {
			t.Fatalf("test #%d = %d %s, want 200", i+1, rec.Code, rec.Body)
		}
	}
	rec := runTest(t, h, "c1", body("T1"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the 31st test = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Errorf("Retry-After = %q, want 2", got)
	}
	if got := decodeBody[map[string]any](t, rec); got["retry_after_ms"] != float64(2000) || got["error"] == "" {
		t.Errorf("body = %v, want the error and retry_after_ms 2000", got)
	}
	if rec := runTest(t, h, "c2", body("T2")); rec.Code != http.StatusOK {
		t.Errorf("another Bot's test = %d, want 200: its bucket is its own", rec.Code)
	}
}
