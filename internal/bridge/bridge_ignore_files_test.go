package bridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// The agent-ignore list reaches KAS at the CONNECTION door, and this drives the
// real call rather than simulating it: the agent-side ignore tests all run
// against fake bridges, so none of them reaches this path.
//
// It asserts on the RAW request bytes because the defect class is a
// fire-and-forget notification that never leaves the process, costing every new
// connection its whole ignore enforcement. The notification precedes
// session/new, so a returned Start means the fake has already logged it.
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

// NIL or EMPTY sends NOTHING, and that is the fail mode rather than an omission:
// `{files: []}` CLEARS the list in KAS, so a bridge whose settings could not be
// read must leave KAS enforcing whatever it was last told instead of disabling
// enforcement it cannot re-derive.
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
