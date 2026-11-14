package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func setKiroSettings(t *testing.T, body string) {
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
}

// Show hook status is applied when the transcript draws, so every hook is recorded whatever it says.
func TestTranslateV3_HookUpdateRecordedInBothSwitchStates(t *testing.T) {
	cases := map[string]struct {
		content   string
		wantCards int
	}{
		"switch_off": {content: `{"hooks.showStatus":false}`, wantCards: 1},
		"switch_on":  {content: `{"hooks.showStatus":true}`, wantCards: 1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			setKiroSettings(t, tc.content)
			h, cs, _ := newTestHub()
			_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
			before := h.bus.fanout.Position().Head

			h.translateACPEvent("c1", h.originOf("c1"), newSessionInfoMsg(map[string]any{
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
