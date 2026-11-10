package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// openTurnsListed fails unless each turn c1 holds open has exactly one live entry naming it.
func openTurnsListed(t *testing.T, h *Runtime) {
	t.Helper()
	lc, ok := h.coord.turns.lookup("c1")
	if !ok {
		return
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	for _, at := range []*activeTurn{lc.own, lc.pending} {
		if at == nil || at.finalizing {
			continue
		}
		n := 0
		for _, e := range lc.run.entries[:lc.run.liveLen()] {
			if e.turnID == at.ID {
				n++
			}
		}
		if n != 1 {
			t.Errorf("open turn %s has %d live entries naming it, want 1", at.ID, n)
		}
	}
}

// compactBehind asks for a compaction behind what is live and waits until the list shows it pending.
func compactBehind(t *testing.T, h *Runtime, cs *testChatStore) {
	t.Helper()
	go func() { _, _ = h.RequestCompact(context.WithoutCancel(t.Context()), "c1") }()
	waitFor(t, func() bool { return slices.Contains(runStates(t, cs, "c1"), "compaction pending") })
}

// cancelHead presses Cancel on the entry the header names and waits for n session/cancel writes in all.
func cancelHead(t *testing.T, h *Runtime, cs *testChatStore, br *fakeBridge, n int) {
	t.Helper()
	target := liveRunCancel(t, cs)
	if target == "" {
		t.Fatalf("run.cancel is empty; list = %v", runStates(t, cs, "c1"))
	}
	if rec := postCmd(t, h, cancelCmd(`{"target":"`+target+`"}`)); rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, func() bool { return len(br.notified(marotte.MethodCancel)) == n })
}

// compactAfter reports whether the compaction was written after a session/cancel.
func compactAfterCancel(br *fakeBridge) bool {
	w := orderWrites(br)
	c, k := slices.Index(w, marotte.MethodCancel), slices.Index(w, marotte.MethodSessionCompact)
	return c >= 0 && k > c
}

// wireOpen delivers a bracketed turn_start and waits for kiro-cli's turn on the list.
func wireOpen(t *testing.T, cs *testChatStore, br *fakeBridge) {
	t.Helper()
	br.deliver(newTurnStartMsg())
	waitFor(t, func() bool { return slices.Contains(runStates(t, cs, "c1"), "wire running") })
}

// foldOpen delivers a frame with no turn open and waits for the fold-opened turn on the list.
func foldOpen(t *testing.T, cs *testChatStore, br *fakeBridge) {
	t.Helper()
	br.deliver(newChunkMsg("kiro-cli on its own"))
	waitFor(t, func() bool { return slices.Contains(runStates(t, cs, "c1"), "wire running") })
}

