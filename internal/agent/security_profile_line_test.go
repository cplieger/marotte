package agent

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

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

// pickProfile is set_security_profile's move of chatID to to.
func pickProfile(t *testing.T, h *Runtime, chatID marotte.ChatID, to string) {
	t.Helper()
	if _, _, err := h.MoveChatProfile(t.Context(), chatID, to); err != nil {
		t.Errorf("MoveChatProfile(%s, %s) = %v", chatID, to, err)
	}
}

func TestMoveChatProfile_AnIdlePickRecordsOneLineAtOnce(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name, c.SecurityProfile = "A", policyfile.ProfileTrusted
		return true
	})
	finishedTurn(t, h, "c1")

	pickProfile(t, h, "c1", policyfile.ProfileReadOnly)

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

func TestMoveChatProfile_AMidTurnPickRecordsItsLineOnlyOnceTheTurnCloses(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name, c.SecurityProfile = "A", policyfile.ProfileTrusted
		return true
	})
	finishedTurn(t, h, "c1")
	id, _ := h.stagePromptTurn(t, "c1")

	pickProfile(t, h, "c1", policyfile.ProfileGuarded)
	pickProfile(t, h, "c1", policyfile.ProfileReadOnly)

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

func TestMoveChatProfile_AMidTurnPickUndoneBeforeTheCloseRecordsNoLine(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name, c.SecurityProfile = "A", policyfile.ProfileTrusted
		return true
	})
	finishedTurn(t, h, "c1")
	id, _ := h.stagePromptTurn(t, "c1")

	pickProfile(t, h, "c1", policyfile.ProfileGuarded)
	pickProfile(t, h, "c1", policyfile.ProfileTrusted)
	endTurn(t, h, "c1", id)
	joinInflight(t, h)

	if got := profileSwitchesOf(t, logOf(t, cs, "c1")); len(got) != 0 {
		t.Errorf("profile_switched entries = %+v, want none: the chat ends the turn on the profile it started it on", got)
	}
}

func TestMoveChatProfile_AnUnstartedChatRecordsNoLine(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name, c.SecurityProfile = "A", policyfile.ProfileTrusted
		return true
	})

	pickProfile(t, h, "c1", policyfile.ProfileReadOnly)

	if got := logOf(t, cs, "c1"); len(got) != 0 {
		t.Errorf("log after a pick on an unstarted chat = %+v, want no entry", got)
	}
}

