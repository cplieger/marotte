package command

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

type spyRenamer struct {
	err   error
	calls []string
}

func (r *spyRenamer) RenameSession(_ context.Context, sessionID, title string) error {
	r.calls = append(r.calls, sessionID+"="+title)
	return r.err
}

func renameReq(t *testing.T, chatID marotte.ChatID, name string) *marotte.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(marotte.RenameChatCommand{Name: name})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &marotte.ClientCommand{Type: marotte.CmdRenameChat, ChatID: chatID, Payload: payload}
}

func seedRenameChat(t *testing.T, store ChatStore, id marotte.ChatID, sessionID string) {
	t.Helper()
	if _, err := store.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool {
		c.Name = "Agent title"
		c.RecordSession(sessionID)
		return true
	}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// seedRenameChain records sessions in order, so the last is current and the rest
// are the chat's prior segments.
func seedRenameChain(t *testing.T, store ChatStore, id marotte.ChatID, sessions ...string) {
	t.Helper()
	if _, err := store.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool {
		c.Name = "Agent title"
		for _, s := range sessions {
			c.RecordSession(s)
		}
		return true
	}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func bridgelessHost(store ChatStore) *bridgeDeps {
	return &bridgeDeps{storeDeps: &storeDeps{benchDeps: &benchDeps{}, store: store}}
}

func TestCmdRenameChat_LiveBridgeLatchesTheSessionAndNeverTheUtilityArm(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedRenameChat(t, store, "c1", "s1")
	b := &recordingBridge{result: map[string]any{"success": true}, sessionID: "s1"}
	host := newBridgeHost(store, b)
	renamer := &spyRenamer{}

	_, err := CmdRenameChat(t.Context(), host, host, renamer, renameReq(t, "c1", "  My chat  "))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("CmdRenameChat status = %d, want 200 (%s)", statusOf(err), errText(err))
	}
	if b.gotMethod != marotte.MethodSessionRename || b.gotParams["sessionId"] != "s1" || b.gotParams["title"] != "My chat" {
		t.Errorf("bridge call = %s %v, want %s {sessionId:s1 title:My chat}", b.gotMethod, b.gotParams, marotte.MethodSessionRename)
	}
	if len(renamer.calls) != 0 {
		t.Errorf("utility renames = %v, want none while the chat's own bridge is live", renamer.calls)
	}
	c, _ := store.Get(t.Context(), "c1")
	if c.Name != "My chat" || !c.NameSetByUser {
		t.Errorf("record = {%q, set_by_user %v}, want {\"My chat\", true}", c.Name, c.NameSetByUser)
	}
}

func TestCmdRenameChat_BridgelessChatUsesTheUtilityArm(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedRenameChat(t, store, "c1", "s1")
	renamer := &spyRenamer{}

	_, err := CmdRenameChat(t.Context(), bridgelessHost(store), store, renamer, renameReq(t, "c1", "Closed chat"))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("CmdRenameChat status = %d, want 200 (%s)", statusOf(err), errText(err))
	}
	if want := "s1=Closed chat"; len(renamer.calls) != 1 || renamer.calls[0] != want {
		t.Errorf("utility renames = %v, want [%s]", renamer.calls, want)
	}
}

func TestCmdRenameChat_NoSessionSendsNothing(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedRenameChat(t, store, "c1", "")
	renamer := &spyRenamer{}

	_, err := CmdRenameChat(t.Context(), bridgelessHost(store), store, renamer, renameReq(t, "c1", "Fresh"))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("CmdRenameChat status = %d, want 200 (%s)", statusOf(err), errText(err))
	}
	if len(renamer.calls) != 0 {
		t.Errorf("utility renames = %v, want none for a chat with no session", renamer.calls)
	}
}

func TestCmdRenameChat_ABridgeOnAnotherSessionSendsNothing(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedRenameChat(t, store, "c1", "s1")
	b := &recordingBridge{result: map[string]any{"success": true}, sessionID: "s2"}
	renamer := &spyRenamer{}

	_, err := CmdRenameChat(t.Context(), newBridgeHost(store, b), store, renamer, renameReq(t, "c1", "Name"))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("CmdRenameChat status = %d, want 200 (%s)", statusOf(err), errText(err))
	}
	if b.callCount != 0 || len(renamer.calls) != 0 {
		t.Errorf("bridge calls %d, utility renames %v; want none for a bridge on another session", b.callCount, renamer.calls)
	}
}

