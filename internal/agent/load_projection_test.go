package agent

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/sse"
)

func replayUpdate(t *testing.T, kind marotte.ACPUpdateKind, text, sub string) json.RawMessage {
	t.Helper()
	kiro := map[string]any{"replay": true}
	if sub != "" {
		kiro["kind"] = sub
	}
	u := map[string]any{
		"sessionUpdate": string(kind),
		"_meta":         map[string]any{"kiro": kiro},
	}
	if text != "" {
		u["content"] = map[string]any{"type": "text", "text": text}
		kiro["messageId"] = "id-" + text
		kiro["timestamp"] = "2026-08-02T20:01:00.000Z"
	}
	raw, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

type settleRecorder struct {
	// err is the swap's outcome the sink reports.
	err   error
	calls int
	turns int
}

func (r *settleRecorder) sink() func(marotte.ChatID, *loadProjection) error {
	return func(_ marotte.ChatID, lp *loadProjection) error {
		r.calls++
		r.turns = len(lp.proj.Turns())
		return r.err
	}
}

// noChats is a bare replay's store: no revert to snapshot and no swap.
type noChats struct{}

func (noChats) NewestRevert(context.Context, marotte.ChatID) (string, bool) { return "", false }

func (noChats) Reconcile(context.Context, marotte.ChatID, func(*chat.EntryLog, chat.EntryHeader) (bool, error)) (string, bool, error) {
	panic("a bare replay swaps nothing")
}

type revertingChats struct{ newest string }

func (c *revertingChats) NewestRevert(context.Context, marotte.ChatID) (string, bool) {
	return c.newest, c.newest != ""
}

func (*revertingChats) Reconcile(context.Context, marotte.ChatID, func(*chat.EntryLog, chat.EntryHeader) (bool, error)) (string, bool, error) {
	panic("a bare replay swaps nothing")
}

func feedOneTurn(t *testing.T, rp *replay, chatID marotte.ChatID) {
	t.Helper()
	for _, f := range []struct {
		kind marotte.ACPUpdateKind
		text string
		sub  string
	}{
		{"user_message_chunk", "ONE", ""},
		{marotte.ACPUpdateSessionInfo, "", "turn_start"},
		{marotte.ACPUpdateAgentChunk, "reply", ""},
		{marotte.ACPUpdateSessionInfo, "", "turn_end"},
	} {
		if !rp.ingestReplayFrame(chatID, f.kind, replayUpdate(t, f.kind, f.text, f.sub)) {
			t.Fatalf("frame %v/%s was not consumed by a projection", f.kind, f.sub)
		}
	}
}

// feedOneTurn's four frames precede the load result, so the load answers at position 4, on
// the forward goroutine's first attachment (1).
const (
	testFwdGen  uint64 = 1
	testLoadSeq uint64 = 4
)

// atFrame is the observation Forward reports after folding the frame at seq.
func atFrame(seq uint64) drainPoint { return drainPoint{gen: testFwdGen, seq: seq} }

func atLoad() drainPoint { return drainPoint{gen: testFwdGen, seq: testLoadSeq} }

// atExit is the bridge-exit seal: an attachment and no position.
func atExit() drainPoint { return drainPoint{gen: testFwdGen} }

// TestReplayProjection_SettleBarrier pins both halves of the race guard: replay frames are
// pushed before Start returns, but notifCh is buffered (256), so Forward may not have folded them.
func TestReplayProjection_SettleBarrier(t *testing.T) {
	const chatID marotte.ChatID = "c1"

	t.Run("no settle before the load returns", func(t *testing.T) {
		rp, rec := replayWithRecorder()
		rp.OpenReplayProjection(t.Context(), chatID, "old-acp", nil)
		feedOneTurn(t, rp, chatID)

		rp.SettleReplayProjection(chatID, atLoad(), false)
		if rec.calls != 0 {
			t.Errorf("settled %d times before the load returned, want 0", rec.calls)
		}
		if !rp.hasProjection(chatID) {
			t.Error("projection was dropped before the load returned")
		}
	})

	t.Run("no settle while the consumer is behind the load position", func(t *testing.T) {
		rp, rec := replayWithRecorder()
		rp.OpenReplayProjection(t.Context(), chatID, "old-acp", nil)
		feedOneTurn(t, rp, chatID)
		rp.MarkReplayLoadedAt(chatID, atLoad())

		// Folded only up to the frame before the result: undrained.
		rp.SettleReplayProjection(chatID, atFrame(testLoadSeq-1), false)
		if rec.calls != 0 {
			t.Errorf("settled %d times one frame short of the load position, want 0", rec.calls)
		}
		if !rp.hasProjection(chatID) {
			t.Error("projection was dropped while the consumer was still behind")
		}
	})

	t.Run("settles once the consumer reaches the load position", func(t *testing.T) {
		rp, rec := replayWithRecorder()
		rp.OpenReplayProjection(t.Context(), chatID, "old-acp", nil)
		feedOneTurn(t, rp, chatID)
		rp.MarkReplayLoadedAt(chatID, atLoad())

		rp.SettleReplayProjection(chatID, atFrame(testLoadSeq), false)
		if rec.calls != 1 {
			t.Fatalf("settled %d times, want exactly 1", rec.calls)
		}
		if rec.turns != 1 {
			t.Errorf("projected %d turns, want 1", rec.turns)
		}
		if rp.hasProjection(chatID) {
			t.Error("projection outlived its settle")
		}
	})

	t.Run("a frame PAST the load position does not delay the settle", func(t *testing.T) {
		rp, rec := replayWithRecorder()
		rp.OpenReplayProjection(t.Context(), chatID, "old-acp", nil)
		feedOneTurn(t, rp, chatID)
		rp.MarkReplayLoadedAt(chatID, atLoad())

		// A post-result catalog frame has a higher position, which satisfies the condition.
		rp.SettleReplayProjection(chatID, atFrame(testLoadSeq+3), false)
		if rec.calls != 1 {
			t.Errorf("settled %d times past the load position, want 1", rec.calls)
		}
	})

	t.Run("settle is idempotent", func(t *testing.T) {
		rp, rec := replayWithRecorder()
		rp.OpenReplayProjection(t.Context(), chatID, "old-acp", nil)
		feedOneTurn(t, rp, chatID)
		rp.MarkReplayLoadedAt(chatID, atLoad())

		// Forward calls this after every frame; a repeat must not re-swap.
		for range 4 {
			rp.SettleReplayProjection(chatID, atFrame(testLoadSeq), false)
		}
		if rec.calls != 1 {
			t.Errorf("settled %d times, want 1: Forward calls settle per frame", rec.calls)
		}
	})

	t.Run("the seal settles despite an unreached position", func(t *testing.T) {
		rp, rec := replayWithRecorder()
		rp.OpenReplayProjection(t.Context(), chatID, "old-acp", nil)
		feedOneTurn(t, rp, chatID)
		rp.MarkReplayLoadedAt(chatID, atLoad())

		// The bridge-exit seal must complete the projection rather than leak it.
		rp.SettleReplayProjection(chatID, atExit(), true)
		if rec.calls != 1 {
			t.Errorf("sealed settle ran %d times, want 1", rec.calls)
		}
	})

	t.Run("the seal still requires the load to have returned", func(t *testing.T) {
		rp, rec := replayWithRecorder()
		rp.OpenReplayProjection(t.Context(), chatID, "old-acp", nil)
		feedOneTurn(t, rp, chatID)

		// A bridge dying before session/load returned has nothing to adopt.
		rp.SettleReplayProjection(chatID, atExit(), true)
		if rec.calls != 0 {
			t.Errorf("sealed settle ran %d times on a load that never returned, want 0", rec.calls)
		}
	})

	t.Run("a straggler from a previous attachment settles nothing", func(t *testing.T) {
		rp, rec := replayWithRecorder()
		rp.OpenReplayProjection(t.Context(), chatID, "old-acp", nil)
		feedOneTurn(t, rp, chatID)
		// This chat's load ran on attachment 2, the reload's forward.
		const reload = testFwdGen + 1
		rp.MarkReplayLoadedAt(chatID, drainPoint{gen: reload, seq: testLoadSeq})

		// The previous bridge's forward is still draining with far-ahead positions; adopting them settles on frame one.
		rp.SettleReplayProjection(chatID, drainPoint{gen: testFwdGen, seq: 900}, false)
		if rec.calls != 0 {
			t.Errorf("a straggling observation from attachment %d settled the replay "+
				"loaded on attachment %d %d times, want 0", testFwdGen, reload, rec.calls)
		}
		if !rp.hasProjection(chatID) {
			t.Fatal("the straggler dropped the projection")
		}

		// Nor stored: a stored 900 would settle on the live attachment's first frame.
		rp.SettleReplayProjection(chatID, drainPoint{gen: reload, seq: 1}, false)
		if rec.calls != 0 {
			t.Errorf("settled %d times on the live attachment's FIRST frame, want 0 — "+
				"the straggler's position was adopted", rec.calls)
		}

		// Its own attachment reaching the load position settles it.
		rp.SettleReplayProjection(chatID, drainPoint{gen: reload, seq: testLoadSeq}, false)
		if rec.calls != 1 {
			t.Errorf("settled %d times once its own attachment caught up, want 1", rec.calls)
		}
	})

	t.Run("a NEW attachment invalidates the load position", func(t *testing.T) {
		rp, rec := replayWithRecorder()
		rp.OpenReplayProjection(t.Context(), chatID, "old-acp", nil)
		feedOneTurn(t, rp, chatID)
		rp.MarkReplayLoadedAt(chatID, atLoad())

		// A second attachment restarts at zero; its positions must not satisfy the old bound.
		rp.SettleReplayProjection(chatID, drainPoint{gen: testFwdGen + 1, seq: 1}, false)
		if rec.calls != 0 {
			t.Errorf("settled %d times on a fresh attachment's first frame, want 0", rec.calls)
		}
	})
}

// TestReplayProjection_ADrainedReplaySettlesWhenTheLoadReturns pins that a replay drained before the RPC
// returned has no later frame to settle it, so the load's return must.
func TestReplayProjection_ADrainedReplaySettlesWhenTheLoadReturns(t *testing.T) {
	const chatID marotte.ChatID = "c1"
	rp, rec := replayWithRecorder()
	rp.OpenReplayProjection(t.Context(), chatID, "old-acp", nil)
	feedOneTurn(t, rp, chatID)

	// The ordinary short transcript: drained while the RPC is in flight.
	rp.SettleReplayProjection(chatID, atFrame(testLoadSeq), false)
	if rec.calls != 0 {
		t.Fatalf("settled %d times before the load returned, want 0", rec.calls)
	}

	rp.MarkReplayLoadedAt(chatID, atLoad())

	if rec.calls != 1 {
		t.Fatalf("recording the load position settled %d times, want 1 — a replay "+
			"already folded has no frame left to trigger it and no caller can wait "+
			"for the bridge to die", rec.calls)
	}
	if rec.turns != 1 {
		t.Errorf("projected %d turns, want 1", rec.turns)
	}
	if rp.hasProjection(chatID) {
		t.Error("the projection is still open after the load returned on a drained replay")
	}
}

// TestReplayProjection_DiscardOnFailedLoad pins that a surviving projection would let the fallback session/new adopt a partial transcript.
func TestReplayProjection_DiscardOnFailedLoad(t *testing.T) {
	const chatID marotte.ChatID = "c1"
	rp, rec := replayWithRecorder()
	rp.OpenReplayProjection(t.Context(), chatID, "old-acp", nil)
	feedOneTurn(t, rp, chatID)

	rp.DiscardReplayProjection(chatID)
	if rp.hasProjection(chatID) {
		t.Error("projection survived a discard")
	}
	// Even the condition holding afterwards must not resurrect it.
	rp.MarkReplayLoadedAt(chatID, atLoad())
	rp.SettleReplayProjection(chatID, atExit(), true)
	if rec.calls != 0 {
		t.Errorf("discarded projection settled %d times, want 0", rec.calls)
	}
}

// TestReplayProjection_FrameWithNoLoadIsRejected pins that a replay frame with no load in flight belongs nowhere.
func TestReplayProjection_FrameWithNoLoadIsRejected(t *testing.T) {
	rp, _ := replayWithRecorder()
	if rp.ingestReplayFrame("nobody", marotte.ACPUpdateAgentChunk,
		replayUpdate(t, marotte.ACPUpdateAgentChunk, "stray", "")) {
		t.Error("a replay frame was consumed with no projection open")
	}
}

// TestReplayProjection_ReloadSupersedes pins that a second load starts clean.
func TestReplayProjection_ReloadSupersedes(t *testing.T) {
	const chatID marotte.ChatID = "c1"
	rp, rec := replayWithRecorder()

	rp.OpenReplayProjection(t.Context(), chatID, "old-acp", nil)
	feedOneTurn(t, rp, chatID)
	rp.OpenReplayProjection(t.Context(), chatID, "old-acp", nil) // re-load
	feedOneTurn(t, rp, chatID)
	rp.MarkReplayLoadedAt(chatID, atLoad())
	rp.SettleReplayProjection(chatID, atFrame(testLoadSeq), false)

	if rec.calls != 1 {
		t.Fatalf("settled %d times, want 1", rec.calls)
	}
	if rec.turns != 1 {
		t.Errorf("projected %d turns, want 1: the first load's frames leaked in", rec.turns)
	}
}

// TestReplayProjection_OneProjectionPerChat pins that swapMerged's revert gate is sound only while a chat
// holds at most one projection and a settle consumes it (NewestRevert is file order, which a rewrite can reorder).
func TestReplayProjection_OneProjectionPerChat(t *testing.T) {
	const chatID marotte.ChatID = "c1"

	t.Run("a re-load supersedes and re-reads the provenance", func(t *testing.T) {
		chats := &revertingChats{}
		rp := &replay{chats: chats, projections: map[marotte.ChatID]*loadProjection{}}

		rp.OpenReplayProjection(t.Context(), chatID, "old-acp", nil)
		feedOneTurn(t, rp, chatID)

		// A rewind lands between the failed load and the fallback re-load.
		chats.newest = "t-2:revert"
		rp.OpenReplayProjection(t.Context(), chatID, "old-acp", nil)

		snapshot, open := rp.projectionSnapshot(chatID)
		if open != 1 {
			t.Fatalf("the replay holds %d projections for one chat after a re-load, want 1", open)
		}
		if snapshot != "t-2:revert" {
			t.Errorf("the open projection's snapshot = %q, want %q: the re-load kept the "+
				"FIRST load's provenance, so SwapMerged compares the log's newest revert "+
				"against a stale id, passes its gate, and hands the rewound turns back",
				snapshot, "t-2:revert")
		}
	})

	t.Run("a settle consumes the projection", func(t *testing.T) {
		rp, rec := replayWithRecorder()
		rp.OpenReplayProjection(t.Context(), chatID, "old-acp", nil)
		feedOneTurn(t, rp, chatID)
		rp.MarkReplayLoadedAt(chatID, atLoad())
		rp.SettleReplayProjection(chatID, atFrame(testLoadSeq), false)
		if rec.calls != 1 {
			t.Fatalf("the settle ran %d times, want 1", rec.calls)
		}

		// Later triggers claim nothing: the claim took it.
		rp.SettleReplayProjection(chatID, atFrame(testLoadSeq), false)
		rp.SettleReplayProjection(chatID, atExit(), true)
		rp.MarkReplayLoadedAt(chatID, atLoad())
		if rec.calls != 1 {
			t.Errorf("the projection settled %d times in total, want 1: the claim did not "+
				"consume it, so a later trigger swaps the same projection again", rec.calls)
		}
		if _, open := rp.projectionSnapshot(chatID); open != 0 {
			t.Errorf("the replay holds %d projections after the settle, want 0", open)
		}
	})
}

// replayWithRecorder builds a bare replay: the lifecycle touches only that type's fields.
func replayWithRecorder() (*replay, *settleRecorder) {
	rec := &settleRecorder{}
	rp := &replay{chats: noChats{}, projections: map[marotte.ChatID]*loadProjection{}}
	rp.onProjection = rec.sink()
	return rp, rec
}

func (rp *replay) hasProjection(chatID marotte.ChatID) bool {
	rp.projMu.Lock()
	defer rp.projMu.Unlock()
	_, ok := rp.projections[chatID]
	return ok
}

func (rp *replay) projectionSnapshot(chatID marotte.ChatID) (string, int) {
	rp.projMu.Lock()
	defer rp.projMu.Unlock()
	lp := rp.projections[chatID]
	if lp == nil {
		return "", len(rp.projections)
	}
	return lp.snapshot, len(rp.projections)
}

func replayNotif(t *testing.T, kind marotte.ACPUpdateKind, text, sub string) *marotte.RPCResponse {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"sessionId": "old-acp",
		"update":    replayUpdate(t, kind, text, sub),
	})
	if err != nil {
		t.Fatalf("marshal notification: %v", err)
	}
	return &marotte.RPCResponse{Method: marotte.MethodSessionUpdate, Params: raw}
}

