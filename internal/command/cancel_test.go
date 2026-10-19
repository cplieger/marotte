package command

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// cancelDeps answers CancelPendingPermissions with err, recording the scope and whether the context it was handed
// was still live once the caller's had ended.
type cancelDeps struct {
	*closeDeps
	err         error
	onCancel    func()
	scopes      []CancelScope
	ctxLive     bool
	killed      int
	inputClears []CancelScope
}

func (d *cancelDeps) CancelPendingPermissions(ctx context.Context, _ marotte.ChatID, scope CancelScope) error {
	d.scopes = append(d.scopes, scope)
	if d.onCancel != nil {
		d.onCancel()
	}
	d.ctxLive = ctx.Err() == nil
	return d.err
}

func (d *cancelDeps) ClearPendingInputAsks(_ marotte.ChatID, scope CancelScope) {
	d.inputClears = append(d.inputClears, scope)
}

func (d *cancelDeps) KillForTurn(turn TurnRef) {
	d.killed++
	d.benchDeps.KillForTurn(turn)
}

// ctxBridge records whether each Notify's context was live.
type ctxBridge struct {
	cancelBridge
	live []bool
}

func (b *ctxBridge) Notify(ctx context.Context, method string, params any) error {
	b.live = append(b.live, ctx.Err() == nil)
	return b.cancelBridge.Notify(ctx, method, params)
}

func newCancelDeps(b Bridge) *cancelDeps {
	return &cancelDeps{closeDeps: &closeDeps{benchDeps: newBenchDeps(), bridge: b}}
}

func runCancel(t *testing.T, ctx context.Context, d *cancelDeps) error {
	t.Helper()
	_, err := cmdCancel(ctx, d, d, d, d, newStubSteerQueue(), &marotte.ClientCommand{ChatID: "c1"})
	return err
}

// Stop answers only the chat session's own asks: a chat-parented run keeps running, so its step's card stays.
func TestCancel_AnswersTheChatSessionScopeThenSessionCancel(t *testing.T) {
	b := &cancelBridge{}
	d := newCancelDeps(b)
	if err := runCancel(t, t.Context(), d); err != nil {
		t.Fatalf("cmdCancel: %v", err)
	}
	if len(d.scopes) != 1 || d.scopes[0] != ScopeChatSession {
		t.Errorf("permission scopes = %v, want [ScopeChatSession]", d.scopes)
	}
	if len(d.inputClears) != 1 || d.inputClears[0] != ScopeChatSession {
		t.Errorf("input clears = %v, want [ScopeChatSession]", d.inputClears)
	}
	if len(b.notified) != 1 || b.notified[0] != marotte.MethodCancel {
		t.Errorf("notifications = %v, want [%s]", b.notified, marotte.MethodCancel)
	}
	if d.killed != 1 {
		t.Errorf("KillForTurn calls = %d, want 1", d.killed)
	}
}

// The browser can leave the moment Stop is pressed; the answers and the session/cancel still go out.
func TestCancel_CallerContextCancelledAfterTheTakeStillAnswersAndCancels(t *testing.T) {
	b := &ctxBridge{}
	d := newCancelDeps(b)
	ctx, cancel := context.WithCancel(t.Context())
	d.onCancel = cancel
	if err := runCancel(t, ctx, d); err != nil {
		t.Fatalf("cmdCancel: %v", err)
	}
	if !d.ctxLive {
		t.Error("the permission answers ran on the caller's cancelled context")
	}
	if len(b.live) != 1 || !b.live[0] {
		t.Errorf("session/cancel contexts live = %v, want [true]", b.live)
	}
}

