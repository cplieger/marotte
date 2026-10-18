package composition

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/mcp"
)

func TestKASHealLoop_HealsADriftedFileOnItsTick(t *testing.T) {
	captureClaimLogs(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	kasFile := filepath.Join(home, ".kiro", "settings", "mcp.json")
	configDir := t.TempDir()

	synctest.Test(t, func(t *testing.T) {
		store, err := mcp.New(t.Context(), configDir, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Create(t.Context(), &mcp.Server{
			Transport: mcp.TransportStdio, Name: "agent-mail", Command: "bash", Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
		var healed atomic.Int32
		healer := &kasHealer{
			heal:      store.HealKASConfig,
			afterHeal: func(context.Context) { healed.Add(1) },
			every:     kasHealInterval,
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			healer.run(ctx)
		}()
		defer func() {
			cancel()
			<-done
		}()

		if err := os.WriteFile(kasFile, []byte(`{"mcpServers": {}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(kasHealInterval + time.Second)
		synctest.Wait()
		if !kasFileHas(t, kasFile, "agent-mail") {
			t.Error("agent-mail still missing from KAS's file after one heal interval")
		}
		if got := healed.Load(); got != 1 {
			t.Errorf("heals after the drift = %d, want 1", got)
		}

		time.Sleep(kasHealInterval)
		synctest.Wait()
		if got := healed.Load(); got != 1 {
			t.Errorf("heals after an interval with no drift = %d, want still 1", got)
		}
	})
}

func kasFileHas(t *testing.T, path, server string) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	_, ok := doc.Servers[server]
	return ok
}
