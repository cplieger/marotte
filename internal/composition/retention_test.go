package composition

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cplieger/go/slogx/capture"
	"github.com/cplieger/marotte/internal/agent"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// TestChatRetention_ThreeLegs walks one config.json through the three states the retention read
// must tell apart: a stored value, an absent key, and an unreadable file. The two last are read
// once the config watch settled them, as a removal or a broken file that lasts is.
func TestChatRetention_ThreeLegs(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	path := filepath.Join(dir, settings.Filename)
	settle := func() {
		t.Helper()
		for range 2 {
			if _, _, err := settings.Poll(ctx, dir, func(context.Context, marotte.ServerEvent) {}); err != nil {
				t.Fatalf("settings.Poll: %v", err)
			}
		}
	}

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
	settle()
	if got := chatRetention(ctx, dir); got != 0 {
		t.Errorf("chatRetention with an unparseable config.json = %v, want 0; applying the default (%v) here purges the chats the user asked to keep",
			got, wantDefault)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove %s: %v", path, err)
	}
	settle()
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
// parentless run at once and leaves a living chat's runs, where for chats it purges
// nothing; N days ages out every finished run, a chat's included.
func TestRunRetention_MapsEverySetting(t *testing.T) {
	cases := []struct {
		desc, config string
		want         agent.RunRetention
	}{
		{desc: "keep forever", config: `{"chat_retention_days":-1}`, want: agent.RunRetention{}},
		{desc: "off", config: `{"chat_retention_days":0}`, want: agent.RunRetention{Enabled: true}},
		{
			desc: "days", config: `{"chat_retention_days":14}`,
			want: agent.RunRetention{Window: 14 * 24 * time.Hour, Enabled: true, ChatRuns: true},
		},
		{desc: "unreadable", config: `{`, want: agent.RunRetention{}},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, filepath.Join(dir, settings.Filename), tc.config)
			if got := runRetention(t.Context(), dir); got != tc.want {
				t.Errorf("runRetention(%s) = %+v, want %+v", tc.config, got, tc.want)
			}
		})
	}
	t.Run("absent", func(t *testing.T) {
		want := agent.RunRetention{
			Window: time.Duration(settings.DefaultChatRetentionDays) * 24 * time.Hour, Enabled: true, ChatRuns: true,
		}
		if got := runRetention(t.Context(), t.TempDir()); got != want {
			t.Errorf("runRetention(no config.json) = %+v, want %+v", got, want)
		}
	})
}

// TestChatRetention_ACtxStopLogsNothing pins that a purge pass whose ctx ended logs no "unreadable"
// ERROR: the document was never read. The live case proves the ERROR still fires over a broken file.
// Serial: slog's default is process-global.
func TestChatRetention_ACtxStopLogsNothing(t *testing.T) {
	const msg = "chat retention: config.json cannot be read; purged nothing this pass rather than applying the default window"
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
