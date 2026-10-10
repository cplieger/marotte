package agent

import (
	"maps"
	"slices"
	"strconv"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func listIDs(t *testing.T, evts []marotte.ServerEvent) []int64 {
	t.Helper()
	ids := make([]int64, 0, len(evts))
	for _, evt := range evts {
		p, ok := evt.Payload.(marotte.PermissionNeededPayload)
		if !ok {
			t.Fatalf("replayed event carries payload %T, want marotte.PermissionNeededPayload", evt.Payload)
		}
		ids = append(ids, p.RequestID)
	}
	return ids
}

// titlesOf reads each replayed permission card's title, the tests' handle on which ask it is.
func titlesOf(t *testing.T, evts []marotte.ServerEvent) []string {
	t.Helper()
	out := make([]string, 0, len(evts))
	for _, evt := range evts {
		p, ok := evt.Payload.(marotte.PermissionNeededPayload)
		if !ok {
			t.Fatalf("replayed event carries payload %T, want marotte.PermissionNeededPayload", evt.Payload)
		}
		out = append(out, p.Title)
	}
	return out
}

// TestPendingPermsTracker_List_OrdersByAskOrder pins replay in the order the asks arrived, whatever
// their ACP ids, each under an ascending ask id.
func TestPendingPermsTracker_List_OrdersByAskOrder(t *testing.T) {
	t.Parallel()
	tracker := newPendingPermsTracker()
	for _, acpID := range []int64{7, 2, 9, 1, 5} {
		tracker.add(acpID, marotte.NewEvent(marotte.EventPermissionNeeded, "chat-1",
			marotte.PermissionNeededPayload{Title: "acp " + strconv.FormatInt(acpID, 10)}), nil)
	}

	want := []string{"acp 7", "acp 2", "acp 9", "acp 1", "acp 5"}
	// Per-pass subtests, so one unlucky pass reports without hiding the rest.
	for pass := range 8 {
		t.Run("pass_"+strconv.Itoa(pass), func(t *testing.T) {
			got := tracker.list("")
			if titles := titlesOf(t, got); !slices.Equal(titles, want) {
				t.Errorf("List order = %v, want %v", titles, want)
			}
			if ids := listIDs(t, got); !slices.IsSorted(ids) {
				t.Errorf("List ask ids = %v, want ascending", ids)
			}
		})
	}
}

// TestPendingPermsTracker_List_OrdersAcrossKinds pins the queue's order across kinds.
func TestPendingPermsTracker_List_OrdersAcrossKinds(t *testing.T) {
	t.Parallel()
	tracker := newPendingPermsTracker()
	kinds := []marotte.EventType{marotte.EventElicitationNeeded, marotte.EventUserInputNeeded, marotte.EventPermissionNeeded}
	for i, kind := range kinds {
		tracker.add(int64(30-i), marotte.NewEvent(kind, "chat-1", marotte.PermissionNeededPayload{}), nil)
	}

	got := tracker.list("chat-1")
	// Fatal: the subtests below index got.
	if len(got) != len(kinds) {
		t.Fatalf("List returned %d events, want %d", len(got), len(kinds))
	}
	for i, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			if got[i].Type != kind {
				t.Errorf("ask %d replayed as %q, want %q", i, got[i].Type, kind)
			}
		})
	}
}

// TestPendingPermsTracker_List_FiltersByChatAndStaysOrdered pins that filtering never re-sorts.
func TestPendingPermsTracker_List_FiltersByChatAndStaysOrdered(t *testing.T) {
	t.Parallel()
	tracker := newPendingPermsTracker()
	for _, a := range []struct {
		chat  marotte.ChatID
		title string
	}{{"chat-1", "a"}, {"chat-2", "b"}, {"chat-1", "c"}, {"chat-2", "d"}, {"chat-1", "e"}} {
		tracker.add(1, marotte.NewEvent(marotte.EventPermissionNeeded, a.chat,
			marotte.PermissionNeededPayload{Title: a.title}), nil)
	}

	for chatID, want := range map[marotte.ChatID][]string{
		"chat-1": {"a", "c", "e"},
		"chat-2": {"b", "d"},
	} {
		t.Run(string(chatID), func(t *testing.T) {
			if got := titlesOf(t, tracker.list(chatID)); !slices.Equal(got, want) {
				t.Errorf("List(%q) order = %v, want %v", chatID, got, want)
			}
		})
	}
}

