package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// configModelUpdate mirrors KAS's shape: a choice gets `_meta: { kiro: { hasEffort } }`
// when the model has effort levels or a rate multiplier.
func configModelUpdate(t *testing.T, current string, choices []map[string]any) []byte {
	t.Helper()
	return mustJSON(t, map[string]any{
		"configOptions": []map[string]any{
			{
				"id":           "model",
				"category":     "model",
				"type":         "select",
				"currentValue": current,
				"options":      choices,
			},
		},
	})
}

// The per-model effortLevels land on SessionModel.EffortTiers, the one capability the effort
// row reads; a choice with no _meta decodes as none.
func TestHandleConfigOptionUpdate_PlumbsEffortTiers(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	raw := configModelUpdate(t, "model-a", []map[string]any{
		{"value": "model-a", "name": "Model A", "_meta": map[string]any{"kiro": map[string]any{"effortLevels": []string{"low", "high"}, "rateMultiplier": 1.0}}},
		{"value": "model-b", "name": "Model B", "_meta": map[string]any{"kiro": map[string]any{"rateMultiplier": 2.0}}},
		{"value": "model-c", "name": "Model C"}, // no _meta at all
	})

	tr.HandleConfigOptionUpdate(t.Context(), "c1", raw, FrameAttribution{})

	c, ok := store.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat c1 missing after config_option_update")
	}
	if c.Model != "model-a" {
		t.Errorf("current model = %q, want model-a", c.Model)
	}
	// The model LIST is the workspace catalog's, not the chat's.
	want := map[string][]string{"model-a": {"low", "high"}, "model-b": nil, "model-c": nil}
	if len(deps.catalogModels) != len(want) {
		t.Fatalf("catalog models len = %d, want %d: %+v", len(deps.catalogModels), len(want), deps.catalogModels)
	}
	for _, m := range deps.catalogModels {
		exp, known := want[m.ID]
		if !known {
			t.Errorf("unexpected model %q in catalog", m.ID)
			continue
		}
		if !slices.Equal(m.EffortTiers, exp) {
			t.Errorf("model %q EffortTiers = %v, want %v", m.ID, m.EffortTiers, exp)
		}
	}
}

// configEffortUpdate carries the session's `effortLevel` option.
func configEffortUpdate(t *testing.T, current string, choices []map[string]any) []byte {
	t.Helper()
	return mustJSON(t, map[string]any{
		"configOptions": []map[string]any{
			{
				"id":           "effortLevel",
				"type":         "select",
				"currentValue": current,
				"options":      choices,
			},
		},
	})
}

// The effortLevel choices are the tier list, and currentValue is the level the session is
// RUNNING at.
func TestHandleConfigOptionUpdate_PlumbsEffortOption(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	raw := configEffortUpdate(t, "medium", []map[string]any{
		{"value": "low", "name": "Low"},
		{"value": "medium", "name": "Medium"},
		{"value": "high", "name": "High"},
	})
	tr.HandleConfigOptionUpdate(t.Context(), "c1", raw, FrameAttribution{})

	c, ok := store.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat c1 missing after config_option_update")
	}
	if c.EffortActive != "medium" {
		t.Errorf("EffortActive = %q, want medium", c.EffortActive)
	}
	wantIDs := []string{"low", "medium", "high"}
	gotIDs := make([]string, 0, len(c.EffortLevels))
	for _, l := range c.EffortLevels {
		gotIDs = append(gotIDs, l.ID)
	}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Errorf("EffortLevels = %v, want %v", gotIDs, wantIDs)
	}
	if len(c.EffortLevels) > 0 && c.EffortLevels[0].Name != "Low" {
		t.Errorf("level name = %q, want Low", c.EffortLevels[0].Name)
	}
	// No session is recorded yet: the level is the opening door's to pin.
	if c.Effort != "" {
		t.Errorf("Effort = %q, want empty while no session is recorded", c.Effort)
	}
}

// An empty list is an answer (a model with no tiers), so it must land.
func TestHandleConfigOptionUpdate_EmptyEffortOptionApplies(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleConfigOptionUpdate(t.Context(), "c1",
		configEffortUpdate(t, "high", []map[string]any{{"value": "high", "name": "High"}}), FrameAttribution{})
	tr.HandleConfigOptionUpdate(t.Context(), "c1", configEffortUpdate(t, "", nil), FrameAttribution{})

	c, _ := store.Get(t.Context(), "c1")
	if len(c.EffortLevels) != 0 {
		t.Errorf("EffortLevels = %v, want empty", c.EffortLevels)
	}
	if c.EffortActive != "" {
		t.Errorf("EffortActive = %q, want empty", c.EffortActive)
	}
}

// Absent and malformed meta both decode to the zero value.
func TestChoiceMeta(t *testing.T) {
	cases := []struct {
		name        string
		meta        string
		wantTiers   []string
		wantDefault string
		wantRate    float64
	}{
		{name: "nil meta"},
		{name: "empty object", meta: `{}`},
		{name: "tiers only", meta: `{"kiro":{"effortLevels":["low","max"]}}`, wantTiers: []string{"low", "max"}},
		{name: "kiro without tiers", meta: `{"kiro":{"rateMultiplier":2}}`, wantRate: 2},
		{
			name:        "default tier, no tiers",
			meta:        `{"kiro":{"rateMultiplier":1,"effortSchemaPath":"reasoning","defaultEffortLevel":"xhigh"}}`,
			wantDefault: "xhigh",
			wantRate:    1,
		},
		{
			name:     "fractional multiplier",
			meta:     `{"kiro":{"rateMultiplier":0.25}}`,
			wantRate: 0.25,
		},
		{name: "malformed", meta: `{not json`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var meta []byte
			if tc.meta != "" {
				meta = []byte(tc.meta)
			}
			got := choiceMeta(meta)
			if !slices.Equal(got.Kiro.EffortLevels, tc.wantTiers) {
				t.Errorf("EffortLevels = %v, want %v", got.Kiro.EffortLevels, tc.wantTiers)
			}
			if got.Kiro.DefaultEffortLevel != tc.wantDefault {
				t.Errorf("DefaultEffortLevel = %q, want %q", got.Kiro.DefaultEffortLevel, tc.wantDefault)
			}
			if got.Kiro.RateMultiplier != tc.wantRate {
				t.Errorf("RateMultiplier = %v, want %v", got.Kiro.RateMultiplier, tc.wantRate)
			}
		})
	}
}

// infoKindFrame builds a session_info_update whose _meta.kiro carries only a kind, the
// shape every unconsumed sub-kind arrives in.
func infoKindFrame(t *testing.T, kind string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"_meta": map[string]any{"kiro": map[string]any{"kind": kind}},
	})
	if err != nil {
		t.Fatalf("marshal info frame: %v", err)
	}
	return b
}

