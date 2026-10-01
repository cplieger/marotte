package command

// `ErrorPayload.TurnScoped` lets the client drop the toast for a chat already on
// screen, on the grounds that the turn's own card holds the reason. It is a property
// of the EMISSION rather than of the code: `prompt_failed` has three emitters and
// `recovery_failed` two, and three of the five open no turn at all, so for those the
// toast is the only surface a per-code answer would silence.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// surfaceDeps records the two durable surfaces a failure can reach — the error
// frames it broadcasts and the turn closes it runs through the turn end rule. It
// embeds benchDeps and overrides only what it records, so an emitter reaching a
// surface this double does not model shows up as a missing observation rather than
// a compile error.
type surfaceDeps struct {
	*benchDeps
	mu        sync.Mutex
	errors    []marotte.ErrorPayload
	abandoned []abandonedTurn
	// spawnErr, when set, is what OpenBridge answers: the respawn-failure path.
	spawnErr error
	// slotHeld makes TryAcquireForPrompt refuse, which is the held-bridge-slot path.
	slotHeld bool
	// startRefused makes StartTurn answer false, the cancelled-during-spawn path.
	startRefused bool
	// callErr, when set, fails the prompt Call itself.
	callErr error
}

// abandonedTurn is one AbandonInFlightTurn call: the turn end rule run on a turn
// the prompt could not finish, with the stop it concluded and the reason the
// turn_close carries.
type abandonedTurn struct {
	turn   string
	stop   marotte.StopReason
	reason string
}

func newSurfaceDeps() *surfaceDeps {
	return &surfaceDeps{benchDeps: newBenchDeps()}
}

func (d *surfaceDeps) Broadcast(_ context.Context, e marotte.ServerEvent) {
	p, ok := e.Payload.(marotte.ErrorPayload)
	if !ok {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.errors = append(d.errors, p)
}

func (d *surfaceDeps) AbandonInFlightTurn(_ context.Context, _ marotte.ChatID, turn string, stop marotte.StopReason, reason string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.abandoned = append(d.abandoned, abandonedTurn{turn: turn, stop: stop, reason: reason})
}

func (d *surfaceDeps) OpenBridge(context.Context, marotte.ChatID, string) (Bridge, error) {
	if d.spawnErr != nil {
		return nil, d.spawnErr
	}
	return &surfaceBridge{deps: d}, nil
}

func (d *surfaceDeps) StartTurn(context.Context, marotte.ChatID, string) bool { return !d.startRefused }

func (d *surfaceDeps) ReserveTurnForPrompt(context.Context, marotte.ChatID, time.Duration) AdmissionOutcome {
	return AdmissionAcquired
}

func (d *surfaceDeps) TryReserveTurn(marotte.ChatID, marotte.TurnOpenSource) bool { return true }

// onlyError fails when the run produced other than one error frame: a second frame
// would mean two surfaces claiming one failure, which is what this file is about.
func (d *surfaceDeps) onlyError(t *testing.T) marotte.ErrorPayload {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.errors) != 1 {
		t.Fatalf("broadcast %d error frames, want exactly 1: %+v", len(d.errors), d.errors)
	}
	return d.errors[0]
}

// surfaceBridge answers a prompt the way the deps dictate.
type surfaceBridge struct{ deps *surfaceDeps }

func (b *surfaceBridge) Call(context.Context, string, any) (*marotte.RPCResponse, error) {
	return &marotte.RPCResponse{}, b.deps.callErr
}

func (b *surfaceBridge) CallAt(context.Context, string, any) (*marotte.RPCResponse, uint64, error) {
	return &marotte.RPCResponse{}, 0, b.deps.callErr
}

func (*surfaceBridge) Notify(context.Context, string, any) error        { return nil }
func (*surfaceBridge) Respond(context.Context, int64, any, error) error { return nil }
func (*surfaceBridge) SessionID() marotte.SessionID                     { return "s1" }
func (b *surfaceBridge) TryAcquireForPrompt() bool                      { return !b.deps.slotHeld }
func (*surfaceBridge) ReleaseAfterPrompt()                              {}
func (*surfaceBridge) BeginPromptCall(context.CancelCauseFunc) uint64   { return 1 }
func (*surfaceBridge) EndPromptCall()                                   {}
func (*surfaceBridge) ArmCancelGrace(uint64, time.Duration) bool        { return true }
func (*surfaceBridge) PromptGeneration() uint64                         { return 1 }