// Keeping the closing chat's entries strands a card; dropping another chat's refuses its answer.
func TestPendingPermsTracker_ClearForChat_DropsOnlyThatChat(t *testing.T) {
	t.Parallel()
	tracker := newPendingPermsTracker()
	var chat2 []int64
	for i, chatID := range []marotte.ChatID{"chat-1", "chat-2", "chat-1", "chat-2"} {
		evt := tracker.add(int64(i), marotte.NewEvent(marotte.EventPermissionNeeded, chatID,
			marotte.PermissionNeededPayload{}), nil)
		if chatID == "chat-2" {
			chat2 = append(chat2, requestIDOf(t, evt))
		}
	}

	tracker.clearForChat("chat-1")

	if got := listIDs(t, tracker.list("chat-1")); len(got) != 0 {
		t.Errorf("List(\"chat-1\") = %v after ClearForChat(\"chat-1\"), want none", got)
	}
	if got := listIDs(t, tracker.list("chat-2")); !slices.Equal(got, chat2) {
		t.Errorf("List(\"chat-2\") = %v after ClearForChat(\"chat-1\"), want %v", got, chat2)
	}
	// The surviving chat's answers are still accepted.
	if _, ok := tracker.takeIfPresent("chat-2", chat2[0]); !ok {
		t.Errorf("TakeIfPresent(\"chat-2\", %d) = false: another chat's clear took chat-2's entry", chat2[0])
	}
}

// An empty chat id is not a wildcard.
func TestPendingPermsTracker_ClearForChat_EmptyChatIDClearsNothing(t *testing.T) {
	t.Parallel()
	tracker := newPendingPermsTracker()
	for _, id := range []int64{1, 2} {
		tracker.add(id, marotte.NewEvent(marotte.EventPermissionNeeded, "chat-1",
			marotte.PermissionNeededPayload{}), nil)
	}

	tracker.clearForChat("")

	if got := len(tracker.list("")); got != 2 {
		t.Errorf("List(\"\") holds %d asks after ClearForChat(\"\"), want 2", got)
	}
}

// TestPendingPermsTracker_ReusedACPIDsAreSeparateAsks pins identity by ask id, never by ACP id:
// every bridge mints ACP ids from zero, so two chats, or one chat's old bridge and its successor,
// send the same one. Each stays listed, answerable and retired by its own origin.
func TestPendingPermsTracker_ReusedACPIDsAreSeparateAsks(t *testing.T) {
	t.Parallel()
	const reused = int64(7)
	for _, tc := range []struct {
		name              string
		oldChat, succChat marotte.ChatID
		oldKind, succKind marotte.EventType
	}{
		{"two chats", "chat-1", "chat-2", marotte.EventPermissionNeeded, marotte.EventUserInputNeeded},
		{"successor, same kind", "chat-1", "chat-1", marotte.EventPermissionNeeded, marotte.EventPermissionNeeded},
		{"successor, other kind", "chat-1", "chat-1", marotte.EventPermissionNeeded, marotte.EventElicitationNeeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracker := newPendingPermsTracker()
			old, succ := newFakeBridge(), newFakeBridge()
			oldID := requestIDOf(t, tracker.add(reused, marotte.NewEvent(tc.oldKind, tc.oldChat, marotte.PermissionNeededPayload{}), old))
			succID := requestIDOf(t, tracker.add(reused, marotte.NewEvent(tc.succKind, tc.succChat, marotte.PermissionNeededPayload{}), succ))
			if oldID == succID {
				t.Fatalf("two asks sent as ACP id %d share ask id %d", reused, oldID)
			}
			if got := len(tracker.list("")); got != 2 {
				t.Fatalf("List holds %d asks, want both", got)
			}
			ended := tracker.endOrigin(old)
			if len(ended) != 1 || ended[0].id != oldID || ended[0].evt.Type != tc.oldKind {
				t.Errorf("EndOrigin(old) = %+v, want only the old bridge's ask %d", ended, oldID)
			}
			ask, ok := tracker.takeIfPresent(tc.succChat, succID)
			if !ok {
				t.Fatalf("the successor's ask %d is no longer answerable after the old bridge ended", succID)
			}
			if ask.origin != acpResponder(succ) || ask.acpID != reused || ask.evt.Type != tc.succKind {
				t.Errorf("claim = {origin %p, acp %d, %q}, want the successor's {%p, %d, %q}",
					ask.origin, ask.acpID, ask.evt.Type, succ, reused, tc.succKind)
			}
		})
	}
}