// A pick in a turn's close window, after a closer claimed it and before its parked move lands, still files one line,
// after the turn's turn_close.
func TestMoveChatProfile_APickDuringTheCloseRecordsOneLineAfterIt(t *testing.T) {
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
			_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
				c.Name, c.SecurityProfile = "A", policyfile.ProfileTrusted
				return true
			})
			finishedTurn(t, h, "c1")
			id, _ := h.stagePromptTurn(t, "c1")
			if tc.parkFirst {
				pickProfile(t, h, "c1", policyfile.ProfileGuarded)
			}
			resp := &marotte.RPCResponse{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}
			switch tc.window {
			case "claimed":
				claimed, ok := h.coord.turns.claimTurn(t.Context(), "c1", id, nil)
				if !ok {
					t.Fatal("Setup: the turn could not be claimed")
				}
				pickProfile(t, h, "c1", policyfile.ProfileReadOnly)
				h.coord.finalizeTurn(t.Context(), "c1",
					&turnClose{Closer: closerPromptResponse, Resp: resp, Turn: id, Claimed: claimed})
			case "finished":
				closed := h.coord.onTurnClosed
				h.coord.onTurnClosed = func(chatID marotte.ChatID, turnID string) {
					if closed != nil {
						closed(chatID, turnID)
					}
					pickProfile(t, h, chatID, policyfile.ProfileReadOnly)
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

// A pick whose header write lands while the close reads the header for its parked move files each move once, each
// from the profile it left.
func TestMoveChatProfile_APickDuringTheLandsHeaderReadFilesEachMoveOnce(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name, c.SecurityProfile = "A", policyfile.ProfileTrusted
		return true
	})
	finishedTurn(t, h, "c1")
	id, _ := h.stagePromptTurn(t, "c1")
	pickProfile(t, h, "c1", policyfile.ProfileGuarded)
	picked := make(chan struct{})
	h.coord.onTurnClosed = func(marotte.ChatID, string) {
		// The close's next read is its parked move's header read.
		hook := func(marotte.ChatID) {
			cs.onGet.Store(nil)
			pick := func() {
				defer close(picked)
				pickProfile(t, h, "c1", policyfile.ProfileReadOnly)
			}
			// Where the land holds the park's lock across that read, the pick must wait for it, not run inside it.
			if !h.coord.parkedProfiles.mu.TryLock() {
				go pick()
				return
			}
			h.coord.parkedProfiles.mu.Unlock()
			pick()
		}
		cs.onGet.Store(&hook)
	}
	endTurn(t, h, "c1", id)
	select {
	case <-picked:
	case <-time.After(5 * time.Second):
		t.Fatal("the pick made during the land never finished")
	}
	joinInflight(t, h)

	got := profileSwitchesOf(t, logOf(t, cs, "c1"))
	want := []marotte.EntryProfileSwitched{
		{From: policyfile.ProfileTrusted, To: policyfile.ProfileGuarded},
		{From: policyfile.ProfileGuarded, To: policyfile.ProfileReadOnly},
	}
	if len(got) != len(want) || !slices.Contains(got, want[0]) || !slices.Contains(got, want[1]) {
		t.Errorf("profile_switched entries = %+v, want each of %+v once", got, want)
	}
}

func TestMoveChatProfile_WritesOnlyThatChatsHeader(t *testing.T) {
	h, cs, _ := newTestHub()
	for _, id := range []marotte.ChatID{"c1", "c2"} {
		_, _ = cs.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool {
			c.SecurityProfile = policyfile.ProfileTrusted
			return true
		})
	}

	found, changed, err := h.MoveChatProfile(t.Context(), "c1", policyfile.ProfileReadOnly)

	if err != nil || !found || !changed {
		t.Fatalf("MoveChatProfile = found %v, changed %v, %v; want a found, changed chat", found, changed, err)
	}
	for id, want := range map[marotte.ChatID]string{"c1": policyfile.ProfileReadOnly, "c2": policyfile.ProfileTrusted} {
		if rec, _ := cs.Get(t.Context(), id); rec.SecurityProfile != want {
			t.Errorf("%s security_profile = %q, want %q", id, rec.SecurityProfile, want)
		}
	}
}

func TestMoveChatProfile_TheSameProfileChangesNothing(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name, c.SecurityProfile = "A", policyfile.ProfileTrusted
		return true
	})
	finishedTurn(t, h, "c1")

	found, changed, err := h.MoveChatProfile(t.Context(), "c1", policyfile.ProfileTrusted)

	if err != nil || !found || changed {
		t.Errorf("MoveChatProfile = found %v, changed %v, %v; want found and unchanged", found, changed, err)
	}
	if got := profileSwitchesOf(t, logOf(t, cs, "c1")); len(got) != 0 {
		t.Errorf("profile_switched entries = %+v, want none", got)
	}
}

func TestMoveChatProfile_AnUnknownChatIsNotCreated(t *testing.T) {
	h, cs, _ := newTestHub()

	found, changed, err := h.MoveChatProfile(t.Context(), "nope", policyfile.ProfileReadOnly)

	if err != nil || found || changed {
		t.Errorf("MoveChatProfile = found %v, changed %v, %v; want not found", found, changed, err)
	}
	if _, ok := cs.Get(t.Context(), "nope"); ok {
		t.Error("the move created the chat")
	}
}
