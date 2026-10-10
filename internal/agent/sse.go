package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/sse"
)

// Without it the connect is a v2 bundle, which still needs numeric floor/head on `connected` and
// the per-item pending replay.
const wireHeader = "SSE-Wire"

const clientTagHeader = "SSE-Client"

// Broadcast publishes evt to every connected client.
func (b *bus) Broadcast(_ context.Context, evt marotte.ServerEvent) {
	b.emit(evt)
}

// PendingPermsAdd registers an unanswered decision so a reconnecting client gets
// it replayed, with the bridge its answer goes back on.
func (b *bus) PendingPermsAdd(acpID int64, evt marotte.ServerEvent, origin translate.AskOrigin) marotte.ServerEvent {
	return b.pendingPerms.add(acpID, evt, origin)
}

// It touches evt.Subject only for chat_status, taking the stamp MergeStamped minted with the
// payload.
func (b *bus) emit(evt marotte.ServerEvent) {
	switch evt.Type {
	case marotte.EventChatStatus:
		// A producer of this event must not hold chatLifecycle.mu: stageStatusDesc takes it (non-reentrant).
		if p, ok := evt.Payload.(marotte.ChatStatusPayload); ok {
			// The raw description: Turn.statusDesc is this turn's declaration.
			b.stageStatusDesc(evt.ChatID, p.Description)
			// The merged payload, since the client replaces both fields. evt is a value.
			evt.Payload, evt.Subject = b.chatStatus.mergeStamped(evt.ChatID, p)
		}
	case marotte.EventTurnClosed:
		b.chatStatus.clearAtTurnEnd(evt.ChatID)
	}
	data, err := json.Marshal(evt)
	if err != nil {
		slog.Error("emit marshal", "type", evt.Type, "error", err)
		return
	}
	_, err = b.fanout.Publish(sse.Event{Topic: string(evt.ChatID), Data: data})
	if err == nil {
		return
	}
	if !errors.Is(err, sse.ErrFrameTooLarge) {
		slog.Error("emit publish", "type", evt.Type, "error", err)
		return
	}
	// The hub refused the frame. A stamped frame is recovered through subject_changed's refetch; an unstamped one
	// that large is dropped and logged.
	if evt.Subject == nil {
		slog.Error("emit: frame exceeds the hub's cap and carries no subject; dropped",
			"type", evt.Type, "chat_id", evt.ChatID, "bytes", len(data), "cap", sse.MaxFrameBytes)
		return
	}
	slog.Warn("emit: frame exceeds the hub's cap; publishing subject_changed instead",
		"type", evt.Type, "chat_id", evt.ChatID, "bytes", len(data), "cap", sse.MaxFrameBytes,
		"kind", evt.Subject.Kind, "ref", logsafe.Field(evt.Subject.Ref))
	substitute := marotte.NewEvent(marotte.EventSubjectChanged, evt.ChatID, marotte.SubjectChangedPayload{})
	substitute.Subject = evt.Subject
	data, err = json.Marshal(substitute)
	if err != nil {
		slog.Error("emit marshal", "type", substitute.Type, "error", err)
		return
	}
	if _, err := b.fanout.Publish(sse.Event{Topic: string(evt.ChatID), Data: data}); err != nil {
		slog.Error("emit publish", "type", substitute.Type, "error", err)
	}
}

// The sse library owns the transport; marotte owns the connected handshake and the initial state
// the event log cannot give.
func (rt *Runtime) handleSSE(w http.ResponseWriter, r *http.Request) {
	legacy := r.Header.Get(wireHeader) == ""
	tag := r.Header.Get(clientTagHeader)
	client := "v3"
	if legacy {
		client = "legacy"
		rt.bus.legacyConnects.Add(1)
	} else {
		rt.bus.v3Connects.Add(1)
	}
	slog.Info("SSE connected", "client", client,
		"last_event_id", logsafe.Field(r.Header.Get("Last-Event-ID")))

	// A reconnect brings the server's own live settings, notification toggles included, up to a hand edit made while
	// the stream was down.
	if r.Header.Get("Last-Event-ID") != "" {
		rt.syncProcessLive(r.Context())
	}

	rt.bus.fanout.Serve(armedWriter(&rt.bus.closeAfter, w), r,
		sse.WithClientTag(tag),
		sse.OnConnect(func(sw *sse.Writer, h sse.Hello) error {
			return rt.streamInitialState(sw, &h, legacy)
		}),
	)
	slog.Info("SSE disconnected", "client", client)
}

// The connect payload's two list bounds, policy numbers: exceeding one is the defect, never the constant.
const (
	// maxBusyChats bounds the handshake's busy list: 256 × 37 bytes ≈ 9.5 KiB, far past any real chat count.
	maxBusyChats = 256
	// maxConnectLiveRuns bounds the handshake's live-run inventory: 128 rows of ~100 bytes ≈ 12.8 KiB. The single-run
	// rule is a product rule, not a bound; stale leases are what inflate it.
	maxConnectLiveRuns = 128
)

