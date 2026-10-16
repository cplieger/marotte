package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/kascap"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/modeltext"
	"github.com/cplieger/slogx/capture"
)

func TestNewEngine_FieldsAndAccessors(t *testing.T) {
	b := NewEngine("/opt/kiro", "/work")
	// cliPath and workDir have no accessor, so pin them directly; TestStop_Idempotent covers channel init.
	if b.cliPath != "/opt/kiro" {
		t.Errorf("cliPath = %q", b.cliPath)
	}
	if b.workDir != "/work" {
		t.Errorf("workDir = %q", b.workDir)
	}
}

// TestSession_AccessorsReturnZeroValues pins a fresh session's zero values: empty strings, and empty slices.
func TestSession_AccessorsReturnZeroValues(t *testing.T) {
	s := newTestSession()
	if got := s.ModelID(); got != "" {
		t.Errorf("ModelID() = %q, want empty", got)
	}
	if got := s.CurrentMode(); got != "" {
		t.Errorf("CurrentMode() = %q, want empty", got)
	}
	if got := s.Modes(); len(got) != 0 {
		t.Errorf("Modes() = %v, want empty slice", got)
	}
	if got := s.Models(); len(got) != 0 {
		t.Errorf("Models() = %v, want empty slice", got)
	}
}

func TestIsDeprecatedOrLegacy(t *testing.T) {
	cases := []struct {
		desc string
		want bool
	}{
		{desc: "", want: false},
		{desc: "Claude 3.5 Sonnet", want: false},
		{desc: "Claude 3.5 Sonnet [Internal]", want: false},
		{desc: "Claude 3.5 Sonnet [Preview]", want: false},
		{desc: "Claude 3.5 Sonnet [Deprecated]", want: true},
		{desc: "Claude 3.5 Sonnet [DEPRECATED]", want: true},
		{desc: "Claude 3.5 Sonnet [Legacy]", want: true},
		{desc: "Claude 3.5 Sonnet [LEGACY]", want: true},
		// "deprecated" in prose must not trigger the filter.
		{desc: "Successor to the deprecated Claude 2 family", want: false},
		{desc: "Old model, deprecated 2024-10-01", want: false},
		{desc: "Model for legacyapi users", want: false},
	}
	for _, tc := range cases {
		if got := modeltext.Hidden(tc.desc); got != tc.want {
			t.Errorf("Hidden(%q) = %v, want %v", tc.desc, got, tc.want)
		}
	}
}

func TestValidIdent(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{in: "", want: true}, // empty = caller decides whether to emit flag
		{in: "kiro_default", want: true},
		{in: "kiro-planner", want: true},
		{in: "my.custom.agent", want: true},
		{in: "Claude3_5", want: true},
		{in: "a", want: true},
		{in: "kiro..planner", want: true}, // interior dots are still fine
		{in: strings.Repeat("a", 128), want: true},
		{in: strings.Repeat("a", 129), want: false},
		{in: "../../../etc/passwd", want: false},
		{in: "agent/with/slashes", want: false},
		{in: "agent with spaces", want: false},
		{in: "agent;rm -rf /", want: false},
		{in: "agent\nname", want: false},
		{in: "agent\x00name", want: false},
		{in: "agent$(whoami)", want: false},
		// Values matching identRe that read as current/parent dir or a hidden entry are rejected.
		{in: ".", want: false},
		{in: "..", want: false},
		{in: "...", want: false},
		{in: ".hidden", want: false},
		{in: "-flag", want: false},
	}
	for _, tc := range cases {
		if got := validIdent(tc.in); got != tc.want {
			t.Errorf("validIdent(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestApplySessionResult_CopiesModesAndModels(t *testing.T) {
	b := newTestSession()
	// v3 shape: a modes block plus the catalog inside the configOptions "model" select, multipliers under _meta.kiro.
	var r sessionCreated
	if err := json.Unmarshal([]byte(`{
		"sessionId": "sess-1",
		"modes": {"currentModeId": "mode-b", "availableModes": [
			{"id": "mode-a", "name": "Alpha", "description": "first"},
			{"id": "mode-b", "name": "Beta", "description": "second"}
		]},
		"configOptions": [{"id": "model", "currentValue": "claude-sonnet", "options": [
			{"value": "claude-sonnet", "name": "Sonnet", "description": "general", "_meta": {"kiro": {"rateMultiplier": 1.0}}},
			{"value": "old-opus", "name": "Opus", "description": "[Deprecated] old", "_meta": {"kiro": {"rateMultiplier": 3.0}}},
			{"value": "preview", "name": "Preview", "description": "[Internal] experimental", "_meta": {"kiro": {"rateMultiplier": 1.5}}},
			{"value": "legacy", "name": "Legacy", "description": "[Legacy] v1", "_meta": {"kiro": {"rateMultiplier": 1.0}}}
		]}]
	}`), &r); err != nil {
		t.Fatalf("unmarshal session result: %v", err)
	}

	b.mu.Lock()
	b.applySessionResultLocked(&r, "fallback-model")
	b.mu.Unlock()

	if got := b.CurrentMode(); got != "mode-b" {
		t.Errorf("currentMode = %q, want mode-b", got)
	}
	if got := b.Modes(); len(got) != 2 {
		t.Errorf("modes len = %d, want 2", len(got))
	}
	if got := b.ModelID(); got != "claude-sonnet" {
		t.Errorf("modelID = %q, want claude-sonnet", got)
	}
	models := b.Models()
	if len(models) != 2 {
		t.Errorf("models len = %d, want 2 (1 kept + 1 internal), got %v", len(models), models)
	}
	seen := map[string]bool{}
	for _, m := range models {
		seen[m.ID] = true
	}
	if !seen["claude-sonnet"] || !seen["preview"] {
		t.Errorf("models = %v, want claude-sonnet + preview", models)
	}
	if seen["old-opus"] || seen["legacy"] {
		t.Errorf("filtered-out models leaked through: %v", models)
	}
}

// With no modes or models block and no model id yet, the fallback model wins.
func TestApplySessionResult_FallbackModelWhenMissing(t *testing.T) {
	b := newTestSession()
	b.mu.Lock()
	b.applySessionResultLocked(&sessionCreated{}, "fallback")
	b.mu.Unlock()
	if got := b.ModelID(); got != "fallback" {
		t.Errorf("modelID = %q, want fallback", got)
	}
	if got := b.Modes(); len(got) != 0 {
		t.Errorf("modes = %v, want empty", got)
	}
	if got := b.Models(); len(got) != 0 {
		t.Errorf("models = %v, want empty", got)
	}
}

// The fallback does not overwrite a current model from the response.
func TestApplySessionResult_FallbackIgnoredWhenCurrentPresent(t *testing.T) {
	b := newTestSession()
	r := sessionCreated{
		ConfigOptions: []sessionConfigOption{
			{ID: "model", CurrentValue: json.RawMessage(`"real-model"`)},
		},
	}
	b.mu.Lock()
	b.applySessionResultLocked(&r, "fallback")
	b.mu.Unlock()
	if got := b.ModelID(); got != "real-model" {
		t.Errorf("modelID = %q, want real-model (fallback must not override)", got)
	}
}

// TestModes_ReturnedSliceIsDefensiveCopy pins that Modes returns a copy.
func TestModes_ReturnedSliceIsDefensiveCopy(t *testing.T) {
	b := newTestSession()
	r := sessionCreated{
		Modes: &sessionModes{
			CurrentModeID: "m1",
			AvailableModes: []sessionMode{
				{ID: "m1", Name: "One", Description: "first"},
				{ID: "m2", Name: "Two", Description: "second"},
			},
		},
	}
	b.mu.Lock()
	b.applySessionResultLocked(&r, "")
	b.mu.Unlock()

	first := b.Modes()
	if len(first) != 2 {
		t.Fatalf("first Modes() len = %d, want 2", len(first))
	}
	// Same backing array across calls: no allocation on the read path.
	second := b.Modes()
	if &first[0] != &second[0] {
		t.Errorf("Modes() returned different backing arrays; expected same frozen slice")
	}
}

// TestModels_ReturnedSliceIsDefensiveCopy pins the same for Models.
func TestModels_ReturnedSliceIsDefensiveCopy(t *testing.T) {
	b := newTestSession()
	var r sessionCreated
	if err := json.Unmarshal([]byte(`{
		"sessionId": "sess-1",
		"configOptions": [{"id": "model", "currentValue": "a", "options": [
			{"value": "a", "name": "Alpha", "description": "x", "_meta": {"kiro": {"rateMultiplier": 1}}},
			{"value": "b", "name": "Beta", "description": "y", "_meta": {"kiro": {"rateMultiplier": 2}}}
		]}]
	}`), &r); err != nil {
		t.Fatalf("unmarshal session result: %v", err)
	}
	b.mu.Lock()
	b.applySessionResultLocked(&r, "")
	b.mu.Unlock()

	first := b.Models()
	if len(first) != 2 {
		t.Fatalf("first Models() len = %d, want 2", len(first))
	}
	first[0].Name = "mutated"
	second := b.Models()
	if second[0].Name != "Alpha" {
		t.Errorf("second Models()[0].Name = %q, want %q", second[0].Name, "Alpha")
	}
}

func TestStop_Idempotent(t *testing.T) {
	b := NewEngine("/nonexistent/cli", "/work")
	// Never started: Stop must be safe, repeatedly.
	b.Stop()
	b.Stop() // would panic on close of closed channel without sync.Once
	b.Stop()
}

// respondEngine returns an engine wired to a pipe so Respond output can be read back.
func respondEngine(t *testing.T) (*Engine, *os.File) {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pr.Close() })
	e := NewEngine("/nonexistent", "/work")
	e.stdin.Store(&stdinPipe{w: pw})
	return e, pr
}

func TestRespond_SuccessResult(t *testing.T) {
	b, pr := respondEngine(t)
	result := map[string]string{"content": "hello"}
	if err := b.Respond(t.Context(), 42, result, nil); err != nil {
		t.Fatal(err)
	}
	_ = b.stdin.Load().w.Close() // signal EOF so the read below terminates

	buf := make([]byte, 4096)
	n, _ := pr.Read(buf)
	var got map[string]json.RawMessage
	if err := json.Unmarshal(buf[:n], &got); err != nil {
		t.Fatalf("unmarshal: %v (raw: %s)", err, buf[:n])
	}
	if string(got["jsonrpc"]) != `"2.0"` {
		t.Errorf("jsonrpc = %s, want \"2.0\"", got["jsonrpc"])
	}
	if string(got["id"]) != "42" {
		t.Errorf("id = %s, want 42", got["id"])
	}
	if _, hasErr := got["error"]; hasErr {
		t.Errorf("unexpected error field: %s", got["error"])
	}
	var res map[string]string
	if err := json.Unmarshal(got["result"], &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if res["content"] != "hello" {
		t.Errorf("result.content = %q, want %q", res["content"], "hello")
	}
}

func TestRespond_GenericError(t *testing.T) {
	b, pr := respondEngine(t)
	if err := b.Respond(t.Context(), 7, nil, errors.New("something broke")); err != nil {
		t.Fatal(err)
	}
	_ = b.stdin.Load().w.Close()

	buf := make([]byte, 4096)
	n, _ := pr.Read(buf)
	var got struct {
		Error struct {
			Message string `json:"message"`
			Code    int    `json:"code"`
		} `json:"error"`
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(buf[:n], &got); err != nil {
		t.Fatalf("unmarshal: %v (raw: %s)", err, buf[:n])
	}
	if got.ID != 7 {
		t.Errorf("id = %d, want 7", got.ID)
	}
	if got.Error.Code != -32603 {
		t.Errorf("error.code = %d, want -32603", got.Error.Code)
	}
	if got.Error.Message != "something broke" {
		t.Errorf("error.message = %q, want %q", got.Error.Message, "something broke")
	}
}

func TestRespond_BoundsAndFlattensGenericErrorMessage(t *testing.T) {
	b, pr := respondEngine(t)
	raw := "path\n\t\u202e" + strings.Repeat("x", 400)
	if err := b.Respond(t.Context(), 8, nil, errors.New(raw)); err != nil {
		t.Fatal(err)
	}
	_ = b.stdin.Load().w.Close()

	buf := make([]byte, 4096)
	n, _ := pr.Read(buf)
	var got struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(buf[:n], &got); err != nil {
		t.Fatalf("unmarshal: %v (raw: %s)", err, buf[:n])
	}
	if strings.ContainsAny(got.Error.Message, "\n\t\r") {
		t.Errorf("generic error message contains a line-breaking control: %q", got.Error.Message)
	}
	if strings.ContainsRune(got.Error.Message, '\u202e') {
		t.Errorf("generic error message contains a bidi override: %q", got.Error.Message)
	}
	// SanitizeSingleLineBounded's "..." sits outside the cap, so a truncated message is cap+3 bytes.
	if maxLen := maxRespondErrorBytes + len("..."); len(got.Error.Message) > maxLen {
		t.Errorf("generic error message length = %d, want at most %d bytes", len(got.Error.Message), maxLen)
	}
}

// TestWrites_OnAnUnstartedEngineRefuseRatherThanPanic pins the three write verbs without a stdin handle; each once
// panicked on a nil interface into an opaque 500.
func TestWrites_OnAnUnstartedEngineRefuseRatherThanPanic(t *testing.T) {
	cases := map[string]func(*Engine) error{
		"Call": func(b *Engine) error {
			_, err := b.Call(t.Context(), marotte.MethodSetMode, nil)
			return err
		},
		"CallAt": func(b *Engine) error {
			_, _, err := b.CallAt(t.Context(), marotte.MethodSetMode, nil)
			return err
		},
		"Notify": func(b *Engine) error {
			return b.Notify(t.Context(), marotte.MethodCancel, nil)
		},
		"Respond": func(b *Engine) error {
			return b.Respond(t.Context(), 1, map[string]string{}, nil)
		},
	}
	for name, write := range cases {
		t.Run(name, func(t *testing.T) {
			// NewEngine without Start: a failed Start's state.
			err := write(NewEngine("/nonexistent", "/work"))
			if !errors.Is(err, marotte.ErrBridgeNotStarted) {
				t.Errorf("%s on an unstarted bridge = %v, want ErrBridgeNotStarted", name, err)
			}
		})
	}
}

// TestCall_OnAnUnstartedEngineLeavesNoPendingWaiter pins that a refused write deregisters its request, or a later response
// could reach a caller that is gone.
func TestCall_OnAnUnstartedEngineLeavesNoPendingWaiter(t *testing.T) {
	b := NewEngine("/nonexistent", "/work")
	if _, err := b.Call(t.Context(), marotte.MethodSetMode, nil); err == nil {
		t.Fatal("Call on an unstarted bridge returned no error")
	}
	b.pendingMu.Lock()
	defer b.pendingMu.Unlock()
	if len(b.pending) != 0 {
		t.Errorf("pending requests after a refused write = %d, want 0", len(b.pending))
	}
}

func TestRespond_TypedRPCError(t *testing.T) {
	b, pr := respondEngine(t)
	rpcErr := &marotte.RPCError{Code: -32001, Message: "custom error"}
	if err := b.Respond(t.Context(), 99, nil, rpcErr); err != nil {
		t.Fatal(err)
	}
	_ = b.stdin.Load().w.Close()

	buf := make([]byte, 4096)
	n, _ := pr.Read(buf)
	var got struct {
		Error struct {
			Message string `json:"message"`
			Code    int    `json:"code"`
		} `json:"error"`
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(buf[:n], &got); err != nil {
		t.Fatalf("unmarshal: %v (raw: %s)", err, buf[:n])
	}
	if got.ID != 99 {
		t.Errorf("id = %d, want 99", got.ID)
	}
	if got.Error.Code != -32001 {
		t.Errorf("error.code = %d, want -32001", got.Error.Code)
	}
	if got.Error.Message != "custom error" {
		t.Errorf("error.message = %q, want %q", got.Error.Message, "custom error")
	}
}

func TestCall_HappyPath(t *testing.T) {
	b := NewEngine("/nonexistent", "/work")
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pr.Close() })
	b.stdin.Store(&stdinPipe{w: pw})

	type result struct {
		resp *marotte.RPCResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		r, e := b.Call(t.Context(), "test/method", map[string]string{"key": "val"})
		done <- result{r, e}
	}()

	waitPending(t, b, 1)

	b.pendingMu.Lock()
	var id int64
	var ch chan pendingReply
	for k, v := range b.pending {
		id = k
		ch = v.ch
		break
	}
	delete(b.pending, id)
	b.pendingMu.Unlock()

	successResp := &marotte.RPCResponse{}
	raw := json.RawMessage(`{"ok":true}`)
	successResp.Result = raw
	ch <- pendingReply{resp: successResp}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("unexpected error: %v", r.err)
		}
		if r.resp != successResp {
			t.Errorf("resp = %+v, want the injected response", r.resp)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Call did not return")
	}
}

