package command

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

type benchDeps struct {
	// holder is what AdmissionHolderSource answers, gated by holderOpen — so a
	// test picks the admission window a steer meets: a prime for that refusal,
	// a shell, or nothing for an idle chat. newBenchDeps defaults to a held
	// PROMPT turn, the situation a steer exists for.
	holder     marotte.TurnOpenSource
	holderOpen bool
	// effortRecords is every PersistEffortChange the double saw, so a test can
	// assert one write for an accepted tier and NONE for the three cases that
	// must leave no transcript row.
	effortRecords []effortRecord
	// thinkingDefaultOff names the models whose own default is thinking off.
	thinkingDefaultOff map[string]bool
	// stops is every chat RequestStop recorded, in order.
	stops []marotte.ChatID
}

type effortRecord struct {
	chatID marotte.ChatID
	model  string
	level  marotte.EffortLevel
}

func newBenchDeps() *benchDeps {
	return &benchDeps{holder: marotte.TurnSourcePrompt, holderOpen: true}
}

// Roles holds chatStore, so benchDeps answers the store methods directly.
func (*benchDeps) Get(context.Context, marotte.ChatID) (*marotte.Chat, bool) { return nil, false }

func (*benchDeps) Mutate(context.Context, marotte.ChatID, func(*marotte.Chat, bool) bool) (string, error) {
	return "", nil
}

func (*benchDeps) Revert(context.Context, marotte.ChatID, string, string) (*marotte.Entry, []*marotte.Entry, error) {
	return &marotte.Entry{Kind: marotte.EntryKindTurnRevert}, nil, nil
}

func (*benchDeps) RewindTarget(context.Context, marotte.ChatID, string) (marotte.RewindTarget, bool, error) {
	return marotte.RewindTarget{}, false, nil
}

func (*benchDeps) PromptReceipt(context.Context, marotte.ChatID, string) (marotte.PromptReceipt, error) {
	return marotte.PromptReceipt{}, nil
}

func (*benchDeps) PromptAttachmentPaths(context.Context, marotte.ChatID, string) ([]string, error) {
	return nil, nil
}

func (*benchDeps) TurnCount(context.Context, marotte.ChatID) (uint64, bool) { return 0, false }

func (*benchDeps) SetDraft(context.Context, marotte.ChatID, string) (*marotte.ComposerState, error) {
	return nil, nil
}

func (*benchDeps) SetAttachments(context.Context, marotte.ChatID, []string) (*marotte.ComposerState, error) {
	return nil, nil
}
func (*benchDeps) Delete(context.Context, marotte.ChatID) ([]string, error) { return nil, nil }
func (*benchDeps) Broadcast(context.Context, marotte.ServerEvent)           {}
func (*benchDeps) Bridge(marotte.ChatID) Bridge                             { return nil }
func (*benchDeps) OpenBridge(context.Context, marotte.ChatID, string) (Bridge, error) {
	return nil, nil
}

// BridgeLive answers true: the stub models a chat whose bridge is past its spawn,
// so a steer takes the wire arm rather than parking. bridgeDeps overrides it to
// follow the bridge it holds.
func (*benchDeps) BridgeLive(marotte.ChatID) bool { return true }

func (*benchDeps) CloseBridge(context.Context, marotte.ChatID, marotte.TurnOutcome) {}

func (*benchDeps) ClearPendingPermsForChat(marotte.ChatID) {}

func (*benchDeps) TakePendingPerm(marotte.ChatID, int64, marotte.SettledBy) (AskReply, bool) {
	return nopReply{}, true
}

func (*benchDeps) TakePendingPermissionOption(marotte.ChatID, int64, string, marotte.SettledBy) (AskReply, bool, bool) {
	return nopReply{}, true, true
}

// nopReply answers a claimed ask nowhere.
type nopReply struct{}

func (nopReply) Respond(context.Context, any) (AnswerOutcome, error) { return AnswerDelivered, nil }

func (*benchDeps) PendingPermission(marotte.ChatID, int64) (marotte.PermissionNeededPayload, bool) {
	return marotte.PermissionNeededPayload{}, false
}

func (*benchDeps) EnsureCustomProfile(context.Context) error { return nil }

