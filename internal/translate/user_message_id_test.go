package translate

// The `user_message_id_assigned` frame: the id KAS's own log holds a prompt under is
// what makes that turn addressable, and there is no correlation key on the frame — the
// prompt it belongs to is the chat's one prompt-class turn awaiting or holding its
// bracket. So what is pinned here is WHICH turn takes the turn_bind, what the entry
// carries, that a frame with no prompt turn appends nothing, and that a frame
// belonging to something other than this chat binds nothing.

import (
	"context"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// commitCountingStore records how many Mutate calls actually committed, which is the
// only way to tell a bind that stayed an entry from one that reached the header.
type commitCountingStore struct {
	ChatRecords
	commits int
}

func (s *commitCountingStore) Mutate(ctx context.Context, id marotte.ChatID, fn func(*marotte.Chat, bool) bool) (string, error) {
	return s.ChatRecords.Mutate(ctx, id, func(c *marotte.Chat, exists bool) bool {
		changed := fn(c, exists)
		if changed {
			s.commits++
		}
		return changed
	})
}

// userMessageIDFrame is the update-level object KAS sends. `_meta` sits at its top,
// which is one level in from `params` — the standing nesting trap on this wire.
func userMessageIDFrame(t *testing.T, kasID string) []byte {
	t.Helper()
	return mustJSON(t, map[string]any{
		"sessionUpdate": "session_info_update",
		"_meta": map[string]any{
			"kiro": map[string]any{
				"kind":          "user_message_id_assigned",
				"userMessageId": kasID,
			},
		},
	})
}

// stagePromptTurn opens c1's turn and registers it as the prompt-class turn
// PromptTurn answers, the one a bind lands in.
func stagePromptTurn(deps *baseDeps) {
	deps.prompts["c1"] = deps.turns.chatTurn("c1")
}

// bindsOf decodes every turn_bind entry in entries, in seal order.
func bindsOf(t *testing.T, entries []marotte.Entry) []marotte.EntryTurnBind {
	t.Helper()
	var out []marotte.EntryTurnBind
	for i := range entries {
		if entries[i].Kind != marotte.EntryKindTurnBind {
			continue
		}
		out = append(out, decodePayload[marotte.EntryTurnBind](t, &entries[i]))
	}
	return out
}

// The bind lands in the prompt turn and names the session the prompt went out on,
// which is what scopes the replay merge.
func TestHandleSessionInfoUpdate_BindsThePromptTurnToTheSession(t *testing.T) {
	deps, events, store := depsWithStore(t, "c1")
	if _, err := store.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.ACPSessionID = "sess-1"
		return true
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	stagePromptTurn(deps)

	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		userMessageIDFrame(t, "38572497-a17f-4172-bdfb-7eb82919a378"), FrameAttribution{})

	binds := bindsOf(t, deps.chatEntries("c1"))
	if len(binds) != 1 {
		t.Fatalf("turn_bind entries = %d, want 1", len(binds))
	}
	want := marotte.EntryTurnBind{KASMessageID: "38572497-a17f-4172-bdfb-7eb82919a378", SessionID: "sess-1"}
	if binds[0] != want {
		t.Errorf("turn_bind = %+v, want %+v", binds[0], want)
	}
	if !hasEntryAppended(events, marotte.EntryKindTurnBind) {
		t.Error("no entry_appended{turn_bind} frame: the bind is born sealed and must be announced")
	}
}

// The empty-turn retry re-sends the SAME prompt, so KAS mints a SECOND record for it and
// the newer id is the one revertMultiple will accept: both binds land, in order, and
// the merge reads the last.
func TestHandleSessionInfoUpdate_ADifferentIDAppendsASecondBind(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	stagePromptTurn(deps)
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", userMessageIDFrame(t, "kas-1"), FrameAttribution{})
	tr.HandleSessionInfoUpdate(t.Context(), "c1", userMessageIDFrame(t, "kas-2"), FrameAttribution{})

	binds := bindsOf(t, deps.chatEntries("c1"))
	if len(binds) != 2 || binds[0].KASMessageID != "kas-1" || binds[1].KASMessageID != "kas-2" {
		t.Errorf("turn_bind entries = %+v, want kas-1 then kas-2: the newest record is the addressable one", binds)
	}
}

// A bind is an entry in the turn's log and never a header write, so the chat file is
// not rewritten for it — a reconnect can redeliver the frame, and a repeat costs an
// append rather than a rewrite.
func TestHandleSessionInfoUpdate_ABindWritesNoHeader(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	stagePromptTurn(deps)
	counting := &commitCountingStore{ChatRecords: store}
	deps.store = counting
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", userMessageIDFrame(t, "kas-1"), FrameAttribution{})
	tr.HandleSessionInfoUpdate(t.Context(), "c1", userMessageIDFrame(t, "kas-1"), FrameAttribution{})

	if counting.commits != 0 {
		t.Errorf("header commits = %d, want 0: a bind is an entry, not a header write", counting.commits)
	}
}

// Nothing to bind is a normal state, not an error: the chat KAS's own auto-wake prompts
// reaches here with no prompt-class turn, and no turn is opened for the frame.
func TestHandleSessionInfoUpdate_NoPromptTurnAppendsNothing(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")

	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		userMessageIDFrame(t, "kas-1"), FrameAttribution{})

	if len(*events) != 0 {
		t.Errorf("events = %v, want none", eventTypes(*events))
	}
	if deps.turns.chats["c1"] != nil {
		t.Error("a turn was opened for the bind; want none")
	}
}

// The bind is positional, so an id belonging to something OTHER than this chat's own
// prompt would land on the reader's prompt turn and make rewind revert the wrong thing.
// A workflow step's answer prompts on the step's session, and a subagent has its own.
func TestHandleSessionInfoUpdate_AForeignFrameBindsNothing(t *testing.T) {
	for name, attr := range map[string]FrameAttribution{
		"a workflow step's own session": {Step: true},
		"a subagent's session":          {SubSessionID: "sess_sub"},
	} {
		t.Run(name, func(t *testing.T) {
			deps, events, _ := depsWithStore(t, "c1")
			stagePromptTurn(deps)

			New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
				userMessageIDFrame(t, "kas-1"), attr)

			if binds := bindsOf(t, deps.chatEntries("c1")); len(binds) != 0 {
				t.Errorf("turn_bind entries = %+v, want none: this frame is not the chat's", binds)
			}
			if hasEntryAppended(events, marotte.EntryKindTurnBind) {
				t.Error("entry_appended{turn_bind} was broadcast for a foreign frame")
			}
		})
	}
}
