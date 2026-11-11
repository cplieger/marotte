package composition

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/cplieger/go/slogx/capture"
	"github.com/cplieger/marotte/internal/steering"
)

func TestRegenerateSteering(t *testing.T) {
	writeFailed := fmt.Errorf("steering: write environment.md: %w", syscall.ENOTDIR)
	cases := []struct {
		name    string
		cancel  bool // the ctx is cancelled before generate returns
		err     error
		wantLog bool
	}{
		{name: "a cancelled ctx is a quiet stop", cancel: true, err: fmt.Errorf("stopped: %w", context.Canceled)},
		{name: "an expired ctx is a quiet stop", err: fmt.Errorf("stopped: %w", context.DeadlineExceeded)},
		{name: "a write failure with a live ctx logs", err: writeFailed, wantLog: true},
		{name: "a write failure racing a cancel logs", cancel: true, err: writeFailed, wantLog: true},
		{name: "a success logs nothing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := &lockedBuffer{}
			capture.SwapDefault(t, slog.New(slog.NewTextHandler(logs, nil)))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			regenerateSteering(ctx, func(context.Context) error {
				if tc.cancel {
					cancel()
				}
				return tc.err
			})

			logged := strings.Contains(logs.String(), `level=ERROR msg="steering: environment.md not written"`)
			if logged != tc.wantLog {
				t.Errorf("logged an ERROR: %v, want %v:\n%s", logged, tc.wantLog, logs.String())
			}
		})
	}
}

func TestRegenerateSteering_ACancelBeforeTheRenderLogsNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	logs := &lockedBuffer{}
	capture.SwapDefault(t, slog.New(slog.NewTextHandler(logs, nil)))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	regenerateSteering(ctx, steering.New(steering.Config{WorkDir: t.TempDir(), ConfigDir: t.TempDir()}).Generate)

	if out := logs.String(); out != "" {
		t.Errorf("a regeneration cancelled before its render logged:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".kiro", "steering", "environment.md")); !os.IsNotExist(err) {
		t.Errorf("a cancelled regeneration wrote environment.md (stat error %v)", err)
	}
}
