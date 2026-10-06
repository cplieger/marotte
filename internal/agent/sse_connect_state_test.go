package agent

// What the connect hook states. The busy set is the negative half no live frame carries, so a list the server
// cannot state completely retracts nothing (BusyStated). v3 gets one whole stamped frame per set; legacy
// keeps the per-item replay and numeric floor/head.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/subject"
	"github.com/cplieger/sse/ssetest"
)

// connectFrames runs one cold connect and returns its id-less frames in wire order; legacy omits SSE-Wire.
func connectFrames(t *testing.T, rt *Runtime, legacy bool) []marotte.ServerEvent {
	t.Helper()
	rec := coldConnectAs(t, rt, legacy)
	frames, err := ssetest.ReadFrames(strings.NewReader(rec.Body.String()), 0)
	if err != nil {
		t.Fatalf("Setup: parse frames: %v", err)
	}
	var out []marotte.ServerEvent
	for _, f := range frames {
		if f.Event != "" || f.ID != "" {
			continue // the hello, a keepalive, a replayed ring frame
		}
		var evt marotte.ServerEvent
		if err := json.Unmarshal([]byte(f.Data), &evt); err != nil {
			t.Fatalf("Setup: frame %q is not a ServerEvent: %v", f.Data, err)
		}
		out = append(out, evt)
	}
	return out
}

// connectPayload decodes the ONE connected frame a cold connect writes.
func connectPayload(t *testing.T, rt *Runtime, _ string) marotte.ConnectedPayload {
	t.Helper()
	return connectedOf(t, connectFrames(t, rt, false))
}

func connectedOf(t *testing.T, frames []marotte.ServerEvent) marotte.ConnectedPayload {
	t.Helper()
	for _, evt := range frames {
		if evt.Type != marotte.EventConnected {
			continue
		}
		var p marotte.ConnectedPayload
		if err := reencode(evt.Payload, &p); err != nil {
			t.Fatalf("Setup: decode connected: %v", err)
		}
		return p
	}
	t.Fatal("the cold connect wrote no connected frame")
	return marotte.ConnectedPayload{}
}

// reencode moves a decoded `any` payload into its typed shape.
func reencode(from, into any) error {
	data, err := json.Marshal(from)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, into)
}

func frameOfType(frames []marotte.ServerEvent, typ marotte.EventType) (marotte.ServerEvent, bool) {
	for _, evt := range frames {
		if evt.Type == typ {
			return evt, true
		}
	}
	return marotte.ServerEvent{}, false
}

func countType(frames []marotte.ServerEvent, typ marotte.EventType) int {
	n := 0
	for _, evt := range frames {
		if evt.Type == typ {
			n++
		}
	}
	return n
}

func busySetOf(p marotte.ConnectedPayload) map[marotte.ChatID]bool {
	out := make(map[marotte.ChatID]bool, len(p.BusyChats))
	for _, id := range p.BusyChats {
		out[id] = true
	}
	return out
}

func TestConnect_StatesTheBusySet(t *testing.T) {
	rt := newBudgetRuntime(t)
	ids := busyChatsWithHugeTurns(t, rt, 3)

	p := connectPayload(t, rt, "")

	if !p.BusyStated {
		t.Fatal("a connect does not state its busy set, so the client retracts " +
			"nothing and every stale `thinking` survives the reconnect")
	}
	busy := busySetOf(p)
	for _, id := range ids {
		if !busy[id] {
			t.Errorf("chat %q is running its own prompt turn and is absent from busy_chats", id)
		}
	}
}

