package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/liveness"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
	"github.com/cplieger/sse"
	"github.com/cplieger/sse/ssetest"
)

// The transport is github.com/cplieger/sse's and tested there; these pin marotte's layer: emit and chat
// topics, the frame-cap substitute, the handshake, the initial-state hook, the draining gate.

func TestEmit_AppendsToReplayBuffer(t *testing.T) {
	h, _, _ := newTestHub()
	h.bus.emit(marotte.ServerEvent{Type: "chat_updated", ChatID: "c1"})
	h.bus.emit(marotte.ServerEvent{Type: "chat_updated", ChatID: "c2"})

	evts := h.bus.fanout.Snapshot()
	if len(evts) != 2 {
		t.Fatalf("replay len = %d, want 2", len(evts))
	}
	if evts[0].Event.Topic != "c1" || evts[1].Event.Topic != "c2" {
		t.Errorf("replay topics: %q, %q", evts[0].Event.Topic, evts[1].Event.Topic)
	}
	if evts[0].Offset >= evts[1].Offset {
		t.Errorf("event offsets not monotonic: %d → %d", evts[0].Offset, evts[1].Offset)
	}
	if !strings.Contains(string(evts[0].Event.Data), `"chat_id":"c1"`) {
		t.Errorf("payload not the marshaled ServerEvent: %s", evts[0].Event.Data)
	}
}

func TestEmit_CapsBufferAtReplayBufSize(t *testing.T) {
	h, _, _ := newTestHub()
	for range replayBufSize + 100 {
		h.bus.emit(marotte.ServerEvent{Type: "test"})
	}
	pos := h.bus.fanout.Position()
	if pos.Head-pos.Floor+1 != uint64(replayBufSize) {
		t.Errorf("window = %d, want cap %d", pos.Head-pos.Floor+1, replayBufSize)
	}
}

func TestEmit_TopicCarriesChatID(t *testing.T) {
	// emit maps ChatID onto the topic (empty is global); the stream is unfiltered, so the topic is diagnostic.
	h, _, _ := newTestHub()
	h.bus.emit(marotte.ServerEvent{Type: "chat_updated", ChatID: "c1"})
	h.bus.emit(marotte.ServerEvent{Type: "chat_updated", ChatID: "c2"})
	h.bus.emit(marotte.ServerEvent{Type: "connected"}) // global: empty topic

	got := h.bus.fanout.Snapshot()
	if len(got) != 3 {
		t.Fatalf("buffered = %d events, want 3", len(got))
	}
	wantTopics := []string{"c1", "c2", ""}
	for i, e := range got {
		if e.Event.Topic != wantTopics[i] {
			t.Errorf("event %d topic = %q, want %q", i, e.Event.Topic, wantTopics[i])
		}
	}
}

// TestEmit_AStampedFrameOverTheCapBecomesSubjectChanged pins that an entry_appended past sse.MaxFrameBytes becomes one
// subject_changed carrying its stamp, with a Warn; a frame under the cap publishes intact.
func TestEmit_AStampedFrameOverTheCapBecomesSubjectChanged(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	h, _, _ := newTestHub()

	stamped := marotte.NewEvent(marotte.EventEntryAppended, "c1", marotte.EntryAppendedPayload{
		Entry: textEntry(t, "t1", 1, strings.Repeat("o", sse.MaxFrameBytes+1)),
	})
	stamped.Subject = &marotte.SubjectStamp{Kind: string(subject.KindChat), Ref: "c1", Version: "7"}
	h.bus.emit(stamped)

	small := marotte.NewEvent(marotte.EventEntryAppended, "c1", marotte.EntryAppendedPayload{
		Entry: textEntry(t, "t1", 2, strings.Repeat("o", 64<<10)),
	})
	h.bus.emit(small)

	ring := h.bus.fanout.Snapshot()
	if len(ring) != 2 {
		t.Fatalf("ring holds %d frames, want 2 (the substitute and the intact entry_appended)", len(ring))
	}
	var substitute marotte.ServerEvent
	if err := reencodeBytes(ring[0].Event.Data, &substitute); err != nil {
		t.Fatalf("decode ring[0]: %v", err)
	}
	if substitute.Type != marotte.EventSubjectChanged || substitute.ChatID != "c1" {
		t.Fatalf("ring[0] = %s for %q, want subject_changed for c1", substitute.Type, substitute.ChatID)
	}
	if substitute.Subject == nil || *substitute.Subject != *stamped.Subject {
		t.Errorf("subject_changed Subject = %+v, want the refused frame's %+v", substitute.Subject, *stamped.Subject)
	}
	if !strings.Contains(string(ring[1].Event.Data), `"type":"entry_appended"`) || !strings.Contains(string(ring[1].Event.Data), `"seq":2`) {
		t.Errorf("ring[1] is not the intact second entry_appended: %.80s", ring[1].Event.Data)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "type=entry_appended") || !strings.Contains(logs.String(), "bytes=") {
		t.Errorf("no Warn naming entry_appended and the size: %s", logs.String())
	}
}