// TestSessionInfoUpdate_UnknownKindWarns pins that an unrecognised sub-kind logs at Warn
// and a known-but-ignored one does not. Serial: slog's default is process-global.
func TestSessionInfoUpdate_UnknownKindWarns(t *testing.T) {
	tests := []struct {
		name     string
		kind     string
		wantWarn bool
	}{
		{name: "a kind KAS added since this was written", kind: "quantum_entanglement_update", wantWarn: true},
		// recap, not turn_start: a bracket kind reaching here is a decode miss.
		{name: "known and deliberately ignored", kind: "recap", wantWarn: false},
		{name: "consumed, but its credits did not decode", kind: "session_usage", wantWarn: true},
		{name: "known compaction marker", kind: "summarization_separator", wantWarn: false},
		{name: "consumed, but its sub-block did not decode", kind: "pending_interaction", wantWarn: true},
		{name: "reaches the wire via SessionInfoEmitter, not a build call site", kind: "repositories_update", wantWarn: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetCensus(t)
			var buf bytes.Buffer
			captureSlog(t, &buf)

			deps, _, _ := depsWithStore(t, "c1")
			New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1", infoKindFrame(t, tt.kind), FrameAttribution{})

			out := buf.String()
			gotWarn := strings.Contains(out, "level=WARN") && strings.Contains(out, "UNKNOWN kind")
			if gotWarn != tt.wantWarn {
				t.Errorf("kind %q: warned = %v, want %v (log was: %s)", tt.kind, gotWarn, tt.wantWarn, out)
			}
			if !strings.Contains(out, tt.kind) {
				t.Errorf("kind %q never reached the log at any level; a dropped sub-kind must leave a trace. log was: %s",
					tt.kind, out)
			}
		})
	}
}

// TestSessionInfoUpdate_NoKindIsSilent pins that a frame with no kind logs nothing, which
// covers every frame the cascade already consumed.
func TestSessionInfoUpdate_NoKindIsSilent(t *testing.T) {
	var buf bytes.Buffer
	captureSlog(t, &buf)

	deps, _, _ := depsWithStore(t, "c1")
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		json.RawMessage(`{"_meta":{"kiro":{}}}`), FrameAttribution{})

	if out := buf.String(); out != "" {
		t.Errorf("a kindless session_info_update logged %q, want silence", out)
	}
}

// turnBracketInfo builds one half of the wire's turn bracket: turn_start a flat `true`,
// turn_end a nested object.
func turnBracketInfo(t *testing.T, kind string) json.RawMessage {
	t.Helper()
	kiro := map[string]any{"kind": kind}
	if kind == "turn_start" {
		kiro["turnStart"] = true
	} else {
		kiro["turnEnd"] = map[string]any{"stopReason": "end_turn"}
	}
	return mustJSON(t, map[string]any{"_meta": map[string]any{"kiro": kiro}})
}

// A workflow STEP's own turn bracket is dropped: a step's fold opens a
// TurnSourceWorkflowStep turn precisely because no bracket will close it.
func TestHandleSessionInfoUpdate_AStepsTurnBracketIsDropped(t *testing.T) {
	tests := []struct {
		name string
		kind string
		attr FrameAttribution
		want []turnBracket
	}{
		{name: "a step's turn_end", kind: "turn_end", attr: FrameAttribution{Step: true}, want: nil},
		{name: "a step's turn_start", kind: "turn_start", attr: FrameAttribution{Step: true}, want: nil},
		{
			name: "the chat's own turn_end",
			kind: "turn_end",
			attr: FrameAttribution{},
			want: []turnBracket{{chat: "c1", kind: "end", stop: marotte.StopReason("end_turn")}},
		},
		{
			name: "the chat's own turn_start",
			kind: "turn_start",
			attr: FrameAttribution{},
			want: []turnBracket{{chat: "c1", kind: "start"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps, _, _ := depsWithStore(t, "c1")
			tr := New(rolesOf(deps))

			tr.HandleSessionInfoUpdate(t.Context(), "c1", turnBracketInfo(t, tt.kind), tt.attr)

			if !slices.Equal(deps.brackets, tt.want) {
				t.Errorf("HandleSessionInfoUpdate(%s) recorded brackets %+v, want %+v",
					tt.name, deps.brackets, tt.want)
			}
		})
	}
}

// activityInfo is the engine's focus_update as the 2.31 wire sends it: a focus status with the activity
// snapshot flat beside the kind.
func activityInfo(t *testing.T, turnActive bool) json.RawMessage {
	t.Helper()
	return mustJSON(t, map[string]any{"_meta": map[string]any{"kiro": map[string]any{
		"kind":     "focus_update",
		"focus":    map[string]any{"status": "idle"},
		"status":   "idle",
		"activity": map[string]any{"turnActive": turnActive, "runningWorkflows": 0, "pausedWorkflows": 0},
	}}})
}

// KAS's activity report reaches the turn lifecycle for the chat's own session only.
func TestHandleSessionInfoUpdate_ActivityReachesTheTurnLifecycle(t *testing.T) {
	tests := []struct {
		name   string
		active bool
		attr   FrameAttribution
		want   []turnBracket
	}{
		{name: "the chat going idle", active: false, want: []turnBracket{{chat: "c1", kind: "idle"}}},
		{name: "the chat running a turn", active: true, want: []turnBracket{{chat: "c1", kind: "active"}}},
		{name: "a step going idle", active: false, attr: FrameAttribution{Step: true}, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps, _, _ := depsWithStore(t, "c1")

			New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1", activityInfo(t, tt.active), tt.attr)

			if !slices.Equal(deps.brackets, tt.want) {
				t.Errorf("brackets = %+v, want %+v", deps.brackets, tt.want)
			}
		})
	}
}

// The compactor hears every live reading, a sub-point move the stored usage drops included, from both usage channels.
func TestPersistUsage_EveryReadingReachesTheObserver(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleUsageUpdate(t.Context(), "c1", mustJSON(t, map[string]any{"size": 100, "used": 50}))
	tr.HandleUsageUpdate(t.Context(), "c1", mustJSON(t, map[string]any{"size": 1000, "used": 504}))
	tr.HandleSessionInfoUpdate(t.Context(), "c1", mustJSON(t, map[string]any{"_meta": map[string]any{"kiro": map[string]any{
		"kind": "context_usage", "contextUsage": map[string]any{"usagePercentage": 40.0},
	}}}), FrameAttribution{})

	if want := []float64{50, 50.4, 40}; !slices.Equal(deps.usageReadings, want) {
		t.Errorf("observed readings = %v, want %v", deps.usageReadings, want)
	}
}

