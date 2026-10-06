package composition

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/settings"
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
