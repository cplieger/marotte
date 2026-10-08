package bridge

import (
	"context"
	"os"
	"path/filepath"
	"slices"
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

func TestStartLive_ReportsTheListSentAndTheTimeoutReappliedAfterInitialize(t *testing.T) {
	dir := t.TempDir()
	b := New(configOptionFake(t, filepath.Join(dir, "requests.log")), dir)
	t.Cleanup(b.Stop)
	reads := 0
	opts := &marotte.StartOpts{
		Lifetime:    t.Context(),
		IgnoreFiles: func(context.Context) []string { return []string{".kiroignore", ".gitignore"} },
		TerminalTimeout: func(context.Context) int {
			reads++
			return []int{1000, 300000}[min(reads-1, 1)]
		},
	}
	if err := b.Start(t.Context(), opts); err != nil {
		t.Fatalf("Start: %v", err)
	}

	files, ms := b.StartLive()
	if !slices.Equal(files, []string{".kiroignore", ".gitignore"}) || ms != 300000 {
		t.Errorf("StartLive() = %v, %d, want [.kiroignore .gitignore], 300000", files, ms)
	}
}

// An unstarted bridge refuses every write (errBridgeNotStarted), which is a failed send.
func TestStartSends_AFailedSendLeavesWhatKASAlreadyHeld(t *testing.T) {
	b := New("/nonexistent/kiro-cli", t.TempDir())
	b.features.TerminalCommandTimeoutMs = 1000

	if got := b.applyIgnoreFiles(t.Context(), func(context.Context) []string { return []string{".gitignore"} }); got != nil {
		t.Errorf("applyIgnoreFiles over a failed send = %v, want nil: nothing reached KAS", got)
	}
	if got := b.reapplyTerminalTimeout(t.Context(), func(context.Context) int { return 300000 }); got != 1000 {
		t.Errorf("reapplyTerminalTimeout over a failed send = %d, want 1000, the value initialize carried", got)
	}
}
