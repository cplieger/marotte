package agent

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestReconcileUserName_ABotKeepsItsNameOverEveryKASTitle pins that a door never takes KAS's stored title for a Bot,
// latched by the user or not: the record's name is re-asserted on KAS instead.
func TestReconcileUserName_ABotKeepsItsNameOverEveryKASTitle(t *testing.T) {
	for _, bot := range []struct {
		name string
		b    *marotte.Bot
	}{{"a draft", &marotte.Bot{}}, {"a started Bot", &marotte.Bot{Goal: "Watch CI.", StartedAt: 1}}} {
		for _, kas := range []struct {
			name      string
			setByUser bool
		}{{"an agent title", false}, {"a title renamed elsewhere", true}} {
			t.Run(bot.name+" over "+kas.name, func(t *testing.T) {
				c := &marotte.Chat{Name: "CI watcher", NameSetByUser: true, CurrentModeID: marotte.BotRoleID, Bot: bot.b}

				renameTo := reconcileUserName(c, "Investigating the failing pipeline", kas.setByUser)

				if c.Name != "CI watcher" || !c.NameSetByUser {
					t.Errorf("name = %q by user %v, want the Bot's own", c.Name, c.NameSetByUser)
				}
				if renameTo != "CI watcher" {
					t.Errorf("renameTo = %q, want the Bot's name re-asserted on KAS", renameTo)
				}
			})
		}
	}
}