// A workflow step's turn is its run's, so the hosting chat is not busy.
func TestConnect_TheBusySetExcludesStepTurns(t *testing.T) {
	rt := newBudgetRuntime(t)
	rt.bridge.mgr.orInsert("c-step")
	rt.bridge.mgr.orInsert("c-own")
	if _, _, err := rt.runs.log.Open(t.Context(), "wf-1", "wf-1/step", "sess-step", "c-step"); err != nil {
		t.Fatalf("Open(step turn): %v", err)
	}
	if !rt.runs.hostsLiveRun("c-step") {
		t.Fatal("the fixture's chat hosts no live run, so nothing below measures the exclusion")
	}
	rt.stagePromptTurn(t, "c-own")

	busy := busySetOf(connectPayload(t, rt, ""))

	if busy["c-step"] {
		t.Error("a hosted workflow-step turn names its LAUNCHING chat busy, so the retraction is " +
			"withheld from exactly the population it was designed for")
	}
	if !busy["c-own"] {
		t.Error("a chat's own prompt turn is absent from busy_chats")
	}
}

// In the admission window no Turn exists yet, so a reconnect must not retract.
func TestConnect_TheBusySetIncludesAnAdmittedPromptWithNoTurnMinted(t *testing.T) {
	rt := newBudgetRuntime(t)
	if !rt.coord.TryReserveTurn("c-admitted", marotte.TurnSourcePrompt) {
		t.Fatal("a fresh chat refused a prompt reservation")
	}
	t.Cleanup(func() { rt.coord.ReleaseTurnReservation("c-admitted") })

	if !busySetOf(connectPayload(t, rt, ""))["c-admitted"] {
		t.Error("a chat whose prompt is admitted but whose Turn is not minted is absent from " +
			"busy_chats, so the connect retraction clears `thinking` under a live prompt")
	}
}

func TestConnect_TheBusySetIsWithheldOverTheCap(t *testing.T) {
	rt := newBudgetRuntime(t)
	for i := range maxBusyChats + 1 {
		id := marotte.ChatID("c-over-" + string(rune('a'+i%26)) + string(rune('a'+i/26)))
		if !rt.coord.TryReserveTurn(id, marotte.TurnSourcePrompt) {
			t.Fatalf("chat %q refused a reservation", id)
		}
	}

	p := connectPayload(t, rt, "")

	if p.BusyStated {
		t.Errorf("an over-cap connect claims its busy set is complete (%d chats, cap %d)",
			len(p.BusyChats), maxBusyChats)
	}
	if len(p.BusyChats) != 0 {
		t.Errorf("an over-cap connect carries a TRUNCATED list of %d chats; a partial list "+
			"read as complete would clear a live turn", len(p.BusyChats))
	}
}

// The inventory rides this frame, so the first paint costs no round trip.
func TestConnect_CarriesEveryHeldLeaseAndStatesTheInventory(t *testing.T) {
	rt := newBudgetRuntime(t)
	store := rt.runs.leaseStore()
	if err := store.Put(t.Context(), &runlease.Lease{WorkflowID: "wf_a", ChatID: "c1", Recipe: "r", Origin: runlease.OriginManual}); err != nil {
		t.Fatalf("put wf_a: %v", err)
	}
	if err := store.Put(t.Context(), &runlease.Lease{WorkflowID: "wf_b", Recipe: "r2", Origin: runlease.OriginAgent}); err != nil {
		t.Fatalf("put wf_b: %v", err)
	}

	p := connectPayload(t, rt, "")

	if !p.LiveRunsStated {
		t.Fatal("the handshake does not state its live-run inventory, so the client pays the " +
			"GET /api/runs/live round trip on every connect")
	}
	got := map[string]bool{}
	for _, r := range p.LiveRuns {
		got[r.WorkflowID] = true
	}
	for _, want := range []string{"wf_a", "wf_b"} {
		if !got[want] {
			t.Errorf("held lease %q is absent from the handshake's live_runs", want)
		}
	}
}

