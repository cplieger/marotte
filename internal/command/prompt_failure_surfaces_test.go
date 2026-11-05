package command

// TurnScoped is a property of the EMISSION, not the code: one code is emitted both with and without
// a turn card holding the reason.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
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
	// spawnErr, when set, is what OpenBridge answers: the reload-failure path.
	spawnErr error
	// slotHeld makes TryAcquireForPrompt refuse, which is the held-bridge-slot path.
	slotHeld bool
	// closedReason, when set, is the reason AwaitTurn reports the turn closed with:
	// the engine's turn_end winning the close.
	closedReason string
	// closedEngineErrorType, when set, is the class of the display_error AwaitTurn reports latched on the turn.
	closedEngineErrorType string
	// startRefused makes StartTurn answer false, the cancelled-during-open path.
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
	kind   marotte.FailureKind
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

func (d *surfaceDeps) AbandonInFlightTurn(_ context.Context, _ marotte.ChatID, turn string, stop marotte.StopReason, reason string, kind marotte.FailureKind, _ uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.abandoned = append(d.abandoned, abandonedTurn{turn: turn, stop: stop, reason: reason, kind: kind})
}

func (d *surfaceDeps) AwaitTurn(ctx context.Context, chatID marotte.ChatID, turnID string) (marotte.TurnResult, error) {
	if d.closedReason != "" || d.closedEngineErrorType != "" {
		return marotte.TurnResult{Turn: turnID, Reason: d.closedReason, EngineErrorType: d.closedEngineErrorType}, nil
	}
	if d.startRefused {
		// A live turn StartTurn refuses was taken by another closer, which the registry retains.
		return marotte.TurnResult{Turn: turnID, Stop: marotte.StopReasonInterrupted}, nil
	}
	return d.benchDeps.AwaitTurn(ctx, chatID, turnID)
}

func (d *surfaceDeps) OpenBridge(context.Context, marotte.ChatID, string) (Bridge, error) {
	if d.spawnErr != nil {
		return nil, d.spawnErr
	}
	return &surfaceBridge{deps: d}, nil
}

func (*surfaceDeps) EndPromptCall(TurnRef) {}

func (d *surfaceDeps) StartTurn(context.Context, TurnRef, context.CancelCauseFunc) bool {
	return !d.startRefused
}

func (*surfaceDeps) reservePromptOld(context.Context, marotte.ChatID, time.Duration) AdmissionOutcome {
	return AdmissionAcquired
}

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

type surfaceBridge struct{ deps *surfaceDeps }

func (b *surfaceBridge) Call(context.Context, string, any) (*marotte.RPCResponse, error) {
	return &marotte.RPCResponse{}, b.deps.callErr
}

func (b *surfaceBridge) PromptAt(ctx context.Context, params any, commit func() error) (*marotte.RPCResponse, uint64, error) {
	return promptViaCallAt(ctx, b, params, commit)
}

func (b *surfaceBridge) CallAt(context.Context, string, any) (*marotte.RPCResponse, uint64, error) {
	return &marotte.RPCResponse{}, 0, b.deps.callErr
}

func (*surfaceBridge) Notify(context.Context, string, any) error        { return nil }
func (*surfaceBridge) Respond(context.Context, int64, any, error) error { return nil }
func (*surfaceBridge) SessionID() marotte.SessionID                     { return "s1" }
func (b *surfaceBridge) TryAcquireForPrompt() bool                      { return !b.deps.slotHeld }
func (*surfaceBridge) ReleaseAfterPrompt()                              {}

