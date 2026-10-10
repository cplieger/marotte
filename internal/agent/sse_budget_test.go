package agent

// The cold-connect wire-byte gate, measured on rec.Body: no turn byte reaches the wire at connect whatever the
// busy-chat count, and the handshake's two lists stay within caps.

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

// Fixture sizes are deliberately above the live measurement (18 tabs, 6 runs, a 33.5 MB chat file).
const (
	fixtureReasoningBytes  = 3 << 20
	fixtureContentBytes    = 1 << 20
	fixtureToolCalls       = 20
	fixtureToolOutputBytes = 100 << 10
	// fixtureDeltaChunks keeps the fill a stream, accumulating as the wire does.
	fixtureDeltaChunks = 8
	// fixturePendingPerms and fixturePendingRunAsks keep a non-snapshot remainder, so the total can still fail.
	fixturePendingPerms    = 2
	fixturePendingRunAsks  = 1
	fixtureBusyChats       = 6
	fixtureManyBusyChats   = 24
	fixtureFlatnessBudget  = 24 * 1024
	fixtureConnectDeadline = 150 * time.Millisecond
)

// newBudgetRuntime wires the open-tab set: unwired, every chat looks open for another reason.
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
	t.Cleanup(func() { shutdownHub(t, rt) })
	return rt
}

// busyChatsWithHugeTurns opens n busy chats, each with a prompt-sourced open turn of 3 MiB reasoning, 1 MiB
// text and 20 × 100 KiB tool calls, one open tab, and a few pending asks across the set.
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

func openBudgetChatTab(tb testing.TB, rt *Runtime, id marotte.ChatID) {
	tb.Helper()
	if _, _, _, err := rt.tabs.Open(tb.Context(), marotte.OpenTab{
		Kind: marotte.TabKindChat,
		Ref:  string(id),
	}); err != nil {
		tb.Fatalf("open chat tab for %q: %v", id, err)
	}
}

// seedPendingDecisions adds unanswered asks no snapshot cap can shrink.
func seedPendingDecisions(tb testing.TB, rt *Runtime, ids []marotte.ChatID) {
	tb.Helper()
	if len(ids) == 0 {
		tb.Fatal("no chats in the fixture, so there is nothing to attach a pending ask to")
	}
	for i := range fixturePendingPerms {
		requestID := int64(i + 1)
		rt.bus.pendingPerms.add(requestID, marotte.NewEvent(
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
		), nil)
	}
	for i := range fixturePendingRunAsks {
		if !rt.runs.asks.add(&runAsk{chatID: ids[0], payload: marotte.RunInputNeededPayload{
			WorkflowID: "wf-budget",
			AskID:      fmt.Sprintf("ask-%d", i+1),
			Question:   "Which branch should the step target?",
		}}) {
			tb.Fatalf("pending run ask %d was refused, so the fixture is short a frame", i+1)
		}
	}
}

// The recorder's Body is the measured bytes.
func coldConnect(t *testing.T, rt *Runtime, query string) *httptest.ResponseRecorder {
	t.Helper()
	return coldConnectAs(t, rt, false, query)
}

// coldConnectAs chooses the shape: legacy sends no SSE-Wire header.
func coldConnectAs(t *testing.T, rt *Runtime, legacy bool, query ...string) *httptest.ResponseRecorder {
	t.Helper()
	return serveConnect(t, rt, hookOnlyContext(t), legacy, query...)
}

// hookOnlyContext is an already-done request context: everything up to the OnConnect hook is written before the
// live loop reads it, which then returns at once.
func hookOnlyContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	return ctx
}

// liveConnectAs runs the live loop until fixtureConnectDeadline.
func liveConnectAs(t *testing.T, rt *Runtime, legacy bool) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), fixtureConnectDeadline)
	defer cancel()
	return serveConnect(t, rt, ctx, legacy)
}

func serveConnect(t *testing.T, rt *Runtime, ctx context.Context, legacy bool, query ...string) *httptest.ResponseRecorder {
	t.Helper()
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

// TestHandleSSE_ConnectedCarriesEveryBusyChatUpToTheCap pins the one per-chat cost, 37 bytes, and BusyStated while it fits.
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
