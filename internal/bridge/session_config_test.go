package bridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// configOptionFake writes a fake kiro-cli that logs every request line to logPath and answers Start's three methods.
// session/new reports a model the caller did not ask for.
func configOptionFake(t *testing.T, logPath string) string {
	t.Helper()
	script := `#!/bin/sh
while IFS= read -r line; do
  printf '%s\n' "$line" >> "` + logPath + `"
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(echo "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"serverInfo":{"name":"fake"}}}\n' "$id"
      ;;
    session/new)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"sess-cfg","configOptions":[{"id":"model","currentValue":"engine-default"}]}}\n' "$id"
      ;;
    *)
      if [ -n "$id" ]; then
        printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"
      fi
      ;;
  esac
done
`
	dir := filepath.Dir(logPath)
	scriptPath := filepath.Join(dir, "fake-kiro-cli")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}
	return scriptPath
}

// The requested model and effort reach a new session as config options: as launch flags kiro-cli refuses them with
// v3 and exits (2.17.0, 2.18.0). Raw request bytes, since the defect is a value that never leaves the process.
func TestNewSession_AppliesRequestedModelAndEffort(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.log")
	scriptPath := configOptionFake(t, logPath)

	b := New(scriptPath, dir)
	t.Cleanup(b.Stop)
	if err := b.Start(context.Background(), &marotte.StartOpts{Lifetime: context.Background(), Model: "claude-opus-5", Effort: "high"}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	got := string(raw)

	wants := []string{
		`"method":"session/set_config_option"`,
		`"configId":"model"`,
		`"value":"claude-opus-5"`,
		`"configId":"effortLevel"`,
		`"value":"high"`,
	}
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("request log does not contain %s\nlog:\n%s", want, got)
		}
	}

	// The bridge reports the model it selected; the chat header reads it.
	if id := b.ModelID(); id != "claude-opus-5" {
		t.Errorf("ModelID() = %q, want claude-opus-5", id)
	}
}

// A model equal to the default costs no round trip, and `auto` is not a model id KAS accepts.
func TestNewSession_SkipsRedundantModelConfigOption(t *testing.T) {
	cases := []struct {
		name  string
		model string
	}{
		{"auto is not a model id", marotte.ModelAuto},
		{"already the session default", "engine-default"},
		{"unset", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "requests.log")
			scriptPath := configOptionFake(t, logPath)

			b := New(scriptPath, dir)
			t.Cleanup(b.Stop)
			if err := b.Start(context.Background(), &marotte.StartOpts{Lifetime: context.Background(), Model: tc.model}); err != nil {
				t.Fatalf("Start: %v", err)
			}

			raw, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("read request log: %v", err)
			}
			if strings.Contains(string(raw), `"configId":"model"`) {
				t.Errorf("model %q sent a config option it did not need\nlog:\n%s", tc.model, raw)
			}
		})
	}
}