// A card that could not be answered on a live session leaves the reply running: no session/cancel, its terminals
// alive, and a 502 that tells the user to press Stop again.
func TestCancel_AnUndeliveredAnswerIsBadGatewayAndSendsNoSessionCancel(t *testing.T) {
	b := &cancelBridge{}
	d := newCancelDeps(b)
	d.err = fmt.Errorf("%w: write refused", ErrCancelNotDelivered)
	err := runCancel(t, t.Context(), d)
	if got := statusOf(err); got != http.StatusBadGateway {
		t.Errorf("status = %d (%v), want 502", got, err)
	}
	if err == nil || !strings.Contains(err.Error(), "Press Stop again") {
		t.Errorf("error = %v, want the press-Stop-again text", err)
	}
	if len(b.notified) != 0 {
		t.Errorf("notifications = %v, want none", b.notified)
	}
	if d.killed != 0 || len(d.inputClears) != 0 {
		t.Errorf("KillForTurn = %d, input clears = %v; want neither while the reply goes on", d.killed, d.inputClears)
	}
}

// An engine end under the answers has already stopped the reply, so Stop succeeds and writes nothing more.
func TestCancel_EngineEndDuringTheAnswersSendsNoSessionCancel(t *testing.T) {
	b := &cancelBridge{}
	d := newCancelDeps(b)
	d.err = fmt.Errorf("%w: ended", marotte.ErrBridgeExited)
	if err := runCancel(t, t.Context(), d); err != nil {
		t.Fatalf("cmdCancel = %v, want 200", err)
	}
	if len(b.notified) != 0 {
		t.Errorf("notifications = %v, want none", b.notified)
	}
}

// A teardown answers the chat's asks and its runs' (it cancels the runs next), and an undelivered answer is logged
// while the teardown still runs: the session's close settles what is left.
func TestCloseTeardown_UndeliveredAnswerStillTearsDown(t *testing.T) {
	logs := captureLogs(t)
	b := &cancelBridge{}
	d := newCancelDeps(b)
	d.err = fmt.Errorf("%w: write refused", ErrCancelNotDelivered)

	closeChatTeardown(t.Context(), d, d, d, d, "c1")

	if len(d.scopes) != 1 || d.scopes[0] != ScopeChatAndRuns {
		t.Errorf("permission scopes = %v, want [ScopeChatAndRuns]", d.scopes)
	}
	if len(b.notified) != 0 {
		t.Errorf("notifications = %v, want none", b.notified)
	}
	if len(d.closed) != 1 {
		t.Errorf("closed state for %v, want [c1]", d.closed)
	}
	if !strings.Contains(logs.String(), "close: turn cancel failed") {
		t.Errorf("no failure line logged: %s", logs.String())
	}
}

// assertAnswersBeforeCancel checks one teardown answered every ask of the chat and its runs, and only then wrote
// session/cancel.
func assertAnswersBeforeCancel(t *testing.T, d *cancelDeps, b *cancelBridge, notifiedAtAnswers int) {
	t.Helper()
	if len(d.scopes) != 1 || d.scopes[0] != ScopeChatAndRuns {
		t.Errorf("permission scopes = %v, want [ScopeChatAndRuns]", d.scopes)
	}
	if notifiedAtAnswers != 0 {
		t.Errorf("%d notifications were written before the asks were answered, want none", notifiedAtAnswers)
	}
	if len(b.notified) != 1 || b.notified[0] != marotte.MethodCancel {
		t.Errorf("notifications = %v, want [%s]", b.notified, marotte.MethodCancel)
	}
}

func TestCloseTeardown_AnswersChatAndStepAsksBeforeSessionCancel(t *testing.T) {
	b := &cancelBridge{}
	d := newCancelDeps(b)
	notifiedAtAnswers := -1
	d.onCancel = func() { notifiedAtAnswers = len(b.notified) }

	closeChatTeardown(t.Context(), d, d, d, d, "c1")

	assertAnswersBeforeCancel(t, d, b, notifiedAtAnswers)
	if len(d.closed) != 1 {
		t.Errorf("closed state for %v, want [c1]", d.closed)
	}
}

