package agent

import (
	"testing"

	"github.com/cplieger/marotte/internal/testsupport"
)

// Both forms carry kirorelease.NoAutoUpdateVar under an `env -i` parent, over a parent opt-out and over the agent's
// own vars.
func TestAgentCommand_RunsWithKiroCLISelfUpdateOff(t *testing.T) {
	parents := map[string]struct {
		parent string
		vars   []termEnvVar
	}{
		"an env -i parent":              {},
		"a parent that opts back in":    {parent: "0"},
		"agent vars that opt back in":   {vars: []termEnvVar{{Name: "KIRO_NO_AUTO_UPDATE", Value: "0"}}},
		"agent vars beside the opt-out": {parent: "0", vars: []termEnvVar{{Name: "KIRO_SESSION_ID", Value: "s"}}},
	}
	for name, tc := range parents {
		for form, args := range map[string]*[]string{"shell": nil, "argv": {}} {
			t.Run(name+", "+form, func(t *testing.T) {
				testsupport.EmptyEnv(t)
				if tc.parent != "" {
					t.Setenv("KIRO_NO_AUTO_UPDATE", tc.parent)
				}
				cli, seen := testsupport.EnvRecorder(t, "KIRO_NO_AUTO_UPDATE")
				line := cli
				if args == nil {
					line = "'" + cli + "'"
				}
				if out, err := agentCommand(t.Context(), line, args, tc.vars).CombinedOutput(); err != nil {
					t.Fatalf("agent command: %v: %s", err, out)
				}
				if got := seen(); got != "1" {
					t.Errorf("kiro-cli saw KIRO_NO_AUTO_UPDATE %q, want 1", got)
				}
			})
		}
	}
}