// publishedUsageInfo is KAS publishing its stored figure (publishContextUsage: a prompt's end, a refresh, a load),
// which carries the summarization threshold nested and flat; a model response's own reading carries none.
func publishedUsageInfo(t *testing.T, pct float64) json.RawMessage {
	t.Helper()
	return mustJSON(t, map[string]any{"_meta": map[string]any{"kiro": map[string]any{
		"kind": "context_usage", "usagePercentage": pct, "summarizationThreshold": 80,
		"contextUsage": map[string]any{"usagePercentage": pct, "summarizationThreshold": 80},
	}}})
}

// KAS's published figure is its stored one, which a compaction leaves at the context before it: it never reaches
// the compactor, and it is shown only while no compaction waits on a live reading. A replayed reading is history.
func TestContextUsage_APublishedFigureFeedsNoCompactor(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", publishedUsageInfo(t, 12), FrameAttribution{})
	tr.HandleSessionInfoUpdate(t.Context(), "c1", mustJSON(t, map[string]any{"_meta": map[string]any{"kiro": map[string]any{
		"kind": "context_usage", "replay": true, "contextUsage": map[string]any{"usagePercentage": 30.0},
	}}}), FrameAttribution{})

	if len(deps.usageReadings) != 0 {
		t.Errorf("observed readings = %v, want none for a published or replayed figure", deps.usageReadings)
	}
	if c, _ := store.Get(t.Context(), "c1"); c.Usage.ContextPct != 30 || !c.Usage.HasRealData {
		t.Errorf("usage = %+v, want the newest figure shown (30) with no compaction waiting", c.Usage)
	}
}

// KAS sends a response's reading twice, the plain frame then a twin with its breakdown, and the twin can land after
// the next response's frames: only the plain frame reaches the compactor, while the twin's figure is still shown.
func TestContextUsage_ABreakdownTwinFeedsNoCompactor(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", contextUsageInfo(t, "contextUsage", 20), FrameAttribution{})
	tr.HandleSessionInfoUpdate(t.Context(), "c1", mustJSON(t, map[string]any{"_meta": map[string]any{"kiro": map[string]any{
		"kind": "context_usage", "usagePercentage": 40.0, "contextUsage": map[string]any{"usagePercentage": 40.0},
		"breakdown": map[string]any{"tools": map[string]any{"tokens": 7131, "percent": 0.7}},
	}}}), FrameAttribution{})

	if want := []float64{20}; !slices.Equal(deps.usageReadings, want) {
		t.Errorf("observed readings = %v, want only the plain frame's %v", deps.usageReadings, want)
	}
	if c, _ := store.Get(t.Context(), "c1"); c.Usage.ContextPct != 40 {
		t.Errorf("usage = %+v, want the twin's figure (40) shown", c.Usage)
	}
}

// After a summary lands, KAS republishes its pre-summary figure until its next live reading: that figure is not
// shown, a live reading is and ends the wait, and a figure published after it is shown again.
func TestContextUsage_ACompactionWaitsForALiveReading(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	if _, err := store.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Usage = marotte.Usage{ContextPct: 96, HasRealData: true}
		return true
	}); err != nil {
		t.Fatalf("seed usage: %v", err)
	}
	tr := New(rolesOf(deps))
	tr.HandleSessionInfoUpdate(t.Context(), "c1", summarizationInfo(t, "success", "history summary"), FrameAttribution{})

	tr.HandleSessionInfoUpdate(t.Context(), "c1", publishedUsageInfo(t, 96), FrameAttribution{})
	if c, _ := store.Get(t.Context(), "c1"); c.Usage.ContextPct != 0 || c.Usage.HasRealData || !c.Usage.ContextCompacted {
		t.Errorf("usage after the republished figure = %+v, want the cleared figure kept, still waiting", c.Usage)
	}
	tr.HandleSessionInfoUpdate(t.Context(), "c1", contextUsageInfo(t, "contextUsage", 31), FrameAttribution{})
	if c, _ := store.Get(t.Context(), "c1"); c.Usage.ContextPct != 31 || !c.Usage.HasRealData || c.Usage.ContextCompacted {
		t.Errorf("usage after a live reading = %+v, want 31 shown and the wait over", c.Usage)
	}
	tr.HandleSessionInfoUpdate(t.Context(), "c1", publishedUsageInfo(t, 33), FrameAttribution{})
	if c, _ := store.Get(t.Context(), "c1"); c.Usage.ContextPct != 33 {
		t.Errorf("usage after a later published figure = %+v, want 33 shown", c.Usage)
	}
	if want := []float64{31}; !slices.Equal(deps.usageReadings, want) {
		t.Errorf("observed readings = %v, want only the live %v", deps.usageReadings, want)
	}
}

// TestMaterialPctDelta pins the gate on a context-percentage rewrite: a sub-point move is
// dropped wherever it lands (the ring cannot render it), a one-point move is kept.
func TestMaterialPctDelta(t *testing.T) {
	cases := map[string]struct {
		old, new float64
		want     bool
	}{
		"identical":                      {50, 50, false},
		"sub-point up is not material":   {50, 50.4, false},
		"sub-point down is not material": {50, 49.6, false},
		"exactly one point up":           {50, 51, true},
		"exactly one point down":         {51, 50, true},
		"large jump":                     {10, 90, true},
		"sub-point move across 70":       {69.9, 70.0, false},
		"sub-point move across 90":       {89.9, 90.0, false},
		"sub-point move across 95 down":  {95.0, 94.9, false},
		"one-point move across 95":       {94.5, 95.5, true},
		"from zero is material":          {0, 1, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := materialPctDelta(tc.old, tc.new); got != tc.want {
				t.Errorf("materialPctDelta(%v, %v) = %v, want %v", tc.old, tc.new, got, tc.want)
			}
		})
	}
}

// turnSummaryInfo builds the turn-end metering block: promptTurnSummaries plus elapsedTime
// in ms. An empty unit omits the key, as KAS does for the default dimension.
func turnSummaryInfo(t *testing.T, elapsedMs float64, summaries []map[string]any) json.RawMessage {
	t.Helper()
	return mustJSON(t, map[string]any{
		"_meta": map[string]any{"kiro": map[string]any{
			"promptTurnSummaries": summaries,
			"elapsedTime":         elapsedMs,
		}},
	})
}

// contextUsageInfo builds a context percentage on one of KAS's two mirrored channels:
// the contextUsage sub-block, or the bare _meta.kiro.usagePercentage.
func contextUsageInfo(t *testing.T, key string, pct float64) json.RawMessage {
	t.Helper()
	kiro := map[string]any{}
	switch key {
	case "contextUsage":
		kiro["contextUsage"] = map[string]any{"usagePercentage": pct}
	case "usagePercentage":
		kiro["usagePercentage"] = pct
	}
	return mustJSON(t, map[string]any{"_meta": map[string]any{"kiro": kiro}})
}

