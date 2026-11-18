package ruleengine

import (
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/rule"
	"github.com/cplieger/marotte/internal/testsupport"
)

// Every trigger label the engine counts under is on the page before its first evaluation: each built kind and Run
// now's.
func TestEngine_TheEvaluationSeriesPresetEveryKind(t *testing.T) {
	want := []string{manualKind}
	for _, k := range rule.NewCatalog().Kinds {
		want = append(want, string(k.Kind))
	}
	slices.Sort(want)
	got := slices.Compact(slices.Sorted(slices.Values(testsupport.PresetLabelValues("marotte_rule_evaluations_total", "trigger"))))
	if !slices.Equal(got, want) {
		t.Errorf("preset trigger labels %v, want %v", got, want)
	}
}
