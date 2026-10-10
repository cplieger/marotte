package bridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func startWithTimeouts(t *testing.T, answers ...int) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.log")
	b := New(configOptionFake(t, logPath), dir)
	t.Cleanup(b.Stop)

	var mu sync.Mutex
	calls := 0
	opts := &marotte.StartOpts{
		Lifetime: t.Context(),
		TerminalTimeout: func(context.Context) int {
			mu.Lock()
			defer mu.Unlock()
			ms := answers[min(calls, len(answers)-1)]
			calls++
			return ms
		},
	}
	if err := b.Start(t.Context(), opts); err != nil {
		t.Fatalf("Start: %v", err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	return string(raw)
}

// A save after StartOpts is built but before initialize reaches the spawn through the initialize read.
func TestStart_InitializeCarriesTheTimeoutReadAtSendTime(t *testing.T) {
	got := startWithTimeouts(t, 300000)

	if !strings.Contains(got, `"commandTimeoutMs":300000`) {
		t.Errorf("initialize does not carry the resolved timeout\nlog:\n%s", got)
	}
	if strings.Contains(got, marotte.MethodTerminalSettingsChanged) {
		t.Errorf("an unchanged timeout sent %s\nlog:\n%s", marotte.MethodTerminalSettingsChanged, got)
	}
}

// A save during initialize, when the live push is refused, is sent once initialize returns.
func TestStart_ATimeoutSavedMidSpawnIsSentAfterInitialize(t *testing.T) {
	got := startWithTimeouts(t, 0, 300000)

	want := `"method":"` + marotte.MethodTerminalSettingsChanged + `"`
	if !strings.Contains(got, want) {
		t.Fatalf("request log does not contain %s\nlog:\n%s", want, got)
	}
	if !strings.Contains(got, `"terminal":{"commandTimeoutMs":300000,"enabled":true}`) {
		t.Errorf("settings_changed does not carry the saved timeout\nlog:\n%s", got)
	}
}
