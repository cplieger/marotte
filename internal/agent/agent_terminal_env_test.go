package agent

import (
	"slices"
	"strings"
	"testing"
)

// TestScreenAgentEnv_RefusesExecutionRedirection covers one case per mechanism, not per name.
func TestScreenAgentEnv_RefusesExecutionRedirection(t *testing.T) {
	cases := []struct {
		name      string
		vars      []termEnvVar
		wantFirst string
	}{
		{
			"loader injection into any dynamically linked process",
			[]termEnvVar{{Name: "LD_PRELOAD", Value: "/tmp/evil.so"}},
			"LD_PRELOAD",
		},
		{
			"interpreter startup hook",
			[]termEnvVar{{Name: "NODE_OPTIONS", Value: "--require /tmp/evil.js"}},
			"NODE_OPTIONS",
		},
		{
			"git helper that takes a command",
			[]termEnvVar{{Name: "GIT_SSH_COMMAND", Value: "sh -c curl|sh"}},
			"GIT_SSH_COMMAND",
		},
		{
			"helper program spawn",
			[]termEnvVar{{Name: "PAGER", Value: "sh -c whoami"}},
			"PAGER",
		},
		{
			"command resolution",
			[]termEnvVar{{Name: "PATH", Value: "/tmp/bin"}},
			"PATH",
		},
		{
			"shell pre-command hook",
			[]termEnvVar{{Name: "BASH_ENV", Value: "/tmp/evil.sh"}},
			"BASH_ENV",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := screenAgentEnv(c.vars, nil)
			if len(got) != 1 || got[0] != c.wantFirst {
				t.Errorf("screenAgentEnv = %v, want [%s] refused", got, c.wantFirst)
			}
		})
	}
}

// TestScreenAgentEnv_AllowsOrdinaryAndInertValues is the usability half: `GIT_PAGER=cat`
// and `PAGER=` must pass, or operators disable the guard.
func TestScreenAgentEnv_AllowsOrdinaryAndInertValues(t *testing.T) {
	ok := []termEnvVar{
		{Name: "CGO_ENABLED", Value: "0"},
		{Name: "GOFLAGS", Value: "-mod=readonly"},
		{Name: "TERM", Value: "dumb"},
		{Name: "LANG", Value: "C.UTF-8"},
		{Name: "GIT_PAGER", Value: "cat"},
		{Name: "PAGER", Value: ""},
		{Name: "GIT_ASKPASS", Value: "true"},
		// Case variants are inert: the loader reads LD_PRELOAD only.
		{Name: "ld_preload", Value: "/tmp/evil.so"},
	}
	if got := screenAgentEnv(ok, nil); len(got) != 0 {
		t.Errorf("screenAgentEnv refused %v, want none refused", got)
	}
}

// TestScreenAgentEnv_ReportsEveryOffenderInOrder pins that the refusal names all of them, in request order.
func TestScreenAgentEnv_ReportsEveryOffenderInOrder(t *testing.T) {
	got := screenAgentEnv([]termEnvVar{
		{Name: "LD_PRELOAD", Value: "/tmp/a.so"},
		{Name: "CGO_ENABLED", Value: "0"},
		{Name: "PYTHONSTARTUP", Value: "/tmp/b.py"},
	}, nil)
	want := []string{"LD_PRELOAD", "PYTHONSTARTUP"}
	if !slices.Equal(got, want) {
		t.Errorf("screenAgentEnv = %v, want %v", got, want)
	}
}

// TestScreenAgentEnv_OperatorAllowlist covers the escape hatch. The allowlist is a
// parameter because sync.OnceValue resolves once per process.
func TestScreenAgentEnv_OperatorAllowlist(t *testing.T) {
	allowed := parseAllowedEnv(" LD_PRELOAD , NODE_PATH ")

	if got := screenAgentEnv([]termEnvVar{{Name: "LD_PRELOAD", Value: "/opt/profiler.so"}}, allowed); len(got) != 0 {
		t.Errorf("an allowed name was still refused: %v", got)
	}
	// Per name, never a blanket off switch.
	if got := screenAgentEnv([]termEnvVar{{Name: "BASH_ENV", Value: "/tmp/evil.sh"}}, allowed); len(got) != 1 {
		t.Errorf("allowing LD_PRELOAD also allowed BASH_ENV: %v", got)
	}
}

