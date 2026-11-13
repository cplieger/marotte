package bridge

import (
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestSessionVerbs_CarryTheCustomAgents pins a session's client custom agents on both verbs inside the door's
// _meta.kiro: session/load rebuilds the session's registry from that call's list.
func TestSessionVerbs_CarryTheCustomAgents(t *testing.T) {
	agent := marotte.ClientCustomAgent{ID: marotte.BotRoleID, Prompt: "probe", Tools: "*", IncludeMCPJSON: true}
	for _, method := range []string{"session/new", "session/load"} {
		opts := &marotte.SessionOpts{Model: "m", CustomAgents: []marotte.ClientCustomAgent{agent}}
		if method == "session/load" {
			opts.SessionID = "sess_resume_door"
		}
		line := captureRequest(t, method, opts)
		kiro := digObject(t, "the session door", line, "params", "_meta", "kiro")
		agents, ok := kiro["customAgents"].([]any)
		if !ok || len(agents) != 1 {
			t.Fatalf("%s carried customAgents=%v, want one entry:\n%s", method, kiro["customAgents"], line)
		}
		got, _ := agents[0].(map[string]any)
		if got["id"] != marotte.BotRoleID || got["prompt"] != "probe" || got["tools"] != "*" || got["includeMcpJson"] != true {
			t.Errorf("%s customAgents[0] = %v, want the probe agent", method, got)
		}
		if _, ok := kiro["settings"].(map[string]any); !ok {
			t.Errorf("%s lost the door's settings beside the custom agents:\n%s", method, line)
		}
	}
}

// TestSessionVerbs_NoCustomAgentsSendsNoKey pins that a regular chat's door sends no key, not [].
func TestSessionVerbs_NoCustomAgentsSendsNoKey(t *testing.T) {
	for _, method := range []string{"session/new", "session/load"} {
		opts := &marotte.SessionOpts{Model: "m"}
		if method == "session/load" {
			opts.SessionID = "sess_resume_door"
		}
		if line := captureRequest(t, method, opts); strings.Contains(line, `"customAgents"`) {
			t.Errorf("%s carried a customAgents key with none set:\n%s", method, line)
		}
	}
}