// The unattended floor claims by bridge and ACP id: a successor's ask under the old bridge's id is
// not the floor's to answer.
func TestPendingPermsTracker_TakeOnOrigin_ClaimsOnlyThatBridgesAsk(t *testing.T) {
	t.Parallel()
	tracker := newPendingPermsTracker()
	old, succ := newFakeBridge(), newFakeBridge()
	oldID := requestIDOf(t, tracker.add(7, marotte.NewEvent(marotte.EventPermissionNeeded, "run:wf_1",
		marotte.PermissionNeededPayload{}), old))
	succID := requestIDOf(t, tracker.add(7, marotte.NewEvent(marotte.EventPermissionNeeded, "run:wf_1",
		marotte.PermissionNeededPayload{}), succ))

	id, ask, ok := tracker.takeOnOrigin(succ, 7)
	if !ok || id != succID || ask.origin != acpResponder(succ) {
		t.Fatalf("TakeOnOrigin(successor, 7) = (%d, origin %p, %t), want the successor's ask %d", id, ask.origin, ok, succID)
	}
	if got := listIDs(t, tracker.list("")); !slices.Equal(got, []int64{oldID}) {
		t.Errorf("asks pending = %v, want the old bridge's [%d] untouched", got, oldID)
	}
	if _, _, ok := tracker.takeOnOrigin(succ, 7); ok {
		t.Error("TakeOnOrigin claimed the successor's ask twice")
	}
}

// A claim naming the wrong chat resolves nothing.
func TestPendingPermsTracker_TakeIfPresent_RefusesAnotherChatsAsk(t *testing.T) {
	t.Parallel()
	tracker := newPendingPermsTracker()
	id := requestIDOf(t, tracker.add(7, marotte.NewEvent(marotte.EventPermissionNeeded, "chat-3",
		marotte.PermissionNeededPayload{}), nil))
	if _, ok := tracker.takeIfPresent("chat-4", id); ok {
		t.Errorf("TakeIfPresent(\"chat-4\", %d) succeeded against chat-3's ask", id)
	}
	if _, _, offered := tracker.takePermissionOption("chat-4", id, ""); offered {
		t.Errorf("TakePermissionOption(\"chat-4\", %d) succeeded against chat-3's ask", id)
	}
}

// requestIDOf reads a card's request id whatever its kind.
func requestIDOf(t *testing.T, evt marotte.ServerEvent) int64 {
	t.Helper()
	switch p := evt.Payload.(type) {
	case marotte.PermissionNeededPayload:
		return p.RequestID
	case marotte.ElicitationNeededPayload:
		return p.RequestID
	case marotte.UserInputNeededPayload:
		return p.RequestID
	default:
		t.Fatalf("replayed event carries payload %T, want one of the three *_needed payloads", evt.Payload)
		return 0
	}
}