// A turn WAS finalized, so the reason is on its card and the toast may stand down.
func TestReportPromptFailure_MarksTheFrameTurnScoped(t *testing.T) {
	deps := newSurfaceDeps()
	reportPromptFailure(t.Context(), promptRolesOf(deps), "c1", "t-7",
		errors.New("connection reset"), 0, time.Second, false)

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

// When the engine's own turn_end closed the turn, the toast carries the sentence
// the card shows rather than marotte's reading of the prompt error.
func TestReportPromptFailure_BroadcastsTheClosedReason(t *testing.T) {
	deps := newSurfaceDeps()
	deps.closedReason = "Your connection was interrupted. Please try again in a moment."
	reportPromptFailure(t.Context(), promptRolesOf(deps), "c1", "t-7",
		errors.New("ACP error -32603: Internal error"), 9, time.Second, false)

	if got := deps.onlyError(t).Message; got != deps.closedReason {
		t.Errorf("error frame Message = %q, want the closed turn's reason %q", got, deps.closedReason)
	}
}

// With a model pinned on the session, KAS asks for the token inside the execution: the
// turn's display_error names the auth class and the prompt reply is a bare -32000 with
// no data. The replies below are the probe's, on kiro-cli 2.37.0 and 2.32.0.
func TestReportPromptFailure_AnEngineNamedAuthClassRoutesToSignIn(t *testing.T) {
	const refusal = "Auth refresh callback failed: Not signed in to Kiro in marotte. Sign in from the account menu.: unauthenticated"
	reply237 := &marotte.RPCError{Code: -32000, Message: "Authentication token is invalid: " + refusal}
	reply232 := &marotte.RPCError{Code: -32000, Message: refusal}
	cases := map[string]struct {
		callErr     error
		engineClass string
		wantCode    marotte.ErrorCode
		wantLatched bool
	}{
		"kiro-cli 2.37.0 credential absent": {
			callErr: reply237, engineClass: "CredentialAbsentError",
			wantCode: marotte.ErrCodeAuthTokenUnavailable, wantLatched: true,
		},
		"kiro-cli 2.32.0 token expired": {
			callErr: reply232, engineClass: "TokenExpiredError",
			wantCode: marotte.ErrCodeAuthTokenUnavailable, wantLatched: true,
		},
		"a throttle the turn named": {
			callErr: reply237, engineClass: "ModelThrottleError",
			wantCode: marotte.ErrCodePromptFailed, wantLatched: false,
		},
		"no class on the turn": {
			callErr:  reply237,
			wantCode: marotte.ErrCodePromptFailed, wantLatched: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			deps := newSurfaceDeps()
			deps.closedEngineErrorType = tc.engineClass
			readiness := new(AuthReadiness)
			roles := promptRolesOf(deps)
			roles.auth = readiness

			reportPromptFailure(t.Context(), roles, "c1", "t-7", tc.callErr, 3, time.Second, false)

			if got := deps.onlyError(t).Code; got != tc.wantCode {
				t.Errorf("error code = %q, want %q", got, tc.wantCode)
			}
			if got := readiness.Unavailable(); got != tc.wantLatched {
				t.Errorf("AuthReadiness.Unavailable() = %v, want %v", got, tc.wantLatched)
			}
		})
	}
}

// providerReach answers ProviderUnreachable with its reason, down when it is non-empty.
type providerReach string

func (p providerReach) ProviderUnreachable() (string, bool) { return string(p), p != "" }

// The unreachable refusal internal/signin/kiro words for KAS, which KAS still grades unauthenticated.
const vaultUnreachable = "marotte could not reach the identity provider to renew the Kiro sign-in; it will try again."

// unreachableCase is one failure graded while the vault does or does not hold the provider unreachable.
type unreachableCase struct {
	callErr     error
	engineClass string
	reach       providerReach
	wantCode    marotte.ErrorCode
	// wantVault: the frame carries the vault's reason off the card, else the card's own reason.
	wantVault   bool
	wantLatched bool
}

