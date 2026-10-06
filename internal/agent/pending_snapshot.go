package agent

import (
	"encoding/json"
	"log/slog"

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
	events := rt.bus.pendingPerms.List("")
	step(1)
	events = append(events, rt.runs.asks.List("")...)
	step(2)
	events = append(events, rt.bus.steers.List("")...)
	step(3)
	items := make([]json.RawMessage, 0, len(events))
	for i := range events {
		data, err := json.Marshal(events[i])
		if err != nil {
			slog.Error("pending snapshot: marshal item", "type", events[i].Type, "error", err)
			continue
		}
		items = append(items, data)
	}
	return marotte.PendingSnapshotPayload{Items: items}, marotte.NewSubjectStamp(string(subject.KindPending), "", version)
}