// liveRunRows serves both doors, so the handshake and endpoint agree.
func TestLiveRunRows_AnswersIdenticallyToTheEndpoint(t *testing.T) {
	rt := newBudgetRuntime(t)
	store := rt.runs.leaseStore()
	for _, id := range []string{"wf_a", "wf_b", "wf_c"} {
		if err := store.Put(t.Context(), &runlease.Lease{WorkflowID: id, ChatID: "c1", Recipe: "r", Origin: runlease.OriginManual}); err != nil {
			t.Fatalf("put %q: %v", id, err)
		}
	}

	fromHandshake := connectPayload(t, rt, "").LiveRuns
	fromMethod := rt.runs.liveRunRows()

	if len(fromHandshake) != len(fromMethod) {
		t.Fatalf("the handshake carries %d rows and liveRunRows answers %d",
			len(fromHandshake), len(fromMethod))
	}
	byID := map[string]marotte.LiveRun{}
	for _, r := range fromMethod {
		byID[r.WorkflowID] = r
	}
	for _, h := range fromHandshake {
		m, ok := byID[h.WorkflowID]
		if !ok {
			t.Errorf("the handshake names run %q the projection does not", h.WorkflowID)
			continue
		}
		if h != m {
			t.Errorf("run %q reads %+v on the handshake and %+v from the projection",
				h.WorkflowID, h, m)
		}
	}
}

// TestConnect_V3CarriesTheWholePendingSetAsOneStampedFrame pins one pending_snapshot at the `pending` version and no per-item frames.
func TestConnect_V3CarriesTheWholePendingSetAsOneStampedFrame(t *testing.T) {
	rt := newBudgetRuntime(t)
	ids := busyChatsWithHugeTurns(t, rt, 2)
	rt.bus.steers.SteerWaiting(ids[1], &marotte.SteerQueuedPayload{SteerID: "s1", Text: "steer text"})

	frames := connectFrames(t, rt, false)

	if got := countType(frames, marotte.EventPendingSnapshot); got != 1 {
		t.Fatalf("a v3 connect wrote %d pending_snapshot frames, want exactly 1", got)
	}
	for _, typ := range []marotte.EventType{marotte.EventPermissionNeeded, marotte.EventRunInputNeeded, marotte.EventSteerQueued, marotte.EventType("turn_state")} {
		if n := countType(frames, typ); n != 0 {
			t.Errorf("a v3 connect wrote %d per-item %s frames beside the aggregate", n, typ)
		}
	}
	snap, _ := frameOfType(frames, marotte.EventPendingSnapshot)
	var payload marotte.PendingSnapshotPayload
	if err := reencode(snap.Payload, &payload); err != nil {
		t.Fatalf("decode pending_snapshot: %v", err)
	}
	// 2 permissions, 1 run ask and 1 steer.
	if len(payload.Items) != fixturePendingPerms+fixturePendingRunAsks+1 {
		t.Errorf("pending_snapshot carries %d items, want %d", len(payload.Items), fixturePendingPerms+fixturePendingRunAsks+1)
	}
	kinds := map[marotte.EventType]int{}
	for _, raw := range payload.Items {
		var item marotte.ServerEvent
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatalf("pending_snapshot item is not an envelope: %v", err)
		}
		kinds[item.Type]++
	}
	if kinds[marotte.EventPermissionNeeded] != fixturePendingPerms || kinds[marotte.EventRunInputNeeded] != fixturePendingRunAsks || kinds[marotte.EventSteerQueued] != 1 {
		t.Errorf("pending_snapshot item kinds = %v, want %d permission_needed, %d run_input_needed, 1 steer_queued",
			kinds, fixturePendingPerms, fixturePendingRunAsks)
	}
	version, _ := rt.versions.Current(subject.KindPending, "")
	want := marotte.SubjectStamp{Kind: "pending", Version: version}
	if snap.Subject == nil || *snap.Subject != want {
		t.Errorf("pending_snapshot Subject = %+v, want %+v", snap.Subject, want)
	}
	if version == subject.Unminted {
		t.Error("the fixture's pending mutations moved nothing; the stamp asserts nothing")
	}
}

