package command

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
)

// refusedImpact is the impact a close_impact_changed refusal carries, failing the test for any other outcome.
func refusedImpact(t *testing.T, err error) *marotte.CloseImpact {
	t.Helper()
	status, reason, _ := StatusOf(err)
	if status != http.StatusConflict || reason != reasonCloseImpactChanged {
		t.Fatalf("outcome = %d %q (%v), want 409 %q", status, reason, err, reasonCloseImpactChanged)
	}
	se, _ := errors.AsType[*statusError](err)
	if se.impact == nil {
		t.Fatal("the refusal carries no impact")
	}
	return se.impact
}

func chatWork(impact *marotte.CloseImpact, ref marotte.ChatID) (marotte.ChatWork, bool) {
	for _, w := range impact.Chats {
		if w.Ref == string(ref) {
			return w, true
		}
	}
	return marotte.ChatWork{}, false
}

// A parent chat tab with one child chat tab under it, both idle on the server unless a row says otherwise.
func TestCloseTab_AnImpactTheDialogDidNotCoverClosesNothing(t *testing.T) {
	type row struct {
		name string
		// server and page are keyed "parent" and "child".
		server   map[string]CloseWork
		page     map[string]marotte.ChatWork
		wantTurn []string // the refusal's turn ids for the chat named in wantChat
		wantChat string
	}
	rows := []row{
		{name: "a chat attached under the tab", page: map[string]marotte.ChatWork{"parent": {}}},
		{
			name:   "an idle chat now with a turn",
			server: map[string]CloseWork{"child": {Turns: []CloseTurn{{ID: "t-1", Admission: "res:3"}}}},
			page:   map[string]marotte.ChatWork{"parent": {}, "child": {}}, wantChat: "child", wantTurn: []string{"t-1"},
		},
		{
			name:   "another device's prompt admitted with no turn yet",
			server: map[string]CloseWork{"parent": {Turns: []CloseTurn{{MessageID: "m-other", Admission: "res:7"}}}},
			page:   map[string]marotte.ChatWork{"parent": {}, "child": {}}, wantChat: "parent", wantTurn: []string{"res:7"},
		},
		{
			name:   "a wire turn kiro-cli started",
			server: map[string]CloseWork{"parent": {Turns: []CloseTurn{{ID: "t-wire"}}}},
			page:   map[string]marotte.ChatWork{"parent": {}, "child": {}}, wantChat: "parent", wantTurn: []string{"t-wire"},
		},
		{
			name:   "an ask",
			server: map[string]CloseWork{"parent": {Asks: []string{"5"}}},
			page:   map[string]marotte.ChatWork{"parent": {}, "child": {}},
		},
		{
			name:   "a new run",
			server: map[string]CloseWork{"parent": {Runs: []string{"wf-1", "wf-2"}}},
			page:   map[string]marotte.ChatWork{"parent": {Runs: []string{"wf-1"}}, "child": {}},
		},
		{
			name:   "a new unread steer",
			server: map[string]CloseWork{"child": {Steers: []string{"s-1"}}},
			page:   map[string]marotte.ChatWork{"parent": {}, "child": {}},
		},
		{
			name:   "a new queued follow-up",
			server: map[string]CloseWork{"child": {Queued: []string{"q-1"}}},
			page:   map[string]marotte.ChatWork{"parent": {}, "child": {}},
		},
		{
			name:     "a reply that ended and another device's new reply",
			server:   map[string]CloseWork{"parent": {Turns: []CloseTurn{{ID: "t-new", MessageID: "m-new", Admission: "res:9"}}}},
			page:     map[string]marotte.ChatWork{"parent": {Turns: []string{"t-old", "m-old"}}, "child": {}},
			wantChat: "parent", wantTurn: []string{"t-new"},
		},
		{
			name:   "a steer read and another added",
			server: map[string]CloseWork{"parent": {Steers: []string{"s-2"}}},
			page:   map[string]marotte.ChatWork{"parent": {Steers: []string{"s-1"}}, "child": {}},
		},
		{
			name:   "an ask answered and another raised",
			server: map[string]CloseWork{"parent": {Asks: []string{"2"}}},
			page:   map[string]marotte.ChatWork{"parent": {Asks: []string{"1"}}, "child": {}},
		},
		{
			name:   "a follow-up sent and another queued",
			server: map[string]CloseWork{"parent": {Queued: []string{"q-2"}}},
			page:   map[string]marotte.ChatWork{"parent": {Queued: []string{"q-1"}}, "child": {}},
		},
		{
			name:   "a drain resending more steers than the dialog named",
			server: map[string]CloseWork{"parent": {Turns: []CloseTurn{{ID: "t-d", MessageID: "m-d", Resends: []string{"s-1", "s-2"}}}}},
			page:   map[string]marotte.ChatWork{"parent": {Steers: []string{"s-1"}}, "child": {}},
		},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			h := newCloseHost(t)
			parent, parentTab := h.chatUnder(t, "op-p", "")
			child, _ := h.chatUnder(t, "op-c", parentTab)
			ids := map[string]marotte.ChatID{"parent": parent, "child": child}
			for k, w := range r.server {
				h.impact.work[ids[k]] = w
			}
			page := &marotte.CloseImpact{}
			for k, w := range r.page {
				w.Ref = string(ids[k])
				page.Chats = append(page.Chats, w)
			}
			framesBefore := len(h.bus.frames(t))

			_, _, err := h.mem.closeTab(t.Context(), parentTab, "op-close", true, page)

			impact := refusedImpact(t, err)
			for k, chatID := range ids {
				w, ok := chatWork(impact, chatID)
				if !ok {
					t.Errorf("the refusal's impact has no entry for the %s chat", k)
					continue
				}
				want := r.server[k]
				if !slices.Equal(w.Asks, nonNil(want.Asks)) || !slices.Equal(w.Runs, nonNil(want.Runs)) ||
					!slices.Equal(w.Steers, nonNil(want.Steers)) || !slices.Equal(w.Queued, nonNil(want.Queued)) {
					t.Errorf("the refusal's %s entry = %+v, want the server's %+v", k, w, want)
				}
				if k == r.wantChat && !slices.Equal(w.Turns, r.wantTurn) {
					t.Errorf("the refusal's %s turns = %v, want %v", k, w.Turns, r.wantTurn)
				}
			}
			if got := len(h.bus.frames(t)); got != framesBefore {
				t.Errorf("a refused close emitted %d tabs_changed frames, want none", got-framesBefore)
			}
			if !h.mem.tabOpen(parentTab) {
				t.Error("a refused close closed the tab")
			}
			if got := h.holds.chatsWith("commit"); len(got) != 0 {
				t.Errorf("a refused close committed holds %v, want none", got)
			}
			if got := len(h.holds.chatsWith("release")); got != 2 {
				t.Errorf("a refused close released %d holds, want both chats' (%v)", got, h.holds.snapshot())
			}
			if jobs := h.queue.snapshot(); len(jobs) != 0 {
				t.Errorf("a refused close enqueued %d teardown jobs, want none", len(jobs))
			}
		})
	}
}

