package ruleengine

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/obs"
	"github.com/cplieger/marotte/internal/rule"
)

// A rule due before the store's first complete scan waits for it: an index built from a partial set could miss a
// Bot's rules, and the scan's completion starts every rule found.
func TestEngine_IndexesOnlyOnceTheScanIsComplete(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.seedBot("b1", armedRule("r1", specOf("Tidy", nil, everyT("t1", 5, "")), h.at(0)))
		h.start()
		h.advance(10 * time.Minute)
		h.wantSent("before the scan", 0)

		h.scan()
		h.advance(time.Minute)
		got := h.wantSent("after the scan", 1)
		if id := eventIDOf(got[0]); id != dueID("t1", h.at(5*time.Minute)) {
			t.Errorf("event %q, want the overdue slot's %q", id, dueID("t1", h.at(5*time.Minute)))
		}
	})
}

func TestEngine_EvaluatesNothingBeforeTheFirstReady(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.seedBot("b1", armedRule("r1", specOf("Tidy", nil, everyT("t1", 5, "")), h.at(0)))
		h.scan()
		h.ready.Store(false)
		h.start()
		h.advance(10 * time.Minute)
		h.wantSent("before the engine is ready", 0)
		if r := h.rule("b1", "r1"); r.Status.LastFire != nil || r.Status.Triggers["t1"].LastAt != 0 {
			t.Errorf("evaluated before the first ready: %+v", r.Status)
		}

		h.ready.Store(true)
		h.advance(time.Minute)
		h.wantSent("once ready", 1)
	})
}

// A due time trigger fires at its due time, not a tick later, and its prompt names the rule and the event.
func TestEngine_FiresATimeTriggerAtItsDueTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.seedBot("b1", armedRule("r1", specOf("Tidy", nil, everyT("t1", 5, "")), h.at(0)))
		h.scan()
		h.start()
		h.advance(5*time.Minute - time.Second)
		h.wantSent("a second early", 0)

		h.advance(time.Second)
		got := h.wantSent("on time", 1)
		want := marotte.Caller{
			Kind: marotte.CallerKindRule, Label: "Tidy", Rule: "r1",
			Event: dueID("t1", h.at(5*time.Minute)), Depth: 1,
		}
		if got[0].from == nil || *got[0].from != want {
			t.Errorf("caller %+v, want %+v", got[0].from, want)
		}
		if got[0].opts.DisplayText != "Rule: Tidy" {
			t.Errorf("display text %q, want %q", got[0].opts.DisplayText, "Rule: Tidy")
		}
		if !strings.HasPrefix(got[0].text, `Automatic prompt from marotte: rule "Tidy"`) ||
			!strings.Contains(got[0].text, testPrompt) {
			t.Errorf("text %q, want the rendered prompt", got[0].text)
		}
		if !got[0].at.Equal(h.t0.Add(5 * time.Minute)) {
			t.Errorf("delivered at %v, want %v", got[0].at, h.t0.Add(5*time.Minute))
		}
		r := h.rule("b1", "r1")
		now := h.at(5 * time.Minute)
		if len(r.Status.Queue) != 0 || len(r.Status.Sent) != 1 || r.Status.Sent[0] != now {
			t.Errorf("queue %+v sent %v, want the event gone and one delivery at %d", r.Status.Queue, r.Status.Sent, now)
		}
		if r.Status.LastFire == nil || r.Status.LastFire.EventID != want.Event || r.Status.Triggers["t1"].LastAt != now {
			t.Errorf("last fire %+v, last_at %d, want the evaluation at %d", r.Status.LastFire, r.Status.Triggers["t1"].LastAt, now)
		}

		h.endTurn("b1")
		h.advance(5 * time.Minute)
		got = h.wantSent("the next interval", 2)
		if id := eventIDOf(got[1]); id != dueID("t1", h.at(10*time.Minute)) {
			t.Errorf("second event %q, want %q", id, dueID("t1", h.at(10*time.Minute)))
		}
	})
}

func TestEngine_SleepsToTheEarliestDueTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.seedBot("b1",
			armedRule("r7", specOf("Seven", nil, everyT("t7", 7, "")), h.at(0)),
			armedRule("r5", specOf("Five", nil, everyT("t5", 5, "")), h.at(0)))
		h.scan()
		h.start()
		h.advance(5 * time.Minute)
		got := h.wantSent("at 5 min", 1)
		if got[0].from.Rule != "r5" {
			t.Errorf("first delivery from %s, want r5", got[0].from.Rule)
		}
		h.endTurn("b1")
		h.advance(2 * time.Minute)
		got = h.wantSent("at 7 min", 2)
		if got[1].from.Rule != "r7" || !got[1].at.Equal(h.t0.Add(7*time.Minute)) {
			t.Errorf("second delivery %s at %v, want r7 at 7 min", got[1].from.Rule, got[1].at)
		}
	})
}

// A Bot that is working gets nothing; its time trigger keeps one waiting event, the newest due time replacing the
// older, and the prompt goes out when the turn ends.
func TestEngine_HoldsAFiredEventThroughATurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.seedBot("b1", armedRule("r1", specOf("Tidy", nil, everyT("t1", 5, "")), h.at(0)))
		h.turns.setLive("b1", true)
		h.scan()
		h.start()
		h.advance(5 * time.Minute)
		h.wantSent("during the turn", 0)
		r := h.rule("b1", "r1")
		if len(r.Status.Queue) != 1 || r.Status.Queue[0].Stage != rule.StageFired || r.Status.LastFire == nil {
			t.Fatalf("queue %+v last fire %+v, want one fired event", r.Status.Queue, r.Status.LastFire)
		}

		h.advance(15 * time.Minute)
		h.wantSent("still working", 0)
		r = h.rule("b1", "r1")
		if len(r.Status.Queue) != 1 || r.Status.Queue[0].EventID != dueID("t1", h.at(20*time.Minute)) || r.Status.Dropped != 3 {
			t.Errorf("queue %v dropped %d, want only the 20 min slot and 3 dropped", eventIDs(r.Status.Queue), r.Status.Dropped)
		}

		h.endTurn("b1")
		got := h.wantSent("at the turn's end", 1)
		if id := eventIDOf(got[0]); id != dueID("t1", h.at(20*time.Minute)) {
			t.Errorf("delivered %q, want the newest slot", id)
		}
	})
}

// A run-list entry that is live or pending makes the Bot busy even with no turn open.
func TestEngine_ABotWithARunEntryIsNotFree(t *testing.T) {
	for _, state := range []marotte.RunEntryState{marotte.RunRunning, marotte.RunPending} {
		t.Run(string(state), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHarness(t)
				h.seedBot("b1", armedRule("r1", specOf("Tidy", nil, everyT("t1", 5, "")), h.at(0)))
				h.mutate("b1", func(c *marotte.Chat) {
					c.Run = []marotte.RunEntry{{ID: "e1", Kind: marotte.RunKindTurn, State: state, Origin: marotte.RunFromWire}}
				})
				h.scan()
				h.start()
				h.advance(5 * time.Minute)
				h.wantSent("with the entry on the list", 0)

				h.mutate("b1", func(c *marotte.Chat) { c.Run = nil })
				h.endTurn("b1")
				h.wantSent("once the list is empty", 1)
			})
		})
	}
}

// An idle trigger counts from the turn's end, so it has no due time, and is never evaluated, while the Bot works.
func TestEngine_AnIdleTriggerWaitsForTheTurnsEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.seedBot("b1", armedRule("r1", specOf("Check in", nil, everyT("t1", 30, rule.AnchorIdle)), h.at(0)))
		h.turns.setLive("b1", true)
		h.scan()
		h.start()
		h.advance(2 * time.Hour)
		r := h.rule("b1", "r1")
		if r.Status.Triggers["t1"].LastAt != 0 || len(r.Status.Queue) != 0 || r.Status.LastFire != nil {
			t.Fatalf("evaluated during the turn: %+v", r.Status)
		}

		h.endTurn("b1")
		h.advance(29 * time.Minute)
		h.wantSent("29 min after the turn", 0)
		h.advance(time.Minute)
		got := h.wantSent("30 min after the turn", 1)
		if id := eventIDOf(got[0]); id != dueID("t1", h.at(150*time.Minute)) {
			t.Errorf("event %q, want the slot 30 min after the turn's end", id)
		}
	})
}