// textEntry is a sealed text entry of the given size in turn t1, the one frame able to outgrow the cap.
func textEntry(t *testing.T, turn string, seq uint64, text string) marotte.Entry {
	t.Helper()
	payload, err := json.Marshal(marotte.EntryText{Text: text})
	if err != nil {
		t.Fatalf("marshal text entry: %v", err)
	}
	return marotte.Entry{ID: fmt.Sprintf("%s-%d", turn, seq), Turn: turn, Kind: marotte.EntryKindText, Payload: payload, Seq: seq}
}

// TestEmit_AnUnstampedFrameOverTheCapIsDroppedWithAnError pins that no subject, no fetch to substitute.
func TestEmit_AnUnstampedFrameOverTheCapIsDroppedWithAnError(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	h, _, _ := newTestHub()

	h.bus.emit(marotte.NewEvent(marotte.EventError, "c1", marotte.ErrorPayload{
		Code: "huge", Message: strings.Repeat("x", sse.MaxFrameBytes+1),
	}))

	if n := len(h.bus.fanout.Snapshot()); n != 0 {
		t.Errorf("ring holds %d frames, want 0: an unstamped over-cap frame has no substitute", n)
	}
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "type=error") {
		t.Errorf("no Error naming the dropped frame: %s", logs.String())
	}
}

func reencodeBytes(data []byte, into any) error {
	return reencode(rawJSON(data), into)
}

// rawJSON lets reencode carry encoded bytes without re-marshalling.
type rawJSON []byte

func (r rawJSON) MarshalJSON() ([]byte, error) { return []byte(r), nil }

// TestHandleSSE_AdvertisesReconnectDelay pins the hub's reconnect hint, which carries no type or id to assert otherwise.
func TestHandleSSE_AdvertisesReconnectDelay(t *testing.T) {
	h, _, _ := newTestHub()
	h.bus.emit(marotte.ServerEvent{Type: "chat_updated", ChatID: "c1"})

	body := coldConnect(t, h, "").Body.String()
	want := fmt.Sprintf("retry: %d\n\n", liveness.ReconnectDelay.Milliseconds())
	if n := strings.Count(body, "retry: "); n != 1 {
		t.Fatalf("body carries %d retry: lines, want exactly 1 (a property of the connection, not of a frame): %q", n, body)
	}
	// Ahead of the replay and handshake, so it applies before the first drop.
	if !strings.HasPrefix(body, want) {
		t.Errorf("body does not open with %q: %q", want, body)
	}
}