func TestCall_ErrorResponse(t *testing.T) {
	b := NewEngine("/nonexistent", "/work")
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pr.Close() })
	b.stdin.Store(&stdinPipe{w: pw})

	type result struct {
		resp *marotte.RPCResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		r, e := b.Call(t.Context(), "test/method", nil)
		done <- result{r, e}
	}()

	waitPending(t, b, 1)

	b.pendingMu.Lock()
	var id int64
	var ch chan pendingReply
	for k, v := range b.pending {
		id = k
		ch = v.ch
		break
	}
	delete(b.pending, id)
	b.pendingMu.Unlock()

	errResp := &marotte.RPCResponse{
		Error: &marotte.RPCError{Code: -32600, Message: "invalid request"},
	}
	ch <- pendingReply{resp: errResp}

	select {
	case r := <-done:
		if r.err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(r.err.Error(), "invalid request") {
			t.Errorf("err = %v, want to contain 'invalid request'", r.err)
		}
		if r.resp != errResp {
			t.Errorf("resp should be returned even on error")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Call did not return")
	}
}

func TestCall_BridgeExitedSentinel(t *testing.T) {
	b := NewEngine("/nonexistent", "/work")
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pr.Close() })
	b.stdin.Store(&stdinPipe{w: pw})

	type result struct {
		resp *marotte.RPCResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		r, e := b.Call(t.Context(), "test/method", nil)
		done <- result{r, e}
	}()

	waitPending(t, b, 1)

	b.pendingMu.Lock()
	var ch chan pendingReply
	for _, v := range b.pending {
		ch = v.ch
		break
	}
	b.pendingMu.Unlock()

	ch <- pendingReply{resp: bridgeExitedResp}

	select {
	case r := <-done:
		if !errors.Is(r.err, errBridgeExited) {
			t.Errorf("err = %v, want errBridgeExited", r.err)
		}
		if r.resp != nil {
			t.Errorf("resp = %+v, want nil on bridge-exited", r.resp)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Call did not return")
	}
}

// BenchmarkBridgeReadLoop measures readLoop's parse and dispatch throughput over a pipe of mixed responses and
// notifications.
func BenchmarkBridgeReadLoop(b *testing.B) {
	respID := int64(1)
	respMsg, _ := json.Marshal(marotte.RPCResponse{
		JSONRPC: "2.0",
		ID:      &respID,
		Result:  json.RawMessage(`{"status":"ok"}`),
	})
	notifMsg, _ := json.Marshal(marotte.RPCResponse{
		JSONRPC: "2.0",
		Method:  "session/update",
		Params:  json.RawMessage(`{"sessionId":"s1","delta":{"type":"text","text":"hi"}}`),
	})
	respLine := append(respMsg, '\n')
	notifLine := append(notifMsg, '\n')

	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()

		pr, pw, _ := os.Pipe()
		br := readLoopEngine(pr)

		const msgCount = 500
		for id := int64(1); id <= msgCount/2; id++ {
			br.pending[id] = &pendingEntry{ch: make(chan pendingReply, 1), lane: br.engineLane}
		}

		go func() {
			for j := range msgCount {
				if j%2 == 0 {
					id := int64(j/2 + 1)
					msg, _ := json.Marshal(marotte.RPCResponse{
						JSONRPC: "2.0",
						ID:      &id,
						Result:  json.RawMessage(`{"status":"ok"}`),
					})
					pw.Write(append(msg, '\n'))
				} else {
					pw.Write(notifLine)
				}
			}
			pw.Close()
		}()

		// Drain the engine lane, as its consumer would.
		drainDone := make(chan struct{})
		go func() {
			for range br.EngineNotifCh() {
			}
			close(drainDone)
		}()

		b.StartTimer()
		br.readLoop()
		<-drainDone
	}
	_ = respLine
	_ = notifLine
}

// fakeKASScript is a fake kiro-cli for the engine: it answers initialize, a session/new with the id marotte minted,
// a session/load with the id it names, and every other request with an empty result.
const fakeKASScript = `#!/bin/sh
while IFS= read -r line; do
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(echo "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  sid=$(echo "$line" | sed -n 's/.*"overrideSessionId":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"serverInfo":{"name":"fake-kiro"}}}\n' "$id"
      ;;
    session/new)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"%s","modes":{"currentModeId":"agent","availableModes":[{"id":"agent","name":"Agent","description":"agent mode"},{"id":"code","name":"Code","description":"code mode"}]},"configOptions":[{"id":"model","currentValue":"model-1","options":[{"value":"model-1","name":"Test Model","description":"A test model","_meta":{"kiro":{"rateMultiplier":1.0}}},{"value":"auto","name":"Auto"}]}]}}\n' "$id" "$sid"
      ;;
    session/load)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"modes":{"currentModeId":"agent","availableModes":[{"id":"agent","name":"Agent","description":"agent mode"}]},"configOptions":[{"id":"model","currentValue":"model-1","options":[{"value":"model-1","name":"Test Model","description":"A test model","_meta":{"kiro":{"rateMultiplier":1.0}}},{"value":"auto","name":"Auto"}]}]}}\n' "$id"
      ;;
    *)
      if [ -n "$id" ]; then
        printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"
      fi
      ;;
  esac
done
`

// startFakeEngine starts an engine over fakeKASScript; the test stops it at cleanup.
func startFakeEngine(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "fake-kiro-cli")
	if err := os.WriteFile(scriptPath, []byte(fakeKASScript), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}
	e := NewEngine(scriptPath, dir)
	// Not t.Context(): the lifetime must outlive the cleanup that stops the engine.
	if err := e.Start(t.Context(), &marotte.EngineOpts{Lifetime: context.Background()}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(e.Stop)
	return e
}

// TestRealEngine_Contract runs the engine and its session doors over a fake kiro-cli script, so drift fails at the
// lifecycle level without a real binary.
func TestRealEngine_Contract(t *testing.T) {
	t.Run("NewSession_mints_the_id", func(t *testing.T) {
		e := startFakeEngine(t)
		s, err := e.NewSession(t.Context(), &marotte.SessionOpts{Role: marotte.SessionRoleChat}, nil)
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		if id := string(s.SessionID()); !strings.HasPrefix(id, "sess_") {
			t.Errorf("SessionID = %q, want a minted sess_ id", id)
		}
		if !s.Open() {
			t.Error("a session whose door succeeded is not open")
		}
	})

	t.Run("LoadSession_keeps_the_id", func(t *testing.T) {
		e := startFakeEngine(t)
		s, err := e.LoadSession(t.Context(), &marotte.SessionOpts{SessionID: "existing-sess"}, nil)
		if err != nil {
			t.Fatalf("LoadSession: %v", err)
		}
		if id := s.SessionID(); id != "existing-sess" {
			t.Errorf("SessionID = %q, want existing-sess", id)
		}
	})

	t.Run("Call_returns_response", func(t *testing.T) {
		e := startFakeEngine(t)
		s, err := e.NewSession(t.Context(), &marotte.SessionOpts{}, nil)
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		resp, err := s.Call(t.Context(), "session/prompt", nil)
		if err != nil || resp == nil {
			t.Fatalf("Call = %v, %v; want a response", resp, err)
		}
	})

	t.Run("Notify_does_not_error", func(t *testing.T) {
		e := startFakeEngine(t)
		if err := e.Notify(t.Context(), "session/update", nil); err != nil {
			t.Errorf("Notify: %v", err)
		}
	})

	t.Run("Stop_closes_every_lane", func(t *testing.T) {
		e := startFakeEngine(t)
		s, err := e.NewSession(t.Context(), &marotte.SessionOpts{}, nil)
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		e.Stop()
		for name, ch := range map[string]<-chan marotte.Notification{"session": s.NotifCh(), "engine": e.EngineNotifCh()} {
			select {
			case _, ok := <-ch:
				if ok {
					for range ch {
					}
				}
			case <-time.After(2 * time.Second):
				t.Errorf("the %s lane is not closed after Stop", name)
			}
		}
		if s.Open() {
			t.Error("a session on a stopped engine still reports open")
		}
	})
}

// Count-driven parseErrTracker cases are a table; the clock-driven ones are synctest bubbles below, exercising the
// real comparisons at no real-time cost.

func TestParseErrTracker(t *testing.T) {
	cases := []struct {
		setup      func(*parseErrTracker)
		name       string
		calls      int
		wantAction parseErrAction
	}{
		{
			name:       "burst phase returns parseErrLog",
			setup:      func(_ *parseErrTracker) {},
			calls:      parseErrBurst,
			wantAction: parseErrLog,
		},
		{
			name: "suppress after burst within window",
			setup: func(tr *parseErrTracker) {
				for range parseErrBurst {
					tr.record()
				}
			},
			calls:      1,
			wantAction: parseErrSuppress,
		},
		{
			name:       "circuit break at consecutive threshold",
			setup:      func(_ *parseErrTracker) {},
			calls:      parseErrMaxConsecutive,
			wantAction: parseErrCircuitBreak,
		},
		{
			name: "reset clears consecutive counter",
			setup: func(tr *parseErrTracker) {
				for range 5 {
					tr.record()
				}
				tr.Reset()
			},
			calls:      parseErrMaxConsecutive,
			wantAction: parseErrCircuitBreak,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var tr parseErrTracker
			tc.setup(&tr)
			var action parseErrAction
			for range tc.calls {
				action = tr.record()
			}
			if action != tc.wantAction {
				t.Errorf("after %d Record() calls: got %d, want %d", tc.calls, action, tc.wantAction)
			}
		})
	}

	t.Run("summary count tracks suppressed errors", func(t *testing.T) {
		var tr parseErrTracker
		for range parseErrBurst {
			tr.record()
		}
		extra := 7
		for range extra {
			tr.record()
		}
		if got := tr.summaryCount(); got != extra {
			t.Errorf("SummaryCount() = %d, want %d", got, extra)
		}
	})
}

// TestParseErrTracker_WindowCadenceIsDrivenByTheClock moves the clock and lets Record decide, rather than staging
// windowStart.
func TestParseErrTracker_WindowCadenceIsDrivenByTheClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var tr parseErrTracker
		for range parseErrBurst {
			if got := tr.record(); got != parseErrLog {
				t.Fatalf("burst Record() = %v, want parseErrLog", got)
			}
		}
		// Inside the window: suppressed.
		synctest.Sleep(parseErrWindow / 2)
		if got := tr.record(); got != parseErrSuppress {
			t.Errorf("Record() inside the window = %v, want parseErrSuppress", got)
		}
		// Past it: one summary, then suppressed again as the window restarts.
		synctest.Sleep(parseErrWindow)
		if got := tr.record(); got != parseErrSummarize {
			t.Errorf("Record() past the window = %v, want parseErrSummarize", got)
		}
		if got := tr.record(); got != parseErrSuppress {
			t.Errorf("Record() straight after a summary = %v, want parseErrSuppress: "+
				"the window must restart at the summary, or a storm emits one line per frame", got)
		}
		// The edge belongs to the window it closes: the comparison is strict, so the cadence is a floor.
		synctest.Sleep(parseErrWindow)
		if got := tr.record(); got != parseErrSuppress {
			t.Errorf("Record() exactly %v after the summary = %v, want parseErrSuppress", parseErrWindow, got)
		}
		synctest.Sleep(time.Nanosecond)
		if got := tr.record(); got != parseErrSummarize {
			t.Errorf("Record() a nanosecond past the cadence = %v, want parseErrSummarize", got)
		}
	})
}

