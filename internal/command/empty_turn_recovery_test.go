package command

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// recoveryOutcome is a turnOutcomeAccess whose captured result and later-turn answer the test
// dictates, with a real single-holder admission slot so the retry's try-reserve contract is
// exercised.
type recoveryOutcome struct {
	laterTurn   bool
	stopped     bool
	reserved    bool
	openedTurns []marotte.TurnOpenSource
	mu          sync.Mutex
}

func (o *recoveryOutcome) OpenTurn(_ context.Context, _ marotte.ChatID, open TurnOpen) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.openedTurns = append(o.openedTurns, open.Source)
	return "t-" + strconv.Itoa(len(o.openedTurns)), nil
}

func (*recoveryOutcome) StartTurn(context.Context, marotte.ChatID, string) bool { return true }

func (*recoveryOutcome) AwaitTurn(context.Context, marotte.ChatID, string) (marotte.TurnResult, error) {
	return marotte.TurnResult{}, marotte.ErrNoSuchTurn
}

func (*recoveryOutcome) AwaitTurnBound(context.Context, marotte.ChatID, string) (bool, error) {
	return false, marotte.ErrNoSuchTurn
}

func (*recoveryOutcome) ReleaseTurn(marotte.ChatID, string) {}

func (*recoveryOutcome) SettleTurnOnResponse(context.Context, marotte.ChatID, string, uint64, *marotte.RPCResponse) {
}

func (o *recoveryOutcome) TurnOpenedAfter(marotte.ChatID, string) bool { return o.laterTurn }

func (o *recoveryOutcome) StopRequestedAfter(marotte.ChatID, string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.stopped
}

func (*recoveryOutcome) AdmissionHolderSource(marotte.ChatID) (marotte.TurnOpenSource, bool) {
	return 0, false
}

func (o *recoveryOutcome) ReserveTurnForPrompt(context.Context, marotte.ChatID, time.Duration) AdmissionOutcome {
	if o.TryReserveTurn("", marotte.TurnSourcePrompt) {
		return AdmissionAcquired
	}
	return AdmissionStarting
}

func (*recoveryOutcome) PromptHolder(marotte.ChatID) (string, bool) { return "", false }

func (o *recoveryOutcome) TryReserveIdleTurn(c marotte.ChatID, s marotte.TurnOpenSource) bool {
	return o.TryReserveTurn(c, s)
}

func (o *recoveryOutcome) TryReserveTurnFenced(c marotte.ChatID, s marotte.TurnOpenSource, _ TurnFence) bool {
	return o.TryReserveTurn(c, s)
}

func (o *recoveryOutcome) TryReserveTurn(marotte.ChatID, marotte.TurnOpenSource) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.reserved {
		return false
	}
	o.reserved = true
	return true
}

func (o *recoveryOutcome) ReleaseTurnReservation(marotte.ChatID) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.reserved = false
}

func (*recoveryOutcome) FinalizeLocalShellTurn(context.Context, marotte.ChatID, string, string) {}

func (*recoveryOutcome) AbandonInFlightTurn(context.Context, marotte.ChatID, string, marotte.StopReason, string, marotte.FailureKind, uint64) {
}

// recoveryBridges records whether the recovery tore the session down, which is the
// first irreversible thing it does and therefore the cleanest observable for
// "did the gate fire".
type recoveryBridges struct {
	*benchDeps
	// onOpen runs inside OpenBridge, standing in for whatever lands mid-spawn.
	onOpen func()
	bridge *recoveryBridge
	closed int
}

func (b *recoveryBridges) CloseBridge(context.Context, marotte.ChatID, marotte.TurnOutcome) {
	b.closed++
}

func (b *recoveryBridges) OpenBridge(context.Context, marotte.ChatID, string) (Bridge, error) {
	if b.onOpen != nil {
		b.onOpen()
	}
	if b.bridge == nil {
		b.bridge = &recoveryBridge{}
	}
	return b.bridge, nil
}

// recoveryBridge is a Bridge that grants the prompt slot and answers every call,
// so the firing row runs the retry to the end rather than abandoning it at the
// slot.
type recoveryBridge struct {
	calls int
}

