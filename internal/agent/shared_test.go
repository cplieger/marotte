package agent

// Utility helpers for agent tests: newTestHub constructor, postCmd helper,
// event inspection helpers, and message builders.

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
	"github.com/cplieger/sse"
)

// --- Runtime construction helpers ---

// newTestHub roots the Runtime's lifetime at context.Background(), so a test that wants
// it torn down calls Shutdown.
func newTestHub() (*Runtime, *testChatStore, *fakeBridge) {
	return newTestHubIn("/tmp/work")
}

// newTestHubUnready is newTestHub with MCP readiness WITHHELD, so a prompt parks in
// WaitForReady's 30s wait. That is the widest part of the window between BeginPromptCall
// and StartTurn, which is the one a cancel has to be driven into.
func newTestHubUnready() (*Runtime, *testChatStore, *fakeBridge) {
	return buildTestHub("/tmp/work", false)
}

// newTestHubIn builds a runtime rooted at workDir. Use it rather than reassigning
// h.lifecycle.workDir afterwards: the workspace paths are read once at wiring time, so a
// post-construction mutation configures something the wiring has already read.
func newTestHubIn(workDir string) (*Runtime, *testChatStore, *fakeBridge) {
	return buildTestHub(workDir, true)
}

// buildTestHub is the ONE wiring sequence every test runtime is built by, and it exists
// because that sequence is ORDER-SENSITIVE and was written out twice: cs.Bus can only be
// set once New has returned the runtime that serves as the bus, and readiness can only be
// signalled once the registry exists. Two copies meant a step added to one silently
// skipped the other, and the readiness-withholding copy was the one no reader thinks to
// check. Readiness is the only axis they differed on, so it is the only parameter.
func buildTestHub(workDir string, mcpReady bool) (*Runtime, *testChatStore, *fakeBridge) {
	cs := newTestChatStore()
	br := newFakeBridge()
	h := New(context.Background(), workDir, func() ACPBridge { return br }, cs)
	// Park the cancel-retry ladder past any test run: its re-attempts ride untracked
	// timers that outlive the test. A test that needs it to fire lowers it itself.
	h.runs.cancelRetryBase = time.Hour
	cs.wire(h)
	if mcpReady {
		// Signal MCP readiness immediately so tests don't wait 30 seconds.
		h.mcpRegistry.SignalReady()
	}
	return h, cs, br
}

// shutdownHub roots its budget at context.Background() rather than t.Context() because
// callers reach for it from t.Cleanup, where t.Context() is already cancelled. 30s sits
// above anything a unit test needs and below go test's own timeout, so an expiry is a
// diagnostic rather than a flake.
func shutdownHub(t *testing.T, h *Runtime) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		t.Errorf("runtime shutdown: %v", err)
	}
}

func (rt *Runtime) handleCommand(w http.ResponseWriter, r *http.Request) {
	rt.dispatcher.ServeHTTP(w, r)
}

func postCmd(t *testing.T, h *Runtime, cmd marotte.ClientCommand) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(cmd)
	req := httptest.NewRequest(http.MethodPost, "/api/command", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	h.handleCommand(rec, req)
	return rec
}

// --- Event inspection helpers ---

// bufferedSince is the test-side offset filter over the hub Snapshot, whose own inspection
// surface is a parameterless snapshot.
func bufferedSince(h *Runtime, sinceID uint64) []sse.ReplayEvent {
	var out []sse.ReplayEvent
	for _, e := range h.bus.fanout.Snapshot() {
		if e.Offset > sinceID {
			out = append(out, e)
		}
	}
	return out
}

// extractTypes returns the events' types in order, for asserting an emit sequence.
func extractTypes(t *testing.T, events []sse.ReplayEvent) []string {
	t.Helper()
	out := make([]string, 0, len(events))
	for _, e := range events {
		var msg marotte.ServerEvent
		if err := json.Unmarshal(e.Event.Data, &msg); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		out = append(out, string(msg.Type))
	}
	return out
}