// The window opens when the burst ends, not when the storm began, so a trickling storm still gets a full window.
func TestParseErrTracker_TheWindowOpensWhenTheBurstEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var tr parseErrTracker
		for range parseErrBurst - 1 {
			if got := tr.record(); got != parseErrLog {
				t.Fatalf("burst Record() = %v, want parseErrLog", got)
			}
		}
		// Long enough to stale a window anchored at the start, short of decay.
		synctest.Sleep(parseErrWindow + time.Second)
		if got := tr.record(); got != parseErrLog {
			t.Fatalf("the last verbatim Record() = %v, want parseErrLog", got)
		}
		if got := tr.record(); got != parseErrSuppress {
			t.Errorf("Record() straight after the burst = %v, want parseErrSuppress: "+
				"nothing is due to be summarized until a window has passed since the burst ended", got)
		}
	})
}

// TestParseErrTracker_DecayRestartsTheBurstButNotTheBreaker pins that decay resets the window, restoring the verbatim burst,
// and leaves consecutive alone so a slow total failure still trips.
func TestParseErrTracker_DecayRestartsTheBurstButNotTheBreaker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var tr parseErrTracker
		for range parseErrBurst + 5 {
			tr.record()
		}
		if got := tr.record(); got != parseErrSuppress {
			t.Fatalf("Record() past the burst = %v, want parseErrSuppress", got)
		}

		// Quiet for exactly parseErrDecay is not yet decayed: this line is summarized.
		synctest.Sleep(parseErrDecay)
		if got := tr.record(); got != parseErrSummarize {
			t.Errorf("Record() after exactly %v of quiet = %v, want parseErrSummarize: "+
				"the burst must not restart until the decay is exceeded", parseErrDecay, got)
		}

		synctest.Sleep(parseErrDecay + time.Second)

		// The burst is back: emitted verbatim.
		if got := tr.record(); got != parseErrLog {
			t.Errorf("Record() after %v of quiet = %v, want parseErrLog: "+
				"decay must restart the burst", parseErrDecay, got)
		}
		if tr.total != 1 {
			t.Errorf("total = %d after decay, want 1", tr.total)
		}

		// The breaker is not reset: consecutive counted every error.
		if tr.consecutive != parseErrBurst+8 {
			t.Fatalf("consecutive = %d, want %d: decay must not touch the breaker's count",
				tr.consecutive, parseErrBurst+8)
		}
		for range parseErrMaxConsecutive - tr.consecutive - 1 {
			tr.record()
		}
		if got := tr.record(); got != parseErrCircuitBreak {
			t.Errorf("Record() at consecutive=%d = %v, want parseErrCircuitBreak: "+
				"decay must not spare a stream that fails totally but slowly",
				tr.consecutive, got)
		}
	})
}

// TestSession_LifecycleContract pins that a session's accessors keep their last values once it closes with its engine.
func TestSession_LifecycleContract(t *testing.T) {
	e := startFakeEngine(t)
	s, err := e.NewSession(t.Context(), &marotte.SessionOpts{}, nil)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	id := s.SessionID()
	if got := s.ModelID(); got != "model-1" {
		t.Errorf("ModelID = %q, want model-1", got)
	}
	if m := s.CurrentMode(); m != "agent" {
		t.Errorf("CurrentMode = %q, want agent", m)
	}
	if modes := s.Modes(); len(modes) != 2 {
		t.Errorf("Modes len = %d, want 2", len(modes))
	}
	if models := s.Models(); len(models) != 2 {
		t.Errorf("Models len = %d, want 2", len(models))
	}

	e.Stop()
	if got := s.SessionID(); got != id {
		t.Errorf("post-stop SessionID = %q, want %q (must not reset)", got, id)
	}
	if got := s.ModelID(); got != "model-1" {
		t.Errorf("post-stop ModelID = %q, want model-1 (must not reset)", got)
	}
	if _, err := s.Call(t.Context(), "session/prompt", nil); !errors.Is(err, marotte.ErrBridgeExited) {
		t.Errorf("a call on a session of a stopped engine = %v, want ErrBridgeExited", err)
	}
	// Double Stop: no panic.
	e.Stop()
}

// TestBridgeRPC_ErrorClassification pins how Call maps JSON-RPC error codes to sentinels.
func TestBridgeRPC_ErrorClassification(t *testing.T) {
	cases := []struct {
		name        string
		message     string
		wantMsg     string
		code        int
		wantNotIdle bool
	}{
		{name: "not-idle by code", code: -32001, message: "session busy", wantNotIdle: true},
		{name: "not-idle by message only", code: -32099, message: "not idle", wantNotIdle: false, wantMsg: "not idle"},
		{name: "parse error", code: -32700, message: "parse error", wantNotIdle: false, wantMsg: "parse error"},
		{name: "invalid request", code: -32600, message: "invalid request", wantNotIdle: false, wantMsg: "invalid request"},
		{name: "method not found", code: -32601, message: "method not found", wantNotIdle: false, wantMsg: "method not found"},
		{name: "internal error", code: -32603, message: "internal error", wantNotIdle: false, wantMsg: "internal error"},
		{name: "server-defined error", code: -32050, message: "custom server err", wantNotIdle: false, wantMsg: "custom server err"},
		{name: "positive code", code: 1, message: "unknown positive", wantNotIdle: false, wantMsg: "unknown positive"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := NewEngine("/nonexistent", "/work")
			pr, pw, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = pr.Close() })
			b.stdin.Store(&stdinPipe{w: pw})

			type result struct {
				resp *marotte.RPCResponse
				err  error
			}
			done := make(chan result, 1)
			go func() {
				r, e := b.Call(t.Context(), "test/method", nil)
				done <- result{r, e}
			}()

			waitPending(t, b, 1)

			b.pendingMu.Lock()
			var ch chan pendingReply
			for _, v := range b.pending {
				ch = v.ch
				break
			}
			b.pendingMu.Unlock()

			errResp := &marotte.RPCResponse{
				Error: &marotte.RPCError{Code: tc.code, Message: tc.message},
			}
			ch <- pendingReply{resp: errResp}

			select {
			case r := <-done:
				if r.err == nil {
					t.Fatal("expected error, got nil")
				}
				if tc.wantNotIdle {
					if !errors.Is(r.err, marotte.ErrNotIdle) {
						t.Errorf("err = %v, want marotte.ErrNotIdle", r.err)
					}
				} else {
					if errors.Is(r.err, marotte.ErrNotIdle) {
						t.Errorf("err = %v, should NOT be marotte.ErrNotIdle", r.err)
					}
					if !strings.Contains(r.err.Error(), tc.wantMsg) {
						t.Errorf("err = %v, want to contain %q", r.err, tc.wantMsg)
					}
				}
				if r.resp != errResp {
					t.Errorf("resp should be returned even on error")
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("Call did not return")
			}
		})
	}
}

func BenchmarkBridgeRespond(b *testing.B) {
	// Drain the read end, as kiro-cli does: discarded, writeFrame filled the 64 KiB buffer and the benchmark hung at
	// -benchtime=50x and above.
	pr, pw, err := os.Pipe()
	if err != nil {
		b.Fatalf("os.Pipe: %v", err)
	}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		_, _ = io.Copy(io.Discard, pr)
	}()
	b.Cleanup(func() {
		_ = pw.Close() // EOF for the drain
		<-drained      // join it, so nothing outlives the benchmark
		_ = pr.Close()
	})

	br := NewEngine("/nonexistent", "/work")
	br.stdin.Store(&stdinPipe{w: pw})

	ctx := b.Context()
	// A typical tool result, about 500 bytes.
	result := map[string]any{
		"content": strings.Repeat("x", 400),
		"status":  "success",
		"meta":    map[string]string{"tool": "file_write", "path": "/workspace/main.go"},
	}

	b.ReportAllocs()
	for b.Loop() {
		if err := br.Respond(ctx, 42, result, nil); err != nil {
			b.Fatalf("Respond: %v", err)
		}
	}
}

// captureWriter stands in for the bridge's stdin, recording what was written, optionally failing every Write.
type captureWriter struct {
	failErr error
	buf     bytes.Buffer
	mu      sync.Mutex
	writes  int
}

func (w *captureWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failErr != nil {
		return 0, w.failErr
	}
	w.writes++
	return w.buf.Write(p)
}

func (*captureWriter) Close() error { return nil }

func (w *captureWriter) wrote() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writes > 0
}

// non-EOF is logged by logReadError, io.EOF ends the loop quietly.
type errReader struct{ failErr error }

func (r errReader) Read([]byte) (int, error) { return 0, r.failErr }

// readLoopEngine is an engine reading r with no process behind it.
func readLoopEngine(r io.Reader) *Engine {
	e := NewEngine("/nonexistent", "/work")
	e.lifecycleCtx = context.Background()
	e.stdout = newFrameReader(bufio.NewReaderSize(r, stdoutBufSize))
	e.readLoopStarted.Store(true)
	return e
}

// readFrame reads one frame the engine wrote, so a test syncs on the write itself; bounded, failing with a
// diagnostic.
func readFrame(t *testing.T, pr *os.File) {
	t.Helper()
	if err := pr.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline on the engine pipe: %v", err)
	}
	if line, err := bufio.NewReader(pr).ReadBytes('\n'); err != nil {
		t.Fatalf("read the framed request from the engine pipe: %v (read %q)", err, line)
	}
}

// waitPending polls until n requests are pending; injecting earlier finds no entry.
func waitPending(t *testing.T, e *Engine, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		e.pendingMu.Lock()
		got := len(e.pending)
		e.pendingMu.Unlock()
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("engine registered %d pending requests, want >=%d within 2s", got, n)
		}
		time.Sleep(time.Millisecond)
	}
}

func runLoadSession(t *testing.T, e *Engine, fallback string, resp *marotte.RPCResponse) error {
	t.Helper()
	_, _, err := runLoadSessionOpts(t, e, &marotte.SessionOpts{SessionID: "acp-session-xyz", Model: fallback}, resp)
	return err
}

// runNewSession drives NewSession like runLoadSessionOpts: answers session/new with resp, its sessionId replaced by
// the minted one, and every repair with success, returning every frame written and the session.
func runNewSession(t *testing.T, e *Engine, opts *marotte.SessionOpts, resp *marotte.RPCResponse) ([]byte, *Session, error) {
	t.Helper()
	var s *Session
	sent, err := driveSessionCall(t, e, resp, true, func(ctx context.Context) error {
		var err error
		s, err = e.NewSession(ctx, opts, nil)
		return err
	})
	return sent, s, err
}

func runLoadSessionOpts(t *testing.T, e *Engine, opts *marotte.SessionOpts, resp *marotte.RPCResponse) ([]byte, *Session, error) {
	t.Helper()
	var s *Session
	sent, err := driveSessionCall(t, e, resp, false, func(ctx context.Context) error {
		var err error
		s, err = e.LoadSession(ctx, opts, nil)
		return err
	})
	return sent, s, err
}

// withMintedID rewrites a session/new result's sessionId to the id the entry's session was minted under.
func withMintedID(entry *pendingEntry, resp *marotte.RPCResponse) *marotte.RPCResponse {
	if resp == nil || len(resp.Result) == 0 || entry.sessionID == "" {
		return resp
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(resp.Result, &m) != nil {
		return resp
	}
	if _, ok := m["sessionId"]; !ok {
		return resp
	}
	m["sessionId"], _ = json.Marshal(string(entry.sessionID))
	raw, _ := json.Marshal(m)
	return &marotte.RPCResponse{Result: raw, Error: resp.Error}
}

// driveSessionCall runs one call against an injected first response, answers every later request with success, and
// returns everything written. mint rewrites the first response's sessionId to the minted id. Each reply is stamped
// with its lane's position, as deliverResponse stamps it.
func driveSessionCall(
	t *testing.T, e *Engine, resp *marotte.RPCResponse, mint bool, run func(context.Context) error,
) ([]byte, error) {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pr.Close() })
	e.stdin.Store(&stdinPipe{w: pw})
	if e.lifecycleCtx == nil {
		e.lifecycleCtx = context.Background()
	}
	sent := make(chan []byte, 1)
	go func() {
		all, _ := io.ReadAll(pr)
		sent <- all
	}()
	done := make(chan error, 1)
	go func() {
		done <- run(t.Context())
	}()

	answer := resp
	first := true
	deadline := time.Now().Add(2 * time.Second)
	for {
		select {
		case callErr := <-done:
			_ = pw.Close()
			return <-sent, callErr
		default:
		}
		var entry *pendingEntry
		e.pendingMu.Lock()
		for id, v := range e.pending {
			entry = v
			delete(e.pending, id)
			break
		}
		e.pendingMu.Unlock()
		if entry != nil {
			if first && mint {
				answer = withMintedID(entry, answer)
			}
			first = false
			entry.ch <- pendingReply{resp: answer, seq: entry.lane.position()}
			answer = &marotte.RPCResponse{Result: json.RawMessage(`{}`)}
			continue
		}
		if time.Now().After(deadline) {
			t.Fatal("the call did not return, and no pending request was waiting for an answer")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// The burst-th Record sets the window start; the next call is inside the window and suppressed.
func TestParseErrTracker_WindowStartSetAtBurst(t *testing.T) {
	var tr parseErrTracker
	var got parseErrAction
	for range parseErrBurst + 1 {
		got = tr.record()
	}
	if got != parseErrSuppress {
		t.Errorf("Record() call #%d = %v, want parseErrSuppress (%v)", parseErrBurst+1, got, parseErrSuppress)
	}
}

// Stop must not dereference a nil Process (an unstarted command).
func TestStop_SkipsKillWhenProcessNil(t *testing.T) {
	b := NewEngine("cli", "work")
	b.cmd = exec.Command("sleep", "30") // never Start()ed -> Process is nil
	b.Stop()                            // must return without panicking
	if b.cmd == nil {
		t.Fatal("b.cmd unexpectedly nil after Stop")
	}
}

// Stop logs no "kill kiro-cli" error when killing a live process succeeds.
func TestStop_NoKillErrorLogOnLiveProcess(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep binary not available")
	}
	c := capture.Default(t)

	b := NewEngine("cli", "work")
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	b.cmd = cmd
	t.Cleanup(func() {
		if b.cmd != nil && b.cmd.Process != nil {
			_ = b.cmd.Process.Kill()
		}
	})

	b.Stop()

	if c.CountExact("kill kiro-cli") > 0 {
		t.Errorf(`Stop emitted "kill kiro-cli" error log on a successful Kill, want none`)
	}
}

// startProcess reports a spawn failure.
func TestStartProcess_ReportsSpawnFailure(t *testing.T) {
	bogus := filepath.Join(t.TempDir(), "no-such-kiro-cli")
	b := NewEngine(bogus, t.TempDir())
	// Not t.Context(): CommandContext binds the subprocess to lifecycleCtx, which must outlive t.Cleanup(b.Stop).
	b.lifecycleCtx = context.Background()
	t.Cleanup(b.Stop)
	err := b.startProcess()
	if err == nil {
		t.Fatal("startProcess with a nonexistent binary returned nil error, want a start failure")
	}
}

// startProcess assigns a 5s WaitDelay before the (failing) Start.
func TestStartProcess_SetsWaitDelayToFiveSeconds(t *testing.T) {
	bogus := filepath.Join(t.TempDir(), "no-such-kiro-cli")
	b := NewEngine(bogus, t.TempDir())
	// Not t.Context(), which is cancelled before cleanups run: lifecycleCtx must outlive t.Cleanup(b.Stop).
	b.lifecycleCtx = context.Background()
	t.Cleanup(b.Stop)
	_ = b.startProcess()
	if b.cmd == nil {
		t.Fatal("b.cmd is nil; startProcess did not reach CommandContext")
	}
	if b.cmd.WaitDelay != 5*time.Second {
		t.Errorf("b.cmd.WaitDelay = %v, want %v", b.cmd.WaitDelay, 5*time.Second)
	}
}

// A JSON line maps to its declared level; an unknown or missing level and plain text fall back to keywords.
func TestClassifyStderrLevel(t *testing.T) {
	cases := []struct {
		name string
		line string
		want slog.Level
	}{
		{"json_warn", `{"level":"WARN"}`, slog.LevelWarn},
		{"json_error", `{"level":"ERROR"}`, slog.LevelError},
		{"json_debug", `{"level":"DEBUG"}`, slog.LevelDebug},
		{"json_unknown_level_is_info", `{"level":"trace"}`, slog.LevelInfo},
		{"json_without_level_field_is_info", `{"msg":"hello"}`, slog.LevelInfo},
		{"plain_error_keyword_is_error", "error: boom", slog.LevelError},
		{"plain_no_keyword_is_info", "all good here", slog.LevelInfo},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyStderrLevel(tc.line)
			if got != tc.want {
				t.Errorf("classifyStderrLevel(%q) = %v, want %v", tc.line, got, tc.want)
			}
		})
	}
}

