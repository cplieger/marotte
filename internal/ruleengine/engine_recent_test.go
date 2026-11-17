package ruleengine

import (
	"strconv"
	"testing"
	"testing/synctest"
	"time"
)

// A time trigger's slot whose id the rule already evaluated is not evaluated again, as after a re-arm under a clock
// that went back; the anchor still moves, so the next slot fires.
func TestEngine_ASlotTheRuleAlreadyEvaluatedFiresNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		r := armedRule("r1", specOf("Tidy", nil, everyT("t1", 5, "")), h.at(0))
		r.Status.Recent = []string{dueID("t1", h.at(5*time.Minute))}
		h.seedBot("b1", r)
		h.scan()
		h.start()
		h.advance(5 * time.Minute)
		h.wantSent("at the evaluated slot", 0)
		if got := h.rule("b1", "r1").Status.Triggers["t1"].LastAt; got != h.at(5*time.Minute) {
			t.Errorf("last_at %d, want the pass at %d", got, h.at(5*time.Minute))
		}
		h.advance(5 * time.Minute)
		got := h.wantSent("at the next slot", 1)
		if eventIDOf(got[0]) != dueID("t1", h.at(10*time.Minute)) {
			t.Errorf("delivered %q, want the 10 min slot", eventIDOf(got[0]))
		}
	})
}

// A rule remembers the last 100 event ids it evaluated, newest last (design §3.3).
func TestEngine_RemembersTheLast100EventIDs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		r := weekly(h)
		for i := range 100 {
			r.Status.Recent = append(r.Status.Recent, "manual:old-"+strconv.Itoa(i))
		}
		h.seedBot("b1", r)
		h.scan()
		h.start()
		h.runNow("op-1")
		got := h.rule("b1", "r1").Status.Recent
		if len(got) != 100 {
			t.Fatalf("recent holds %d ids %v, want 100", len(got), got)
		}
		if got[0] != "manual:old-1" || got[99] != "manual:op-1" {
			t.Errorf("recent runs from %q to %q, want manual:old-1 to manual:op-1", got[0], got[99])
		}
	})
}
