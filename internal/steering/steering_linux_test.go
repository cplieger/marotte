package steering

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// workDirOpens watches workDir for opens; a render's first read is os.ReadDir(workDir), and the
// kernel reports it whatever the ctx says, unlike a settings read, which stays silent on a
// cancelled ctx.
func workDirOpens(t *testing.T, workDir string) func() bool {
	t.Helper()
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		t.Fatalf("inotify_init1: %v", err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	if _, err := unix.InotifyAddWatch(fd, workDir, unix.IN_OPEN); err != nil {
		t.Fatalf("inotify_add_watch: %v", err)
	}
	return func() bool {
		buf := make([]byte, 4096)
		n, err := unix.Read(fd, buf)
		if err != nil && !errors.Is(err, unix.EAGAIN) {
			t.Fatalf("read inotify: %v", err)
		}
		return n > 0
	}
}

func TestGenerate_CancelledDuringItsSnapshotsRendersNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workDir := t.TempDir()
	opened := workDirOpens(t, workDir)
	g := New(workDir, t.TempDir(), "")
	ctx, cancel := context.WithCancel(t.Context())
	g.SetToolsSnapshot(func() (ToolsSnapshot, bool) { cancel(); return ToolsSnapshot{}, true })

	err := g.Generate(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Generate cancelled during its snapshots returned %v, want its ctx's error", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".kiro", "steering", "environment.md")); !os.IsNotExist(err) {
		t.Errorf("Generate wrote environment.md after its ctx was cancelled (stat error %v)", err)
	}
	if opened() {
		t.Errorf("Generate scanned the workspace after its ctx was cancelled")
	}
	g.SetToolsSnapshot(func() (ToolsSnapshot, bool) { return ToolsSnapshot{}, true })
	if err := g.Generate(t.Context()); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !opened() {
		t.Errorf("a live Generate raised no open event on the workspace, so the check above cannot fail")
	}
}
