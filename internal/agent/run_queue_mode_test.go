package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// Beside a steerable turn the header's send is steer whatever the instance default, so the composer can offer both
// halves; a send goes where its own mode says, and a send naming none (the notify route, an API caller) follows the
// interrupt_mode setting.
func TestSubmit_ASendsModeDecidesBesideASteerableTurn(t *testing.T) {
	cases := []struct {
		name    string
		setting string
		mode    marotte.InterruptMode
		want    marotte.SendVerb
	}{
		{name: "no setting, no mode", want: marotte.SendSteer},
		{name: "queue setting, no mode", setting: `{"interrupt_mode":"queue"}`, want: marotte.SendQueue},
		{
			name: "queue setting, a steer send", setting: `{"interrupt_mode":"queue"}`,
			mode: marotte.InterruptSteer, want: marotte.SendSteer,
		},
		{
			name: "steer setting, a queue send", setting: `{"interrupt_mode":"steer"}`,
			mode: marotte.InterruptQueue, want: marotte.SendQueue,
		},
		{name: "an unknown setting reads as steer", setting: `{"interrupt_mode":"later"}`, want: marotte.SendSteer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.setting != "" {
				writeAgentSettings(t, dir, tc.setting)
			}
			h, cs, _ := newTestHub(WithConfigDir(dir))
			t.Cleanup(func() { shutdownHub(t, h) })
			seedChat(t, cs, "c1")
			h.coord.launchEntry = func(context.Context, marotte.ChatID, *command.EntryLaunch) (string, error) {
				return "", errors.New("not launched in this test")
			}
			s := &command.Submission{
				Row:    marotte.QueuedPrompt{ID: "m-new", Text: "hello"},
				Origin: marotte.RunFromPrompt, Kind: marotte.SendPlain, Mode: tc.mode, Wait: true, Composer: true,
			}
			mode := h.coord.sendMode(t.Context(), s)
			lc := h.coord.turns.testLifecycle("c1")
			lc.mu.Lock()
			defer lc.mu.Unlock()
			lc.run.entries = []*runEntry{{
				id: "t-1", kind: marotte.RunKindTurn, origin: marotte.RunFromWire, state: marotte.RunRunning,
				turnID: "t-1", steerable: true, released: true,
			}}
			if got := lc.runViewLocked().Send; got != marotte.SendSteer {
				t.Fatalf("RunView.Send = %q, want steer whatever the default", got)
			}
			if _, _, err := lc.submitLocked(h.coord.LifeStamp(), s, mode); err != nil {
				t.Fatalf("submit = %v", err)
			}
			_, e := lc.run.find("m-new")
			if e == nil {
				t.Fatal("the send is not on the list")
			}
			placed := marotte.SendQueue
			if e.state == marotte.RunOffered {
				placed = marotte.SendSteer
			}
			if placed != tc.want {
				t.Errorf("placed as %q (entry %s), want %q", placed, e.state, tc.want)
			}
		})
	}
}
