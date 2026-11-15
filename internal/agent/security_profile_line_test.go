package agent

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/policyfile"
)

func profileSwitchesOf(t *testing.T, entries []marotte.Entry) []marotte.EntryProfileSwitched {
	t.Helper()
	var out []marotte.EntryProfileSwitched
	for i := range entries {
		if entries[i].Kind != marotte.EntryKindProfileSwitched {
			continue
		}
		var p marotte.EntryProfileSwitched
		if err := json.Unmarshal(entries[i].Payload, &p); err != nil {
			t.Fatalf("decode profile_switched %q: %v", entries[i].ID, err)
		}
		out = append(out, p)
	}
	return out
}

// pickProfile stands in for set_security_profile: the header takes the pick, then the runtime hears of the move.
func pickProfile(t *testing.T, h *Runtime, chatID marotte.ChatID, from, to string) {
	t.Helper()
	setChatProfile(t, h, chatID, to)
	h.ChatProfileChanged(chatID, from, to)
}

func TestChatProfileChanged_AnIdlePickRecordsOneLineAtOnce(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	finishedTurn(t, h, "c1")

	pickProfile(t, h, "c1", policyfile.ProfileTrusted, policyfile.ProfileReadOnly)

	entries := logOf(t, cs, "c1")
	got := profileSwitchesOf(t, entries)
	want := []marotte.EntryProfileSwitched{{From: policyfile.ProfileTrusted, To: policyfile.ProfileReadOnly}}
	if !slices.Equal(got, want) {
		t.Fatalf("profile_switched entries = %+v, want %+v", got, want)
	}
	if last := entries[len(entries)-1]; last.Kind != marotte.EntryKindProfileSwitched {
		t.Errorf("the newest entry is %s, want the profile_switched line after the finished turn", last.Kind)
	}
}

func TestChatProfileChanged_AMidTurnPickRecordsItsLineOnlyOnceTheTurnCloses(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	finishedTurn(t, h, "c1")
	id, _ := h.stagePromptTurn(t, "c1")

	pickProfile(t, h, "c1", policyfile.ProfileTrusted, policyfile.ProfileGuarded)
	pickProfile(t, h, "c1", policyfile.ProfileGuarded, policyfile.ProfileReadOnly)

	if got := profileSwitchesOf(t, logOf(t, cs, "c1")); len(got) != 0 {
		t.Fatalf("profile_switched entries during the turn = %+v, want none until it closes", got)
	}
	endTurn(t, h, "c1", id)
	joinInflight(t, h)

	entries := logOf(t, cs, "c1")
	got := profileSwitchesOf(t, entries)
	want := []marotte.EntryProfileSwitched{{From: policyfile.ProfileTrusted, To: policyfile.ProfileReadOnly}}
	if !slices.Equal(got, want) {
		t.Fatalf("profile_switched entries after the turn = %+v, want one from the turn's profile to the last pick %+v", got, want)
	}
	closed := slices.IndexFunc(entries, func(e marotte.Entry) bool {
		return e.Turn == id && e.Kind == marotte.EntryKindTurnClose
	})
	line := slices.IndexFunc(entries, func(e marotte.Entry) bool { return e.Kind == marotte.EntryKindProfileSwitched })
	if closed < 0 || line < closed {
		t.Errorf("the line sits at %d and the turn's close at %d, want the line after the close", line, closed)
	}
}

func TestChatProfileChanged_AMidTurnPickUndoneBeforeTheCloseRecordsNoLine(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	finishedTurn(t, h, "c1")
	id, _ := h.stagePromptTurn(t, "c1")

	pickProfile(t, h, "c1", policyfile.ProfileTrusted, policyfile.ProfileGuarded)
	pickProfile(t, h, "c1", policyfile.ProfileGuarded, policyfile.ProfileTrusted)
	endTurn(t, h, "c1", id)
	joinInflight(t, h)

	if got := profileSwitchesOf(t, logOf(t, cs, "c1")); len(got) != 0 {
		t.Errorf("profile_switched entries = %+v, want none: the chat ends the turn on the profile it started it on", got)
	}
}

func TestChatProfileChanged_AnUnstartedChatRecordsNoLine(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	pickProfile(t, h, "c1", policyfile.ProfileTrusted, policyfile.ProfileReadOnly)

	if got := logOf(t, cs, "c1"); len(got) != 0 {
		t.Errorf("log after a pick on an unstarted chat = %+v, want no entry", got)
	}
}

// A pick in a turn's close window, after a closer claimed it and before its parked move lands, still files one line,
// after the turn's turn_close.
func TestChatProfileChanged_APickDuringTheCloseRecordsOneLineAfterIt(t *testing.T) {
	tests := map[string]struct {
		parkFirst bool
		window    string
	}{
		"claimed, no park":        {window: "claimed"},
		"claimed, a park stands":  {window: "claimed", parkFirst: true},
		"finished, a park stands": {window: "finished", parkFirst: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h, cs, _ := newTestHub()
			_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
			finishedTurn(t, h, "c1")
			id, _ := h.stagePromptTurn(t, "c1")
			from := policyfile.ProfileTrusted
			if tc.parkFirst {
				pickProfile(t, h, "c1", from, policyfile.ProfileGuarded)
				from = policyfile.ProfileGuarded
			}
			resp := &marotte.RPCResponse{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}
			switch tc.window {
			case "claimed":
				claimed, ok := h.coord.turns.claimTurn(t.Context(), "c1", id, nil)
				if !ok {
					t.Fatal("Setup: the turn could not be claimed")
				}
				pickProfile(t, h, "c1", from, policyfile.ProfileReadOnly)
				h.coord.finalizeTurn(t.Context(), "c1",
					&turnClose{Closer: closerPromptResponse, Resp: resp, Turn: id, Claimed: claimed})
			case "finished":
				closed := h.coord.onTurnClosed
				h.coord.onTurnClosed = func(chatID marotte.ChatID, turnID string) {
					if closed != nil {
						closed(chatID, turnID)
					}
					pickProfile(t, h, chatID, from, policyfile.ProfileReadOnly)
				}
				endTurn(t, h, "c1", id)
			}
			joinInflight(t, h)

			entries := logOf(t, cs, "c1")
			got := profileSwitchesOf(t, entries)
			want := []marotte.EntryProfileSwitched{{From: policyfile.ProfileTrusted, To: policyfile.ProfileReadOnly}}
			if !slices.Equal(got, want) {
				t.Errorf("profile_switched entries = %+v, want %+v", got, want)
			}
			closedAt := slices.IndexFunc(entries, func(e marotte.Entry) bool {
				return e.Turn == id && e.Kind == marotte.EntryKindTurnClose
			})
			line := slices.IndexFunc(entries, func(e marotte.Entry) bool { return e.Kind == marotte.EntryKindProfileSwitched })
			if closedAt < 0 || line < closedAt {
				t.Errorf("the line sits at %d and the turn's close at %d, want the line after the close", line, closedAt)
			}
		})
	}
}
