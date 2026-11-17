package ruleengine

import (
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/rule"
)

// panicWhile makes every evaluation panic while on is set.
func panicWhile(t *testing.T, on *atomic.Bool) {
	t.Helper()
	was := evalCond
	evalCond = func(c *rule.Cond, s *rule.Snapshot) rule.Result {
		if on.Load() {
			panic("a bug")
		}
		return was(c, s)
	}
	t.Cleanup(func() { evalCond = was })
}

func (h *harness) wantReplay(op string, want command.RunNowAnswer) {
	h.t.Helper()
	if got := h.runNow(op); got != want {
		h.t.Errorf("replay of %s answered %+v, want its first answer %+v", op, got, want)
	}
}

// A replayed Run now press answers what its first press answered, whatever the rule did since and across a restart,
// and evaluates, queues and sends nothing.
func TestEngine_AReplayedPressAnswersItsFirstAnswerOnly(t *testing.T) {
	waiting := leafCond(rule.Leaf{Kind: rule.LeafBotWaiting})
	notWaiting := leafCond(rule.Leaf{Kind: rule.LeafBotWaiting, Not: true})
	skipped := command.RunNowAnswer{Outcome: command.RunNowSkipped, Reason: "this Bot is not waiting on you"}
	queued := command.RunNowAnswer{Outcome: command.RunNowQueued}
	for _, c := range []struct {
		name    string
		restart bool
	}{{name: "in one process"}, {name: "across a restart", restart: true}} {
		t.Run("a skip after the rule skipped again "+c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHarness(t)
				h.seedBot("b1", armedRule("r1", specOf("Tidy", waiting, everyT("t1", 5, "")), h.at(0)))
				h.scan()
				h.start()
				if got := h.runNow("op-1"); got != skipped {
					t.Fatalf("the press answered %+v, want %+v", got, skipped)
				}
				h.advance(5*time.Minute + time.Second)
				if s := h.rule("b1", "r1").Status.LastSkip; s == nil || s.EventID == "manual:op-1" {
					t.Fatalf("last skip %+v, want the time trigger's", s)
				}
				if c.restart {
					h.restart()
				}
				h.wantReplay("op-1", skipped)
				h.advance(time.Minute)
				h.wantSent("after the replay", 0)
			})
		})
		t.Run("a press refused beside a waiting one "+c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHarness(t)
				h.seedBot("b1", weekly(h))
				h.turns.setLive("b1", true)
				h.scan()
				h.start()
				for _, op := range []string{"op-1", "op-2"} {
					if got := h.runNow(op); got != queued {
						t.Fatalf("%s answered %+v, want queued", op, got)
					}
				}
				if c.restart {
					h.restart()
				} else {
					h.endTurn("b1")
				}
				got := h.wantSent("once the Bot is free", 1)
				if id := eventIDOf(got[0]); id != "manual:op-1" {
					t.Fatalf("delivered %q, want the first press", id)
				}
				h.wantReplay("op-2", queued)
				h.wantReplay("op-1", queued)
				h.endTurn("b1")
				h.advance(2 * time.Minute)
				h.wantSent("after both replays", 1)
			})
		})
	}
	t.Run("a queued press the recheck dropped", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t)
			h.seedBot("b1", armedRule("r1", specOf("Tidy", notWaiting, everyT("t1", rule.MaxEveryMinutes, "")), h.at(0)))
			h.turns.setLive("b1", true)
			h.scan()
			h.start()
			if got := h.runNow("op-1"); got != queued {
				t.Fatalf("the press answered %+v, want queued", got)
			}
			h.turns.setWaiting("b1", true)
			h.advance(time.Minute)
			h.endTurn("b1")
			if s := h.rule("b1", "r1").Status.LastSkip; s == nil || s.EventID != "manual:op-1" {
				t.Fatalf("last skip %+v, want the press dropped by its recheck", s)
			}
			h.wantReplay("op-1", queued)
			h.wantSent("after the replay", 0)
		})
	})
	t.Run("a panicked press after another panicked", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t)
			h.seedBot("b1", weekly(h))
			h.scan()
			var on atomic.Bool
			on.Store(true)
			panicWhile(t, &on)
			h.start()
			for _, op := range []string{"op-1", "op-2"} {
				if _, err := h.eng.RunNow(t.Context(), "b1", "r1", op); err == nil {
					t.Fatalf("%s answered no error for an evaluation that panicked", op)
				}
			}
			on.Store(false)
			_, err := h.eng.RunNow(t.Context(), "b1", "r1", "op-1")
			if err == nil || !strings.Contains(err.Error(), "the evaluation panicked: a bug") {
				t.Errorf("replay of op-1 answered %v, want its first press's failure", err)
			}
			if got := h.logs.lines(slog.LevelError, "rule evaluation failed"); got != 2 {
				t.Errorf("%d ERROR lines, want 2: a replay evaluates nothing", got)
			}
			synctest.Wait()
			h.wantSent("after the replay", 0)
		})
	})
}