// unreachableCases are both auth shapes KAS reports, on the execution path and the catalog path, with the
// controls that keep the vault's reason to them. failed is the path's non-auth code.
func unreachableCases(failed marotte.ErrorCode) map[string]unreachableCase {
	execReply := &marotte.RPCError{Code: -32000, Message: "Authentication token is invalid: Auth refresh callback failed: " + vaultUnreachable + ": transport"}
	mapped := &marotte.RPCError{Code: -32603, Message: "Internal error", Data: []byte(`{"errorType":"ModelRegistryUnauthenticatedError"}`)}
	return map[string]unreachableCase{
		"the turn names a credential class while the provider is down": {
			callErr: execReply, engineClass: "CredentialAbsentError", reach: vaultUnreachable,
			wantCode: failed, wantVault: true,
		},
		"a mapped auth envelope while the provider is down": {
			callErr: mapped, reach: vaultUnreachable,
			wantCode: failed, wantVault: true,
		},
		"the turn names a credential class while the provider answers": {
			callErr: execReply, engineClass: "CredentialAbsentError",
			wantCode: marotte.ErrCodeAuthTokenUnavailable, wantLatched: true,
		},
		"a throttle while the provider is down": {
			callErr: execReply, engineClass: "ModelThrottleError", reach: vaultUnreachable,
			wantCode: failed,
		},
	}
}

func checkUnreachableFrame(t *testing.T, got marotte.ErrorPayload, tc unreachableCase, cardReason string, latched bool) {
	t.Helper()
	if got.Code != tc.wantCode {
		t.Errorf("error code = %q, want %q", got.Code, tc.wantCode)
	}
	wantMsg := cardReason
	if tc.wantVault {
		wantMsg = vaultUnreachable
	}
	if !strings.HasSuffix(got.Message, wantMsg) {
		t.Errorf("Message = %q, want it to end with %q", got.Message, wantMsg)
	}
	if got.TurnScoped == tc.wantVault {
		t.Errorf("TurnScoped = %v, want %v: the card carries KAS's sentence, not the vault's reason", got.TurnScoped, !tc.wantVault)
	}
	if latched != tc.wantLatched {
		t.Errorf("AuthReadiness.Unavailable() = %v, want %v", latched, tc.wantLatched)
	}
}

// KAS grades a credential ask the vault refused as unreachable as unauthenticated, but no sign-in fixes an outage
// of the identity provider, so the vault's reason replaces the Sign in route and health stays signed in.
func TestReportPromptFailure_AnUnreachableProviderIsNotASignInFailure(t *testing.T) {
	const card = "Authentication failed. Please sign in again."
	for name, tc := range unreachableCases(marotte.ErrCodePromptFailed) {
		t.Run(name, func(t *testing.T) {
			deps := newSurfaceDeps()
			deps.closedReason = card
			deps.closedEngineErrorType = tc.engineClass
			readiness := new(AuthReadiness)
			roles := promptRolesOf(deps)
			roles.auth, roles.signIn = readiness, tc.reach

			reportPromptFailure(t.Context(), roles, "c1", "t-7", tc.callErr, 3, time.Second, false)

			checkUnreachableFrame(t, deps.onlyError(t), tc, card, readiness.Unavailable())
		})
	}
}

