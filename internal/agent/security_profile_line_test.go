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