// errorPayloadsSince returns the ErrorPayload of every `error` event buffered after
// sinceID. It exists because ServerEvent.Payload is an `any`, so reading a typed
// payload back off the wire is a two-step round-trip every caller otherwise writes out
// again; a non-error event and an undecodable payload are SKIPPED rather than fatal,
// since the buffer legitimately carries unrelated frames.
func errorPayloadsSince(t *testing.T, h *Runtime, sinceID uint64) []marotte.ErrorPayload {
	t.Helper()
	var out []marotte.ErrorPayload
	for _, e := range bufferedSince(h, sinceID) {
		var msg marotte.ServerEvent
		if json.Unmarshal(e.Event.Data, &msg) != nil || msg.Type != marotte.EventError {
			continue
		}
		raw, err := json.Marshal(msg.Payload)
		if err != nil {
			continue
		}
		var p marotte.ErrorPayload
		if json.Unmarshal(raw, &p) == nil {
			out = append(out, p)
		}
	}
	return out
}

// missingEvents ignores order within `got`: it backs did-these-fire assertions, which
// stay at the call site so a failure names the case rather than a shared helper.
func missingEvents(got []string, want ...string) []string {
	var missing []string
	for _, w := range want {
		if !slices.Contains(got, w) {
			missing = append(missing, w)
		}
	}
	return missing
}

// mustJSON takes testing.TB rather than *testing.T so a benchmark builds the same wire
// frames a test does; a benchmark's own copy of a frame is how fixtures drift.
func mustJSON(t testing.TB, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

// newChunkMsg is the ONE builder for a session/update agent_message_chunk in this
// package, because the `update` nesting is the protocol and hand-rolled copies of it
// are how a consumer came to read the kind off the outer object and drop every chunk
// while its own tests stayed green. It takes no testing.TB so the fake bridge's Call
// can use it: a builder callable from a fake must not end a test from another goroutine.
func newChunkMsg(text string) *marotte.RPCResponse {
	return newSessionChunkMsg("", text)
}

// newSessionChunkMsg sets the envelope's `sessionId`, which the utility bridge's
// own-session screen reads. An empty id omits the key.
func newSessionChunkMsg(sessionID, text string) *marotte.RPCResponse {
	update, _ := json.Marshal(map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": text},
	})
	// json.RawMessage, not []byte: a []byte field marshals to a base64 STRING,
	// which decodes as no frame at all.
	env := map[string]any{"update": json.RawMessage(update)}
	if sessionID != "" {
		env["sessionId"] = sessionID
	}
	params, _ := json.Marshal(env)
	return &marotte.RPCResponse{Method: marotte.MethodSessionUpdate, Params: params}
}

func newToolCallMsg(t *testing.T, id, title, status string) *marotte.RPCResponse {
	t.Helper()
	raw := mustJSON(t, map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    id,
		"title":         title,
		"kind":          "read",
		"status":        status,
	})
	return &marotte.RPCResponse{
		Method: "session/update",
		Params: mustJSON(t, map[string]any{"update": raw}),
	}
}

// --- Log capture ---

// logCapture is mutex-guarded because the logs it captures are written from background
// goroutines.
type logCapture struct {
	buf bytes.Buffer
	mu  sync.Mutex
}