// An event held more than 30 s is evaluated again before delivery, with what holds then.
func TestEngine_ReevaluatesAnEventThatWaited(t *testing.T) {
	notWaiting := leafCond(rule.Leaf{Kind: rule.LeafBotWaiting, Not: true})
	t.Run("past 30 s", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t)
			h.seedBot("b1", armedRule("r1", specOf("Tidy", notWaiting, everyT("t1", 5, "")), h.at(0)))
			h.turns.setLive("b1", true)
			h.scan()
			h.start()
			h.advance(5 * time.Minute)
			h.turns.setWaiting("b1", true)
			h.advance(time.Minute)
			h.endTurn("b1")
			h.wantSent("after waiting", 0)
			r := h.rule("b1", "r1")
			if len(r.Status.Queue) != 0 || r.Status.LastSkip == nil ||
				r.Status.LastSkip.Reason != "Skipped after waiting: this Bot is waiting on you" {
				t.Errorf("queue %v last skip %+v, want the event skipped after waiting", eventIDs(r.Status.Queue), r.Status.LastSkip)
			}
		})
	})
	t.Run("within 30 s", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t)
			h.seedBot("b1", armedRule("r1", specOf("Tidy", notWaiting, everyT("t1", 5, "")), h.at(0)))
			h.turns.setLive("b1", true)
			h.scan()
			h.start()
			h.advance(5 * time.Minute)
			h.turns.setWaiting("b1", true)
			h.advance(20 * time.Second)
			h.endTurn("b1")
			h.wantSent("delivered as it fired", 1)
		})
	})
}

// An event whose stored data no longer decodes is dropped with the same failure on either path, at its recheck after
// waiting or as it is sent, never sent or held, and counts one failed delivery.
func TestEngine_DropsAnEventThatCannotBeRead(t *testing.T) {
	for _, tc := range []struct {
		name, logLine string
		firedAgo      time.Duration
	}{
		{name: "at its recheck after waiting", firedAgo: time.Minute, logLine: "rule evaluation failed"},
		{name: "as it is sent", logLine: "rule delivery failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failedBefore := failedDeliveries(t)
			synctest.Test(t, func(t *testing.T) {
				h := newHarness(t)
				r := weekly(h)
				r.Status.Queue = []rule.QueuedEvent{{
					Stage: rule.StageFired, Trigger: "t1", EventID: "every:t1:1", Depth: 1,
					Data:   json.RawMessage(`{"fields":{"x":{"nested":1}}}`),
					SeenAt: h.at(-tc.firedAgo), FiredAt: h.at(-tc.firedAgo),
				}}
				h.seedBot("b1", r)
				h.scan()
				h.start()
				h.advance(time.Minute)
				h.wantSent("an unreadable event", 0)
				got := h.rule("b1", "r1").Status
				if len(got.Queue) != 0 || got.LastFailure == nil || got.LastFailure.EventID != "every:t1:1" ||
					!strings.HasPrefix(got.LastFailure.Reason, "Could not deliver: the event could not be read: ") {
					t.Errorf("queue %v last failure %+v, want the event dropped with its failure", eventIDs(got.Queue), got.LastFailure)
				}
				if n := h.logs.lines(slog.LevelError, tc.logLine); n != 1 {
					t.Errorf("%d %q ERROR lines, want 1", n, tc.logLine)
				}
			})
			if n := failedDeliveries(t) - failedBefore; n != 1 {
				t.Errorf("%v failed deliveries counted, want 1", n)
			}
		})
	}
}

// failedDeliveries reads the failed-delivery counter off the metrics page; read outside a synctest bubble.
func failedDeliveries(t *testing.T) float64 {
	t.Helper()
	rec := httptest.NewRecorder()
	obs.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody))
	for line := range strings.Lines(rec.Body.String()) {
		if v, ok := strings.CutPrefix(line, `marotte_rule_deliveries_total{outcome="failed"} `); ok {
			n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				t.Fatalf("failed deliveries %q: %v", v, err)
			}
			return n
		}
	}
	t.Fatal("the metrics page lacks the failed-delivery series")
	return 0
}