// Whichever channel carries the context percentage must keep the ring fresh.
func TestHandleSessionInfoUpdate_ContextPctArrivesOnEitherChannel(t *testing.T) {
	for _, key := range []string{"contextUsage", "usagePercentage"} {
		t.Run(key, func(t *testing.T) {
			deps, _, store := depsWithStore(t, "c1")
			tr := New(rolesOf(deps))

			tr.HandleSessionInfoUpdate(t.Context(), "c1", contextUsageInfo(t, key, 42.5), FrameAttribution{})

			c, ok := store.Get(t.Context(), "c1")
			if !ok {
				t.Fatal("chat c1 missing after session_info_update")
			}
			if c.Usage.ContextPct != 42.5 {
				t.Errorf("Usage.ContextPct after a %s frame = %v, want 42.5", key, c.Usage.ContextPct)
			}
			if !c.Usage.HasRealData {
				t.Errorf("Usage.HasRealData after a %s frame = false, want true", key)
			}
		})
	}
}

// sessionUsageInfo is KAS's session_usage frame: the session's running total by unit, flat beside the kind.
func sessionUsageInfo(t *testing.T, replay bool, credits ...map[string]any) json.RawMessage {
	t.Helper()
	kiro := map[string]any{"kind": "session_usage", "credits": credits}
	if replay {
		kiro["replay"] = true
	}
	return mustJSON(t, map[string]any{"_meta": map[string]any{"kiro": kiro}})
}

// chatOnSession seeds chat c1 running on KAS session sess-1, with the hook to edit it first.
func chatOnSession(t *testing.T, seed func(*marotte.Chat)) (*Translator, *testsupport.InMemoryChatStore) {
	t.Helper()
	deps, _, store := depsWithStore(t, "c1")
	if _, err := store.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.RecordSession("sess-1")
		if seed != nil {
			seed(c)
		}
		return true
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return New(rolesOf(deps)), store
}

func creditsOfChat(t *testing.T, store *testsupport.InMemoryChatStore) marotte.Usage {
	t.Helper()
	c, ok := store.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat c1 missing")
	}
	return c.Usage
}

var onSession = FrameAttribution{SessionID: "sess-1"}

// KAS reports credits as unit "credit" or with the unit key absent; both are spend.
func TestSessionUsage_AnEmptyUnitCountsAsCredits(t *testing.T) {
	for _, unit := range []string{"", "credit"} {
		t.Run("unit_"+unit, func(t *testing.T) {
			tr, store := chatOnSession(t, nil)
			credit := map[string]any{"usage": 0.5}
			if unit != "" {
				credit["unit"] = unit
			}
			tr.HandleSessionInfoUpdate(t.Context(), "c1", sessionUsageInfo(t, false, credit), onSession)

			u := creditsOfChat(t, store)
			if u.Credits != 0.5 {
				t.Errorf("Usage.Credits after a total with unit %q = %v, want 0.5", unit, u.Credits)
			}
			if !u.HasRealData {
				t.Errorf("Usage.HasRealData after a total with unit %q = false, want true", unit)
			}
		})
	}
}

// A total replaces the last one, so a duplicate or a missed frame cannot skew the figure.
func TestSessionUsage_OverwritesTheSessionTotal(t *testing.T) {
	tr, store := chatOnSession(t, nil)
	for _, total := range []float64{0.5, 1.25} {
		tr.HandleSessionInfoUpdate(t.Context(), "c1", sessionUsageInfo(t, false, map[string]any{"usage": total, "unit": "credit"}), onSession)
	}
	if got := creditsOfChat(t, store).Credits; got != 1.25 {
		t.Errorf("Usage.Credits = %v, want the last total 1.25", got)
	}
}

// The chat's own turn_completion feeds the turn footer only; its spend arrives in the session total.
func TestTurnCompletion_DoesNotDoubleBillWithSessionUsage(t *testing.T) {
	tr, store := chatOnSession(t, nil)
	tr.HandleSessionInfoUpdate(t.Context(), "c1", turnSummaryInfo(t, 1200, []map[string]any{{"unit": "credit", "usage": 0.5}}), onSession)
	tr.HandleSessionInfoUpdate(t.Context(), "c1", sessionUsageInfo(t, false, map[string]any{"usage": 0.5, "unit": "credit"}), onSession)
	if got := creditsOfChat(t, store).Credits; got != 0.5 {
		t.Errorf("Usage.Credits = %v, want 0.5", got)
	}
}

// A retired session's straggler cannot overwrite the current session's figure.
func TestSessionUsage_StaleSessionIsIgnored(t *testing.T) {
	tr, store := chatOnSession(t, nil)
	tr.HandleSessionInfoUpdate(t.Context(), "c1", sessionUsageInfo(t, false, map[string]any{"usage": 0.75}), onSession)
	tr.HandleSessionInfoUpdate(t.Context(), "c1", sessionUsageInfo(t, false, map[string]any{"usage": 9}), FrameAttribution{SessionID: "sess-old"})
	if got := creditsOfChat(t, store).Credits; got != 0.75 {
		t.Errorf("Usage.Credits = %v, want 0.75 from the current session", got)
	}
}

// A step's spend is added on top of the chat's session total, and survives the next total.
func TestStepSpend_AddsOnTopOfTheSessionTotal(t *testing.T) {
	tr, store := chatOnSession(t, nil)
	tr.HandleSessionInfoUpdate(t.Context(), "c1", sessionUsageInfo(t, false, map[string]any{"usage": 1.0}), onSession)
	tr.metering.AccumulateSpend(t.Context(), "c1", 0.25)
	tr.HandleSessionInfoUpdate(t.Context(), "c1", sessionUsageInfo(t, false, map[string]any{"usage": 1.5}), onSession)
	if got := creditsOfChat(t, store).Credits; got != 1.75 {
		t.Errorf("Usage.Credits = %v, want the session's 1.5 plus the step's 0.25", got)
	}
}

// A tangent's forked session reports the parent's spend first, replay-stamped at its load; that is not the tangent's.
func TestTangent_InheritedSpendIsNotTheTangents(t *testing.T) {
	tr, store := chatOnSession(t, func(c *marotte.Chat) { c.SpendInherited = true })
	tr.HandleSessionInfoUpdate(t.Context(), "c1", sessionUsageInfo(t, true, map[string]any{"usage": 2.0}), onSession)
	if got := creditsOfChat(t, store).Credits; got != 0 {
		t.Errorf("Usage.Credits after the load-time total = %v, want 0", got)
	}
	tr.HandleSessionInfoUpdate(t.Context(), "c1", sessionUsageInfo(t, false, map[string]any{"usage": 2.4}), onSession)
	if got := creditsOfChat(t, store).Credits; got != 0.4 {
		t.Errorf("Usage.Credits after the tangent's first turn = %v, want 0.4", got)
	}
}

