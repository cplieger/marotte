package bridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// The agent-ignore list reaches KAS at the connection door through the real call, asserted on raw request bytes: a
// notification that never leaves the process costs a connection all ignore enforcement. It precedes session/new,
// so a returned Start means it was logged.
func TestStart_SendsTheAgentIgnoreFileList(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.log")
	scriptPath := configOptionFake(t, logPath)

	b := New(scriptPath, dir)
	t.Cleanup(b.Stop)
	opts := &marotte.StartOpts{
		Lifetime: t.Context(),
		IgnoreFiles: func(context.Context) []string {
			return []string{".kiroignore", ".dockerignore"}
		},
	}
	if err := b.Start(t.Context(), opts); err != nil {
		t.Fatalf("Start: %v", err)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	got := string(raw)

	for _, want := range []string{
		`"method":"` + marotte.MethodPolicyIgnoreFilesChanged + `"`,
		`"` + marotte.ParamIgnoreFiles + `":[".kiroignore",".dockerignore"]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("request log does not contain %s\nlog:\n%s", want, got)
		}
	}
}

// Nil or empty sends nothing: `{files: []}` clears the list in KAS, so an unreadable setting keeps the last list.
func TestStart_SendsNoIgnoreFilesFrameWithoutAList(t *testing.T) {
	cases := []struct {
		name    string
		resolve func(context.Context) []string
	}{
		{"nil resolver", nil},
		{"empty list", func(context.Context) []string { return nil }},
		{"empty non-nil list", func(context.Context) []string { return []string{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "requests.log")
			scriptPath := configOptionFake(t, logPath)

			b := New(scriptPath, dir)
			t.Cleanup(b.Stop)
			opts := &marotte.StartOpts{Lifetime: t.Context(), IgnoreFiles: tc.resolve}
			if err := b.Start(t.Context(), opts); err != nil {
				t.Fatalf("Start: %v", err)
			}

			raw, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("read request log: %v", err)
			}
			if strings.Contains(string(raw), marotte.MethodPolicyIgnoreFilesChanged) {
				t.Errorf("a %s sent %s, which CLEARS the list in KAS\nlog:\n%s",
					tc.name, marotte.MethodPolicyIgnoreFilesChanged, raw)
			}
		})
	}
}
