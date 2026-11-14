package command

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// closingTurnHost answers TurnIsOpen from open, which a test flips to stand in for a close.
type closingTurnHost struct {
	*benchDeps
	open atomic.Bool
}

func (h *closingTurnHost) TurnIsOpen(marotte.ChatID, string) bool { return h.open.Load() }

// closeOnLock is a steer queue whose lock wait runs onLock first: the close and reopen
// that land while an old prompt waits for the chat's steer lock.
type closeOnLock struct {
	*stubSteerQueue
	onLock func()
}

func (q closeOnLock) LockSteerOps(ctx context.Context, chatID marotte.ChatID) (func(), error) {
	q.onLock()
	return q.stubSteerQueue.LockSteerOps(ctx, chatID)
}

// An old prompt past StartTurn whose turn a close ended while it waited for the steer lock
// leaves the reopened chat's record alone: no end resolved, no post-load clear.
func TestSettleSteersAtStart_AClosedTurnTouchesNoRecord(t *testing.T) {
	for _, tc := range []struct {
		name      string
		closeTurn bool
		wantCalls []string
	}{
		{name: "the turn closed during the lock wait", closeTurn: true, wantCalls: []string{"lock", "unlock"}},
		{name: "the turn is still open", wantCalls: []string{
			"lock", "job-rows", "end-unsent", "post-load-cleared", "unlock",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := &closingTurnHost{benchDeps: newBenchDeps()}
			host.open.Store(true)
			stub := newStubSteerQueue()
			stub.postLoad = true
			stub.ends = []SteerEnd{{Owner: "t-new", End: SteerTurnEnd{TurnSeq: 1}}}
			stub.jobRows = []SteerRow{}
			q := closeOnLock{stubSteerQueue: stub, onLock: func() {
				if tc.closeTurn {
					host.open.Store(false)
				}
			}}
			roles := steerRolesOf(host, NewSteerLedger(), q)

			settleSteersAtStart(t.Context(), roles, "c1", "t-old")

			if got := stub.callLog(); !slices.Equal(got, tc.wantCalls) {
				t.Errorf("steer record calls = %v, want %v", got, tc.wantCalls)
			}
		})
	}
}