// TestHandleSSE_KeepaliveIsANamedIDLessFrameOutsideTheRing pins the named keepalive: the client's name, no id:
// (Last-Event-ID stays put), never in the ring. Serial: it writes a package var read at construction.
func TestHandleSSE_KeepaliveIsANamedIDLessFrameOutsideTheRing(t *testing.T) {
	prev := keepaliveInterval
	keepaliveInterval = 10 * time.Millisecond
	t.Cleanup(func() { keepaliveInterval = prev })
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	h.bus.emit(marotte.ServerEvent{Type: "chat_updated", ChatID: "c1"})
	headBefore := h.bus.fanout.Position().Head

	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	h.handleSSE(rec, req)

	body := rec.Body.String()
	var beats []string
	for frame := range strings.SplitSeq(body, "\n\n") {
		if strings.Contains(frame, "event: heartbeat") {
			beats = append(beats, frame)
		}
	}
	if len(beats) == 0 {
		t.Fatalf("handleSSE body carries no event: heartbeat frame, want at least one: %q", body)
	}
	for _, frame := range beats {
		hasData := false
		for line := range strings.SplitSeq(frame, "\n") {
			if strings.HasPrefix(line, "id:") {
				t.Errorf("handleSSE keepalive frame %q carries an id: line, want none", frame)
			}
			if strings.HasPrefix(line, "data: ") {
				hasData = true
			}
		}
		if !hasData {
			t.Errorf("handleSSE keepalive frame %q carries no data: line, want one so the browser dispatches it", frame)
		}
	}
	if headAfter := h.bus.fanout.Position().Head; headAfter != headBefore {
		t.Errorf("handleSSE ring head after keepalives = %d, want %d (a keepalive must not enter the replay ring)", headAfter, headBefore)
	}
}

// cursorAt is the Last-Event-ID a client that last saw offset holds on this hub.
func cursorAt(h *Runtime, offset uint64) string {
	return sse.Cursor{Epoch: h.bus.fanout.Position().Epoch, Offset: offset}.String()
}

func TestHandleSSE_ReplaysSinceLastEventID(t *testing.T) {
	h, _, _ := newTestHub()

	h.bus.emit(marotte.ServerEvent{Type: "chat_updated", ChatID: "c1"})
	h.bus.emit(marotte.ServerEvent{Type: "chat_updated", ChatID: "c2"})
	h.bus.emit(marotte.ServerEvent{Type: "chat_updated", ChatID: "c3"})

	ctx := hookOnlyContext(t)
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	req.Header.Set(wireHeader, "1")
	req.Header.Set("Last-Event-ID", cursorAt(h, 1)) // skip event 1 only
	rec := httptest.NewRecorder()

	h.handleSSE(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, `"chat_id":"c1"`) {
		t.Errorf("replay included event <= Last-Event-ID: %s", body)
	}
	if !strings.Contains(body, `"chat_id":"c2"`) || !strings.Contains(body, `"chat_id":"c3"`) {
		t.Errorf("replay missed events after Last-Event-ID: %s", body)
	}
	if !strings.Contains(body, `"resumed":true`) {
		t.Errorf("the hello does not report the cursor as resumed: %s", body)
	}
}

// A resume past the reply cap is a gap: the hello says so and nothing is replayed.
func TestHandleSSE_AResumePastTheReplyCapIsAGap(t *testing.T) {
	h, _, _ := newTestHub()
	for i := 1; i <= replyMaxEvents+50; i++ {
		h.bus.emit(marotte.ServerEvent{Type: "chat_updated", ChatID: marotte.ChatID(fmt.Sprintf("c%d", i))})
	}

	ctx := hookOnlyContext(t)
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	req.Header.Set(wireHeader, "1")
	req.Header.Set("Last-Event-ID", cursorAt(h, 1))
	rec := httptest.NewRecorder()

	h.handleSSE(rec, req)

	frames, err := ssetest.ReadFrames(strings.NewReader(rec.Body.String()), 0)
	if err != nil {
		t.Fatalf("parse frames: %v", err)
	}
	replayed := 0
	for _, f := range frames {
		if f.ID != "" {
			replayed++
		}
	}
	if replayed != 0 {
		t.Errorf("a resume %d frames behind replayed %d ring frames, want 0 past the %d cap", replyMaxEvents+49, replayed, replyMaxEvents)
	}
	if !strings.Contains(rec.Body.String(), `"verdict":"gap_budget"`) {
		t.Errorf("the hello does not report gap_budget: %.300s", rec.Body.String())
	}
}

