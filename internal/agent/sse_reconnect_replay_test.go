package agent

// The reported reconnect sequence, at the seam rather than through a browser: a reader
// whose window closes mid-turn and re-opens gets a connect that names the chat busy
// and carries no turn content, so the transcript GET is the only channel that hands
// back the reply already on screen.
//
// Both halves are asserted in one test on purpose: the content-free connect is what
// makes the GET load-bearing, so asserting the GET alone would pass just as well on a
// build where the connect happened to carry the turn after all.

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

// transcriptPage is the single-chat GET's response as this test READS it, spelled by
// hand rather than taken from the production struct: the field names ARE the contract
// the client decodes, so a test importing the server's own type would assert it against
// itself and pass through a rename.
type transcriptPage struct {
	Entries []struct {
		Kind    string          `json:"kind"`
		Payload json.RawMessage `json:"payload"`
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

// newReplayGapRuntime wires the runtime to a REAL chat store plus its real routes,
// because the defect IS a disagreement between two surfaces (what the connect
// carries and what the transcript GET carries), so both have to be the shipped ones.
// The tab store is wired for newBudgetRuntime's reason: an unwired one makes every
// chat look open for a different reason.
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
	rt.mcpRegistry.SignalReady()
	t.Cleanup(func() { shutdownHub(t, rt) })
	mux := http.NewServeMux()
	cs.RegisterRoutes(mux)
	return rt, mux
}

// openReplayGapTurn opens the turn the reader watched and fills it the way the wire
// does: a thinking delta, then a text delta in the same lane, so the reasoning is
// sealed and the text is the entry still coalescing when the GET reads it.
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

// getTranscript drives the real route and decodes the newest page. The store's own
// rules about the page (the older-page withhold, the lock order, the stamps) are
// internal/chat's and are tested there; this file's subject is whether the open
// turn reaches the wire at all.
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

// TestReconnectMidTurn_TheTranscriptGETCarriesTheInFlightTurn is the reported defect.
// Before the fix the connect carried no turn and the GET carried no open tail either,
// so the reloaded view showed the prompt over an empty body.
func TestReconnectMidTurn_TheTranscriptGETCarriesTheInFlightTurn(t *testing.T) {
	rt, mux := newReplayGapRuntime(t)
	openReplayGapTurn(t, rt)

	// The connect the reloaded window makes carries no turn content at all, which is
	// what makes the GET the only channel for the in-flight reply.
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

// TestTranscriptGET_AnOpenTurnThatProducedNothingCarriesNoTail is the boundary between
// "a turn is running" and "there is a reply to hand back". `live` already carries the
// first, and an open entry with no text would mount a blank row under the prompt.
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

// TestTranscriptGET_AnIdleChatIsNotLive is the other direction: a chat whose turn
// was taken must answer no, or a reader keeps waiting for deltas that never come.
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
