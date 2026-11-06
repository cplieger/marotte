package chat

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// searchItemsFixture is testdata/search_items.json: what one search hit is, per case, as searchItems decides it.
// static-src/search-items-contract.test.ts renders each body through the transcript and reads its
// `data-find-unit` stamps, so a boundary the renderer draws and the server does not fails there.
type searchItemsFixture struct {
	Comment []string          `json:"_comment"`
	Cases   []searchItemsCase `json:"cases"`
}

type searchItemsCase struct {
	Name string `json:"name"`
	// HooksHidden runs the case with hooks.showStatus off, through searchView.
	HooksHidden bool               `json:"hooks_hidden,omitempty"`
	Body        []searchItemsEntry `json:"body"`
	// LaterWake is a turn_wake payload recorded in a later turn of the same chat.
	LaterWake json.RawMessage   `json:"later_wake,omitempty"`
	Items     []searchItemsWant `json:"items"`
}

type searchItemsEntry struct {
	ID      string            `json:"id"`
	Kind    marotte.EntryKind `json:"kind"`
	Lane    string            `json:"lane,omitempty"`
	Payload json.RawMessage   `json:"payload"`
}

type searchItemsWant struct {
	Kind    ItemKind `json:"kind"`
	Lane    string   `json:"lane"`
	Anchor  string   `json:"anchor"`
	Entries []string `json:"entries"`
}

// searchItemsTurn is every case's turn id: a minted say is `<turn>:e<k>`, so both readers must use this id.
const searchItemsTurn = "t-1"

var searchItemsComment = []string{
	"What one search hit is: the items internal/chat's searchItems finds in a turn, in order.",
	"",
	"Go PRODUCES this file (TestSearchItemsFixture) and static-src/search-items-contract.test.ts",
	"CONSUMES it, rendering body as turn " + searchItemsTurn + " (its turn_open first) and reading each",
	"[data-find-unit] element: its kind, its anchor (data-entry-id) and, for a message, every member",
	"(data-entries). A tool card and the plan card list their folded members here only. Header and",
	"turn_failure items are the turn card's, outside the body the TS side renders, so it skips them.",
	"hooks_hidden runs a case with hooks.showStatus off. later_wake is a turn_wake in a later turn.",
	"",
	"Regenerate with: UPDATE_GOLDEN=1 go test ./internal/chat/ -run TestSearchItemsFixture",
	"then, from static-src/: npx vitest --run search-items-contract.test.ts",
}

func itemsEntry(id string, kind marotte.EntryKind, lane string, payload any) searchItemsEntry {
	raw, err := json.Marshal(payload)
	if err != nil {
		panic("itemsEntry: " + err.Error())
	}
	return searchItemsEntry{ID: id, Kind: kind, Lane: lane, Payload: raw}
}

func itemsText(id, text string) searchItemsEntry {
	return itemsEntry(id, marotte.EntryKindText, "", marotte.EntryText{Text: text})
}

func itemsCall(id, title string, kind marotte.ToolKind) searchItemsEntry {
	return itemsEntry(id, marotte.EntryKindToolCall, "", marotte.EntryToolCall{ID: "t-" + id, Title: title, Kind: kind, Status: marotte.ToolCompleted})
}

func itemsResult(callID, output string) searchItemsEntry {
	return itemsEntry(callID+":result", marotte.EntryKindToolResult, "", marotte.EntryToolResult{Status: marotte.ToolCompleted, Output: output})
}

func itemsHook(id, trigger, output string) searchItemsEntry {
	return itemsEntry(id, marotte.EntryKindToolCall, "", marotte.EntryToolCall{
		ID: "t-" + id, Title: "Hook fired: guard", Kind: marotte.ToolKindHook, Status: marotte.ToolCompleted,
		HookTrigger: trigger, Output: output,
	})
}

