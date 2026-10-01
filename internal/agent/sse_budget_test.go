package agent

// The cold-connect WIRE-BYTE gate. Every assertion here measures rec.Body after
// rt.handleSSE — the bytes that actually go out — never a count of events, because
// the defect being gated is a payload size and an event count cannot see it. Since
// the connect hook stopped carrying turn content (the live turn reaches the client
// through GET /api/chats/{id} and the live_turn digest subject), the gate is that
// no turn byte reaches the wire at connect however many busy chats there are, and
// that the two lists the handshake does carry stay within their caps.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
	"github.com/cplieger/marotte/internal/turnlog"
)

// Fixture sizes, all deliberately OVER the state measured on the live instance
// (18 open tabs, 6 concurrent runs, one 33.5 MB chat file): a gate sized
// at the observation would pass the moment the observation moved.
const (
	fixtureReasoningBytes  = 3 << 20
	fixtureContentBytes    = 1 << 20
	fixtureToolCalls       = 20
	fixtureToolOutputBytes = 100 << 10
	// fixtureDeltaChunks keeps the fill a STREAM rather than one delta, so the
	// per-block accumulation the wire performs is the accumulation measured.
	fixtureDeltaChunks = 8
	// fixturePendingPerms and fixturePendingRunAsks keep the non-snapshot
	// remainder non-zero, so the total assertion can still fail on a connect that
	// withholds every snapshot.
	fixturePendingPerms    = 2
	fixturePendingRunAsks  = 1
	fixtureBusyChats       = 6
	fixtureManyBusyChats   = 24
	fixtureFlatnessBudget  = 24 * 1024
	fixtureFrameSeparator  = "\n\n"
	fixtureConnectDeadline = 150 * time.Millisecond
)

// newBudgetRuntime builds the runtime the budget tests connect to, with the
// open-tab set WIRED: an unwired store makes every chat look open for a different
// reason, so a fixture that skipped it could not tell a tab filter that works from
// one that was never consulted.
func newBudgetRuntime(t *testing.T) *Runtime {
	t.Helper()
	dir := t.TempDir()
	st, err := tabs.NewStore(dir)
	if err != nil {
		t.Fatalf("tabs.NewStore(%q): %v", dir, err)
	}
	cs := newTestChatStore()
	br := newFakeBridge()
	rt := New(context.Background(), t.TempDir(), func() ACPBridge { return br }, cs,
		WithTabs(st), WithConfigDir(dir))
	cs.wire(rt)
	rt.mcpRegistry.SignalReady()
	t.Cleanup(func() { shutdownHub(t, rt) })
	return rt
}

// busyChatsWithHugeTurns opens n busy chats, each with a PROMPT-sourced open turn
// holding 3 MiB of reasoning, 1 MiB of text and 20 tool calls of 100 KiB (the bytes
// the flatness gate would see if a connect path ever read a turn again), one open
// chat TAB, and 2 pending permission asks plus 1 run ask across the set so the
// connect carries a real pending set beside the busy list.
func busyChatsWithHugeTurns(tb testing.TB, rt *Runtime, n int) []marotte.ChatID {
	tb.Helper()
	ids := make([]marotte.ChatID, 0, n)
	for i := range n {
		id := marotte.ChatID(fmt.Sprintf("c-budget-%02d", i))
		rt.bridge.mgr.orInsert(id)
		_, log := rt.stagePromptTurn(tb, id)
		fillTurn(tb, log, string(id))
		openBudgetChatTab(tb, rt, id)
		ids = append(ids, id)
	}
	seedPendingDecisions(tb, rt, ids)
	return ids
}

// fillTurn writes one turn's worth of content through the accumulator's own
// methods: reasoning and text as separate lanes of deltas, then the tool calls,
// each with its output already settled.
func fillTurn(tb testing.TB, log *turnlog.Turn, chatID string) {
	tb.Helper()
	ctx := tb.Context()
	reasoning := strings.Repeat("r", fixtureReasoningBytes/fixtureDeltaChunks)
	content := strings.Repeat("c", fixtureContentBytes/fixtureDeltaChunks)
	for range fixtureDeltaChunks {
		if _, err := log.ThinkingDelta(ctx, "", "say-"+chatID, reasoning); err != nil {
			tb.Fatalf("ThinkingDelta(%q): %v", chatID, err)
		}
	}
	for range fixtureDeltaChunks {
		if _, err := log.TextDelta(ctx, "", "say-"+chatID, content); err != nil {
			tb.Fatalf("TextDelta(%q): %v", chatID, err)
		}
	}
	output := strings.Repeat("o", fixtureToolOutputBytes)
	for i := range fixtureToolCalls {
		toolID := fmt.Sprintf("%s-tool-%02d", chatID, i)
		if _, err := log.ToolCall(ctx, "", &marotte.EntryToolCall{
			ID:     toolID,
			Title:  "budget fixture",
			Kind:   marotte.ToolKindExecute,
			Status: marotte.ToolCompleted,
			Output: output,
		}); err != nil {
			tb.Fatalf("ToolCall(%q): %v", toolID, err)
		}
	}
}