// The recheck of an event that waited reads the rule's last run from before the event fired: its own fire is not a
// run, and a run before it still is.
func TestEngine_ARecheckReadsTheRulesLastRunBeforeItsEvent(t *testing.T) {
	cooldown := leafCond(rule.Leaf{Kind: rule.LeafRuleLastRan, Rule: &rule.LastRanLeaf{Minutes: 180}})
	t.Run("a time trigger's event", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t)
			h.seedBot("b1", armedRule("r1", specOf("Tidy", cooldown, everyT("t1", 15, "")), h.at(0)))
			h.turns.setLive("b1", true)
			h.scan()
			h.start()
			h.advance(20 * time.Minute)
			h.endTurn("b1")
			got := h.wantSent("after the turn", 1)
			if id := eventIDOf(got[0]); id != dueID("t1", h.at(15*time.Minute)) {
				t.Errorf("event %q, want the 15 min slot", id)
			}
			if s := h.rule("b1", "r1").Status.LastSkip; s != nil {
				t.Errorf("last skip %+v, want none", s)
			}
		})
	})
	t.Run("a Run now press", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t)
			h.seedBot("b1", armedRule("r1", specOf("Tidy", cooldown, everyT("t1", rule.MaxEveryMinutes, "")), h.at(0)))
			h.turns.setLive("b1", true)
			h.scan()
			h.start()
			if ans := h.runNow("op-1"); ans.Outcome != command.RunNowQueued {
				t.Errorf("Run now answered %+v, want queued", ans)
			}
			h.advance(time.Minute)
			h.endTurn("b1")
			got := h.wantSent("after the turn", 1)
			if id := eventIDOf(got[0]); id != "manual:op-1" {
				t.Errorf("event %q, want the press", id)
			}
		})
	})
	t.Run("a run before the event", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t)
			ranRecently := leafCond(rule.Leaf{Kind: rule.LeafRuleLastRan, Not: true, Rule: &rule.LastRanLeaf{Minutes: 180}})
			r := armedRule("r1", specOf("Tidy", ranRecently, everyT("t1", 5, "")), h.at(0))
			r.Status.LastFire = &rule.Mark{Trigger: "t1", EventID: "earlier", At: h.at(-170 * time.Minute)}
			h.seedBot("b1", r)
			h.turns.setLive("b1", true)
			h.scan()
			h.start()
			h.advance(6 * time.Minute)
			h.endTurn("b1")
			h.wantSent("after the turn", 1)
		})
	})
}

// A Bot waiting on the user still gets its rules' prompts: a rule opts out with "This Bot is waiting on you".
func TestEngine_DeliversToABotWaitingOnTheUser(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.seedBot("b1", armedRule("r1", specOf("Tidy", nil, everyT("t1", 5, "")), h.at(0)))
		h.turns.setWaiting("b1", true)
		h.scan()
		h.start()
		h.advance(5 * time.Minute)
		h.wantSent("while waiting on the user", 1)
	})
}

// weekly is a rule whose one trigger is a week away, so only Run now fires it within a test.
func weekly(h *harness) rule.Rule {
	return armedRule("r1", specOf("Tidy", nil, everyT("t1", rule.MaxEveryMinutes, "")), h.at(0))
}

func (h *harness) runNow(op string) command.RunNowAnswer {
	h.t.Helper()
	ans, err := h.eng.RunNow(h.t.Context(), "b1", "r1", op)
	if err != nil {
		h.t.Fatalf("RunNow(%s): %v", op, err)
	}
	synctest.Wait()
	return ans
}

func TestEngine_RetriesARefusalThatMayPassLater(t *testing.T) {
	for _, c := range []struct {
		err  error
		name string
	}{
		{name: "a 409", err: command.StatusError(http.StatusConflict, errors.New("the chat is starting"))},
		{name: "a 503", err: command.StatusError(http.StatusServiceUnavailable, errors.New("the engine is restarting"))},
		{name: "any other fault", err: errors.New("disk on fire")},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHarness(t)
				h.notes.place = func(int) (placement, error) { return opensTurn, c.err }
				h.seedBot("b1", weekly(h))
				h.scan()
				h.start()
				h.runNow("op-1")
				var at []time.Duration
				for range 200 {
					for len(at) < len(h.notes.sent()) {
						at = append(at, time.Since(h.t0).Round(time.Minute))
					}
					if len(at) == 3 {
						if q := h.rule("b1", "r1").Status.Queue; len(q) != 1 || q[0].Tries != 3 {
							t.Errorf("after 3 tries the queue is %+v, want the event with tries 3", q)
						}
					}
					h.advance(time.Minute)
				}
				want := []int{0, 1, 3, 8, 18, 48, 78, 108, 138, 168}
				if len(at) != len(want) {
					t.Fatalf("tries at %v, want %v min", at, want)
				}
				for i := range want {
					if at[i] != time.Duration(want[i])*time.Minute {
						t.Errorf("try %d at %v, want %v min", i+1, at[i], want[i])
					}
				}
				r := h.rule("b1", "r1")
				if len(r.Status.Queue) != 0 || r.Status.LastFailure == nil ||
					!strings.HasPrefix(r.Status.LastFailure.Reason, "Could not deliver after 10 tries: ") {
					t.Errorf("queue %v last failure %+v, want the event dropped with its failure", eventIDs(r.Status.Queue), r.Status.LastFailure)
				}
			})
		})
	}
}

