package ruleengine

import (
	"maps"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rule"
)

// The settle's write names, by rule, each event it drops after its last try, and only those: an attempt over a
// record that no longer holds the event names nothing, even after an earlier attempt that dropped it.
func TestApplyVerdicts_NamesOnlyTheEventsItDropsAfterTheirLastTry(t *testing.T) {
	k := openedPrompt{rule: "r1", event: "manual:op-1"}
	holding := func() *marotte.Chat {
		r := armedRule("r1", specOf("Tidy", nil, everyT("t1", 5, "")), 0)
		r.Status.Queue = []rule.QueuedEvent{{EventID: k.event, Trigger: "t1", Stage: rule.StageFired, Tries: maxTries - 1}}
		return &marotte.Chat{Bot: &marotte.Bot{Rules: []rule.Rule{r}}}
	}
	failed := map[openedPrompt]string{}
	apply := applyVerdicts(map[openedPrompt]verdict{k: launchFailedEvent}, time.Now(), failed)

	c := holding()
	if !apply(c) || len(c.Bot.Rules[0].Status.Queue) != 0 {
		t.Fatalf("queue %v, want the event dropped", eventIDs(c.Bot.Rules[0].Status.Queue))
	}
	if want := map[openedPrompt]string{k: "Tidy"}; !maps.Equal(failed, want) {
		t.Errorf("failed %v after the drop, want %v", failed, want)
	}

	gone := holding()
	gone.Bot.Rules[0].Status.Queue = nil
	apply(gone)
	if len(failed) != 0 {
		t.Errorf("failed %v over a record without the event, want none", failed)
	}

	unfired := holding()
	unfired.Bot.Rules[0].Status.Queue[0].Stage = rule.StageNew
	apply(unfired)
	if len(failed) != 0 || len(unfired.Bot.Rules[0].Status.Queue) != 1 {
		t.Errorf("failed %v queue %v over an event not at fired, want it kept and not named",
			failed, eventIDs(unfired.Bot.Rules[0].Status.Queue))
	}
}