// A turn WAS finalized, so the reason is on its card and the toast may stand down.
func TestReportPromptFailure_MarksTheFrameTurnScoped(t *testing.T) {
	deps := newSurfaceDeps()
	reportPromptFailure(t.Context(), promptRolesOf(deps), "c1", "t-7",
		errors.New("connection reset"), time.Second, false)

	got := deps.onlyError(t)
	if got.Code != marotte.ErrCodePromptFailed {
		t.Errorf("code = %q, want %q", got.Code, marotte.ErrCodePromptFailed)
	}
	if !got.TurnScoped {
		t.Error("TurnScoped = false, want true: AbandonInFlightTurn stamps this same " +
			"reason on the turn's carrier, so the card says it durably and a toast for " +
			"the chat on screen is a second copy of it")
	}
}

// TurnScoped follows the turn, not the code: a pre-call exit that closes the
// prompt's turn through closeBeforeStart stamps its reason on that turn_close and
// reads TurnScoped, and the one exit that closes no turn leaves it false, because
// then the toast is the failure's only surface.
func TestPromptFailure_PreCallEmittersReadTurnScopedWhenTheyCloseATurn(t *testing.T) {
	cases := []struct {
		name string
		want marotte.ErrorCode
		// arrange puts the double on the path that produces the emitter's failure.
		arrange func(*surfaceDeps)
		// run drives the production path.
		run func(context.Context, *promptRoles, *marotte.PromptCommand)
		// turnScoped is whether the exit closed the prompt's turn.
		turnScoped bool
	}{
		{
			// The spawn failed, so the opened turn closes with the spawn's reason.
			name:    "the bridge could not be spawned",
			want:    marotte.ErrCodeBridgeStartFailed,
			arrange: func(d *surfaceDeps) { d.spawnErr = errors.New("no such binary") },
			run: func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand) {
				runPromptTurn(ctx, func() {}, roles, "c1", benchTurnID, p)
			},
			turnScoped: true,
		},
		{
			// A held slot despite an owned reservation is a programming error rather
			// than a fault, but the turn is open and the POST acked, so it still has
			// to report.
			name:    "the bridge slot was held despite the reservation",
			want:    marotte.ErrCodePromptFailed,
			arrange: func(d *surfaceDeps) { d.slotHeld = true },
			run: func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand) {
				runPromptTurn(ctx, func() {}, roles, "c1", benchTurnID, p)
			},
			turnScoped: true,
		},
		{
			// The most reachable: a refused start in the spawn / MCP window means no
			// ACP call, and the turn CmdPrompt opened closes with the refusal's reason.
			name:    "the turn was refused before it started",
			want:    marotte.ErrCodePromptFailed,
			arrange: func(d *surfaceDeps) { d.startRefused = true },
			run: func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand) {
				runPromptTurn(ctx, func() {}, roles, "c1", benchTurnID, p)
			},
			turnScoped: true,
		},
		{
			// The turn being replaced was already finalized and the retry's turn is
			// never opened, so this failure finalizes nothing; its frame names the
			// cause because the toast is its only surface.
			name:    "empty-turn recovery could not respawn the session",
			want:    marotte.ErrCodeRecoveryFailed,
			arrange: func(d *surfaceDeps) { d.spawnErr = errors.New("no such binary") },
			run: func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand) {
				retryEmptyTurnPrompt(ctx, roles, "c1", p, map[string]any{})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := newSurfaceDeps()
			tc.arrange(deps)
			tc.run(t.Context(), promptRolesOf(deps), &marotte.PromptCommand{Text: "hi", MessageID: "m1"})

			got := deps.onlyError(t)
			if got.Code != tc.want {
				t.Errorf("code = %q, want %q", got.Code, tc.want)
			}
			if got.TurnScoped != tc.turnScoped {
				t.Errorf("TurnScoped = %v, want %v: the frame reads turn-scoped exactly when the exit closed a turn carrying this reason", got.TurnScoped, tc.turnScoped)
			}
			if closed := len(deps.abandoned) == 1 && deps.abandoned[0].reason == got.Message; closed != tc.turnScoped {
				t.Errorf("a turn closed with the frame's reason = %v (abandoned %+v), want %v", closed, deps.abandoned, tc.turnScoped)
			}
			if got.Message == "" {
				t.Error("Message is empty: the frame is the failure's account")
			}
		})
	}
}

