package bridge

import (
	"slices"
	"testing"
)

// TestBuildACPArgs_SpawnArgsComeFromTheDeclaration pins the engine's argv: the v3 engine, then the sign-in backend's
// declared arguments, once, and nothing else of marotte's. The operator's tail cannot add a second auth method,
// because refuseReason filters it before it is appended.
func TestBuildACPArgs_SpawnArgsComeFromTheDeclaration(t *testing.T) {
	declared := []string{"--auth-method", "client"}
	got := buildACPArgs(declared)
	if want := []string{"acp", "--agent-engine", "v3", "--auth-method", "client"}; !slices.Equal(got, want) {
		t.Errorf("buildACPArgs(%v) = %v, want %v", declared, got, want)
	}
	full := append(got, filterACPArgs([]string{"--auth-method", "external_idp", "--authMethod=external_idp", "-v"})...)
	n := 0
	for _, a := range full {
		if a == flagAuthMethod || a == flagAuthMethodAlias || len(a) > len(flagAuthMethod) && a[:len(flagAuthMethod)+1] == flagAuthMethod+"=" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("argv %v carries %d auth-method flags, want exactly the declaration's one", full, n)
	}
	if bare := buildACPArgs(nil); !slices.Equal(bare, []string{"acp", "--agent-engine", "v3"}) {
		t.Errorf("buildACPArgs(nil) = %v, want no auth method marotte did not get from a declaration", bare)
	}
}

// kiro-cli refuses --model and --effort alongside --agent-engine=v3 and exits before initialize:
//
//	error: the following arguments are not supported with --agent-engine=v3: --model, --effort
//
// Emitting either stops the engine, so the initial values go over session/set_config_option. Measured on kiro-cli
// 2.17.0 and 2.18.0; `-v` is the only other flag v3 accepts.
func TestBuildACPArgs_OmitsFlagsV3Refuses(t *testing.T) {
	refused := []string{"--model", "--effort", "--agent", "--trust-all-tools", "--trust-tools"}
	args := buildACPArgs([]string{"--auth-method", "client"})
	for _, flag := range refused {
		if slices.Contains(args, flag) {
			t.Errorf("buildACPArgs = %v, must not carry %s", args, flag)
		}
	}
}
