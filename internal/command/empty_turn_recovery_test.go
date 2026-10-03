package command

// The empty-turn recovery gate: three clauses, each of which has to hold before a
// prompt is re-executed and paid for twice.

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// recoveryOutcome is a TurnOutcomeAccess whose captured result and later-turn
// answer the test dictates, so each clause of the gate can be moved on its own.
// Its admission slot is a real single-holder slot, so the retry's TRY-reserve
// contract — a competitor that won the slot abandons the retry — is exercised
// rather than scripted.
type recoveryOutcome struct {
	laterTurn   bool
	reserved    bool
	openedTurns []marotte.TurnOpenSource
	mu          sync.Mutex
}

func (o *recoveryOutcome) OpenTurn(_ context.Context, _ marotte.ChatID, source marotte.TurnOpenSource, _ *marotte.EntryPrompt, _ func(*marotte.Chat)) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.openedTurns = append(o.openedTurns, source)
	return "t-" + strconv.Itoa(len(o.openedTurns)), nil
}

func (o *recoveryOutcome) StartTurn(context.Context, marotte.ChatID, string) bool { return true }

func (o *recoveryOutcome) AwaitTurn(context.Context, marotte.ChatID, string) (marotte.TurnResult, error) {
	return marotte.TurnResult{}, marotte.ErrNoSuchTurn
}

func (o *recoveryOutcome) ReleaseTurn(marotte.ChatID, string) {}

func (o *recoveryOutcome) SettleTurnOnResponse(context.Context, marotte.ChatID, string, uint64, *marotte.RPCResponse) {
}

func (o *recoveryOutcome) TurnOpenedAfter(marotte.ChatID, string) bool { return o.laterTurn }

func (o *recoveryOutcome) AdmissionHolderSource(marotte.ChatID) (marotte.TurnOpenSource, bool) {
	return 0, false
}

func (o *recoveryOutcome) ReserveTurnForPrompt(context.Context, marotte.ChatID, time.Duration) AdmissionOutcome {
	if o.TryReserveTurn("", marotte.TurnSourcePrompt) {
		return AdmissionAcquired
	}
	return AdmissionStarting
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

func (o *recoveryOutcome) FinalizeLocalShellTurn(context.Context, marotte.ChatID, string, string) {}

func (o *recoveryOutcome) AbandonInFlightTurn(context.Context, marotte.ChatID, string, marotte.StopReason, string) {
}

// recoveryBridges records whether the recovery tore the session down, which is the
// first irreversible thing it does and therefore the cleanest observable for
// "did the gate fire".
type recoveryBridges struct {
	*benchDeps
	closed int
}

func (b *recoveryBridges) CloseBridge(context.Context, marotte.ChatID, marotte.TurnOutcome) {
	b.closed++
}

func (b *recoveryBridges) OpenBridge(context.Context, marotte.ChatID, string) (Bridge, error) {
	return &recoveryBridge{}, nil
}

// recoveryBridge is a Bridge that grants the prompt slot and answers every call,
// so the firing row runs the retry to the end rather than abandoning it at the
// slot.
type recoveryBridge struct{}

func (*recoveryBridge) Call(context.Context, string, any) (*marotte.RPCResponse, error) {
	return &marotte.RPCResponse{}, nil
}

func (*recoveryBridge) CallAt(context.Context, string, any) (*marotte.RPCResponse, uint64, error) {
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

// The three clauses, each one moved on its own from a firing baseline. Every row
// but the first must NOT re-prompt.
func TestRecoverEmptyTurn_GateRequiresAllThreeClauses(t *testing.T) {
	firing := marotte.TurnResult{
		Stop:           marotte.StopReasonEndTurn,
		EmittedNothing: true,
		WireEnded:      true,
	}
	cases := []struct {
		name      string
		result    marotte.TurnResult
		laterTurn bool
		wantFire  bool
	}{
		{
			name:     "all three hold, so the turn really did end empty on the wire",
			result:   firing,
			wantFire: true,
		},
		{
			// A locally-closed turn's outcome is the prompt response's, which can be
			// nothing richer than end_turn or cancelled and carries nothing on a fault.
			// `end_turn` there says only that marotte had nothing better to call it.
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
			// The STRUCTURAL clause, and the only one that depends on no frame arriving.
			// A zero-content or tool-only auto-wake never sends agentInitiated, so a
			// mis-binding is never revised and the mis-bound pre-open closes with the
			// first three clauses satisfied. What it necessarily violates is this one:
			// the real agent-initiated turn is a later turn on the same chat.
			name:      "a later turn opened, so the bracket this turn closed on was not ours",
			result:    firing,
			laterTurn: true,
			wantFire:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome := &recoveryOutcome{laterTurn: tc.laterTurn}
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