// TestConnect_V3EmptySetsAreOneFrameEach pins that an empty snapshot frame is what clears a row resolved during the gap.
func TestConnect_V3EmptySetsAreOneFrameEach(t *testing.T) {
	rt := newBudgetRuntime(t)

	frames := connectFrames(t, rt, false)

	if len(frames) != 3 {
		t.Fatalf("a v3 connect on an empty workspace wrote %d frames, want 3 (connected, pending_snapshot, status_snapshot): %+v", len(frames), frames)
	}
	if frames[0].Type != marotte.EventConnected || frames[1].Type != marotte.EventPendingSnapshot || frames[2].Type != marotte.EventStatusSnapshot {
		t.Fatalf("frame order = [%s %s %s], want [connected pending_snapshot status_snapshot]", frames[0].Type, frames[1].Type, frames[2].Type)
	}
	var pending marotte.PendingSnapshotPayload
	if err := reencode(frames[1].Payload, &pending); err != nil || pending.Items == nil || len(pending.Items) != 0 {
		t.Errorf("empty pending_snapshot payload = %+v (%v), want items: []", frames[1].Payload, err)
	}
	var status marotte.StatusSnapshotPayload
	if err := reencode(frames[2].Payload, &status); err != nil || status.Rows == nil || len(status.Rows) != 0 {
		t.Errorf("empty status_snapshot payload = %+v (%v), want rows: []", frames[2].Payload, err)
	}
	for i, kind := range []string{"", "pending", "status"} {
		if i == 0 {
			if frames[0].Subject != nil {
				t.Errorf("connected carries Subject %+v; it combines several subjects and must carry none", *frames[0].Subject)
			}
			continue
		}
		want := marotte.SubjectStamp{Kind: kind, Version: subject.Unminted}
		if frames[i].Subject == nil || *frames[i].Subject != want {
			t.Errorf("%s Subject = %+v, want %+v", frames[i].Type, frames[i].Subject, want)
		}
	}
}

// TestConnect_V3StatusSnapshotCarriesTheWaitingSetMinusBusyChats pins that a running chat suppresses a stale waiting_on_user.
func TestConnect_V3StatusSnapshotCarriesTheWaitingSetMinusBusyChats(t *testing.T) {
	rt := newBudgetRuntime(t)
	rt.bus.chatStatus.MergeStamped("c-waiting", marotte.ChatStatusPayload{Status: marotte.ChatStatusWaitingOnUser, Description: "pick one"})
	rt.bus.chatStatus.MergeStamped("c-working", marotte.ChatStatusPayload{Status: "in_progress"})
	rt.bus.chatStatus.MergeStamped("c-busy", marotte.ChatStatusPayload{Status: marotte.ChatStatusWaitingOnUser})
	rt.bridge.mgr.orInsert("c-busy")
	rt.stagePromptTurn(t, "c-busy")

	frames := connectFrames(t, rt, false)

	snap, ok := frameOfType(frames, marotte.EventStatusSnapshot)
	if !ok {
		t.Fatal("a v3 connect wrote no status_snapshot")
	}
	if n := countType(frames, marotte.EventChatStatus); n != 0 {
		t.Errorf("a v3 connect wrote %d per-row chat_status frames beside the aggregate", n)
	}
	var payload marotte.StatusSnapshotPayload
	if err := reencode(snap.Payload, &payload); err != nil {
		t.Fatalf("decode status_snapshot: %v", err)
	}
	if len(payload.Rows) != 1 || payload.Rows[0].ChatID != "c-waiting" || payload.Rows[0].Description != "pick one" {
		t.Errorf("status_snapshot rows = %+v, want the one non-busy waiting row", payload.Rows)
	}
	version, _ := rt.versions.Current(subject.KindStatus, "")
	want := marotte.SubjectStamp{Kind: "status", Version: version}
	if snap.Subject == nil || *snap.Subject != want {
		t.Errorf("status_snapshot Subject = %+v, want %+v", snap.Subject, want)
	}
}

