package bridge

import (
	"slices"
	"strings"
	"testing"
)

func TestFilterACPArgs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty", nil, []string{}},
		{"keeps verbose", []string{"-v"}, []string{"-v"}},
		{
			// kiro-cli rejects --agent alongside v3 and exits before initialize.
			name: "refuses agent and its value",
			in:   []string{"--agent", "my-agent", "-v"},
			want: []string{"-v"},
		},
		{
			name: "refuses agent inline without eating the next token",
			in:   []string{"--agent=my-agent", "-v"},
			want: []string{"-v"},
		},
		{
			// marotte removed the v2 handlers, so an operator v1/v2 stalls session/new.
			name: "refuses agent-engine and its value",
			in:   []string{"--agent-engine", "v2", "-v"},
			want: []string{"-v"},
		},
		{
			// The --flag=value spelling is refused too.
			name: "refuses agent-engine in inline form without eating the next token",
			in:   []string{"--agent-engine=v2", "-v"},
			want: []string{"-v"},
		},
		{
			name: "refuses trust-all long and short",
			in:   []string{"--trust-all-tools", "-a", "-v"},
			want: []string{"-v"},
		},
		{
			name: "refuses trust-tools and its value",
			in:   []string{"--trust-tools", "fs_read,fs_write", "-v"},
			want: []string{"-v"},
		},
		{
			name: "refuses trust-tools inline",
			in:   []string{"--trust-tools=fs_read", "-v"},
			want: []string{"-v"},
		},
		{
			// Fatal: kiro-cli rejects these with --agent-engine=v3 and exits before initialize, taking down every chat bridge.
			name: "refuses model and its value",
			in:   []string{"--model", "claude-opus-5", "-v"},
			want: []string{"-v"},
		},
		{
			name: "refuses model inline without eating the next token",
			in:   []string{"--model=claude-opus-5", "-v"},
			want: []string{"-v"},
		},
		{
			name: "refuses effort and its value",
			in:   []string{"--effort", "max", "-v"},
			want: []string{"-v"},
		},
		{
			name: "refuses effort inline without eating the next token",
			in:   []string{"--effort=max", "-v"},
			want: []string{"-v"},
		},
		{
			name: "keeps an unknown future flag — the whole point of the hatch",
			in:   []string{"--some-flag-upstream-adds", "value"},
			want: []string{"--some-flag-upstream-adds", "value"},
		},
		{
			// -a takes no value, so the next token survives.
			name: "short trust-all does not consume the next token",
			in:   []string{"-a", "--future", "x"},
			want: []string{"--future", "x"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterACPArgs(tc.in)
			if !slices.Equal(got, tc.want) {
				t.Errorf("FilterACPArgs(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestFilterACPArgs_RefusesAuthMethod(t *testing.T) {
	// kiro-cli exits 2 before initialize on an invalid value:
	// error: invalid value 'bogus' for '--auth-method <METHOD>' [possible values: cli]
	cases := map[string][]string{
		"long_separate":  {"--auth-method", "bogus", "-v"},
		"long_inline":    {"--auth-method=bogus", "-v"},
		"alias_separate": {"--authMethod", "bogus", "-v"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if got := filterACPArgs(args); !slices.Equal(got, []string{"-v"}) {
				t.Errorf("FilterACPArgs(%v) = %v, want [-v]", args, got)
			}
		})
	}
}

func TestParseACPArgs(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"empty yields nil", "", nil},
		{"whitespace only yields nil", "   \t ", nil},
		{"splits on whitespace", "-v --future x", []string{"-v", "--future", "x"}},
		{"collapses runs of whitespace", "-v    --future\tx", []string{"-v", "--future", "x"}},
		{"filters while parsing", "--agent-engine v1 -v", []string{"-v"}},
		// Everything refused: empty, not nil.
		{"all refused", "--agent-engine v1 -a", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseACPArgs(tc.raw)
			if !slices.Equal(got, tc.want) {
				t.Errorf("ParseACPArgs(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestRefuseReasonNamesTheRealSurface pins that an operator whose --trust-all-tools silently vanished would assume
// permissions are off, so the reason must point at permissions.yaml. Same for the engine pin.
func TestRefuseReasonNamesTheRealSurface(t *testing.T) {
	cases := []struct {
		flag        string
		wantSubstrs []string
	}{
		{flagAgentEngine, []string{"v3-only"}},
		{flagAuthMethod, []string{"exits before initialize", "cli"}},
		{flagAuthMethodAlias, []string{"exits before initialize", "cli"}},
		{flagTrustAll, []string{"exits before initialize", "permissions.yaml"}},
		{flagTrustAllShort, []string{"exits before initialize", "permissions.yaml"}},
		{flagTrustTools, []string{"exits before initialize", "permissions.yaml"}},
		{flagAgent, []string{"exits before initialize", "mode"}},
		// The fatal pair must say why and where the real control lives.
		{flagModel, []string{"exits before initialize", "composer"}},
		{flagEffort, []string{"exits before initialize", "composer"}},
	}
	for _, tc := range cases {
		t.Run(tc.flag, func(t *testing.T) {
			reason, refused := refuseReason(tc.flag)
			if !refused {
				t.Fatalf("refuseReason(%q) refused = false, want true", tc.flag)
			}
			for _, want := range tc.wantSubstrs {
				if !strings.Contains(reason, want) {
					t.Errorf("reason %q does not mention %q", reason, want)
				}
			}
		})
	}
	// Allow-unknown: a flag upstream adds later must pass.
	for _, flag := range []string{"-v", "--future"} {
		if _, refused := refuseReason(flag); refused {
			t.Errorf("refuseReason(%q) refused = true, want false", flag)
		}
	}
}

// TestBuildACPArgsPrecedesExtraArgs pins that a launch flag is an initial value, so it follows the derived args (kiro-cli
// takes the last spelling) and switch_model / set_effort still win later.
func TestBuildACPArgsPrecedesExtraArgs(t *testing.T) {
	derived := buildACPArgs("v3")
	extra := []string{"-v"}
	full := append(slices.Clone(derived), extra...)

	if len(full) <= len(derived) {
		t.Fatalf("extra args did not append: %v", full)
	}
	if full[len(full)-1] != "-v" {
		t.Errorf("last arg = %q, want the operator flag last", full[len(full)-1])
	}
	if !slices.Equal(full[:len(derived)], derived) {
		t.Errorf("derived prefix changed: %v, want %v", full[:len(derived)], derived)
	}
}

// The counts are the whole diagnostic: values are never logged, since a flag could carry a secret. Not parallel: it
// swaps the process-wide slog default.
func TestParseACPArgs_LogsHowManyItKeptAndRefused(t *testing.T) {
	logs := captureLogs(t)

	const raw = "--agent-engine v2 -v"
	ParseACPArgs(raw)

	for _, want := range []string{"acp_args_count=1", "refused_count=2"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("ParseACPArgs(%q) log does not contain %s\nlog:\n%s", raw, want, logs.String())
		}
	}
}
