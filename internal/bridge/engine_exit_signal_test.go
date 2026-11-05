package bridge

import (
	"syscall"
	"testing"
	"time"
)

// waitExited waits until b's process has exited, unreaped, as one another process signalled would have.
func waitExited(t *testing.T, b *Engine) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !exitedUnreaped(b.cmd.Process.Pid) {
		if time.Now().After(deadline) {
			t.Fatal("Setup: the fixture never exited")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The signal a kiro-cli process died of before marotte signalled it, whether it died of it or caught it and exited
// 128+N as kiro-cli does for SIGTERM.
func TestExit_NamesTheSignalAnotherProcessEndedKiroCLIWith(t *testing.T) {
	for name, tc := range map[string]struct {
		script string
		want   syscall.Signal
	}{
		"a caught SIGTERM, exit 143": {script: "exit 143", want: syscall.SIGTERM},
		"a caught SIGINT, exit 130":  {script: "exit 130", want: syscall.SIGINT},
		"killed by SIGTERM":          {script: "kill -TERM $$", want: syscall.SIGTERM},
		"killed by SIGHUP":           {script: "kill -HUP $$", want: syscall.SIGHUP},
		"killed by SIGKILL":          {script: "kill -KILL $$", want: syscall.SIGKILL},
		"killed by SIGSEGV":          {script: "kill -SEGV $$", want: syscall.SIGSEGV},
		"a plain non-zero exit":      {script: "exit 1"},
		"a clean exit":               {script: "exit 0"},
	} {
		t.Run(name, func(t *testing.T) {
			b := startTeardownFixture(t, tc.script)
			waitExited(t, b)
			b.Stop()
			if got := b.Exit().SidecarSignal; got != tc.want {
				t.Errorf("SidecarSignal = %v (%d), want %v (%d)", got, int(got), tc.want, int(tc.want))
			}
		})
	}
}

// marotte's own SIGTERM at a stop is not another process's.
func TestExit_MarottesOwnStopSignalNamesNone(t *testing.T) {
	setTeardownGrace(t, 5*time.Second)
	b := startTeardownFixture(t, `trap 'exit 143' TERM; while :; do sleep 0.05; done`)
	b.Stop()
	if got := b.Exit().SidecarSignal; got != 0 {
		t.Errorf("SidecarSignal after marotte's own stop = %v, want none", got)
	}
}
