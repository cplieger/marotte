package bridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// workflowLaunchScript is a fake kiro-cli whose door result offers workflowLaunch at $WL_OFFERED (no option when
// empty), and whose set_config_option reply reports the value sent when $WL_CONFIRM is 1, else $WL_OFFERED, as KAS's
// setter answers for a value it ignored. Every request is appended to $RPC_CAPTURE.
const workflowLaunchScript = `#!/bin/sh
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$RPC_CAPTURE"
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(echo "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  sid=$(echo "$line" | sed -n 's/.*"overrideSessionId":"\([^"]*\)".*/\1/p')
  [ -n "$sid" ] || sid=$(echo "$line" | sed -n 's/.*"sessionId":"\([^"]*\)".*/\1/p')
  wl=''
  if [ -n "$WL_OFFERED" ]; then wl=',{"id":"workflowLaunch","type":"select","currentValue":"'"$WL_OFFERED"'"}'; fi
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"serverInfo":{"name":"fake"}}}\n' "$id"
      ;;
    session/new|session/load)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"%s","modes":{"currentModeId":"default","availableModes":[{"id":"default","name":"Default","description":"d"}]},"configOptions":[{"id":"model","currentValue":"m","options":[{"value":"m","name":"M","description":"x","_meta":{"kiro":{"rateMultiplier":1.0}}}]}%s]}}\n' "$id" "$sid" "$wl"
      ;;
    session/set_config_option)
      case "$line" in
        *'"configId":"workflowLaunch"'*)
          got="$WL_OFFERED"
          if [ "$WL_CONFIRM" = 1 ]; then got=$(echo "$line" | sed -n 's/.*"value":"\([^"]*\)".*/\1/p'); fi
          printf '{"jsonrpc":"2.0","id":%s,"result":{"configOptions":[{"id":"workflowLaunch","type":"select","currentValue":"%s"}]}}\n' "$id" "$got"
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

// openWorkflowLaunchSession opens one session over workflowLaunchScript, as openSessionOn does, and returns it with
// the capture file's path.
func openWorkflowLaunchSession(t *testing.T, offered string, confirm bool, opts *marotte.SessionOpts) (s *Session, capture string) {
	t.Helper()
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "fake-kiro-cli")
	if err := os.WriteFile(scriptPath, []byte(workflowLaunchScript), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}
	t.Setenv("HOME", t.TempDir())
	capture = filepath.Join(t.TempDir(), "rpc.jsonl")
	t.Setenv("RPC_CAPTURE", capture)
	t.Setenv("WL_OFFERED", offered)
	confirmEnv := "0"
	if confirm {
		confirmEnv = "1"
	}
	t.Setenv("WL_CONFIRM", confirmEnv)
	s, err := openSessionOn(t, NewEngine(scriptPath, dir), opts)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	return s, capture
}

// workflowLaunchSets returns the value of every workflowLaunch set_config_option in the capture, in order.
func workflowLaunchSets(t *testing.T, capture string) []string {
	t.Helper()
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read rpc capture: %v", err)
	}
	var values []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if !strings.Contains(line, `"method":"session/set_config_option"`) ||
			!strings.Contains(line, `"configId":"`+marotte.ConfigOptionWorkflowLaunch+`"`) {
			continue
		}
		params := digObject(t, "the workflowLaunch set_config_option params", line, "params")
		v, _ := params[keyConfigValue].(string)
		values = append(values, v)
	}
	return values
}

func TestNewSession_AssertsWorkflowLaunchWhenOfferedAndDifferent(t *testing.T) {
	_, capture := openWorkflowLaunchSession(t, marotte.WorkflowLaunchWhenAsked, true, &marotte.SessionOpts{
		Model: "m", Features: marotte.SessionFeatures{Workflows: true, WorkflowLaunch: marotte.WorkflowLaunchDelegateAll},
	})
	got := workflowLaunchSets(t, capture)
	if len(got) != 1 || got[0] != marotte.WorkflowLaunchDelegateAll {
		t.Errorf("session/new offering %q with want %q sent workflowLaunch values %q, want exactly [%q]",
			marotte.WorkflowLaunchWhenAsked, marotte.WorkflowLaunchDelegateAll, got, marotte.WorkflowLaunchDelegateAll)
	}
}

func TestLoadSession_ReAssertsWorkflowLaunch(t *testing.T) {
	_, capture := openWorkflowLaunchSession(t, marotte.WorkflowLaunchWhenAsked, true, &marotte.SessionOpts{
		Model: "m", SessionID: "sess_wl_load",
		Features: marotte.SessionFeatures{Workflows: true, WorkflowLaunch: marotte.WorkflowLaunchLargeTasks},
	})
	got := workflowLaunchSets(t, capture)
	if len(got) != 1 || got[0] != marotte.WorkflowLaunchLargeTasks {
		t.Errorf("session/load offering %q with want %q sent workflowLaunch values %q, want exactly [%q]",
			marotte.WorkflowLaunchWhenAsked, marotte.WorkflowLaunchLargeTasks, got, marotte.WorkflowLaunchLargeTasks)
	}
}

func TestSessionDoors_SendNoWorkflowLaunchWhenNotOfferedOrEqual(t *testing.T) {
	cases := []struct {
		name    string
		offered string
		want    string
	}{
		{"not offered", "", marotte.WorkflowLaunchDelegateAll},
		{"already at the wanted value", marotte.WorkflowLaunchLargeTasks, marotte.WorkflowLaunchLargeTasks},
		{"no wanted value", marotte.WorkflowLaunchWhenAsked, ""},
	}
	for _, tc := range cases {
		for _, verb := range []string{"new", "load"} {
			t.Run(tc.name+" on "+verb, func(t *testing.T) {
				opts := &marotte.SessionOpts{Model: "m", Features: marotte.SessionFeatures{Workflows: true, WorkflowLaunch: tc.want}}
				if verb == "load" {
					opts.SessionID = "sess_wl_quiet"
				}
				_, capture := openWorkflowLaunchSession(t, tc.offered, true, opts)
				if got := workflowLaunchSets(t, capture); len(got) != 0 {
					t.Errorf("session/%s sent workflowLaunch values %q, want none", verb, got)
				}
			})
		}
	}
}

func TestHealWorkflowLaunch_StopsAfterAnUnconfirmedReply(t *testing.T) {
	s, capture := openWorkflowLaunchSession(t, marotte.WorkflowLaunchWhenAsked, false, &marotte.SessionOpts{
		Model: "m", Features: marotte.SessionFeatures{Workflows: true, WorkflowLaunch: marotte.WorkflowLaunchDelegateAll},
	})
	if got := workflowLaunchSets(t, capture); len(got) != 1 {
		t.Fatalf("the door sent workflowLaunch values %q, want one assert", got)
	}
	if err := s.HealWorkflowLaunch(t.Context(), marotte.WorkflowLaunchWhenAsked); err != nil {
		t.Errorf("HealWorkflowLaunch after the refused door assert = %v, want nil", err)
	}
	if got := workflowLaunchSets(t, capture); len(got) != 1 {
		t.Errorf("a heal after an unconfirmed door assert sent workflowLaunch values %q, want only the door's one", got)
	}
}

func TestHealWorkflowLaunch_AssertsOnceTheOptionAppears(t *testing.T) {
	s, capture := openWorkflowLaunchSession(t, "", true, &marotte.SessionOpts{
		Model: "m", Features: marotte.SessionFeatures{Workflows: true, WorkflowLaunch: marotte.WorkflowLaunchDelegateAll},
	})
	if got := workflowLaunchSets(t, capture); len(got) != 0 {
		t.Fatalf("a door result without the option sent workflowLaunch values %q, want none", got)
	}
	// The option appears on a mode switch; the session confirms, and its own echo then sends nothing.
	for _, reported := range []string{marotte.WorkflowLaunchWhenAsked, marotte.WorkflowLaunchDelegateAll} {
		if err := s.HealWorkflowLaunch(t.Context(), reported); err != nil {
			t.Errorf("HealWorkflowLaunch(%q) = %v, want nil", reported, err)
		}
	}
	got := workflowLaunchSets(t, capture)
	if len(got) != 1 || got[0] != marotte.WorkflowLaunchDelegateAll {
		t.Errorf("heals sent workflowLaunch values %q, want exactly [%q]", got, marotte.WorkflowLaunchDelegateAll)
	}
}
