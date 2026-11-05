package translate

import (
	"reflect"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// The refusal texts as KAS's acp-server.js words them (RXu, PWo), and the run_command wrapper
// (WUo) that frames one as "Output:\n<reason>\n\nExit Code: -1".
const (
	unparseableRefusal = "Tool call denied: Kiro could not parse this shell command to check it against the user's permissions. It was not run.\n" +
		"Unterminated heredoc\nThe same command, rewritten without the heredoc, may pass."
	permissionsInvalidRefusal = "Tool call denied: the user's permission file(s) failed to parse.\nYou cannot fix this yourself."
	cedarRefusal              = "Tool call denied: Kiro's permission engine (cedar-wasm) could not be loaded, so this call could not be checked against the user's permission rules.\nWhile it is broken, every tool call it would check is denied."
	notLoadedRefusal          = "Blocked by the permission policy (no policy loaded, so every call is denied); the user did not reject it."
)

func runCommandOutput(reason string) string { return "Output:\n" + reason + "\n\nExit Code: -1\n" }

func TestEngineRefusal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		want     *marotte.ToolDenial
		name     string
		status   marotte.ToolStatus
		terminal string
		output   string
	}{
		{
			name: "an unparseable command in the run_command wrapper", status: marotte.ToolFailed,
			output: runCommandOutput(unparseableRefusal),
			want:   &marotte.ToolDenial{Capability: "shell", Basis: marotte.DenialUnparseable},
		},
		{name: "the same text from a command that ran", status: marotte.ToolFailed, terminal: "term-1", output: runCommandOutput(unparseableRefusal)},
		{name: "the same text on a completed call", status: marotte.ToolCompleted, output: runCommandOutput(unparseableRefusal)},
		{
			name: "a permissions file that failed to parse", status: marotte.ToolFailed, output: permissionsInvalidRefusal,
			want: &marotte.ToolDenial{Basis: marotte.DenialPermissionsInvalid},
		},
		{
			name: "the permission engine failing to load", status: marotte.ToolFailed, output: cedarRefusal,
			want: &marotte.ToolDenial{Basis: marotte.DenialCedarUnavailable},
		},
		{
			name: "no policy loaded", status: marotte.ToolFailed, output: notLoadedRefusal,
			want: &marotte.ToolDenial{Basis: marotte.DenialNotLoaded},
		},
		{
			name: "a rule's block, which policyDenial carries", status: marotte.ToolFailed,
			output: `Blocked by a permission rule (deny shell matching "rm *"); the user did not reject it.`,
		},
		{name: "an ordinary failure", status: marotte.ToolFailed, output: "Output:\nboom\n\nExit Code: 1"},
		{name: "the lead past the first line", status: marotte.ToolFailed, output: "boom\n" + unparseableRefusal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := engineRefusal(c.status, c.terminal, c.output); !reflect.DeepEqual(got, c.want) {
				t.Errorf("engineRefusal(%q, %q, %q) = %+v, want %+v", c.status, c.terminal, c.output, got, c.want)
			}
		})
	}
}
