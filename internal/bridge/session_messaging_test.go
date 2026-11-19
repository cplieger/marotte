package bridge

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// messagingScript is a fake kiro-cli whose door result offers sessionToolsMessaging at $SM_OFFERED (no option when
// empty), and whose set_config_option reply reports the value sent. Every request is appended to $RPC_CAPTURE.
const messagingScript = `#!/bin/sh
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$RPC_CAPTURE"
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(echo "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  sid=$(echo "$line" | sed -n 's/.*"overrideSessionId":"\([^"]*\)".*/\1/p')
  [ -n "$sid" ] || sid=$(echo "$line" | sed -n 's/.*"sessionId":"\([^"]*\)".*/\1/p')
  sm=''
  if [ -n "$SM_OFFERED" ]; then sm=',{"id":"sessionToolsMessaging","type":"select","currentValue":"'"$SM_OFFERED"'"}'; fi
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"serverInfo":{"name":"fake"}}}\n' "$id"
      ;;
    session/new|session/load)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"%s","modes":{"currentModeId":"default","availableModes":[{"id":"default","name":"Default","description":"d"}]},"configOptions":[{"id":"model","currentValue":"m","options":[{"value":"m","name":"M","description":"x","_meta":{"kiro":{"rateMultiplier":1.0}}}]}%s]}}\n' "$id" "$sid" "$sm"
      ;;
    session/set_config_option)
      case "$line" in
        *'"configId":"sessionToolsMessaging"'*)
          got=$(echo "$line" | sed -n 's/.*"value":"\([^"]*\)".*/\1/p')
          printf '{"jsonrpc":"2.0","id":%s,"result":{"configOptions":[{"id":"sessionToolsMessaging","type":"select","currentValue":"%s"}]}}\n' "$id" "$got"
          ;;
        *) printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id" ;;
      esac
      ;;
    *)
      if [ -n "$id" ]; then printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"; fi
      ;;
  esac
done
`

// openMessagingSession opens one session over messagingScript, a session/load when sessionID is set, and returns it
// with the capture file's path.
func openMessagingSession(t *testing.T, offered, sessionID string) (s *Session, capture string) {
	t.Helper()
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "fake-kiro-cli")
	if err := os.WriteFile(scriptPath, []byte(messagingScript), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}
	t.Setenv("HOME", t.TempDir())
	capture = filepath.Join(t.TempDir(), "rpc.jsonl")
	t.Setenv("RPC_CAPTURE", capture)
	t.Setenv("SM_OFFERED", offered)
	s, err := openSessionOn(t, NewEngine(scriptPath, dir), &marotte.SessionOpts{
		Model: "m", SessionID: sessionID, Features: marotte.SessionFeatures{Messaging: true},
	})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	return s, capture
}

// messagingSets returns the value of every sessionToolsMessaging set_config_option in the capture, in order.
func messagingSets(t *testing.T, capture string) []string {
	t.Helper()
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read rpc capture: %v", err)
	}
	var values []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if !strings.Contains(line, `"method":"session/set_config_option"`) ||
			!strings.Contains(line, `"configId":"`+marotte.ConfigOptionSessionMessaging+`"`) {
			continue
		}
		params := digObject(t, "the sessionToolsMessaging set_config_option params", line, "params")
		v, _ := params[keyConfigValue].(string)
		values = append(values, v)
	}
	return values
}

func ensureMessaging(t *testing.T, s *Session, on bool) {
	t.Helper()
	if err := s.EnsureMessaging(t.Context(), on); err != nil {
		t.Fatalf("EnsureMessaging(%v) = %v, want nil", on, err)
	}
}

func TestSessionEnsureMessaging_SendsOnlyWhenTheReportDiffers(t *testing.T) {
	s, capture := openMessagingSession(t, marotte.SessionMessagingOn, "")
	ensureMessaging(t, s, true)
	if got := messagingSets(t, capture); len(got) != 0 {
		t.Fatalf("EnsureMessaging(true) on a session reporting on sent %q, want nothing", got)
	}
	// A config_option_update's report is the session's value too.
	s.ObserveMessaging(marotte.SessionMessagingOff)
	ensureMessaging(t, s, false)
	if got := messagingSets(t, capture); len(got) != 0 {
		t.Fatalf("EnsureMessaging(false) after the session reported off sent %q, want nothing", got)
	}
	ensureMessaging(t, s, true)
	if got := messagingSets(t, capture); !slices.Equal(got, []string{marotte.SessionMessagingOn}) {
		t.Errorf("EnsureMessaging(true) on a session reporting off sent %q, want [on]", got)
	}
}

func TestSessionEnsureMessaging_SendsNothingWhereTheOptionIsAbsent(t *testing.T) {
	s, capture := openMessagingSession(t, "", "")
	ensureMessaging(t, s, false)
	ensureMessaging(t, s, true)
	if got := messagingSets(t, capture); len(got) != 0 {
		t.Errorf("a session whose door result lists no sessionToolsMessaging was sent %q, want nothing", got)
	}
}

func TestSessionEnsureMessaging_ReadsTheDoorResultAndTheReply(t *testing.T) {
	for _, door := range []struct{ name, sessionID string }{{"new", ""}, {"load", "sess_sm_load"}} {
		t.Run(door.name, func(t *testing.T) {
			s, capture := openMessagingSession(t, marotte.SessionMessagingOff, door.sessionID)
			ensureMessaging(t, s, false)
			if got := messagingSets(t, capture); len(got) != 0 {
				t.Fatalf("EnsureMessaging(false) after a door result reporting off sent %q, want nothing", got)
			}
			ensureMessaging(t, s, true)
			ensureMessaging(t, s, true)
			if got := messagingSets(t, capture); !slices.Equal(got, []string{marotte.SessionMessagingOn}) {
				t.Errorf("two EnsureMessaging(true) calls sent %q, want one on: the reply's value is the session's", got)
			}
		})
	}
}