func (*recoveryBridge) Call(context.Context, string, any) (*marotte.RPCResponse, error) {
	return &marotte.RPCResponse{}, nil
}

func (b *recoveryBridge) CallAt(context.Context, string, any) (*marotte.RPCResponse, uint64, error) {
	b.calls++
	return &marotte.RPCResponse{}, 0, nil
}

func (*recoveryBridge) Notify(context.Context, string, any) error        { return nil }
func (*recoveryBridge) Respond(context.Context, int64, any, error) error { return nil }
func (*recoveryBridge) SessionID() marotte.SessionID                     { return "s1" }
func (*recoveryBridge) TryAcquireForPrompt() bool                        { return true }
func (*recoveryBridge) ReleaseAfterPrompt()                              {}
func (*recoveryBridge) BeginPromptCall(context.CancelCauseFunc) uint64   { return 1 }
func (*recoveryBridge) EndPromptCall()                                   {}
func (*recoveryBridge) ArmCancelGrace(uint64, time.Duration) bool        { return true }
func (*recoveryBridge) PromptGeneration() uint64                         { return 1 }

// The four clauses, each one moved on its own from a firing baseline. Every row
// but the first must NOT re-prompt.
func TestRecoverEmptyTurn_GateRequiresAllClauses(t *testing.T) {
	firing := marotte.TurnResult{
		Stop:           marotte.StopReasonEndTurn,
		EmittedNothing: true,
		WireEnded:      true,
	}
	cases := []struct {
		name      string
		result    marotte.TurnResult
		laterTurn bool
		stopped   bool
		wantFire  bool
	}{
		{
			name:     "all three hold, so the turn really did end empty on the wire",
			result:   firing,
			wantFire: true,
		},
		{
			name:     "the turn was closed LOCALLY, so its end_turn is an inference",
			result:   marotte.TurnResult{Stop: marotte.StopReasonEndTurn, EmittedNothing: true},
			wantFire: false,
		},
		{
			name:     "the turn emitted content, so there is nothing to recover",
			result:   marotte.TurnResult{Stop: marotte.StopReasonEndTurn, WireEnded: true},
			wantFire: false,
		},
		{
			name:     "the wire named a different outcome",
			result:   marotte.TurnResult{Stop: marotte.StopReasonRefusal, EmittedNothing: true, WireEnded: true},
			wantFire: false,
		},
		{
			// The structural clause, the one that depends on no frame arriving: a mis-bound
			// pre-open satisfies the first three clauses, but the real agent-initiated turn is a
			// later turn on the same chat.
			name:      "a later turn opened, so the bracket this turn closed on was not ours",
			result:    firing,
			laterTurn: true,
			wantFire:  false,
		},
		{
			name:     "a stop was requested after the turn opened",
			result:   firing,
			stopped:  true,
			wantFire: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome := &recoveryOutcome{laterTurn: tc.laterTurn, stopped: tc.stopped}
			bridges := &recoveryBridges{benchDeps: newBenchDeps()}
			p := &marotte.PromptCommand{Text: "an ordinary question", MessageID: "m1"}

			roles := promptRolesOf(bridges)
			roles.admission = outcome
			roles.turnOutcome = outcome

			recoverEmptyTurn(t.Context(), roles, "c1", "t-0", tc.result, p, map[string]any{})

			fired := bridges.closed > 0
			if fired != tc.wantFire {
				t.Errorf("session torn down = %v, want %v: the recovery re-executes the prompt and pays for it twice",
					fired, tc.wantFire)
			}
			retried := len(outcome.openedTurns) > 0
			if retried != tc.wantFire {
				t.Errorf("a retry turn was opened = %v, want %v", retried, tc.wantFire)
			}
			if tc.wantFire && !hasSource(outcome.openedTurns, marotte.TurnSourceEmptyRetry) {
				t.Errorf("the retry opened %v, want an emptyRetry turn: its reply must not extend "+
					"the message of the turn it replaced", outcome.openedTurns)
			}
		})
	}
}