func TestCloseTab_ARefusedCloseReleasesItsHoldsUncommitted(t *testing.T) {
	h := newCloseHost(t)
	chatID, tab := h.chatUnder(t, "op-a", "")
	h.impact.work[chatID] = CloseWork{Turns: []CloseTurn{{ID: "t-1"}}}

	_, _, err := h.mem.closeTab(t.Context(), tab, "op-close", true, &marotte.CloseImpact{Chats: []marotte.ChatWork{{Ref: string(chatID)}}})

	refusedImpact(t, err)
	want := []string{"hold " + string(chatID), "release " + string(chatID)}
	if got := h.holds.snapshot(); !slices.Equal(got, want) {
		t.Errorf("hold events = %v, want %v", got, want)
	}
}

func TestCloseTab_ACoveredImpactCloses(t *testing.T) {
	rows := []struct {
		name   string
		server CloseWork
		page   marotte.ChatWork
	}{
		{
			name: "exact", server: CloseWork{Turns: []CloseTurn{{ID: "t-1"}}, Asks: []string{"4"}, Runs: []string{"wf"}, Steers: []string{"s"}, Queued: []string{"q"}},
			page: marotte.ChatWork{Turns: []string{"t-1"}, Asks: []string{"4"}, Runs: []string{"wf"}, Steers: []string{"s"}, Queued: []string{"q"}},
		},
		{
			name: "over-named", server: CloseWork{},
			page: marotte.ChatWork{Turns: []string{"t-gone"}, Asks: []string{"1"}, Steers: []string{"s-read"}},
		},
		{
			name:   "a queued row the dialog named became the drain's turn",
			server: CloseWork{Turns: []CloseTurn{{ID: "t-d", MessageID: "q-1", Admission: "res:2"}}}, page: marotte.ChatWork{Queued: []string{"q-1"}},
		},
		{
			name:   "unread steers the dialog named became the drain's resend turn",
			server: CloseWork{Turns: []CloseTurn{{ID: "t-r", MessageID: "m-r", Resends: []string{"s-1", "s-2"}}}},
			page:   marotte.ChatWork{Steers: []string{"s-1", "s-2"}},
		},
		{
			name:   "the page's own send named by its message id before its turn opened",
			server: CloseWork{Turns: []CloseTurn{{MessageID: "m-1", Admission: "res:5"}}}, page: marotte.ChatWork{Turns: []string{"m-1"}},
		},
		{
			name:   "the page's own send named by its message id after its turn opened",
			server: CloseWork{Turns: []CloseTurn{{ID: "t-5", MessageID: "m-1", Admission: "res:5"}}}, page: marotte.ChatWork{Turns: []string{"m-1"}},
		},
		{
			name:   "an admission id an earlier refusal named whose turn has since opened",
			server: CloseWork{Turns: []CloseTurn{{ID: "t-6", Admission: "res:6"}}}, page: marotte.ChatWork{Turns: []string{"res:6"}},
		},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			h := newCloseHost(t)
			chatID, tab := h.chatUnder(t, "op-a", "")
			h.impact.work[chatID] = r.server
			r.page.Ref = string(chatID)

			closed, _, err := h.mem.closeTab(t.Context(), tab, "op-close", true, &marotte.CloseImpact{Chats: []marotte.ChatWork{r.page}})
			if err != nil {
				t.Fatalf("closeTab = %v, want the covered close to commit", err)
			}
			if !slices.Equal(subjectIDs(closed), []string{tab}) {
				t.Errorf("closed = %v, want [%s]", subjectIDs(closed), tab)
			}
			if got := h.holds.chatsWith("commit"); !slices.Equal(got, []string{string(chatID)}) {
				t.Errorf("committed holds = %v, want the chat's", got)
			}
			if jobs := h.queue.snapshot(); len(jobs) != 1 || jobs[0].chatID != chatID {
				t.Errorf("enqueued jobs = %+v, want one for %s", jobs, chatID)
			}
		})
	}
}

