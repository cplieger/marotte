package obs

import (
	"strings"
	"testing"
)

// The rule families carry no chat or rule label, and every label value an alert or dashboard reads is on the page
// before its first event, so increase() sees the first one.
func TestRuleMetrics_EveryLabelValueIsOnThePageBeforeItsFirstEvent(t *testing.T) {
	body := scrape().Body.String()
	var want []string
	for _, trigger := range []string{"every", "at", "manual"} {
		for _, outcome := range []string{"fired", "skipped"} {
			want = append(want, `marotte_rule_evaluations_total{outcome="`+outcome+`",trigger="`+trigger+`"} `)
		}
	}
	for _, outcome := range []string{"delivered", "retry", "failed", "dropped"} {
		want = append(want, `marotte_rule_deliveries_total{outcome="`+outcome+`"} `)
	}
	want = append(want, "marotte_rule_queue_events ", "marotte_rule_engine_write_failing ")
	for _, series := range want {
		if !strings.Contains(body, "\n"+series) {
			t.Errorf("the page lacks %q before any event", series)
		}
	}
	for line := range strings.Lines(body) {
		if strings.HasPrefix(line, "marotte_rule_") && (strings.Contains(line, "chat") || strings.Contains(line, "rule_id")) {
			t.Errorf("%q carries a chat or rule label", strings.TrimSpace(line))
		}
	}
}

// Each recorder moves its own series and no other.
func TestRuleMetrics_RecordEachEvent(t *testing.T) {
	read := func(series string) string {
		v, ok := sample(series)
		if !ok {
			t.Fatalf("the page lacks %s", series)
		}
		return v
	}
	fired := `marotte_rule_evaluations_total{outcome="fired",trigger="at"}`
	skipped := `marotte_rule_evaluations_total{outcome="skipped",trigger="at"}`
	failed := `marotte_rule_deliveries_total{outcome="failed"}`
	firedBefore, skippedBefore, failedBefore := read(fired), read(skipped), read(failed)

	RuleEvaluated("at", RuleFired)
	RuleDelivery(RuleDeliveryFailed)
	RuleQueueEvents(7)
	RuleEngineWriteFailing(true)

	if read(fired) == firedBefore || read(skipped) != skippedBefore {
		t.Errorf("fired %s→%s skipped %s→%s, want only fired moved", firedBefore, read(fired), skippedBefore, read(skipped))
	}
	if read(failed) == failedBefore {
		t.Errorf("failed deliveries %s, want one more", read(failed))
	}
	if got := read("marotte_rule_queue_events"); got != "7" {
		t.Errorf("queue events = %s, want 7", got)
	}
	if got := read("marotte_rule_engine_write_failing"); got != "1" {
		t.Errorf("write failing = %s, want 1", got)
	}
	RuleEngineWriteFailing(false)
	if got := read("marotte_rule_engine_write_failing"); got != "0" {
		t.Errorf("write failing = %s after the resume, want 0", got)
	}
}
