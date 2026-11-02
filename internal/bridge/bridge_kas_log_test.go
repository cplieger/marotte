package bridge

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/slogx/capture"
)

const kasLoggingBlock = `"logging":{"logDir":"/config/home/.kiro/logs/2026-10-05T01-02-03Z",` +
	`"channels":[{"name":"kiro","filePath":"/config/home/.kiro/logs/2026-10-05T01-02-03Z/kiro.log"},` +
	`{"name":"mcp","filePath":"/config/home/.kiro/logs/2026-10-05T01-02-03Z/mcp.log"}]}`

func TestInitialize_DecodesKASLogging(t *testing.T) {
	b := NewEngine("/nonexistent", "/work")
	resp := &marotte.RPCResponse{Result: json.RawMessage(`{"agentCapabilities":{"_meta":{"kiro":{` + kasLoggingBlock + `}}}}`)}
	if _, err := driveSessionCall(t, b, resp, false, b.initialize); err != nil {
		t.Fatalf("initialize = %v, want nil", err)
	}

	const dir = "/config/home/.kiro/logs/2026-10-05T01-02-03Z"
	if got := b.KASLogDir(); got != dir {
		t.Errorf("KASLogDir() = %q, want %q", got, dir)
	}
}

func TestInitialize_MalformedKASLoggingKeepsTheHandshake(t *testing.T) {
	b := NewEngine("/nonexistent", "/work")
	resp := &marotte.RPCResponse{Result: json.RawMessage(`{"agentCapabilities":{"_meta":{"kiro":{"logging":"nope","replayMarking":true}}}}`)}
	if _, err := driveSessionCall(t, b, resp, false, b.initialize); err != nil {
		t.Fatalf("initialize = %v, want nil (a bad logging block is diagnostic only)", err)
	}
	if got := b.KASLogDir(); got != "" {
		t.Errorf("KASLogDir() = %q, want empty", got)
	}
	if !b.agentKiro.Load().ReplayMarking {
		t.Error("ReplayMarking = false, want the rest of the block decoded")
	}
}

func writeKASLogScript(t *testing.T, newResult string) string {
	t.Helper()
	script := `#!/bin/sh
while IFS= read -r line; do
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(echo "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"agentCapabilities":{"_meta":{"kiro":{` + kasLoggingBlock + `}}}}}\n' "$id"
      ;;
    session/new)
      printf '{"jsonrpc":"2.0","id":%s,` + newResult + `}\n' "$id"
      ;;
    *)
      if [ -n "$id" ]; then
        printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"
      fi
      ;;
  esac
done
`
	path := filepath.Join(t.TempDir(), "fake-kiro-cli")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}
	return path
}

func TestStart_EngineStartedCarriesTheKASLogDir(t *testing.T) {
	logs := capture.Default(t)
	b := NewEngine(writeKASLogScript(t, `"result":{"sessionId":"sess-1"}`), t.TempDir())
	if err := b.Start(t.Context(), &marotte.EngineOpts{Lifetime: t.Context()}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(b.Stop)

	got, ok := logs.AttrValueExact("engine started", "kas_log_dir")
	if !ok || got != "/config/home/.kiro/logs/2026-10-05T01-02-03Z" {
		t.Errorf(`"engine started" kas_log_dir = %q (present %v), want the advertised dir`, got, ok)
	}
}

func TestNewSession_DoorFailureLogsTheKASLogDir(t *testing.T) {
	logs := capture.Default(t)
	b := NewEngine(writeKASLogScript(t, `"error":{"code":-32603,"message":"boom"}`), t.TempDir())
	if err := b.Start(t.Context(), &marotte.EngineOpts{Lifetime: t.Context()}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(b.Stop)
	if _, err := b.NewSession(t.Context(), &marotte.SessionOpts{Role: marotte.SessionRoleChat}, nil); err == nil {
		t.Fatal("NewSession = nil, want the session/new error")
	}

	got, ok := logs.AttrValueExact("session door failed", "kas_log_dir")
	if !ok || got != "/config/home/.kiro/logs/2026-10-05T01-02-03Z" {
		t.Errorf(`"session door failed" kas_log_dir = %q (present %v), want the advertised dir`, got, ok)
	}
	if n := logs.CountLevel(slog.LevelWarn, "session door failed"); n != 1 {
		t.Errorf(`"session door failed" at Warn = %d lines, want 1`, n)
	}
	if logs.Contains("session opened") {
		t.Error(`a failed door logged "session opened"`)
	}
}