// A refusal that cannot pass on a retry is not retried; only a bug is a failure on the rule's card.
func TestEngine_DropsARefusalNoRetryCanPass(t *testing.T) {
	for _, c := range []struct {
		err         error
		name        string
		dropped     int
		wantFailure bool
	}{
		{name: "a deleted chat", err: command.ErrChatNotFound},
		{name: "a closed Bot", err: command.StatusErrorReason(http.StatusConflict, "bot_closed", errors.New("closed")), dropped: 1},
		{name: "a text the door refuses", err: command.StatusError(http.StatusBadRequest, errors.New("mention")), wantFailure: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHarness(t)
				h.notes.place = func(int) (placement, error) { return opensTurn, c.err }
				h.seedBot("b1", weekly(h))
				h.scan()
				h.start()
				h.runNow("op-1")
				h.advance(2 * time.Hour)
				h.wantSent("after two hours", 1)
				r := h.rule("b1", "r1")
				if len(r.Status.Queue) != 0 || r.Status.Dropped != c.dropped || (r.Status.LastFailure != nil) != c.wantFailure {
					t.Errorf("queue %v dropped %d last failure %+v, want empty, %d, failure %v",
						eventIDs(r.Status.Queue), r.Status.Dropped, r.Status.LastFailure, c.dropped, c.wantFailure)
				}
			})
		})
	}
}

// An engine not ready, or a runtime draining, holds the event and counts no try against it.
func TestEngine_HoldsWithoutATryWhileTheRuntimeCannotTakeIt(t *testing.T) {
	for _, c := range []struct {
		block func(h *harness, on bool)
		name  string
	}{
		{name: "engine not ready", block: func(h *harness, on bool) { h.ready.Store(!on) }},
		{name: "draining", block: func(h *harness, on bool) { h.refused.Store(on) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHarness(t)
				h.seedBot("b1", weekly(h))
				h.scan()
				h.start()
				c.block(h, true)
				h.runNow("op-1")
				h.advance(10 * time.Minute)
				h.wantSent("while blocked", 0)
				if q := h.rule("b1", "r1").Status.Queue; len(q) != 1 || q[0].Tries != 0 {
					t.Errorf("queue %+v, want the event held with no try", q)
				}
				c.block(h, false)
				h.advance(time.Minute)
				h.wantSent("once unblocked", 1)
			})
		})
	}
}