// TestParseAllowedEnv covers the shapes a hand-edited compose file produces.
func TestParseAllowedEnv(t *testing.T) {
	if got := parseAllowedEnv(""); got != nil {
		t.Errorf("empty = %v, want nil (no allowlist at all)", got)
	}
	if got := parseAllowedEnv("   "); got != nil {
		t.Errorf("blank = %v, want nil: whitespace is not an allowlist of one", got)
	}
	got := parseAllowedEnv("LD_PRELOAD, NODE_PATH ,, ")
	if len(got) != 2 {
		t.Fatalf("parseAllowedEnv = %v, want 2 names (empty entries dropped)", got)
	}
	for _, want := range []string{"LD_PRELOAD", "NODE_PATH"} {
		if _, ok := got[want]; !ok {
			t.Errorf("%q missing from %v: surrounding spaces must be trimmed", want, got)
		}
	}
}

// TestDangerousAgentEnv_MatchesUpstream pins the list against kiro-cli's
// `dangerous_env_vars`, so upstream drift becomes a deliberate edit. Hardcoded: CI has no binary.
func TestDangerousAgentEnv_MatchesUpstream(t *testing.T) {
	upstream := strings.Fields(`
		PAGER EDITOR VISUAL BROWSER MANPAGER GIT_PAGER LESS LESSOPEN LESSCLOSE
		LD_PRELOAD LD_LIBRARY_PATH DYLD_INSERT_LIBRARIES DYLD_LIBRARY_PATH
		PYTHONWARNINGS PYTHONSTARTUP PYTHONPATH PYTHONHOME
		PERL5OPT PERL5LIB RUBYOPT RUBYLIB NODE_OPTIONS NODE_PATH
		IFS PATH HOME SHELL PROMPT_COMMAND BASH_ENV ENV
		GIT_EDITOR GIT_SEQUENCE_EDITOR GIT_ASKPASS GIT_EXTERNAL_DIFF
		GIT_SSH GIT_SSH_COMMAND GIT_PROXY_COMMAND GIT_EXEC_PATH GIT_TEMPLATE_DIR`)

	for _, name := range upstream {
		if _, ok := dangerousAgentEnv[name]; !ok {
			t.Errorf("upstream screens %q and this list does not", name)
		}
	}
	if len(dangerousAgentEnv) != len(upstream) {
		t.Errorf("list has %d names, upstream has %d: reconcile the difference deliberately",
			len(dangerousAgentEnv), len(upstream))
	}
	for _, v := range []string{"", "true", "cat"} {
		if _, ok := safeAgentEnvValues[v]; !ok {
			t.Errorf("upstream treats %q as an inert value and this does not", v)
		}
	}
}

// lastValueFor answers what the child sees for name: os/exec keeps the last value.
func lastValueFor(env []string, name string) string {
	prefix := name + "="
	value := ""
	for _, entry := range env {
		if v, ok := strings.CutPrefix(entry, prefix); ok {
			value = v
		}
	}
	return value
}

// TestTermEnv_LocalePinBeatsAnAgentSuppliedLocale pins ordering, not membership: LANG is
// absent from dangerousAgentEnv, which stays upstream's verbatim.
func TestTermEnv_LocalePinBeatsAnAgentSuppliedLocale(t *testing.T) {
	env := termEnv([]termEnvVar{{Name: termLocaleEnvVar, Value: "hostile"}})

	want := termLocaleEnv()[0]
	got := termLocaleEnvVar + "=" + lastValueFor(env, termLocaleEnvVar)
	if got != want {
		t.Errorf("termEnv(%s=hostile) leaves %q in force, want %q: os/exec keeps the last value, so the pin must be appended AFTER the agent's pairs",
			termLocaleEnvVar, got, want)
	}
}