// matchesKeyword matches only at a word boundary: a preceding letter is a substring; a non-letter before, the end,
// or a separator after are boundaries.
func TestMatchesKeyword(t *testing.T) {
	const kw = "error"
	cases := []struct {
		name string
		low  string
		want bool
	}{
		{"keyword_at_start_then_separator", "error:", true},
		{"preceded_by_letter_a_is_substring", "aerror", false},
		{"preceded_by_letter_z_is_substring", "zerror", false},
		{"preceded_by_dot_is_boundary", "..error", true},
		{"preceded_by_brace_is_boundary", "a{error", true},
		{"trailing_letter_no_match", ".errorx", false},
		{"keyword_at_end_of_string", ".error", true},
		{"trailing_colon_is_boundary", ".error:", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := matchesKeyword(tc.low, kw)
			if got != tc.want {
				t.Errorf("matchesKeyword(%q, %q) = %v, want %v", tc.low, kw, got, tc.want)
			}
		})
	}
}

// '[', ']', ' ' and '=' each end the keyword.
func TestMatchesKeyword_SeparatorChain(t *testing.T) {
	const kw = "error"
	cases := []struct {
		name string
		low  string
		want bool
	}{
		{"open_bracket_is_boundary", "error[x", true},
		{"close_bracket_is_boundary", "error]x", true},
		{"space_is_boundary", "error x", true},
		{"equals_is_boundary", "error=x", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchesKeyword(tc.low, kw); got != tc.want {
				t.Errorf("matchesKeyword(%q, %q) = %v, want %v", tc.low, kw, got, tc.want)
			}
		})
	}
}

// The exit drain logs how many in-flight calls it answered, and nothing when none: session/prompt has no deadline,
// so a wedged kiro-cli is otherwise one prompt line then silence.
func TestExitPath_ReportsTheStrandedCalls(t *testing.T) {
	t.Run("names the count", func(t *testing.T) {
		c := capture.Default(t)
		b := NewEngine("/nonexistent", "/work")
		for _, id := range []int64{1, 2} {
			b.pending[id] = &pendingEntry{ch: make(chan pendingReply, 1), lane: b.engineLane}
		}

		b.exitPath()

		if !c.HasAttr("in flight", "failed_requests", "2") {
			t.Errorf(`the exit drain did not report failed_requests=2; the count failPending
already computes is being discarded`)
		}
	})

	t.Run("silent on an idle exit", func(t *testing.T) {
		c := capture.Default(t)
		b := NewEngine("/nonexistent", "/work")

		b.exitPath()

		if c.Count("in flight") > 0 {
			t.Errorf("the exit drain logged on an idle teardown; every tab close would carry the line")
		}
	})
}

// readLoop logs "ACP read" on a non-EOF scanner error and ends the engine.
func TestReadLoop_LogsACPReadOnScanError(t *testing.T) {
	c := capture.Default(t)
	b := readLoopEngine(errReader{failErr: errors.New("read boom")})

	b.readLoop()
	select {
	case <-b.done:
	case <-time.After(time.Second):
		t.Fatal("readLoop did not end the engine (done not closed)")
	}
	if got := b.Exit().Reason; got != exitReadError {
		t.Errorf("exit reason = %q, want %q", got, exitReadError)
	}

	if c.CountExact("ACP read") == 0 {
		t.Errorf(`readLoop on a scanner error did not log "ACP read"; want it present`)
	}
}

// readLoop logs nothing on a clean EOF.
func TestReadLoop_NoACPReadOnCleanEOF(t *testing.T) {
	c := capture.Default(t)
	b := readLoopEngine(errReader{failErr: io.EOF})

	b.readLoop()
	select {
	case <-b.done:
	case <-time.After(time.Second):
		t.Fatal("readLoop did not end the engine (done not closed)")
	}
	if got := b.Exit().Reason; got != ExitEnded {
		t.Errorf("exit reason = %q, want %q: the engine ended itself", got, ExitEnded)
	}

	if c.CountExact("ACP read") > 0 {
		t.Errorf(`readLoop on a clean EOF logged "ACP read"; want it absent`)
	}
}

// Notify returns the context error and writes nothing when ctx is already canceled.
func TestNotify_CanceledCtxReturnsErrNoWrite(t *testing.T) {
	b := NewEngine("/nonexistent", "/work")
	w := &captureWriter{}
	b.stdin.Store(&stdinPipe{w: w})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := b.Notify(ctx, "session/update", nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Notify(canceled ctx) err = %v, want context.Canceled", err)
	}
	if w.wrote() {
		t.Errorf("Notify(canceled ctx) wrote to stdin; want no write")
	}
}

// Notify marshals and writes a frame on the happy path.
func TestNotify_GoodCtxValidParamsWritesFrame(t *testing.T) {
	b := NewEngine("/nonexistent", "/work")
	w := &captureWriter{}
	b.stdin.Store(&stdinPipe{w: w})

	if err := b.Notify(t.Context(), "session/update", map[string]any{"k": "v"}); err != nil {
		t.Fatalf("Notify(good ctx, valid params) err = %v, want nil", err)
	}
	if !w.wrote() {
		t.Errorf("Notify(good ctx, valid params) wrote nothing; want a frame written to stdin")
	}
}

// Notify returns the marshal error and writes nothing for unmarshalable params.
func TestNotify_MarshalErrorReturnsErrNoWrite(t *testing.T) {
	b := NewEngine("/nonexistent", "/work")
	w := &captureWriter{}
	b.stdin.Store(&stdinPipe{w: w})

	err := b.Notify(t.Context(), "session/update", map[string]any{"bad": make(chan int)})
	if err == nil {
		t.Errorf("Notify(unmarshalable params) err = nil, want a marshal error")
	}
	if w.wrote() {
		t.Errorf("Notify(unmarshalable params) wrote to stdin; want no write")
	}
}

// session/new carries the chat's model and effort level in _meta.kiro. KAS starts on `auto`, which has no tiers, so a
// level set later is dropped (2.19.1) and the first prompt runs the default tier.
func TestNewSession_SendsTheModelAndEffortInSessionMeta(t *testing.T) {
	b := NewEngine("/nonexistent", "/work")
	sent, _, err := runNewSession(t, b,
		&marotte.SessionOpts{Model: "claude-opus-5", Effort: "max"},
		&marotte.RPCResponse{Result: json.RawMessage(
			`{"sessionId":"acp-session-xyz","configOptions":[` +
				`{"id":"model","currentValue":"claude-opus-5","options":[{"value":"claude-opus-5","name":"O5","_meta":{"kiro":{"rateMultiplier":1}}}]},` +
				`{"id":"effortLevel","currentValue":"max","options":[{"value":"max","name":"max"}]}]}`,
		)})
	if err != nil {
		t.Fatalf("newSession returned error: %v", err)
	}

	first, _, _ := bytes.Cut(sent, []byte("\n"))
	for _, want := range []string{`"modelId":"claude-opus-5"`, `"effortLevel":"max"`} {
		if !bytes.Contains(first, []byte(want)) {
			t.Errorf("session/new params carry no %s; the frame was:\n%s", want, first)
		}
	}
	// Nothing follows: the result reported both, so neither repair sends.
	if _, rest, found := bytes.Cut(sent, []byte("\n")); found && len(bytes.TrimSpace(rest)) != 0 {
		t.Errorf("session/new made follow-up calls after the door reported a match:\n%s", rest)
	}
}

// A build that ignores the meta keys still converges through the repair.
func TestNewSession_RepairsWhenTheDoorWasIgnored(t *testing.T) {
	b := NewEngine("/nonexistent", "/work")
	sent, _, err := runNewSession(t, b,
		&marotte.SessionOpts{Effort: "max"},
		// The session came back at the model's default, not max.
		&marotte.RPCResponse{Result: json.RawMessage(
			`{"sessionId":"acp-session-xyz","configOptions":[{"id":"effortLevel","currentValue":"high","options":[{"value":"high"},{"value":"max"}]}]}`,
		)})
	if err != nil {
		t.Fatalf("newSession returned error: %v", err)
	}

	_, rest, _ := bytes.Cut(sent, []byte("\n"))
	if !bytes.Contains(rest, []byte(`"configId":"effortLevel"`)) || !bytes.Contains(rest, []byte(`"value":"max"`)) {
		t.Errorf("newSession did not repair the level the result reported; later frames were:\n%s", rest)
	}
}

// A malformed level never reaches the door (config.json is user-editable). Shape only: a well-formed unknown tier
// flows.
func TestNewSession_OmitsAMalformedLevelFromTheDoor(t *testing.T) {
	b := NewEngine("/nonexistent", "/work")
	sent, _, err := runNewSession(t, b,
		&marotte.SessionOpts{Effort: "TURBO"},
		&marotte.RPCResponse{Result: json.RawMessage(`{"sessionId":"acp-session-xyz"}`)})
	if err != nil {
		t.Fatalf("newSession returned error: %v", err)
	}
	if bytes.Contains(sent, []byte("effortLevel")) {
		t.Errorf("a malformed level reached the wire; frames were:\n%s", sent)
	}
}

// A well-formed tier marotte does not name still reaches the door: gpt-luna ships "none", and the catalog is
// upstream's.
func TestNewSession_SendsAWellFormedUnknownLevel(t *testing.T) {
	b := NewEngine("/nonexistent", "/work")
	sent, _, err := runNewSession(t, b,
		&marotte.SessionOpts{Effort: "none"},
		&marotte.RPCResponse{Result: json.RawMessage(`{"sessionId":"acp-session-xyz"}`)})
	if err != nil {
		t.Fatalf("newSession returned error: %v", err)
	}
	if !bytes.Contains(sent, []byte(`"effortLevel":"none"`)) {
		t.Errorf("a well-formed tier did not reach the session door; frames were:\n%s", sent)
	}
}

// loadSession applies a well-formed result: the parsed model, not the fallback.
func TestLoadSession_AppliesParsedResult(t *testing.T) {
	b := NewEngine("/nonexistent", "/work")
	resp := &marotte.RPCResponse{
		Result: json.RawMessage(`{"sessionId":"acp-session-xyz","configOptions":[{"id":"model","currentValue":"parsed-model","options":[{"value":"parsed-model","name":"Parsed","description":"ok","_meta":{"kiro":{"rateMultiplier":1}}}]}]}`),
	}
	_, s, err := runLoadSessionOpts(t, b, &marotte.SessionOpts{SessionID: "acp-session-xyz", Model: "fb-model"}, resp)
	if err != nil {
		t.Fatalf("loadSession returned error: %v", err)
	}
	if got := s.ModelID(); got != "parsed-model" {
		t.Errorf("loadSession ModelID() = %q, want %q (parsed result must be applied, not fallback)", got, "parsed-model")
	}
}

