package translate

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestHandleSessionInfoUpdate_AFocusTitleNeverRenamesABot pins that a Bot keeps the user's name: its role prompt
// says who it is by that name, so an agent-chosen title would rewrite it.
func TestHandleSessionInfoUpdate_AFocusTitleNeverRenamesABot(t *testing.T) {
	for _, tc := range []struct {
		name string
		bot  *marotte.Bot
	}{
		{"a draft", &marotte.Bot{}},
		{"a started Bot", &marotte.Bot{Goal: "Watch CI.", StartedAt: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, _, store := depsWithStore(t, "c1")
			if _, err := store.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
				c.Name = "CI watcher"
				c.NameSetByUser = true
				c.CurrentModeID = marotte.BotRoleID
				c.Bot = tc.bot
				return true
			}); err != nil {
				t.Fatal(err)
			}
			tr := New(rolesOf(deps))

			tr.HandleSessionInfoUpdate(t.Context(), "c1", focusFrame(t, map[string]any{
				"title": "Investigating the failing pipeline", "status": "in_progress",
			}), FrameAttribution{})

			if c, _ := store.Get(t.Context(), "c1"); c.Name != "CI watcher" {
				t.Errorf("name = %q, want the user's \"CI watcher\"", c.Name)
			}
		})
	}
}