// The empty-turn retry grades an unreachable provider as its first attempt does.
func TestRetryEmptyTurnPrompt_AnUnreachableProviderIsNotASignInFailure(t *testing.T) {
	const card = "Authentication failed. Please sign in again."
	for name, tc := range unreachableCases(marotte.ErrCodeRecoveryFailed) {
		t.Run(name, func(t *testing.T) {
			deps := newSurfaceDeps()
			deps.callErr = tc.callErr
			deps.closedReason = card
			deps.closedEngineErrorType = tc.engineClass
			readiness := new(AuthReadiness)
			roles := promptRolesOf(deps)
			roles.auth, roles.signIn = readiness, tc.reach

			retryEmptyTurnPrompt(t.Context(), roles, testReservation("c1", true), "t-empty", &marotte.PromptCommand{Text: "hi", MessageID: "m1"}, map[string]any{})

			got := deps.onlyError(t)
			if !strings.HasPrefix(got.Message, "Retry prompt failed: ") {
				t.Errorf("Message = %q, want the retry's prefix", got.Message)
			}
			checkUnreachableFrame(t, got, tc, card, readiness.Unavailable())
		})
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
			name:    "the chat's session could not be opened",
			want:    marotte.ErrCodeBridgeStartFailed,
			arrange: func(d *surfaceDeps) { d.spawnErr = errors.New("no such binary") },
			run: func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand) {
				runPromptTurn(ctx, func() {}, roles, testReservation("c1", true), benchTurnID, p)
			},
			turnScoped: true,
		},
		{
			name:    "the bridge slot was held despite the reservation",
			want:    marotte.ErrCodePromptFailed,
			arrange: func(d *surfaceDeps) { d.slotHeld = true },
			run: func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand) {
				runPromptTurn(ctx, func() {}, roles, testReservation("c1", true), benchTurnID, p)
			},
			turnScoped: true,
		},
		{
			name:    "the turn was refused before it started",
			want:    marotte.ErrCodePromptFailed,
			arrange: func(d *surfaceDeps) { d.startRefused = true },
			run: func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand) {
				runPromptTurn(ctx, func() {}, roles, testReservation("c1", true), benchTurnID, p)
			},
			turnScoped: true,
		},
		{
			name:    "empty-turn recovery could not reload the session",
			want:    marotte.ErrCodeRecoveryFailed,
			arrange: func(d *surfaceDeps) { d.spawnErr = errors.New("no such binary") },
			run: func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand) {
				retryEmptyTurnPrompt(ctx, roles, testReservation("c1", true), "t-empty", p, map[string]any{})
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

	retryEmptyTurnPrompt(t.Context(), roles, testReservation("c1", true), "t-empty", &marotte.PromptCommand{Text: "hi", MessageID: "m1"}, map[string]any{})

	got := deps.onlyError(t)
	if got.Code != marotte.ErrCodeRecoveryFailed {
		t.Errorf("code = %q, want %q", got.Code, marotte.ErrCodeRecoveryFailed)
	}
	if !got.TurnScoped {
		t.Error("TurnScoped = false, want true: the retry was a turn of its own and the " +
			"abandon stamped this reason on it")
	}
}

// A retry refused for sign-in has the first attempt's remedy: the Sign in route and
// health's sign-in required. The reply is the probe's kiro-cli 2.37.0 execution-path one.
func TestRetryEmptyTurnPrompt_AnAuthFailureRoutesToSignIn(t *testing.T) {
	reply := &marotte.RPCError{Code: -32000, Message: "Authentication token is invalid: Auth refresh callback failed: Not signed in to Kiro in marotte. Sign in from the account menu.: unauthenticated"}
	cases := map[string]struct {
		callErr     error
		engineClass string
		wantCode    marotte.ErrorCode
		wantLatched bool
	}{
		"the turn names a credential class": {
			callErr: reply, engineClass: "CredentialAbsentError",
			wantCode: marotte.ErrCodeAuthTokenUnavailable, wantLatched: true,
		},
		"the reply is a mapped auth envelope": {
			callErr:  &marotte.RPCError{Code: -32603, Message: "Internal error", Data: []byte(`{"errorType":"ModelRegistryUnauthenticatedError"}`)},
			wantCode: marotte.ErrCodeAuthTokenUnavailable, wantLatched: true,
		},
		"the turn names a throttle": {
			callErr: reply, engineClass: "ModelThrottleError",
			wantCode: marotte.ErrCodeRecoveryFailed, wantLatched: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			deps := newSurfaceDeps()
			deps.callErr = tc.callErr
			deps.closedEngineErrorType = tc.engineClass
			readiness := new(AuthReadiness)
			roles := promptRolesOf(deps)
			roles.auth = readiness

			retryEmptyTurnPrompt(t.Context(), roles, testReservation("c1", true), "t-empty", &marotte.PromptCommand{Text: "hi", MessageID: "m1"}, map[string]any{})

			got := deps.onlyError(t)
			if got.Code != tc.wantCode {
				t.Errorf("error code = %q, want %q", got.Code, tc.wantCode)
			}
			if !got.TurnScoped {
				t.Error("TurnScoped = false, want true: the retry's turn carries the reason")
			}
			if latched := readiness.Unavailable(); latched != tc.wantLatched {
				t.Errorf("AuthReadiness.Unavailable() = %v, want %v", latched, tc.wantLatched)
			}
		})
	}
}