// TestConnect_LegacyKeepsThePerItemReplayAndNumericBounds pins the v2 shape: no aggregate frames, per-item frames, numeric floor/head.
func TestConnect_LegacyKeepsThePerItemReplayAndNumericBounds(t *testing.T) {
	rt := newBudgetRuntime(t)
	ids := busyChatsWithHugeTurns(t, rt, 2)
	rt.bus.steers.SteerWaiting(ids[1], &marotte.SteerQueuedPayload{SteerID: "s1", Text: "steer text"})
	rt.bus.chatStatus.MergeStamped("c-waiting", marotte.ChatStatusPayload{Status: marotte.ChatStatusWaitingOnUser})
	rt.bus.emit(marotte.ServerEvent{Type: marotte.EventChatUpdated, ChatID: "c1"})

	frames := connectFrames(t, rt, true)

	for _, typ := range []marotte.EventType{marotte.EventPendingSnapshot, marotte.EventStatusSnapshot, marotte.EventType("turn_state")} {
		if n := countType(frames, typ); n != 0 {
			t.Errorf("a legacy connect wrote %d %s frames; the v2 bundle has no decoder for it", n, typ)
		}
	}
	if got := countType(frames, marotte.EventPermissionNeeded); got != fixturePendingPerms {
		t.Errorf("legacy connect replayed %d permission_needed frames, want %d", got, fixturePendingPerms)
	}
	if got := countType(frames, marotte.EventRunInputNeeded); got != fixturePendingRunAsks {
		t.Errorf("legacy connect replayed %d run_input_needed frames, want %d", got, fixturePendingRunAsks)
	}
	if got := countType(frames, marotte.EventSteerQueued); got != 1 {
		t.Errorf("legacy connect replayed %d steer_queued frames, want 1", got)
	}
	if got := countType(frames, marotte.EventChatStatus); got != 1 {
		t.Errorf("legacy connect replayed %d chat_status frames, want 1 (the waiting row)", got)
	}
	p := connectedOf(t, frames)
	if p.Floor == nil || p.Head == nil {
		t.Fatalf("legacy connected carries floor=%v head=%v, want both numbers", p.Floor, p.Head)
	}
	if *p.Floor != 0 {
		t.Errorf("legacy fresh connect floor = %d, want 0 (not resumed, so the v2 client refetches)", *p.Floor)
	}
	if head := rt.bus.fanout.Position().Head; *p.Head != head {
		t.Errorf("legacy connected head = %d, want the ring head %d", *p.Head, head)
	}
	// The v2 client reads numbers.
	body := coldConnectAs(t, rt, true).Body.String()
	if !strings.Contains(body, `"floor":0`) || !strings.Contains(body, `"head":`+string(rune('0'+int(*p.Head)))) {
		t.Errorf("legacy connected does not carry numeric floor/head: %s", body)
	}
}

// TestConnect_V3ConnectedCarriesNoFloorOrHead pins that a v3 client gets them from the library's hello.
func TestConnect_V3ConnectedCarriesNoFloorOrHead(t *testing.T) {
	rt := newBudgetRuntime(t)
	rt.bus.emit(marotte.ServerEvent{Type: marotte.EventChatUpdated, ChatID: "c1"})

	p := connectPayload(t, rt, "")
	if p.Floor != nil || p.Head != nil {
		t.Errorf("v3 connected carries floor=%v head=%v, want neither", p.Floor, p.Head)
	}
	// The hello has its own floor/head; read the application frame alone.
	frames, err := ssetest.ReadFrames(strings.NewReader(coldConnectAs(t, rt, false).Body.String()), 0)
	if err != nil {
		t.Fatalf("parse frames: %v", err)
	}
	for _, f := range frames {
		if f.Event == "" && strings.Contains(f.Data, `"type":"connected"`) && (strings.Contains(f.Data, `"floor"`) || strings.Contains(f.Data, `"head"`)) {
			t.Errorf("v3 connected frame still carries floor/head on the wire: %s", f.Data)
		}
	}
}

// TestHandleSSE_CountsConnectsByWireGeneration pins the counter the legacy overlap is retired on.
func TestHandleSSE_CountsConnectsByWireGeneration(t *testing.T) {
	rt := newBudgetRuntime(t)
	coldConnectAs(t, rt, true)
	coldConnectAs(t, rt, false)
	coldConnectAs(t, rt, false)
	if got := rt.bus.legacyConnects.Load(); got != 1 {
		t.Errorf("legacy_connect = %d, want 1", got)
	}
	if got := rt.bus.v3Connects.Load(); got != 2 {
		t.Errorf("v3_connect = %d, want 2", got)
	}
}
