package agent

import (
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/buffer"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/notice"
	"github.com/cplieger/marotte/internal/translate"
)

// TranslateRolesContractTest exercises every method of every translate role against the real wiring: an owner
// that stops satisfying a role fails to compile at the literal, a regressing method fails here.
func TranslateRolesContractTest(t *testing.T, newRoles func(t *testing.T) *translate.Roles) {
	t.Helper()

	t.Run("chat_store_is_wired", func(t *testing.T) {
		r := newRoles(t)
		if _, ok := r.Chats.Get(t.Context(), "no-such-chat"); ok {
			t.Error("Chats.Get on an empty store reported found")
		}
	})

	t.Run("WorkDir_non_empty", func(t *testing.T) {
		r := newRoles(t)
		if r.WorkDir == "" {
			t.Error("WorkDir is empty")
		}
	})

	t.Run("Broadcast_does_not_panic", func(t *testing.T) {
		r := newRoles(t)
		r.Bus.Broadcast(t.Context(), marotte.ServerEvent{Type: "test_event", ChatID: "chat-1"})
	})

	t.Run("ParentACPSession_empty_for_unknown_chat", func(t *testing.T) {
		r := newRoles(t)
		if s := r.Sessions.ParentACPSession("unknown-chat"); s != "" {
			t.Errorf("ParentACPSession(unknown) = %q, want empty", s)
		}
	})

	t.Run("IsHookStatusEnabled_returns_bool", func(t *testing.T) {
		r := newRoles(t)
		_ = r.HookStatus.IsHookStatusEnabled()
	})

	t.Run("TerminalOutput_unknown_terminal_is_not_ok", func(t *testing.T) {
		// An unknown terminal reports not-known, which adoption logs as a miss.
		r := newRoles(t)
		if _, _, ok := r.Terminals.Output("term-never-created"); ok {
			t.Error("Output(unknown) reported ok, want false")
		}
	})

	t.Run("MCPRecorder_does_not_panic", func(t *testing.T) {
		r := newRoles(t)
		if r.MCP == nil {
			t.Fatal("MCP role is nil")
		}
		r.MCP.RecordConnected(t.Context(), "test-server", marotte.MCPSource{}, nil, nil, nil, nil)
	})

	t.Run("PendingPermsAdd_does_not_panic", func(t *testing.T) {
		r := newRoles(t)
		r.PendingPerms.PendingPermsAdd(42, marotte.ServerEvent{Type: "permission_needed", ChatID: "c1"}, nil)
	})

	t.Run("Notify_does_not_panic", func(t *testing.T) {
		r := newRoles(t)
		n := notice.Question(r.Push.NoticeTarget(t.Context(), "", ""), "", "test body")
		r.Push.Notify(t.Context(), "", &n)
	})

	t.Run("turns_runs_and_lines_are_wired", func(t *testing.T) {
		r := newRoles(t)
		if r.Turns.TurnFoldTarget(t.Context(), "c1") == nil {
			t.Error("Turns.TurnFoldTarget returned nil")
		}
		if _, ok := r.Turns.OwnTurn("no-such-chat"); ok {
			t.Error("Turns.OwnTurn opened a turn")
		}
		if r.Runs == nil || r.Bracket == nil {
			t.Errorf("Runs = %v, Bracket = %v; want both wired", r.Runs, r.Bracket)
		}
		r.Lines.RecordFromDiffs("c1", nil, 0, "")
	})

	t.Run("SetGovernance_does_not_panic", func(t *testing.T) {
		r := newRoles(t)
		r.Governance.SetGovernance(t.Context(), marotte.GovernanceStatePayload{})
	})

	t.Run("IsScheduledRun_false_for_an_unlaunched_run", func(t *testing.T) {
		// A manual run reported as scheduled would toast every hand launch.
		r := newRoles(t)
		if r.RunOrigin.IsScheduled("wf-never-launched") {
			t.Error("IsScheduled(unlaunched) = true, want false")
		}
	})

	t.Run("RunMadeProgress_does_not_panic", func(t *testing.T) {
		r := newRoles(t)
		r.RunBounds.RunMadeProgress("wf-never-launched")
	})
}

func TestHub_TranslateRolesContract(t *testing.T) {
	TranslateRolesContractTest(t, func(t *testing.T) *translate.Roles {
		t.Helper()
		h, cs, _ := newTestHub()
		// A wire turn opens against the chat's record.
		cs.seed(t, "c1", nil)
		// The production wiring itself, not a copy.
		return h.translateRoles()
	})
}

// TestRequireWired_RefusesBothNils pins the guard, including the typed nil (a nil *T in an interface) that a
// field IsNil() misses. It runs in production at the call site: after New returns every field looks populated.
func TestRequireWired_RefusesBothNils(t *testing.T) {
	full := func() *translate.Roles {
		h, _, _ := newTestHub()
		return h.translateRoles()
	}

	t.Run("a fully wired set is returned unchanged", func(t *testing.T) {
		r := full()
		if got := requireWired(r); got != r {
			t.Error("requireWired did not return its argument")
		}
	})

	t.Run("an outright nil role panics naming the field", func(t *testing.T) {
		r := full()
		r.Bus = nil
		defer func() {
			msg, _ := recover().(string)
			if !strings.Contains(msg, "Bus") || !strings.Contains(msg, "is nil") {
				t.Errorf("panic = %q, want it to name Bus as nil", msg)
			}
		}()
		requireWired(r)
		t.Error("requireWired accepted a nil role")
	})

	t.Run("a typed nil panics naming the concrete type", func(t *testing.T) {
		r := full()
		r.Lines = (*buffer.LineTracker)(nil)
		defer func() {
			msg, _ := recover().(string)
			if !strings.Contains(msg, "Lines") || !strings.Contains(msg, "*buffer.LineTracker") {
				t.Errorf("panic = %q, want it to name Lines and the nil concrete type", msg)
			}
		}()
		requireWired(r)
		t.Error("requireWired accepted an interface holding a nil pointer")
	})
}
