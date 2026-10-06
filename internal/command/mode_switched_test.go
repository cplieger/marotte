package command

import (
	"context"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// modeSwitchSpy records what the command asked to persist. It embeds the host double
// so every other role it fills is unchanged, and shadows the one method under test.
type modeSwitchSpy struct {
	hostDouble
	switches []marotte.EntryModeSwitched
}

func (s *modeSwitchSpy) PersistModeSwitch(_ context.Context, _ marotte.ChatID, sw marotte.EntryModeSwitched) {
	s.switches = append(s.switches, sw)
}

func newModeSwitchSpy(t *testing.T) *modeSwitchSpy {
	t.Helper()
	return &modeSwitchSpy{hostDouble: newTestHost(t, testsupport.NewInMemoryChatStore())}
}

// A pick that MOVED the mode leaves exactly one entry, naming both endpoints and the
// reader as its source. Both endpoints because a mode id names a workflow rather than
// a version, so the banner cannot say what changed from the destination alone.
func TestCmdSetMode_RecordsTheSwitchItApplied(t *testing.T) {
	spy := newModeSwitchSpy(t)

	if _, err := CmdSetMode(t.Context(), spy, spy, spy, spy, setModeReq(t, "c1", "spec")); err != nil {
		t.Fatalf("first pick: %v", err)
	}
	if _, err := CmdSetMode(t.Context(), spy, spy, spy, spy, setModeReq(t, "c1", "vibe")); err != nil {
		t.Fatalf("second pick: %v", err)
	}

	if len(spy.switches) != 2 {
		t.Fatalf("two picks recorded %d switches, want 2: %+v", len(spy.switches), spy.switches)
	}
	want := []marotte.EntryModeSwitched{
		{From: "", To: "spec", Source: marotte.ModeSwitchSourceUser},
		{From: "spec", To: "vibe", Source: marotte.ModeSwitchSourceUser},
	}
	for i, w := range want {
		if spy.switches[i] != w {
			t.Errorf("switch %d = %+v, want %+v", i, spy.switches[i], w)
		}
	}
}

// Idempotence, and it is the whole reason the header write leads: a pick of the mode
// already in force changes nothing, so it must leave no entry — otherwise a reader
// clicking the live mode twice gets a banner for a switch that never happened. The
// same guard is what stops KAS's own echo of this switch (current_mode_update, which
// HandleModeUpdate consumes) appending a second entry for one change.
func TestCmdSetMode_RepeatPickRecordsNothing(t *testing.T) {
	spy := newModeSwitchSpy(t)

	if _, err := CmdSetMode(t.Context(), spy, spy, spy, spy, setModeReq(t, "c1", "spec")); err != nil {
		t.Fatalf("first pick: %v", err)
	}
	before := len(spy.switches)
	if _, err := CmdSetMode(t.Context(), spy, spy, spy, spy, setModeReq(t, "c1", "spec")); err != nil {
		t.Fatalf("repeat pick: %v", err)
	}

	if got := len(spy.switches) - before; got != 0 {
		t.Errorf("a repeat pick of the mode already in force recorded %d switches, want 0: %+v",
			got, spy.switches[before:])
	}
}