func TestHandleSSE_RejectsNonFlusher(t *testing.T) {
	h, _, _ := newTestHub()
	rec := &nonFlusherWriter{}
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	h.handleSSE(rec, req)
	if rec.status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.status)
	}
}

// Through the mux: the gate is a registration-time wrapper. An ungated route is checked too: a health probe
// must report the wind-down.
func TestRegisterRoutes_DrainingGate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"event stream", http.MethodGet, "/api/events", ""},
		{"command", http.MethodPost, "/api/command", `{"type":"create_chat","request_id":"r1"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newTestHub()
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)
			h.lifecycle.draining.Store(true)

			var body io.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}
			// Bounded, so a regression fails fast instead of blocking on an open stream.
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			req := httptest.NewRequest(tc.method, tc.path, body).WithContext(ctx)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want 503 while draining", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "shutting down") {
				t.Errorf("body = %q, want marotte's shutting-down envelope", rec.Body.String())
			}
		})
	}

	t.Run("an ungated route still answers while draining", func(t *testing.T) {
		h, _, _ := newTestHub()
		mux := http.NewServeMux()
		h.RegisterRoutes(mux)
		h.lifecycle.draining.Store(true)

		req := httptest.NewRequest(http.MethodGet, "/api/config-template", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code == http.StatusServiceUnavailable {
			t.Error("an ungated route answered 503: the drain gate has become global")
		}
	})
}

type nonFlusherWriter struct {
	hdr    http.Header
	body   strings.Builder
	status int
}

func (w *nonFlusherWriter) Header() http.Header {
	if w.hdr == nil {
		w.hdr = make(http.Header)
	}
	return w.hdr
}
func (w *nonFlusherWriter) Write(p []byte) (int, error) { return w.body.Write(p) }
func (w *nonFlusherWriter) WriteHeader(code int)        { w.status = code }

// BenchmarkEmit measures marshal and publish (fan-out is benchmarked in the sse library).
func BenchmarkEmit(b *testing.B) {
	h, _, _ := newTestHub()
	evt := marotte.ServerEvent{Type: "chat_updated", ChatID: "bench"}
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		h.bus.emit(evt)
	}
}

// The hook's post-handshake state: a permission dialog aged out of the ring would block the agent unseen. v3
// rides the aggregate, legacy its own frame; neither synthesizes a turn_state.
func TestHandleSSE_ReplaysTheStateAClientCannotDeriveFromTheEventLog(t *testing.T) {
	h, _, br := newTestHub()

	h.bus.pendingPerms.Add(9, marotte.NewEvent(marotte.EventPermissionNeeded, "c1",
		marotte.PermissionNeededPayload{RequestID: 9}))
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})
	h.stagePromptTurn(t, "c1")

	for _, legacy := range []bool{false, true} {
		frames := connectFrames(t, h, legacy)
		if _, ok := frameOfType(frames, marotte.EventConnected); !ok {
			t.Fatalf("legacy=%v: no handshake, so the stream never opened", legacy)
		}
		if !busySetOf(connectedOf(t, frames))["c1"] {
			t.Errorf("legacy=%v: the chat mid-turn is absent from busy_chats", legacy)
		}
		if n := countType(frames, marotte.EventType("turn_state")); n != 0 {
			t.Errorf("legacy=%v: the connect synthesized %d turn_state frames; that channel is gone", legacy, n)
		}
		if legacy {
			if n := countType(frames, marotte.EventPermissionNeeded); n != 1 {
				t.Errorf("legacy connect replayed %d permission_needed frames, want 1", n)
			}
			continue
		}
		snap, ok := frameOfType(frames, marotte.EventPendingSnapshot)
		if !ok {
			t.Fatal("v3 connect wrote no pending_snapshot")
		}
		var payload marotte.PendingSnapshotPayload
		if err := reencode(snap.Payload, &payload); err != nil {
			t.Fatalf("decode pending_snapshot: %v", err)
		}
		if len(payload.Items) != 1 || !strings.Contains(string(payload.Items[0]), `"type":"permission_needed"`) {
			t.Errorf("v3 pending_snapshot items = %s, want the one permission_needed envelope", payload.Items)
		}
	}
}

// A parked run has no deadline and its event does not re-fire, so a reload with no replay leaves nothing to answer.
func TestHandleSSE_ReplaysAParkedStepsQuestion(t *testing.T) {
	h, _, _ := newTestHub()
	h.runs.asks.Add(&runAsk{
		chatID: "c1",
		payload: marotte.RunInputNeededPayload{
			WorkflowID: "wf_1", AskID: "a1", NodeID: "review", Question: "which branch?",
		},
	})

	body := coldConnectAs(t, h, true).Body.String()
	if !strings.Contains(body, `"type":"run_input_needed"`) {
		t.Fatalf("a parked step's question was not replayed, so the run stays parked with "+
			"nothing on screen to answer it: %q", body)
	}
	if !strings.Contains(body, "which branch?") {
		t.Errorf("the replayed ask carried no question, and no endpoint has one: %q", body)
	}
	v3 := coldConnectAs(t, h, false).Body.String()
	if !strings.Contains(v3, "which branch?") {
		t.Errorf("the v3 pending_snapshot does not carry the question: %q", v3)
	}

	// The claim deleted the entry, so a second connection must not re-offer it.
	if _, ok := h.runs.asks.TakeIfPresent("wf_1", "a1"); !ok {
		t.Fatal("Setup: the ask could not be claimed")
	}
	if after := coldConnectAs(t, h, true).Body.String(); strings.Contains(after, `"type":"run_input_needed"`) {
		t.Errorf("an answered ask was replayed to a later connection: %q", after)
	}
	if after := coldConnectAs(t, h, false).Body.String(); strings.Contains(after, "which branch?") {
		t.Errorf("an answered ask rode a later v3 pending_snapshot: %q", after)
	}
}

func TestHandleSSE_OnlyAReconnectAppliesNotificationTogglesEditedWhileTheStreamWasDown(t *testing.T) {
	cases := []struct {
		name        string
		lastEventID bool
		wantPushes  int
	}{
		{name: "a reconnect applies them", lastEventID: true, wantPushes: 1},
		{name: "a fresh connection does not", lastEventID: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := newTestChatStore()
			configDir := t.TempDir()
			rec := newLiveRecorder(t)
			h := New(context.Background(), t.TempDir(),
				func() ACPBridge { return newFakeBridge() }, cs, WithConfigDir(configDir), WithPush(rec))
			cs.wire(h)
			h.bus.emit(marotte.ServerEvent{Type: "chat_updated", ChatID: "c1"})
			h.bus.emit(marotte.ServerEvent{Type: "chat_updated", ChatID: "c2"})
			rewriteConfigByHand(t, configDir, `{"notify_pr_status":true}`)

			ctx := hookOnlyContext(t)
			req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
			if tc.lastEventID {
				req.Header.Set("Last-Event-ID", cursorAt(h, 2))
			}
			h.handleSSE(httptest.NewRecorder(), req)

			rec.mu.Lock()
			prefs := slices.Clone(rec.prefs)
			rec.mu.Unlock()
			if len(prefs) != tc.wantPushes {
				t.Fatalf("handleSSE with a Last-Event-ID %v pushed notification toggles %v, want %d push(es)",
					tc.lastEventID, prefs, tc.wantPushes)
			}
			if tc.wantPushes > 0 && !prefs[0][marotte.PushKindPRStatus] {
				t.Errorf("the reconnect pushed toggles %v, want pr_status on as the hand edit set it", prefs[0])
			}
		})
	}
}