// LoadSession records its lane's position at the response, the only thing a replay can be ordered against. 0, or a
// position read afterwards, would make completion trivially true and adopt a partial transcript.
func TestLoadSession_RecordsTheResponsePosition(t *testing.T) {
	e := NewEngine("/nonexistent", "/work")
	e.lifecycleCtx = context.Background()
	e.stdin.Store(&stdinPipe{w: &captureWriter{}})
	type loaded struct {
		s   *Session
		err error
	}
	done := make(chan loaded, 1)
	go func() {
		s, err := e.LoadSession(t.Context(), &marotte.SessionOpts{SessionID: "acp-session-xyz"}, nil)
		done <- loaded{s, err}
	}()
	waitPending(t, e, 1)
	var entry *pendingEntry
	e.pendingMu.Lock()
	for id, v := range e.pending {
		entry = v
		delete(e.pending, id)
	}
	e.pendingMu.Unlock()
	// Seven replay frames reach the session's lane before the result, as a short transcript's load.
	for range 7 {
		entry.lane.push(&marotte.RPCResponse{Method: marotte.MethodSessionUpdate})
	}
	entry.ch <- pendingReply{resp: &marotte.RPCResponse{Result: json.RawMessage(`{}`)}, seq: entry.lane.position()}
	waitPending(t, e, 1)
	for {
		select {
		case got := <-done:
			if got.err != nil {
				t.Fatalf("LoadSession: %v", got.err)
			}
			if pos := got.s.SessionLoadSeq(); pos != 7 {
				t.Errorf("SessionLoadSeq() = %d, want 7 (the position the load result arrived at)", pos)
			}
			return
		default:
		}
		e.pendingMu.Lock()
		for id, v := range e.pending {
			delete(e.pending, id)
			v.ch <- pendingReply{resp: &marotte.RPCResponse{Result: json.RawMessage(`{}`)}}
		}
		e.pendingMu.Unlock()
		time.Sleep(time.Millisecond)
	}
}

// session/new leaves the load position at zero: there is no replay.
func TestNewSession_LeavesTheLoadPositionUnset(t *testing.T) {
	b := NewEngine("/nonexistent", "/work")
	resp := &marotte.RPCResponse{Result: json.RawMessage(`{"sessionId":"fresh"}`)}
	_, s, err := runNewSession(t, b, &marotte.SessionOpts{}, resp)
	if err != nil {
		t.Fatalf("newSession returned error: %v", err)
	}
	if got := s.SessionLoadSeq(); got != 0 {
		t.Errorf("SessionLoadSeq() = %d after session/new, want 0", got)
	}
}

// loadSession warns and falls back when the result cannot be parsed.
func TestLoadSession_WarnsOnUnparseableResult(t *testing.T) {
	c := capture.Default(t)
	b := NewEngine("/nonexistent", "/work")
	resp := &marotte.RPCResponse{Result: json.RawMessage(`{"sessionId":"x"`)} // truncated -> parse error
	if err := runLoadSession(t, b, "fb-model", resp); err != nil {
		t.Fatalf("loadSession returned error: %v", err)
	}
	if c.CountExact("session/load: unparseable result, using fallback") == 0 {
		t.Errorf("loadSession on an unparseable result did not log the fallback warn; want it present")
	}
}

// An unparseable result fills an empty model from the fallback (the `b.modelID == ""` gate).
func TestLoadSession_FallbackModelAppliedOnUnparseableResult(t *testing.T) {
	b := NewEngine("/nonexistent", "/work")
	resp := &marotte.RPCResponse{Result: json.RawMessage(`{"sessionId":"x"`)} // truncated -> parse error
	_, s, err := runLoadSessionOpts(t, b, &marotte.SessionOpts{SessionID: "acp-session-xyz", Model: "fb-model"}, resp)
	if err != nil {
		t.Fatalf("loadSession returned error: %v", err)
	}
	if got := s.ModelID(); got != "fb-model" {
		t.Errorf("loadSession ModelID() = %q, want %q (an empty model must be filled from the fallback on an unparseable result)", got, "fb-model")
	}
}

// A session resolving settings.workflows against marotte's declaration is logged. The door depends on
// KiroSessionMetaSchema's `.passthrough()`; if that goes, the agent silently loses workflowChatTools.
func TestApplySessionResult_ReportsAWorkflowsDisagreement(t *testing.T) {
	for name, tc := range map[string]struct {
		result   string
		wantWarn bool
	}{
		// Workflows is declared on, so a resolved false is the failure.
		"resolved false against a declared true": {
			result:   `{"sessionId":"s","_meta":{"workflowsEnabled":false}}`,
			wantWarn: true,
		},
		"agreement is silent": {
			result:   `{"sessionId":"s","_meta":{"workflowsEnabled":true}}`,
			wantWarn: false,
		},
		// An absent member says nothing; warning would fire against every older KAS.
		"an absent member is not a disagreement": {
			result:   `{"sessionId":"s","_meta":{}}`,
			wantWarn: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := capture.Default(t)
			b := NewEngine("/nonexistent", "/work")
			if _, _, err := runLoadSessionOpts(t, b,
				&marotte.SessionOpts{SessionID: "acp-session-xyz", Features: marotte.SessionFeatures{Workflows: true}},
				&marotte.RPCResponse{Result: json.RawMessage(tc.result)}); err != nil {
				t.Fatalf("loadSession returned error: %v", err)
			}
			got := c.CountExact("session resolved the workflows setting against what marotte declared; "+
				"the agent's workflow tools are not what this session asked for") > 0
			if got != tc.wantWarn {
				t.Errorf("warn logged = %v, want %v", got, tc.wantWarn)
			}
		})
	}
}

// A load result omitting the `model` option or the `modes` block keeps the previous catalog. Common: KAS resolves
// models asynchronously, so the catalog arrives later on config_option_update (kiro-cli 2.20.0), and an expired
// token gives the same shape. The keep rests on a `continue` a refactor could drop.
func TestLoadSession_AbsentCatalogKeepsThePreviousOne(t *testing.T) {
	b := NewEngine("/nonexistent", "/work")

	// Seed the catalog as a live session does, then resume.
	seeded := &marotte.RPCResponse{
		Result: json.RawMessage(`{"sessionId":"acp-session-xyz",` +
			`"modes":{"currentModeId":"vibe","availableModes":[{"id":"vibe","name":"Default"}]},` +
			`"configOptions":[{"id":"model","currentValue":"seeded-model","options":[` +
			`{"value":"seeded-model","name":"Seeded","description":"ok"},` +
			`{"value":"gone-model","name":"Gone","description":"[Deprecated] retired"}]}]}`),
	}
	_, s, err := runNewSession(t, b, &marotte.SessionOpts{}, seeded)
	if err != nil {
		t.Fatalf("seeding newSession returned error: %v", err)
	}
	if len(s.Models()) != 1 || len(s.Catalog()) != 2 || len(s.Modes()) != 1 {
		t.Fatalf("seed did not take: models=%v catalog=%v modes=%v", s.Models(), s.Catalog(), s.Modes())
	}

	// Absent and empty are guarded by different code: `r.Modes != nil` and the length check.
	for name, result := range map[string]string{
		// The measured 2.20.0 shape: three options, no `model`, no modes block.
		"no block at all": `{"sessionId":"acp-session-xyz","configOptions":[` +
			`{"id":"mode","currentValue":"vibe"},` +
			`{"id":"autopilot","currentValue":"on"},` +
			`{"id":"contentCollection","currentValue":"on"}]}`,
		// A block reporting no catalog rather than an empty one.
		"empty block": `{"sessionId":"acp-session-xyz",` +
			`"modes":{"currentModeId":"vibe","availableModes":[]},` +
			`"configOptions":[{"id":"model","currentValue":"seeded-model","options":[]}]}`,
		// kiro-cli 2.32.0's cold pin: the saved model as the only choice while KAS's catalog is empty.
		"cold-catalog pin": `{"sessionId":"acp-session-xyz","configOptions":[` +
			`{"id":"model","currentValue":"seeded-model","options":[{"value":"seeded-model","name":"Seeded"}]},` +
			`{"id":"effortLevel","currentValue":"high","options":[{"value":"high","name":"High"}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			// A second result on the same handle, as a load result folds over what the session holds.
			s.adoptLoadedSession("", &marotte.RPCResponse{Result: json.RawMessage(result)})

			if got := s.Models(); len(got) != 1 || got[0].ID != "seeded-model" {
				t.Errorf("Models() = %v, want the seeded catalog kept (an absent option is not an empty one)", got)
			}
			if got := s.Catalog(); len(got) != 2 {
				t.Errorf("Catalog() = %v, want the seeded unfiltered catalog kept: ApplyServedModels derives the entitlement set from it, so emptying it refuses a model the account holds", got)
			}
			if got := s.Modes(); len(got) != 1 || got[0].ID != "vibe" {
				t.Errorf("Modes() = %v, want the seeded mode list kept; nothing refreshes modes afterwards, so this loss is permanent for the session", got)
			}
		})
	}
}

// A resume re-applies the chat's effort: nothing reconciles Chat.Effort, so a max chat resumed at KAS's level while
// the pill said max.
func TestLoadSession_ReAppliesTheChatsEffort(t *testing.T) {
	b := NewEngine("/nonexistent", "/work")
	resp := &marotte.RPCResponse{Result: json.RawMessage(`{"sessionId":"acp-session-xyz"}`)}

	sent, _, err := runLoadSessionOpts(t, b,
		&marotte.SessionOpts{SessionID: "acp-session-xyz", Effort: "max"}, resp)
	if err != nil {
		t.Fatalf("loadSession returned error: %v", err)
	}

	if !bytes.Contains(sent, []byte(`"configId":"effortLevel"`)) {
		t.Errorf("session/load sent no effortLevel option; frames were:\n%s", sent)
	}
	if !bytes.Contains(sent, []byte(`"value":"max"`)) {
		t.Errorf("session/load sent no max level; frames were:\n%s", sent)
	}
}

// A chat with no level sends no effort option on resume.
func TestLoadSession_SendsNoEffortWhenTheChatChoseNone(t *testing.T) {
	b := NewEngine("/nonexistent", "/work")
	resp := &marotte.RPCResponse{Result: json.RawMessage(`{"sessionId":"acp-session-xyz"}`)}

	sent, _, err := runLoadSessionOpts(t, b,
		&marotte.SessionOpts{SessionID: "acp-session-xyz"}, resp)
	if err != nil {
		t.Fatalf("loadSession returned error: %v", err)
	}

	if bytes.Contains(sent, []byte("effortLevel")) {
		t.Errorf("session/load sent an effortLevel option for a chat that chose no level; frames were:\n%s", sent)
	}
}

// A malformed level never reaches the wire (config.json is user-editable); well-formed tiers flow whatever their name.
func TestEnsureEffort_DropsAMalformedLevel(t *testing.T) {
	for _, level := range []string{"", "HIGH", "max ", "9max"} {
		t.Run(level, func(t *testing.T) {
			b, s := engineWithSession()
			sent, err := driveSessionCall(t, b, &marotte.RPCResponse{Result: json.RawMessage(`{}`)}, false,
				func(ctx context.Context) error { return s.EnsureEffort(ctx, level) })
			if err != nil {
				t.Errorf("EnsureEffort(%q) = %v, want nil", level, err)
			}
			if len(sent) != 0 {
				t.Errorf("EnsureEffort(%q) reached the wire; frames were:\n%s", level, sent)
			}
		})
	}
}

// A level the session already reports costs no round trip, so the prompt path can call this per prompt.
func TestEnsureEffort_SkipsTheLevelTheSessionReports(t *testing.T) {
	b, s := engineWithSession()
	s.mu.Lock()
	s.effortLevel = "max"
	s.mu.Unlock()

	sent, err := driveSessionCall(t, b, &marotte.RPCResponse{Result: json.RawMessage(`{}`)}, false,
		func(ctx context.Context) error { return s.EnsureEffort(ctx, "max") })
	if err != nil {
		t.Fatalf("EnsureEffort(the reported level) = %v, want nil", err)
	}
	if len(sent) != 0 {
		t.Errorf("EnsureEffort sent a call for the level already reported; frames were:\n%s", sent)
	}
}

// A model swap clears the cached level so the next EnsureEffort asserts. On 2.19.1 a same-tier swap keeps it, a
// swap to `auto` drops it, and the session sees neither.
func TestSetModel_ClearsTheCachedEffortLevel(t *testing.T) {
	b, s := engineWithSession()
	s.mu.Lock()
	s.effortLevel = "max"
	s.mu.Unlock()

	sent, err := driveSessionCall(t, b, &marotte.RPCResponse{Result: json.RawMessage(`{}`)}, false,
		func(ctx context.Context) error { return s.SetModel(ctx, "claude-sonnet-5") })
	if err != nil {
		t.Fatalf("SetModel returned error: %v", err)
	}
	if !bytes.Contains(sent, []byte(`"configId":"model"`)) {
		t.Fatalf("SetModel sent no model option; frames were:\n%s", sent)
	}

	s.mu.Lock()
	got := s.effortLevel
	s.mu.Unlock()
	if got != "" {
		t.Errorf("cached effort level after a swap = %q, want it cleared", got)
	}
}

// ObserveEffort is the other channel the level is reported on. Here the door asked max and got it, then KAS's
// first-prompt pin moved the session to high on config_option_update; comparing max to max skipped the repair.
func TestObserveEffort_MakesTheNextEnsureAssert(t *testing.T) {
	b, s := engineWithSession()
	s.mu.Lock()
	s.effortLevel = "max" // what the session door asked for and the reply confirmed
	s.mu.Unlock()

	s.ObserveEffort("high") // what the notification says it is running at now

	sent, err := driveSessionCall(t, b, &marotte.RPCResponse{Result: json.RawMessage(`{}`)}, false,
		func(ctx context.Context) error { return s.EnsureEffort(ctx, "max") })
	if err != nil {
		t.Fatalf("EnsureEffort after an observation = %v, want nil", err)
	}
	if !bytes.Contains(sent, []byte(`"configId":"effortLevel"`)) || !bytes.Contains(sent, []byte(`"value":"max"`)) {
		t.Errorf("EnsureEffort did not re-assert the level after the session reported another; frames were:\n%s", sent)
	}
}

// An absent report keeps the previous value, matching applyEffortConfigOptionLocked.
func TestObserveEffort_IgnoresAnEmptyReport(t *testing.T) {
	_, s := engineWithSession()
	s.mu.Lock()
	s.effortLevel = "max"
	s.mu.Unlock()

	s.ObserveEffort("")

	s.mu.Lock()
	got := s.effortLevel
	s.mu.Unlock()
	if got != "max" {
		t.Errorf("cached level after an empty report = %q, want it left at %q", got, "max")
	}
}

// TestInitialize_DeclaresTheEngineCapabilities pins that the one engine always declares _meta.kiro.hooks
// {enabled:true,v2:true}, so KAS autofires .kiro/hooks/*.json itself, beside openExternalUrl and infrastructureSafety.
func TestInitialize_DeclaresTheEngineCapabilities(t *testing.T) {
	// A fake kiro-cli that appends the raw initialize request to $INIT_CAPTURE.
	script := `#!/bin/sh
while IFS= read -r line; do
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(echo "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  sid=$(echo "$line" | sed -n 's/.*"overrideSessionId":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize)
      printf '%s\n' "$line" >> "$INIT_CAPTURE"
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"serverInfo":{"name":"fake"}}}\n' "$id"
      ;;
    session/new)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"%s","modes":{"currentModeId":"default","availableModes":[{"id":"default","name":"Default","description":"d"}]},"configOptions":[{"id":"model","currentValue":"m","options":[{"value":"m","name":"M","description":"x","_meta":{"kiro":{"rateMultiplier":1.0}}}]}]}}\n' "$id" "$sid"
      ;;
    *)
      if [ -n "$id" ]; then printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"; fi
      ;;
  esac
