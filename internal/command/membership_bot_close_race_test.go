package command

// A draft's close and its start, from two devices, end as one of the two serial orders would: a start that lands
// after the close sampled the draft keeps a started Bot, closed and listed, never a deleted one.

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
)

// startAfterSample is the real chat store running start once, right after a Get answers id's draft: between the
// close's sample of the draft and its delete.
type startAfterSample struct {
	*chat.Store
	start func()
	id    marotte.ChatID
}

func (s *startAfterSample) Get(ctx context.Context, id marotte.ChatID) (*marotte.Chat, bool) {
	c, ok := s.Store.Get(ctx, id)
	if s.start != nil && id == s.id && ok && c.Bot != nil && c.Bot.StartedAt == 0 {
		start := s.start
		s.start = nil
		start()
	}
	return c, ok
}

func TestCloseTab_AStartLandingAfterTheDraftCloseSampledItKeepsTheStartedBot(t *testing.T) {
	store, err := chat.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("Setup: chat.NewStore: %v", err)
	}
	racing := &startAfterSample{Store: store}
	h := newEscalationHost(t, racing, retentionOn)
	draft := createBot(t, h.mem, "op-draft")
	tab := h.mem.tabForChat(draft)
	roles, _ := newBotRoles(t, store)
	var saveErr error
	racing.id = draft
	racing.start = func() {
		_, saveErr = cmdBotSave(t.Context(), roles, botSaveReq(t, draft,
			&marotte.BotSaveCommand{Name: "CI watcher", Goal: "Keep CI green.", Start: true}))
	}
	mark := len(h.bus.frames(t))

	if _, _, err := h.mem.closeTab(t.Context(), tab, "op-close", true, nil); err != nil {
		t.Fatalf("closeTab(draft) = %v", err)
	}

	if racing.start != nil {
		t.Fatal("the start never ran inside the close")
	}
	if saveErr != nil {
		t.Fatalf("the start answered %d %v, want 200: the draft existed when it ran", statusOf(saveErr), saveErr)
	}
	c, ok := store.Get(t.Context(), draft)
	if !ok || c.Bot == nil {
		t.Fatal("the close deleted a Bot whose start answered 200")
	}
	if c.Bot.StartedAt == 0 || c.Bot.ClosedAt == 0 {
		t.Errorf("started_at %d closed_at %d, want a started Bot marked closed", c.Bot.StartedAt, c.Bot.ClosedAt)
	}
	if _, deleted := h.teardown.deletedByChain[draft]; deleted {
		t.Error("the kept Bot's close ran the delete-grade teardown")
	}
	recent := h.st.Recent()
	if i := slices.IndexFunc(recent, func(e marotte.RecentClose) bool { return e.Ref == string(draft) }); i < 0 || !recent[i].Bot {
		t.Errorf("Recent() = %+v, want the kept Bot listed as a Bot", recent)
	}
	listed := false
	for _, f := range h.bus.frames(t)[mark:] {
		if f.Recent != nil && slices.ContainsFunc(f.Recent.Items, func(e marotte.RecentClose) bool { return e.Ref == string(draft) }) {
			listed = true
		}
	}
	if !listed {
		t.Error("no tabs_changed frame listed the kept Bot, so no device shows it closed")
	}
}

func TestCloseTab_ADraftClosedBeforeItsStartIsRefusedAndGone(t *testing.T) {
	h := newBotHost(t, retentionOn)
	draft := createBot(t, h.mem, "op-draft")
	if _, _, err := h.mem.closeTab(t.Context(), h.mem.tabForChat(draft), "op-close", true, nil); err != nil {
		t.Fatalf("closeTab(draft) = %v", err)
	}
	roles, f := newBotRoles(t, h.chats)

	_, err := cmdBotSave(t.Context(), roles, botSaveReq(t, draft,
		&marotte.BotSaveCommand{Name: "CI watcher", Goal: "Keep CI green.", Start: true}))

	if statusOf(err) != http.StatusConflict || reasonOf(err) != reasonBotNotFound {
		t.Errorf("start after the close = %d %q, want 409 bot_not_found", statusOf(err), reasonOf(err))
	}
	if _, ok := h.chats.Get(t.Context(), draft); ok {
		t.Error("a refused start brought the closed draft back")
	}
	if len(f.kickoffs) != 0 {
		t.Errorf("a refused start sent %d kickoff(s)", len(f.kickoffs))
	}
}
