package agent

import (
	"maps"
	"slices"
	"strconv"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// listIDs reads the ids off a List snapshot, in replay order.
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

// TestPendingPermsTracker_List_OrdersByRequestID pins ascending request id (ask order), added out
// of order and asserted as the full sequence.
func TestPendingPermsTracker_List_OrdersByRequestID(t *testing.T) {
	t.Parallel()
	tracker := newPendingPermsTracker()
	for _, id := range []int64{7, 2, 9, 1, 5} {
		tracker.Add(id, marotte.NewEvent(marotte.EventPermissionNeeded, "chat-1",
			marotte.PermissionNeededPayload{RequestID: id}))
	}

	want := []int64{1, 2, 5, 7, 9}
	// Per-pass subtests, so one unlucky pass reports without hiding the rest.
	for pass := range 8 {
		t.Run("pass_"+strconv.Itoa(pass), func(t *testing.T) {
			if got := listIDs(t, tracker.List("")); !slices.Equal(got, want) {
				t.Errorf("List order = %v, want %v", got, want)
			}
		})
	}
}

// TestPendingPermsTracker_List_OrdersAcrossKinds pins the queue's order across kinds.
func TestPendingPermsTracker_List_OrdersAcrossKinds(t *testing.T) {
	t.Parallel()
	tracker := newPendingPermsTracker()
	kinds := map[int64]marotte.EventType{
		31: marotte.EventPermissionNeeded,
		12: marotte.EventElicitationNeeded,
		20: marotte.EventUserInputNeeded,
	}
	for id, kind := range kinds {
		tracker.Add(id, marotte.NewEvent(kind, "chat-1", marotte.PermissionNeededPayload{RequestID: id}))
	}

	got := tracker.List("chat-1")
	wantIDs := []int64{12, 20, 31}
	// Fatal: the subtests below index got.
	if len(got) != len(wantIDs) {
		t.Fatalf("List returned %d events, want %d: %v", len(got), len(wantIDs), listIDs(t, got))
	}
	if ids := listIDs(t, got); !slices.Equal(ids, wantIDs) {
		t.Errorf("List order = %v, want %v", ids, wantIDs)
	}
	for i, id := range wantIDs {
		t.Run(string(kinds[id]), func(t *testing.T) {
			if got[i].Type != kinds[id] {
				t.Errorf("id %d replayed as %q, want %q", id, got[i].Type, kinds[id])
			}
		})
	}
}

// TestPendingPermsTracker_List_FiltersByChatAndStaysOrdered pins that filtering never re-sorts.
func TestPendingPermsTracker_List_FiltersByChatAndStaysOrdered(t *testing.T) {
	t.Parallel()
	tracker := newPendingPermsTracker()
	owners := map[int64]marotte.ChatID{4: "chat-1", 8: "chat-2", 1: "chat-1", 6: "chat-2", 3: "chat-1"}
	for id, chatID := range owners {
		tracker.Add(id, marotte.NewEvent(marotte.EventPermissionNeeded, chatID,
			marotte.PermissionNeededPayload{RequestID: id}))
	}

	for chatID, want := range map[marotte.ChatID][]int64{
		"chat-1": {1, 3, 4},
		"chat-2": {6, 8},
	} {
		t.Run(string(chatID), func(t *testing.T) {
			if got := listIDs(t, tracker.List(chatID)); !slices.Equal(got, want) {
				t.Errorf("List(%q) order = %v, want %v", chatID, got, want)
			}
		})
	}
}

// Keeping the closing chat's entries strands a card; dropping another chat's refuses its answer.
func TestPendingPermsTracker_ClearForChat_DropsOnlyThatChat(t *testing.T) {
	t.Parallel()
	tracker := newPendingPermsTracker()
	owners := map[int64]marotte.ChatID{1: "chat-1", 2: "chat-2", 3: "chat-1", 4: "chat-2"}
	for id, chatID := range owners {
		tracker.Add(id, marotte.NewEvent(marotte.EventPermissionNeeded, chatID,
			marotte.PermissionNeededPayload{RequestID: id}))
	}

	tracker.ClearForChat("chat-1")

	if got := listIDs(t, tracker.List("chat-1")); len(got) != 0 {
		t.Errorf("List(\"chat-1\") = %v after ClearForChat(\"chat-1\"), want none", got)
	}
	want := []int64{2, 4}
	if got := listIDs(t, tracker.List("chat-2")); !slices.Equal(got, want) {
		t.Errorf("List(\"chat-2\") = %v after ClearForChat(\"chat-1\"), want %v", got, want)
	}
	// The surviving chat's answers are still accepted.
	if _, ok := tracker.TakeIfPresent("chat-2", 2); !ok {
		t.Error(`TakeIfPresent("chat-2", 2) = false: another chat's clear took chat-2's entry`)
	}
}

// An empty chat id is not a wildcard.
func TestPendingPermsTracker_ClearForChat_EmptyChatIDClearsNothing(t *testing.T) {
	t.Parallel()
	tracker := newPendingPermsTracker()
	for _, id := range []int64{1, 2} {
		tracker.Add(id, marotte.NewEvent(marotte.EventPermissionNeeded, "chat-1",
			marotte.PermissionNeededPayload{RequestID: id}))
	}

	tracker.ClearForChat("")

	want := []int64{1, 2}
	if got := listIDs(t, tracker.List("")); !slices.Equal(got, want) {
		t.Errorf("List(\"\") = %v after ClearForChat(\"\"), want %v", got, want)
	}
}

// TestPendingPermsTracker_TwoChatsMayHoldTheSameRequestID pins that ids are per bridge, so an id-only key
// would overwrite one chat's card.
func TestPendingPermsTracker_TwoChatsMayHoldTheSameRequestID(t *testing.T) {
	t.Parallel()
	tracker := newPendingPermsTracker()
	const shared = int64(7)
	tracker.Add(shared, marotte.NewEvent(marotte.EventPermissionNeeded, "chat-1",
		marotte.PermissionNeededPayload{RequestID: shared, Title: "chat-1 asked"}))
	tracker.Add(shared, marotte.NewEvent(marotte.EventUserInputNeeded, "chat-2",
		marotte.UserInputNeededPayload{RequestID: shared, Question: "chat-2 asked"}))

	// Each chat replays its own card.
	for _, tc := range []struct {
		chat marotte.ChatID
		want marotte.EventType
	}{
		{chat: "chat-1", want: marotte.EventPermissionNeeded},
		{chat: "chat-2", want: marotte.EventUserInputNeeded},
	} {
		got := tracker.List(tc.chat)
		if len(got) != 1 {
			t.Fatalf("List(%q) returned %d cards, want 1", tc.chat, len(got))
		}
		if got[0].Type != tc.want {
			t.Errorf("List(%q) card type = %q, want %q: the other chat's request "+
				"overwrote this one", tc.chat, got[0].Type, tc.want)
		}
	}

	// chat-2's request survives chat-1 answering.
	evt, ok := tracker.TakeIfPresent("chat-1", shared)
	if !ok {
		t.Fatal(`TakeIfPresent("chat-1", 7) refused a pending request`)
	}
	if evt.ChatID != "chat-1" {
		t.Errorf("chat-1's claim returned chat %q's event", evt.ChatID)
	}
	if _, ok := tracker.TakeIfPresent("chat-2", shared); !ok {
		t.Error(`TakeIfPresent("chat-2", 7) = false after chat-1 answered: chat-2's ` +
			"turn now waits forever for a response nothing can send")
	}
	// A claim naming the wrong chat resolves nothing.
	tracker.Add(shared, marotte.NewEvent(marotte.EventPermissionNeeded, "chat-3",
		marotte.PermissionNeededPayload{RequestID: shared}))
	if _, ok := tracker.TakeIfPresent("chat-4", shared); ok {
		t.Error(`TakeIfPresent("chat-4", 7) succeeded against chat-3's request`)
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
	for _, e := range entries {
		tracker.Add(e.id, marotte.NewEvent(kindOf[e.id], launching, e.payload))
	}

	tracker.ClearForRun("wf_1")

	left := map[int64]bool{}
	for _, evt := range tracker.List("") {
		left[requestIDOf(t, evt)] = true
	}
	for _, e := range entries {
		t.Run(e.name, func(t *testing.T) {
			if left[e.id] != e.survives {
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
	tracker.Add(1, marotte.NewEvent(marotte.EventPermissionNeeded, "c1",
		marotte.PermissionNeededPayload{RequestID: 1}))

	tracker.ClearForRun("")

	if got := len(tracker.List("")); got != 1 {
		t.Errorf("an empty run id left %d cards, want the chat's own 1", got)
	}
}

// TestOpenNodesForRun_NamesTheRunsOwnUnansweredSteps pins the idle window's evidence, across chats and kinds.
func TestOpenNodesForRun_NamesTheRunsOwnUnansweredSteps(t *testing.T) {
	t.Parallel()
	tracker := newPendingPermsTracker()
	tracker.Add(1, marotte.NewEvent(marotte.EventPermissionNeeded, "c-parent",
		marotte.PermissionNeededPayload{RequestID: 1, RunID: "wf_1", NodeID: "a"}))
	tracker.Add(2, marotte.NewEvent(marotte.EventElicitationNeeded, runChatID("wf_1"),
		marotte.ElicitationNeededPayload{RequestID: 2, RunID: "wf_1", NodeID: "b"}))
	tracker.Add(3, marotte.NewEvent(marotte.EventUserInputNeeded, "c-parent",
		marotte.UserInputNeededPayload{RequestID: 3, RunID: "wf_1", NodeID: "c"}))
	tracker.Add(4, marotte.NewEvent(marotte.EventPermissionNeeded, "c-parent",
		marotte.PermissionNeededPayload{RequestID: 4, RunID: "wf_1"}))
	tracker.Add(5, marotte.NewEvent(marotte.EventPermissionNeeded, "c-parent",
		marotte.PermissionNeededPayload{RequestID: 5, RunID: "wf_2", NodeID: "d"}))

	got := slices.Sorted(maps.Keys(tracker.OpenNodesForRun("wf_1")))
	if want := []string{"a", "b", "c"}; !slices.Equal(got, want) {
		t.Errorf("OpenNodesForRun(wf_1) = %v, want %v: every kind, any chat, no node-less ask", got, want)
	}

	if _, ok := tracker.TakeIfPresent("c-parent", 1); !ok {
		t.Fatal("request 1 was not pending")
	}
	if _, held := tracker.OpenNodesForRun("wf_1")["a"]; held {
		t.Error("an answered decision still names its node")
	}
	if got := tracker.OpenNodesForRun(""); got != nil {
		t.Errorf("OpenNodesForRun(\"\") = %v, want nil: an empty run would match every chat ask", got)
	}
}
