package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// A load KAS answers with a blank record restores nothing; these pin that marotte tells such a load from a real
// one by its replay, retires the session, and says so in the chat.

const lostSession = "old-acp"

// doorFakes hands every door its own fake, numbered in door order, so a retired session and its replacement are
// told apart by identity.
type doorFakes struct {
	configure func(b *fakeBridge)
	made      []*fakeBridge
	mu        sync.Mutex
}

func (d *doorFakes) next() engineSession {
	d.mu.Lock()
	defer d.mu.Unlock()
	b := newFakeBridge()
	b.sessionID = fmt.Sprintf("sess-%d", len(d.made)+1)
	if d.configure != nil {
		d.configure(b)
	}
	d.made = append(d.made, b)
	return b
}

// doors are the fakes that went through a door, in order, with whether each door was a load.
func (d *doorFakes) doors() (out []*fakeBridge, loads []bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, b := range d.made {
		if opts := b.lastSessionOpts(); opts != nil {
			out = append(out, b)
			loads = append(loads, opts.SessionID != "")
		}
	}
	return out, loads
}

func lostRecordHub(t *testing.T, configure func(b *fakeBridge)) (*Runtime, *testChatStore, *doorFakes) {
	t.Helper()
	cs := newTestChatStore()
	fakes := &doorFakes{configure: configure}
	h := newRuntimeOn(context.Background(), t.TempDir(), fakeEngineOf(fakes.next, nil), cs)
	cs.wire(h)
	settleBoot(t, h)
	t.Cleanup(func() { _ = h.Shutdown(context.Background()) })
	return h, cs, fakes
}

// onSession records the chat as running session.
func onSession(t *testing.T, cs *testChatStore, chatID marotte.ChatID, session string) {
	t.Helper()
	if _, err := cs.Mutate(t.Context(), chatID, func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.RecordSession(session)
		return true
	}); err != nil {
		t.Fatalf("record session %q: %v", session, err)
	}
}

// boundTurn writes one closed prompt turn whose prompt KAS bound on session, as a sent prompt leaves it.
func boundTurn(t *testing.T, cs *testChatStore, chatID marotte.ChatID, session string) string {
	t.Helper()
	ctx := t.Context()
	opened, err := cs.OpenTurn(ctx, chatID, &chat.TurnSpec{
		Source: marotte.TurnOpenNamePrompt,
		Prompt: &marotte.EntryPrompt{ID: "m-" + session, Text: "hello"},
	}, nil)
	if err != nil {
		t.Fatalf("open the bound turn: %v", err)
	}
	appendTo := func(id string, kind marotte.EntryKind, payload any) {
		raw, mErr := json.Marshal(payload)
		if mErr != nil {
			t.Fatal(mErr)
		}
		if aErr := cs.Append(ctx, chatID, &marotte.Entry{ID: id, Turn: opened.Turn, Kind: kind, Payload: raw}); aErr != nil {
			t.Fatalf("append %s: %v", kind, aErr)
		}
	}
	appendTo(opened.Turn+":bind", marotte.EntryKindTurnBind, marotte.EntryTurnBind{KASMessageID: "kas-" + session, SessionID: session})
	appendTo(opened.Turn+":close", marotte.EntryKindTurnClose, marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted})
	return opened.Turn
}

func contextResets(t *testing.T, cs *testChatStore, chatID marotte.ChatID) []marotte.ContextResetCause {
	t.Helper()
	entries, err := cs.All(t.Context(), chatID)
	if err != nil {
		t.Fatalf("All(%q): %v", chatID, err)
	}
	var causes []marotte.ContextResetCause
	for i := range entries {
		if entries[i].Kind != marotte.EntryKindContextReset {
			continue
		}
		var p marotte.EntryContextReset
		if err := json.Unmarshal(entries[i].Payload, &p); err != nil {
			t.Fatalf("decode context_reset: %v", err)
		}
		causes = append(causes, p.Cause)
	}
	return causes
}

