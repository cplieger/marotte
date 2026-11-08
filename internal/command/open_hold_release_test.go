package command

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// heldOpens hands out a counted release with every open, so a test reads how many open holds no path released.
type heldOpens struct {
	bridgeAccess
	opened atomic.Int32
	held   atomic.Int32
}

func (h *heldOpens) OpenBridge(ctx context.Context, chatID marotte.ChatID, model string) (Bridge, func(), error) {
	b, release, err := h.bridgeAccess.OpenBridge(ctx, chatID, model)
	return b, h.track(release), err
}

func (h *heldOpens) OpenBridgeAt(ctx context.Context, chatID marotte.ChatID, model string, res Reservation) (Bridge, func(), error) {
	b, release, err := h.bridgeAccess.OpenBridgeAt(ctx, chatID, model, res)
	return b, h.track(release), err
}

func (h *heldOpens) track(release func()) func() {
	h.opened.Add(1)
	h.held.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() {
			h.held.Add(-1)
			release()
		})
	}
}

// assertReleased fails unless exactly one open ran and its hold ended.
func (h *heldOpens) assertReleased(t *testing.T) {
	t.Helper()
	if n := h.opened.Load(); n != 1 {
		t.Fatalf("opens = %d, want 1: the path under test never opened the chat's session", n)
	}
	if n := h.held.Load(); n != 0 {
		t.Errorf("open holds left = %d, want 0: an exit kept the chat's session from closing", n)
	}
}

// refusedRetryOpen refuses the empty-turn retry's turn, as a stop recorded during the reload does.
type refusedRetryOpen struct{ turnOutcomeAccess }

func (refusedRetryOpen) OpenTurn(context.Context, marotte.ChatID, TurnOpen) (string, error) {
	return "", ErrTurnStopped
}

func openedOn(session marotte.SessionID) *heldOpens {
	return &heldOpens{bridgeAccess: &bridgeDeps{
		storeDeps: &storeDeps{benchDeps: newBenchDeps()},
		opened:    &recordingBridge{sessionID: session},
	}}
}

func TestOpenHold_EveryRefusedOpenReleasesItsHold(t *testing.T) {
	t.Run("a rewind whose session was not resumed", func(t *testing.T) {
		opens := openedOn("sess-new")
		_, release, err := resumeForRevert(t.Context(), opens, "c1", "sess-old")
		if !errors.Is(err, errRewindSessionNotResumed) {
			t.Fatalf("resumeForRevert = %v, want %v", err, errRewindSessionNotResumed)
		}
		opens.assertReleased(t)
		release()
	})
	t.Run("a fork parent opened with no session", func(t *testing.T) {
		opens := openedOn("")
		b, release := parentBridge(t.Context(), opens, "p1")
		if b != nil {
			t.Fatal("parentBridge answered a bridge with no session")
		}
		opens.assertReleased(t)
		release()
	})
	t.Run("a branch whose parent session was not resumed", func(t *testing.T) {
		opens := openedOn("sess-new")
		_, err := forkSession(t.Context(), opens, Workspace{Dir: t.TempDir()},
			marotte.ForkChatCommand{ParentChatID: "p1"}, &marotte.Chat{ACPSessionID: "sess-old"}, "m-1")
		if !errors.Is(err, errBranchSessionNotResumed) {
			t.Fatalf("forkSession = %v, want %v", err, errBranchSessionNotResumed)
		}
		opens.assertReleased(t)
	})
	for _, tc := range []struct {
		arrange func(d *surfaceDeps, roles *promptRoles)
		run     func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand)
		name    string
	}{
		{
			name:    "a prompt whose session could not be opened",
			arrange: func(d *surfaceDeps, _ *promptRoles) { d.spawnErr = errors.New("no such binary") },
			run: func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand) {
				runPromptTurn(ctx, func() {}, roles, testReservation("c1", true), benchTurnID, p)
			},
		},
		{
			name:    "a prompt whose turn was refused before it started",
			arrange: func(d *surfaceDeps, _ *promptRoles) { d.startRefused = true },
			run: func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand) {
				runPromptTurn(ctx, func() {}, roles, testReservation("c1", true), benchTurnID, p)
			},
		},
		{
			name:    "a prompt whose call failed",
			arrange: func(d *surfaceDeps, _ *promptRoles) { d.callErr = errors.New("connection reset") },
			run: func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand) {
				runPromptTurn(ctx, func() {}, roles, testReservation("c1", true), benchTurnID, p)
			},
		},
		{
			name: "an empty-turn retry whose turn was refused",
			arrange: func(_ *surfaceDeps, roles *promptRoles) {
				roles.turnOutcome = refusedRetryOpen{roles.turnOutcome}
			},
			run: func(ctx context.Context, roles *promptRoles, p *marotte.PromptCommand) {
				retryEmptyTurnPrompt(ctx, roles, testReservation("c1", true), "t-empty", p, map[string]any{})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := newSurfaceDeps()
			roles := promptRolesOf(deps)
			opens := &heldOpens{bridgeAccess: deps}
			roles.bridges = opens
			tc.arrange(deps, roles)

			tc.run(t.Context(), roles, &marotte.PromptCommand{Text: "hi", MessageID: "m1"})

			opens.assertReleased(t)
		})
	}
}
