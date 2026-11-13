package composition

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/go/slogx/capture"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
)

// TestPruneTabs_DropsAChatTabWhoseChatIsGoneAndKeepsTheRest is the whole resolver as one table,
// every arm a decision: only a chat tab resolves against the chat store.
func TestPruneTabs_DropsAChatTabWhoseChatIsGoneAndKeepsTheRest(t *testing.T) {
	chatsDir := filepath.Join(t.TempDir(), "chats")
	chatStore, err := chat.NewStore(chatsDir)
	if err != nil {
		t.Fatalf("chat store: %v", err)
	}
	if _, err := chatStore.Mutate(t.Context(), "c-live", func(c *marotte.Chat, _ bool) bool {
		c.Name = "still here"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(chatsDir, "c-unread"), 0o700); err != nil {
		t.Fatalf("seed unread chat: %v", err)
	}
	if err := os.WriteFile(filepath.Join(chatsDir, "c-unread", "chat.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed unread chat: %v", err)
	}
	tabStore, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("tab store: %v", err)
	}
	opened := map[string]string{}
	for _, spec := range []marotte.OpenTab{
		{Kind: marotte.TabKindChat, Ref: "c-live"},
		{Kind: marotte.TabKindChat, Ref: "c-gone"},
		{Kind: marotte.TabKindChat, Ref: "c-unread"},
		{Kind: marotte.TabKindEditor, Ref: "/workspace/deleted-by-a-branch-switch.go"},
		{Kind: marotte.TabKindRun, Ref: "wf-finished"},
		{Kind: marotte.TabKindSettings},
	} {
		sub, _, _, openErr := tabStore.Open(t.Context(), spec)
		if openErr != nil {
			t.Fatalf("open %+v: %v", spec, openErr)
		}
		opened[string(spec.Kind)+":"+spec.Ref] = sub.ID
	}

	pruneTabs(t.Context(), tabStore, chatStore)

	open, _ := tabStore.List()
	kept := map[string]bool{}
	for _, tab := range open {
		kept[string(tab.Kind)+":"+tab.Ref] = true
	}
	cases := []struct {
		desc string
		key  string
		want bool
	}{
		{desc: "a chat tab whose chat is still there survives", key: "chat:c-live", want: true},
		{desc: "a chat tab whose chat is gone is dropped", key: "chat:c-gone", want: false},
		{desc: "a chat tab whose record could not be read survives", key: "chat:c-unread", want: true},
		{desc: "an editor tab for a missing file is left alone", key: "editor:/workspace/deleted-by-a-branch-switch.go", want: true},
		{desc: "a run tab is left alone", key: "run:wf-finished", want: true},
		{desc: "a singleton always resolves", key: "settings:", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			if kept[tc.key] != tc.want {
				t.Errorf("%s kept = %v, want %v (set: %+v)", tc.key, kept[tc.key], tc.want, open)
			}
		})
	}
}