// TestRetryEmptyTurnPrompt_WithholdsTheFrameOnACancel: the reader's own Stop reaches the retry's
// failure branch and must withhold the frame like every cancelled exit.
func TestRetryEmptyTurnPrompt_WithholdsTheFrameOnAnUnackedCancel(t *testing.T) {
	deps := newSurfaceDeps()
	deps.callErr = context.Canceled
	roles := promptRolesOf(deps)

	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	cancel(ErrCancelGraceExpired)

	retryEmptyTurnPrompt(ctx, roles, testReservation("c1", true), "t-empty", &marotte.PromptCommand{Text: "hi", MessageID: "m1"}, map[string]any{})

	deps.mu.Lock()
	frames := deps.errors
	deps.mu.Unlock()

	if len(frames) != 0 {
		t.Errorf("broadcast %d error frames, want none: %+v", len(frames), frames)
	}
}

// A failed reload names the cause in its frame: the empty turn already closed
// with its own outcome and the retry's turn never opened, so nothing else says
// what stopped the retry.
func TestRetryEmptyTurnPrompt_ReopenFailureNamesTheCause(t *testing.T) {
	deps := newSurfaceDeps()
	deps.spawnErr = errors.New("no such binary")

	retryEmptyTurnPrompt(t.Context(), promptRolesOf(deps), testReservation("c1", true), "t-empty", &marotte.PromptCommand{Text: "hi", MessageID: "m1"}, map[string]any{})

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

// TestPromptExits_CloseTheTurnThroughTheTurnEndRule: every prompt exit closes the turn cmdPrompt
// opened through AbandonInFlightTurn, graded from the stop that ended it.
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
		// frame is whether this exit also broadcasts an error; asserted both ways, false demanding
		// zero error payloads.
		frame bool
	}{
		{
			name:    "the bridge could not be opened",
			arrange: func(d *surfaceDeps) { d.spawnErr = errors.New("no such binary") },
			run: func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand) {
				runPromptTurn(ctx, func() {}, roles, testReservation("c1", true), benchTurnID, p)
			},
			want:  "no such binary",
			stop:  marotte.StopReasonInterrupted,
			frame: true,
		},
		{
			name:    "the bridge slot was held despite the reservation",
			arrange: func(d *surfaceDeps) { d.slotHeld = true },
			run: func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand) {
				runPromptTurn(ctx, func() {}, roles, testReservation("c1", true), benchTurnID, p)
			},
			want:  "The prompt could not start",
			stop:  marotte.StopReasonInterrupted,
			frame: true,
		},
		{
			name:    "the turn was cancelled before it started",
			arrange: func(d *surfaceDeps) { d.startRefused = true },
			run: func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand) {
				runPromptTurn(ctx, func() {}, roles, testReservation("c1", true), benchTurnID, p)
			},
			want:  "cancelled before the agent answered",
			stop:  marotte.StopReasonInterrupted,
			frame: true,
		},
		{
			name:    "the empty-turn retry's own turn never started",
			arrange: func(d *surfaceDeps) { d.startRefused = true },
			run: func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand) {
				retryEmptyTurnPrompt(ctx, roles, testReservation("c1", true), "t-empty", p, map[string]any{})
			},
			want: "cancelled before the agent answered",
			stop: marotte.StopReasonInterrupted,
		},
		{
			name:    "an unacked cancel killed the context before the turn started",
			arrange: func(d *surfaceDeps) { d.startRefused = true },
			run: func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand) {
				ctx, cancel := context.WithCancelCause(ctx)
				defer cancel(nil)
				cancel(ErrCancelGraceExpired)
				runPromptTurn(ctx, func() {}, roles, testReservation("c1", true), benchTurnID, p)
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

// logLines splits a captureLogs buffer into the lines whose text contains sub.
func logLines(buf *bytes.Buffer, sub string) []string {
	var out []string
	for line := range strings.Lines(buf.String()) {
		if strings.Contains(line, sub) {
			out = append(out, line)
		}
	}
	return out
}

// A prompt the engine's exit killed is a casualty of the one "engine exited" record, so neither
// failure site may log "prompt failed" for it, and the surfaces the user sees stay as they are.
func TestReportPromptFailure_EngineExitIsNotAPromptFailure(t *testing.T) {
	exited := &marotte.TransportError{Err: marotte.ErrBridgeExited, Retryable: true}
	cases := []struct {
		name     string
		run      func(context.Context, *promptRoles, *surfaceDeps)
		wantCode marotte.ErrorCode
	}{
		{
			name: "the prompt's own failure",
			run: func(ctx context.Context, roles *promptRoles, _ *surfaceDeps) {
				reportPromptFailure(ctx, roles, "c1", "t-7", exited, 0, time.Second, false)
			},
			wantCode: marotte.ErrCodePromptFailed,
		},
		{
			name: "the empty-turn retry's failure",
			run: func(ctx context.Context, roles *promptRoles, d *surfaceDeps) {
				d.callErr = exited
				retryEmptyTurnPrompt(ctx, roles, testReservation("c1", true), "t-empty", &marotte.PromptCommand{Text: "hi", MessageID: "m1"}, map[string]any{})
			},
			wantCode: marotte.ErrCodeRecoveryFailed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			deps := newSurfaceDeps()
			tc.run(t.Context(), promptRolesOf(deps), deps)

			if got := logLines(logs, "prompt failed"); len(got) != 0 {
				t.Errorf("logged %q, want no line containing \"prompt failed\" for an engine-exit casualty", got)
			}
			warns := logLines(logs, "msg="+strconv.Quote(logMsgPromptEngineEnded))
			if len(warns) != 1 {
				t.Fatalf("logged %d %q lines, want exactly 1:\n%s", len(warns), logMsgPromptEngineEnded, logs)
			}
			if !strings.Contains(warns[0], "level=WARN") || !strings.Contains(warns[0], "class=pipe_death") {
				t.Errorf("engine-exit line = %q, want level=WARN and class=pipe_death", warns[0])
			}
			frame := deps.onlyError(t)
			if frame.Code != tc.wantCode || !frame.TurnScoped {
				t.Errorf("error frame = %+v, want code %q and TurnScoped: the user's surface is unchanged", frame, tc.wantCode)
			}
		})
	}
}