done
`
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "fake-kiro-cli")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	run := func(t *testing.T) string {
		t.Helper()
		capture := filepath.Join(t.TempDir(), "init.jsonl")
		t.Setenv("INIT_CAPTURE", capture)
		b := NewEngine(scriptPath, dir)
		// Knowledge on, so the per-key loop below has a true to check.
		if err := b.Start(t.Context(), &marotte.EngineOpts{Lifetime: context.Background(), Knowledge: true}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		data, err := os.ReadFile(capture)
		if err != nil {
			t.Fatalf("read init capture: %v", err)
		}
		return string(data)
	}

	t.Run("declares v2 hooks opt-in", func(t *testing.T) {
		got := run(t)
		if !strings.Contains(got, `"hooks":{"enabled":true,"v2":true}`) {
			t.Errorf("initialize missing hooks opt-in; got: %s", got)
		}
		if !strings.Contains(got, `"openExternalUrl":true`) || !strings.Contains(got, `"infrastructureSafety":true`) {
			t.Errorf("initialize missing base kiro capabilities; got: %s", got)
		}
		// Each settings key is asserted separately: an exact substring breaks on additions and once hid two missing keys,
		// and each key gates a different KAS subsystem, silently when absent. `workflows` is per session and lives on the
		// session door.
		for _, key := range []string{"codeIntelligence", "knowledge", "goal"} {
			if !strings.Contains(got, `"`+key+`":{"enabled":true}`) {
				t.Errorf("initialize missing the %s settings opt-in; got: %s", key, got)
			}
		}
		// KAS reads both flags with a strict `=== true` at _meta.kiro's top level and fails silently: without
		// backgroundProcesses the process tools vanish, without knowledge the prompt lists no knowledge base.
		if !strings.Contains(got, `"backgroundProcesses":true`) {
			t.Errorf("initialize missing the background-process opt-in; got: %s", got)
		}
		if !strings.Contains(got, `"knowledge":true`) {
			t.Errorf("initialize missing the knowledge opt-in; got: %s", got)
		}
	})
}

// sessionDoorScript is a fake kiro-cli that appends every request to $RPC_CAPTURE, one JSON line each, so a test
// inspects the session call itself: initialize carries its own _meta.kiro.
const sessionDoorScript = `#!/bin/sh
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$RPC_CAPTURE"
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(echo "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  sid=$(echo "$line" | sed -n 's/.*"overrideSessionId":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"serverInfo":{"name":"fake"}}}\n' "$id"
      ;;
    session/new|session/load)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"%s","modes":{"currentModeId":"default","availableModes":[{"id":"default","name":"Default","description":"d"}]},"configOptions":[{"id":"model","currentValue":"m","options":[{"value":"m","name":"M","description":"x","_meta":{"kiro":{"rateMultiplier":1.0}}}]}]}}\n' "$id" "$sid"
      ;;
    *)
      if [ -n "$id" ]; then printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"; fi
      ;;
  esac
done
`

// openSessionOn starts e over its script and opens one session with opts: a load when opts names a session, else a
// new one. The engine stops at cleanup.
func openSessionOn(t *testing.T, e *Engine, opts *marotte.SessionOpts) (*Session, error) {
	t.Helper()
	return openSessionWith(t, e, &marotte.EngineOpts{}, opts)
}

// openSessionWith is openSessionOn with the engine's own options; their Lifetime is set here.
func openSessionWith(t *testing.T, e *Engine, eo *marotte.EngineOpts, opts *marotte.SessionOpts) (*Session, error) {
	t.Helper()
	eo.Lifetime = context.Background()
	if err := e.Start(t.Context(), eo); err != nil {
		return nil, err
	}
	t.Cleanup(e.Stop)
	if opts.SessionID != "" {
		return e.LoadSession(t.Context(), opts, nil)
	}
	return e.NewSession(t.Context(), opts, nil)
}

// alsoContains narrows a repeated method (set_config_option carries model, effort and autopilot).
func captureRequest(t *testing.T, method string, opts *marotte.SessionOpts, alsoContains ...string) string {
	t.Helper()
	return captureEngineRequest(t, method, &marotte.EngineOpts{}, opts, alsoContains...)
}

// captureEngineRequest is captureRequest with the engine's own options.
func captureEngineRequest(t *testing.T, method string, eo *marotte.EngineOpts, opts *marotte.SessionOpts, alsoContains ...string) string {
	t.Helper()
	data := captureEngineRequests(t, eo, opts)
	needles := append([]string{`"method":"` + method + `"`}, alsoContains...)
	for line := range strings.SplitSeq(strings.TrimSpace(data), "\n") {
		matched := true
		for _, needle := range needles {
			if !strings.Contains(line, needle) {
				matched = false
				break
			}
		}
		if matched {
			return line
		}
	}
	t.Fatalf("no %s request matching %q in the capture; got:\n%s", method, alsoContains, data)
	return ""
}

// captureRequests returns the whole capture, for asserting a call the start must not make.
func captureRequests(t *testing.T, opts *marotte.SessionOpts) string {
	t.Helper()
	return captureEngineRequests(t, &marotte.EngineOpts{}, opts)
}

// captureEngineRequests is captureRequests with the engine's own options.
func captureEngineRequests(t *testing.T, eo *marotte.EngineOpts, opts *marotte.SessionOpts) string {
	t.Helper()
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "fake-kiro-cli")
	if err := os.WriteFile(scriptPath, []byte(sessionDoorScript), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}
	t.Setenv("HOME", t.TempDir())
	capturePath := filepath.Join(t.TempDir(), "rpc.jsonl")
	t.Setenv("RPC_CAPTURE", capturePath)

	b := NewEngine(scriptPath, dir)
	if _, err := openSessionWith(t, b, eo, opts); err != nil {
		t.Fatalf("open session: %v", err)
	}
	b.Stop()

	data, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read rpc capture: %v", err)
	}
	return string(data)
}

// digObject walks a captured request down nested objects, failing at the first absent level: a key at the wrong
// depth passes strings.Contains and resolves to nothing in KAS.
func digObject(t *testing.T, what, line string, levels ...string) map[string]any {
	t.Helper()
	var req map[string]any
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		t.Fatalf("captured request is not JSON: %v\n%s", err, line)
	}
	node := req
	for _, level := range levels {
		next, ok := node[level].(map[string]any)
		if !ok {
			t.Fatalf("captured request has no %s object (%T); %s is missing or misnested:\n%s",
				level, node[level], what, line)
		}
		node = next
	}
	return node
}

func metaKiroSettings(t *testing.T, line string) map[string]any {
	t.Helper()
	return digObject(t, "the session door's block", line, "params", "_meta", "kiro", "settings")
}

// TestSessionNewCarriesWorkflowsAtSessionDoor pins the workflows opt-in on session/new at KAS's depth. KAS resolves
// it only per session, so on initialize it resolved false and the workflow tools silently vanished.
func TestSessionNewCarriesWorkflowsAtSessionDoor(t *testing.T) {
	line := captureRequest(t, "session/new", &marotte.SessionOpts{
		Model: "m", Features: marotte.SessionFeatures{Workflows: true},
	})
	settings := metaKiroSettings(t, line)
	got, ok := settings["workflows"].(map[string]any)
	if !ok {
		t.Fatalf(`session/new carried no workflows settings entry (%T); the row left the session
door. Captured:
%s`, settings["workflows"], line)
	}
	// An object: isSettingEnabled reads val.enabled, so `workflows: true` reads as disabled.
	if got["enabled"] != true {
		t.Errorf("session/new sent workflows=%v, want {\"enabled\":true}", got)
	}
}

// TestSessionLoadCarriesWorkflowsAtSessionDoor pins the same key on session/load: KAS falls back to the value
// persisted at creation, so a new-only key leaves resumed chats without the tools.
func TestSessionLoadCarriesWorkflowsAtSessionDoor(t *testing.T) {
	line := captureRequest(t, "session/load", &marotte.SessionOpts{
		Model: "m", SessionID: "sess_resume_door",
		Features: marotte.SessionFeatures{Workflows: true},
	})
	settings := metaKiroSettings(t, line)
	got, ok := settings["workflows"].(map[string]any)
	if !ok {
		t.Fatalf(`session/load carried no workflows settings entry (%T); a resumed chat would
lose a capability a fresh one has. Captured:
%s`, settings["workflows"], line)
	}
	if got["enabled"] != true {
		t.Errorf("session/load sent workflows=%v, want {\"enabled\":true}", got)
	}
}

// TestSessionDoorCarriesDisableAutoCompaction pins SessionOpts.DisableAutoCompaction on both verbs in both states.
func TestSessionDoorCarriesDisableAutoCompaction(t *testing.T) {
	for _, method := range []string{"session/new", "session/load"} {
		for _, disabled := range []bool{false, true} {
			opts := &marotte.SessionOpts{Model: "m", DisableAutoCompaction: disabled}
			if method == "session/load" {
				opts.SessionID = "sess_resume_door"
			}
			settings := metaKiroSettings(t, captureRequest(t, method, opts))
			got, ok := settings["disableAutoCompaction"].(map[string]any)
			if !ok || got["enabled"] != disabled {
				t.Errorf("%s with DisableAutoCompaction=%v sent disableAutoCompaction=%v, want {\"enabled\":%v}",
					method, disabled, settings["disableAutoCompaction"], disabled)
			}
		}
	}
}

func TestAutoCompactionDisabled_ReportsTheSentValue(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		dir := t.TempDir()
		scriptPath := filepath.Join(dir, "fake-kiro-cli")
		if err := os.WriteFile(scriptPath, []byte(sessionDoorScript), 0o755); err != nil {
			t.Fatalf("write fake script: %v", err)
		}
		t.Setenv("HOME", t.TempDir())
		t.Setenv("RPC_CAPTURE", filepath.Join(t.TempDir(), "rpc.jsonl"))
		b := NewEngine(scriptPath, dir)
		s, err := openSessionOn(t, b, &marotte.SessionOpts{Model: "m", DisableAutoCompaction: disabled})
		if err != nil {
			t.Fatalf("open session: %v", err)
		}
		if got := s.AutoCompactionDisabled(); got != disabled {
			t.Errorf("AutoCompactionDisabled() after a door with DisableAutoCompaction=%v = %v", disabled, got)
		}
		b.Stop()
	}
}

// TestSessionDoorWorkflowsFollowsTheSetting pins the Workflows setting on both verbs; off is sent as
// {"enabled":false}, since on load an absent key keeps the persisted value.
func TestSessionDoorWorkflowsFollowsTheSetting(t *testing.T) {
	for _, method := range []string{"session/new", "session/load"} {
		for _, on := range []bool{false, true} {
			opts := &marotte.SessionOpts{Model: "m", Features: marotte.SessionFeatures{Workflows: on}}
			if method == "session/load" {
				opts.SessionID = "sess_resume_door"
			}
			settings := metaKiroSettings(t, captureRequest(t, method, opts))
			got, ok := settings["workflows"].(map[string]any)
			if !ok || got["enabled"] != on {
				t.Errorf("%s with Workflows=%v sent workflows=%v, want {\"enabled\":%v}",
					method, on, settings["workflows"], on)
			}
		}
	}
}

// TestSessionDoorCarriesBackgroundExecution pins settings.backgroundExecution as {"enabled":true} on both verbs.
func TestSessionDoorCarriesBackgroundExecution(t *testing.T) {
	for _, method := range []string{"session/new", "session/load"} {
		opts := &marotte.SessionOpts{Model: "m"}
		if method == "session/load" {
			opts.SessionID = "sess_resume_door"
		}
		settings := metaKiroSettings(t, captureRequest(t, method, opts))
		got, ok := settings["backgroundExecution"].(map[string]any)
		if !ok || got["enabled"] != true {
			t.Errorf("%s sent backgroundExecution=%v, want {\"enabled\":true}", method, settings["backgroundExecution"])
		}
	}
}

// TestSessionVerbs_CarryClientSteering pins client steering on both verbs inside the door's _meta.kiro: KAS
// persists none of it.
func TestSessionVerbs_CarryClientSteering(t *testing.T) {
	doc := marotte.ClientSteeringDoc{Name: "marotte", Inclusion: "always", Content: "probe"}
	for _, method := range []string{"session/new", "session/load"} {
		opts := &marotte.SessionOpts{Model: "m", Steering: []marotte.ClientSteeringDoc{doc}}
		if method == "session/load" {
			opts.SessionID = "sess_resume_door"
		}
		line := captureRequest(t, method, opts)
		kiro := digObject(t, "the session door", line, "params", "_meta", "kiro")
		docs, ok := kiro["steering"].([]any)
		if !ok || len(docs) != 1 {
			t.Fatalf("%s carried steering=%v, want one doc:\n%s", method, kiro["steering"], line)
		}
		got, _ := docs[0].(map[string]any)
		if got["name"] != "marotte" || got["inclusion"] != "always" || got["content"] != "probe" {
			t.Errorf("%s steering[0] = %v, want the marotte doc", method, got)
		}
		if _, ok := kiro["settings"].(map[string]any); !ok {
			t.Errorf("%s lost the door's settings beside the steering:\n%s", method, line)
		}
	}
}

// TestSessionVerbs_NoSteeringSendsNoKey pins that a bridge without client steering sends no key, not [].
func TestSessionVerbs_NoSteeringSendsNoKey(t *testing.T) {
	for _, method := range []string{"session/new", "session/load"} {
		opts := &marotte.SessionOpts{Model: "m"}
		if method == "session/load" {
			opts.SessionID = "sess_resume_door"
		}
		line := captureRequest(t, method, opts)
		if strings.Contains(line, `"steering"`) {
			t.Errorf("%s carried a steering key with none set:\n%s", method, line)
		}
	}
}

// TestSessionDoor_ShellTypeMatchesTheResponder pins the door's shellType to the shell_type responder's answer.
func TestSessionDoor_ShellTypeMatchesTheResponder(t *testing.T) {
	if got := kascap.SessionMeta(&kascap.SessionSpawn{})["shellType"]; got != marotte.HostShellType {
		t.Errorf("session door shellType = %v, want marotte.HostShellType %q", got, marotte.HostShellType)
	}
	line := captureRequest(t, "session/new", &marotte.SessionOpts{Model: "m"})
	if kiro := digObject(t, "the session door", line, "params", "_meta", "kiro"); kiro["shellType"] != marotte.HostShellType {
		t.Errorf("session/new shellType = %v, want %q", kiro["shellType"], marotte.HostShellType)
	}
}

// TestApplySupervised_SendsTheStringKASDeclares pins autopilot as the declared string. On kiro-cli 2.20.0 a boolean
// gets -32602 and the session stays in autopilot. Asserted on bytes.
func TestApplySupervised_SendsTheStringKASDeclares(t *testing.T) {
	line := captureRequest(t, "session/set_config_option",
		&marotte.SessionOpts{Model: "m", Supervised: true},
		`"configId":"`+marotte.ConfigOptionAutopilot+`"`)
	if !strings.Contains(line, `"value":"`+marotte.ConfigValueAutopilotOff+`"`) {
		t.Errorf(`autopilot did not carry "value":%q on the wire; KAS refuses every other
