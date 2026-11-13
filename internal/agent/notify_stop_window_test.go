package agent

// The two timing windows of a Stop beside a wake, through the chat's real lifecycle: the wake's take opening when the
// Stop lands, and a wake sent while the stopped turn closes. Every case ends idle under synctest, so "nothing opened"
// is read once every goroutine is blocked, not after a guessed delay.

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

func resumeQueue(t *testing.T, h *Runtime) {
	t.Helper()
	if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdResumeQueue, ChatID: "c1"}); rec.Code != http.StatusOK {
		t.Fatalf("resume_queue = %d %s, want 200", rec.Code, rec.Body.String())
	}
}

func opensIn(t *testing.T, cs *testChatStore) int { return len(opensOf(t, logOf(t, cs, "c1"))) }

func TestRunList_StopWindows(t *testing.T) {
	t.Run("a Stop while the wake's take opens puts it back paused, and Resume opens it", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s, cs, _ := steerOpsHub(t)
			id, _ := s.h.stagePromptTurn(t, "c1")
			s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
			s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnEndMsg("end_turn"))
			s.h.coord.releaseTurn("c1", id)
			idleAfter(t, s.h, "c1", id)
			parked, release := make(chan struct{}), make(chan struct{})
			launch := s.h.coord.launchEntry
			first := true
			s.h.coord.launchEntry = func(ctx context.Context, chatID marotte.ChatID, l *command.EntryLaunch) (string, error) {
				if first {
					first = false
					close(parked)
					<-release
				}
				return launch(ctx, chatID, l)
			}

			wake := notifyWake(t, s.h, "c1")
			<-parked
			plainStop(t, s.h)
			close(release)
			if got := awaitDelivery(t, wake); got != command.NotifyQueued {
				t.Errorf("wake answered %q, want queued: the Stop withdrew its take", got)
			}
			joinInflight(t, s.h)
			synctest.Wait()

			if n := opensIn(t, cs); n != 1 {
				t.Errorf("turns opened after the Stop = %d, want none", n-1)
			}
			if got, want := listedRows(s.h, "c1"), []string{"grelot:paused"}; !slices.Equal(got, want) {
				t.Fatalf("waiting rows = %v, want %v", got, want)
			}
			resumeQueue(t, s.h)
			if next := nextPrompt(t, cs, "c1", 1); next.Text != grelotWake || !fromGrelot(next.Caller) {
				t.Errorf("turn after Resume = %q (caller %+v), want the wake from grelot", next.Text, next.Caller)
			}
		})
	})

	t.Run("a wake sent while the stopped turn closes waits paused, and Resume opens it", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s, cs, br := steerOpsHub(t)
			k := kas320On(s.h, "c1", br)
			id, _ := s.h.stagePromptTurn(t, "c1")
			s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
			plainStop(t, s.h)
			if got := awaitDelivery(t, notifyWake(t, s.h, "c1")); got != command.NotifyQueued {
				t.Errorf("wake answered %q, want queued: the turn is stopping", got)
			}
			s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnEndMsg("cancelled"))
			s.h.coord.releaseTurn("c1", id)
			idleAfter(t, s.h, "c1", id)
			synctest.Wait()

			if n := opensIn(t, cs); n != 1 {
				t.Errorf("turns opened after the stopped close = %d, want none", n-1)
			}
			if got := k.steered(); len(got) != 0 {
				t.Errorf("steers sent = %q, want none into the stopping turn", got)
			}
			resumeQueue(t, s.h)
			if next := nextPrompt(t, cs, "c1", 1); next.Text != grelotWake || !fromGrelot(next.Caller) {
				t.Errorf("turn after Resume = %q (caller %+v), want the wake from grelot", next.Text, next.Caller)
			}
		})
	})

	t.Run("after Resume during the stopped turn's close, a wake sent then goes at that close", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s, cs, br := steerOpsHub(t)
			kas320On(s.h, "c1", br)
			id, _ := s.h.stagePromptTurn(t, "c1")
			s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
			plainStop(t, s.h)
			resumeQueue(t, s.h)
			if got := awaitDelivery(t, notifyWake(t, s.h, "c1")); got != command.NotifyQueued {
				t.Errorf("wake answered %q, want queued behind the stopping turn", got)
			}
			if got, want := listedRows(s.h, "c1"), []string{"grelot:pending"}; !slices.Equal(got, want) {
				t.Errorf("waiting rows before the close = %v, want %v: Resume lifted the pause", got, want)
			}
			s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnEndMsg("cancelled"))
			s.h.coord.releaseTurn("c1", id)

			if next := nextPrompt(t, cs, "c1", 1); next.Text != grelotWake || !fromGrelot(next.Caller) {
				t.Errorf("turn after the close = %q (caller %+v), want the wake from grelot", next.Text, next.Caller)
			}
		})
	})
}