func (b *logCapture) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logCapture) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// swapDefaultLogger installs h as the slog default for the duration of tb, so a
// caller must NOT call Parallel.
//
// The log package's writer and flags are restored too: slog.SetDefault also points
// log at the new handler, and it skips pointing it back when the restored handler
// is the stock one (which reaches log.Output), so every later line in the package
// would land in this buffer.
func swapDefaultLogger(tb testing.TB, h slog.Handler) {
	tb.Helper()
	prevLogger, prevWriter, prevFlags := slog.Default(), log.Writer(), log.Flags()
	tb.Cleanup(func() {
		slog.SetDefault(prevLogger)
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
	slog.SetDefault(slog.New(h))
}

// captureLogs mutates the global slog default, so a test using it must NOT call
// t.Parallel. The previous logger is restored at test end.
func captureLogs(t *testing.T) *logCapture {
	t.Helper()
	out := &logCapture{}
	swapDefaultLogger(t, slog.NewJSONHandler(out, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return out
}

// quietLogs silences the default handler for a benchmark whose subject logs once
// per iteration: at a real -benchtime that is millions of lines of package output.
func quietLogs(b *testing.B) {
	b.Helper()
	swapDefaultLogger(b, slog.DiscardHandler)
}

// --- Turn helpers ---

// stageWireTurn opens a wireTurnStart turn when none is open, the test-side
// equivalent of the first frame of a turn marotte did not prompt, and answers the
// accumulator the frames fold into.
func (rt *Runtime) stageWireTurn(tb testing.TB, chatID marotte.ChatID) *turnlog.Turn {
	tb.Helper()
	return rt.coord.TurnFoldTarget(tb.Context(), chatID)
}

// stagePromptTurn opens a prompt turn (the record's turn_open, then StartTurn) and
// hands back its id beside its accumulator. The id is the point: an id-scoped
// closer handed another turn's id closes nothing, so a test passing the wrong one
// exercises the fallthrough rather than its closer.
func (rt *Runtime) stagePromptTurn(tb testing.TB, chatID marotte.ChatID) (string, *turnlog.Turn) {
	tb.Helper()
	id, err := rt.coord.OpenTurn(tb.Context(), chatID, marotte.TurnSourcePrompt,
		&marotte.EntryPrompt{ID: "m-" + string(chatID), Text: "prompt"},
		func(c *marotte.Chat) { c.Name = "test chat" })
	if err != nil {
		tb.Fatalf("OpenTurn(%q) failed: %v", chatID, err)
	}
	if !rt.coord.StartTurn(tb.Context(), chatID, id) {
		tb.Fatalf("StartTurn(%q, %q) refused, so there is no turn to stage", chatID, id)
	}
	log, ok := rt.coord.OwnTurn(chatID)
	if !ok {
		tb.Fatalf("OwnTurn(%q) holds nothing after StartTurn", chatID)
	}
	return id, log
}

// liveTurn is the accumulator the NEXT frame would fold into: the chat's own open
// turn, nil when none is open.
// endTurn settles the chat's turn through the prompt-response closer with a
// clean end_turn, the shape every test that only needs a closed turn wants.
func endTurn(t *testing.T, h *Runtime, chatID marotte.ChatID, turnID string) {
	t.Helper()
	h.SettleTurnOnResponse(t.Context(), chatID, turnID, 0,
		&marotte.RPCResponse{Result: json.RawMessage(`{"stopReason":"end_turn"}`)})
}

func (rt *Runtime) liveTurn(chatID marotte.ChatID) *turnlog.Turn {
	log, ok := rt.coord.OwnTurn(chatID)
	if !ok {
		return nil
	}
	return log
}

// --- session_info_update builders ---

// newSessionInfoMsg takes the `_meta.kiro` block because session_info_update is a
// CARRIER — 22+ sub-kinds multiplex through it and marotte dispatches on which sub-BLOCK
// is present, so each helper below fills the one member its frame is about.
func newSessionInfoMsg(kiro map[string]any) *marotte.RPCResponse {
	update, _ := json.Marshal(map[string]any{
		"sessionUpdate": "session_info_update",
		"_meta":         map[string]any{"kiro": kiro},
	})
	params, _ := json.Marshal(map[string]any{"update": json.RawMessage(update)})
	return &marotte.RPCResponse{Method: marotte.MethodSessionUpdate, Params: params}
}

// newTurnStartMsg: KAS emits this bracket for every turn, one marotte never prompted
// included.
func newTurnStartMsg() *marotte.RPCResponse {
	return newSessionInfoMsg(map[string]any{"kind": "turn_start", "turnStart": true})
}

// newTurnEndMsg carries the outcome no local closer can know.
func newTurnEndMsg(stop string) *marotte.RPCResponse {
	return newSessionInfoMsg(map[string]any{
		"kind":    "turn_end",
		"turnEnd": map[string]any{"stopReason": stop},
	})
}

// newTurnCompletionMsg consumes a notification and folds NOTHING, the shape a settle
// bounded by folds alone parks behind forever.
func newTurnCompletionMsg() *marotte.RPCResponse {
	return newSessionInfoMsg(map[string]any{
		"kind":                "turn_completion",
		"promptTurnSummaries": []map[string]any{{"unit": "credit", "usage": 0.01}},
		"elapsedTime":         float64(1200),
	})
}

// newReplayedTurnEndMsg is a turn_end from a session/load replay: history, not now.
func newReplayedTurnEndMsg(stop string) *marotte.RPCResponse {
	update, _ := json.Marshal(map[string]any{
		"sessionUpdate": "session_info_update",
		"_meta": map[string]any{"kiro": map[string]any{
			"replay":  true,
			"kind":    "turn_end",
			"turnEnd": map[string]any{"stopReason": stop},
		}},
	})
	params, _ := json.Marshal(map[string]any{"update": json.RawMessage(update)})
	return &marotte.RPCResponse{Method: marotte.MethodSessionUpdate, Params: params}
}

// newAgentInitiatedChunkMsg carries the ONE flag that tells a prompted turn from an
// agent-initiated one. It rides content and never the bracket, which is why
// acknowledgement is provisional.
func newAgentInitiatedChunkMsg(text string) *marotte.RPCResponse {
	update, _ := json.Marshal(map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": text},
		"_meta":         map[string]any{"kiro": map[string]any{"agentInitiated": true}},
	})
	params, _ := json.Marshal(map[string]any{"update": json.RawMessage(update)})
	return &marotte.RPCResponse{Method: marotte.MethodSessionUpdate, Params: params}
}

// --- sequence helpers ---

// waitForParkedSettle proves the settle is PARKED before the folder is let move. It
// polls the registry's own state rather than sleeping: the discriminator is that no
// frame has been consumed yet, and a sleep would only make that likely.
func waitForParkedSettle(tb testing.TB, reg *turnRegistry, chatID marotte.ChatID, turnID string, want uint64) {
	tb.Helper()
	lc := reg.lifecycleFor(chatID)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		lc.mu.Lock()
		t := lc.turnLocked(turnID)
		parked := t != nil && t.NeedSeq == want
		lc.mu.Unlock()
		if parked {
			return
		}
		runtime.Gosched()
	}
	tb.Fatalf("the settle for turn %q never recorded NeedSeq %d, so it is not parked", turnID, want)
}

// payloadsOfType is generic over the payload so a caller reads the FIELD it cares about
// rather than a decoded map, where a lookup would pass on a renamed field.
func payloadsOfType[T any](tb testing.TB, events []sse.ReplayEvent, want marotte.EventType) []T {
	tb.Helper()
	var out []T
	for _, e := range events {
		var env struct {
			Type    marotte.EventType `json:"type"`
			Payload T                 `json:"payload"`
		}
		if err := json.Unmarshal(e.Event.Data, &env); err != nil {
			tb.Fatalf("unmarshal event: %v", err)
		}
		if env.Type == want {
			out = append(out, env.Payload)
		}
	}
	return out
}

// hasText reports whether the chat's log holds a sealed text entry containing
// want, the entry-model reading of "the assistant said this".
func (s *testChatStore) hasText(tb testing.TB, chatID marotte.ChatID, want string) bool {
	tb.Helper()
	entries, err := s.All(tb.Context(), chatID)
	if err != nil {
		tb.Fatalf("All(%q): %v", chatID, err)
	}
	for i := range entries {
		if entries[i].Kind != marotte.EntryKindText {
			continue
		}
		var text marotte.EntryText
		if json.Unmarshal(entries[i].Payload, &text) == nil && strings.Contains(text.Text, want) {
			return true
		}
	}
	return false
}

// sayText gives a staged turn one sealed word, so its end_turn grades completed
// rather than empty: the closer narrows a silent end_turn to `empty`, which earns
// no push.
func sayText(tb testing.TB, log *turnlog.Turn) {
	tb.Helper()
	if _, err := log.TextDelta(tb.Context(), "", "say-1", "hello"); err != nil {
		tb.Fatalf("TextDelta: %v", err)
	}
}
