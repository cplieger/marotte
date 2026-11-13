package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

func promptIDs(t *testing.T, cs *testChatStore, chatID marotte.ChatID) []string {
	t.Helper()
	var ids []string
	for _, o := range opensOf(t, logOf(t, cs, chatID)) {
		if o.Prompt != nil {
			ids = append(ids, o.Prompt.ID)
		}
	}
	return ids
}

// queuedRows is the rows waiting on the chat's stored run list, in list order: not taken, not running.
func queuedRows(t *testing.T, cs *testChatStore, chatID marotte.ChatID) []marotte.RunEntry {
	t.Helper()
	c, ok := cs.Get(t.Context(), chatID)
	if !ok {
		t.Fatalf("chat %q has no record", chatID)
	}
	var out []marotte.RunEntry
	for _, e := range c.Run {
		if e.Row != nil && !e.State.Live() {
			out = append(out, e)
		}
	}
	return out
}

// queuedIDs is queuedRows' ids, and the ids marked to go next.
func queuedIDs(t *testing.T, cs *testChatStore, chatID marotte.ChatID) (ids, marked []string) {
	t.Helper()
	for _, r := range queuedRows(t, cs, chatID) {
		ids = append(ids, r.ID)
		if r.Next {
			marked = append(marked, r.ID)
		}
	}
	return ids, marked
}