// A parent with no spend sends no load-time total, so a live frame must not be taken for the baseline.
func TestTangent_LiveFrameNeverSetsTheBaseline(t *testing.T) {
	tr, store := chatOnSession(t, func(c *marotte.Chat) { c.SpendInherited = true })
	tr.HandleSessionInfoUpdate(t.Context(), "c1", sessionUsageInfo(t, false, map[string]any{"usage": 0.4}), onSession)
	if got := creditsOfChat(t, store).Credits; got != 0.4 {
		t.Errorf("Usage.Credits = %v, want the tangent's own 0.4", got)
	}
}

// A turn that reported no elapsed time keeps the previous duration.
func TestPersistTurnSummary_ZeroElapsedKeepsThePreviousDuration(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	credit := []map[string]any{{"unit": "credit", "usage": 0.25}}

	tr.HandleSessionInfoUpdate(t.Context(), "c1", turnSummaryInfo(t, 1200, credit), FrameAttribution{})
	tr.HandleSessionInfoUpdate(t.Context(), "c1", turnSummaryInfo(t, 0, credit), FrameAttribution{})

	c, ok := store.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat c1 missing after session_info_update")
	}
	if c.Usage.LastTurnMs != 1200 {
		t.Errorf("Usage.LastTurnMs after a zero-elapsed turn = %v, want 1200 (the previous measurement)", c.Usage.LastTurnMs)
	}
}

// A zero-credit summary must not flip HasRealData, which would report an unconfirmed 0.00.
func TestPersistTurnSummary_ZeroCreditsIsNotRealSpend(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1",
		turnSummaryInfo(t, 1200, []map[string]any{{"unit": "credit", "usage": 0.0}}), FrameAttribution{})

	c, ok := store.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat c1 missing after session_info_update")
	}
	if c.Usage.Credits != 0 {
		t.Errorf("Usage.Credits after a zero-credit turn = %v, want 0", c.Usage.Credits)
	}
	if c.Usage.HasRealData {
		t.Error("Usage.HasRealData after a zero-credit turn = true, want false (nothing was spent)")
	}
}

// An effort-only frame leaves the model catalog standing: KAS sends the two selects
// independently.
func TestHandleConfigOptionUpdate_EffortOnlyFrameKeepsTheModelCatalog(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleConfigOptionUpdate(t.Context(), "c1", configModelUpdate(t, "model-a", []map[string]any{
		{"value": "model-a", "name": "Model A"},
		{"value": "model-b", "name": "Model B"},
	}), FrameAttribution{})
	tr.HandleConfigOptionUpdate(t.Context(), "c1", configEffortUpdate(t, "high", []map[string]any{
		{"value": "high", "name": "High"},
	}), FrameAttribution{})

	c, ok := store.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat c1 missing after config_option_update")
	}
	gotIDs := make([]string, 0, len(deps.catalogModels))
	for _, m := range deps.catalogModels {
		gotIDs = append(gotIDs, m.ID)
	}
	if !slices.Equal(gotIDs, []string{"model-a", "model-b"}) {
		t.Errorf("catalog models after an effort-only frame = %v, want [model-a model-b]", gotIDs)
	}
	if c.EffortActive != "high" {
		t.Errorf("EffortActive = %q, want high (the effort half still applied)", c.EffortActive)
	}
}

func turnEndWithDetails(t *testing.T, stop string, details any) json.RawMessage {
	t.Helper()
	return mustJSON(t, map[string]any{"_meta": map[string]any{"kiro": map[string]any{
		"kind":    "turn_end",
		"turnEnd": map[string]any{"stopReason": stop, "stopDetails": details},
	}}})
}

// TestHandleSessionInfoUpdate_TurnEndLatchesStopDetailsRefusal pins that a turn_end whose
// refusal reached no chunk carries it in stopDetails.
func TestHandleSessionInfoUpdate_TurnEndLatchesStopDetailsRefusal(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	startedTurn(deps, "c1")
	details := map[string]any{"refusal": map[string]any{
		"category": "cyber", "explanation": "x", "recommendedModel": "m",
	}}

	tr.HandleSessionInfoUpdate(t.Context(), "c1", turnEndWithDetails(t, "refusal", details), FrameAttribution{})

	got := closedRefusal(t, deps, "c1")
	want := marotte.RefusalInfo{Category: "cyber", Explanation: "x", RecommendedModel: "m"}
	if got == nil || *got != want {
		t.Errorf("turn_close.refusal = %+v, want %+v", got, want)
	}
	wantBrackets := []turnBracket{{chat: "c1", kind: "end", stop: marotte.StopReasonRefusal}}
	if !slices.Equal(deps.brackets, wantBrackets) {
		t.Errorf("brackets = %+v, want %+v", deps.brackets, wantBrackets)
	}
}

// TestHandleSessionInfoUpdate_TurnEndRefusalIsFirstWins pins that a turn_end cannot
// relabel a refusal a chunk latched first.
func TestHandleSessionInfoUpdate_TurnEndRefusalIsFirstWins(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	startedTurn(deps, "c1").SetRefusal(&marotte.RefusalInfo{Category: "a"})
	details := map[string]any{"refusal": map[string]any{"category": "b"}}

	tr.HandleSessionInfoUpdate(t.Context(), "c1", turnEndWithDetails(t, "refusal", details), FrameAttribution{})

	if got := closedRefusal(t, deps, "c1"); got == nil || got.Category != "a" {
		t.Errorf("turn_close.refusal = %+v, want category a", got)
	}
}

// TestHandleSessionInfoUpdate_TurnEndNonObjectStopDetailsStillCloses pins that a shape
// change costs the refusal read, never the bracket.
func TestHandleSessionInfoUpdate_TurnEndNonObjectStopDetailsStillCloses(t *testing.T) {
	for _, tt := range []struct {
		name    string
		details any
	}{
		{name: "a bare string", details: "The upstream model dropped the stream."},
		{name: "an array", details: []any{1, 2, 3}},
		{name: "an object with no refusal", details: map[string]any{"message": "x"}},
		{name: "a refusal that is not an object", details: map[string]any{"refusal": "no"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			deps, _, _ := depsWithStore(t, "c1")
			tr := New(rolesOf(deps))
			startedTurn(deps, "c1")

			tr.HandleSessionInfoUpdate(t.Context(), "c1", turnEndWithDetails(t, "error", tt.details), FrameAttribution{})

			want := []turnBracket{{chat: "c1", kind: "end", stop: marotte.StopReasonError}}
			if !slices.Equal(deps.brackets, want) {
				t.Errorf("brackets = %+v, want %+v", deps.brackets, want)
			}
			if got := closedRefusal(t, deps, "c1"); got != nil {
				t.Errorf("turn_close.refusal = %+v, want none", got)
			}
		})
	}
}