func TestDeleteTeardown_AnswersChatAndStepAsksBeforeSessionCancel(t *testing.T) {
	b := &cancelBridge{}
	d := newCancelDeps(b)
	notifiedAtAnswers := -1
	d.onCancel = func() { notifiedAtAnswers = len(b.notified) }

	deleteChatTeardown(t.Context(), d, d, d, d, "c1", []string{"sess-1"})

	assertAnswersBeforeCancel(t, d, b, notifiedAtAnswers)
}

// Each stop records who issued it before its session/cancel goes out: the reader's Stop, or a tab close or chat
// delete, whose teardown follows the cancel. The steering clear inside that cancel reads the cause.
func TestStop_RecordsItsIssuersCause(t *testing.T) {
	for _, tc := range []struct {
		run  func(t *testing.T, d *cancelDeps)
		name string
		want marotte.TurnStopCause
	}{
		{name: "the reader's stop", want: marotte.TurnStopReader, run: func(t *testing.T, d *cancelDeps) {
			if err := runCancel(t, t.Context(), d); err != nil {
				t.Fatalf("cmdCancel: %v", err)
			}
		}},
		{name: "a tab close", want: marotte.TurnStopTeardown, run: func(t *testing.T, d *cancelDeps) {
			closeChatTeardown(t.Context(), d, d, d, d, "c1")
		}},
		{name: "a chat delete", want: marotte.TurnStopTeardown, run: func(t *testing.T, d *cancelDeps) {
			deleteChatTeardown(t.Context(), d, d, d, d, "c1", []string{"sess-1"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newCancelDeps(&cancelBridge{})

			tc.run(t, d)

			if len(d.stopCauses) != 1 || d.stopCauses[0] != tc.want {
				t.Errorf("stop causes = %v, want [%v]", d.stopCauses, tc.want)
			}
		})
	}
}

// A stop acts on the turns it captured at its admission, never re-read: the own turn's terminals, a superseded
// prompt's terminals too, then the grace on those targets, and its send hold lifts last. An undelivered cancel
// arms nothing and still lifts the hold.
func TestCancel_ActsOnTheTurnsCapturedAtItsAdmission(t *testing.T) {
	targets := TurnStop{Own: TurnRef{Chat: "c1", Turn: "t-wire"}, Call: TurnRef{Chat: "c1", Turn: "t-prompt"}, Wire: true}
	t.Run("delivered", func(t *testing.T) {
		d := newCancelDeps(&cancelBridge{})
		d.stopTargets = targets
		if err := runCancel(t, t.Context(), d); err != nil {
			t.Fatalf("cmdCancel: %v", err)
		}
		want := []string{"request", "kill t-wire", "kill t-prompt", "arm", "release"}
		if !slices.Equal(d.stopSteps, want) {
			t.Errorf("stop steps = %q, want %q", d.stopSteps, want)
		}
	})
	t.Run("the call is the own turn", func(t *testing.T) {
		d := newCancelDeps(&cancelBridge{})
		d.stopTargets = TurnStop{Own: targets.Call, Call: targets.Call}
		if err := runCancel(t, t.Context(), d); err != nil {
			t.Fatalf("cmdCancel: %v", err)
		}
		want := []string{"request", "kill t-prompt", "arm", "release"}
		if !slices.Equal(d.stopSteps, want) {
			t.Errorf("stop steps = %q, want %q", d.stopSteps, want)
		}
	})
	t.Run("undelivered", func(t *testing.T) {
		d := newCancelDeps(&cancelBridge{})
		d.stopTargets = targets
		d.err = fmt.Errorf("%w: write refused", ErrCancelNotDelivered)
		if got := statusOf(runCancel(t, t.Context(), d)); got != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", got)
		}
		want := []string{"request", "release"}
		if !slices.Equal(d.stopSteps, want) {
			t.Errorf("stop steps = %q, want %q", d.stopSteps, want)
		}
	})
}
