package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
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

// The per-model hasEffort lands on SessionModel.HasEffort so the picker can hide the
// effort row; a choice with no _meta decodes as false.
func TestHandleConfigOptionUpdate_PlumbsHasEffort(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	raw := configModelUpdate(t, "model-a", []map[string]any{
		{"value": "model-a", "name": "Model A", "_meta": map[string]any{"kiro": map[string]any{"hasEffort": true, "rateMultiplier": 1.0}}},
		{"value": "model-b", "name": "Model B", "_meta": map[string]any{"kiro": map[string]any{"hasEffort": false, "rateMultiplier": 2.0}}},
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
	want := map[string]bool{"model-a": true, "model-b": false, "model-c": false}
	if len(deps.catalogModels) != len(want) {
		t.Fatalf("catalog models len = %d, want %d: %+v", len(deps.catalogModels), len(want), deps.catalogModels)
	}
	for _, m := range deps.catalogModels {
		exp, known := want[m.ID]
		if !known {
			t.Errorf("unexpected model %q in catalog", m.ID)
			continue
		}
		if m.HasEffort != exp {
			t.Errorf("model %q HasEffort = %v, want %v", m.ID, m.HasEffort, exp)
		}
	}
}

// configEffortUpdate carries the `effortLevel` option kiro-cli's TUI builds its picker
// from; there is no per-model tier list on the wire.
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
	// The chat's CHOICE is untouched, or a service default would pin into later sessions
	// through StartOpts.Effort.
	if c.Effort != "" {
		t.Errorf("Effort = %q, want empty (the option is not a choice)", c.Effort)
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

// Absent and malformed meta both decode to the zero value, read as "not plumbed".
func TestChoiceMeta(t *testing.T) {
	cases := []struct {
		name        string
		meta        string
		wantHas     bool
		wantDefault string
		wantRate    float64
	}{
		{name: "nil meta"},
		{name: "empty object", meta: `{}`},
		{name: "hasEffort only", meta: `{"kiro":{"hasEffort":true}}`, wantHas: true},
		{name: "hasEffort false", meta: `{"kiro":{"hasEffort":false}}`},
		{name: "kiro without hasEffort", meta: `{"kiro":{"rateMultiplier":2}}`, wantRate: 2},
		{
			// The 2.18.0 shape: a default tier, no capability flag.
			name:        "default tier, no hasEffort",
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
			if got.Kiro.HasEffort != tc.wantHas {
				t.Errorf("HasEffort = %v, want %v", got.Kiro.HasEffort, tc.wantHas)
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
		{name: "known compaction marker", kind: "summarization_separator", wantWarn: false},
		{name: "consumed, but its sub-block did not decode", kind: "pending_interaction", wantWarn: true},
		{name: "reaches the wire via SessionInfoEmitter, not a build call site", kind: "repositories_update", wantWarn: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			restore := captureSlog(&buf)
			defer restore()

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
	restore := captureSlog(&buf)
	defer restore()

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

// TestMaterialPctDelta pins the gate on a context-percentage rewrite: a sub-point move is
// dropped (the ring cannot render it), a tier crossing is kept.
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
		// marotte's own client thresholds (70, 90 recolour the ring; 95 stops input), not KAS's 80/95.
		"tiny move crossing 70":              {69.9, 70.0, true},
		"tiny move crossing 70 downward":     {70.0, 69.9, true},
		"tiny move crossing 90":              {89.9, 90.0, true},
		"tiny move crossing 95":              {94.9, 95.0, true},
		"tiny move crossing 95 downward":     {95.0, 94.9, true},
		"tiny move inside the warning band":  {75.0, 75.2, false},
		"tiny move inside the critical band": {91.0, 91.3, false},
		"tiny move above the cutoff":         {96.0, 96.3, false},
		// 80 is KAS's boundary and not one of marotte's, so a sub-point move
		// across it is correctly ignored.
		"tiny move crossing KAS's 80 is not material": {79.9, 80.0, false},
		"from zero is material":                       {0, 1, true},
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

// KAS reports credits as unit "credit" or with the unit key absent; both are spend.
func TestPersistTurnSummary_AnEmptyUnitCountsAsCredits(t *testing.T) {
	for _, unit := range []string{"", "credit"} {
		t.Run("unit_"+unit, func(t *testing.T) {
			deps, _, store := depsWithStore(t, "c1")
			tr := New(rolesOf(deps))
			summary := map[string]any{"usage": 0.5}
			if unit != "" {
				summary["unit"] = unit
			}

			tr.HandleSessionInfoUpdate(t.Context(), "c1", turnSummaryInfo(t, 1200, []map[string]any{summary}), FrameAttribution{})

			c, ok := store.Get(t.Context(), "c1")
			if !ok {
				t.Fatal("chat c1 missing after session_info_update")
			}
			if c.Usage.Credits != 0.5 {
				t.Errorf("Usage.Credits after a summary with unit %q = %v, want 0.5", unit, c.Usage.Credits)
			}
			if !c.Usage.HasRealData {
				t.Errorf("Usage.HasRealData after a summary with unit %q = false, want true", unit)
			}
		})
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

// turnEndWithDetails builds a turn_end frame carrying stopDetails verbatim.
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
		{SubSessionID: "sess-sub", SessionID: "sess-sub"},
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

	tr.HandleConfigOptionUpdate(t.Context(), "c1", configModelOffering(t, "fable", "fable"), FrameAttribution{})

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

	tr.HandleConfigOptionUpdate(t.Context(), "c1", configModelOffering(t, "fable", "fable"), FrameAttribution{})

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

// A step's or a subagent's display_error is not the chat's own account.
func TestHandleSessionInfoUpdate_DisplayErrorIgnoresAForeignFrame(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	turn := startedTurn(deps, "c1")

	tr.HandleSessionInfoUpdate(t.Context(), "c1", displayErrorFrame(t, "Sub-agent stalled.", "ge"), FrameAttribution{SubSessionID: "sess-sub"})

	if got := turn.EngineError(); got != nil {
		t.Errorf("EngineError() = %+v, want nil for a subagent's frame", got)
	}
}
