package command

// BeginMessage: where a message's intent toward a chat begins. A Bot is checked and stamped in one hold, so a Bot
// left with no tab refuses the message, and a regular chat takes its stamp as before.

import (
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
	"github.com/cplieger/marotte/internal/testsupport"
)

// stampCounter answers stamp 7 and counts how often it was taken.
type stampCounter struct{ taken int }

func (s *stampCounter) stamp() uint64 {
	s.taken++
	return 7
}

func TestBeginMessage_APagesMessageToAClosedBotIsRefused(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, _ := newTabbedMembership(t, store)
	seedStartedBot(t, store, "c-bot", "CI watcher")
	var stamps stampCounter

	_, err := mem.BeginMessage(t.Context(), "c-bot", nil, stamps.stamp)

	if statusOf(err) != http.StatusConflict || reasonOf(err) != reasonChatClosed {
		t.Errorf("BeginMessage = %d %q, want 409 %s", statusOf(err), reasonOf(err), reasonChatClosed)
	}
	if stamps.taken != 0 {
		t.Errorf("stamps taken = %d, want none for a refused message", stamps.taken)
	}
}

func TestBeginMessage_AnOpenBotTakesItsStamp(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, _ := newTabbedMembership(t, store)
	seedStartedBot(t, store, "c-bot", "CI watcher")
	openChatTab(t, mem, "c-bot", "")
	var stamps stampCounter

	got, err := mem.BeginMessage(t.Context(), "c-bot", nil, stamps.stamp)

	if err != nil || got != 7 || stamps.taken != 1 {
		t.Errorf("BeginMessage = %d %v, stamps taken %d; want stamp 7 taken once", got, err, stamps.taken)
	}
}

func TestBeginMessage_AnOutsideCallersMessageReopensAClosedBotAndStamps(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, _ := newTabbedMembership(t, store)
	seedBot(t, store, "c-bot", "CI watcher", &marotte.Bot{Goal: "Keep CI green.", StartedAt: 1, ClosedAt: 2})
	var stamps stampCounter

	got, err := mem.BeginMessage(t.Context(), "c-bot", grelotCaller, stamps.stamp)

	if err != nil || got != 7 {
		t.Fatalf("BeginMessage = %d %v, want stamp 7", got, err)
	}
	if _, ok := chatTabOf(st, "c-bot"); !ok {
		t.Error("the Bot has no tab after an outside caller's message, want it reopened")
	}
	if c := botOf(t, store, "c-bot"); c.Bot.ClosedAt != 0 {
		t.Errorf("closed_at = %d, want 0 once reopened", c.Bot.ClosedAt)
	}
}

func TestBeginMessage_ABotTheReopenCannotOpenIsRefused(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, _ := newTabbedMembership(t, store)
	for i := range tabs.MaxOpenTabs {
		id := marotte.ChatID(fmt.Sprintf("c-%02d", i))
		seedNamed(t, store, id, "Open")
		openChatTab(t, mem, id, "")
	}
	seedBot(t, store, "c-bot", "CI watcher", &marotte.Bot{Goal: "Keep CI green.", StartedAt: 1, ClosedAt: 2})
	var stamps stampCounter

	_, err := mem.BeginMessage(t.Context(), "c-bot", grelotCaller, stamps.stamp)

	if statusOf(err) != http.StatusConflict || reasonOf(err) != reasonChatClosed || stamps.taken != 0 {
		t.Errorf("BeginMessage at 48 tabs = %d %q, stamps taken %d; want 409 %s and no stamp",
			statusOf(err), reasonOf(err), stamps.taken, reasonChatClosed)
	}
}

func TestBeginMessage_ARegularChatWithNoTabTakesItsStampAndOpensNothing(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, _ := newTabbedMembership(t, store)
	seedNamed(t, store, "c-a", "Alpha")
	var stamps stampCounter

	got, err := mem.BeginMessage(t.Context(), "c-a", nil, stamps.stamp)

	if err != nil || got != 7 || stamps.taken != 1 {
		t.Errorf("BeginMessage = %d %v, stamps taken %d; want stamp 7 taken once", got, err, stamps.taken)
	}
	if _, ok := chatTabOf(st, "c-a"); ok {
		t.Error("a page's message opened a tab, want only an outside caller's to")
	}
}

func TestNotify_ABotItsReopenCannotOpenIsRefusedForALaterRetry(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, _ := newTabbedMembership(t, store)
	for i := range tabs.MaxOpenTabs {
		id := marotte.ChatID(fmt.Sprintf("c-%02d", i))
		seedNamed(t, store, id, "Open")
		openChatTab(t, mem, id, "")
	}
	seedBot(t, store, "c-bot", "CI watcher", &marotte.Bot{Goal: "Keep CI green.", StartedAt: 1, ClosedAt: 2})
	roles := promptRolesOf(newTestHost(t, store))
	run := &countingRun{}
	roles.runq, roles.door = run, mem
	d := New()
	d.prompts = roles

	_, err := d.Notify(t.Context(), "c-bot", "1 new message", &marotte.Caller{Label: "grelot"}, NotifyOptions{})

	if reasonOf(err) != reasonChatClosed || ClassifyNotifyError(err) != NotifyRetryLater {
		t.Errorf("notify = %v reason %q, want 409 %s classed NotifyRetryLater", err, reasonOf(err), reasonChatClosed)
	}
	if got := run.submitted(); got != 0 {
		t.Errorf("%d submission(s) reached the run order, want none for a Bot with no tab", got)
	}
}

