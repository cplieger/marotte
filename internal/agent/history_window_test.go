package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/marotte/internal/tabs"
)

// waitShown polls the tab store until the History list it serves is want, failing after a few seconds with the
// list it last saw.
func waitShown(t *testing.T, st *tabs.Store, want []string, after string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, recent, _ := st.Snapshot()
		got := make([]string, 0, len(recent))
		for _, e := range recent {
			got = append(got, e.Ref)
		}
		if slices.Equal(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the History list %s = %v, want %v", after, got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSettingsWritten_ReappliesTheHistoryWindow(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	doc, err := json.Marshal(map[string]any{
		"tabs": []any{},
		"recent": []marotte.RecentClose{
			{Ref: "c-10h", Name: "Ten", ClosedAt: now.Add(-10 * time.Hour).UnixMilli()},
			{Ref: "c-30h", Name: "Thirty", ClosedAt: now.Add(-30 * time.Hour).UnixMilli()},
		},
		"version": 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tabs.json"), doc, 0o600); err != nil {
		t.Fatalf("Setup: write tabs.json: %v", err)
	}
	st, err := tabs.NewStore(dir, settings.DefaultHistoryWindow)
	if err != nil {
		t.Fatalf("Setup: tabs.NewStore(%q): %v", dir, err)
	}
	cs := newTestChatStore()
	for _, id := range []marotte.ChatID{"c-10h", "c-30h"} {
		if _, err := cs.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool { c.Name = string(id); return true }); err != nil {
			t.Fatalf("Setup: seed %s: %v", id, err)
		}
	}
	br := newFakeBridge()
	rt := newRuntimeOn(context.Background(), t.TempDir(), fakeEngineOf(func() engineSession { return br }, laneOf(br)), cs,
		WithTabs(st), WithConfigDir(dir))
	cs.wire(rt)
	t.Cleanup(func() { shutdownHub(t, rt) })
	awaitBoot(rt)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		rt.Membership().RunRecentExpiry(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	waitShown(t, st, []string{"c-10h"}, "under the default 24h")

	writeSetting(t, dir, settings.KeyHistoryWindowHours, 6)
	rt.SettingsWritten(t.Context(), []string{settings.KeyHistoryWindowHours})
	waitShown(t, st, []string{}, "after history_window_hours 6")

	writeSetting(t, dir, settings.KeyHistoryWindowHours, 72)
	rt.SettingsWritten(t.Context(), nil)
	waitShown(t, st, []string{"c-10h", "c-30h"}, "after history_window_hours 72 with unknown keys")

	writeSetting(t, dir, settings.KeyChatRetentionDays, 1)
	rt.SettingsWritten(t.Context(), []string{settings.KeyChatRetentionDays})
	waitShown(t, st, []string{"c-10h"}, "after chat_retention_days 1 caps 72h at a day")
}