// Every path that opens, folds, closes or reports a turn, with the entry it becomes, how that entry ends, what
// Cancel writes for it and where a compaction asked for behind it goes.
func TestRunOrder_TurnPaths(t *testing.T) {
	t.Run("P1 a taken entry's prompt turn", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		open := promptGates(t, br)
		br.setCallResult(marotte.MethodPrompt, json.RawMessage(`{"stopReason":"cancelled"}`))
		if code := postPrompt(t, h, "a"); code != http.StatusOK {
			t.Fatalf("Setup: prompt = %d", code)
		}
		waitFor(t, func() bool { return slices.Equal(runStates(t, cs, "c1"), []string{"m-a running"}) })
		openTurnsListed(t, h)
		compactBehind(t, h, cs)
		if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{"m-a running", "compaction pending"}) {
			t.Errorf("list = %v, want [m-a running, compaction pending]", got)
		}
		cancelHead(t, h, cs, br, 1)
		open(0)
		waitForCall(t, br, marotte.MethodSessionCompact)
		if !compactAfterCancel(br) {
			t.Errorf("writes = %v, want the compaction after the turn's close", orderWrites(br))
		}
	})

	t.Run("P2 a bracketed wire turn", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		wireOpen(t, cs, br)
		if v := runView(t, cs, "c1"); len(v.Entries) != 1 || !v.Entries[0].Steerable || v.Entries[0].Yields {
			t.Errorf("list = %+v, want one steerable wire turn", v.Entries)
		}
		openTurnsListed(t, h)
		compactBehind(t, h, cs)
		cancelHead(t, h, cs, br, 1)
		staysFalse(t, 100*time.Millisecond, func() bool { return slices.Contains(br.callLog(), marotte.MethodSessionCompact) },
			"the compaction was written while kiro-cli's turn was open")
		br.deliver(newTurnEndMsg("cancelled"))
		waitForCall(t, br, marotte.MethodSessionCompact)
		if !compactAfterCancel(br) {
			t.Errorf("writes = %v, want the compaction after the turn's close", orderWrites(br))
		}
	})

	t.Run("P3 a fold-opened wire turn", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		foldOpen(t, cs, br)
		if v := runView(t, cs, "c1"); len(v.Entries) != 1 || v.Entries[0].Steerable || !v.Entries[0].Yields {
			t.Errorf("list = %+v, want one yielding wire turn", v.Entries)
		}
		openTurnsListed(t, h)
		cancelHead(t, h, cs, br, 1)
	})

	t.Run("P4 a wire turn while a taken entry's turn has not opened", func(t *testing.T) {
		h, cs, br, open, held, releaseOpen := takenRowHub(t)
		open(0)
		<-held
		wireOpen(t, cs, br)
		if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{"wire running", "m-q1 starting", "m-q2 pending"}) {
			t.Errorf("list = %v, want [wire running, m-q1 starting, m-q2 pending]", got)
		}
		compactBehind(t, h, cs)
		if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{"wire running", "m-q1 starting", "compaction pending", "m-q2 pending"}) {
			t.Errorf("list = %v, want the compaction behind the taken row", got)
		}
		cancelHead(t, h, cs, br, 1)
		close(releaseOpen)
		br.deliver(newTurnEndMsg("cancelled"))
		open(1)
		waitForCall(t, br, marotte.MethodSessionCompact)
		w := orderWrites(br)
		if n := countOf(w[:slices.Index(w, marotte.MethodSessionCompact)], marotte.MethodPrompt); n != 3 {
			t.Errorf("writes = %v, want the compaction after m-q1's prompt", w)
		}
	})

	t.Run("P5 a wire turn beside a sent unbound prompt", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		open := promptGates(t, br)
		if code := postPrompt(t, h, "1"); code != http.StatusOK {
			t.Fatalf("Setup: prompt = %d", code)
		}
		waitFor(t, func() bool { return countOf(br.callLog(), marotte.MethodPrompt) == 2 })
		br.deliver(newPromptTurnStartMsg("another-tag"))
		waitFor(t, func() bool { return slices.Equal(runStates(t, cs, "c1"), []string{"wire running", "m-1 running"}) })
		openTurnsListed(t, h)
		compactBehind(t, h, cs)
		if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{"wire running", "m-1 running", "compaction pending"}) {
			t.Errorf("list = %v, want the compaction behind the prompt", got)
		}
		cancelHead(t, h, cs, br, 1)
		open(0)
	})

	t.Run("P6 a wire turn under a running compaction", func(t *testing.T) {
		h, cs, br, release := compactingHubIn(t, "/tmp/work")
		wireOpen(t, cs, br)
		if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{"compaction running", "wire running"}) {
			t.Errorf("list = %v, want [compaction running, wire running]", got)
		}
		openTurnsListed(t, h)
		cancelHead(t, h, cs, br, 1)
		release()
	})

	t.Run("P7 a wire turn before a taken compaction is written", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		entered, releaseStart := make(chan struct{}), make(chan struct{})
		withCompactionStartHook(t, func(marotte.ChatID) {
			close(entered)
			<-releaseStart
		})
		go func() { _, _ = h.RequestCompact(context.WithoutCancel(t.Context()), "c1") }()
		<-entered
		wireOpen(t, cs, br)
		close(releaseStart)
		if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{"wire running", "compaction pending"}) {
			t.Errorf("list = %v, want [wire running, compaction pending]", got)
		}
		cancelHead(t, h, cs, br, 1)
		if got := compactionEntry(t, cs, "c1").State; got != marotte.RunPending {
			t.Errorf("compaction after the cancel = %q, want pending", got)
		}
		br.deliver(newTurnEndMsg("cancelled"))
		waitForCall(t, br, marotte.MethodSessionCompact)
	})

	t.Run("P8 a wire turn in a closing entry's tail replaces it", func(t *testing.T) {
		_, cs, br, turnID := heldCleanClose(t)
		wireOpen(t, cs, br)
		got := runStates(t, cs, "c1")
		if slices.Contains(got, turnID+" closing") || !slices.Equal(got, []string{"wire running", "compaction pending"}) {
			t.Errorf("list = %v, want [wire running, compaction pending]", got)
		}
	})

	t.Run("P9 a wire turn while a run entry stands for kiro-cli's run replaces it", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		h.coord.WireTurnActivity("c1", true)
		waitFor(t, func() bool { return slices.Equal(runStates(t, cs, "c1"), []string{"run running"}) })
		wireOpen(t, cs, br)
		if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{"wire running"}) {
			t.Errorf("list = %v, want [wire running]", got)
		}
	})

	t.Run("P10 a turn_start under an acked turn closes it and opens the next", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		wireOpen(t, cs, br)
		first := runView(t, cs, "c1").Entries[0].ID
		br.deliver(newTurnStartMsg())
		waitFor(t, func() bool {
			v := runView(t, cs, "c1")
			return len(v.Entries) == 1 && v.Entries[0].ID != first && v.Entries[0].State == marotte.RunRunning
		})
		openTurnsListed(t, h)
	})

	t.Run("F1 a frame folding into the open turn changes nothing", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		wireOpen(t, cs, br)
		before := runView(t, cs, "c1").Entries
		br.deliver(newChunkMsg("more of kiro-cli's turn"))
		staysFalse(t, 100*time.Millisecond, func() bool { return !slices.EqualFunc(runView(t, cs, "c1").Entries, before, sameView) },
			"a folded frame changed the list")
		openTurnsListed(t, h)
	})

	t.Run("F2 a turn_start during a fold-opened turn opens a new bracketed one", func(t *testing.T) {
		_, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		foldOpen(t, cs, br)
		fold := runView(t, cs, "c1").Entries[0].ID
		br.deliver(newTurnStartMsg())
		waitFor(t, func() bool {
			v := runView(t, cs, "c1")
			return len(v.Entries) == 1 && v.Entries[0].ID != fold
		})
		if e := runView(t, cs, "c1").Entries[0]; e.Yields || !e.Steerable {
			t.Errorf("the new turn = %+v, want bracketed: steerable, not yielding", e)
		}
	})

	t.Run("C1 a prompt's answer closes its turn and the next row goes", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		open := promptGates(t, br)
		if code := postPrompt(t, h, "a"); code != http.StatusOK {
			t.Fatalf("Setup: prompt = %d", code)
		}
		queueDuring(t, h, "m-q1")
		open(0)
		open(1)
		waitFor(t, func() bool { return opensInOrder(t, cs, "m-a", "m-q1") })
	})

	t.Run("C2 a failed prompt's turn pauses the rows behind it", func(t *testing.T) {
		h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
		id, _ := h.stagePromptTurn(t, "c1")
		queueDuring(t, h, "m-q1")
		h.AbandonInFlightTurn(t.Context(), "c1", id, marotte.StopReasonInterrupted, "the pipe died", "", 0)
		waitFor(t, func() bool { return slices.Equal(runStates(t, cs, "c1"), []string{"m-q1 paused"}) })
	})

	t.Run("C3 kiro-cli's clean turn_end unpauses the rows", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		seedQueue(t, cs, "c1", marotte.QueuedPrompt{ID: "m-q0", Text: "paused"})
		wireOpen(t, cs, br)
		br.deliver(newChunkMsg("kiro-cli's answer"))
		br.deliver(newTurnEndMsg("end_turn"))
		h.coord.WireTurnActivity("c1", false)
		waitFor(t, func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-q0") })
	})

	t.Run("C4 a lost bracket closes the turn it supersedes", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		wireOpen(t, cs, br)
		first := runView(t, cs, "c1").Entries[0].Turn
		br.deliver(newTurnStartMsg())
		waitFor(t, func() bool { return closedTurn(t, cs, first) })
		openTurnsListed(t, h)
	})

	t.Run("C5 a stopped turn's close lives through the KAS hold", func(t *testing.T) {
		h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
		stopUnderHold(t, h)
		got := runStates(t, cs, "c1")
		if len(got) != 2 || !strings.HasSuffix(got[0], " closing") || got[1] != "m-q0 paused" {
			t.Errorf("list under the hold = %v, want [<turn> closing, m-q0 paused]", got)
		}
		h.coord.WireTurnActivity("c1", false)
		waitFor(t, func() bool { return !listLive(h, "c1") })
	})

	t.Run("C6 kiro-cli's idle report closes a bracketed turn after the grace", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		h.coord.idleEndGrace = 20 * time.Millisecond
		wireOpen(t, cs, br)
		h.coord.WireTurnActivity("c1", true)
		h.coord.WireTurnActivity("c1", false)
		waitFor(t, func() bool { return !listLive(h, "c1") })
	})

	t.Run("C7 a prompt's send closes a fold-opened turn and takes its place", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		foldOpen(t, cs, br)
		if got := ackAs(t, postCmd(t, h, promptCmd("m-p", "beside the fold"))); got != "started" {
			t.Errorf("prompt answered %s, want started", got)
		}
		waitFor(t, func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-p") && !listLive(h, "c1") })
	})

	t.Run("C8 a bridge death closes every open turn", func(t *testing.T) {
		h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
		h.stagePromptTurn(t, "c1")
		queueDuring(t, h, "m-q1")
		h.coord.closeTurnOnBridgeDeath(t.Context(), "c1", marotte.TurnOutcomeInterrupted)
		waitFor(t, func() bool { return !h.coord.turns.live("c1") })
		waitFor(t, func() bool { return slices.Contains(runStates(t, cs, "c1"), "m-q1 paused") })
	})

	t.Run("C9 the life's end keeps only rows", func(t *testing.T) {
		for _, shape := range []string{"a compaction running", "kiro-cli's turn with a row behind it", "a taken row answered queued"} {
			t.Run(shape, func(t *testing.T) {
				h, cs, br, tab := closeTabHub(t)
				t.Cleanup(func() { shutdownHub(t, h) })
				br.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"done"}}
				runPrompt(t, h, cs, "c1", "m-first")
				switch shape {
				case "a compaction running":
					holdCall(t, br, marotte.MethodSessionCompact)
					go func() { _, _ = h.RequestCompact(context.WithoutCancel(t.Context()), "c1") }()
					waitForCall(t, br, marotte.MethodSessionCompact)
				case "kiro-cli's turn with a row behind it":
					wireOpen(t, cs, br)
					queueDuring(t, h, "m-q1")
				default:
					shrinkAdmissionWait(t, 60*time.Millisecond)
					h.coord.turns.openHook = func() { time.Sleep(300 * time.Millisecond) }
					if got := ackAs(t, postCmd(t, h, promptCmd("m-x", "slow open"))); got != "queued" {
						t.Fatalf("Setup: the slow open answered %s, want queued", got)
					}
				}
				before := chatRunVersion(t, cs)
				if code := postCloseTab(t, h, tab); code != http.StatusOK {
					t.Fatalf("close = %d", code)
				}
				awaitTeardown(t, h, "c1")

				for _, s := range runStates(t, cs, "c1") {
					if !strings.HasSuffix(s, " paused") && !strings.HasSuffix(s, " held") {
						t.Errorf("list after the close = %v, want only paused or held rows", runStates(t, cs, "c1"))
						break
					}
				}
				v := runView(t, cs, "c1")
				if v.Cancel != "" || v.Send != marotte.SendPrompt {
					t.Errorf("header after the close: cancel %q, send %q; want none and prompt", v.Cancel, v.Send)
				}
				if got := chatRunVersion(t, cs); got <= before {
					t.Errorf("run version after the close = %d, want past %d", got, before)
				}
				after := chatRunVersion(t, cs)
				staysFalse(t, 100*time.Millisecond, func() bool { return chatRunVersion(t, cs) != after },
					"the list was written after the life ended")
			})
		}
		t.Run("a dead head stored before the life is dropped though the life changed nothing", func(t *testing.T) {
			h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
			h.coord.turns.forget("c1")
			if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
				c.Run = []marotte.RunEntry{
					{ID: "compact-9", Kind: marotte.RunKindCompaction, State: marotte.RunRunning, Origin: marotte.RunFromRequest},
					{
						ID: "m-q1", Kind: marotte.RunKindRow, State: marotte.RunPaused, Origin: marotte.RunFromQueue,
						Row: &marotte.QueuedPrompt{ID: "m-q1", Text: "kept"},
					},
				}
				c.RunVersion++
				return true
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := h.coord.turns.lifecycleAt(t.Context(), "c1", h.coord.LifeStamp()); err != nil {
				t.Fatal(err)
			}
			h.coord.turns.forget("c1")
			if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{"m-q1 paused"}) {
				t.Errorf("record after the life = %v, want [m-q1 paused]", got)
			}
		})
		t.Run("a deleted chat gets no record back from its life's last write", func(t *testing.T) {
			h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
			h.stagePromptTurn(t, "c1")
			queueDuring(t, h, "m-q1")
			if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdDeleteChat, ChatID: "c1"}); rec.Code != http.StatusOK {
				t.Fatalf("delete_chat = %d %s", rec.Code, rec.Body.String())
			}
			awaitTeardown(t, h, "c1")
			if _, ok := cs.Get(t.Context(), "c1"); ok {
				t.Error("the deleted chat has a record again after its life ended")
			}
		})
		t.Run("a run chat's list is never written to a record", func(t *testing.T) {
			h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
			const run marotte.ChatID = "run:wf-1"
			h.coord.turns.lifecycleForRun(run)
			h.coord.WireTurnActivity(run, true)
			h.coord.WireTurnActivity(run, false)
			h.coord.turns.forget(run)
			if _, ok := cs.Get(t.Context(), run); ok {
				t.Error("the run chat has a chat record after its list changed and its life ended")
			}
		})
		t.Run("a new chat's first prompt is stored once its turn opens the record", func(t *testing.T) {
			h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
			open := promptGates(t, br)
			cmd := promptCmd("m-new", "the first words")
			cmd.ChatID = "c-new"
			if got := ackAs(t, postCmd(t, h, cmd)); got != "started" {
				t.Fatalf("first prompt answered %s, want started", got)
			}
			waitFor(t, func() bool {
				c, ok := cs.Get(t.Context(), "c-new")
				return ok && slices.ContainsFunc(c.Run, func(e marotte.RunEntry) bool { return e.ID == "m-new" && e.State.Live() })
			})
			open(0)
		})
		t.Run("a life that changed a clean list in nothing writes nothing at its end", func(t *testing.T) {
			h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
			seedQueue(t, cs, "c1", marotte.QueuedPrompt{ID: "m-q1", Text: "kept"})
			h.coord.turns.forget("c1")
			if _, err := h.coord.turns.lifecycleAt(t.Context(), "c1", h.coord.LifeStamp()); err != nil {
				t.Fatal(err)
			}
			before := chatRunVersion(t, cs)
			h.coord.turns.forget("c1")
			if got := chatRunVersion(t, cs); got != before {
				t.Errorf("run version after an unchanged life = %d, want %d", got, before)
			}
		})
	})

	t.Run("K1 kiro-cli's run with no turn here is a run entry", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		h.coord.WireTurnActivity("c1", true)
		waitFor(t, func() bool { return slices.Equal(runStates(t, cs, "c1"), []string{"run running"}) })
		compactBehind(t, h, cs)
		if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{"run running", "compaction pending"}) {
			t.Errorf("list = %v, want [run running, compaction pending]", got)
		}
		cancelHead(t, h, cs, br, 1)
		staysFalse(t, 100*time.Millisecond, func() bool { return slices.Contains(br.callLog(), marotte.MethodSessionCompact) },
			"the compaction was written while kiro-cli reported its run")
		h.coord.WireTurnActivity("c1", false)
		waitForCall(t, br, marotte.MethodSessionCompact)
		if !compactAfterCancel(br) {
			t.Errorf("writes = %v, want the compaction once kiro-cli reported idle", orderWrites(br))
		}
	})

	t.Run("K2 kiro-cli's run report beside a live entry changes nothing", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		open := promptGates(t, br)
		if code := postPrompt(t, h, "a"); code != http.StatusOK {
			t.Fatalf("Setup: prompt = %d", code)
		}
		waitFor(t, func() bool { return slices.Equal(runStates(t, cs, "c1"), []string{"m-a running"}) })
		h.coord.WireTurnActivity("c1", true)
		staysFalse(t, 100*time.Millisecond, func() bool { return !slices.Equal(runStates(t, cs, "c1"), []string{"m-a running"}) },
			"kiro-cli's report changed the list beside a live turn")
		h.coord.WireTurnActivity("c1", false)
		open(0)
	})

	t.Run("K3 kiro-cli's idle report removes the run entry", func(t *testing.T) {
		h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
		h.coord.WireTurnActivity("c1", true)
		waitFor(t, func() bool { return slices.Equal(runStates(t, cs, "c1"), []string{"run running"}) })
		h.coord.WireTurnActivity("c1", false)
		waitFor(t, func() bool { return len(runStates(t, cs, "c1")) == 0 })
	})

	t.Run("K4 a close's KAS hold keeps its closing entry", func(t *testing.T) {
		h, cs, _, turnID := heldCleanClose(t)
		staysFalse(t, 100*time.Millisecond, func() bool { return !slices.Contains(runStates(t, cs, "c1"), turnID+" closing") },
			"the closing entry left the list under the close's hold")
		h.coord.WireTurnActivity("c1", false)
		waitFor(t, func() bool { return !slices.Contains(runStates(t, cs, "c1"), turnID+" closing") })
	})

	t.Run("K5 kiro-cli's re-wake after a cancel is a run entry, then its turn", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		wireOpen(t, cs, br)
		cancelHead(t, h, cs, br, 1)
		br.deliver(newTurnEndMsg("cancelled"))
		waitFor(t, func() bool { return !listLive(h, "c1") })
		h.coord.WireTurnActivity("c1", true)
		waitFor(t, func() bool { return slices.Equal(runStates(t, cs, "c1"), []string{"run running"}) })
		wireOpen(t, cs, br)
		if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{"wire running"}) {
			t.Errorf("list = %v, want [wire running]", got)
		}
	})

	t.Run("K6 a subagent's wake hold changes nothing and refuses no take", func(t *testing.T) {
		h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
		h.coord.turns.holdWake("c1", "wf-1")
		if got := runStates(t, cs, "c1"); len(got) != 0 {
			t.Errorf("list after the wake hold = %v, want empty", got)
		}
		if got := ackAs(t, postCmd(t, h, promptCmd("m-p", "during the wake hold"))); got != "started" {
			t.Errorf("prompt answered %s, want started", got)
		}
	})

	t.Run("K7 a hosted run's step turn is not the chat's", func(t *testing.T) {
		h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
		h.coord.turns.holdStep("c1", "t-step")
		if got := runStates(t, cs, "c1"); len(got) != 0 {
			t.Errorf("list with a step turn hosted = %v, want empty", got)
		}
		h.coord.turns.endStep("c1", "t-step")
	})

	t.Run("S-1 a reader's stop by id leaves a pending compaction pending", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		open := promptGates(t, br)
		if code := postPrompt(t, h, "a"); code != http.StatusOK {
			t.Fatalf("Setup: prompt = %d", code)
		}
		waitFor(t, func() bool { return slices.Equal(runStates(t, cs, "c1"), []string{"m-a running"}) })
		compactBehind(t, h, cs)
		wantStoppedNothing(t, postCmd(t, h, cancelCmd(`{"target":"m-first"}`)))
		cancelHead(t, h, cs, br, 1)
		if got := compactionEntry(t, cs, "c1").State; got != marotte.RunPending {
			t.Errorf("compaction after the stop = %q, want pending", got)
		}
		open(0)
	})

	t.Run("S-2 a teardown's stop changes no entry and the life's end keeps the rows", func(t *testing.T) {
		h, cs, br, tab := closeTabHub(t)
		t.Cleanup(func() { shutdownHub(t, h) })
		br.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"done"}}
		runPrompt(t, h, cs, "c1", "m-first")
		h.stagePromptTurn(t, "c1")
		queueDuring(t, h, "m-q1")
		if code := postCloseTab(t, h, tab); code != http.StatusOK {
			t.Fatalf("close = %d", code)
		}
		awaitTeardown(t, h, "c1")
		if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{"m-q1 paused"}) {
			t.Errorf("list after the teardown = %v, want [m-q1 paused]", got)
		}
	})

	t.Run("S-3 a stop of an opened unsent prompt keeps it unsent", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		parked, releaseSend := make(chan struct{}), make(chan struct{})
		var opened, once atomicFlag
		h.coord.turns.openHook = func() { opened.set() }
		h.coord.turns.claimHook = func() {
			if opened.get() && once.setOnce() {
				close(parked)
				<-releaseSend
			}
		}
		t.Cleanup(func() {
			if once.get() {
				select {
				case <-releaseSend:
				default:
					close(releaseSend)
				}
			}
		})
		answered := postAsync(h, promptCmd("m-p", "opened, not sent"))
		<-parked
		if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{"m-p starting"}) {
			t.Errorf("list at the send gate = %v, want [m-p starting]", got)
		}
		if rec := postCmd(t, h, cancelCmd(`{"target":"m-p"}`)); rec.Code != http.StatusOK {
			t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
		}
		close(releaseSend)
		<-answered
		waitFor(t, func() bool { return !listLive(h, "c1") })
		if n := countOf(br.callLog(), marotte.MethodPrompt); n != 1 {
			t.Errorf("session/prompt written %d times, want 1 (m-first)", n)
		}
		if n := len(br.notified(marotte.MethodCancel)); n != 0 {
			t.Errorf("session/cancel sent %d times, want none: nothing was sent", n)
		}
	})
}

// atomicFlag is a test's one-shot flag shared with a hook.
type atomicFlag struct{ v atomic.Bool }

func (f *atomicFlag) set()          { f.v.Store(true) }
func (f *atomicFlag) get() bool     { return f.v.Load() }
func (f *atomicFlag) setOnce() bool { return f.v.CompareAndSwap(false, true) }

// sameView compares two header entries by identity and state.
func sameView(a, b marotte.RunEntryView) bool {
	return a.ID == b.ID && a.State == b.State && a.Steerable == b.Steerable && a.Yields == b.Yields
}

// closedTurn reports whether the chat's log closed turnID.
func closedTurn(t *testing.T, cs *testChatStore, turnID string) bool {
	t.Helper()
	return slices.ContainsFunc(logOf(t, cs, "c1"), func(e marotte.Entry) bool {
		return e.Turn == turnID && e.Kind == marotte.EntryKindTurnClose
	})
}

func chatRunVersion(t *testing.T, cs *testChatStore) uint64 {
	t.Helper()
	c, _ := cs.Get(t.Context(), "c1")
	if c == nil {
		return 0
	}
	return c.RunVersion
}