// An API caller sends no impact, and its close is not checked.
func TestCloseTab_NoImpactIsUnchecked(t *testing.T) {
	h := newCloseHost(t)
	chatID, tab := h.chatUnder(t, "op-a", "")
	h.impact.work[chatID] = CloseWork{Turns: []CloseTurn{{ID: "t-1"}}, Runs: []string{"wf"}}

	if _, _, err := h.mem.closeTab(t.Context(), tab, "op-close", true, nil); err != nil {
		t.Fatalf("closeTab with no impact = %v, want it to close", err)
	}
	if n := h.impact.readCount(); n != 0 {
		t.Errorf("an unchecked close read the impact %d times, want none", n)
	}
	if h.mem.tabOpen(tab) {
		t.Error("the tab is still open")
	}
}

func closeTabCmd(t *testing.T, payload any) *marotte.ClientCommand {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return &marotte.ClientCommand{Type: marotte.CmdCloseTab, Payload: raw}
}

func workIDs(n int, prefix string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = prefix + "-" + strconv.Itoa(i)
	}
	return out
}

// invalidImpacts are the impacts the confirmation's validation refuses, shared by close_tab and delete_chat.
func invalidImpacts() map[string]*marotte.CloseImpact {
	tooManyChats := &marotte.CloseImpact{}
	for i := range tabs.MaxOpenTabs + 1 {
		tooManyChats.Chats = append(tooManyChats.Chats, marotte.ChatWork{Ref: "c-" + strconv.Itoa(i)})
	}
	one := func(w marotte.ChatWork) *marotte.CloseImpact {
		w.Ref = "c-1"
		return &marotte.CloseImpact{Chats: []marotte.ChatWork{w}}
	}
	return map[string]*marotte.CloseImpact{
		"too many chats":        tooManyChats,
		"a bad ref":             {Chats: []marotte.ChatWork{{Ref: "../c"}}},
		"an empty ref":          {Chats: []marotte.ChatWork{{Ref: ""}}},
		"a duplicate ref":       {Chats: []marotte.ChatWork{{Ref: "c-1"}, {Ref: "c-1"}}},
		"too many turns":        one(marotte.ChatWork{Turns: workIDs(maxImpactTurns+1, "t")}),
		"too many asks":         one(marotte.ChatWork{Asks: workIDs(maxImpactAsks+1, "a")}),
		"too many runs":         one(marotte.ChatWork{Runs: workIDs(maxImpactRuns+1, "w")}),
		"too many steers":       one(marotte.ChatWork{Steers: workIDs(maxImpactSteers+1, "s")}),
		"too many queued":       one(marotte.ChatWork{Queued: workIDs(maxImpactQueued+1, "q")}),
		"a disallowed byte":     one(marotte.ChatWork{Turns: []string{"t 1"}}),
		"a non-ASCII byte":      one(marotte.ChatWork{Steers: []string{"sé"}}),
		"an empty entry":        one(marotte.ChatWork{Asks: []string{""}}),
		"a 129-byte entry":      one(marotte.ChatWork{Queued: []string{strings.Repeat("q", 129)}}),
		"a slash in an entry":   one(marotte.ChatWork{Runs: []string{"wf/1"}}),
		"a newline in an entry": one(marotte.ChatWork{Turns: []string{"t\n"}}),
	}
}