func TestCmdRenameChat_RenamesEverySessionInTheChain(t *testing.T) {
	cases := []struct {
		name        string
		bridgeOn    marotte.SessionID
		utilityErr  error
		wantBridge  bool
		wantUtility []string
	}{
		{"live bridge on the current session", "s1", nil, true, []string{"p1=Named", "p2=Named"}},
		{"bridgeless chat", "", nil, false, []string{"p1=Named", "p2=Named", "s1=Named"}},
		{"bridge on another session defers the current one", "s9", nil, false, []string{"p1=Named", "p2=Named"}},
		{"a failed prior rename still answers 200", "s1", errors.New("utility down"), true, []string{"p1=Named", "p2=Named"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			seedRenameChain(t, store, "c1", "p1", "p2", "s1")
			renamer := &spyRenamer{err: tc.utilityErr}
			var host BridgeAccess = bridgelessHost(store)
			var b *recordingBridge
			if tc.bridgeOn != "" {
				b = &recordingBridge{result: map[string]any{"success": true}, sessionID: tc.bridgeOn}
				host = newBridgeHost(store, b)
			}

			_, err := CmdRenameChat(t.Context(), host, store, renamer, renameReq(t, "c1", "Named"))

			if statusOf(err) != http.StatusOK {
				t.Fatalf("CmdRenameChat status = %d, want 200 (%s)", statusOf(err), errText(err))
			}
			if !slices.Equal(renamer.calls, tc.wantUtility) {
				t.Errorf("utility renames = %v, want %v", renamer.calls, tc.wantUtility)
			}
			gotBridge := b != nil && b.callCount == 1 && b.gotParams["sessionId"] == "s1"
			if gotBridge != tc.wantBridge {
				t.Errorf("current session renamed through the bridge = %v, want %v", gotBridge, tc.wantBridge)
			}
		})
	}
}

func TestCmdRenameChat_RefusesEmptyAndOverlongNames(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"blank", "   ", http.StatusBadRequest},
		{"emoji over the unit cap", strings.Repeat("🙂", 65), http.StatusBadRequest},
		{"exactly the unit cap", strings.Repeat("🙂", 64), http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			seedRenameChat(t, store, "c1", "")

			_, err := CmdRenameChat(t.Context(), bridgelessHost(store), store, &spyRenamer{}, renameReq(t, "c1", tc.in))

			if statusOf(err) != tc.want {
				t.Errorf("CmdRenameChat(%q) status = %d, want %d", tc.in, statusOf(err), tc.want)
			}
			c, _ := store.Get(t.Context(), "c1")
			if tc.want != http.StatusOK && (c.Name != "Agent title" || c.NameSetByUser) {
				t.Errorf("refused rename changed the record to {%q, %v}", c.Name, c.NameSetByUser)
			}
		})
	}
}

func TestCmdRenameChat_UnknownChatIs404(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()

	_, err := CmdRenameChat(t.Context(), bridgelessHost(store), store, &spyRenamer{}, renameReq(t, "nope", "x"))

	if statusOf(err) != http.StatusNotFound {
		t.Errorf("status = %d, want 404", statusOf(err))
	}
	if _, ok := store.Get(t.Context(), "nope"); ok {
		t.Error("a rename created a chat record")
	}
}

func TestCmdRenameChat_ARefusedRPCStillRenamesTheRecord(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedRenameChat(t, store, "c1", "s1")
	b := &recordingBridge{callErr: errors.New("bridge gone"), sessionID: "s1"}

	_, err := CmdRenameChat(t.Context(), newBridgeHost(store, b), store, &spyRenamer{}, renameReq(t, "c1", "Kept"))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200: the record is canonical", statusOf(err))
	}
	if c, _ := store.Get(t.Context(), "c1"); c.Name != "Kept" || !c.NameSetByUser {
		t.Errorf("record = {%q, %v}, want {Kept, true}", c.Name, c.NameSetByUser)
	}
}

func TestRenameOutcome_FalseSuccessIsAFailure(t *testing.T) {
	if err := RenameResultOutcome(json.RawMessage(`{"success":false}`)); err == nil {
		t.Error(`RenameResultOutcome({"success":false}) = nil, want an error`)
	}
	if err := RenameResultOutcome(json.RawMessage(`{"success":true}`)); err != nil {
		t.Errorf(`RenameResultOutcome({"success":true}) = %v, want nil`, err)
	}
}

func TestKASStoredTitle_MirrorsKASsCap(t *testing.T) {
	long := strings.Repeat("a", 81)
	if got, want := marotte.KASStoredTitle(long), strings.Repeat("a", 77)+"..."; got != want {
		t.Errorf("KASStoredTitle(81 a) = %q, want %q", got, want)
	}
	if got := marotte.KASStoredTitle(" short "); got != "short" {
		t.Errorf("KASStoredTitle(%q) = %q, want %q", " short ", got, "short")
	}
}

// A user name that equals the default still beats the first-prompt label.
func TestSettleComposerOnPrompt_LeavesAUserNamedChatAlone(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	if _, err := store.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = marotte.DefaultChatName
		c.NameSetByUser = true
		return true
	}); err != nil {
		t.Fatal(err)
	}

	settleComposerOnPrompt(t.Context(), store, &benchDeps{}, "c1", &marotte.PromptCommand{Text: "fix the bug", MessageID: "m1"})

	if c, _ := store.Get(t.Context(), "c1"); c.Name != marotte.DefaultChatName {
		t.Errorf("Name = %q, want the user's %q kept", c.Name, marotte.DefaultChatName)
	}
}