shape with -32602 and leaves the session in autopilot. Captured:
%s`, marotte.ConfigValueAutopilotOff, line)
	}
	// And the decoded value is a string, so a look-alike spelling cannot pass the byte check alone.
	params := digObject(t, "the autopilot set_config_option params", line, "params")
	if got, ok := params[keyConfigValue].(string); !ok || got != marotte.ConfigValueAutopilotOff {
		t.Errorf("autopilot value = %#v (%T), want the string %q",
			params[keyConfigValue], params[keyConfigValue], marotte.ConfigValueAutopilotOff)
	}
}

// autopilotScript is sessionDoorScript plus an arm: with AUTOPILOT_REFUSE set it answers the autopilot
// set_config_option with -32602, as KAS does for unaccepted values.
const autopilotScript = `#!/bin/sh
while IFS= read -r line; do
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(echo "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  sid=$(echo "$line" | sed -n 's/.*"overrideSessionId":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"serverInfo":{"name":"fake"}}}\n' "$id"
      ;;
    session/new|session/load)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"%s"}}\n' "$id" "$sid"
      ;;
    *)
      if [ -n "$id" ]; then
        if [ -n "$AUTOPILOT_REFUSE" ] && echo "$line" | grep -q '"configId":"autopilot"'; then
          printf '{"jsonrpc":"2.0","id":%s,"error":{"code":-32602,"message":"Invalid params"}}\n' "$id"
        else
          printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"
        fi
      fi
      ;;
  esac
done
`

// TestApplySupervised_RecordsWhetherTheSessionTookIt pins the outcome the coordinator's refusal report reads. The
// third case is the honest zero: false also means nobody asked.
func TestApplySupervised_RecordsWhetherTheSessionTookIt(t *testing.T) {
	cases := []struct {
		name       string
		supervised bool
		refuse     bool
		want       bool
	}{
		{name: "an accepted assert is recorded", supervised: true, want: true},
		{name: "a refused assert is not recorded", supervised: true, refuse: true},
		{name: "a chat that asked for nothing records nothing", supervised: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			scriptPath := filepath.Join(dir, "fake-kiro-cli")
			if err := os.WriteFile(scriptPath, []byte(autopilotScript), 0o755); err != nil {
				t.Fatalf("write fake script: %v", err)
			}
			t.Setenv("HOME", t.TempDir())
			if tc.refuse {
				t.Setenv("AUTOPILOT_REFUSE", "1")
			}

			b := NewEngine(scriptPath, dir)
			s, err := openSessionOn(t, b, &marotte.SessionOpts{Supervised: tc.supervised})
			if err != nil {
				t.Fatalf("open session: %v", err)
			}

			// A refusal never fails the session.
			if got := s.SupervisedApplied(); got != tc.want {
				t.Errorf("SupervisedApplied() = %v, want %v; the coordinator reads this to decide "+
					"whether to tell the user the chat is running unsupervised", got, tc.want)
			}
		})
	}
}

// TestLoadSession_ReAssertsSupervised pins that a forked session copies no autopilot, so a supervised chat's tangent ran in
// autopilot (parent load reports "off", fork "on").
func TestLoadSession_ReAssertsSupervised(t *testing.T) {
	line := captureRequest(t, "session/set_config_option",
		&marotte.SessionOpts{SessionID: "sess_forked", Supervised: true},
		`"configId":"`+marotte.ConfigOptionAutopilot+`"`)
	if !strings.Contains(line, `"value":"`+marotte.ConfigValueAutopilotOff+`"`) {
		t.Errorf(`a supervised resume did not carry "value":%q; the forked session then runs
every turn without asking. Captured:
%s`, marotte.ConfigValueAutopilotOff, line)
	}
}

// An unsupervised resume sends nothing, or a restart would pin autopilot off unasked.
func TestLoadSession_LeavesAnUnsupervisedResumeAlone(t *testing.T) {
	data := captureRequests(t, &marotte.SessionOpts{SessionID: "sess_plain"})
	if strings.Contains(data, `"`+marotte.ConfigOptionAutopilot+`"`) {
		t.Errorf("an unsupervised resume sent the autopilot option:\n%s", data)
	}
}

// TestApplySessionResult_TakesFlatMetaTitle pins `_meta.title` as flat: every other `_meta` here is under `kiro`,
// and moving it there compiles and yields "". KAS spreads session metadata onto `_meta` (probed 2026-08-02).
func TestApplySessionResult_TakesFlatMetaTitle(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "flat _meta.title is adopted",
			body: `{"sessionId":"s1","_meta":{"id":"s1","title":"Reaper live-session exemption","agentMode":"vibe"}}`,
			want: "Reaper live-session exemption",
		},
		{
			name: "nested _meta.kiro.title is NOT the wire shape",
			body: `{"sessionId":"s1","_meta":{"kiro":{"title":"wrong nesting"}}}`,
			want: "",
		},
		{
			name: "session/new placeholder arrives verbatim for the caller to reject",
			body: `{"sessionId":"s1","_meta":{"title":"New Session"}}`,
			want: "New Session",
		},
		{
			name: "absent _meta leaves the title empty",
			body: `{"sessionId":"s1"}`,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r sessionCreated
			if err := json.Unmarshal([]byte(tc.body), &r); err != nil {
				t.Fatalf("unmarshal session result: %v", err)
			}
			b := newTestSession()
			b.mu.Lock()
			b.applySessionResultLocked(&r, "")
			b.mu.Unlock()
			if got := b.SessionTitle(); got != tc.want {
				t.Errorf("SessionTitle() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestApplySessionResult_TakesFlatMetaContextUsage pins the threshold at flat `_meta.contextUsage`; nested yields
// nothing, and the sibling truncationThreshold must decode.
func TestApplySessionResult_TakesFlatMetaContextUsage(t *testing.T) {
	cases := []struct {
		name string
		body string
		want float64
	}{
		{
			name: "flat _meta.contextUsage is adopted",
			body: `{"sessionId":"s1","_meta":{"id":"s1","contextUsage":{"summarizationThreshold":80,"truncationThreshold":95}}}`,
			want: 80,
		},
		{
			name: "nested _meta.kiro.contextUsage is NOT the wire shape",
			body: `{"sessionId":"s1","_meta":{"kiro":{"contextUsage":{"summarizationThreshold":80,"truncationThreshold":95}}}}`,
		},
		{
			name: "absent _meta leaves the threshold unknown",
			body: `{"sessionId":"s1"}`,
		},
		{
			name: "a block carrying only truncationThreshold adopts nothing",
			body: `{"sessionId":"s1","_meta":{"contextUsage":{"truncationThreshold":95}}}`,
		},
		{
			name: "non-integer percentages survive the decode",
			body: `{"sessionId":"s1","_meta":{"contextUsage":{"summarizationThreshold":80.5,"truncationThreshold":95.25}}}`,
			want: 80.5,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r sessionCreated
			if err := json.Unmarshal([]byte(tc.body), &r); err != nil {
				t.Fatalf("unmarshal session result: %v", err)
			}
			b := newTestSession()
			b.mu.Lock()
			b.applySessionResultLocked(&r, "")
			b.mu.Unlock()
			if got := b.SummarizationThreshold(); got != tc.want {
				t.Errorf("SummarizationThreshold() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestApplySessionResult_KeepsSummarizationThresholdOnAbsent pins that only a resume carries it, so a zero would never heal.
func TestApplySessionResult_KeepsSummarizationThresholdOnAbsent(t *testing.T) {
	cases := map[string]string{
		"no contextUsage block at all": `{"sessionId":"s1","_meta":{"title":"x"}}`,
		"a block with absent members":  `{"sessionId":"s1","_meta":{"contextUsage":{}}}`,
		"a block whose members are 0":  `{"sessionId":"s1","_meta":{"contextUsage":{"summarizationThreshold":0,"truncationThreshold":0}}}`,
		"an explicitly null block":     `{"sessionId":"s1","_meta":{"contextUsage":null}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			var r sessionCreated
			if err := json.Unmarshal([]byte(body), &r); err != nil {
				t.Fatalf("unmarshal session result: %v", err)
			}
			b := newTestSession()
			b.mu.Lock()
			b.summarizationPct = 80
			b.applySessionResultLocked(&r, "")
			b.mu.Unlock()
			if got := b.SummarizationThreshold(); got != 80 {
				t.Errorf("SummarizationThreshold() = %v, want 80", got)
			}
		})
	}
}

// TestCancelClosesStdinSoTheTreeSeesEOF pins that the kiro-cli tree shares marotte's pipes in one session, and closing the
// write end is what reclaims it; WaitDelay's SIGKILL hits only the head (kiro-cli 2.16.0 leaked about 250 MB in 2/2
// trials). The head ignores SIGTERM, so only EOF reclaims the grandchild.
func TestCancelClosesStdinSoTheTreeSeesEOF(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")
	// The head ignores SIGTERM and blocks; its child's `head -c 1` returns when the pipe closes, proving EOF reached the
	// tree. `exec 3<&0`: without job control a background command's stdin is /dev/null and would pass vacuously.
	script := "#!/bin/sh\ntrap '' TERM\nexec 3<&0\nhead -c 1 <&3 >/dev/null &\n" +
		"echo $! > " + pidFile + "\nwhile :; do sleep 0.05; done\n"
	scriptPath := filepath.Join(dir, "fake-kiro-cli")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write bait script: %v", err)
	}

	// Not t.Context(): the cancel fires from t.Cleanup, after t.Context() is cancelled.
	ctx, cancel := context.WithCancel(context.Background())
	b := NewEngine(scriptPath, dir)
	// The bait speaks no ACP, so drive the spawn directly; Cancel fires from lifecycleCtx.
	b.lifecycleCtx = ctx
	if err := b.startProcess(); err != nil {
		t.Fatalf("startProcess: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		b.Stop()
	})

	grandchild := waitForBridgePID(t, pidFile)
	if !processAlive(grandchild) {
		t.Fatalf("bait grandchild %d not alive before cancel; the test proves nothing", grandchild)
	}

	cancel() // fires cmd.Cancel

	deadline := time.Now().Add(3 * time.Second)
	for processAlive(grandchild) {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d survived ctx cancel; Cancel signalled the head without closing stdin, so the tree never saw EOF", grandchild)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// processAlive reports whether pid is a live, non-zombie process, from /proc/<pid>/stat. kill(pid,
// 0) calls a zombie alive, adding the shell's reaping latency, and a zombie already proves the EOF.
// The state follows the last ')' because comm may contain parens.
func processAlive(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)) // #nosec G304 -- pid from the test's own child
	if err != nil {
		return false // no /proc entry: reaped and gone
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return false
	}
	return s[i+2] != 'Z'
}