// TestClearForRun_DropsOnlyTheNamedRunsDecisions pins the run read off the payload, across all three kinds a step raises.
func TestClearForRun_DropsOnlyTheNamedRunsDecisions(t *testing.T) {
	t.Parallel()
	const launching marotte.ChatID = "c-parent"
	entries := []struct {
		id      int64
		name    string
		payload any
		// survives says the entry must still replay after ClearForRun("wf_1").
		survives bool
	}{
		{1, "a step's question", marotte.UserInputNeededPayload{RequestID: 1, RunID: "wf_1"}, false},
		{2, "a step's permission", marotte.PermissionNeededPayload{RequestID: 2, RunID: "wf_1"}, false},
		{3, "a step's elicitation", marotte.ElicitationNeededPayload{RequestID: 3, RunID: "wf_1"}, false},
		// A sibling run shares the chat's entries, so the clear separates by run.
		{4, "a sibling run's question", marotte.UserInputNeededPayload{RequestID: 4, RunID: "wf_2"}, true},
		// An ordinary chat ask carries no run.
		{5, "the chat's own permission", marotte.PermissionNeededPayload{RequestID: 5}, true},
	}
	kindOf := map[int64]marotte.EventType{
		1: marotte.EventUserInputNeeded, 2: marotte.EventPermissionNeeded,
		3: marotte.EventElicitationNeeded, 4: marotte.EventUserInputNeeded,
		5: marotte.EventPermissionNeeded,
	}
	tracker := newPendingPermsTracker()
	askIDs := map[int64]int64{}
	for _, e := range entries {
		askIDs[e.id] = requestIDOf(t, tracker.add(e.id, marotte.NewEvent(kindOf[e.id], launching, e.payload), nil))
	}

	tracker.clearForRun("wf_1")

	left := map[int64]bool{}
	for _, evt := range tracker.list("") {
		left[requestIDOf(t, evt)] = true
	}
	for _, e := range entries {
		t.Run(e.name, func(t *testing.T) {
			if left[askIDs[e.id]] != e.survives {
				verb := "survived the run's end"
				if e.survives {
					verb = "was swept by another run's end"
				}
				t.Errorf("request %d (%s) %s", e.id, e.name, verb)
			}
		})
	}
}

// TestClearForRun_RefusesAnEmptyRunID pins that an empty id would match every ordinary chat ask.
func TestClearForRun_RefusesAnEmptyRunID(t *testing.T) {
	t.Parallel()
	tracker := newPendingPermsTracker()
	tracker.add(1, marotte.NewEvent(marotte.EventPermissionNeeded, "c1",
		marotte.PermissionNeededPayload{RequestID: 1}), nil)

	tracker.clearForRun("")

	if got := len(tracker.list("")); got != 1 {
		t.Errorf("an empty run id left %d cards, want the chat's own 1", got)
	}
}

// TestOpenNodesForRun_NamesTheRunsOwnUnansweredSteps pins the idle window's evidence, across chats and kinds.
func TestOpenNodesForRun_NamesTheRunsOwnUnansweredSteps(t *testing.T) {
	t.Parallel()
	tracker := newPendingPermsTracker()
	first := requestIDOf(t, tracker.add(1, marotte.NewEvent(marotte.EventPermissionNeeded, "c-parent",
		marotte.PermissionNeededPayload{RunID: "wf_1", NodeID: "a"}), nil))
	tracker.add(2, marotte.NewEvent(marotte.EventElicitationNeeded, runChatID("wf_1"),
		marotte.ElicitationNeededPayload{RequestID: 2, RunID: "wf_1", NodeID: "b"}), nil)
	tracker.add(3, marotte.NewEvent(marotte.EventUserInputNeeded, "c-parent",
		marotte.UserInputNeededPayload{RequestID: 3, RunID: "wf_1", NodeID: "c"}), nil)
	tracker.add(4, marotte.NewEvent(marotte.EventPermissionNeeded, "c-parent",
		marotte.PermissionNeededPayload{RequestID: 4, RunID: "wf_1"}), nil)
	tracker.add(5, marotte.NewEvent(marotte.EventPermissionNeeded, "c-parent",
		marotte.PermissionNeededPayload{RequestID: 5, RunID: "wf_2", NodeID: "d"}), nil)

	got := slices.Sorted(maps.Keys(tracker.openNodesForRun("wf_1")))
	if want := []string{"a", "b", "c"}; !slices.Equal(got, want) {
		t.Errorf("OpenNodesForRun(wf_1) = %v, want %v: every kind, any chat, no node-less ask", got, want)
	}

	if _, ok := tracker.takeIfPresent("c-parent", first); !ok {
		t.Fatal("node a's ask was not pending")
	}
	if _, held := tracker.openNodesForRun("wf_1")["a"]; held {
		t.Error("an answered decision still names its node")
	}
	if got := tracker.openNodesForRun(""); got != nil {
		t.Errorf("OpenNodesForRun(\"\") = %v, want nil: an empty run would match every chat ask", got)
	}
}
