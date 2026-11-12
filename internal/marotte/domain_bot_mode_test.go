package marotte

import "testing"

// TestChat_AdoptModeHoldsABotOnItsRole pins design §7.1 "Holding the mode": a Bot's record names only the role, a
// regular chat's never does, and the answer is whether the record changed.
func TestChat_AdoptModeHoldsABotOnItsRole(t *testing.T) {
	cases := []struct {
		name      string
		bot       *Bot
		from, id  string
		wantMode  string
		wantWrote bool
	}{
		{name: "a Bot reported in another mode keeps its role", bot: &Bot{StartedAt: 1}, from: BotRoleID, id: DefaultModeID, wantMode: BotRoleID},
		{name: "a Bot reported on its role writes nothing", bot: &Bot{StartedAt: 1}, from: BotRoleID, id: BotRoleID, wantMode: BotRoleID},
		{name: "a Bot whose record left its role is put back", bot: &Bot{StartedAt: 1}, from: "kiro_spec", id: DefaultModeID, wantMode: BotRoleID, wantWrote: true},
		{name: "a draft Bot keeps its role too", bot: &Bot{}, from: BotRoleID, id: "kiro_spec", wantMode: BotRoleID},
		{name: "a regular chat never takes the role", from: DefaultModeID, id: BotRoleID, wantMode: DefaultModeID},
		{name: "a regular chat takes any other mode", from: DefaultModeID, id: "kiro_spec", wantMode: "kiro_spec", wantWrote: true},
		{name: "a regular chat reported in its mode writes nothing", from: "kiro_spec", id: "kiro_spec", wantMode: "kiro_spec"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Chat{CurrentModeID: tc.from, Bot: tc.bot}

			wrote := c.AdoptMode(tc.id)

			if c.CurrentModeID != tc.wantMode {
				t.Errorf("CurrentModeID = %q, want %q", c.CurrentModeID, tc.wantMode)
			}
			if wrote != tc.wantWrote {
				t.Errorf("AdoptMode(%q) = %v, want %v", tc.id, wrote, tc.wantWrote)
			}
		})
	}
}
