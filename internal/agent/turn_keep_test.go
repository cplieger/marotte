package agent

// The turn keep fact the registry writes: held with the reservation, released at the idle transition.

import (
	"context"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

func hasTurnFact(t *testing.T, cs *testChatStore, id marotte.ChatID) bool {
	t.Helper()
	c, found, err := cs.Lookup(t.Context(), id)
	if err != nil || !found {
		t.Fatalf("lookup %s: found=%v err=%v", id, found, err)
	}
	_, ok := c.Keep[marotte.KeyTurn]
	return ok
}

func TestReservation_HoldsTheTurnFactUntilTheLifecycleGoesIdle(t *testing.T) {
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	if !h.coord.TryReserveIdleTurn("c1", marotte.TurnSourcePrompt) {
		t.Fatal("reservation refused on an idle chat")
	}
	if !hasTurnFact(t, cs, "c1") {
		t.Error("a reserved chat carries no turn fact")
	}
	h.coord.ReleaseTurnReservation("c1")
	if hasTurnFact(t, cs, "c1") {
		t.Error("the turn fact survived the reservation's release")
	}
}

func TestReservation_ADeletedChatIsRefusedAndNothingIsReserved(t *testing.T) {
	cases := map[string]func(bc *bridgeCoordinator) bool{
		"prompt": func(bc *bridgeCoordinator) bool {
			return bc.ReserveTurnForPrompt(context.Background(), "c1", 10*time.Millisecond) == command.AdmissionChatGone
		},
		"try reserve": func(bc *bridgeCoordinator) bool { return !bc.TryReserveTurn("c1", marotte.TurnSourcePrompt) },
		"try reserve idle": func(bc *bridgeCoordinator) bool {
			return !bc.TryReserveIdleTurn("c1", marotte.TurnSourcePrompt)
		},
	}
	for name, refused := range cases {
		t.Run(name, func(t *testing.T) {
			h, cs, _ := newTestHub()
			seedChat(t, cs, "c1")
			if _, err := cs.Delete(t.Context(), "c1"); err != nil {
				t.Fatal(err)
			}
			if !refused(h.coord) {
				t.Error("a reservation on a deleted chat was not refused")
			}
			if _, held := h.coord.AdmissionHolderSource("c1"); held {
				t.Error("the refused reservation left the slot held")
			}
		})
	}
}

// lockProbeRecords records whether the lifecycle mutex was held when the registry released and announced.
type lockProbeRecords struct {
	nopTurnRecords
	lc               *chatLifecycle
	endUnderLock     bool
	announceUnlocked bool
}

func (p *lockProbeRecords) EndTurn(context.Context, marotte.ChatID) bool {
	if p.lc.mu.TryLock() {
		p.lc.mu.Unlock()
		return true
	}
	p.endUnderLock = true
	return true
}

func (p *lockProbeRecords) AnnounceHeader(context.Context, marotte.ChatID) {
	if p.lc.mu.TryLock() {
		p.lc.mu.Unlock()
		p.announceUnlocked = true
	}
}

// The release must run inside the lifecycle mutex, or the next turn's open can write the fact the release then deletes.
func TestTurnRelease_RunsUnderTheLifecycleLockAndAnnouncesAfter(t *testing.T) {
	cases := map[string]func(reg *turnRegistry, chatID marotte.ChatID){
		"finish": func(reg *turnRegistry, chatID marotte.ChatID) {
			reg.finish(openTestTurn(t, reg, chatID), marotte.TurnResult{})
		},
		"release reservation": func(reg *turnRegistry, chatID marotte.ChatID) {
			if !reg.tryReserve(chatID, marotte.TurnSourcePrompt, command.TurnFence{}) {
				t.Fatal("reservation refused")
			}
			reg.releaseReservation(chatID)
		},
	}
	for name, drive := range cases {
		t.Run(name, func(t *testing.T) {
			probe := &lockProbeRecords{}
			reg := newTurnRegistry(probe)
			probe.lc = reg.lifecycleFor("c1")
			drive(reg, "c1")
			if !probe.endUnderLock {
				t.Error("EndTurn ran without the lifecycle mutex")
			}
			if !probe.announceUnlocked {
				t.Error("AnnounceHeader did not run after the lifecycle mutex was released")
			}
		})
	}
}