func loadedChat(t *testing.T, cs *testChatStore, chatID marotte.ChatID) {
	t.Helper()
	if _, err := cs.Mutate(t.Context(), chatID, func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.RecordSession("old-acp")
		return true
	}); err != nil {
		t.Fatalf("seed the chat: %v", err)
	}
}

// Never widen it to fix a failure: an early settle drops every later frame. A one-message
// transcript is that bug; an empty one is a stuck Forward.
const awaitPatience = 20 * time.Second

func awaitReplayedTurn(t *testing.T, cs *testChatStore, chatID marotte.ChatID, want string) {
	t.Helper()
	stop := time.Now().Add(awaitPatience)
	for {
		entries, err := cs.All(t.Context(), chatID)
		if err != nil {
			t.Fatalf("All(%q): %v", chatID, err)
		}
		got := textsOf(t, entries)
		if slices.Contains(got, want) {
			return
		}
		if time.Now().After(stop) {
			t.Fatalf("the replayed turn %q never reached the chat's log within %v; a "+
				"resumed chat shows an empty history instead of the conversation KAS replayed. "+
				"The log holds %d entries with texts %q (a short transcript means a projection "+
				"settled before the replay finished and the rest was dropped)",
				want, awaitPatience, len(entries), got)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestSessionLoad_AdoptsTheReplayedTranscript is the load path end to end: the projection opens
// before Forward attaches, and the load's return is recorded.
func TestSessionLoad_AdoptsTheReplayedTranscript(t *testing.T) {
	// A fresh bridge per spawn, so the rehydrate sweep's utility bridge does not drain this replay.
	cs := newTestChatStore()
	h := New(context.Background(), t.TempDir(), func() ACPBridge {
		b := newFakeBridge()
		b.notifsOnStart = []*marotte.RPCResponse{
			replayNotif(t, "user_message_chunk", "ONE", ""),
			replayNotif(t, marotte.ACPUpdateSessionInfo, "", "turn_start"),
			replayNotif(t, marotte.ACPUpdateAgentChunk, "reply", ""),
			replayNotif(t, marotte.ACPUpdateSessionInfo, "", "turn_end"),
		}
		return b
	}, cs)
	cs.wire(h)
	const chatID marotte.ChatID = "c1"
	loadedChat(t, cs, chatID)

	// The replay arrives inside this call, as KAS delivers it inside session/load.
	sb, err := h.coord.openBridge(t.Context(), chatID, "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	br, ok := sb.bridge.(*fakeBridge)
	if !ok {
		t.Fatalf("the chat's bridge is %T, want the fake", sb.bridge)
	}
	if opts := br.lastStartOpts(); opts == nil || opts.SessionID == "" {
		// Without a named session the fake replays nothing, so fail as invalid.
		t.Fatalf("the chat's bridge was started with StartOpts %+v, want one naming "+
			"the stored ACP session so the fake replays a transcript", opts)
	}

	// The bridge exit is the backstop settle.
	br.Stop()

	awaitReplayedTurn(t, cs, chatID, "reply")
}

// TestForward_ReportsEachFramesOwnPosition pins that Forward reports each frame's own Seq; the
// load position is recorded first and the bridge never stopped, so no other door settles it.
func TestForward_ReportsEachFramesOwnPosition(t *testing.T) {
	h, cs, br := newTestHub()
	const chatID marotte.ChatID = "c1"
	loadedChat(t, cs, chatID)

	frames := []*marotte.RPCResponse{
		replayNotif(t, "user_message_chunk", "ONE", ""),
		replayNotif(t, marotte.ACPUpdateSessionInfo, "", "turn_start"),
		replayNotif(t, marotte.ACPUpdateAgentChunk, "reply", ""),
		replayNotif(t, marotte.ACPUpdateSessionInfo, "", "turn_end"),
	}

	h.replay.OpenReplayProjection(t.Context(), chatID, "old-acp", nil)
	gen := h.coord.turns.attachForward(chatID)
	go h.coord.forwardAt(chatID, br, gen)

	// The load answered at the last frame's position, before anything was folded.
	h.replay.MarkReplayLoadedAt(chatID, drainPoint{gen: gen, seq: uint64(len(frames))})
	if !h.replay.hasProjection(chatID) {
		t.Fatal("the replay settled with nothing folded, so this test cannot tell " +
			"a per-frame settle from an unconditional one")
	}

	for _, f := range frames {
		br.deliver(f)
	}

	awaitReplayedTurn(t, cs, chatID, "reply")
	if br.isStopped() {
		t.Error("the bridge was stopped, so the seal could have settled this instead " +
			"of the frames' own positions")
	}
}

// TestForwardExit_SettlesALoadWhoseTrailingFramesNeverCame pins the seal at Forward's exit,
// or the chat resumes empty and the rebuild leaks.
func TestForwardExit_SettlesALoadWhoseTrailingFramesNeverCame(t *testing.T) {
	h, cs, br := newTestHub()
	const chatID marotte.ChatID = "c1"
	loadedChat(t, cs, chatID)

	// Folded without being consumed off a channel, so the position stays short.
	h.replay.OpenReplayProjection(t.Context(), chatID, "old-acp", nil)
	feedOneTurn(t, h.replay, chatID)
	h.replay.MarkReplayLoadedAt(chatID, atLoad())

	br.Stop() // the bridge exits with nothing further to deliver
	h.coord.Forward(chatID, br)

	awaitReplayedTurn(t, cs, chatID, "reply")
	// The rebuild is released.
	if h.replay.ingestReplayFrame(chatID, marotte.ACPUpdateAgentChunk,
		replayUpdate(t, marotte.ACPUpdateAgentChunk, "late", "")) {
		t.Error("a projection was still open after the bridge exited, so every later " +
			"replay frame folds into a transcript nothing will settle")
	}
}

// TestReplayProjection_ConcurrentLoadsAreIndependent pins per-chat keying: a restart with several
// tabs loads several chats at once.
func TestReplayProjection_ConcurrentLoadsAreIndependent(t *testing.T) {
	rp, rec := replayWithRecorder()
	const first marotte.ChatID = "c1"
	const second marotte.ChatID = "c2"

	rp.OpenReplayProjection(t.Context(), first, "old-acp", nil)
	feedOneTurn(t, rp, first)
	rp.MarkReplayLoadedAt(first, atLoad())

	// The second spawn while the first is in flight.
	rp.OpenReplayProjection(t.Context(), second, "old-acp", nil)
	if !rp.hasProjection(first) {
		t.Fatal("opening a second chat's load dropped the first chat's rebuild, so that " +
			"chat resumes with an empty transcript")
	}
	feedOneTurn(t, rp, second)

	rp.SettleReplayProjection(first, atFrame(testLoadSeq), false)
	if rec.calls != 1 {
		t.Fatalf("the first chat settled %d times, want 1", rec.calls)
	}
	if rec.turns != 1 {
		t.Errorf("the first chat projected %d turns, want 1", rec.turns)
	}
	if !rp.hasProjection(second) {
		t.Error("settling the first chat dropped the second chat's rebuild")
	}
}

// TestReplayProjection_SettleReportsFramesAgainstTurns pins the settle's frame and turn counts:
// many frames and zero turns is a decoding bug nothing else reports.
func TestReplayProjection_SettleReportsFramesAgainstTurns(t *testing.T) {
	logs := captureLogs(t)
	rp, rec := replayWithRecorder()
	const chatID marotte.ChatID = "c1"

	rp.OpenReplayProjection(t.Context(), chatID, "old-acp", nil)
	feedOneTurn(t, rp, chatID) // four frames: user, turn_start, reply, turn_end
	rp.MarkReplayLoadedAt(chatID, atLoad())
	rp.SettleReplayProjection(chatID, atFrame(testLoadSeq), false)

	out := logs.String()
	if !strings.Contains(out, `"msg":"replay projection settled"`) {
		t.Fatalf("a completed settle said nothing: %s", out)
	}
	if !strings.Contains(out, `"frames":4`) {
		t.Errorf("the settle line does not report the 4 frames it ingested: %s", out)
	}
	if rec.turns != 1 {
		t.Errorf("the settle handed the swap %d turns, want 1", rec.turns)
	}
}

func projectedTurn(t *testing.T, h *Runtime) *loadProjection {
	t.Helper()
	lp := &loadProjection{
		proj:      translate.NewEntryProjection(newMessageID, h.replay.workDir),
		sessionID: "old-acp",
	}
	for _, f := range []struct {
		kind marotte.ACPUpdateKind
		text string
		sub  string
	}{
		{"user_message_chunk", "ONE", ""},
		{marotte.ACPUpdateSessionInfo, "", "turn_start"},
		{marotte.ACPUpdateAgentChunk, "reply", ""},
		{marotte.ACPUpdateSessionInfo, "", "turn_end"},
	} {
		lp.proj.Ingest(f.kind, replayUpdate(t, f.kind, f.text, f.sub))
	}
	return lp
}

func eventFor(t *testing.T, events []sse.ReplayEvent, want marotte.EventType) (marotte.ServerEvent, bool) {
	t.Helper()
	for _, e := range events {
		var ev marotte.ServerEvent
		if err := json.Unmarshal(e.Event.Data, &ev); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		if ev.Type == want {
			return ev, true
		}
	}
	return marotte.ServerEvent{}, false
}

// TestSwapProjectedTranscript_AnnouncesTheReplacement pins the refetch with the rewrite's chat stamp.
func TestSwapProjectedTranscript_AnnouncesTheReplacement(t *testing.T) {
	h, cs, _ := newTestHub()
	const chatID marotte.ChatID = "c1"
	loadedChat(t, cs, chatID)
	before := bufferedSince(h, 0)
	if len(before) == 0 {
		t.Fatal("seeding the chat broadcast nothing, so there is no id to measure from")
	}

	h.replay.swapProjectedTranscript(chatID, projectedTurn(t, h))

	if got := textsOf(t, logOf(t, cs, chatID)); !slices.Contains(got, "reply") {
		t.Fatalf("the merged log holds texts %q, want the replayed reply", got)
	}
	events := bufferedSince(h, before[len(before)-1].Offset)
	ev, ok := eventFor(t, events, marotte.EventSubjectChanged)
	if !ok {
		t.Fatalf("the swap broadcast %v and never told the client to refetch", extractTypes(t, events))
	}
	if ev.ChatID != chatID {
		t.Errorf("the instruction names chat %q, want %q; a workspace-global frame reaches no chat's handler", ev.ChatID, chatID)
	}
	if ev.Subject == nil {
		t.Fatal("the fetch instruction carries no subject stamp, so the client's version map cannot record which version the refetch commits at")
	}
	if ev.Subject.Kind != string(subject.KindChat) || ev.Subject.Ref != string(chatID) || ev.Subject.Version == "" {
		t.Errorf("subject = %s:%s@%q, want %s:%s with the rewrite's version", ev.Subject.Kind, ev.Subject.Ref, ev.Subject.Version, subject.KindChat, chatID)
	}
}

// TestSwapProjectedTranscript_AnnouncesNothingWhenTheSetIsUnchanged pins that no rewrite and no refetch.
func TestSwapProjectedTranscript_AnnouncesNothingWhenTheSetIsUnchanged(t *testing.T) {
	h, cs, _ := newTestHub()
	const chatID marotte.ChatID = "c1"
	loadedChat(t, cs, chatID)
	h.replay.swapProjectedTranscript(chatID, projectedTurn(t, h))
	first := logOf(t, cs, chatID)
	before := bufferedSince(h, 0)

	h.replay.swapProjectedTranscript(chatID, projectedTurn(t, h))

	if got := extractTypes(t, bufferedSince(h, before[len(before)-1].Offset)); slices.Contains(got, string(marotte.EventSubjectChanged)) {
		t.Errorf("a replay that changed nothing broadcast %v; every resumed tab would refetch a transcript nobody edited", got)
	}
	if again := logOf(t, cs, chatID); len(again) != len(first) {
		t.Errorf("the log holds %d entries after an identical rebuild, want %d unchanged", len(again), len(first))
	}
}

// TestSwapProjectedTranscript_WritesOnACancelledLifetime is the restart case: refusing at shutdown loses the turn.
func TestSwapProjectedTranscript_WritesOnACancelledLifetime(t *testing.T) {
	h, cs, _ := newTestHub()
	const chatID marotte.ChatID = "c1"
	loadedChat(t, cs, chatID)

	h.lifecycle.shutdownCancel()

	h.replay.swapProjectedTranscript(chatID, projectedTurn(t, h))

	if got := textsOf(t, logOf(t, cs, chatID)); !slices.Contains(got, "reply") {
		t.Errorf("the merged log holds texts %q, want the replayed reply; the merge was refused at shutdown", got)
	}
}

// TestSwapProjectedTranscript_MergesAChatARewindAlreadyTouched pins the provenance carried from
// open into the swapRequest: an empty snapshot makes every resume of a rewound chat discard its projection.
func TestSwapProjectedTranscript_MergesAChatARewindAlreadyTouched(t *testing.T) {
	h, cs, br := newTestHub()
	const chatID marotte.ChatID = "c1"
	loadedChat(t, cs, chatID)

	// Only a chat already rewound tells the store's answer from the empty one.
	opened, err := cs.OpenTurn(t.Context(), chatID, &chat.TurnSpec{
		Source: marotte.TurnOpenNamePrompt,
		Prompt: &marotte.EntryPrompt{ID: "m-1", Text: "first"},
	}, nil)
	if err != nil {
		t.Fatalf("open the turn the rewind reverts: %v", err)
	}
	if _, _, err := cs.Revert(t.Context(), chatID, opened.Turn, ""); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	revert, held := cs.NewestRevert(t.Context(), chatID)
	if !held || revert == "" {
		t.Fatalf("the fixture's log holds no revert (%q/%v), so an empty snapshot would "+
			"pass the gate too and this test would assert nothing", revert, held)
	}

	// Through the production door, so the snapshot is the store's.
	h.replay.OpenReplayProjection(t.Context(), chatID, "old-acp", nil)
	feedOneTurn(t, h.replay, chatID)
	h.replay.MarkReplayLoadedAt(chatID, atLoad())
	br.Stop()
	h.coord.Forward(chatID, br)

	if got := textsOf(t, logOf(t, cs, chatID)); !slices.Contains(got, "reply") {
		t.Errorf("the merged log holds texts %q, want the replayed reply: the swap "+
			"discarded the projection, so a rewound chat never merges again", got)
	}
	if got, _ := cs.NewestRevert(t.Context(), chatID); got != revert {
		t.Errorf("the log's newest revert is %q after the swap, want %q unchanged: the "+
			"gate's own other input moved, so the assertion above says nothing about "+
			"which value the projection carried", got, revert)
	}

	// The swap's input is the whole file, so the rewrite carries the reverted entries through.
	hidden, reverted := revertedEntriesAfterSwap(t, cs, chatID, opened.Turn)
	if len(hidden) == 0 {
		t.Errorf("the rewritten log holds no entry of the reverted turn %q (reverted set "+
			"%v), want its entries still on disk: the merge read the surviving view, so "+
			"the rewrite compacted the rewind's own history out of the file",
			opened.Turn, slices.Sorted(maps.Keys(reverted)))
	}
}

func revertedEntriesAfterSwap(t *testing.T, cs *testChatStore, chatID marotte.ChatID, turn string) ([]marotte.Entry, map[string]struct{}) {
	t.Helper()
	var (
		held     []marotte.Entry
		reverted map[string]struct{}
	)
	if _, _, err := cs.Reconcile(t.Context(), chatID, func(l *chat.EntryLog, _ chat.EntryHeader) (bool, error) {
		entries, rev, readErr := l.AllWithReverted()
		if readErr != nil {
			return false, readErr
		}
		reverted = rev
		for i := range entries {
			if entries[i].Turn == turn {
				held = append(held, entries[i])
			}
		}
		return false, nil
	}); err != nil {
		t.Fatalf("read the whole log after the swap: %v", err)
	}
	return held, reverted
}