func chatRecord(t *testing.T, cs *testChatStore, chatID marotte.ChatID) *marotte.Chat {
	t.Helper()
	c, ok := cs.Get(t.Context(), chatID)
	if !ok {
		t.Fatalf("chat %q is gone", chatID)
	}
	return c
}

func TestSessionLoad_ALoadThatReplaysNothingForItsBoundSessionOpensANewOne(t *testing.T) {
	h, cs, fakes := lostRecordHub(t, nil)
	const chatID marotte.ChatID = "c1"
	onSession(t, cs, chatID, lostSession)
	boundTurn(t, cs, chatID, lostSession)

	sb, err := h.coord.openReleased(t.Context(), chatID, "")
	if err != nil {
		t.Fatalf("openBridge: %v", err)
	}

	doors, loads := fakes.doors()
	if len(doors) != 2 || !loads[0] || loads[1] {
		t.Fatalf("doors = %d (loads %v), want the load then one session/new", len(doors), loads)
	}
	if !doors[0].isClosed() {
		t.Error("the blank session the load produced was left open; KAS keeps it resident beside the new one")
	}
	rec := chatRecord(t, cs, chatID)
	if rec.ACPSessionID != doors[1].sessionID || sb.SessionID() != marotte.SessionID(doors[1].sessionID) {
		t.Errorf("chat runs %q (bridge %q), want the new session %q", rec.ACPSessionID, sb.SessionID(), doors[1].sessionID)
	}
	if !slices.Contains(rec.PriorACPSessionIDs, lostSession) {
		t.Errorf("prior sessions = %v, want %q kept in the chain", rec.PriorACPSessionIDs, lostSession)
	}
	if got := contextResets(t, cs, chatID); !slices.Equal(got, []marotte.ContextResetCause{marotte.ContextResetSessionMissing}) {
		t.Errorf("context resets = %v, want exactly one session_missing", got)
	}
}

func TestSessionLoad_AnEmptyReplayOfASessionWithNoBoundTurnKeepsIt(t *testing.T) {
	h, cs, fakes := lostRecordHub(t, nil)
	const chatID marotte.ChatID = "c1"
	onSession(t, cs, chatID, "s-a")
	boundTurn(t, cs, chatID, "s-a")
	onSession(t, cs, chatID, "s-b")

	if _, err := h.coord.openReleased(t.Context(), chatID, ""); err != nil {
		t.Fatalf("openBridge: %v", err)
	}

	if _, loads := fakes.doors(); !slices.Equal(loads, []bool{true}) {
		t.Errorf("doors (load?) = %v, want the one load of s-b kept", loads)
	}
	if rec := chatRecord(t, cs, chatID); rec.ACPSessionID != "s-b" {
		t.Errorf("chat runs %q, want s-b kept: it never held a prompt, so an empty replay is its true state", rec.ACPSessionID)
	}
	if got := contextResets(t, cs, chatID); len(got) != 0 {
		t.Errorf("context resets = %v, want none", got)
	}
}

func TestSessionLoad_AnEmptyReplayAfterEveryBoundTurnWasRewoundKeepsIt(t *testing.T) {
	h, cs, fakes := lostRecordHub(t, nil)
	const chatID marotte.ChatID = "c1"
	onSession(t, cs, chatID, lostSession)
	turn := boundTurn(t, cs, chatID, lostSession)
	if _, _, err := cs.Revert(t.Context(), chatID, turn, "kas-"+lostSession); err != nil {
		t.Fatalf("Revert: %v", err)
	}

	if _, err := h.coord.openReleased(t.Context(), chatID, ""); err != nil {
		t.Fatalf("openBridge: %v", err)
	}

	if _, loads := fakes.doors(); !slices.Equal(loads, []bool{true}) {
		t.Errorf("doors (load?) = %v, want the one load kept: the rewind left KAS no messages", loads)
	}
	if rec := chatRecord(t, cs, chatID); rec.ACPSessionID != lostSession {
		t.Errorf("chat runs %q, want %q kept", rec.ACPSessionID, lostSession)
	}
	if got := contextResets(t, cs, chatID); len(got) != 0 {
		t.Errorf("context resets = %v, want none", got)
	}
}

