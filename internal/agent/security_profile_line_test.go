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
	joinInflight(h)

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
	joinInflight(h)

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
			joinInflight(h)

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

// pickOnGet makes a pick of to when the store's nth Get from now runs. Where the caller holds the park's lock there,
// the pick waits for it in its own goroutine, as a second device's would, instead of deadlocking inside it; the
// returned channel closes once the pick is done.
func pickOnGet(t *testing.T, h *Runtime, cs *testChatStore, chatID marotte.ChatID, nth int, to string) <-chan struct{} {
	t.Helper()
	picked := make(chan struct{})
	n := 0
	hook := func(marotte.ChatID) {
		if n++; n < nth {
			return
		}
		cs.onGet.Store(nil)
		pick := func() {
			defer close(picked)
			pickProfile(t, h, chatID, to)
		}
		if !h.coord.parkedProfiles.mu.TryLock() {
			go pick()
			return
		}
		h.coord.parkedProfiles.mu.Unlock()
		pick()
	}
	cs.onGet.Store(&hook)
	return picked
}

func awaitPick(t *testing.T, picked <-chan struct{}) {
	t.Helper()
	select {
	case <-picked:
	case <-time.After(5 * time.Second):
		t.Fatal("the injected pick never finished")
	}
}

// A pick made while the close lands its parked move, whether on the land's header read or on its line's append, files
// its line after the land's: the transcript's last line names the profile the chat runs.
func TestMoveChatProfile_APickDuringTheLandFilesItsLineAfterTheLands(t *testing.T) {
	tests := map[string]int{
		"on the land's header read": 1,
		"on the land's append":      2,
	}
	for name, nth := range tests {
		t.Run(name, func(t *testing.T) {
			h, cs, _ := newTestHub()
			_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
				c.Name, c.SecurityProfile = "A", policyfile.ProfileTrusted
				return true
			})
			finishedTurn(t, h, "c1")
			id, _ := h.stagePromptTurn(t, "c1")
			pickProfile(t, h, "c1", policyfile.ProfileGuarded)
			var picked <-chan struct{}
			h.coord.onTurnClosed = func(marotte.ChatID, string) {
				picked = pickOnGet(t, h, cs, "c1", nth, policyfile.ProfileReadOnly)
			}
			endTurn(t, h, "c1", id)
			awaitPick(t, picked)
			joinInflight(h)

			entries := logOf(t, cs, "c1")
			got := profileSwitchesOf(t, entries)
			want := []marotte.EntryProfileSwitched{
				{From: policyfile.ProfileTrusted, To: policyfile.ProfileGuarded},
				{From: policyfile.ProfileGuarded, To: policyfile.ProfileReadOnly},
			}
			if !slices.Equal(got, want) {
				t.Errorf("profile_switched entries in log order = %+v, want %+v", got, want)
			}
			closedAt := slices.IndexFunc(entries, func(e marotte.Entry) bool {
				return e.Turn == id && e.Kind == marotte.EntryKindTurnClose
			})
			line := slices.IndexFunc(entries, func(e marotte.Entry) bool { return e.Kind == marotte.EntryKindProfileSwitched })
			if closedAt < 0 || line < closedAt {
				t.Errorf("the first line sits at %d and the turn's close at %d, want every line after the close", line, closedAt)
			}
		})
	}
}

// Two picks between turns, the second made while the first files its line, file their lines in the order they moved
// the header.
func TestMoveChatProfile_IdlePicksFileTheirLinesInTheOrderTheyMoved(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name, c.SecurityProfile = "A", policyfile.ProfileTrusted
		return true
	})
	finishedTurn(t, h, "c1")

	// The first pick's only Get is its append's.
	picked := pickOnGet(t, h, cs, "c1", 1, policyfile.ProfileReadOnly)
	pickProfile(t, h, "c1", policyfile.ProfileGuarded)
	awaitPick(t, picked)

	got := profileSwitchesOf(t, logOf(t, cs, "c1"))
	want := []marotte.EntryProfileSwitched{
		{From: policyfile.ProfileTrusted, To: policyfile.ProfileGuarded},
		{From: policyfile.ProfileGuarded, To: policyfile.ProfileReadOnly},
	}
	if !slices.Equal(got, want) {
		t.Errorf("profile_switched entries in log order = %+v, want %+v", got, want)
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