// TestHandleSessionInfoUpdate_TurnEndWithoutStopDetailsSaysNothing is the control, the
// case every measured build sends.
func TestHandleSessionInfoUpdate_TurnEndWithoutStopDetailsSaysNothing(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", turnBracketInfo(t, "turn_end"), FrameAttribution{})

	want := []turnBracket{{chat: "c1", kind: "end", stop: marotte.StopReason("end_turn")}}
	if !slices.Equal(deps.brackets, want) {
		t.Errorf("brackets = %+v, want %+v", deps.brackets, want)
	}
}

func TestHandleConfigOptionUpdate_RefreshesTheEntitlementSet(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	_, _ = store.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.ServedModelIDs = []string{"old-a", "old-b"}
		return true
	})

	tr.HandleConfigOptionUpdate(t.Context(), "c1", configModelUpdate(t, "new-a", []map[string]any{
		{"value": "new-a", "name": "New A"},
		{"value": "new-b", "name": "New B"},
		{"value": "new-c", "name": "New C"},
		{"value": "new-d", "name": "New D"},
	}), FrameAttribution{})

	c, _ := store.Get(t.Context(), "c1")
	if !slices.Equal(c.ServedModelIDs, []string{"new-a", "new-b", "new-c", "new-d"}) {
		t.Errorf("ServedModelIDs = %v, want refreshed four-model set", c.ServedModelIDs)
	}
}

// TestHandleConfigOptionUpdate_AColdCatalogPinChangesNothing pins kiro-cli 2.32.0's cold pin (one Model
// choice equal to currentValue, one Effort choice) as no catalog: read as one, refuseUnservedModel turns down
// every other model until the catalog loads, and for good when its refresh fails.
func TestHandleConfigOptionUpdate_AColdCatalogPinChangesNothing(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	tr.HandleConfigOptionUpdate(t.Context(), "c1", configModelAndEffortUpdate(t, "opus", "high"), FrameAttribution{})
	before, _ := store.Get(t.Context(), "c1")

	tr.HandleConfigOptionUpdate(t.Context(), "c1", mustJSON(t, map[string]any{
		"configOptions": []map[string]any{
			{"id": "model", "type": "select", "currentValue": "opus", "options": []map[string]any{{"value": "opus", "name": "Opus"}}},
			{"id": "effortLevel", "type": "select", "currentValue": "high", "options": []map[string]any{{"value": "high", "name": "High"}}},
		},
	}), FrameAttribution{})

	c, _ := store.Get(t.Context(), "c1")
	if !slices.Equal(c.ServedModelIDs, []string{"opus", "fable"}) {
		t.Errorf("ServedModelIDs = %v, want [opus fable] kept", c.ServedModelIDs)
	}
	if !slices.Equal(modelIDs(deps.catalogModels), []string{"opus", "fable"}) {
		t.Errorf("catalog = %v, want [opus fable] kept", modelIDs(deps.catalogModels))
	}
	if !sameEffortLevels(c.EffortLevels, before.EffortLevels) || len(c.EffortLevels) != 2 {
		t.Errorf("EffortLevels = %v, want the two tiers kept", c.EffortLevels)
	}
}

// An end-of-life id stays in the entitlement set; the picker's filtering is the bridge's.
func TestHandleConfigOptionUpdate_KeepsEndOfLifeIDsInTheServedSet(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleConfigOptionUpdate(t.Context(), "c1", configModelUpdate(t, "new", []map[string]any{
		{"value": "old", "name": "Old", "description": "[Deprecated]"},
		{"value": "new", "name": "New"},
	}), FrameAttribution{})

	c, _ := store.Get(t.Context(), "c1")
	if !slices.Equal(c.ServedModelIDs, []string{"old", "new"}) {
		t.Errorf("ServedModelIDs = %v, want [old new]", c.ServedModelIDs)
	}
}

// configModelAndEffortUpdate is one frame carrying both selects, the shape a session
// reports after a model switch.
func configModelAndEffortUpdate(t *testing.T, model, effort string) []byte {
	t.Helper()
	return mustJSON(t, map[string]any{
		"configOptions": []map[string]any{
			{
				"id":           "model",
				"type":         "select",
				"currentValue": model,
				"options":      []map[string]any{{"value": "opus", "name": "Opus"}, {"value": "fable", "name": "Fable"}},
			},
			{
				"id":           "effortLevel",
				"type":         "select",
				"currentValue": effort,
				"options":      []map[string]any{{"value": "high", "name": "High"}, {"value": "max", "name": "Max"}},
			},
		},
	})
}

// A step's config frame arrives under the LAUNCHING chat's id with the step's own model
// and effort: the catalog refreshes, but the current values must not become the chat's.
func TestHandleConfigOptionUpdate_AStepsFrameRefreshesTheCatalogAndWritesNoSessionState(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	_, _ = store.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Model = "opus"
		c.EffortActive = "max"
		c.EffortLevels = []marotte.SessionEffortLevel{{ID: "max", Name: "Max"}}
		return true
	})

	for _, attr := range []FrameAttribution{
		{Step: true, SessionID: "sess-step", RunID: "wf_1", NodePath: "wf_1/step"},
	} {
		tr.HandleConfigOptionUpdate(t.Context(), "c1", configModelAndEffortUpdate(t, "fable", "high"), attr)

		c, _ := store.Get(t.Context(), "c1")
		if c.Model != "opus" {
			t.Errorf("attr %+v: Model = %q, want opus (a foreign session's current model stays off the chat)", attr, c.Model)
		}
		if c.EffortActive != "max" {
			t.Errorf("attr %+v: EffortActive = %q, want max", attr, c.EffortActive)
		}
		if len(c.EffortLevels) != 1 || c.EffortLevels[0].ID != "max" {
			t.Errorf("attr %+v: EffortLevels = %v, want the chat's own [max]", attr, c.EffortLevels)
		}
		if !slices.Equal(c.ServedModelIDs, []string{"opus", "fable"}) {
			t.Errorf("attr %+v: ServedModelIDs = %v, want [opus fable] (the catalog half applies)", attr, c.ServedModelIDs)
		}
	}
	if len(deps.catalogModels) != 2 {
		t.Errorf("catalog models = %+v, want the two the step reported", deps.catalogModels)
	}

	tr.HandleConfigOptionUpdate(t.Context(), "c1", configModelAndEffortUpdate(t, "fable", "high"), FrameAttribution{})

	c, _ := store.Get(t.Context(), "c1")
	if c.Model != "fable" {
		t.Errorf("own frame: Model = %q, want fable", c.Model)
	}
	if c.EffortActive != "high" {
		t.Errorf("own frame: EffortActive = %q, want high", c.EffortActive)
	}
	if len(c.EffortLevels) != 2 {
		t.Errorf("own frame: EffortLevels = %v, want the two the session reported", c.EffortLevels)
	}
}