func (*benchDeps) TurnContext(reqCtx context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(context.WithoutCancel(reqCtx))
}
func (*benchDeps) InflightAdd(int) {}
func (*benchDeps) InflightDone()   {}
func (*benchDeps) Draining() bool  { return false }
func (*benchDeps) DeleteChatStateByChain(context.Context, marotte.ChatID, []string, RunStopCause) {
}
func (*benchDeps) CloseChatState(context.Context, marotte.ChatID) {}
func (*benchDeps) BeginChatTeardown(marotte.ChatID, bool)         {}
func (*benchDeps) KillForTurn(marotte.ChatID)                     {}

// Every id-keyed method accepts it.
const benchTurnID = "t-bench"

func (*benchDeps) OpenTurn(context.Context, marotte.ChatID, TurnOpen) (string, error) {
	return benchTurnID, nil
}

// StartTurn answers true: false is the REFUSAL (a dead ctx, or an id the registry
// dropped), and a stub answering it makes every prompt and `!cmd` test close its
// turn before the call.
func (*benchDeps) StartTurn(context.Context, marotte.ChatID, string) bool { return true }

// ReserveTurnForPrompt admits every prompt: contention is a per-test double's
// business, not the bench stub's.
func (*benchDeps) ReserveTurnForPrompt(context.Context, marotte.ChatID, time.Duration) AdmissionOutcome {
	return AdmissionAcquired
}

func (*benchDeps) TryReserveTurn(marotte.ChatID, marotte.TurnOpenSource) bool { return true }

func (d *benchDeps) TryReserveIdleTurn(marotte.ChatID, marotte.TurnOpenSource) bool {
	return !d.holderOpen
}

func (*benchDeps) TryReserveTurnFenced(marotte.ChatID, marotte.TurnOpenSource, TurnFence) bool {
	return true
}
func (*benchDeps) PromptHolder(marotte.ChatID) (string, bool) { return "", false }
func (*benchDeps) ReleaseTurnReservation(marotte.ChatID)      {}

func (*benchDeps) AwaitTurn(context.Context, marotte.ChatID, string) (marotte.TurnResult, error) {
	return marotte.TurnResult{}, marotte.ErrNoSuchTurn
}

func (*benchDeps) AwaitTurnBound(context.Context, marotte.ChatID, string) (bool, error) {
	return false, marotte.ErrNoSuchTurn
}

func (*benchDeps) ReleaseTurn(marotte.ChatID, string) {}
func (*benchDeps) SettleTurnOnResponse(context.Context, marotte.ChatID, string, uint64, *marotte.RPCResponse) {
}

func (*benchDeps) TurnOpenedAfter(marotte.ChatID, string) bool { return false }

func (*benchDeps) StopRequestedAfter(marotte.ChatID, string) bool { return false }

func (d *benchDeps) RequestStop(chatID marotte.ChatID) { d.stops = append(d.stops, chatID) }

func (d *benchDeps) AdmissionHolderSource(marotte.ChatID) (marotte.TurnOpenSource, bool) {
	return d.holder, d.holderOpen
}

func (*benchDeps) FinalizeLocalShellTurn(context.Context, marotte.ChatID, string, string) {}

func (*benchDeps) AbandonInFlightTurn(context.Context, marotte.ChatID, string, marotte.StopReason, string, marotte.FailureKind, uint64) {
}

// The mode recorder is a sink here: the tests that assert on the entry a mode
// switch leaves carry their own spy (mode_switched_test.go).
func (*benchDeps) PersistModeSwitch(context.Context, marotte.ChatID, marotte.EntryModeSwitched) {
}

func (d *benchDeps) PersistEffortChange(_ context.Context, chatID marotte.ChatID, model string, level marotte.EffortLevel) {
	d.effortRecords = append(d.effortRecords, effortRecord{chatID: chatID, model: model, level: level})
}

func (d *benchDeps) ThinkingDefaultOff(model string) bool { return d.thinkingDefaultOff[model] }

// The bench host launched no runs, so a rewind's cut never holds one.
func (*benchDeps) LiveRuns([]string) []LiveRunRef          { return nil }
func (*benchDeps) CancelRun(context.Context, string) error { return nil }

// TestBenchDeps_NoPanic verifies that every benchDeps method can be called
// with zero-value arguments without panicking.
func TestBenchDeps_NoPanic(t *testing.T) {
	d := newBenchDeps()

	if turnCtx, cancel := d.TurnContext(t.Context()); turnCtx == nil {
		t.Error("TurnContext() returned a nil context")
	} else {
		cancel()
	}

	d.Broadcast(t.Context(), marotte.ServerEvent{})
	d.CloseBridge(t.Context(), "x", marotte.TurnOutcomeCancelled)
	d.ClearPendingPermsForChat("x")
	d.TakePendingPerm("x", 0, marotte.SettledByUser)
	d.InflightAdd(1)
	d.InflightDone()
	d.DeleteChatStateByChain(t.Context(), "x", nil, RunStopChatDeleted)
	d.StartTurn(t.Context(), "x", benchTurnID)
	d.ReleaseTurn("x", benchTurnID)
}

