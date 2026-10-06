package bridge

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

func contentCollectionOff(context.Context) bool { return false }
func contentCollectionOn(context.Context) bool  { return true }

// Content collection is process-scoped in KAS and never persisted, so both session doors carry it, as a string: a
// boolean is ignored.
func TestSessionDoors_AssertContentCollection(t *testing.T) {
	for _, tc := range []struct {
		resolve func(context.Context) bool
		name    string
		session string
		want    string
	}{
		{name: "new_off", resolve: contentCollectionOff, want: marotte.ConfigValueContentCollectionDisabled},
		{name: "new_on", resolve: contentCollectionOn, want: marotte.ConfigValueContentCollectionEnabled},
		{name: "load_off", resolve: contentCollectionOff, session: "sess_resumed", want: marotte.ConfigValueContentCollectionDisabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := captureRequest(t, "session/set_config_option",
				&marotte.StartOpts{Lifetime: t.Context(), SessionID: tc.session, ContentCollection: tc.resolve},
				`"configId":"`+marotte.ConfigOptionContentCollection+`"`)
			params := digObject(t, "the contentCollection params", line, "params")
			if got, ok := params[keyConfigValue].(string); !ok || got != tc.want {
				t.Errorf("contentCollection value = %#v (%T), want the string %q", params[keyConfigValue], params[keyConfigValue], tc.want)
			}
		})
	}
}

func TestSessionDoors_NoResolverSendsNoContentCollection(t *testing.T) {
	data := captureRequests(t, &marotte.StartOpts{Lifetime: t.Context()})
	if strings.Contains(data, `"`+marotte.ConfigOptionContentCollection+`"`) {
		t.Errorf("a spawn with no resolver sent contentCollection:\n%s", data)
	}
}

// contentCollectionScript answers every set_config_option stuck at "enabled", like a build ignoring the option.
const contentCollectionScript = `#!/bin/sh
while IFS= read -r line; do
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(echo "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"serverInfo":{"name":"fake"}}}\n' "$id"
      ;;
    session/new|session/load)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"sess_doortest"}}\n' "$id"
      ;;
    *)
      if [ -n "$id" ]; then
        printf '{"jsonrpc":"2.0","id":%s,"result":{"configOptions":[{"id":"contentCollection","currentValue":"enabled"}]}}\n' "$id"
      fi
      ;;
  esac
done
`

// A reply reporting another value is the silent opt-in, so setContentCollection errors.
func TestSetContentCollection_ReportsAMismatchedReply(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "fake-kiro-cli")
	if err := os.WriteFile(scriptPath, []byte(contentCollectionScript), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}
	t.Setenv("HOME", t.TempDir())
	b := New(scriptPath, dir)
	if err := b.Start(t.Context(), &marotte.StartOpts{Lifetime: t.Context()}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer b.Stop()
	if err := b.setContentCollection(t.Context(), false); err == nil {
		t.Error("setContentCollection(false) against a reply reporting enabled = nil, want an error")
	}
	if err := b.setContentCollection(t.Context(), true); err != nil {
		t.Errorf("setContentCollection(true) against a reply reporting enabled = %v, want nil", err)
	}
}

// A lock change lands while a door holds a stale resolution; the following push must be KAS's last write. The resolve
// gives an unserialized push 200ms to land first.
func TestAssertContentCollection_AStaleDoorWriteNeverLandsLast(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "fake-kiro-cli")
	if err := os.WriteFile(scriptPath, []byte(sessionDoorScript), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}
	t.Setenv("HOME", t.TempDir())
	capturePath := filepath.Join(t.TempDir(), "rpc.jsonl")
	t.Setenv("RPC_CAPTURE", capturePath)

	b := New(scriptPath, dir)
	var (
		mu       sync.Mutex
		resolves int
	)
	pushResolved := make(chan struct{})
	pushDone := make(chan error, 1)
	resolve := func(context.Context) bool {
		mu.Lock()
		resolves++
		n := resolves
		mu.Unlock()
		if n > 1 {
			close(pushResolved)
			return false
		}
		go func() {
			_, err := b.AssertContentCollection(t.Context())
			pushDone <- err
		}()
		select {
		case <-pushResolved:
		case <-time.After(200 * time.Millisecond):
		}
		return true
	}
	if err := b.Start(t.Context(), &marotte.StartOpts{Lifetime: t.Context(), ContentCollection: resolve}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := <-pushDone; err != nil {
		t.Fatalf("push AssertContentCollection: %v", err)
	}
	b.Stop()

	data, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read rpc capture: %v", err)
	}
	var values []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if !strings.Contains(line, `"configId":"`+marotte.ConfigOptionContentCollection+`"`) {
			continue
		}
		params := digObject(t, "the contentCollection params", line, "params")
		v, _ := params[keyConfigValue].(string)
		values = append(values, v)
	}
	want := []string{marotte.ConfigValueContentCollectionEnabled, marotte.ConfigValueContentCollectionDisabled}
	if !slices.Equal(values, want) {
		t.Errorf("contentCollection writes in wire order = %q, want %q (the push's newer value last)", values, want)
	}
}
