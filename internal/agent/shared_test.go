package agent

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

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
	"github.com/cplieger/sse"
)

// Call Shutdown to tear it down.
func newTestHub() (*Runtime, *testChatStore, *fakeBridge) {
	return newTestHubIn("/tmp/work")
}

// Reassigning workDir afterwards misses the wiring's one read. Order-sensitive: cs.Bus can only be
// set once New returned.
func newTestHubIn(workDir string) (*Runtime, *testChatStore, *fakeBridge) {
	cs := newTestChatStore()
	br := newFakeBridge()
	h := New(context.Background(), workDir, func() ACPBridge { return br }, cs)
	// Park the cancel-retry ladder: its untracked timers outlive the test.
	h.runs.cancelRetryBase = time.Hour
	cs.wire(h)
	return h, cs, br
}

// originOf is the bridge a frame for chatID arrives on in a test, which is what forwardAt passes:
// the one registered under chatID now, or nil when none is.
func (rt *Runtime) originOf(chatID marotte.ChatID) acpResponder {
	if sb := rt.bridge.mgr.get(chatID); sb != nil {
		return sb.current()
	}
	return nil
}

// Call only once nothing else will Add.
func joinInflight(t *testing.T, h *Runtime) {
	t.Helper()
	drained := make(chan struct{})
	go func() {
		h.lifecycle.inflight.Wait()
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("the runtime's inflight work did not drain within 5s")
	}
}

// shutdownHub uses context.Background(): from t.Cleanup t.Context() is already done. 30s is above any unit test and below go test's timeout.
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

func bufferedSince(h *Runtime, sinceID uint64) []sse.ReplayEvent {
	var out []sse.ReplayEvent
	for _, e := range h.bus.fanout.Snapshot() {
		if e.Offset > sinceID {
			out = append(out, e)
		}
	}
	return out
}

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

// errorPayloadsSince returns the ErrorPayload of every `error` event after sinceID, skipping unrelated or undecodable frames.
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

// The assertion stays at the call site.
func missingEvents(got []string, want ...string) []string {
	var missing []string
	for _, w := range want {
		if !slices.Contains(got, w) {
			missing = append(missing, w)
		}
	}
	return missing
}

// mustJSON takes testing.TB so benchmarks build the same frames as tests.
func mustJSON(t testing.TB, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

// newChunkMsg is the package's one builder for a session/update agent_message_chunk: hand-rolled `update`
// nesting lets a consumer read the kind off the outer object. No testing.TB, so the fake's Call can use it.
func newChunkMsg(text string) *marotte.RPCResponse {
	return newSessionChunkMsg("", text)
}

// Empty omits it.
func newSessionChunkMsg(sessionID, text string) *marotte.RPCResponse {
	update, _ := json.Marshal(map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": text},
	})
	// json.RawMessage: a []byte marshals as a base64 string.
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

// logCapture is mutex-guarded: background goroutines write logs.
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

// swapDefaultLogger installs h as the slog default for tb; callers must not be parallel. The log package's
// writer and flags are restored too, since slog.SetDefault redirects log and skips undoing it for the stock handler.
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

// captureLogs swaps the global slog default, so its test must not call t.Parallel.
func captureLogs(t *testing.T) *logCapture {
	t.Helper()
	out := &logCapture{}
	swapDefaultLogger(t, slog.NewJSONHandler(out, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return out
}

func quietLogs(b *testing.B) {
	b.Helper()
	swapDefaultLogger(b, slog.DiscardHandler)
}

func (rt *Runtime) stageWireTurn(tb testing.TB, chatID marotte.ChatID) *turnlog.Turn {
	tb.Helper()
	return rt.coord.TurnFoldTarget(tb.Context(), chatID)
}

// stagePromptTurn opens a prompt turn and returns its id with its accumulator: an id-scoped closer given another id closes nothing.
func (rt *Runtime) stagePromptTurn(tb testing.TB, chatID marotte.ChatID) (string, *turnlog.Turn) {
	tb.Helper()
	id, err := rt.coord.OpenTurn(tb.Context(), chatID, command.TurnOpen{
		Source: marotte.TurnSourcePrompt,
		Prompt: &marotte.EntryPrompt{ID: "m-" + string(chatID), Text: "prompt"},
		Init:   func(c *marotte.Chat) { c.Name = "test chat" },
	})
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

// newSessionInfoMsg takes the `_meta.kiro` block: session_info_update carries 22+ sub-kinds dispatched by which sub-block is present.
func newSessionInfoMsg(kiro map[string]any) *marotte.RPCResponse {
	update, _ := json.Marshal(map[string]any{
		"sessionUpdate": "session_info_update",
		"_meta":         map[string]any{"kiro": kiro},
	})
	params, _ := json.Marshal(map[string]any{"update": json.RawMessage(update)})
	return &marotte.RPCResponse{Method: marotte.MethodSessionUpdate, Params: params}
}

// newTurnStartMsg pins that KAS brackets every turn, unprompted ones included.
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

// newTurnCompletionMsg consumes a notification and folds nothing, which a fold-bounded settle would park behind forever.
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

// newAgentInitiatedChunkMsg carries the one flag separating a prompted turn from an agent-initiated one, on content only.
func newAgentInitiatedChunkMsg(text string) *marotte.RPCResponse {
	update, _ := json.Marshal(map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": text},
		"_meta":         map[string]any{"kiro": map[string]any{"agentInitiated": true}},
	})
	params, _ := json.Marshal(map[string]any{"update": json.RawMessage(update)})
	return &marotte.RPCResponse{Method: marotte.MethodSessionUpdate, Params: params}
}

// waitForParkedSettle polls the registry until the settle is parked with no frame consumed, before the folder moves.
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

// payloadsOfType is generic so callers read a typed field, not a map lookup that passes on a rename.
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

// sayText seals one word into a staged turn so its end_turn grades completed, not empty (which earns no push).
func sayText(tb testing.TB, log *turnlog.Turn) {
	tb.Helper()
	if _, err := log.TextDelta(tb.Context(), "", "say-1", "hello"); err != nil {
		tb.Fatalf("TextDelta: %v", err)
	}
}