func TestCmdCloseTab_RefusesAnInvalidImpact(t *testing.T) {
	for name, impact := range invalidImpacts() {
		t.Run(name, func(t *testing.T) {
			h := newCloseHost(t)
			_, tab := h.chatUnder(t, "op-a", "")

			_, err := cmdCloseTab(t.Context(), h.mem, closeTabCmd(t, marotte.CloseTabCommand{ID: tab, Impact: impact}))

			if statusOf(err) != http.StatusBadRequest {
				t.Errorf("status = %d (%v), want 400", statusOf(err), err)
			}
			if !h.mem.tabOpen(tab) {
				t.Error("an invalid impact closed the tab")
			}
		})
	}
}

// The limits themselves are accepted: an entry of 128 bytes, the list caps, ids with every allowed byte.
func TestCmdCloseTab_AcceptsAnImpactAtItsLimits(t *testing.T) {
	h := newCloseHost(t)
	chatID, tab := h.chatUnder(t, "op-a", "")
	impact := &marotte.CloseImpact{Chats: []marotte.ChatWork{{
		Ref:    string(chatID),
		Turns:  append(workIDs(maxImpactTurns-2, "t"), "res:12", "Az09-_.:"),
		Asks:   workIDs(maxImpactAsks, "a"),
		Runs:   workIDs(maxImpactRuns, "w"),
		Steers: workIDs(maxImpactSteers, "s"),
		Queued: append(workIDs(maxImpactQueued-1, "q"), strings.Repeat("q", maxImpactWorkIDLen)),
	}}}

	if _, err := cmdCloseTab(t.Context(), h.mem, closeTabCmd(t, marotte.CloseTabCommand{ID: tab, Impact: impact})); err != nil {
		t.Fatalf("cmdCloseTab at the limits = %v, want it to close", err)
	}
}

// C's tab holds D's chat tab; deleting C from History closes D's tab, so the delete's confirm names D's work.
func newDeleteHost(t *testing.T) (h *closeHost, c, d marotte.ChatID) {
	t.Helper()
	h = newCloseHost(t)
	c, cTab := h.chatUnder(t, "op-c", "")
	d, _ = h.chatUnder(t, "op-d", cTab)
	return h, c, d
}

func TestDeleteChatAndCloseTabs_AnUncoveredDescendantDeletesNothing(t *testing.T) {
	h, c, d := newDeleteHost(t)
	page := &marotte.CloseImpact{Chats: []marotte.ChatWork{{Ref: string(d)}}}
	h.impact.work[d] = CloseWork{Turns: []CloseTurn{{ID: "t-d"}}}
	framesBefore := len(h.bus.frames(t))

	err := h.mem.DeleteChatAndCloseTabs(t.Context(), c, "op-del", page)

	impact := refusedImpact(t, err)
	if w, ok := chatWork(impact, d); !ok || !slices.Equal(w.Turns, []string{"t-d"}) {
		t.Errorf("the refusal's impact = %+v, want D's new turn named", impact)
	}
	if _, ok := chatWork(impact, c); ok {
		t.Error("the refusal names the deleted chat's own work, which is never compared")
	}
	if _, ok := h.store.Get(t.Context(), c); !ok {
		t.Error("a refused delete removed the record")
	}
	if got := len(h.bus.frames(t)); got != framesBefore {
		t.Errorf("a refused delete emitted %d tabs_changed frames, want none", got-framesBefore)
	}
	if got := h.holds.chatsWith("commit"); len(got) != 0 {
		t.Errorf("a refused delete committed holds %v, want none", got)
	}
	if got := h.holds.chatsWith("release"); len(got) != 2 {
		t.Errorf("a refused delete released holds %v, want C's and D's", got)
	}
	if jobs := h.queue.snapshot(); len(jobs) != 0 {
		t.Errorf("a refused delete enqueued %d jobs, want none", len(jobs))
	}
}