// Every failure that is not an engine-exit casualty is still the "prompt failed" Error line the
// alert counts: only ErrBridgeExited is exempt, not every retryable transport error.
func TestReportPromptFailure_OtherFailuresStillCountAsPromptFailed(t *testing.T) {
	cases := map[string]error{
		"a reset connection":        errors.New("connection reset"),
		"a refused credential":      rpcErr(t, marotte.RPCCodeInternal, "Internal error", map[string]string{"details": "not logged in"}),
		"a retryable write failure": &marotte.TransportError{Err: errors.New("write to ACP: broken pipe"), Retryable: true},
	}
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			logs := captureLogs(t)
			reportPromptFailure(t.Context(), promptRolesOf(newSurfaceDeps()), "c1", "t-7", err, 0, time.Second, false)

			got := logLines(logs, `msg="prompt failed"`)
			if len(got) != 1 || !strings.Contains(got[0], "level=ERROR") {
				t.Errorf("prompt-failed lines = %q, want exactly one at level=ERROR:\n%s", got, logs)
			}
			if warns := logLines(logs, logMsgPromptEngineEnded); len(warns) != 0 {
				t.Errorf("logged %q, want no engine-exit line for a failure the engine's exit did not cause", warns)
			}
		})
	}
}

func (r *surfaceDeps) ReserveTurnForPrompt(ctx context.Context, chatID marotte.ChatID, wait time.Duration, _ uint64, _ string) (AdmissionOutcome, Reservation) {
	out := r.reservePromptOld(ctx, chatID, wait)
	return out, testReservation(chatID, out == AdmissionAcquired)
}

