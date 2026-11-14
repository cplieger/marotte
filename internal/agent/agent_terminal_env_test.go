package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
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
				if out, err := agentCommand(t.Context(), line, args, "", tc.vars).CombinedOutput(); err != nil {
					t.Fatalf("agent command: %v: %s", err, out)
				}
				if got := seen(); got != "1" {
					t.Errorf("kiro-cli saw KIRO_NO_AUTO_UPDATE %q, want 1", got)
				}
			})
		}
	}
}

// Both cwd sources, with and without agent vars, under a parent PWD that names another directory.
func TestTermCreate_CommandSeesItsCwdAsPWD(t *testing.T) {
	for _, withVars := range []bool{false, true} {
		for _, cwd := range []string{"", "sub"} {
			t.Run(fmt.Sprintf("vars=%v cwd=%q", withVars, cwd), func(t *testing.T) {
				t.Setenv("PWD", "/marotte-own-pwd")
				work := t.TempDir()
				if err := os.Mkdir(filepath.Join(work, "sub"), 0o750); err != nil {
					t.Fatal(err)
				}
				h := hubWithBridge(t, work, newRecordingTermBridge())
				id := int64(1)
				params := map[string]any{"command": "printenv", "args": []string{"PWD"}}
				if cwd != "" {
					params["cwd"] = filepath.Join(work, cwd)
				}
				if withVars {
					params["env"] = []map[string]string{{"name": "KIRO_SESSION_ID", "value": "s"}}
				}
				msg := &marotte.RPCResponse{ID: &id, Method: methodTermCreate, Params: mustJSON(t, params)}
				got := strings.TrimSuffix(waitForTermExit(t, h, msg), "\n")
				dir := singleTerm(t, h).cmd.Dir
				if want := filepath.Join(work, cwd); dir != want {
					t.Fatalf("command ran in %q, want %q", dir, want)
				}
				if got != dir {
					t.Errorf("command saw PWD %q, want its cwd %q", got, dir)
				}
			})
		}
	}
}
