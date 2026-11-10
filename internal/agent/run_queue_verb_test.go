package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// The composer's face and the server's placement agree: for each live prefix of the send table, the header's
// RunView.Send is the table's verb, and a plain send's Submit answer is that verb (prompt: its own take; steer: into
// the running turn; queue: on the list).
func TestSubmit_PlacesASendAsTheHeadersVerbSays(t *testing.T) {
	running := func(id string, steerable bool) *runEntry {
		return &runEntry{
			id: id, kind: marotte.RunKindTurn, origin: marotte.RunFromWire, state: marotte.RunRunning,
			turnID: id, steerable: steerable, released: true,
		}
	}
	row := func(id string, state marotte.RunEntryState) *runEntry {
		return &runEntry{
			id: id, kind: marotte.RunKindRow, origin: marotte.RunFromQueue, state: state,
			row: &marotte.QueuedPrompt{ID: id, Text: id},
		}
	}
	compaction := func(state marotte.RunEntryState) *runEntry {
		return &runEntry{
			id: "compact-1", kind: marotte.RunKindCompaction, origin: marotte.RunFromRequest, state: state,
			done: newCompactWait(),
		}
	}
	cases := []struct {
		name    string
		mode    marotte.InterruptMode
		entries func() []*runEntry
		want    marotte.SendVerb
	}{
		{name: "an idle list", want: marotte.SendPrompt, entries: func() []*runEntry { return nil }},
		{name: "only a paused row", want: marotte.SendPrompt, entries: func() []*runEntry {
			return []*runEntry{row("m-q0", marotte.RunPaused)}
		}},
		{name: "a running steerable turn, steer mode", want: marotte.SendSteer, entries: func() []*runEntry {
			return []*runEntry{running("t-1", true)}
		}},
		{
			name: "a running steerable turn, queue mode", mode: marotte.InterruptQueue, want: marotte.SendQueue,
			entries: func() []*runEntry { return []*runEntry{running("t-1", true)} },
		},
		{name: "a running run step", want: marotte.SendQueue, entries: func() []*runEntry {
			return []*runEntry{running("t-1", false)}
		}},
		{name: "a starting turn", want: marotte.SendQueue, entries: func() []*runEntry {
			e := row("m-1", marotte.RunStarting)
			return []*runEntry{e}
		}},
		{name: "a stopping turn", want: marotte.SendQueue, entries: func() []*runEntry {
			e := running("t-1", false)
			e.state = marotte.RunStopping
			return []*runEntry{e}
		}},
		{name: "a closing turn", want: marotte.SendQueue, entries: func() []*runEntry {
			e := running("t-1", false)
			e.state = marotte.RunClosing
			return []*runEntry{e}
		}},
		{name: "a running compaction", want: marotte.SendQueue, entries: func() []*runEntry {
			return []*runEntry{compaction(marotte.RunRunning)}
		}},
		{name: "kiro-cli's turn under a running compaction", want: marotte.SendQueue, entries: func() []*runEntry {
			return []*runEntry{compaction(marotte.RunRunning), running("t-w", true)}
		}},
		{name: "a fold-opened turn kiro-cli admits beside", want: marotte.SendPrompt, entries: func() []*runEntry {
			e := running("t-f", false)
			e.yields, e.admits = true, true
			return []*runEntry{e}
		}},
		{name: "a fold-opened turn with a compaction pending", want: marotte.SendQueue, entries: func() []*runEntry {
			e := running("t-f", false)
			e.yields, e.admits = true, true
			return []*runEntry{e, compaction(marotte.RunPending)}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, cs, _ := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			seedChat(t, cs, "c1")
			h.coord.launchEntry = func(context.Context, marotte.ChatID, *command.EntryLaunch) (string, error) {
				return "", errors.New("not launched in this test")
			}
			lc := h.coord.turns.testLifecycle("c1")
			lc.mu.Lock()
			defer lc.mu.Unlock()
			lc.run.entries = tc.entries()

			if got := lc.runViewLocked(tc.mode).Send; got != tc.want {
				t.Fatalf("RunView.Send = %q, want %q", got, tc.want)
			}
			ans, taken, err := lc.submitLocked(h.coord.LifeStamp(), &command.Submission{
				Row:    marotte.QueuedPrompt{ID: "m-new", Text: "hello"},
				Origin: marotte.RunFromPrompt, Kind: marotte.SendPlain, Wait: true, Composer: true,
			}, tc.mode)
			if err != nil {
				t.Fatalf("submit = %v", err)
			}
			var placed marotte.SendVerb
			switch {
			case ans.Steer:
				placed = marotte.SendSteer
			case taken != nil:
				placed = marotte.SendPrompt
			case ans.As == command.SubmitQueued:
				placed = marotte.SendQueue
			}
			if placed != tc.want {
				t.Errorf("submit placed the send as %q (answer %+v), want %q as the header said", placed, ans, tc.want)
			}
		})
	}
}

// Chat.RunVersion grows with every write of Chat.Run, whichever writer makes it (a carried row written with no live
// life, then a live life's list), so a page holding the newest version never takes an older list.
func TestRunVersion_GrowsAcrossWriters(t *testing.T) {
	h, cs, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	seedChat(t, cs, "c1")
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.RunVersion = 5
		return true
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	version := func() uint64 {
		c, _ := cs.Get(t.Context(), "c1")
		return c.RunVersion
	}

	carried := &marotte.QueuedPrompt{ID: "m-carried", Text: "unread", Resends: []string{"steer-a"}}
	if err := h.coord.holdCarried(t.Context(), "c1", carried); err != nil {
		t.Fatalf("holdCarried: %v", err)
	}
	afterCarry := version()
	if afterCarry <= 5 {
		t.Fatalf("version after the carried row = %d, want above the stored 5", afterCarry)
	}

	runPrompt(t, h, cs, "c1", "m-1")
	if got := version(); got <= afterCarry {
		t.Errorf("version after a live life's writes = %d, want above %d", got, afterCarry)
	}
}