// openBudgetChatTab puts the chat in the server-owned open-tab set, which is the
// half of the fixture a tab filter reads.
func openBudgetChatTab(tb testing.TB, rt *Runtime, id marotte.ChatID) {
	tb.Helper()
	if _, _, _, err := rt.tabs.Open(tb.Context(), marotte.OpenTab{
		Kind: marotte.TabKindChat,
		Ref:  string(id),
	}); err != nil {
		tb.Fatalf("open chat tab for %q: %v", id, err)
	}
}

// seedPendingDecisions adds the unanswered asks a real reconnect replays beside the
// snapshots, so the measured total includes the part no snapshot cap can shrink.
func seedPendingDecisions(tb testing.TB, rt *Runtime, ids []marotte.ChatID) {
	tb.Helper()
	if len(ids) == 0 {
		tb.Fatal("no chats in the fixture, so there is nothing to attach a pending ask to")
	}
	for i := range fixturePendingPerms {
		requestID := int64(i + 1)
		rt.bus.pendingPerms.Add(requestID, marotte.NewEvent(
			marotte.EventPermissionNeeded, ids[i%len(ids)], marotte.PermissionNeededPayload{
				RequestID:  requestID,
				ToolCallID: fmt.Sprintf("perm-%d", requestID),
				Title:      "Run a command",
				Kind:       marotte.ToolKindExecute,
				Options: []marotte.PermissionOption{
					{OptionID: "allow", Name: "Allow", Kind: "allow_once"},
					{OptionID: "reject", Name: "Reject", Kind: "reject_once"},
				},
			},
		))
	}
	for i := range fixturePendingRunAsks {
		if !rt.runs.asks.Add(&runAsk{chatID: ids[0], payload: marotte.RunInputNeededPayload{
			WorkflowID: "wf-budget",
			AskID:      fmt.Sprintf("ask-%d", i+1),
			Question:   "Which branch should the step target?",
		}}) {
			tb.Fatalf("pending run ask %d was refused, so the fixture is short a frame", i+1)
		}
	}
}

// coldConnect drives one cold v3 connect and hands back the recorder, whose Body IS
// the measured wire bytes. The 150ms deadline bounds only the LIVE loop that
// follows: the connect replay is written synchronously from the OnConnect hook
// before it, so the measurement is deterministic rather than a race with the clock.
func coldConnect(t *testing.T, rt *Runtime, query string) *httptest.ResponseRecorder {
	t.Helper()
	return coldConnectAs(t, rt, false, query)
}

// coldConnectAs is coldConnect with the connect shape chosen: a legacy request sends
// no SSE-Wire header, which is how a v2 bundle presents.
func coldConnectAs(t *testing.T, rt *Runtime, legacy bool, query ...string) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), fixtureConnectDeadline)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events"+strings.Join(query, ""), nil).WithContext(ctx)
	if !legacy {
		req.Header.Set(wireHeader, "1")
	}
	rec := httptest.NewRecorder()
	rt.handleSSE(rec, req)
	return rec
}

func TestHandleSSE_ColdConnectIsFlatInTheNumberOfBusyChats(t *testing.T) {
	few := measureColdConnect(t, fixtureBusyChats)
	many := measureColdConnect(t, fixtureManyBusyChats)
	if delta := many - few; delta > fixtureFlatnessBudget {
		t.Errorf("cold connect grew by %d bytes from %d to %d busy chats, want at most %d: "+
			"the connect carries no turn content, so its size may move only by the busy-chat ids",
			delta, fixtureBusyChats, fixtureManyBusyChats, fixtureFlatnessBudget)
	}
}

// measureColdConnect returns the wire bytes of one cold v3 connect over n busy
// chats each holding a huge turn, and asserts no turn content reached the wire.
func measureColdConnect(t *testing.T, n int) int {
	t.Helper()
	rt := newBudgetRuntime(t)
	busyChatsWithHugeTurns(t, rt, n)
	body := coldConnect(t, rt, "").Body.String()
	if strings.Contains(body, strings.Repeat("r", 64)) || strings.Contains(body, strings.Repeat("c", 64)) {
		t.Fatalf("the connect carries turn content: %d bytes over %d busy chats", len(body), n)
	}
	if strings.Contains(body, string(marotte.EventType("turn_state"))) {
		t.Fatalf("the connect carries a turn_state frame; that channel is gone")
	}
	return len(body)
}

// TestHandleSSE_ConnectedCarriesEveryBusyChatUpToTheCap pins the one per-chat
// cost that survives: 37 bytes of id per busy chat, and BusyStated true while the
// list fits.
func TestHandleSSE_ConnectedCarriesEveryBusyChatUpToTheCap(t *testing.T) {
	rt := newBudgetRuntime(t)
	ids := busyChatsWithHugeTurns(t, rt, fixtureBusyChats)
	p := connectPayload(t, rt, "")
	if !p.BusyStated {
		t.Fatal("BusyStated = false under the cap")
	}
	if len(p.BusyChats) != len(ids) {
		t.Errorf("busy_chats carries %d ids, want %d", len(p.BusyChats), len(ids))
	}
}
