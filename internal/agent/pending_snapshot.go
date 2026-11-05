package agent

import (
	"encoding/json"
	"log/slog"

	"github.com/cplieger/go/sse"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
)

// mintPending bumps the workspace `pending` counter for one of the three pending stores, called
// under that store's mutex; a nil registry is replaced by a private one.
func mintPending(versions **subject.Versions) {
	if *versions == nil {
		*versions = &subject.Versions{}
	}
	(*versions).BumpCounter(subject.KindPending, "")
}

// pendingSnapshotStamped is the pending_snapshot payload with its `pending` stamp: every unresolved
// permission, run ask and steer as the envelope the live path would publish. The counter is read
// FIRST, then each store under its own lock, so a racing mutation can only make the set newer
// than the stamp, never a false unchanged.
func (rt *Runtime) pendingSnapshotStamped() (marotte.PendingSnapshotPayload, *marotte.SubjectStamp) {
	return rt.pendingSnapshot(nil)
}

// pendingSnapshot is pendingSnapshotStamped with afterRead run after read n (0 the counter, then
// the three stores), so the interleaving property drives mutations into every gap.
func (rt *Runtime) pendingSnapshot(afterRead func(n int)) (marotte.PendingSnapshotPayload, *marotte.SubjectStamp) {
	step := func(n int) {
		if afterRead != nil {
			afterRead(n)
		}
	}
	version, _ := rt.versions.Current(subject.KindPending, "")
	step(0)
	events := rt.bus.pendingPerms.list("")
	step(1)
	events = append(events, rt.runs.asks.list()...)
	step(2)
	events = append(events, rt.bus.steers.list("")...)
	step(3)
	return marotte.PendingSnapshotPayload{Items: fitPendingItems(events, pendingSnapshotBudget)},
		marotte.NewSubjectStamp(string(subject.KindPending), "", version)
}

// pendingSnapshotBudget is what the items may take of the one connect frame, the rest kept for the envelope and
// its stamp: a frame over sse.MaxFrameBytes is refused, and with it the connect.
const pendingSnapshotBudget = sse.MaxFrameBytes - 4<<10

// fitPendingItems marshals events into at most budget bytes, counting a separator per item. Past the budget the
// remaining asks go without their command text, keeping its byte count so the card says what it hides; an item
// that fits not even so is left out, logged.
func fitPendingItems(events []marotte.ServerEvent, budget int) []json.RawMessage {
	whole, slim := marshalPendingItems(events)
	if framedLen(whole) <= budget {
		return toRaw(whole)
	}
	rest := framedLen(slim)
	items := make([]json.RawMessage, 0, len(whole))
	used, dropped, left := 0, 0, 0
	for i := range whole {
		rest -= len(slim[i]) + 1
		switch {
		case used+len(whole[i])+1+rest <= budget:
			items = append(items, whole[i])
			used += len(whole[i]) + 1
		case used+len(slim[i]) < budget:
			items = append(items, slim[i])
			used += len(slim[i]) + 1
			if len(slim[i]) < len(whole[i]) {
				dropped++
			}
		default:
			left++
		}
	}
	if dropped > 0 {
		slog.Warn("pending snapshot over its frame budget: command text dropped", "asks", dropped, "budget", budget)
	}
	if left > 0 {
		slog.Error("pending snapshot: items left out, too large for the connect frame even without command text",
			"items", left, "budget", budget)
	}
	return items
}

// marshalPendingItems is each event marshalled whole and, at the same index, without its command text (the
// whole bytes again when it has none); an event that does not marshal is left out of both, logged.
func marshalPendingItems(events []marotte.ServerEvent) (whole, slim [][]byte) {
	whole = make([][]byte, 0, len(events))
	slim = make([][]byte, 0, len(events))
	for i := range events {
		data, err := json.Marshal(events[i])
		if err != nil {
			slog.Error("pending snapshot: marshal item", "type", events[i].Type, "error", err)
			continue
		}
		short := data
		if evt, ok := withoutCommandText(events[i]); ok {
			if b, err := json.Marshal(evt); err == nil {
				short = b
			}
		}
		whole, slim = append(whole, data), append(slim, short)
	}
	return whole, slim
}

// framedLen is the bytes frames take with a separator after each.
func framedLen(frames [][]byte) int {
	n := 0
	for _, f := range frames {
		n += len(f) + 1
	}
	return n
}

// withoutCommandText is a permission ask with its command reduced to the byte count; ok is false for any other
// event.
func withoutCommandText(evt marotte.ServerEvent) (marotte.ServerEvent, bool) {
	p, ok := evt.Payload.(marotte.PermissionNeededPayload)
	if !ok || p.Command == nil {
		return evt, false
	}
	c := p.Command
	p.Command = &marotte.PermissionCommand{OmittedBytes: len(c.Text) + c.OmittedBytes + len(c.Tail)}
	evt.Payload = p
	return evt, true
}

func toRaw(frames [][]byte) []json.RawMessage {
	items := make([]json.RawMessage, len(frames))
	for i, f := range frames {
		items[i] = f
	}
	return items
}