func waitForBridgePID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		raw, err := os.ReadFile(path) // #nosec G304 -- t.TempDir path
		if err == nil {
			if pid, cErr := strconv.Atoi(strings.TrimSpace(string(raw))); cErr == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("bait never wrote its grandchild pid to %s", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestSessionParams_CarryNoMCPServers pins `mcpServers` present (required since kiro-cli 2.16) and empty: KAS merges
// client entries over the file per name, so an inline entry would make UI edits look like nothing. Raw bytes, since
// the bug is a map key.
func TestSessionParams_CarryNoMCPServers(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.log")

	// `tee -a` logs every received line.
	script := `#!/bin/sh
while IFS= read -r line; do
  printf '%s\n' "$line" >> "` + logPath + `"
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(echo "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  sid=$(echo "$line" | sed -n 's/.*"overrideSessionId":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"serverInfo":{"name":"fake"}}}\n' "$id"
      ;;
    session/new)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"%s"}}\n' "$id" "$sid"
      ;;
    session/load)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"%s"}}\n' "$id" "$sid"
      ;;
    *)
      if [ -n "$id" ]; then
        printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"
      fi
      ;;
  esac
done
`
	scriptPath := filepath.Join(dir, "fake-kiro-cli")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}

	cases := []struct {
		name   string
		opts   *marotte.SessionOpts
		method string
	}{
		{"session/new", &marotte.SessionOpts{Model: "m"}, "session/new"},
		{"session/load", &marotte.SessionOpts{SessionID: "existing", Model: "m"}, "session/load"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(logPath, nil, 0o600); err != nil {
				t.Fatalf("reset log: %v", err)
			}
			b := NewEngine(scriptPath, dir)
			if _, err := openSessionOn(t, b, tc.opts); err != nil {
				t.Fatalf("open session: %v", err)
			}
			b.Stop()

			data, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("read log: %v", err)
			}
			var found bool
			for line := range strings.SplitSeq(string(data), "\n") {
				if !strings.Contains(line, `"method":"`+tc.method+`"`) {
					continue
				}
				found = true
				if !strings.Contains(line, `"mcpServers":[]`) {
					t.Errorf("%s must carry an EMPTY mcpServers array (2.16 requires the key; entries would outrank KAS's config file):\n%s", tc.method, line)
				}
			}
			if !found {
				t.Fatalf("no %s request in the log; the test proved nothing:\n%s", tc.method, data)
			}
		})
	}
}

// coreIOToolIDs is KAS's CORE_IO_TOOL_IDS, from the 2.16.1 bundle (acp-server.js:464015-464022). KAS picks the
// clamped ExecuteBash (a 30-minute ceiling) only while the client declares none of these.
var coreIOToolIDs = []string{
	"execute_bash", "read_file", "fs_write", "str_replace", "grep_search", "file_search",
}

// TestInitialize_DeclaresNoCoreIOTool pins that a client tool named above silently unbounds ExecuteBash. Wanting one means
// deleting this test. Raw initialize bytes, since the ids could arrive via clientCapabilities or _meta.
func TestInitialize_DeclaresNoCoreIOTool(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "init.log")

	script := `#!/bin/sh
while IFS= read -r line; do
  printf '%s\n' "$line" >> "` + logPath + `"
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(echo "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  sid=$(echo "$line" | sed -n 's/.*"overrideSessionId":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"serverInfo":{"name":"fake"}}}\n' "$id"
      ;;
    session/new)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"%s"}}\n' "$id" "$sid"
      ;;
    *)
      if [ -n "$id" ]; then
        printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"
      fi
      ;;
  esac
done
`
	scriptPath := filepath.Join(dir, "fake-kiro-cli")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}

	b := NewEngine(scriptPath, dir)
	if err := b.Start(t.Context(), &marotte.EngineOpts{Lifetime: context.Background()}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	b.Stop()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var initLine string
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.Contains(line, `"method":"initialize"`) {
			initLine = line
			break
		}
	}
	if initLine == "" {
		t.Fatalf("no initialize request in the log; the test proved nothing:\n%s", data)
	}
	for _, id := range coreIOToolIDs {
		if strings.Contains(initLine, `"`+id+`"`) {
			t.Errorf(`initialize declares %q, which is in KAS's CORE_IO_TOOL_IDS.
That flips hasClientIOTools and swaps the agent onto the UNBOUNDED ExecuteBash,
removing the 30 minute ceiling on every shell command. If this is intended, the
bound has to be reintroduced deliberately (see engine.go's clientCapabilities).
initialize was:
%s`, id, initLine)
		}
	}
}

// initializeGoldenPath is the committed byte-for-byte capture of every initialize request marotte can send.
const initializeGoldenPath = "testdata/initialize.golden"

const initializeGoldenCmd = "UPDATE_GOLDEN=1 go test ./internal/bridge/ -run TestInitializeDeclaresExactly"

// initGateCases is the complete matrix of the engine's initialize gates: SecretStorage and DisableTelemetry set a
// value, Knowledge two values, and the engine features. The order is the golden's line order.
var initGateCases = []struct {
	name          string
	features      marotte.EngineFeatures
	secretStorage bool
	knowledge     bool
	telemetryOff  bool
}{
	{"gates off", marotte.EngineFeatures{}, false, false, false},
	{"secret storage only", marotte.EngineFeatures{}, true, false, false},
	{"knowledge on", marotte.EngineFeatures{}, false, true, false},
	// Proves Start copies EngineOpts.Features.
	{"agent capabilities on", marotte.EngineFeatures{
		SteeringReminders:  true,
		InfraSafetyMonitor: "on", TerminalCommandTimeoutMs: 300000,
	}, false, false, false},
	{"telemetry off", marotte.EngineFeatures{}, false, false, true},
	{"every gate on", marotte.EngineFeatures{}, true, true, false},
}

// TestInitializeDeclaresExactly pins every initialize request's exact bytes against a golden, since each failure is
// silent on the wire. The capture is the raw stdin line. Regenerate with:
//
//	UPDATE_GOLDEN=1 go test ./internal/bridge/ -run TestInitializeDeclaresExactly
func TestInitializeDeclaresExactly(t *testing.T) {
	// A fake kiro-cli that appends the raw initialize request to $INIT_CAPTURE and answers the rest minimally.
	script := `#!/bin/sh
while IFS= read -r line; do
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(echo "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  sid=$(echo "$line" | sed -n 's/.*"overrideSessionId":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize)
      printf '%s\n' "$line" >> "$INIT_CAPTURE"
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"serverInfo":{"name":"fake"}}}\n' "$id"
      ;;
    session/new)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"%s","modes":{"currentModeId":"default","availableModes":[{"id":"default","name":"Default","description":"d"}]},"configOptions":[{"id":"model","currentValue":"m","options":[{"value":"m","name":"M","description":"x","_meta":{"kiro":{"rateMultiplier":1.0}}}]}]}}\n' "$id" "$sid"
      ;;
    *)
      if [ -n "$id" ]; then printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"; fi
      ;;
  esac
done
`
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "fake-kiro-cli")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}
	t.Setenv("HOME", t.TempDir())

	var got strings.Builder
	for _, tc := range initGateCases {
		capturePath := filepath.Join(t.TempDir(), "init.jsonl")
		t.Setenv("INIT_CAPTURE", capturePath)
		b := NewEngine(scriptPath, dir)
		err := b.Start(t.Context(), &marotte.EngineOpts{
			Lifetime:         context.Background(),
			SecretStorage:    tc.secretStorage,
			Knowledge:        tc.knowledge,
			DisableTelemetry: tc.telemetryOff,
			Features:         tc.features,
		})
		if err != nil {
			t.Fatalf("%s: Start: %v", tc.name, err)
		}
		b.Stop()
		data, err := os.ReadFile(capturePath)
		if err != nil {
			t.Fatalf("%s: read init capture: %v", tc.name, err)
		}
		if len(data) == 0 {
			t.Fatalf("%s: captured no initialize request; an empty capture would pass forever", tc.name)
		}
		got.Write(data)
	}

	// Write then compare, so the comparison proves the write.
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(initializeGoldenPath), 0o750); err != nil {
			t.Fatalf("create testdata dir: %v", err)
		}
		if err := os.WriteFile(initializeGoldenPath, []byte(got.String()), 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("wrote %s (%d bytes, %d cases)", initializeGoldenPath, got.Len(), len(initGateCases))
	}

	want, err := os.ReadFile(initializeGoldenPath)
	if err != nil {
		t.Fatalf("read golden %s (regenerate with: %s): %v", initializeGoldenPath, initializeGoldenCmd, err)
	}
	if string(want) == got.String() {
		return
	}
	wantLines := strings.Split(strings.TrimSuffix(string(want), "\n"), "\n")
	gotLines := strings.Split(strings.TrimSuffix(got.String(), "\n"), "\n")
	if len(wantLines) != len(gotLines) {
		t.Fatalf(`initialize golden has %d request(s), the code produced %d.
A case was added to or removed from initGateCases without regenerating.
Regenerate with: %s`, len(wantLines), len(gotLines), initializeGoldenCmd)
	}
	for i := range wantLines {
		if wantLines[i] == gotLines[i] {
			continue
		}
		name := "case " + strconv.Itoa(i)
		if i < len(initGateCases) {
			name = initGateCases[i].name
		}
		t.Errorf(`initialize wire bytes changed for %q.
This is a WIRE change, not a refactor. If it is deliberate, regenerate with:
  %s
--- want
%s
+++ got
%s`, name, initializeGoldenCmd, wantLines[i], gotLines[i])
	}
}

func TestInitialize_RetainsKiroAgentCapabilities(t *testing.T) {
	b := NewEngine("/nonexistent", "/work")
	resp := &marotte.RPCResponse{Result: json.RawMessage(`{
		"agentCapabilities":{"_meta":{"kiro":{
			"extensionMethods":["_kiro/one","_kiro/two"],
			"replayMarking":true
		}}}
	}`)}
	if _, err := driveSessionCall(t, b, resp, false, b.initialize); err != nil {
		t.Fatalf("initialize = %v, want nil", err)
	}

	got := b.agentKiro.Load()
	if !slices.Equal(got.ExtensionMethods, []string{"_kiro/one", "_kiro/two"}) {
		t.Errorf("ExtensionMethods = %v, want [_kiro/one _kiro/two]", got.ExtensionMethods)
	}
	if !got.ReplayMarking {
		t.Error("ReplayMarking = false, want true")
	}
}

func TestInitialize_MissingKiroAgentCapabilitiesIsZeroValue(t *testing.T) {
	b := NewEngine("/nonexistent", "/work")
	resp := &marotte.RPCResponse{Result: json.RawMessage(`{"protocolVersion":1}`)}
	if _, err := driveSessionCall(t, b, resp, false, b.initialize); err != nil {
		t.Fatalf("initialize = %v, want nil", err)
	}

	got := b.agentKiro.Load()
	if len(got.ExtensionMethods) != 0 || got.ReplayMarking {
		t.Errorf("capabilities = %+v, want zero value", got)
	}
}

func TestForwardStderr_TruncatesOneLineAndContinues(t *testing.T) {
	logs := capture.Default(t)
	b := NewEngine("/nonexistent", "/work")
	b.lifecycleCtx = t.Context()
	first := strings.Repeat("x", stderrLineCap+128)

	b.forwardStderr(strings.NewReader(first + "\nsecond line\n"))

	lines := logs.AttrValuesExact("kiro-cli stderr", "line")
	if len(lines) != 2 {
		t.Fatalf("forwardStderr logged %d lines, want 2", len(lines))
	}
	const marker = "... [truncated]"
	if len(lines[0]) > stderrLineCap || !strings.HasSuffix(lines[0], marker) {
		t.Errorf("first forwarded line has length %d and suffix %q, want at most %d bytes ending in %q",
			len(lines[0]), lines[0][max(0, len(lines[0])-len(marker)):], stderrLineCap, marker)
	}
	if lines[1] != "second line" {
		t.Errorf("second forwarded line = %q, want %q", lines[1], "second line")
	}
}

// TestStdinPublication_IsRaceFree races Start assigning stdin against a writer: the engine lane exists before Start,
// so a caller can write into it mid-spawn. The subprocess exits at once; only publication is under test.
func TestStdinPublication_IsRaceFree(t *testing.T) {
	b := NewEngine("/bin/true", t.TempDir())
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(func() { cancel(); b.Stop() })

	var wg sync.WaitGroup
	wg.Add(2)
	// Every outcome is legal; the race detector is the assertion.
	go func() {
		defer wg.Done()
		for range 200 {
			_ = b.Notify(ctx, marotte.MethodCancel, nil)
		}
	}()
	go func() {
		defer wg.Done()
		// Start fails at the handshake after startProcess assigned stdin.
		_ = b.Start(ctx, &marotte.EngineOpts{Lifetime: ctx})
	}()
	wg.Wait()
}

// TestLoadSession_ReAssertsMemoryReflection pins that KAS freezes an explicit session's mode, so a resume re-sends reflection
// as the string KAS's select takes (a boolean is a no-op).
func TestLoadSession_ReAssertsMemoryReflection(t *testing.T) {
	for _, on := range []bool{false, true} {
		want := marotte.ConfigValueAutopilotOff
		if on {
			want = marotte.ConfigValueAutopilotOn
		}
		line := captureRequest(t, "session/set_config_option",
			&marotte.SessionOpts{SessionID: "sess_mem", Memory: marotte.MemoryPreference{Mode: "read_write", Reflection: on}},
			`"configId":"`+marotte.ConfigOptionMemoryReflection+`"`)
		params := digObject(t, "the memoryReflection set_config_option params", line, "params")
		if got, ok := params[keyConfigValue].(string); !ok || got != want {
			t.Errorf("Reflection=%v: memoryReflection value = %#v (%T), want the string %q",
				on, params[keyConfigValue], params[keyConfigValue], want)
		}
	}
	data := captureRequests(t, &marotte.SessionOpts{Memory: marotte.MemoryPreference{Mode: "read_write", Reflection: true}})
	if strings.Contains(data, `"`+marotte.ConfigOptionMemoryReflection+`"`) {
		t.Errorf("a fresh session/new sent memoryReflection; the session door already carries it:\n%s", data)
	}
}

// titleSetByUser is flat beside title.
func TestApplySessionResult_TakesFlatTitleSetByUser(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"flat latch", `{"sessionId":"s1","_meta":{"title":"X","titleSetByUser":true}}`, true},
		{"nested under kiro is not the wire shape", `{"sessionId":"s1","_meta":{"kiro":{"titleSetByUser":true}}}`, false},
		{"absent", `{"sessionId":"s1","_meta":{"title":"X"}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r sessionCreated
			if err := json.Unmarshal([]byte(tc.body), &r); err != nil {
				t.Fatalf("unmarshal session result: %v", err)
			}
			b := newTestSession()
			b.mu.Lock()
			b.applySessionResultLocked(&r, "")
			b.mu.Unlock()
			if got := b.SessionTitleSetByUser(); got != tc.want {
				t.Errorf("SessionTitleSetByUser() = %v, want %v", got, tc.want)
			}
		})
	}
}
