package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/web-terminal-engine/v6/terminal"
)

func terminalReference(t *testing.T, ws command.Workspace, token string) string {
	t.Helper()
	for _, b := range command.BuildPromptBlocks(t.Context(), token, nil, 0, ws, nil) {
		res, ok := b["resource"].(map[string]any)
		if ok && res["uri"] == token {
			text, _ := res["text"].(string)
			return text
		}
	}
	t.Fatalf("no resource block for %s", token)
	return ""
}

// The engine's reader writes the screen while a reference resolves (-race on a real PTY).
func TestReadShell_ResolvesWhileThePTYWrites(t *testing.T) {
	dir := t.TempDir()
	sm := &ShellManager{workDir: dir}
	sm.handler = terminal.NewHandler(
		[]string{"sh", "-c", `i=0; while [ $i -lt 3000 ]; do echo "line $i"; i=$((i+1)); if [ $((i % 100)) -eq 0 ]; then sleep 0.03; fi; done; sleep 2`},
		terminal.WithWorkDir(dir),
	)
	t.Cleanup(func() { retireHandler(context.Background(), sm.handler, "test") })
	ws := command.Workspace{Terminal: sm, Dir: dir}

	if got := terminalReference(t, ws, "#[[terminal:]]"); !strings.Contains(got, "(the shell has not started yet)") {
		t.Fatalf("terminal reference before start = %q, want the not-started reason", got)
	}
	if err := sm.handler.StartEager(); err != nil {
		t.Fatalf("StartEager: %v", err)
	}
	var got string
	midStream := 0
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		got = terminalReference(t, ws, "#[[terminal:50]]")
		if strings.HasSuffix(got, "line 2999") {
			if !strings.HasPrefix(got, "Current terminal contents:\n") {
				t.Fatalf("terminal reference = %q, want the IDE header first", got)
			}
			if midStream == 0 {
				t.Fatal("no read landed while the shell was still writing, so nothing raced")
			}
			return
		}
		if strings.Contains(got, "line ") {
			midStream++
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("terminal reference never reached the last line within 15s; last read %q", got[max(0, len(got)-200):])
}

// A restart swaps the handler under the mutex, so a read skipping it races (-race).
func TestReadShell_RacesARestartSafely(t *testing.T) {
	sm := &ShellManager{workDir: t.TempDir()}
	sm.handler = sm.newHandler()
	t.Cleanup(func() { retireHandler(context.Background(), sm.handler, "test") })
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 20 {
			sm.restart()
		}
	}()
	for {
		select {
		case <-done:
			return
		default:
		}
		if _, ok := sm.ReadShell(10); ok {
			t.Fatal("ReadShell on an unstarted shell reported a reading")
		}
	}
}

// A spent shell's final output stays readable until the next socket swaps it.
// The child exits only once its output is on the screen: engine v6.1.0 can cancel the PTY reader
// at exit before it drains, which would drop the output this test reads.
func TestReadShell_KeepsTheFinalOutputOfAnExitedShell(t *testing.T) {
	dir := t.TempDir()
	sm := &ShellManager{workDir: dir}
	sm.handler = terminal.NewHandler(
		[]string{"sh", "-c", "echo goodbye; while [ ! -e release ]; do sleep 0.01; done"},
		terminal.WithWorkDir(dir),
		terminal.WithOnProcessExit(func(error) {
			sm.mu.Lock()
			sm.spent = true
			sm.mu.Unlock()
		}),
	)
	t.Cleanup(func() { retireHandler(context.Background(), sm.handler, "test") })
	if err := sm.handler.StartEager(); err != nil {
		t.Fatalf("StartEager: %v", err)
	}
	ws := command.Workspace{Terminal: sm, Dir: dir}
	waitFor(t, func() bool { return strings.Contains(terminalReference(t, ws, "#[[terminal:]]"), "goodbye") })
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
		t.Fatalf("release the shell: %v", err)
	}
	// Exited() turns true after the exit callback returns, so spent is latched by then.
	waitFor(t, sm.handler.Exited)
	got := terminalReference(t, ws, "#[[terminal:]]")
	if !strings.Contains(got, "goodbye") || !strings.HasSuffix(got, "[The shell has exited.]") {
		t.Fatalf("terminal reference after exit = %q, want the final output and the exit note", got)
	}
}
