package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/go/slogx/capture"
	"github.com/cplieger/go/sse/v2"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// Call Shutdown to tear it down.
func newTestHub(opts ...Option) (*Runtime, *testChatStore, *fakeBridge) {
	return newTestHubIn("/tmp/work", opts...)
}

// settleBoot is awaitBoot for a runtime a test built itself.
func settleBoot(t *testing.T, h *Runtime) {
	t.Helper()
	if err := awaitBoot(h); err != nil {
		t.Fatal(err)
	}
}

// Reassigning workDir afterwards misses the wiring's one read. Order-sensitive: cs.Bus can only be
// set once New returned.
func newTestHubIn(workDir string, opts ...Option) (*Runtime, *testChatStore, *fakeBridge) {
	return newTestHubOn(workDir, false, opts...)
}

// newColdTestHub is newTestHub whose engine sends no governance profile, so the administrator rules stay unread
// until a test resolves them.
func newColdTestHub(opts ...Option) (*Runtime, *fakeBridge) {
	h, _, br := newTestHubOn("/tmp/work", true, opts...)
	return h, br
}

func newTestHubOn(workDir string, coldGovernance bool, opts ...Option) (*Runtime, *testChatStore, *fakeBridge) {
	cs := newTestChatStore()
	br := newFakeBridge()
	br.noGovernance = coldGovernance
	src := fakeEngineOf(func() engineSession { return br }, br)
	src.noGovernance = coldGovernance
	h := newRuntimeOn(context.Background(), workDir, src, cs, opts...)
	cs.wire(h)
	// The engine's boot work reads br's scripts; a test edits them unlocked, so only after this.
	if err := awaitBoot(h); err != nil {
		panic(err)
	}
	br.forgetCalls()
	seededHubs.Store(cs, h)
	return h, cs, br
}

// awaitBoot waits for h's first engine generation and the work its readiness started, so the replay ring
// already holds every boot frame (engine_status among them) and no boot call is still reading a fake.
func awaitBoot(h *Runtime) error {
	if !awaitEngine(h) {
		return errors.New("setup: the fake engine was not ready within 5s")
	}
	// The profile's handler adds its follow-ups to inflight from the observer's forward loop, which is off inflight:
	// draining before it ran could Wait while it Adds from zero.
	if src, ok := h.engine.source.(*fakeEngineSource); ok && !src.noGovernance {
		select {
		case <-h.config.governance.warm:
		case <-time.After(5 * time.Second):
			return errors.New("setup: the boot's governance profile did not arrive within 5s")
		}
	}
	drained := make(chan struct{})
	go func() {
		h.lifecycle.inflight.Wait()
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		return errors.New("setup: the engine's boot work did not drain within 5s")
	}
	return awaitBootPowersSync(h)
}