func modelSwitchesOf(t *testing.T, entries []marotte.Entry) []marotte.EntryModelSwitched {
	t.Helper()
	var out []marotte.EntryModelSwitched
	for _, e := range entriesOfKind(entries, marotte.EntryKindModelSwitched) {
		var p marotte.EntryModelSwitched
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("decode model_switched %q: %v", e.ID, err)
		}
		out = append(out, p)
	}
	return out
}

func configModelOffering(t *testing.T, current string, offered ...string) []byte {
	t.Helper()
	opts := make([]map[string]any, 0, len(offered))
	for _, id := range offered {
		opts = append(opts, map[string]any{"value": id, "name": id})
	}
	return mustJSON(t, map[string]any{"configOptions": []map[string]any{
		{"id": "model", "type": "select", "currentValue": current, "options": opts},
	}})
}

func TestHandleConfigOptionUpdate_AnUnsolicitedRepinIsRecorded(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	_, _ = store.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Model = "opus"
		c.Effort = "max"
		return true
	})

	tr.HandleConfigOptionUpdate(t.Context(), "c1", configModelOffering(t, "fable", "fable", marotte.ModelAuto), FrameAttribution{})

	c, _ := store.Get(t.Context(), "c1")
	if c.Model != "fable" {
		t.Errorf("Model = %q, want fable", c.Model)
	}
	if c.Effort != "" {
		t.Errorf("Effort = %q, want empty (the tier was chosen for opus)", c.Effort)
	}
	got := modelSwitchesOf(t, deps.between["c1"])
	want := []marotte.EntryModelSwitched{{From: "opus", To: "fable", Reason: marotte.ModelSwitchReasonUnavailable}}
	if !slices.Equal(got, want) {
		t.Errorf("model_switched entries = %+v, want %+v", got, want)
	}
}

// A recorded session's reported level becomes the chat's when the record holds none: a repin
// drops the old model's tier for the new model's, and a level held already stands.
func TestHandleConfigOptionUpdate_AdoptsTheRunningLevelWhenTheRecordHoldsNone(t *testing.T) {
	tests := map[string]struct {
		model, effort, session string
		want                   string
	}{
		"a session with no level adopts the reported one": {model: "fable", session: "sess-1", want: "medium"},
		"a repin replaces the old model's tier":           {model: "opus", effort: "max", session: "sess-1", want: "medium"},
		"a level the record holds stands":                 {model: "fable", effort: "max", session: "sess-1", want: "max"},
		"a session still opening adopts nothing":          {model: "fable", want: ""},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			deps, _, store := depsWithStore(t, "c1")
			tr := New(rolesOf(deps))
			_, _ = store.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
				c.Model, c.Effort = test.model, test.effort
				c.RecordSession(test.session)
				return true
			})
			raw := mustJSON(t, map[string]any{"configOptions": []map[string]any{
				{"id": "model", "type": "select", "currentValue": "fable", "options": []map[string]any{{"value": "fable", "name": "fable"}, {"value": "opus", "name": "opus"}}},
				{"id": "effortLevel", "type": "select", "currentValue": "medium", "options": []map[string]any{{"value": "medium", "name": "Medium"}}},
			}})

			tr.HandleConfigOptionUpdate(t.Context(), "c1", raw, FrameAttribution{})

			if c, _ := store.Get(t.Context(), "c1"); c.Effort != test.want {
				t.Errorf("Effort = %q, want %q", c.Effort, test.want)
			}
		})
	}
}

func TestHandleConfigOptionUpdate_AMoveWhileTheOldModelIsOfferedIsStillARepin(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	_, _ = store.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Model = "opus"
		c.Effort = "max"
		return true
	})

	tr.HandleConfigOptionUpdate(t.Context(), "c1", configModelOffering(t, "fable", "opus", "fable"), FrameAttribution{})

	got := modelSwitchesOf(t, deps.between["c1"])
	want := []marotte.EntryModelSwitched{{From: "opus", To: "fable", Reason: marotte.ModelSwitchReasonUnavailable}}
	if !slices.Equal(got, want) {
		t.Errorf("model_switched entries = %+v, want %+v", got, want)
	}
	if c, _ := store.Get(t.Context(), "c1"); c.Effort != "" {
		t.Errorf("Effort = %q, want empty (the tier was chosen for opus)", c.Effort)
	}
}

type writeFailingStore struct{ nopChatRecords }

func (writeFailingStore) Mutate(_ context.Context, _ marotte.ChatID, fn func(*marotte.Chat, bool) bool) (string, error) {
	fn(&marotte.Chat{Model: "opus"}, true)
	return "", errBoom
}

func TestHandleConfigOptionUpdate_AFailedWriteRecordsNoRepin(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	deps.store = writeFailingStore{}
	tr := New(rolesOf(deps))

	tr.HandleConfigOptionUpdate(t.Context(), "c1", configModelOffering(t, "fable", "fable", marotte.ModelAuto), FrameAttribution{})

	if got := modelSwitchesOf(t, deps.between["c1"]); len(got) != 0 {
		t.Errorf("model_switched entries = %+v, want none", got)
	}
}

func TestHandleConfigOptionUpdate_RecordsNoRepinWhenTheMoveIsNotKASs(t *testing.T) {
	cases := []struct {
		attr       FrameAttribution
		name       string
		model      string
		pending    string
		effortOnly bool
	}{
		{name: "from_empty", model: ""},
		{name: "from_auto", model: marotte.ModelAuto},
		{name: "same_model", model: "fable"},
		{name: "pending_pick", model: "opus", pending: "fable"},
		{name: "step_frame", model: "opus", attr: FrameAttribution{Step: true, SessionID: "sess-step", RunID: "wf_1", NodePath: "wf_1/step"}},
		{name: "no_model_option", model: "opus", effortOnly: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, _, store := depsWithStore(t, "c1")
			tr := New(rolesOf(deps))
			_, _ = store.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
				c.Model = tc.model
				c.PendingModel = tc.pending
				c.Effort = "max"
				return true
			})

			frame := configModelAndEffortUpdate(t, "fable", "high")
			if tc.effortOnly {
				frame = configEffortUpdate(t, "high", []map[string]any{{"value": "high", "name": "High"}})
			}
			tr.HandleConfigOptionUpdate(t.Context(), "c1", frame, tc.attr)

			if got := modelSwitchesOf(t, deps.between["c1"]); len(got) != 0 {
				t.Errorf("model_switched entries = %+v, want none", got)
			}
			c, _ := store.Get(t.Context(), "c1")
			if c.Effort != "max" {
				t.Errorf("Effort = %q, want max (no repin, so the tier stands)", c.Effort)
			}
		})
	}
}

