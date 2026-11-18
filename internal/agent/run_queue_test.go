package agent

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

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

// queuedIDs is queuedRows' ids.
func queuedIDs(t *testing.T, cs *testChatStore, chatID marotte.ChatID) []string {
	t.Helper()
	var ids []string
	for _, r := range queuedRows(t, cs, chatID) {
		ids = append(ids, r.ID)
	}
	return ids
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
	h := cs.runtime.Load()
	lc, ok := h.coord.turns.lookup(chatID)
	if !ok {
		return
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	// The live entries and the steers kiro-cli holds stay: a seed on a running chat queues behind what runs, as a
	// queued send would.
	lc.run.entries = slices.DeleteFunc(lc.run.entries, func(e *runEntry) bool { return !e.live() && !e.inFlight() })
	for i := range rows {
		lc.run.entries = append(lc.run.entries, &runEntry{
			id: rows[i].ID, kind: marotte.RunKindRow, state: marotte.RunPaused, origin: marotte.RunFromQueue, row: &rows[i],
		})
	}
	lc.run.touch()
	lc.wakeLocked()
}

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

// busyHub is a chat whose kiro-cli runs with no turn here (a run entry heads its list) until release reports it idle,
// so every send waits on the list behind it.
func busyHub(t *testing.T) (h *Runtime, cs *testChatStore, release func()) {
	t.Helper()
	h, cs, _, release = busyHubIn(t, "/tmp/work")
	return h, cs, release
}

func busyHubIn(t *testing.T, workDir string) (h *Runtime, cs *testChatStore, br *fakeBridge, release func()) {
	t.Helper()
	h, cs, br, _ = idleAfterFirstTurn(t, workDir)
	h.coord.WireTurnActivity("c1", true)
	waitFor(t, func() bool { return slices.Equal(runStates(t, cs, "c1"), []string{"run running"}) })
	return h, cs, br, func() { h.coord.WireTurnActivity("c1", false) }
}

func queueDuring(t *testing.T, h *Runtime, ids ...string) {
	t.Helper()
	for _, id := range ids {
		queueTextDuring(t, h, id, "queued behind the turn")
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

func TestRun_AQueuedRowOpensItsTurnAfterACleanClose(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// Messages queued while kiro-cli runs with no turn here open no turn under it, then go after it, one per turn in the
// order they were queued.
func TestRun_RowsQueuedDuringKiroCLIsRunGoAfterItOneATurn(t *testing.T) {
	h, cs, release := busyHub(t)
	queueDuring(t, h, "m-q1", "m-q2")
	if ids := promptIDs(t, cs, "c1"); !slices.Equal(ids, []string{"m-first"}) {
		t.Fatalf("turns opened while kiro-cli ran = %v, want none", ids)
	}

	release()

	waitFor(t, func() bool {
		return slices.Equal(promptIDs(t, cs, "c1"), []string{"m-first", "m-q1", "m-q2"}) &&
			len(queuedRows(t, cs, "c1")) == 0 && !h.coord.turns.live("c1")
	})
}

// A prompt that reaches the server while kiro-cli runs with no turn here joins the list behind it and answers queued.
func TestRun_APromptDuringKiroCLIsRunIsQueuedBehindIt(t *testing.T) {
	h, cs, release := busyHub(t)

	rec := postCmd(t, h, promptCmd("m-p1", "what changed?"))
	if got := ackAs(t, rec); got != "queued" {
		t.Errorf("prompt during kiro-cli's run answered %s, want queued", got)
	}
	if rows := queuedRows(t, cs, "c1"); len(rows) != 1 || rows[0].ID != "m-p1" || rows[0].Row.Text != "what changed?" {
		t.Errorf("queue = %+v, want the prompt as its one row", rows)
	}

	release()

	waitFor(t, func() bool {
		return slices.Equal(promptIDs(t, cs, "c1"), []string{"m-first", "m-p1"}) && len(queuedRows(t, cs, "c1")) == 0
	})
}

// Send now on a row waiting behind kiro-cli's run opens no turn under it, and the row goes first once the run ends.
func TestRun_SendNowDuringKiroCLIsRunSendsTheRowFirstAfterIt(t *testing.T) {
	h, cs, release := busyHub(t)
	queueDuring(t, h, "m-q1", "m-q2")

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdSendQueuedNow, ChatID: "c1", Payload: json.RawMessage(`{"message_id":"m-q2"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("send_queued_now during kiro-cli's run = %d %s", rec.Code, rec.Body.String())
	}
	if ids := promptIDs(t, cs, "c1"); !slices.Equal(ids, []string{"m-first"}) {
		t.Errorf("turns opened while kiro-cli ran = %v, want none", ids)
	}

	release()

	waitFor(t, func() bool {
		return slices.Equal(promptIDs(t, cs, "c1"), []string{"m-first", "m-q2", "m-q1"}) && len(queuedRows(t, cs, "c1")) == 0
	})
}

// A message past the draft cap sent during kiro-cli's run is queued behind it and goes when the run ends.
func TestRun_ARowPastTheDraftCapQueuesBehindKiroCLIsRun(t *testing.T) {
	h, cs, release := busyHub(t)
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

// A context reference in a row queued behind kiro-cli's run is resolved when its turn opens, as a prompt's is.
func TestRun_ARowBehindKiroCLIsRunResolvesAContextReference(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("remember the milk\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, cs, br, release := busyHubIn(t, dir)
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

// A steer-facts read the list has already passed (an older sequence, read before lc.mu) changes nothing: its count
// of no end owed would let a held close's take go while an end is still owed. Sequence 0 is the process's first facts
// state, so it is older than 1.
func TestRunFactsChanged_AnOlderReadKeepsTheEndOwed(t *testing.T) {
	t.Parallel()
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
				facts uint64
			)
			h.coord.steerFacts = func(marotte.ChatID) (int, uint64) {
				mu.Lock()
				defer mu.Unlock()
				return ends, facts
			}
			read := func(n int, seq uint64) {
				mu.Lock()
				ends, facts = n, seq
				mu.Unlock()
				h.coord.runFactsChanged("c1")
			}

			read(1, tc.newer)
			read(0, tc.older)

			lc.mu.Lock()
			owed := lc.run.endsOwed
			lc.mu.Unlock()
			if !owed {
				t.Error("no end owed after an older read: the newer read's end is still owed")
			}
		})
	}
}
