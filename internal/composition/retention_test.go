package composition

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/slogx/capture"
)

// TestChatRetention_ThreeLegs walks one config.json through the three states the retention read
// must tell apart: a stored value, an absent key, and an unreadable file.
func TestChatRetention_ThreeLegs(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	path := filepath.Join(dir, settings.Filename)

	if settings.DefaultChatRetentionDays <= 0 {
		t.Fatalf("DefaultChatRetentionDays = %d; a non-positive default makes leg 2 vacuous",
			settings.DefaultChatRetentionDays)
	}
	wantDefault := time.Duration(settings.DefaultChatRetentionDays) * 24 * time.Hour

	writeConfig(t, path, `{"chat_retention_days":-1}`)
	if got := chatRetention(ctx, dir); got != 0 {
		t.Fatalf("chatRetention with a stored -1 = %v, want 0 (never purge)", got)
	}

	writeConfig(t, path, `{`)
	if got := chatRetention(ctx, dir); got != 0 {
		t.Errorf("chatRetention with an unparseable config.json = %v, want 0; applying the default (%v) here purges the chats the user asked to keep",
			got, wantDefault)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove %s: %v", path, err)
	}
	if got := chatRetention(ctx, dir); got != wantDefault {
		t.Errorf("chatRetention with no config.json = %v, want %v (absence is not a failure)", got, wantDefault)
	}
}

// TestChatRetention_StoredWindow pins the ordinary path, so the refusal above
// cannot be satisfied by a function that never purges anything.
func TestChatRetention_StoredWindow(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, filepath.Join(dir, settings.Filename), `{"chat_retention_days":7}`)

	if got, want := chatRetention(t.Context(), dir), 7*24*time.Hour; got != want {
		t.Errorf("chatRetention with a stored 7 = %v, want %v", got, want)
	}
}

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// The run purge reads the same setting with a different zero: 0 purges a finished
// parentless run at once, where for chats it purges nothing.
func TestRunRetention_ReadsTheSharedSetting(t *testing.T) {
	cases := []struct {
		desc, config string
		window       time.Duration
		purge        bool
	}{
		{desc: "keep forever", config: `{"chat_retention_days":-1}`, purge: false},
		{desc: "off", config: `{"chat_retention_days":0}`, window: 0, purge: true},
		{desc: "days", config: `{"chat_retention_days":3}`, window: 3 * 24 * time.Hour, purge: true},
		{desc: "unreadable", config: `{`, purge: false},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, filepath.Join(dir, settings.Filename), tc.config)
			window, purge := runRetention(t.Context(), dir)
			if purge != tc.purge || window != tc.window {
				t.Errorf("runRetention(%s) = (%v, %v), want (%v, %v)", tc.config, window, purge, tc.window, tc.purge)
			}
		})
	}
	t.Run("absent", func(t *testing.T) {
		window, purge := runRetention(t.Context(), t.TempDir())
		want := time.Duration(settings.DefaultChatRetentionDays) * 24 * time.Hour
		if !purge || window != want {
			t.Errorf("runRetention(no config.json) = (%v, %v), want (%v, true)", window, purge, want)
		}
	})
}

// TestChatRetention_ACtxStopLogsNothing pins that a purge pass whose ctx ended logs no "unreadable"
// ERROR: the document was never read. The live case proves the ERROR still fires over a broken file.
// Serial: slog's default is process-global.
func TestChatRetention_ACtxStopLogsNothing(t *testing.T) {
	const msg = "chat retention: config.json is present but unreadable; purged nothing this pass rather than applying the default window"
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		wantLog int
	}{
		{"a cancelled ctx", cancelled, 0},
		{"a live ctx", t.Context(), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, filepath.Join(dir, settings.Filename), `{`)

			rec := capture.Default(t)
			if got := chatRetention(tc.ctx, dir); got != 0 {
				t.Errorf("chatRetention over an unparseable config.json with %s = %v, want 0", tc.name, got)
			}
			if n := rec.CountExact(msg); n != tc.wantLog {
				t.Errorf("chatRetention with %s logged the unreadable ERROR %d times, want %d; messages: %v",
					tc.name, n, tc.wantLog, rec.Messages())
			}
		})
	}
}