// The retry ran as a turn of its own and AbandonInFlightTurn stamped the reason on
// it, so this failure IS turn-scoped.
func TestRetryEmptyTurnPrompt_MarksTheRetryFailureTurnScoped(t *testing.T) {
	deps := newSurfaceDeps()
	deps.callErr = errors.New("connection reset")
	roles := promptRolesOf(deps)

	retryEmptyTurnPrompt(t.Context(), roles, "c1", &marotte.PromptCommand{Text: "hi", MessageID: "m1"}, map[string]any{})

	got := deps.onlyError(t)
	if got.Code != marotte.ErrCodeRecoveryFailed {
		t.Errorf("code = %q, want %q", got.Code, marotte.ErrCodeRecoveryFailed)
	}
	if !got.TurnScoped {
		t.Error("TurnScoped = false, want true: the retry was a turn of its own and the " +
			"abandon stamped this reason on it")
	}
}

// The reader's own Stop reaches that same branch, and it must withhold the frame like
// every other cancelled exit: the retry takes the prompt slot and registers its own
// cancel precisely so the grace can unblock a retry KAS never answers, so the expiry
// trips it here. `recovery_failed` routes to a toast and TurnScoped suppresses it only
// for a reader already on that chat, so any other chat got a red toast for a stop the
// reader asked for — reading "Retry prompt failed: " and nothing after it, because a
// cancel supplies no prose.
func TestRetryEmptyTurnPrompt_WithholdsTheFrameOnAnUnackedCancel(t *testing.T) {
	deps := newSurfaceDeps()
	deps.callErr = context.Canceled
	roles := promptRolesOf(deps)

	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	cancel(ErrCancelGraceExpired)

	retryEmptyTurnPrompt(ctx, roles, "c1", &marotte.PromptCommand{Text: "hi", MessageID: "m1"}, map[string]any{})

	deps.mu.Lock()
	frames := deps.errors
	deps.mu.Unlock()

	if len(frames) != 0 {
		t.Errorf("broadcast %d error frames, want none: %+v", len(frames), frames)
	}
}

// A failed respawn names the cause in its frame: the empty turn already closed
// with its own outcome and the retry's turn never opened, so nothing else says
// what stopped the retry.
func TestRetryEmptyTurnPrompt_RespawnFailureNamesTheCause(t *testing.T) {
	deps := newSurfaceDeps()
	deps.spawnErr = errors.New("no such binary")

	retryEmptyTurnPrompt(t.Context(), promptRolesOf(deps), "c1", &marotte.PromptCommand{Text: "hi", MessageID: "m1"}, map[string]any{})

	frame := deps.onlyError(t)
	if !strings.Contains(frame.Message, "Session refresh failed") || !strings.Contains(frame.Message, "no such binary") {
		t.Errorf("frame message = %q, want it to name the failed refresh and the cause", frame.Message)
	}
	deps.mu.Lock()
	defer deps.mu.Unlock()
	if len(deps.abandoned) != 0 {
		t.Errorf("abandoned %+v, want no turn closed: the retry's turn never opened", deps.abandoned)
	}
}