// streamInitialState is the OnConnect body: `connected`, then workspace state the event log cannot give. A v3
// connect gets pending_snapshot and status_snapshot, each whole and stamped; a legacy one gets the per-item
// replay and numeric floor/head. Frames are id-less and unnamed (v2 reads onmessage); `connected` has no Subject.
func (rt *Runtime) streamInitialState(sw *sse.Writer, h *sse.Hello, legacy bool) error {
	busy := rt.coord.turns.busyChatIDs()
	// Withheld rather than truncated, so the client retracts nothing.
	busyStated := len(busy) <= maxBusyChats
	if !busyStated {
		slog.Warn("connect busy-chat list withheld: over cap", "cap", maxBusyChats, "count", len(busy))
		busy = nil
	}
	liveRuns := rt.runs.liveRunRows()
	liveRunsStated := len(liveRuns) <= maxConnectLiveRuns
	if !liveRunsStated {
		slog.Warn("connect live-run inventory withheld: over cap",
			"cap", maxConnectLiveRuns, "count", len(liveRuns))
		liveRuns = nil
	}
	connected := marotte.ConnectedPayload{
		Workspace:      rt.lifecycle.workDir,
		BusyChats:      busy,
		LiveRuns:       liveRuns,
		BusyStated:     busyStated,
		LiveRunsStated: liveRunsStated,
	}
	if legacy {
		// The v2 bundle reads floor 0 as "not resumed, refetch".
		var floor uint64
		if h.Resumed {
			floor = h.Floor
		}
		head := h.Head
		connected.Floor, connected.Head = &floor, &head
	}
	writeEvent := func(evt marotte.ServerEvent) error {
		data, err := json.Marshal(evt)
		if err != nil {
			slog.Error("connect: marshal frame", "type", evt.Type, "error", err)
			return nil //nolint:nilerr // skip the unmarshalable frame, keep the stream
		}
		return sw.Event("", data)
	}
	if err := writeEvent(marotte.NewEvent(marotte.EventConnected, "", connected)); err != nil {
		return err
	}
	if legacy {
		return rt.replayLegacyState(writeEvent)
	}
	pending, pendingStamp := rt.pendingSnapshotStamped()
	pendingFrame := marotte.NewEvent(marotte.EventPendingSnapshot, "", pending)
	pendingFrame.Subject = pendingStamp
	if err := writeEvent(pendingFrame); err != nil {
		return err
	}
	status, statusStamp := rt.bus.chatStatus.snapshotStamped(rt.coord.turns.ownTurns())
	statusFrame := marotte.NewEvent(marotte.EventStatusSnapshot, "", status)
	statusFrame.Subject = statusStamp
	return writeEvent(statusFrame)
}

// replayLegacyState is the per-item replay v2 decoders know: pending permissions, run asks and steers, then
// waiting statuses of idle chats. Kept while a v2 bundle can exist.
func (rt *Runtime) replayLegacyState(writeEvent func(marotte.ServerEvent) error) error {
	if err := rt.replayPendingPermissions(writeEvent); err != nil {
		return err
	}
	// Separate from permissions: different lifetimes (run_ask.go).
	if err := rt.replayPendingRunAsks(writeEvent); err != nil {
		return err
	}
	// KAS holds the steering buffer and nothing can read it back (replayPendingSteers).
	if err := rt.replayPendingSteers(writeEvent); err != nil {
		return err
	}
	return rt.replayWaitingStatus(writeEvent, rt.coord.turns.ownTurns())
}

// replayWaitingStatus emits chat_status for every chat left waiting on a person whose turn is not running.
func (rt *Runtime) replayWaitingStatus(
	writeFn func(marotte.ServerEvent) error,
	open map[marotte.ChatID]*activeTurn,
) error {
	for id, p := range rt.bus.chatStatus.snapshot() {
		if _, busy := open[id]; busy {
			continue
		}
		if p.Status != marotte.ChatStatusWaitingOnUser {
			continue
		}
		if err := writeFn(marotte.NewEvent(marotte.EventChatStatus, id, p)); err != nil {
			return err
		}
	}
	return nil
}

// replayPendingPermissions sends every unresolved permission_needed to a new client, however old: KAS holds the request open until answered.
func (rt *Runtime) replayPendingPermissions(writeFn func(marotte.ServerEvent) error) error {
	for _, evt := range rt.bus.pendingPerms.list("") {
		if err := writeFn(evt); err != nil {
			return err
		}
	}
	return nil
}

// The dock de-duplicates by ask id.
func (rt *Runtime) replayPendingRunAsks(writeFn func(marotte.ServerEvent) error) error {
	for _, evt := range rt.runs.asks.list("") {
		if err := writeFn(evt); err != nil {
			return err
		}
	}
	return nil
}
