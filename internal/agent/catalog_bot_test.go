package agent

import (
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

var (
	vibeMode = marotte.SessionMode{ID: marotte.DefaultModeID, Name: "Default", Source: "bundled"}
	specMode = marotte.SessionMode{ID: "kiro_spec", Name: "Spec", Source: "bundled"}
	botMode  = marotte.SessionMode{ID: marotte.BotRoleID, Name: marotte.BotRoleID, Description: "Weekly answers: Answer the weekly questions.", Source: "bundled"}
)

// TestCatalog_NeverRecordsTheBotRole pins that the role is one session's agent, not a workspace fact, so the catalog
// drops it by id before it compares or stores, and a Bot's list equal to a regular chat's changes nothing.
func TestCatalog_NeverRecordsTheBotRole(t *testing.T) {
	c := &catalog{}

	if !c.setModes([]marotte.SessionMode{vibeMode, botMode, specMode}) {
		t.Error("the first list reported no change")
	}
	held, _, stamp := c.modesModelsStamped()
	if want := []marotte.SessionMode{vibeMode, specMode}; !slices.Equal(held, want) {
		t.Errorf("modes = %+v, want %+v", held, want)
	}

	for name, modes := range map[string][]marotte.SessionMode{
		"a regular chat's list":           {vibeMode, specMode},
		"a Bot's list with the same rest": {vibeMode, specMode, botMode},
	} {
		if c.setModes(modes) {
			t.Errorf("%s reported a change", name)
		}
	}
	if _, _, again := c.modesModelsStamped(); again.Version != stamp.Version {
		t.Errorf("catalog version %q -> %q with nothing but the role changing", stamp.Version, again.Version)
	}

	if !c.setModes([]marotte.SessionMode{vibeMode, botMode}) {
		t.Error("a list that really lost a mode reported no change")
	}
	if _, _, moved := c.modesModelsStamped(); moved.Version == stamp.Version {
		t.Error("the catalog version did not move on a real change")
	}
}

// TestCatalog_AListOfOnlyTheBotRoleIsIgnored pins that a list the drop leaves empty is treated as the empty report
// it now is, on a seeded and an empty catalog alike.
func TestCatalog_AListOfOnlyTheBotRoleIsIgnored(t *testing.T) {
	for name, seed := range map[string][]marotte.SessionMode{"a seeded catalog": {vibeMode}, "an empty catalog": nil} {
		t.Run(name, func(t *testing.T) {
			c := &catalog{}
			c.setModes(seed)

			if c.setModes([]marotte.SessionMode{botMode}) {
				t.Error("a list of only the role reported a change")
			}
			if held, _, _ := c.modesModelsStamped(); !slices.Equal(held, seed) {
				t.Errorf("modes = %+v, want %+v", held, seed)
			}
		})
	}
}