// TestBenchDeps_Contract documents which methods intentionally return nil
// (safe only because benchmarks don't invoke handlers that call them) vs.
// which return usable values needed by the dispatch path.
func TestBenchDeps_Contract(t *testing.T) {
	d := newBenchDeps()

	t.Run("usable_values", func(t *testing.T) {
		if turnCtx, cancel := d.TurnContext(t.Context()); turnCtx == nil {
			t.Error("TurnContext must return a non-nil context")
		} else {
			cancel()
		}
	})

	t.Run("intentionally_nil", func(t *testing.T) {
		if d.Bridge("any") != nil {
			t.Error("Bridge expected nil for bench stub")
		}
	})

	t.Run("no_panic_zero_value_calls", func(t *testing.T) {
		d.Broadcast(t.Context(), marotte.ServerEvent{})
		d.CloseBridge(t.Context(), "x", marotte.TurnOutcomeCancelled)
		d.ClearPendingPermsForChat("x")
		d.TakePendingPerm("x", 0, marotte.SettledByUser)
		d.InflightAdd(1)
		d.InflightDone()
		d.DeleteChatStateByChain(t.Context(), "x", nil, RunStopChatDeleted)
		if _, err := d.AwaitTurn(t.Context(), "x", benchTurnID); err == nil {
			t.Error("AwaitTurn on the stub should report no such turn")
		}
		d.ReleaseTurn("x", benchTurnID)
		d.SettleTurnOnResponse(t.Context(), "x", benchTurnID, 0, nil)
		d.FinalizeLocalShellTurn(t.Context(), "x", benchTurnID, "")
		if d.TurnOpenedAfter("x", benchTurnID) {
			t.Error("TurnOpenedAfter on the stub should report false")
		}
		if _, err := d.OpenBridge(t.Context(), "x", ""); err != nil {
			t.Errorf("OpenBridge returned error: %v", err)
		}
	})
}

// BenchmarkDispatcherServeHTTP measures the envelope path: decode, validate, table lookup, handler.
// Replay cost belongs to the header middleware.
func BenchmarkDispatcherServeHTTP(b *testing.B) {
	d := New()
	d.Register("create_chat", func(context.Context, *marotte.ClientCommand) (any, error) {
		return responseOK, nil
	})

	body, _ := json.Marshal(marotte.ClientCommand{
		Type:   "create_chat",
		ChatID: "chat-bench-1",
	})

	b.Run("dispatch", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			req := httptest.NewRequest(http.MethodPost, "/api/command", bytes.NewReader(body))
			w := httptest.NewRecorder()
			d.ServeHTTP(w, req)
		}
	})

	b.Run("unknown_command", func(b *testing.B) {
		unknownBody, _ := json.Marshal(marotte.ClientCommand{
			Type:   "nonexistent_cmd",
			ChatID: "chat-bench-1",
		})
		b.ReportAllocs()
		for b.Loop() {
			req := httptest.NewRequest(http.MethodPost, "/api/command", bytes.NewReader(unknownBody))
			w := httptest.NewRecorder()
			d.ServeHTTP(w, req)
		}
	})
}

// hostDouble is every role a handler declares, for this package's all-in-one test doubles only;
// production has no aggregate over the roles (shape_test.go reads production files only).
type hostDouble interface {
	bridgeAccess
	chatStore
	broadcaster
	chatTeardown
	pendingPermAccess
	profileSwitcher
	terminalAccess
	turnStopper
	lifecycleAccess
	turnAdmission
	turnOutcomeAccess
	effortRecorder
	modeRecorder
}

var _ hostDouble = (*benchDeps)(nil)

func promptRolesOf(d hostDouble) *promptRoles {
	stub := newStubSteerQueue()
	return &promptRoles{
		bridges:     d,
		chats:       d,
		bus:         d,
		workspace:   Workspace{Dir: "/tmp", ConfigDir: "/tmp"},
		lifecycle:   d,
		admission:   d,
		turnOutcome: d,
		steers:      NewSteerLedger(),
		queue:       stub,
		jobs:        stub,
	}
}