func (r *surfaceDeps) OpenBridgeAt(ctx context.Context, chatID marotte.ChatID, model string, _ uint64) (Bridge, error) {
	return r.OpenBridge(ctx, chatID, model)
}

// A prompt the chat's close ended (its door refused, its lease cut, or its turn already forgotten) is the close's
// casualty, not a failure: it sends no bridge_start_failed, recovery_failed or prompt_failed frame, logs no failure,
// and a turn it closes is cancelled with no reason.
func TestPromptFailure_AClosedChatsPromptSendsNoFailureFrame(t *testing.T) {
	ends := map[string]error{
		"the close during the call": fmt.Errorf("prompt: %w", marotte.ErrChatClosedDuringCall),
		"a forgotten turn":          marotte.ErrNoSuchTurn,
	}
	paths := map[string]struct {
		arrange func(d *surfaceDeps, end error)
		run     func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand)
	}{
		"the prompt's session open": {
			arrange: func(d *surfaceDeps, end error) { d.spawnErr = end },
			run: func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand) {
				runPromptTurn(ctx, func() {}, roles, testReservation("c1", true), benchTurnID, p)
			},
		},
		"the prompt's call": {
			arrange: func(d *surfaceDeps, end error) { d.callErr = end },
			run: func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand) {
				runPromptTurn(ctx, func() {}, roles, testReservation("c1", true), benchTurnID, p)
			},
		},
		"the empty-turn retry's reload": {
			arrange: func(d *surfaceDeps, end error) { d.spawnErr = end },
			run: func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand) {
				retryEmptyTurnPrompt(ctx, roles, testReservation("c1", true), "t-empty", p, map[string]any{})
			},
		},
		"the empty-turn retry's call": {
			arrange: func(d *surfaceDeps, end error) { d.callErr = end },
			run: func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand) {
				retryEmptyTurnPrompt(ctx, roles, testReservation("c1", true), "t-empty", p, map[string]any{})
			},
		},
	}
	for endName, end := range ends {
		for pathName, path := range paths {
			t.Run(endName+" at "+pathName, func(t *testing.T) {
				logs := captureLogs(t)
				deps := newSurfaceDeps()
				path.arrange(deps, end)
				path.run(t.Context(), promptRolesOf(deps), &marotte.PromptCommand{Text: "hi", MessageID: "m1"})

				deps.mu.Lock()
				defer deps.mu.Unlock()
				if len(deps.errors) != 0 {
					t.Errorf("broadcast %+v, want no error frame for a prompt the chat's close ended", deps.errors)
				}
				for _, failed := range [][]string{logLines(logs, "prompt failed"), logLines(logs, "prompt failure"), logLines(logs, "level=ERROR")} {
					if len(failed) != 0 {
						t.Errorf("logged %q, want no failure line for the close's casualty", failed)
					}
				}
				for _, a := range deps.abandoned {
					if a.stop != marotte.StopReasonCancelled || a.reason != "" {
						t.Errorf("turn %s closed %q with reason %q, want cancelled with no reason", a.turn, a.stop, a.reason)
					}
				}
			})
		}
	}
}
