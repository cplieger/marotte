package bridge

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// settleFake writes a fake kiro-cli whose session/new and session/load results carry modelOption as their model
// select, logging every request to the returned log path. refuseModel answers a model set_config_option with an error.
func settleFake(t *testing.T, modelOption string, refuseModel bool) (script, logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "requests.log")
	refuse := ""
	if refuseModel {
		refuse = `
  case "$line" in
    *'"configId":"model"'*)
      printf '{"jsonrpc":"2.0","id":%s,"error":{"code":-32602,"message":"no"}}\n' "$id"
      continue
      ;;
  esac`
	}
	body := fmt.Sprintf(`#!/bin/sh
while IFS= read -r line; do
  printf '%%s\n' "$line" >> "%s"
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(echo "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  sid=$(echo "$line" | sed -n 's/.*"overrideSessionId":"\([^"]*\)".*/\1/p')%s
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%%s,"result":{"protocolVersion":1,"serverInfo":{"name":"fake"}}}\n' "$id"
      ;;
    session/new)
      printf '{"jsonrpc":"2.0","id":%%s,"result":{"sessionId":"%%s","configOptions":[%s]}}\n' "$id" "$sid"
      ;;
    session/load)
      printf '{"jsonrpc":"2.0","id":%%s,"result":{"configOptions":[%s]}}\n' "$id"
      ;;
    *)
      if [ -n "$id" ]; then
        printf '{"jsonrpc":"2.0","id":%%s,"result":{}}\n' "$id"
      fi
      ;;
  esac
done
`, logPath, refuse, modelOption, modelOption)
	script = filepath.Join(dir, "fake-kiro-cli")
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}
	return script, logPath
}

// modelSelect is a model config option reporting current with choices listed.
func modelSelect(current string, choices ...string) string {
	opts := make([]string, len(choices))
	for i, c := range choices {
		opts[i] = fmt.Sprintf(`{"value":"%s","name":"%s"}`, c, c)
	}
	return fmt.Sprintf(`{"id":"model","currentValue":"%s","options":[%s]}`, current, strings.Join(opts, ","))
}

// modelSets is the values of every model set_config_option the fake received, in order.
func modelSets(t *testing.T, logPath string) []string {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	var values []string
	for line := range strings.SplitSeq(string(raw), "\n") {
		if !strings.Contains(line, `"method":"session/set_config_option"`) || !strings.Contains(line, `"configId":"model"`) {
			continue
		}
		_, after, _ := strings.Cut(line, `"value":"`)
		value, _, _ := strings.Cut(after, `"`)
		values = append(values, value)
	}
	return values
}

func openSettleSession(t *testing.T, modelOption string, refuse, load bool) (*Session, []string) {
	t.Helper()
	script, logPath := settleFake(t, modelOption, refuse)
	opts := &marotte.SessionOpts{}
	if load {
		opts.SessionID = "sess-settle"
	}
	s, err := openSessionOn(t, NewEngine(script, filepath.Dir(script)), opts)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	return s, modelSets(t, logPath)
}

// KAS restores a persisted model on session/load without judging it against the catalog; the door moves a session
// pinned to an unlisted auto onto the first listed id when auto is not offered.
func TestSessionLoadMovesOffUnlistedModel(t *testing.T) {
	s, sets := openSettleSession(t, modelSelect(marotte.ModelAuto, "m1", "m2"), false, true)
	if len(sets) != 1 || sets[0] != "m1" {
		t.Errorf("model set_config_option values = %q, want [m1]", sets)
	}
	if got := s.ModelID(); got != "m1" {
		t.Errorf("ModelID() = %q, want m1", got)
	}
}

// A concrete unlisted id is the same defect as auto, and a listed auto is the fallback ahead of the first id.
func TestSessionLoadMovesOffUnlistedConcreteModel(t *testing.T) {
	s, sets := openSettleSession(t, modelSelect("retired-1", "m1", marotte.ModelAuto), false, true)
	if len(sets) != 1 || sets[0] != marotte.ModelAuto {
		t.Errorf("model set_config_option values = %q, want [auto]", sets)
	}
	if got := s.ModelID(); got != marotte.ModelAuto {
		t.Errorf("ModelID() = %q, want auto", got)
	}
}

// session/new runs the same step, so a session KAS seeded on an unlisted id is moved too.
func TestSessionNewMovesOffUnlistedModel(t *testing.T) {
	s, sets := openSettleSession(t, modelSelect("retired-1", "m1", "m2"), false, false)
	if len(sets) != 1 || sets[0] != "m1" {
		t.Errorf("model set_config_option values = %q, want [m1]", sets)
	}
	if got := s.ModelID(); got != "m1" {
		t.Errorf("ModelID() = %q, want m1", got)
	}
}

// A listed model is left alone, and an unknown catalog (no choices, or kiro-cli's cold one-choice pin) judges nothing.
func TestSessionDoorKeepsListedOrColdModel(t *testing.T) {
	cases := []struct {
		name, option, want string
		load               bool
	}{
		{"a listed model on load", modelSelect("m2", "m1", "m2"), "m2", true},
		{"a listed auto on load", modelSelect(marotte.ModelAuto, "m1", marotte.ModelAuto), marotte.ModelAuto, true},
		{"a listed model on new", modelSelect("m1", "m1", "m2"), "m1", false},
		{"no choices", `{"id":"model","currentValue":"retired-1"}`, "retired-1", true},
		{"a cold pin", modelSelect("retired-1", "retired-1"), "retired-1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, sets := openSettleSession(t, tc.option, false, tc.load)
			if len(sets) != 0 {
				t.Errorf("model set_config_option values = %q, want none", sets)
			}
			if got := s.ModelID(); string(got) != tc.want {
				t.Errorf("ModelID() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The step is best-effort like the door's other appliers: a refused move opens the session on the model KAS reported.
func TestSessionDoorSurvivesRefusedSettle(t *testing.T) {
	s, sets := openSettleSession(t, modelSelect(marotte.ModelAuto, "m1"), true, true)
	if len(sets) != 1 || sets[0] != "m1" {
		t.Errorf("model set_config_option values = %q, want [m1]", sets)
	}
	if !s.Open() {
		t.Error("the door failed on a refused move")
	}
	if got := s.ModelID(); got != marotte.ModelAuto {
		t.Errorf("ModelID() = %q, want auto (unchanged)", got)
	}
}