func TestEngine_RunNow(t *testing.T) {
	t.Run("an idle Bot", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t)
			h.seedBot("b1", weekly(h))
			h.scan()
			h.start()
			if ans := h.runNow("op-1"); ans.Outcome != command.RunNowFired {
				t.Errorf("answer %+v, want fired", ans)
			}
			got := h.wantSent("Run now", 1)
			if id := eventIDOf(got[0]); id != "manual:op-1" {
				t.Errorf("event %q, want manual:op-1", id)
			}
			h.endTurn("b1")
			h.runNow("op-1")
			h.advance(time.Minute)
			h.wantSent("the same press again", 1)
		})
	})
	t.Run("a press replayed after a later one", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t)
			h.seedBot("b1", weekly(h))
			h.scan()
			h.start()
			h.runNow("op-1")
			h.endTurn("b1")
			h.advance(time.Minute)
			if ans := h.runNow("op-2"); ans.Outcome != command.RunNowFired {
				t.Errorf("op-2 answered %+v, want fired", ans)
			}
			h.endTurn("b1")
			h.advance(time.Minute)
			if ans := h.runNow("op-1"); ans.Outcome != command.RunNowFired {
				t.Errorf("op-1 again answered %+v, want what its first press did, fired", ans)
			}
			h.advance(time.Minute)
			h.wantSent("op-1 replayed after op-2", 2)
		})
	})
	t.Run("a working Bot holds one press", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t)
			h.seedBot("b1", weekly(h))
			h.turns.setLive("b1", true)
			h.scan()
			h.start()
			for _, op := range []string{"op-1", "op-2", "op-3"} {
				if ans := h.runNow(op); ans.Outcome != command.RunNowQueued {
					t.Errorf("%s answered %+v, want queued", op, ans)
				}
			}
			if q := h.rule("b1", "r1").Status.Queue; len(q) != 1 || q[0].EventID != "manual:op-1" {
				t.Errorf("queue %v, want the first press alone", eventIDs(q))
			}
			h.endTurn("b1")
			h.wantSent("at the turn's end", 1)
		})
	})
	t.Run("a condition that does not hold", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t)
			r := weekly(h)
			r.Spec.Condition = leafCond(rule.Leaf{Kind: rule.LeafBotWaiting})
			h.seedBot("b1", r)
			h.scan()
			h.start()
			ans := h.runNow("op-1")
			if ans.Outcome != command.RunNowSkipped || ans.Reason != "this Bot is not waiting on you" {
				t.Errorf("answer %+v, want skipped with its reason", ans)
			}
			h.wantSent("a skip", 0)
		})
	})
	t.Run("a paused Bot", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t)
			h.seedBot("b1", weekly(h))
			h.mutate("b1", func(c *marotte.Chat) { c.Bot.Paused = true })
			h.scan()
			h.start()
			if ans := h.runNow("op-1"); ans.Outcome != command.RunNowFired {
				t.Errorf("answer %+v, want fired", ans)
			}
			h.wantSent("Run now while paused", 1)
		})
	})
}

// A rule's 1-minute gap delays its next prompt; it never skips it.
func TestEngine_PacingWaitsAndNeverSkips(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		r := weekly(h)
		r.Status.Sent = []int64{h.at(-30 * time.Second)}
		h.seedBot("b1", r)
		h.scan()
		h.start()
		if ans := h.runNow("op-1"); ans.Outcome != command.RunNowFired {
			t.Errorf("answer %+v, want fired", ans)
		}
		h.advance(29 * time.Second)
		h.wantSent("inside the gap", 0)
		h.advance(time.Second)
		h.wantSent("at the gap's end", 1)
	})
}

// Past 60 automatic prompts in an hour every rule of the Bot waits, and the engine says until when.
func TestEngine_TheBotCeilingHoldsEveryRule(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		r1, r2 := weekly(h), armedRule("r2", specOf("Other", nil, everyT("t2", rule.MaxEveryMinutes, "")), h.at(0))
		for range 30 {
			r1.Status.Sent = append(r1.Status.Sent, h.at(-50*time.Minute))
			r2.Status.Sent = append(r2.Status.Sent, h.at(-40*time.Minute))
		}
		h.seedBot("b1", r1, r2)
		h.scan()
		h.start()
		h.runNow("op-1")
		if until := h.eng.Status("b1").CeilingUntil; !until.Equal(h.t0.Add(10 * time.Minute)) {
			t.Errorf("ceiling until %v, want %v", until, h.t0.Add(10*time.Minute))
		}
		h.advance(10*time.Minute - time.Second)
		h.wantSent("under the ceiling", 0)
		h.advance(time.Second)
		h.wantSent("once the oldest leave the hour", 1)
		if until := h.eng.Status("b1").CeilingUntil; !until.IsZero() {
			t.Errorf("ceiling until %v after it lifted, want none", until)
		}
	})
}