// A malformed effort is dropped, so a corrupt setting cannot fail session creation. Shape only: well-formed unknown
// tiers flow.
func TestNewSession_DropsMalformedEffort(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.log")
	scriptPath := configOptionFake(t, logPath)

	b := New(scriptPath, dir)
	t.Cleanup(b.Stop)
	if err := b.Start(context.Background(), &marotte.StartOpts{Lifetime: context.Background(), Effort: "Ultra!"}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	if strings.Contains(string(raw), `"configId":"effortLevel"`) {
		t.Errorf("malformed effort reached the wire\nlog:\n%s", raw)
	}
}

// An invalid model identifier is refused before spawn; Start is the one place that sees it.
func TestStart_RefusesInvalidModelIdentifier(t *testing.T) {
	dir := t.TempDir()
	scriptPath := configOptionFake(t, filepath.Join(dir, "requests.log"))

	b := New(scriptPath, dir)
	t.Cleanup(b.Stop)
	err := b.Start(context.Background(), &marotte.StartOpts{Lifetime: context.Background(), Model: "bad model; rm -rf /"})
	if err == nil {
		t.Fatal("Start accepted an invalid model identifier, want an error")
	}
	if !strings.Contains(err.Error(), "invalid model identifier") {
		t.Errorf("error = %v, want it to name the invalid model identifier", err)
	}
}

// The handshake context must not own the subprocess. A per-turn context once closed stdin when the first prompt
// returned; kiro-cli's helpers kept stdout open, so the bridge looked healthy while every write failed and each tree
// leaked about 250 MB. Asserted by a live round trip.
func TestStart_HandshakeCtxDoesNotOwnTheSubprocess(t *testing.T) {
	dir := t.TempDir()
	scriptPath := configOptionFake(t, filepath.Join(dir, "requests.log"))

	b := New(scriptPath, dir)
	t.Cleanup(b.Stop)

	handshakeCtx, cancelHandshake := context.WithCancel(context.Background())
	lifetime, cancelLifetime := context.WithCancel(context.Background())
	t.Cleanup(cancelLifetime)

	if err := b.Start(handshakeCtx, &marotte.StartOpts{Lifetime: lifetime}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// What CmdPrompt's `defer cancel()` does when a turn returns.
	cancelHandshake()

	if _, err := b.Call(context.Background(), "_probe/ping", map[string]any{}); err != nil {
		t.Fatalf("Call after the handshake ctx was cancelled: %v; the subprocess took its lifetime from the handshake context", err)
	}
}

// The lifetime context owns the subprocess, so shutdown still reaps it if Stop races or panics.
func TestStart_LifetimeCtxOwnsTheSubprocess(t *testing.T) {
	dir := t.TempDir()
	scriptPath := configOptionFake(t, filepath.Join(dir, "requests.log"))

	b := New(scriptPath, dir)
	t.Cleanup(b.Stop)

	lifetime, cancelLifetime := context.WithCancel(context.Background())
	if err := b.Start(context.Background(), &marotte.StartOpts{Lifetime: lifetime}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	cancelLifetime()

	// Cancel closes stdin, the fake's read loop ends, readLoop closes notifCh.
	select {
	case <-b.NotifCh():
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling StartOpts.Lifetime did not tear the bridge down")
	}
}

// A nil lifetime is refused; a caller wanting an uncancellable one passes context.Background().
func TestStart_RefusesNilLifetime(t *testing.T) {
	dir := t.TempDir()
	scriptPath := configOptionFake(t, filepath.Join(dir, "requests.log"))

	b := New(scriptPath, dir)
	t.Cleanup(b.Stop)

	err := b.Start(context.Background(), &marotte.StartOpts{})
	if err == nil {
		t.Fatal("Start accepted a nil StartOpts.Lifetime; it must be refused rather than " +
			"substituted with an uncancellable context at the point the subprocess is spawned")
	}
	if !strings.Contains(err.Error(), "Lifetime") {
		t.Errorf("error = %v, want it to name the required Lifetime field", err)
	}
	if b.SessionID() != "" {
		t.Errorf("refused Start left a session id (%q); nothing should have been spawned", b.SessionID())
	}
}

// A chosen role rides session/new as _meta.kiro.modeId in the door's one _meta.kiro object, with no set_mode after.
func TestNewSession_SendsModeAtTheDoor(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.log")
	scriptPath := configOptionFake(t, logPath)

	b := New(scriptPath, dir)
	t.Cleanup(b.Stop)
	if err := b.Start(context.Background(), &marotte.StartOpts{Lifetime: context.Background(), Mode: "spec", Model: "claude-opus-5"}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	if strings.Contains(string(raw), `"method":"session/set_mode"`) {
		t.Errorf("Start sent session/set_mode; the mode belongs on the session/new door\nlog:\n%s", raw)
	}
	kiro := sessionNewKiroMeta(t, string(raw))
	if kiro["modeId"] != "spec" {
		t.Errorf("session/new _meta.kiro.modeId = %v, want spec", kiro["modeId"])
	}
	if kiro["modelId"] != "claude-opus-5" {
		t.Errorf("session/new _meta.kiro.modelId = %v, want claude-opus-5 beside the mode", kiro["modelId"])
	}
	if _, ok := kiro["settings"].(map[string]any); !ok {
		t.Errorf("session/new _meta.kiro lost the session door's settings beside the mode: %v", kiro)
	}
}

// An unset mode sends no modeId: KAS would validate an empty id.
func TestNewSession_UnsetModeSendsNoModeKey(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.log")
	scriptPath := configOptionFake(t, logPath)

	b := New(scriptPath, dir)
	t.Cleanup(b.Stop)
	if err := b.Start(context.Background(), &marotte.StartOpts{Lifetime: context.Background()}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	if strings.Contains(string(raw), `"method":"session/set_mode"`) {
		t.Errorf("an unset mode sent a switch\nlog:\n%s", raw)
	}
	if _, present := sessionNewKiroMeta(t, string(raw))["modeId"]; present {
		t.Errorf("an unset mode put a modeId on the door\nlog:\n%s", raw)
	}
}

// sessionNewKiroMeta returns the session/new request's _meta.kiro object.
func sessionNewKiroMeta(t *testing.T, log string) map[string]any {
	t.Helper()
	for line := range strings.SplitSeq(strings.TrimSpace(log), "\n") {
		if strings.Contains(line, `"method":"session/new"`) {
			return digObject(t, "the session door", line, "params", "_meta", "kiro")
		}
	}
	t.Fatalf("no session/new request in the log:\n%s", log)
	return nil
}

// A warning is the operator's only sign a chat is not on its requested model or effort, so a clean start stays
// quiet. Not parallel: it swaps the slog default.
func TestNewSession_SuccessfulConfigCallsStayQuiet(t *testing.T) {
	dir := t.TempDir()
	scriptPath := configOptionFake(t, filepath.Join(dir, "requests.log"))

	logs := captureLogs(t)

	b := New(scriptPath, dir)
	t.Cleanup(b.Stop)
	opts := &marotte.StartOpts{
		Lifetime: context.Background(),
		Model:    "claude-opus-5",
		Effort:   "high",
		Mode:     "spec",
	}
	if err := b.Start(context.Background(), opts); err != nil {
		t.Fatalf("Start: %v", err)
	}

	for _, unwanted := range []string{
		`msg="apply initial session model"`,
		`msg="apply initial reasoning effort"`,
	} {
		if strings.Contains(logs.String(), unwanted) {
			t.Errorf("a successful start logged %q\nlog:\n%s", unwanted, logs.String())
		}
	}
}