func TestDeleteChatAndCloseTabs_ACoveredDescendantDeletes(t *testing.T) {
	h, c, d := newDeleteHost(t)
	h.impact.work[d] = CloseWork{Turns: []CloseTurn{{ID: "t-d"}}}
	page := &marotte.CloseImpact{Chats: []marotte.ChatWork{{Ref: string(d), Turns: []string{"t-d"}}}}

	if err := h.mem.DeleteChatAndCloseTabs(t.Context(), c, "op-del", page); err != nil {
		t.Fatalf("DeleteChatAndCloseTabs = %v, want the covered delete to proceed", err)
	}
	if _, ok := h.store.Get(t.Context(), c); ok {
		t.Error("the record survived a covered delete")
	}
	var grades []string
	for _, j := range h.queue.snapshot() {
		grades = append(grades, string(j.chatID)+":"+j.grade.String())
	}
	if want := []string{string(c) + ":delete", string(d) + ":close"}; !slices.Equal(grades, want) {
		t.Errorf("enqueued = %v, want %v", grades, want)
	}
}

func TestDeleteChatAndCloseTabs_TheDeletedChatsOwnWorkIsNotCompared(t *testing.T) {
	h, c, d := newDeleteHost(t)
	h.impact.work[c] = CloseWork{Turns: []CloseTurn{{ID: "t-c"}}, Runs: []string{"wf-c"}}
	page := &marotte.CloseImpact{Chats: []marotte.ChatWork{{Ref: string(d)}, {Ref: string(c)}}}

	if err := h.mem.DeleteChatAndCloseTabs(t.Context(), c, "op-del", page); err != nil {
		t.Fatalf("DeleteChatAndCloseTabs = %v, want the delete to ignore the deleted chat's own reply", err)
	}
	if _, ok := h.store.Get(t.Context(), c); ok {
		t.Error("the record survived")
	}
}

func deleteChatCmd(t *testing.T, chatID marotte.ChatID, payload string) *marotte.ClientCommand {
	t.Helper()
	return &marotte.ClientCommand{Type: marotte.CmdDeleteChat, ChatID: chatID, Payload: json.RawMessage(payload)}
}

func TestCmdDeleteChat_RefusesAnInvalidImpact(t *testing.T) {
	for name, impact := range invalidImpacts() {
		t.Run(name, func(t *testing.T) {
			h, c, _ := newDeleteHost(t)
			raw, err := json.Marshal(marotte.CloseTabCommand{Impact: impact})
			if err != nil {
				t.Fatal(err)
			}

			_, err = cmdDeleteChat(t.Context(), h.mem, deleteChatCmd(t, c, string(raw)))

			if statusOf(err) != http.StatusBadRequest {
				t.Errorf("status = %d (%v), want 400", statusOf(err), err)
			}
			if _, ok := h.store.Get(t.Context(), c); !ok {
				t.Error("an invalid impact deleted the chat")
			}
		})
	}
}

// A payload that does not decode is ignored, as it always was: the delete runs unchecked.
func TestCmdDeleteChat_IgnoresAnUnreadablePayload(t *testing.T) {
	h, c, d := newDeleteHost(t)
	h.impact.work[d] = CloseWork{Turns: []CloseTurn{{ID: "t-d"}}}

	if _, err := cmdDeleteChat(t.Context(), h.mem, deleteChatCmd(t, c, `{"impact":"not an impact"}`)); err != nil {
		t.Fatalf("cmdDeleteChat with an unreadable payload = %v, want the delete unchecked", err)
	}
	if _, ok := h.store.Get(t.Context(), c); ok {
		t.Error("the record survived")
	}
}
