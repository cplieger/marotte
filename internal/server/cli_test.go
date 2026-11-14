package server

import (
	"testing"

	"github.com/cplieger/marotte/internal/testsupport"
)

// Every settings, diagnostics and seed call runs the binary, so each carries kirorelease.NoAutoUpdateVar.
func TestExecCLIRunner_TurnsOffKiroCLISelfUpdate(t *testing.T) {
	cases := map[string]func() []string{
		"no overlay, as the unpinned runtime passes": nil,
		"an overlay that opts back in":               func() []string { return []string{"KIRO_NO_AUTO_UPDATE=0"} },
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			testsupport.EmptyEnv(t)
			cli, seen := testsupport.EnvRecorder(t, "KIRO_NO_AUTO_UPDATE")
			r := &execCLIRunner{cliPath: func() string { return cli }, env: env}
			if out, err := r.Run(t.Context(), "settings", "list"); err != nil {
				t.Fatalf("Run: %v: %s", err, out)
			}
			if got := seen(); got != "1" {
				t.Errorf("Run: kiro-cli saw KIRO_NO_AUTO_UPDATE %q, want 1", got)
			}
			if _, _, err := r.RunStdoutCapped(t.Context(), 1024, "--version"); err != nil {
				t.Fatalf("RunStdoutCapped: %v", err)
			}
			if got := seen(); got != "1" {
				t.Errorf("RunStdoutCapped: kiro-cli saw KIRO_NO_AUTO_UPDATE %q, want 1", got)
			}
		})
	}
}
