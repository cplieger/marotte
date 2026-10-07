package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// setKiroSettings writes a kiro-cli settings file under a throwaway HOME, returned for extension.
func setKiroSettings(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".kiro", "settings")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if body != "" {
		// cli.json, where `kiro-cli settings` persists (kiroSettingsPath).
		if err := os.WriteFile(filepath.Join(dir, "cli.json"), []byte(body), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	t.Setenv("HOME", home)
	// Some CI shells let USERPROFILE shadow HOME.
	t.Setenv("USERPROFILE", home)
	return home
}

func TestIsHookStatusEnabled_fileMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	h, _, _ := newTestHub()
	if !h.hookStatus.IsHookStatusEnabled() {
		t.Error("missing cli.json: want default true")
	}
}

func TestIsHookStatusEnabled_cases(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"invalid_outer_json", "{not json", true},
		{"key_absent", `{}`, true},
		{"key_not_bool", `{"hooks.showStatus":"yes"}`, true},
		{"key_false", `{"hooks.showStatus":false}`, false},
		{"key_true", `{"hooks.showStatus":true}`, true},
		// encoding/json leaves *bool untouched for `null`, so false; kiro-cli never writes null.
		{"key_null", `{"hooks.showStatus":null}`, false},
		{"other_keys_only", `{"telemetry.enabled":true}`, true},
		// The underscore key in marotte's own config must not decide this.
		{"snake_case_key_ignored", `{"hooks_show_status":false}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setKiroSettings(t, tc.content)
			h, _, _ := newTestHub()
			if got := h.hookStatus.IsHookStatusEnabled(); got != tc.want {
				t.Errorf("content %q: got %v, want %v", tc.content, got, tc.want)
			}
		})
	}
}

func TestTranslateV3_HookUpdateFollowsKiroCliHookStatus(t *testing.T) {
	cases := map[string]struct {
		content   string
		wantCards int
	}{
		"switch_off": {content: `{"hooks.showStatus":false}`, wantCards: 0},
		"switch_on":  {content: `{"hooks.showStatus":true}`, wantCards: 1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			setKiroSettings(t, tc.content)
			h, cs, _ := newTestHub()
			_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
			before := h.bus.fanout.Position().Head

			h.translateACPEvent("c1", newSessionInfoMsg(map[string]any{
				"kind": "hook_update",
				"hook": map[string]any{
					"hookId": "h1", "operationId": "op-1", "name": "probe-save",
					"status": "completed", "actionType": "runCommand",
				},
			}))

			cards := 0
			for _, p := range payloadsOfType[marotte.EntryAppendedPayload](t, bufferedSince(h, before), marotte.EventEntryAppended) {
				if p.Entry.Kind == marotte.EntryKindToolCall {
					cards++
				}
			}
			if cards != tc.wantCards {
				t.Errorf("cli.json %s: Hook fired cards broadcast = %d, want %d", tc.content, cards, tc.wantCards)
			}
		})
	}
}