func TestSessionLoad_ARefusedLoadOfABoundSessionRecordsAContextReset(t *testing.T) {
	h, cs, fakes := lostRecordHub(t, func(b *fakeBridge) { b.loadErr = errors.New("session/load refused") })
	const chatID marotte.ChatID = "c1"
	onSession(t, cs, chatID, lostSession)
	boundTurn(t, cs, chatID, lostSession)

	if _, err := h.coord.openReleased(t.Context(), chatID, ""); err != nil {
		t.Fatalf("openBridge: %v", err)
	}

	if doors, loads := fakes.doors(); !slices.Equal(loads, []bool{false}) || len(doors) != 1 {
		t.Errorf("completed doors (load?) = %v, want the one session/new after the refusal", loads)
	}
	if got := contextResets(t, cs, chatID); !slices.Equal(got, []marotte.ContextResetCause{marotte.ContextResetLoadFailed}) {
		t.Errorf("context resets = %v, want exactly one load_failed", got)
	}
}

func TestSessionLoad_ARefusedLoadOfAnUnboundSessionRecordsNothing(t *testing.T) {
	h, cs, _ := lostRecordHub(t, func(b *fakeBridge) { b.loadErr = errors.New("session/load refused") })
	const chatID marotte.ChatID = "c1"
	onSession(t, cs, chatID, "s-b")
	boundTurn(t, cs, chatID, "s-a")

	if _, err := h.coord.openReleased(t.Context(), chatID, ""); err != nil {
		t.Fatalf("openBridge: %v", err)
	}

	if got := contextResets(t, cs, chatID); len(got) != 0 {
		t.Errorf("context resets = %v, want none: the chat shows no turn s-b was sent", got)
	}
}