// awaitBootPowersSync waits for the powers re-render the boot's administrator rules asked for: syncLoop runs off
// inflight, so its powers_changed would otherwise land inside the test.
func awaitBootPowersSync(h *Runtime) error {
	if !h.config.adminPolicyKnown() {
		return nil
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, e := range h.bus.fanout.Snapshot() {
			var msg marotte.ServerEvent
			if json.Unmarshal(e.Event.Data, &msg) == nil && msg.Type == marotte.EventPowersChanged {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return errors.New("setup: the boot's powers re-render did not broadcast within 5s")
		}
		time.Sleep(time.Millisecond)
	}
}

// originOf is the bridge a frame for chatID arrives on in a test, which is what forwardAt passes:
// the one registered under chatID now, or nil when none is.
func (rt *Runtime) originOf(chatID marotte.ChatID) acpResponder {
	// The test drives the frames its forward would, so the chat has the life a forward's attach creates.
	rt.coord.turns.ensureTestLife(chatID)
	if sb := rt.bridge.mgr.get(chatID); sb != nil {
		return sb.session()
	}
	return nil
}

// bootComing reports whether h's first engine generation is still going to become ready. Its ready hook adds
// to inflight, and an Add from zero concurrent with a Wait is a WaitGroup misuse the race detector reports.
func bootComing(h *Runtime) bool {
	select {
	case <-h.EngineReady():
		return false
	default:
	}
	if h.engine.unavailable {
		return false
	}
	src, ok := h.engine.source.(*fakeEngineSource)
	if !ok {
		return false
	}
	src.mu.Lock()
	startErr, gate := src.startErr, src.startGate
	src.mu.Unlock()
	if startErr != nil {
		return false
	}
	if gate == nil {
		return true
	}
	select {
	case <-gate:
		return true
	default:
		return false
	}
}

// Call only once nothing else will Add; a first engine still coming up is waited for first.
func joinInflight(t testing.TB, h *Runtime) {
	t.Helper()
	if bootComing(h) && !awaitEngine(h) {
		t.Fatal("the fake engine was not ready within 5s")
	}
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

// beginDraining flips draining as Shutdown's first step does, without the teardown that follows it.
func beginDraining(h *Runtime) {
	h.lifecycle.drainGate.Lock()
	h.lifecycle.draining.Store(true)
	h.lifecycle.drainGate.Unlock()
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

// postCmd posts cmd as marotte's own page, so what it sends is the user's (apicaller.Is).
func postCmd(t *testing.T, h *Runtime, cmd marotte.ClientCommand) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(cmd)
	req := httptest.NewRequest(http.MethodPost, "/api/command", strings.NewReader(string(body)))
	req.Header.Set("Sec-Fetch-Site", "same-origin")
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

// extractTypes is the events' types, engine_status left out: the engine's own start publishes it asynchronously.
func extractTypes(t *testing.T, events []sse.ReplayEvent) []string {
	t.Helper()
	out := make([]string, 0, len(events))
	for _, e := range events {
		var msg marotte.ServerEvent
		if err := json.Unmarshal(e.Event.Data, &msg); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		if msg.Type != marotte.EventEngineStatus {
			out = append(out, string(msg.Type))
		}
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

// captureLogs swaps the global slog default, so its test must not call t.Parallel.
func captureLogs(t *testing.T) *logCapture {
	t.Helper()
	out := &logCapture{}
	capture.SwapDefault(t, slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return out
}

func quietLogs(b *testing.B) {
	b.Helper()
	capture.SwapDefault(b, slog.New(slog.DiscardHandler))
}

func (rt *Runtime) stageWireTurn(tb testing.TB, chatID marotte.ChatID) *turnlog.Turn {
	tb.Helper()
	return rt.coord.foldTargetNow(tb.Context(), chatID)
}

// stagePromptTurn opens a prompt turn and returns its id with its accumulator: an id-scoped closer given another id closes nothing.
func (rt *Runtime) stagePromptTurn(tb testing.TB, chatID marotte.ChatID) (string, *turnlog.Turn) {
	tb.Helper()
	return rt.stagePromptTurnCalling(tb, chatID, nil)
}

// stagePromptTurnCalling is stagePromptTurn for a turn whose prompt call cancel trips.
func (rt *Runtime) stagePromptTurnCalling(tb testing.TB, chatID marotte.ChatID, cancel context.CancelCauseFunc) (string, *turnlog.Turn) {
	tb.Helper()
	id, err := rt.coord.openTurnNow(tb.Context(), chatID, command.TurnOpen{
		Source: marotte.TurnSourcePrompt,
		Prompt: &marotte.EntryPrompt{ID: "m-" + string(chatID), Text: "prompt"},
		Init:   func(c *marotte.Chat) { c.Name = "test chat" },
	})
	if err != nil {
		tb.Fatalf("OpenTurn(%q) failed: %v", chatID, err)
	}
	if !rt.coord.startTurn(tb.Context(), command.TurnRef{Chat: chatID, Turn: id}, cancel) {
		tb.Fatalf("startTurn(%q, %q) refused, so there is no turn to stage", chatID, id)
	}
	// The staged prompt counts as sent and written, so the turn_start a test delivers next is its own and kiro-cli has
	// a turn to file a steer in.
	commit, err := rt.coord.AwaitPromptSend(tb.Context(), chatID, id)
	if err != nil {
		tb.Fatalf("AwaitPromptSend(%q, %q): %v", chatID, id, err)
	}
	if commit != nil {
		if err := commit(); err != nil {
			tb.Fatalf("the staged prompt's write commit refused: %v", err)
		}
		rt.awaitPrompted(tb, chatID, id)
	}
	log, ok := rt.coord.OwnTurn(chatID)
	if !ok {
		tb.Fatalf("OwnTurn(%q) holds nothing after startTurn", chatID)
	}
	return id, log
}

// awaitPrompted waits for the commit's mark of turnID prompted (chatLifecycle.markPrompted runs it on a spawned
// goroutine): a test entering a synctest bubble next would otherwise have that goroutine, started outside the bubble,
// close a channel the bubble made.
func (rt *Runtime) awaitPrompted(tb testing.TB, chatID marotte.ChatID, turnID string) {
	tb.Helper()
	lc := rt.coord.turns.testLifecycle(chatID)
	if lc.reg == nil || lc.reg.runHost == nil {
		return
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		lc.mu.Lock()
		_, e := lc.run.byTurn(turnID)
		done := lc.ended || (e != nil && e.prompted)
		lc.mu.Unlock()
		if done {
			return
		}
		if time.Now().After(deadline) {
			tb.Fatalf("turn %s of %s was not marked prompted after its write commit", turnID, chatID)
		}
	}
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

// newTurnStartMsg is the untagged bracket of a turn KAS started itself.
func newTurnStartMsg() *marotte.RPCResponse {
	return newSessionInfoMsg(map[string]any{"kind": "turn_start", "turnStart": true})
}

// promptTurnStartMsg is the turn_start KAS sends for the newest session/prompt, echoing its tag.
func (b *fakeBridge) promptTurnStartMsg() *marotte.RPCResponse {
	return newPromptTurnStartMsg(promptTagOf(b.paramsFor(marotte.MethodPrompt)))
}

// promptTagOf is a session/prompt's _meta.kiro.userMessageTag, "" when absent.
func promptTagOf(params map[string]any) string {
	meta, _ := params["_meta"].(map[string]any)
	kiro, _ := meta["kiro"].(map[string]any)
	tag, _ := kiro["userMessageTag"].(string)
	return tag
}

// newPromptTurnStartMsg is the turn_start KAS sends for the prompt marotte tagged with turnID.
func newPromptTurnStartMsg(turnID string) *marotte.RPCResponse {
	return newSessionInfoMsg(map[string]any{
		"kind": "turn_start", "turnStart": true, "userMessageTag": turnID, "turnId": "kas-" + turnID,
	})
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

// waitForParkedSettle polls the registry until the settle is parked with no frame consumed, before the folder moves.
func waitForParkedSettle(tb testing.TB, reg *turnRegistry, chatID marotte.ChatID, turnID string, want uint64) {
	tb.Helper()
	lc := reg.testLifecycle(chatID)
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