// TestRecoverEmptyTurn_StopDuringRespawnSendsNothing: a stop landing while the
// retry's bridge spawns reaches no bridge that could carry it, so the retry's own
// read is the only thing between the reader's Stop and a second paid prompt.
func TestRecoverEmptyTurn_StopDuringRespawnSendsNothing(t *testing.T) {
	outcome := &recoveryOutcome{}
	bridges := &recoveryBridges{benchDeps: newBenchDeps()}
	bridges.onOpen = func() {
		outcome.mu.Lock()
		outcome.stopped = true
		outcome.mu.Unlock()
	}
	roles := promptRolesOf(bridges)
	roles.admission = outcome
	roles.turnOutcome = outcome
	firing := marotte.TurnResult{Stop: marotte.StopReasonEndTurn, EmittedNothing: true, WireEnded: true}

	recoverEmptyTurn(t.Context(), roles, "c1", "t-0", firing, &marotte.PromptCommand{Text: "q", MessageID: "m1"}, map[string]any{})

	if bridges.closed != 1 {
		t.Errorf("session refreshes = %d, want 1: the stop landed after the refresh", bridges.closed)
	}
	if len(outcome.openedTurns) != 0 {
		t.Errorf("turns opened = %v, want none for a stopped retry", outcome.openedTurns)
	}
	if bridges.bridge == nil || bridges.bridge.calls != 0 {
		t.Errorf("prompt calls on the respawned bridge = %v, want 0", bridges.bridge)
	}
}

func hasSource(got []marotte.TurnOpenSource, want marotte.TurnOpenSource) bool {
	return slices.Contains(got, want)
}

// orderedRetryBridges hands the retry a bridge that logs its prompt into the same
// ordered log the steer lock writes to.
type orderedRetryBridges struct {
	*recoveryBridges
	add func(string)
}

func (b *orderedRetryBridges) OpenBridge(context.Context, marotte.ChatID, string) (Bridge, error) {
	return &orderedRetryBridge{add: b.add}, nil
}

type orderedRetryBridge struct {
	recoveryBridge
	add func(string)
}

func (b *orderedRetryBridge) CallAt(_ context.Context, method string, _ any) (*marotte.RPCResponse, uint64, error) {
	if method == marotte.MethodPrompt {
		b.add("prompt")
	}
	return &marotte.RPCResponse{}, 0, nil
}

func (b *orderedRetryBridge) Call(ctx context.Context, method string, params any) (*marotte.RPCResponse, error) {
	resp, _, err := b.CallAt(ctx, method, params)
	return resp, err
}

type orderedSteerLock struct {
	*stubSteerQueue
	add func(string)
}

func (q orderedSteerLock) LockSteerOps(context.Context, marotte.ChatID) (func(), error) {
	q.add("lock")
	return func() { q.add("unlock") }, nil
}

// The retry's execution starts only after its drain has held the chat's steer lock,
// as a prompt's does, so a delete in flight ends before the retry's cursor reads.
func TestRecoverEmptyTurn_TheRetryDrainsUnderTheSteerLockBeforeItsPrompt(t *testing.T) {
	var mu sync.Mutex
	var order []string
	add := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, s)
	}
	outcome := &recoveryOutcome{}
	roles := promptRolesOf(&orderedRetryBridges{recoveryBridges: &recoveryBridges{benchDeps: newBenchDeps()}, add: add})
	roles.admission = outcome
	roles.turnOutcome = outcome
	q := orderedSteerLock{stubSteerQueue: newStubSteerQueue(), add: add}
	roles.queue, roles.jobs = q, q
	firing := marotte.TurnResult{Stop: marotte.StopReasonEndTurn, EmittedNothing: true, WireEnded: true}

	recoverEmptyTurn(t.Context(), roles, "c1", "t-0", firing, &marotte.PromptCommand{Text: "q", MessageID: "m1"}, map[string]any{})

	mu.Lock()
	defer mu.Unlock()
	if want := []string{"lock", "unlock", "prompt"}; !slices.Equal(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
}