// displayErrorFrame builds a display_error frame the way the live wire spreads it:
// the nested block plus its fields flat beside it.
func displayErrorFrame(t *testing.T, message, errorType string) json.RawMessage {
	t.Helper()
	return mustJSON(t, map[string]any{"_meta": map[string]any{"kiro": map[string]any{
		"kind": "display_error", "message": message, "errorType": errorType,
		"displayError": map[string]any{"message": message, "errorType": errorType},
	}}})
}

// A display_error latches on the chat's own open turn, last write winning, so the
// close of a broken turn has the engine's sentence to say.
func TestHandleSessionInfoUpdate_DisplayErrorLatchesOnTheOwnTurn(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	turn := startedTurn(deps, "c1")

	tr.HandleSessionInfoUpdate(t.Context(), "c1", displayErrorFrame(t, "An earlier attempt failed.", "ge"), FrameAttribution{})
	tr.HandleSessionInfoUpdate(t.Context(), "c1", displayErrorFrame(t, "Your connection was interrupted.", "ye"), FrameAttribution{})

	got := turn.EngineError()
	if got == nil || got.Message != "Your connection was interrupted." || got.ErrorType != "ye" {
		t.Errorf("EngineError() = %+v, want the last frame's account", got)
	}
}

// An MCP connect failure is broadcast to every session with no turn; the MCP
// status channel already reports it, so it must not become a turn's reason.
func TestHandleSessionInfoUpdate_DisplayErrorDropsAnMCPConnectFailure(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	turn := startedTurn(deps, "c1")

	tr.HandleSessionInfoUpdate(t.Context(), "c1", displayErrorFrame(t, "Failed to connect to MCP server x.", "mcp_connection_error"), FrameAttribution{})

	if got := turn.EngineError(); got != nil {
		t.Errorf("EngineError() = %+v, want nil for an MCP connect failure", got)
	}
}

// A step's display_error is not the chat's own account.
func TestHandleSessionInfoUpdate_DisplayErrorIgnoresAForeignFrame(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	turn := startedTurn(deps, "c1")

	tr.HandleSessionInfoUpdate(t.Context(), "c1", displayErrorFrame(t, "Step stalled.", "ge"), FrameAttribution{Step: true, SessionID: "sess-step", RunID: "wf_1"})

	if got := turn.EngineError(); got != nil {
		t.Errorf("EngineError() = %+v, want nil for a step's frame", got)
	}
}

// The reported tiers, running tier and thinking are the reporting model's: a frame naming a model
// without an effort option (a tierless one) clears them, a frame naming neither leaves them.
func TestHandleConfigOptionUpdate_ReportedEffortIsTheNamedModels(t *testing.T) {
	cases := []struct {
		name       string
		model      string
		frame      func(t *testing.T) []byte
		wantActive string
		wantLevels int
	}{
		{name: "a move onto auto", model: "opus", frame: func(t *testing.T) []byte {
			return configModelOffering(t, marotte.ModelAuto, marotte.ModelAuto, "opus")
		}},
		{name: "the current model, now tierless", model: "opus", frame: func(t *testing.T) []byte {
			return configModelOffering(t, "opus", "opus", marotte.ModelAuto)
		}},
		{name: "a frame naming no model", model: "opus", wantActive: "high", wantLevels: 2, frame: func(t *testing.T) []byte {
			return mustJSON(t, map[string]any{"configOptions": []map[string]any{
				{"id": "thinking", "type": "select", "currentValue": "on"},
			}})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, _, store := depsWithStore(t, "c1")
			tr := New(rolesOf(deps))
			_, _ = store.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
				c.Model, c.EffortActive = tc.model, "high"
				c.EffortLevels = []marotte.SessionEffortLevel{{ID: "low"}, {ID: "high"}}
				return true
			})

			tr.HandleConfigOptionUpdate(t.Context(), "c1", tc.frame(t), FrameAttribution{})

			c, _ := store.Get(t.Context(), "c1")
			if c.EffortActive != tc.wantActive || len(c.EffortLevels) != tc.wantLevels {
				t.Errorf("{EffortActive %q, EffortLevels %v}, want {%q, %d tiers}",
					c.EffortActive, c.EffortLevels, tc.wantActive, tc.wantLevels)
			}
		})
	}
}

// KAS 3.1.0's real session_usage frame, unitPlural included, reaches every door without a census Warn. The chat
// door takes its total as the figure; the step and run-host doors bill nothing (a step bills per turn_completion).
// Serial: slog's default is process-global.
func TestSessionInfoUpdate_SessionUsageIsCensusQuiet(t *testing.T) {
	frame := sessionUsageInfo(t, false, map[string]any{"usage": 1.25, "unit": "credit", "unitPlural": "credits"})
	step := FrameAttribution{Step: true, RunID: "wf", NodePath: "wf/s"}
	for _, tt := range []struct {
		name        string
		feed        func(*Translator)
		wantCredits float64
	}{
		{name: "chat", wantCredits: 1.25, feed: func(tr *Translator) {
			tr.HandleSessionInfoUpdate(t.Context(), "c1", turnSummaryInfo(t, 900, []map[string]any{{"unit": "credit", "usage": 0.5}}), onSession)
			tr.HandleSessionInfoUpdate(t.Context(), "c1", frame, onSession)
		}},
		{name: "step", feed: func(tr *Translator) { tr.HandleSessionInfoUpdate(t.Context(), "c1", frame, step) }},
		{name: "run host", feed: func(tr *Translator) { tr.HandleStepInfoUpdate(t.Context(), "c1", frame, step) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resetCensus(t)
			var buf bytes.Buffer
			captureSlog(t, &buf)
			tr, store := chatOnSession(t, nil)
			tt.feed(tr)
			if out := buf.String(); strings.Contains(out, "level=WARN") {
				t.Errorf("a session_usage frame logged a Warn: %s", out)
			}
			if got := creditsOfChat(t, store).Credits; got != tt.wantCredits {
				t.Errorf("credits = %v, want %v", got, tt.wantCredits)
			}
		})
	}
}
