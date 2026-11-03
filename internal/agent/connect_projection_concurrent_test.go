package agent

// The connect handshake's projections are read while their state is written; nothing else in
// this package races them, so a read moved outside its lock would otherwise ship green.

import (
	"fmt"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

// A projection may report any subset of what is live, never a value nothing minted. Bounded
// by connects, so the mutation is in flight while streamInitialState runs.
func TestConnectProjections_ReadConcurrentlyWithTheirOwnMutation(t *testing.T) {
	rt := newBudgetRuntime(t)
	const fixtureChats, fixtureRuns, readers = 4, 4, 3

	chatIDs := make([]marotte.ChatID, 0, fixtureChats)
	knownChat := make(map[marotte.ChatID]bool, fixtureChats)
	for i := range fixtureChats {
		id := marotte.ChatID(fmt.Sprintf("c-conc-%02d", i))
		chatIDs = append(chatIDs, id)
		knownChat[id] = true
		rt.bridge.mgr.orInsert(id)
	}
	knownRun := make(map[string]bool, fixtureRuns)
	runIDs := make([]string, 0, fixtureRuns)
	for i := range fixtureRuns {
		id := fmt.Sprintf("wf-conc-%02d", i)
		runIDs = append(runIDs, id)
		knownRun[id] = true
	}

	// One chat holds an open turn throughout.
	rt.stagePromptTurn(t, chatIDs[0])

	store := rt.runs.leaseStore()
	ctx := t.Context()
	stop := make(chan struct{})
	badChat := make(chan marotte.ChatID, 1)
	badRun := make(chan string, 1)
	var wg sync.WaitGroup

	// Writers of lc.reserved and lc.reservedSource.
	for _, id := range chatIDs[1:] {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				if rt.coord.TryReserveTurn(id, marotte.TurnSourcePrompt) {
					rt.coord.ReleaseTurnReservation(id)
				}
			}
		})
	}
	for _, id := range runIDs {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = store.Put(ctx, &runlease.Lease{
					WorkflowID: id,
					ChatID:     string(chatIDs[0]),
					Recipe:     "r",
					Origin:     runlease.OriginManual,
				})
				_ = store.Release(ctx, id)
			}
		})
	}

	// Readers report through channels: Fatal off the test goroutine ends the wrong goroutine.
	for range readers {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, id := range rt.coord.turns.busyChatIDs() {
					if !knownChat[id] {
						select {
						case badChat <- id:
						default:
						}
					}
				}
				for _, r := range rt.runs.liveRunRows() {
					if !knownRun[r.WorkflowID] {
						select {
						case badRun <- r.WorkflowID:
						default:
						}
					}
				}
				for _, id := range chatIDs {
					_ = rt.coord.turns.live(id)
					_ = rt.coord.turns.openTurnIDs(id)
				}
			}
		})
	}

	// On the test goroutine, twice, so the second lands with every writer running.
	for range 2 {
		if p := connectPayload(t, rt, ""); !p.BusyStated {
			t.Error("a connect taken while the lifecycle set is mutating withholds its busy " +
				"list, so the client retracts nothing and a stale `thinking` survives")
		}
	}
	close(stop)
	wg.Wait()

	select {
	case id := <-badChat:
		t.Errorf("busyChatIDs reported chat %q, which no fixture minted", id)
	default:
	}
	select {
	case id := <-badRun:
		t.Errorf("liveRunRows reported run %q, which no fixture minted", id)
	default:
	}
}