// A crash between a delete's record removal and its Forget, or a hand-removed chats/<id>, leaves an entry naming a
// chat that is gone; the next boot's prune drops it and keeps the entry whose record only could not be read.
func TestPruneTabs_DropsARecentlyClosedEntryWhoseChatIsGone(t *testing.T) {
	const keptWarn = "tab prune kept a chat whose record could not be read"
	chatsDir := filepath.Join(t.TempDir(), "chats")
	writer, err := chat.NewStore(chatsDir)
	if err != nil {
		t.Fatalf("Setup: chat store: %v", err)
	}
	tabsDir := t.TempDir()
	first, err := tabs.NewStore(tabsDir)
	if err != nil {
		t.Fatalf("Setup: tab store: %v", err)
	}
	for _, ref := range []string{"c-kept", "c-removed", "c-unread"} {
		if _, err := writer.Mutate(t.Context(), marotte.ChatID(ref), func(c *marotte.Chat, _ bool) bool {
			c.Name = ref
			return true
		}); err != nil {
			t.Fatalf("Setup: seed %s: %v", ref, err)
		}
		sub, _, _, err := first.Open(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: ref})
		if err != nil {
			t.Fatalf("Setup: open %s: %v", ref, err)
		}
		if _, _, _, err := first.Close(t.Context(), sub.ID, nil, map[string]tabs.RecentFacts{ref: {Name: ref}}); err != nil {
			t.Fatalf("Setup: close %s: %v", ref, err)
		}
	}
	if err := os.RemoveAll(filepath.Join(chatsDir, "c-removed")); err != nil {
		t.Fatalf("Setup: remove c-removed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(chatsDir, "c-unread", "chat.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("Setup: corrupt c-unread: %v", err)
	}
	chatStore, err := chat.NewStore(chatsDir)
	if err != nil {
		t.Fatalf("Setup: reopen chat store: %v", err)
	}
	tabStore, err := tabs.NewStore(tabsDir)
	if err != nil {
		t.Fatalf("Setup: reopen tab store: %v", err)
	}
	_, before := tabStore.List()
	logs := capture.Default(t)

	pruneTabs(t.Context(), tabStore, chatStore)

	var got []string
	for _, e := range tabStore.Recent() {
		got = append(got, e.Ref)
	}
	if want := []string{"c-unread", "c-kept"}; !slices.Equal(got, want) {
		t.Errorf("Recent() after the boot prune = %v, want %v", got, want)
	}
	if _, after := tabStore.List(); after != before+1 {
		t.Errorf("version after a prune that dropped only an entry = %d, want %d", after, before+1)
	}
	if n := logs.CountExact(keptWarn); n != 1 {
		t.Errorf("Warn %q logged %d times, want 1 for c-unread's entry", keptWarn, n)
	}
}

// TestPruneTabs_OpensEachKeptChatTabsLog: a chat's read position is projected only from an open
// log, so boot opens every chat tab's, and one unreadable log costs only that chat.
func TestPruneTabs_OpensEachKeptChatTabsLog(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "chats")
	writer, err := chat.NewStore(dir)
	if err != nil {
		t.Fatalf("chat store: %v", err)
	}
	for _, id := range []marotte.ChatID{"c-bad", "c-good"} {
		if _, err := writer.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool {
			c.Name = "a chat"
			return true
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	opened, err := writer.OpenTurn(t.Context(), "c-good", &chat.TurnSpec{
		Source: marotte.TurnOpenNamePrompt, Prompt: &marotte.EntryPrompt{ID: "m-1", Text: "hi"},
	}, nil)
	if err != nil {
		t.Fatalf("open turn: %v", err)
	}
	closer, err := json.Marshal(marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Append(t.Context(), "c-good", &marotte.Entry{
		ID: opened.Turn + ":close", Turn: opened.Turn, Kind: marotte.EntryKindTurnClose, Payload: closer,
	}); err != nil {
		t.Fatalf("close turn: %v", err)
	}
	// A directory where the log file belongs makes c-bad's log unopenable.
	if err := os.Mkdir(filepath.Join(dir, "c-bad", "entries.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	chatStore, err := chat.NewStore(dir)
	if err != nil {
		t.Fatalf("chat store: %v", err)
	}
	tabStore, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("tab store: %v", err)
	}
	for _, ref := range []string{"c-bad", "c-good"} {
		if _, _, _, err := tabStore.Open(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: ref}); err != nil {
			t.Fatalf("open tab %s: %v", ref, err)
		}
	}

	pruneTabs(t.Context(), tabStore, chatStore)

	positions := map[string]uint64{}
	for _, h := range chatStore.List(t.Context()) {
		positions[h.ID] = h.ReadPosition
	}
	if positions["c-good"] != 1 {
		t.Errorf("c-good's read_position after boot = %d, want 1 (positions %v)", positions["c-good"], positions)
	}
}

// TestPruneTabs_ToleratesAnUnwiredStore. A build with no config dir has no tab
// store, and load-time recovery must not be the thing that stops it booting.
func TestPruneTabs_ToleratesAnUnwiredStore(t *testing.T) {
	chatStore, err := chat.NewStore(filepath.Join(t.TempDir(), "chats"))
	if err != nil {
		t.Fatalf("chat store: %v", err)
	}

	pruneTabs(t.Context(), nil, chatStore)
}