// seedQueue lists rows as waiting for the next clean turn end (paused), as a stopped turn leaves them: on the chat's
// live list when it has one, else on its stored list.
func seedQueue(t *testing.T, cs *testChatStore, chatID marotte.ChatID, rows ...marotte.QueuedPrompt) {
	t.Helper()
	if _, err := cs.Mutate(t.Context(), chatID, func(c *marotte.Chat, _ bool) bool {
		c.Run = nil
		for i := range rows {
			c.Run = append(c.Run, marotte.RunEntry{
				Row: &rows[i], ID: rows[i].ID, Kind: marotte.RunKindRow, State: marotte.RunPaused,
				Origin: marotte.RunFromQueue,
			})
		}
		c.RunVersion++
		return true
	}); err != nil {
		t.Fatalf("seed queue: %v", err)
	}
	h, ok := seededHubs.Load(cs)
	if !ok {
		return
	}
	lc, ok := h.(*Runtime).coord.turns.lookup(chatID)
	if !ok {
		return
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	// The live entries stay: a seed on a running chat queues behind what runs, as a queued send would.
	lc.run.entries = slices.DeleteFunc(lc.run.entries, func(e *runEntry) bool { return !e.live() })
	for i := range rows {
		lc.run.entries = append(lc.run.entries, &runEntry{
			id: rows[i].ID, kind: marotte.RunKindRow, state: marotte.RunPaused, origin: marotte.RunFromQueue, row: &rows[i],
		})
	}
	lc.run.touch()
	lc.wakeLocked()
}

// seededHubs maps a hub's chat store to the hub, so seedQueue reaches a chat's live list.
var seededHubs sync.Map

// runPrompt sends messageID as a prompt and waits until its turn closed and the list is idle again.
func runPrompt(t *testing.T, h *Runtime, cs *testChatStore, chatID marotte.ChatID, messageID string) {
	t.Helper()
	before := len(closesOf(t, logOf(t, cs, chatID)))
	rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdPrompt, ChatID: chatID,
		Payload: json.RawMessage(`{"text":"hello","message_id":"` + messageID + `"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("prompt %s = %d %s", messageID, rec.Code, rec.Body.String())
	}
	waitFor(t, func() bool { return len(closesOf(t, logOf(t, cs, chatID))) > before && !h.coord.turns.live(chatID) })
	// The closed turn's entry leaves the list only once its tail ended, so what is sent next starts on an idle list.
	waitFor(t, func() bool { return !listLive(h, chatID) })
}

// listLive reports whether chatID's run list has a live entry.
func listLive(h *Runtime, chatID marotte.ChatID) bool {
	lc, ok := h.coord.turns.lookup(chatID)
	if !ok {
		return false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return len(marotte.LivePrefix(lc.run.project())) > 0
}

// idleAfterFirstTurn is chat c1 with a live session, idle once its first turn m-first and that close's tail ended;
// tails counts the close tails that ended. Every compaction call answers accepted.
func idleAfterFirstTurn(t *testing.T, workDir string) (h *Runtime, cs *testChatStore, br *fakeBridge, tails *atomic.Int32) {
	t.Helper()
	h, cs, br = newTestHubIn(workDir)
	// A hub left running carries its prompt goroutines into the next test, which races its package-var seams.
	t.Cleanup(func() { shutdownHub(t, h) })
	br.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"done"}}
	tails = &atomic.Int32{}
	tailHooks(h, func(_ marotte.ChatID, ended bool) {
		if ended {
			tails.Add(1)
		}
	})
	seedQueue(t, cs, "c1")
	runPrompt(t, h, cs, "c1", "m-first")
	waitFor(t, func() bool { return tails.Load() == 1 })
	br.setCallResult(marotte.MethodSessionCompact, json.RawMessage(`{"success":true}`))
	return h, cs, br, tails
}

// tailHooks adds fn to the hub's close tail hooks, which run before each tail's end and once it ended.
func tailHooks(h *Runtime, fn func(chatID marotte.ChatID, ended bool)) {
	prev := h.coord.turns.tailHook
	h.coord.turns.tailHook = func(chatID marotte.ChatID, ended bool) {
		if prev != nil {
			prev(chatID, ended)
		}
		fn(chatID, ended)
	}
}

// compactingHub is a chat whose reader's compaction runs on its idle session until release is called.
func compactingHub(t *testing.T) (h *Runtime, cs *testChatStore, release func()) {
	t.Helper()
	h, cs, _, release = compactingHubIn(t, "/tmp/work")
	return h, cs, release
}

func compactingHubIn(t *testing.T, workDir string) (h *Runtime, cs *testChatStore, br *fakeBridge, release func()) {
	t.Helper()
	h, cs, br, _ = idleAfterFirstTurn(t, workDir)
	release = holdCall(t, br, marotte.MethodSessionCompact)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = h.RequestCompact(context.WithoutCancel(t.Context()), "c1")
	}()
	waitForCall(t, br, marotte.MethodSessionCompact)
	t.Cleanup(func() {
		release()
		<-done
	})
	return h, cs, br, release
}

func queueDuring(t *testing.T, h *Runtime, ids ...string) {
	t.Helper()
	for _, id := range ids {
		queueTextDuring(t, h, id, "after the compaction")
	}
}

func queueTextDuring(t *testing.T, h *Runtime, id, text string) {
	t.Helper()
	payload, err := json.Marshal(marotte.QueuePromptCommand{Text: text, MessageID: id})
	if err != nil {
		t.Fatal(err)
	}
	rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdQueuePrompt, ChatID: "c1", Payload: payload})
	if rec.Code != http.StatusOK {
		t.Fatalf("queue_prompt %s = %d %s, want 200", id, rec.Code, rec.Body.String())
	}
}

// compactionEntry is the chat's compaction on its stored run list, the zero entry for none.
func compactionEntry(t *testing.T, cs *testChatStore, chatID marotte.ChatID) marotte.RunEntry {
	t.Helper()
	c, _ := cs.Get(t.Context(), chatID)
	if c == nil {
		return marotte.RunEntry{}
	}
	for _, e := range c.Run {
		if e.Kind == marotte.RunKindCompaction {
			return e
		}
	}
	return marotte.RunEntry{}
}

// contextLimitCompaction is chat c1 whose turn m-old was queued behind ended refused for the context's size, its
// turn-end compaction running until release is called.
func contextLimitCompaction(t *testing.T) (h *Runtime, cs *testChatStore, tails *atomic.Int32, release func()) {
	t.Helper()
	h, cs, br, tails := idleAfterFirstTurn(t, "/tmp/work")
	h.coord.autoCompact.policy = func(context.Context) (bool, int) { return true, 90 }
	release = holdCall(t, br, marotte.MethodSessionCompact)
	id, _ := h.stagePromptTurn(t, "c1")
	queueTextDuring(t, h, "m-old", "queued while the turn ran")
	h.AbandonInFlightTurn(t.Context(), "c1", id, marotte.StopReasonInterrupted, "too long", marotte.FailureKindContextLimit, 0)
	waitForCall(t, br, marotte.MethodSessionCompact)
	return h, cs, tails, release
}

func TestRun_AQueuedRowOpensItsTurnAfterACleanClose(t *testing.T) {
	h, cs, br := newTestHub()
	br.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"done"}}
	unblock := make(chan struct{})
	br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: unblock}

	postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdPrompt, ChatID: "c1", Payload: json.RawMessage(`{"text":"start","message_id":"m-1"}`),
	})
	waitForCall(t, br, marotte.MethodPrompt)
	queueTextDuring(t, h, "m-q1", "then run the tests")
	queueTextDuring(t, h, "m-q2", "then commit")
	close(unblock)

	waitFor(t, func() bool {
		return slices.Equal(promptIDs(t, cs, "c1"), []string{"m-1", "m-q1", "m-q2"}) && len(queuedRows(t, cs, "c1")) == 0
	})
}

// Rows a broken close left wait for the next clean turn end; none is sent by the close itself.
func TestRun_RowsWaitAfterABrokenClose(t *testing.T) {
	h, cs, _, tails := idleAfterFirstTurn(t, "/tmp/work")
	id, _ := h.stagePromptTurn(t, "c1")
	queueTextDuring(t, h, "m-q1", "after this turn")
	h.AbandonInFlightTurn(t.Context(), "c1", id, marotte.StopReasonInterrupted, "the pipe died", "", 0)
	waitFor(t, func() bool { return tails.Load() == 2 })

	if ids := promptIDs(t, cs, "c1"); slices.Contains(ids, "m-q1") {
		t.Errorf("turns opened = %v, want the row still waiting after a broken close", ids)
	}
	if rows := queuedRows(t, cs, "c1"); len(rows) != 1 || rows[0].State != marotte.RunPaused {
		t.Errorf("queue = %+v, want m-q1 paused", rows)
	}
}

// Send now on a row a broken close left waiting sends it at once, ahead of the other waiting rows.
func TestRun_SendNowOnAnIdleChatSendsTheRowAtOnce(t *testing.T) {
	h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
	seedQueue(t, cs, "c1",
		marotte.QueuedPrompt{ID: "m-q1", Text: "first"},
		marotte.QueuedPrompt{ID: "m-q2", Text: "second"},
	)

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdSendQueuedNow, ChatID: "c1", Payload: json.RawMessage(`{"message_id":"m-q2"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("send_queued_now = %d %s", rec.Code, rec.Body.String())
	}

	waitFor(t, func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-q2") && !h.coord.turns.live("c1") })
	if ids := promptIDs(t, cs, "c1"); ids[1] != "m-q2" {
		t.Errorf("turns opened = %v, want m-q2 sent at once", ids)
	}
}

// Messages queued while the reader's compaction runs on an idle chat open no turn under it, then go after it, one per
// turn in the order they were queued.
func TestRun_RowsQueuedDuringAnIdleCompactionGoAfterItOneATurn(t *testing.T) {
	h, cs, release := compactingHub(t)
	queueDuring(t, h, "m-q1", "m-q2")
	if ids := promptIDs(t, cs, "c1"); !slices.Equal(ids, []string{"m-first"}) {
		t.Fatalf("turns opened while the compaction ran = %v, want none", ids)
	}

	release()

	waitFor(t, func() bool {
		return slices.Equal(promptIDs(t, cs, "c1"), []string{"m-first", "m-q1", "m-q2"}) &&
			len(queuedRows(t, cs, "c1")) == 0 && !h.coord.turns.live("c1")
	})
}

// A prompt that reaches the server while a compaction runs joins the list behind it and answers queued.
func TestRun_APromptDuringACompactionIsQueuedBehindIt(t *testing.T) {
	h, cs, release := compactingHub(t)

	rec := postCmd(t, h, promptCmd("m-p1", "what changed?"))
	if got := ackAs(t, rec); got != "queued" {
		t.Errorf("prompt during the compaction answered %s, want queued", got)
	}
	if rows := queuedRows(t, cs, "c1"); len(rows) != 1 || rows[0].ID != "m-p1" || rows[0].Row.Text != "what changed?" {
		t.Errorf("queue = %+v, want the prompt as its one row", rows)
	}

	release()

	waitFor(t, func() bool {
		return slices.Equal(promptIDs(t, cs, "c1"), []string{"m-first", "m-p1"}) && len(queuedRows(t, cs, "c1")) == 0
	})
}

// Send now on a row waiting for a compaction opens no turn under it, and the row goes first once it ends.
func TestRun_SendNowDuringACompactionSendsTheRowFirstAfterIt(t *testing.T) {
	h, cs, release := compactingHub(t)
	queueDuring(t, h, "m-q1", "m-q2")

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdSendQueuedNow, ChatID: "c1", Payload: json.RawMessage(`{"message_id":"m-q2"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("send_queued_now during the compaction = %d %s", rec.Code, rec.Body.String())
	}
	if ids := promptIDs(t, cs, "c1"); !slices.Equal(ids, []string{"m-first"}) {
		t.Errorf("turns opened while the compaction ran = %v, want none", ids)
	}

	release()

	waitFor(t, func() bool {
		return slices.Equal(promptIDs(t, cs, "c1"), []string{"m-first", "m-q2", "m-q1"}) && len(queuedRows(t, cs, "c1")) == 0
	})
}

// A message past the draft cap sent during a compaction is queued behind it and goes when the compaction ends.
func TestRun_ARowPastTheDraftCapQueuesBehindACompaction(t *testing.T) {
	h, cs, release := compactingHub(t)
	long := strings.Repeat("x", marotte.MaxDraftBytes+1)

	queueTextDuring(t, h, "m-long", long)
	if rows := queuedRows(t, cs, "c1"); len(rows) != 1 || rows[0].Row.Text != long {
		t.Fatalf("queue holds %d rows, want the long message as its one row", len(rows))
	}
	release()

	waitFor(t, func() bool {
		return slices.Equal(promptIDs(t, cs, "c1"), []string{"m-first", "m-long"}) && len(queuedRows(t, cs, "c1")) == 0
	})
}

// A context reference in a row queued behind a compaction is resolved when its turn opens, as a prompt's is.
func TestRun_ARowBehindACompactionResolvesAContextReference(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("remember the milk\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, cs, br, release := compactingHubIn(t, dir)
	const token = "#[[file:notes.txt]]"

	queueTextDuring(t, h, "m-ref", "read "+token)
	release()

	waitFor(t, func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-ref") && !h.coord.turns.live("c1") })
	blocks, _ := br.paramsFor(marotte.MethodPrompt)[marotte.KeyPrompt].([]map[string]any)
	resolved := slices.ContainsFunc(blocks, func(b map[string]any) bool {
		res, ok := b["resource"].(map[string]any)
		text, _ := res["text"].(string)
		return ok && res["uri"] == token && strings.Contains(text, "remember the milk")
	})
	if !resolved {
		t.Errorf("prompt blocks = %v, want a resource block for %s with the file's text", blocks, token)
	}
}

// The compaction after a turn the model refused for the context's size sends no row that turn left waiting: the row
// reads as waiting for a turn after it, and is still waiting once it ends.
func TestRun_AfterARefusedTurnItsCompactionSendsNoRowThatTurnLeft(t *testing.T) {
	h, cs, tails, release := contextLimitCompaction(t)
	view := h.coord.RunView(t.Context(), "c1")
	if len(view.Entries) < 2 || view.Entries[1].ID != "m-old" || view.Entries[1].Wait.After == marotte.WaitCompaction {
		t.Errorf("run during the compaction = %+v, want m-old behind it, waiting for a turn", view.Entries)
	}

	release()

	waitFor(t, func() bool { return tails.Load() == 2 && compactionEntry(t, cs, "c1").ID == "" })
	if ids := promptIDs(t, cs, "c1"); slices.Contains(ids, "m-old") {
		t.Errorf("turns opened = %v, want m-old still waiting", ids)
	}
	if ids, _ := queuedIDs(t, cs, "c1"); !slices.Equal(ids, []string{"m-old"}) {
		t.Errorf("queue = %v, want m-old still waiting", ids)
	}
}

// A message sent during that compaction goes first: the list holds it above the row the refused turn left, it reads
// as going after the compaction, and the two turns open in that order.
func TestRun_AfterARefusedTurnTheMessageSentDuringItsCompactionGoesFirst(t *testing.T) {
	h, cs, _, release := contextLimitCompaction(t)

	queueTextDuring(t, h, "m-new", "sent during the compaction")

	if ids, _ := queuedIDs(t, cs, "c1"); !slices.Equal(ids, []string{"m-new", "m-old"}) {
		t.Errorf("queue during the compaction = %v, want [m-new m-old], the order they are sent in", ids)
	}
	if got := runWait(t, cs, "c1", "m-new"); got != "compaction" {
		t.Errorf("m-new waits for %s, want the compaction", got)
	}

	release()

	waitFor(t, func() bool {
		return slices.Contains(promptIDs(t, cs, "c1"), "m-old") && len(queuedRows(t, cs, "c1")) == 0 && !h.coord.turns.live("c1")
	})
	ids := promptIDs(t, cs, "c1")
	if n, o := slices.Index(ids, "m-new"), slices.Index(ids, "m-old"); n < 0 || n > o {
		t.Errorf("turns opened = %v, want m-new before m-old", ids)
	}
}

// compactionSends reads the chat's header: whether its compaction runs, and whether the first row behind it reads as
// going when it ends.
func compactionSends(t *testing.T, cs *testChatStore, chatID marotte.ChatID) (running, sends bool) {
	t.Helper()
	c, ok := cs.Get(t.Context(), chatID)
	if !ok {
		return false, false
	}
	seen := false
	for _, e := range c.Header().Run.Entries {
		switch {
		case e.Kind == marotte.RunKindCompaction:
			running, seen = e.State == marotte.RunRunning, true
		case seen && e.Row != nil && !e.State.Live():
			return running, e.Wait.After == marotte.WaitCompaction
		}
	}
	return running, false
}

// A steer-facts read the list has already passed (an older sequence, read before lc.mu) changes nothing: its unsent
// count of zero would let the taker drop the unread entry whose steer is still unsent. Sequence 0 is the process's
// first facts state, so it is older than 1.
func TestRunFactsChanged_AnOlderReadKeepsTheUnsentCount(t *testing.T) {
	for _, tc := range []struct {
		name         string
		newer, older uint64
	}{
		{name: "two then one", newer: 2, older: 1},
		{name: "one then zero", newer: 1, older: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, cs, br := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			seedChat(t, cs, "c1")
			h.insertBridgeNow("c1", testBridge(br, bridgeReady))
			lc, ok := h.coord.turns.lookup("c1")
			if !ok {
				t.Fatal("setup: the chat has no lifecycle")
			}
			var (
				mu    sync.Mutex
				ends  int
				keys  []string
				facts uint64
			)
			h.coord.steerFacts = func(marotte.ChatID, bool) (int, []string, uint64) {
				mu.Lock()
				defer mu.Unlock()
				return ends, keys, facts
			}
			read := func(n int, unsent []string, seq uint64) {
				mu.Lock()
				ends, keys, facts = n, unsent, seq
				mu.Unlock()
				h.coord.runFactsChanged("c1")
			}

			read(0, []string{"steer-a"}, tc.newer)
			read(1, nil, tc.older)

			lc.mu.Lock()
			got := lc.orderFactsLocked().unsent()
			lc.mu.Unlock()
			if got != 1 {
				t.Errorf("unsent after an older read = %d, want 1: the newer read's steer is still unsent", got)
			}
		})
	}
}

// A close whose death bits differ from the list's reads the steer facts again with the list's bits, so the unsent
// count is the one those bits yield, not the previous read's.
func TestCloseEndsResolved_AReadWithOtherDeathBitsIsMadeAgain(t *testing.T) {
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	seedChat(t, cs, "c1")
	h.insertBridgeNow("c1", testBridge(br, bridgeReady))
	lc, ok := h.coord.turns.lookup("c1")
	if !ok {
		t.Fatal("setup: the chat has no lifecycle")
	}
	var reads atomic.Int32
	h.coord.steerFacts = func(_ marotte.ChatID, drained bool) (int, []string, uint64) {
		reads.Add(1)
		if drained {
			return 0, nil, 1
		}
		return 0, []string{"steer-a"}, 1
	}

	h.coord.closeEndsResolved(lc, "no-such-turn", command.EndFacts{Death: true})

	lc.mu.Lock()
	got := lc.orderFactsLocked().unsent()
	lc.mu.Unlock()
	if got != 1 {
		t.Errorf("unsent after a close with other death bits = %d, want 1: the list is undrained and steer-a is unsent", got)
	}
	if n := reads.Load(); n != 2 {
		t.Errorf("steer facts reads = %d, want 2: one with the close's bits, one with the list's", n)
	}
}

// However often the list's drained bit moves while the steer facts are read unlocked, the read that finally matches
// it sets the unsent count, so the unread entry for a steer still unsent survives the taker.
func TestSteerFacts_EveryRereadEndsOnAnAppliedRead(t *testing.T) {
	for _, path := range []struct {
		name string
		run  func(h *Runtime, lc *chatLifecycle)
	}{
		{name: "facts changed", run: func(h *Runtime, _ *chatLifecycle) { h.coord.runFactsChanged("c1") }},
		{name: "close", run: func(h *Runtime, lc *chatLifecycle) {
			h.coord.closeEndsResolved(lc, "no-such-turn", command.EndFacts{Death: true})
		}},
	} {
		for _, rereads := range []int{1, 2, 5} {
			t.Run(fmt.Sprintf("%s %d rereads", path.name, rereads), func(t *testing.T) {
				h, cs, br := newTestHub()
				t.Cleanup(func() { shutdownHub(t, h) })
				seedChat(t, cs, "c1")
				h.insertBridgeNow("c1", testBridge(br, bridgeReady))
				lc, ok := h.coord.turns.lookup("c1")
				if !ok {
					t.Fatal("setup: the chat has no lifecycle")
				}
				reads := 0
				h.coord.steerFacts = func(_ marotte.ChatID, drained bool) (int, []string, uint64) {
					reads++
					if reads <= rereads {
						lc.mu.Lock()
						lc.run.drained = !drained
						lc.mu.Unlock()
					}
					return 0, slices.Repeat([]string{"steer"}, reads), uint64(reads)
				}

				path.run(h, lc)

				lc.mu.Lock()
				defer lc.mu.Unlock()
				f := lc.orderFactsLocked()
				if got, want := f.unsent(), rereads+1; got != want {
					t.Errorf("unsent = %d, want %d: the count of the read that matched the list's bits", got, want)
				}
				if reads != rereads+1 {
					t.Errorf("steer facts reads = %d, want %d: one per move of the list's bits, then the applied one", reads, rereads+1)
				}
				lc.run.nextPending(&f)
				if !slices.ContainsFunc(lc.run.entries, func(e *runEntry) bool { return e.kind == marotte.RunKindUnread }) {
					t.Error("no unread entry after the taker ran: the steers are still unsent")
				}
			})
		}
	}
}
