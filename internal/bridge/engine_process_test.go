package bridge

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/cplieger/marotte/internal/testsupport"
	"golang.org/x/sys/unix"
)

// The engine carries KIRO_NO_AUTO_UPDATE=1 from kirorelease.SpawnEnv whatever HOME and parent environment it starts under.
func TestProcessEnv_TurnsOffKiroCLISelfUpdate(t *testing.T) {
	t.Run("an env -i parent and no overlay, as the unpinned runtime starts it", func(t *testing.T) {
		testsupport.EmptyEnv(t)
		e := NewEngine("kiro-cli", t.TempDir())
		e.lifecycleCtx = t.Context()
		if v, ok := envValue(e.processEnv(), "KIRO_NO_AUTO_UPDATE"); v != "1" {
			t.Errorf("KIRO_NO_AUTO_UPDATE = %q (set %v), want 1", v, ok)
		}
	})
	t.Run("an inherited and an overlaid opt-out", func(t *testing.T) {
		t.Setenv("KIRO_NO_AUTO_UPDATE", "0")
		e := NewEngine("kiro-cli", t.TempDir(), WithEnv([]string{"KIRO_NO_AUTO_UPDATE=0"}))
		e.lifecycleCtx = t.Context()
		if v, ok := envValue(e.processEnv(), "KIRO_NO_AUTO_UPDATE"); v != "1" {
			t.Errorf("KIRO_NO_AUTO_UPDATE = %q (set %v), want 1", v, ok)
		}
	})
}

// A hook or stdio MCP server the engine spawns reads the engine's session and group, never the
// server's, so `kill -- -<id>` inside it cannot reach marotte.
func TestStartProcess_EngineChildrenReadTheEnginesOwnSession(t *testing.T) {
	dir := t.TempDir()
	ids := filepath.Join(dir, "ids")
	script := "#!/bin/sh\nsh -c 'cut -d\" \" -f5,6 /proc/$$/stat > \"" + ids + ".tmp\"; mv \"" + ids + ".tmp\" \"" + ids + "\"'\ncat >/dev/null\n"
	scriptPath := filepath.Join(dir, "fake-kiro-cli")
	syscall.ForkLock.RLock()
	err := os.WriteFile(scriptPath, []byte(script), 0o755) // #nosec G306 -- the fake must be executable
	syscall.ForkLock.RUnlock()
	if err != nil {
		t.Fatalf("Setup: write fake kiro-cli: %v", err)
	}
	b := NewEngine(scriptPath, dir)
	// Not t.Context(): CommandContext binds the subprocess to lifecycleCtx, which must outlive t.Cleanup(b.Stop).
	b.lifecycleCtx = context.Background()
	t.Cleanup(b.Stop)
	if err := b.startProcess(); err != nil {
		t.Fatalf("Setup: startProcess: %v", err)
	}
	waitForFile(t, ids)
	raw, err := os.ReadFile(ids)
	if err != nil {
		t.Fatalf("read ids: %v", err)
	}
	f := strings.Fields(string(raw))
	if len(f) != 2 {
		t.Fatalf("the engine's child recorded %q, want its group and session", raw)
	}
	pgrp, pErr := strconv.Atoi(f[0])
	sid, sErr := strconv.Atoi(f[1])
	if pErr != nil || sErr != nil {
		t.Fatalf("parse ids %q: %v %v", raw, pErr, sErr)
	}
	self, err := unix.Getsid(0)
	if err != nil {
		t.Fatalf("Setup: getsid: %v", err)
	}
	engine := b.cmd.Process.Pid
	if sid != engine || sid == self {
		t.Errorf("the engine's child is in session %d, want the engine's own %d (the server's is %d)", sid, engine, self)
	}
	if pgrp != engine || pgrp == syscall.Getpgrp() {
		t.Errorf("the engine's child is in group %d, want the engine's own %d (the server's is %d)", pgrp, engine, syscall.Getpgrp())
	}
}