// Held events go out earliest fired first; events one pass fired go out in rule order.
func TestEngine_DeliversTheEarliestFiredFirst(t *testing.T) {
	t.Run("by fired time", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t)
			ra := armedRule("ra", specOf("A", nil, everyT("ta", rule.MaxEveryMinutes, "")), h.at(0))
			rb := armedRule("rb", specOf("B", nil, everyT("tb", rule.MaxEveryMinutes, "")), h.at(0))
			h.seedBot("b1", ra, rb)
			h.turns.setLive("b1", true)
			h.scan()
			h.start()
			for _, id := range []string{"rb", "ra"} {
				if _, err := h.eng.RunNow(t.Context(), "b1", id, "op-"+id); err != nil {
					t.Fatalf("RunNow(%s): %v", id, err)
				}
				h.advance(10 * time.Second)
			}
			h.endTurn("b1")
			h.endTurn("b1")
			got := h.wantSent("both", 2)
			if got[0].from.Rule != "rb" || got[1].from.Rule != "ra" {
				t.Errorf("delivered %s then %s, want rb (fired first) then ra", got[0].from.Rule, got[1].from.Rule)
			}
		})
	})
	t.Run("one pass in rule order", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t)
			h.seedBot("b1",
				armedRule("ra", specOf("A", nil, everyT("ta", 5, "")), h.at(0)),
				armedRule("rb", specOf("B", nil, everyT("tb", 5, "")), h.at(0)))
			h.turns.setLive("b1", true)
			h.scan()
			h.start()
			h.advance(5 * time.Minute)
			h.endTurn("b1")
			h.advance(time.Minute)
			h.endTurn("b1")
			got := h.wantSent("both", 2)
			if got[0].from.Rule != "ra" || got[1].from.Rule != "rb" {
				t.Errorf("delivered %s then %s, want ra then rb", got[0].from.Rule, got[1].from.Rule)
			}
		})
	})
}

// Reindex follows each command's write: a rule added, a Bot paused, a Bot deleted.
func TestEngine_ReindexFollowsTheRecord(t *testing.T) {
	t.Run("a rule added", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t)
			h.seedBot("b1")
			h.scan()
			h.start()
			h.advance(time.Minute)
			h.mutate("b1", func(c *marotte.Chat) {
				c.Bot.Rules = []rule.Rule{armedRule("r1", specOf("Tidy", nil, everyT("t1", 5, "")), h.at(time.Minute))}
			})
			h.eng.Reindex("b1")
			h.advance(5 * time.Minute)
			h.wantSent("5 min after the save", 1)
		})
	})
	for _, c := range []struct {
		change func(h *harness)
		name   string
	}{
		{name: "a Bot paused", change: func(h *harness) { h.mutate("b1", func(c *marotte.Chat) { c.Bot.Paused = true }) }},
		{name: "a Bot deleted", change: func(h *harness) {
			if _, err := h.store.Delete(h.t.Context(), "b1"); err != nil {
				h.t.Fatalf("Delete: %v", err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHarness(t)
				h.seedBot("b1", armedRule("r1", specOf("Tidy", nil, everyT("t1", 5, "")), h.at(0)))
				h.scan()
				h.start()
				h.advance(time.Minute)
				c.change(h)
				h.eng.Reindex("b1")
				h.advance(time.Hour)
				h.wantSent("an hour on", 0)
			})
		})
	}
}

// A turn's end reads a record only for a chat the engine knows as a Bot with rules, however it came to know it.
func TestEngine_AnotherChatsTurnEndReadsNoRecord(t *testing.T) {
	t.Run("a regular chat beside a Bot", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t)
			h.seedBot("b1", weekly(h))
			h.mutate("c1", func(c *marotte.Chat) { c.Name = "c1" })
			h.scan()
			h.start()
			h.advance(time.Minute)
			before := h.fs.gets.Load()
			h.endTurn("c1")
			if got := h.fs.gets.Load(); got != before {
				t.Errorf("%d record reads for a chat's turn end, want none", got-before)
			}
			h.endTurn("b1")
			if got := h.fs.gets.Load(); got == before {
				t.Errorf("no record read for a Bot's turn end")
			}
		})
	})
	t.Run("a Bot whose rules were all removed", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t)
			h.seedBot("b1", weekly(h))
			h.scan()
			h.start()
			h.mutate("b1", func(c *marotte.Chat) { c.Bot.Rules = nil })
			h.eng.Reindex("b1")
			synctest.Wait()
			before := h.fs.gets.Load()
			h.endTurn("b1")
			if got := h.fs.gets.Load(); got != before {
				t.Errorf("%d record reads for a forgotten Bot's turn end, want none", got-before)
			}
		})
	})
	t.Run("a Bot given rules while a pass forgets it", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t)
			h.seedBot("b1")
			var once atomic.Bool
			h.fs.afterGet = func(id marotte.ChatID) {
				if id != "b1" || once.Swap(true) {
					return
				}
				if _, err := h.store.Mutate(context.Background(), id, func(c *marotte.Chat, _ bool) bool {
					c.Bot.Rules = []rule.Rule{weekly(h)}
					return true
				}); err == nil {
					h.eng.Reindex(id)
				}
			}
			h.scan()
			h.start()
			h.advance(time.Minute)
			before := h.fs.gets.Load()
			h.endTurn("b1")
			if got := h.fs.gets.Load(); got == before {
				t.Errorf("no record read for the Bot's turn end")
			}
		})
	})
}