// EVERY prompt exit runs the turn end rule on the turn CmdPrompt opened at
// admission, through AbandonInFlightTurn: the turn_close it writes grades the turn
// from the stop that ended it and carries the real reason, an `interrupted` for
// every exit that broke and a bare `cancelled` for the one that did not. An exit
// that closed nothing would leave a turn with a turn_open and no turn_close, which
// the store-open closer would later write off as a crash.
func TestPromptExits_CloseTheTurnThroughTheTurnEndRule(t *testing.T) {
	cases := []struct {
		name string
		// arrange puts the double on the path that produces this exit.
		arrange func(*surfaceDeps)
		// run drives the production path.
		run func(context.Context, *promptRoles, *marotte.PromptCommand)
		// want is a substring of the close's reason: the cause a reader can act on.
		// Empty means the close carries NO reason, which is what a cancel says.
		want string
		// stop is what the exit concludes.
		stop marotte.StopReason
		// frame is whether this exit also broadcasts an error, in which case the two
		// surfaces must read one sentence. Asserted in BOTH directions: false demands
		// zero error payloads.
		frame bool
	}{
		{
			name:    "the bridge could not be opened",
			arrange: func(d *surfaceDeps) { d.spawnErr = errors.New("no such binary") },
			run: func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand) {
				runPromptTurn(ctx, func() {}, roles, "c1", benchTurnID, p)
			},
			want:  "no such binary",
			stop:  marotte.StopReasonInterrupted,
			frame: true,
		},
		{
			name:    "the bridge slot was held despite the reservation",
			arrange: func(d *surfaceDeps) { d.slotHeld = true },
			run: func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand) {
				runPromptTurn(ctx, func() {}, roles, "c1", benchTurnID, p)
			},
			want:  "The prompt could not start",
			stop:  marotte.StopReasonInterrupted,
			frame: true,
		},
		{
			name:    "the turn was cancelled before it started",
			arrange: func(d *surfaceDeps) { d.startRefused = true },
			run: func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand) {
				runPromptTurn(ctx, func() {}, roles, "c1", benchTurnID, p)
			},
			want:  "cancelled before the agent answered",
			stop:  marotte.StopReasonInterrupted,
			frame: true,
		},
		{
			// The one exit with no frame at all: it logged a Warn and returned, so the
			// close is the whole of what a reader ever learns about it.
			name:    "the empty-turn retry's own turn never started",
			arrange: func(d *surfaceDeps) { d.startRefused = true },
			run: func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand) {
				retryEmptyTurnPrompt(ctx, roles, "c1", p, map[string]any{})
			},
			want: "cancelled before the agent answered",
			stop: marotte.StopReasonInterrupted,
		},
		{
			// The reader pressed Stop and KAS never acked it, so the grace budget killed
			// the prompt context during the spawn / MCP window. Nothing is broken, so
			// the close is a bare `cancelled` with no reason and no toast.
			name:    "an unacked cancel killed the context before the turn started",
			arrange: func(d *surfaceDeps) { d.startRefused = true },
			run: func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand) {
				ctx, cancel := context.WithCancelCause(ctx)
				defer cancel(nil)
				cancel(ErrCancelGraceExpired)
				runPromptTurn(ctx, func() {}, roles, "c1", benchTurnID, p)
			},
			stop: marotte.StopReasonCancelled,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := newSurfaceDeps()
			tc.arrange(deps)
			tc.run(t.Context(), promptRolesOf(deps), &marotte.PromptCommand{Text: "hi", MessageID: "m1"})

			deps.mu.Lock()
			abandoned := deps.abandoned
			frames := len(deps.errors)
			deps.mu.Unlock()

			if len(abandoned) != 1 {
				t.Fatalf("closed %d turns, want exactly 1: %+v", len(abandoned), abandoned)
			}
			got := abandoned[0]
			if got.turn != benchTurnID {
				t.Errorf("closed turn %q, want the one CmdPrompt opened, %q", got.turn, benchTurnID)
			}
			if got.stop != tc.stop {
				t.Errorf("stop = %q, want %q: the stop is what grades the turn", got.stop, tc.stop)
			}
			if tc.want == "" {
				if got.reason != "" {
					t.Errorf("reason = %q, want empty: a cancel has no account to give", got.reason)
				}
			} else if !strings.Contains(got.reason, tc.want) {
				t.Errorf("reason = %q, want it to contain %q", got.reason, tc.want)
			}
			if tc.frame {
				if frame := deps.onlyError(t); frame.Message != got.reason {
					t.Errorf("frame message %q != close reason %q: one failure, one rendering",
						frame.Message, got.reason)
				}
			} else if frames != 0 {
				t.Errorf("broadcast %d error frames, want none: this exit's surface is the "+
					"turn_close alone", frames)
			}
		})
	}
}
