package agent

// The reported reconnect sequence at the seam: a mid-turn reopen gets a content-free connect naming the chat
// busy, so the transcript GET alone must return the visible reply. Both halves in one test, since the
// content-free connect is what makes the GET load-bearing.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
)

const replayGapChat marotte.ChatID = "c-replay-gap"

const (
	replayGapReasoning = "weighing the two shapes before answering"
	replayGapText      = "here is the first half of the reply"
)

// transcriptPage is the single-chat GET's response spelled by hand: its field names are the client contract,
// so importing the server's type would test it against itself.
type transcriptPage struct {
	Entries []struct {
		Kind string `json:"kind"`
	} `json:"entries"`
	OpenEntries []struct {
		Turn string `json:"turn"`
		ID   string `json:"id"`
		Kind string `json:"kind"`
		Text string `json:"text"`
		N    uint64 `json:"n"`
	} `json:"open_entries"`
	Live bool `json:"live"`
}

// newReplayGapRuntime wires a real chat store and routes: the defect is a disagreement between two shipped
// surfaces. The tab store is wired as in newBudgetRuntime.
func newReplayGapRuntime(t *testing.T) (*Runtime, *http.ServeMux) {
	t.Helper()
	dir := t.TempDir()
	ts, err := tabs.NewStore(dir)
	if err != nil {
		t.Fatalf("tabs.NewStore(%q): %v", dir, err)
	}
	var rt *Runtime
	cs, err := chat.NewStore(t.TempDir(),
		chat.WithLiveTurn(func(id marotte.ChatID) bool { return rt.TurnLive(id) }),
		chat.WithOpenTurns(func(id marotte.ChatID) []chat.OpenTurnTail { return rt.OpenTurns(id) }),
	)
	if err != nil {
		t.Fatalf("chat.NewStore: %v", err)
	}
	rt = New(t.Context(), t.TempDir(), func() ACPBridge { return newFakeBridge() }, cs,
		WithTabs(ts), WithConfigDir(dir))
	t.Cleanup(func() { shutdownHub(t, rt) })
	mux := http.NewServeMux()
	cs.RegisterRoutes(mux)
	return rt, mux
}

// openReplayGapTurn fills the turn as the wire does: a thinking delta, then a text delta in the same lane, so
// reasoning is sealed and text still coalescing.
func openReplayGapTurn(t *testing.T, rt *Runtime) {
	t.Helper()
	rt.bridge.mgr.orInsert(replayGapChat)
	_, log := rt.stagePromptTurn(t, replayGapChat)
	if _, err := log.ThinkingDelta(t.Context(), "", "say-1", replayGapReasoning); err != nil {
		t.Fatalf("ThinkingDelta: %v", err)
	}
	if _, err := log.TextDelta(t.Context(), "", "say-1", replayGapText); err != nil {
		t.Fatalf("TextDelta: %v", err)
	}
	openBudgetChatTab(t, rt, replayGapChat)
}

// The page rules are internal/chat's.
func getTranscript(t *testing.T, mux *http.ServeMux, id marotte.ChatID) transcriptPage {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/chats/"+string(id), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/chats/%s = %d, want 200; body = %s", id, rec.Code, rec.Body.String())
	}
	var page transcriptPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode transcript page: %v; body = %s", err, rec.Body.String())
	}
	return page
}

func (p transcriptPage) kinds() []string {
	kinds := make([]string, 0, len(p.Entries))
	for _, e := range p.Entries {
		kinds = append(kinds, e.Kind)
	}
	return kinds
}

// TestReconnectMidTurn_TheTranscriptGETCarriesTheInFlightTurn — a reloaded view must not show the prompt over an empty body.
func TestReconnectMidTurn_TheTranscriptGETCarriesTheInFlightTurn(t *testing.T) {
	rt, mux := newReplayGapRuntime(t)
	openReplayGapTurn(t, rt)

	// The reloaded window's connect carries no turn content.
	body := coldConnect(t, rt, "").Body.String()
	if strings.Contains(body, string(marotte.EventType("turn_state"))) || strings.Contains(body, replayGapText) {
		t.Fatalf("the connect carries turn content; the GET assertions below would pass without it: %q", body)
	}
	if p := connectPayload(t, rt, ""); !p.BusyStated || !busySetOf(p)[replayGapChat] {
		t.Fatal("the fixture's chat is not busy at connect, so nothing below is measuring the reconnect this test is about")
	}

	page := getTranscript(t, mux, replayGapChat)

	if !page.Live {
		t.Errorf("live = false while a turn is in flight, so the client cannot tell this open tail from a torn one")
	}
	if got, want := page.kinds(), []string{"turn_open", "thinking"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("entries kinds = %v, want %v: the sealed reasoning rides the window and the open text does not", got, want)
	}
	if len(page.OpenEntries) != 1 {
		t.Fatalf("the transcript GET carries %d open entries while a text delta is coalescing, want 1: a client "+
			"that reconnected has no other channel, so the reply already on screen is unreachable",
			len(page.OpenEntries))
	}
	open := page.OpenEntries[0]
	if open.Kind != "text" || open.Text != replayGapText {
		t.Errorf("open_entries[0] = %s %q, want text %q", open.Kind, open.Text, replayGapText)
	}
	if open.ID == "" || open.Turn == "" {
		t.Errorf("open_entries[0] carries id %q turn %q: the client keys the deltas that follow on both, "+
			"so an unnamed tail is one it cannot extend", open.ID, open.Turn)
	}
	if open.N != 1 {
		t.Errorf("open_entries[0].n = %d, want 1 after one delta: the client drops deltas at or below n", open.N)
	}
}

// TestTranscriptGET_AnOpenTurnThatProducedNothingCarriesNoTail pins that `live` says a turn runs; an empty open entry would mount a blank row.
func TestTranscriptGET_AnOpenTurnThatProducedNothingCarriesNoTail(t *testing.T) {
	rt, mux := newReplayGapRuntime(t)
	const quiet marotte.ChatID = "c-quiet"
	rt.bridge.mgr.orInsert(quiet)
	rt.stagePromptTurn(t, quiet)

	page := getTranscript(t, mux, quiet)

	if !page.Live {
		t.Fatalf("live = false on the fixture's open turn, so nothing below measures the empty case")
	}
	if len(page.OpenEntries) != 0 {
		t.Errorf("open_entries = %+v for a turn that has produced nothing, want none", page.OpenEntries)
	}
	if got, want := page.kinds(), []string{"turn_open"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("entries kinds = %v, want %v", got, want)
	}
}

// TestTranscriptGET_AnIdleChatIsNotLive pins that or a reader waits for deltas that never come.
func TestTranscriptGET_AnIdleChatIsNotLive(t *testing.T) {
	rt, mux := newReplayGapRuntime(t)
	const idle marotte.ChatID = "c-idle"
	rt.bridge.mgr.orInsert(idle)
	id, _ := rt.stagePromptTurn(t, idle)
	endTurn(t, rt, idle, id)

	page := getTranscript(t, mux, idle)

	if page.Live {
		t.Error("live = true after the turn closed, so the client keeps a running indicator on a finished turn")
	}
	if len(page.OpenEntries) != 0 {
		t.Errorf("open_entries = %+v after the turn closed, want none", page.OpenEntries)
	}
	if got, want := page.kinds(), []string{"turn_open", "turn_close"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("entries kinds = %v, want %v", got, want)
	}
}