func TestSessionLoad_ABlankLoadUnderAPromptSendsThePromptToTheNewSession(t *testing.T) {
	h, cs, fakes := lostRecordHub(t, nil)
	const chatID marotte.ChatID = "c1"
	onSession(t, cs, chatID, lostSession)
	boundTurn(t, cs, chatID, lostSession)
	turnID, err := h.coord.openTurnNow(t.Context(), chatID, command.TurnOpen{
		Source: marotte.TurnSourcePrompt,
		Prompt: &marotte.EntryPrompt{ID: "m-2", Text: "next"},
	})
	if err != nil {
		t.Fatalf("OpenTurn: %v", err)
	}
	t.Cleanup(func() { h.coord.releaseTurn(chatID, turnID) })

	if _, err := h.coord.openReleased(t.Context(), chatID, ""); err != nil {
		t.Fatalf("openBridge: %v", err)
	}

	doors, _ := fakes.doors()
	if len(doors) != 2 {
		t.Fatalf("doors = %d, want the load then session/new", len(doors))
	}
	// The blank session's forward has exited; give its tail the time it takes to misread the close.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if cur := h.coord.bridgeFor(chatID); cur == nil || cur.SessionID() != marotte.SessionID(doors[1].sessionID) {
			t.Fatalf("the chat's bridge is %v, want the new session %q registered", cur, doors[1].sessionID)
		}
		time.Sleep(10 * time.Millisecond)
	}
	entries, err := cs.All(t.Context(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range entries {
		if entries[i].Turn == turnID && entries[i].Kind == marotte.EntryKindTurnClose {
			t.Fatalf("the prompt's turn was closed (%s) by the blank session's close, so it never reaches the new session",
				entries[i].Payload)
		}
	}
}

func TestOpenBridge_ABlankLoadRecordsTheUnservedModelOnce(t *testing.T) {
	h, cs, _ := lostRecordHub(t, nil)
	const chatID marotte.ChatID = "c1"
	onSession(t, cs, chatID, lostSession)
	boundTurn(t, cs, chatID, lostSession)
	if _, err := cs.Mutate(t.Context(), chatID, func(c *marotte.Chat, _ bool) bool {
		c.Model = "gone-model"
		c.ServedModelIDs = []string{"fake-model"}
		// A raw Append moves no header counter; a started conversation is what lets a switch entry land.
		c.TurnCount = 1
		return true
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := h.coord.openReleased(t.Context(), chatID, ""); err != nil {
		t.Fatalf("openBridge: %v", err)
	}

	entries, err := cs.All(t.Context(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	unavailable := 0
	for i := range entries {
		var sw marotte.EntryModelSwitched
		if entries[i].Kind == marotte.EntryKindModelSwitched && json.Unmarshal(entries[i].Payload, &sw) == nil &&
			sw.Reason == marotte.ModelSwitchReasonUnavailable {
			unavailable++
		}
	}
	if unavailable != 1 {
		t.Errorf("model_switched{reason: unavailable} entries = %d, want 1: the blank load wrote session facts", unavailable)
	}
}

func TestSessionLoad_ASessionEndingDuringItsReplayFailsTheOpenAndKeepsTheSession(t *testing.T) {
	h, cs, fakes := lostRecordHub(t, func(b *fakeBridge) {
		b.notifsOnStart = []*marotte.RPCResponse{
			replayNotif(t, "user_message_chunk", "ONE", ""),
			replayNotif(t, marotte.ACPUpdateSessionInfo, "", "turn_start"),
			replayNotif(t, marotte.ACPUpdateAgentChunk, "reply", ""),
			replayNotif(t, marotte.ACPUpdateSessionInfo, "", "turn_end"),
		}
		b.withholdReplay = 1
	})
	const chatID marotte.ChatID = "c1"
	onSession(t, cs, chatID, lostSession)

	opened := make(chan error, 1)
	go func() {
		_, err := h.coord.openReleased(t.Context(), chatID, "")
		opened <- err
	}()
	var loaded *fakeBridge
	for deadline := time.Now().Add(awaitPatience); loaded == nil; {
		if doors, _ := fakes.doors(); len(doors) > 0 && doors[0].SessionLoadSeq() > 0 {
			loaded = doors[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the load never answered")
		}
		time.Sleep(time.Millisecond)
	}
	loaded.end()

	select {
	case err := <-opened:
		if !errors.Is(err, marotte.ErrBridgeExited) {
			t.Errorf("openBridge = %v, want an error wrapping ErrBridgeExited", err)
		}
	case <-time.After(awaitPatience):
		t.Fatal("openBridge never answered after the session ended under its replay")
	}
	if rec := chatRecord(t, cs, chatID); rec.ACPSessionID != lostSession {
		t.Errorf("chat runs %q, want %q kept for the next open", rec.ACPSessionID, lostSession)
	}
	if _, loads := fakes.doors(); slices.Contains(loads, false) {
		t.Errorf("doors (load?) = %v, want no session/new", loads)
	}
}

func TestSwapProjectedTranscript_ZeroFramesForABoundSessionRestoredNothing(t *testing.T) {
	h, cs, _ := newTestHub()
	const chatID marotte.ChatID = "c1"
	onSession(t, cs, chatID, lostSession)
	boundTurn(t, cs, chatID, lostSession)
	h.coord.turns.testLifecycle(chatID)
	before := logOf(t, cs, chatID)

	blank := &loadProjection{proj: projectedTurn(t, h).proj, sessionID: lostSession}
	if err := h.replay.swapProjectedTranscript(chatID, blank); !errors.Is(err, errReplayRestoredNothing) {
		t.Errorf("swap of a zero-frame replay = %v, want errReplayRestoredNothing", err)
	}
	if after := logOf(t, cs, chatID); len(after) != len(before) {
		t.Errorf("the log holds %d entries after a refused swap, want %d unchanged", len(after), len(before))
	}

	replayed := projectedTurn(t, h)
	replayed.sessionID, replayed.frames = lostSession, 4
	if err := h.replay.swapProjectedTranscript(chatID, replayed); err != nil {
		t.Errorf("swap of a four-frame replay = %v, want nil", err)
	}
}

func TestHoldsTurnsOf(t *testing.T) {
	bind := func(turn, session string) marotte.Entry {
		raw, err := json.Marshal(marotte.EntryTurnBind{KASMessageID: "kas", SessionID: session})
		if err != nil {
			t.Fatal(err)
		}
		return marotte.Entry{ID: turn + ":bind", Turn: turn, Kind: marotte.EntryKindTurnBind, Payload: raw}
	}
	text := marotte.Entry{ID: "t1:text", Turn: "t1", Kind: marotte.EntryKindText, Payload: json.RawMessage(`{"text":"hi"}`)}
	cases := []struct {
		name     string
		entries  []marotte.Entry
		reverted map[string]struct{}
		want     bool
	}{
		{name: "a_bind_to_the_session", entries: []marotte.Entry{text, bind("t1", "s")}, want: true},
		{name: "a_bind_to_another_session", entries: []marotte.Entry{bind("t1", "other")}},
		{name: "a_bind_only_in_a_reverted_turn", entries: []marotte.Entry{bind("t1", "s")}, reverted: map[string]struct{}{"t1": {}}},
		{name: "no_bind", entries: []marotte.Entry{text}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := holdsTurnsOf(tc.entries, tc.reverted, "s"); got != tc.want {
				t.Errorf("holdsTurnsOf = %v, want %v", got, tc.want)
			}
		})
	}
}

// An empty turn's retry runs on a new session, so a chat that showed turns the old one was sent gets exactly one
// context_reset, between the empty turn and the retry.
func TestPromptTurn_AnEmptyTurnsRetryRecordsOneContextReset(t *testing.T) {
	gate := make(chan struct{})
	h, cs, fakes := lostRecordHub(t, func(b *fakeBridge) {
		if b.sessionID == "sess-1" {
			b.blockOn = map[string]chan struct{}{marotte.MethodPrompt: gate}
		}
	})
	const chatID marotte.ChatID = "c1"
	seedChat(t, cs, chatID)

	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: chatID,
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	doorAt := func(i int) *fakeBridge {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if doors, _ := fakes.doors(); len(doors) > i {
				return doors[i]
			}
			if time.Now().After(deadline) {
				t.Fatalf("session %d never opened", i+1)
			}
			time.Sleep(time.Millisecond)
		}
	}
	first := doorAt(0)
	waitForCall(t, first, marotte.MethodPrompt)
	first.deliver(first.promptTurnStartMsg())
	first.deliver(newSessionInfoMsg(map[string]any{"kind": "user_message_id_assigned", "userMessageId": "kas-1"}))
	first.deliver(newTurnEndMsg("end_turn"))
	close(gate)
	waitForCall(t, doorAt(1), marotte.MethodPrompt)

	if got := contextResets(t, cs, chatID); !slices.Equal(got, []marotte.ContextResetCause{marotte.ContextResetEmptyRetry}) {
		t.Fatalf("context_reset causes = %v, want exactly [%s]", got, marotte.ContextResetEmptyRetry)
	}
	entries := logOf(t, cs, chatID)
	reset := slices.IndexFunc(entries, func(e marotte.Entry) bool { return e.Kind == marotte.EntryKindContextReset })
	retry := slices.IndexFunc(entries, func(e marotte.Entry) bool {
		return e.Kind == marotte.EntryKindTurnOpen && strings.Contains(string(e.Payload), string(marotte.TurnOpenNameEmptyRetry))
	})
	if retry < 0 || reset > retry {
		t.Errorf("context_reset at %d, empty_retry turn_open at %d: want the reset above the retry", reset, retry)
	}
}