func searchItemsCases() []searchItemsCase {
	plan := func(content string) marotte.EntryPlan {
		return marotte.EntryPlan{Entries: []marotte.PlanEntry{{Content: content, Status: marotte.PlanPending}}}
	}
	return []searchItemsCase{
		{Name: "a tool card breaks a message", Body: []searchItemsEntry{
			itemsText("s1#0", "first half"), itemsCall("c1", "go vet", marotte.ToolKindExecute), itemsResult("c1", "ok"),
			itemsText("s1#1", "second half"),
		}},
		{Name: "a result renders nothing, so a message runs across it", Body: []searchItemsEntry{
			itemsCall("c1", "go vet", marotte.ToolKindExecute), itemsText("s1#0", "first half"), itemsResult("c1", "ok"),
			itemsText("s1#1", "second half"),
		}},
		{Name: "two named says are two messages", Body: []searchItemsEntry{
			itemsText("say-a", "one message"), itemsText("say-b", "another message"),
		}},
		{Name: "a minted say rides the message it follows", Body: []searchItemsEntry{
			itemsText("say-a#0", "one message"), itemsText(searchItemsTurn+":e4", " continued"), itemsText("say-a#1", " and done"),
		}},
		{Name: "a delegate's text is its lane's message", Body: []searchItemsEntry{
			itemsText("say-a", "the chat's words"),
			itemsEntry("d1", marotte.EntryKindText, "sub-1", marotte.EntryText{Text: "the delegate's words"}),
			itemsText("say-a#1", " go on"),
		}},
		{Name: "reasoning, a steer and notices are items of their own", Body: []searchItemsEntry{
			itemsEntry("th1", marotte.EntryKindThinking, "", marotte.EntryThinking{Text: "weighing it"}),
			itemsText("say-a", "the answer"),
			itemsEntry("st1", marotte.EntryKindSteer, "", marotte.EntrySteer{Text: "also this", Origin: marotte.SteerOriginUser, State: marotte.SteerStateRead}),
			itemsEntry("cf1", marotte.EntryKindCompactionFailed, "", marotte.EntryCompactionFailed{Reason: "too large"}),
			itemsEntry("mr1", marotte.EntryKindModelRouted, "", marotte.EntryModelRouted{Message: "routed to a smaller model"}),
		}},
		{Name: "a drawn row with no searched text ends a message and is no item", Body: []searchItemsEntry{
			itemsText("say-a#0", "before"),
			itemsEntry("ms1", marotte.EntryKindModeSwitched, "", marotte.EntryModeSwitched{From: "vibe", To: "plan"}),
			itemsText("say-a#1", "after"),
		}},
		{Name: "a steer acknowledgement is a message of its own", Body: []searchItemsEntry{
			itemsText("say-a#0", "before"),
			itemsEntry("st1:ack", marotte.EntryKindSteerAck, "", marotte.EntrySteerAck{SteerID: "st1", Text: "noted"}),
			itemsText("say-a#1", "after"),
		}},
		{Name: "every plan update folds into the first plan's card", Body: []searchItemsEntry{
			itemsEntry("p1", marotte.EntryKindPlan, "", plan("draft the fix")),
			itemsText("say-a#0", "working"),
			itemsEntry("p2", marotte.EntryKindPlan, "", plan("land the fix")),
			itemsText("say-a#1", " still working"),
		}},
		{Name: "a folded hook is its own tool item", Body: []searchItemsEntry{
			itemsHook("h1", "PreToolUse", ""), itemsCall("c1", "go vet", marotte.ToolKindExecute),
			itemsText("say-a", "done"),
		}},
		{Name: "a hidden hook is no item and ends nothing", HooksHidden: true, Body: []searchItemsEntry{
			itemsText("say-a#0", "before"), itemsHook("h1", "SessionStart", ""), itemsText("say-a#1", "after"),
		}},
		{
			Name: "a row a wake draws is no item where it was filed",
			Body: []searchItemsEntry{
				itemsText("say-a", "launched"), itemsEntry("wfmsg-q", marotte.EntryKindSteer, "", wakeRow("Rewrite the links?", "warning")),
			},
			LaterWake: mustJSON(marotte.EntryTurnWake{
				Reason: marotte.WakeReasonStepMessage,
				Steers: []marotte.WakeSteer{{ID: "wfmsg-q", Run: wakeRunID, Step: "draft", Severity: "warning", Text: "Rewrite the links?"}},
			}),
		},
		{Name: "a turn's header and failure are items", Body: []searchItemsEntry{
			itemsText("say-a", "partial"),
			itemsEntry(searchItemsTurn+":close", marotte.EntryKindTurnClose, "", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeFailed, FailureReason: "budget ran out"}),
		}},
	}
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic("mustJSON: " + err.Error())
	}
	return raw
}

func TestSearchItemsFixture(t *testing.T) {
	fx := searchItemsFixture{Comment: searchItemsComment, Cases: searchItemsCases()}
	for i := range fx.Cases {
		c := &fx.Cases[i]
		entries := []marotte.Entry{*entryOf(searchItemsTurn, "", searchItemsTurn, marotte.EntryKindTurnOpen,
			marotte.EntryTurnOpen{N: 1, Prompt: &marotte.EntryPrompt{ID: "m-1", Text: "go"}})}
		for _, e := range c.Body {
			entries = append(entries, marotte.Entry{ID: e.ID, Turn: searchItemsTurn, Lane: e.Lane, Kind: e.Kind, Payload: e.Payload})
		}
		if c.LaterWake != nil {
			entries = append(entries,
				*entryOf("t-2", "", "t-2", marotte.EntryKindTurnOpen, marotte.EntryTurnOpen{N: 2}),
				marotte.Entry{ID: "t-2:wake", Turn: "t-2", Kind: marotte.EntryKindTurnWake, Payload: c.LaterWake})
		}
		s := &Store{hookStatusShown: func() bool { return !c.HooksHidden }}
		view := s.searchView(entries)
		turns := indexSearchTurns(view, map[string]struct{}{searchItemsTurn: {}})
		c.Items = []searchItemsWant{}
		for _, it := range searchItems(view, turns) {
			c.Items = append(c.Items, searchItemsWant{Kind: it.kind, Lane: it.lane, Anchor: it.entries[0], Entries: it.entries})
		}
	}
	pinGolden(t, "testdata/search_items.json", fx, "TestSearchItemsFixture", "search-items-contract.test.ts")
}