// A drain stops the loop and leaves what waits on disk for the next process.
func TestEngine_ADrainLeavesTheQueueOnDisk(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.seedBot("b1", armedRule("r1", specOf("Tidy", nil, everyT("t1", 5, "")), h.at(0)))
		h.turns.setLive("b1", true)
		h.scan()
		h.start()
		h.advance(5 * time.Minute)
		h.stop()
		h.turns.setLive("b1", false)
		h.advance(time.Hour)
		h.wantSent("after the drain", 0)
		if q := h.rule("b1", "r1").Status.Queue; len(q) != 1 || q[0].EventID != dueID("t1", h.at(5*time.Minute)) {
			t.Errorf("queue %v, want the fired event kept", eventIDs(q))
		}
	})
}

func eventIDs(q []rule.QueuedEvent) []string {
	out := make([]string, 0, len(q))
	for i := range q {
		out = append(out, q[i].EventID)
	}
	return out
}

// panicking makes the first evaluation panic, as one with a bug in it, and the rest evaluate as they should. It runs
// before the engine starts, so the engine's goroutine never sees the variable change.
func panicking(t *testing.T) {
	t.Helper()
	was := evalCond
	var calls atomic.Int32
	evalCond = func(c *rule.Cond, s *rule.Snapshot) rule.Result {
		if calls.Add(1) == 1 {
			panic("a bug")
		}
		return was(c, s)
	}
	t.Cleanup(func() { evalCond = was })
}

// An event whose evaluation panics is dropped after that one try with the rule's failure and an ERROR line, and the
// engine goes on with the next.
func TestEngine_DropsAnEventWhoseEvaluationPanics(t *testing.T) {
	t.Run("a time trigger", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t)
			h.seedBot("b1", armedRule("r1", specOf("Tidy", nil, everyT("t1", 5, "")), h.at(0)))
			h.scan()
			panicking(t)
			h.start()
			h.advance(5 * time.Minute)
			h.wantSent("after the panic", 0)
			r := h.rule("b1", "r1")
			if len(r.Status.Queue) != 0 || r.Status.LastFire != nil || r.Status.LastFailure == nil ||
				r.Status.LastFailure.EventID != dueID("t1", h.at(5*time.Minute)) ||
				!strings.HasPrefix(r.Status.LastFailure.Reason, "Could not deliver: ") {
				t.Errorf("queue %v last fire %+v last failure %+v, want the event dropped with its failure",
					eventIDs(r.Status.Queue), r.Status.LastFire, r.Status.LastFailure)
			}
			if got := h.logs.lines(slog.LevelError, "rule evaluation failed"); got != 1 {
				t.Errorf("%d ERROR lines, want 1", got)
			}
			h.advance(5 * time.Minute)
			h.wantSent("the next interval", 1)
		})
	})
	t.Run("Run now", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t)
			h.seedBot("b1", weekly(h))
			h.scan()
			panicking(t)
			h.start()
			if _, err := h.eng.RunNow(t.Context(), "b1", "r1", "op-1"); err == nil {
				t.Errorf("RunNow answered no error for an evaluation that panicked")
			}
			synctest.Wait()
			h.wantSent("after the panic", 0)
			if r := h.rule("b1", "r1"); len(r.Status.Queue) != 0 || r.Status.LastFailure == nil ||
				r.Status.LastFailure.EventID != "manual:op-1" {
				t.Errorf("queue %v last failure %+v, want the press dropped with its failure", eventIDs(r.Status.Queue), r.Status.LastFailure)
			}
			if _, err := h.eng.RunNow(t.Context(), "b1", "r1", "op-1"); err == nil {
				t.Errorf("the replayed press answered no error, want what its first press answered")
			}
			if got := h.logs.lines(slog.LevelError, "rule evaluation failed"); got != 1 {
				t.Errorf("%d ERROR lines, want 1: a replay evaluates nothing", got)
			}
		})
	})
}