func TestPrompt_APagesPromptToAClosedBotIsRefusedAndSubmitsNothing(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, _ := newTabbedMembership(t, store)
	seedBot(t, store, "c-bot", "CI watcher", &marotte.Bot{Goal: "Keep CI green.", StartedAt: 1, ClosedAt: 2})
	roles := promptRolesOf(newTestHost(t, store))
	run := &countingRun{}
	roles.runq, roles.door = run, mem

	_, err := cmdPrompt(t.Context(), roles, promptReq(t, "c-bot", "hello"))

	if statusOf(err) != http.StatusConflict || reasonOf(err) != reasonChatClosed {
		t.Errorf("prompt = %d %q, want 409 %s", statusOf(err), reasonOf(err), reasonChatClosed)
	}
	if got := run.submitted(); got != 0 {
		t.Errorf("%d submission(s) reached the run order, want none for a closed Bot", got)
	}
	if _, ok := chatTabOf(st, "c-bot"); ok {
		t.Error("a page's prompt reopened the Bot, want only an outside caller's message to")
	}
}

// ruleCaller is the caller the rule engine's delivery names.
var ruleCaller = &marotte.Caller{Kind: marotte.CallerKindRule, Label: "Rule: Weekly", Rule: "r-weekly", Event: "every:t-a:1", Depth: 1}

// A rule's prompt is the Bot acting on its own: a Bot the user closed stays closed, so the message opens nothing,
// writes nothing and sends no frame, and its 409 bot_closed tells the engine to drop the event.
func TestBeginMessage_ARulesMessageNeverReopensABot(t *testing.T) {
	cases := []struct {
		name     string
		closedAt int64
	}{
		{name: "a closed Bot", closedAt: 2},
		{name: "an open Bot whose tab is gone", closedAt: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			mem, st, bus := newTabbedMembership(t, store)
			seedBot(t, store, "c-bot", "CI watcher", &marotte.Bot{Goal: "Keep CI green.", StartedAt: 1, ClosedAt: tc.closedAt})
			was := botOf(t, store, "c-bot")
			frames := len(bus.frames(t))
			var stamps stampCounter

			_, err := mem.BeginMessage(t.Context(), "c-bot", ruleCaller, stamps.stamp)

			if statusOf(err) != http.StatusConflict || reasonOf(err) != reasonBotClosed {
				t.Errorf("BeginMessage = %d %q, want 409 %s", statusOf(err), reasonOf(err), reasonBotClosed)
			}
			if got := ClassifyNotifyError(err); got != NotifyBotClosed {
				t.Errorf("class = %v, want NotifyBotClosed", got)
			}
			if stamps.taken != 0 {
				t.Errorf("stamps taken = %d, want none", stamps.taken)
			}
			if _, ok := chatTabOf(st, "c-bot"); ok {
				t.Error("a rule's message opened the Bot's tab")
			}
			if c := botOf(t, store, "c-bot"); !reflect.DeepEqual(c, was) {
				t.Errorf("record = %+v, want nothing written (was %+v)", c, was)
			}
			if n := len(bus.frames(t)); n != frames {
				t.Errorf("%d frame(s) sent, want none", n-frames)
			}
		})
	}
}

func TestBeginMessage_ARulesMessageIntoAnOpenBotTakesItsStamp(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, _ := newTabbedMembership(t, store)
	seedStartedBot(t, store, "c-bot", "CI watcher")
	openChatTab(t, mem, "c-bot", "")
	var stamps stampCounter

	got, err := mem.BeginMessage(t.Context(), "c-bot", ruleCaller, stamps.stamp)

	if err != nil || got != 7 || stamps.taken != 1 {
		t.Errorf("BeginMessage = %d %v, stamps taken %d; want stamp 7 taken once", got, err, stamps.taken)
	}
}

func TestReopenForMessage_ARulesMessageOpensNoClosedBot(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, _ := newTabbedMembership(t, store)
	seedBot(t, store, "c-bot", "CI watcher", &marotte.Bot{Goal: "Keep CI green.", StartedAt: 1, ClosedAt: 2})

	mem.ReopenForMessage(t.Context(), "c-bot", ruleCaller)

	if _, ok := chatTabOf(st, "c-bot"); ok {
		t.Error("a rule's message reopened a closed Bot")
	}
	if c := botOf(t, store, "c-bot"); c.Bot.ClosedAt != 2 {
		t.Errorf("closed_at = %d, want 2 kept", c.Bot.ClosedAt)
	}
}

func TestNotify_ARulesPromptIntoAClosedBotIsBotClosedAndSubmitsNothing(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, _ := newTabbedMembership(t, store)
	seedBot(t, store, "c-bot", "CI watcher", &marotte.Bot{Goal: "Keep CI green.", StartedAt: 1, ClosedAt: 2})
	roles := promptRolesOf(newTestHost(t, store))
	run := &countingRun{}
	roles.runq, roles.door = run, mem
	d := New()
	d.prompts = roles

	_, err := d.Notify(t.Context(), "c-bot", "rule \"Weekly\" (trigger: every hour)", ruleCaller, NotifyOptions{DisplayText: "Rule: Weekly"})

	if reasonOf(err) != reasonBotClosed || ClassifyNotifyError(err) != NotifyBotClosed {
		t.Errorf("notify = %v reason %q, want 409 %s classed NotifyBotClosed", err, reasonOf(err), reasonBotClosed)
	}
	if got := run.submitted(); got != 0 {
		t.Errorf("%d submission(s) reached the run order, want none", got)
	}
	if _, ok := chatTabOf(st, "c-bot"); ok {
		t.Error("a rule's prompt reopened the closed Bot")
	}
}
