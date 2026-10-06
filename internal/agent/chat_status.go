package agent

// Last-declared chat status per chat, the status_snapshot input no message or replay holds
// (KAS's focus_update channel). Ephemeral: one merged entry per chat, dropped at turn end,
// never persisted.

import (
	"cmp"
	"maps"
	"slices"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
)

type chatStatusCache struct {
	byChat map[marotte.ChatID]marotte.ChatStatusPayload
	// versions holds the `status` counter, certifying the retained waiting set: only Merge always
	// mints; ClearWaiting and Clear mint only when removing a waiting_on_user row.
	versions *subject.Versions
	mu       sync.Mutex
}

func newChatStatusCache() *chatStatusCache {
	return &chatStatusCache{byChat: make(map[marotte.ChatID]marotte.ChatStatusPayload)}
}

// MergeStamped merges a chat's latest declaration into its entry and returns the effective
// payload and its `status` mint from one critical section, which is why bus.emit may stamp
// from it. KAS's focus channel is omit-if-unchanged, so an empty field means absent; a
// both-empty payload is a clear, tested before the merge. Keyed on the chat because a
// status can precede the turn's first chunk.
func (c *chatStatusCache) MergeStamped(chatID marotte.ChatID, p marotte.ChatStatusPayload) (marotte.ChatStatusPayload, *marotte.SubjectStamp) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if chatID == "" {
		// A global event has nothing to merge against; a zero payload would blank the frame.
		current, _ := c.registry().Current(subject.KindStatus, "")
		return p, statusStamp(current)
	}
	// Only Runtime.DischargeWaiting produces a both-empty payload; handleFocusUpdate returns early on one.
	if p.Status == "" && p.Description == "" {
		delete(c.byChat, chatID)
		return p, statusStamp(c.registry().BumpCounter(subject.KindStatus, ""))
	}
	prev := c.byChat[chatID]
	p.Status = cmp.Or(p.Status, prev.Status)
	p.Description = cmp.Or(p.Description, prev.Description)
	c.byChat[chatID] = p
	return p, statusStamp(c.registry().BumpCounter(subject.KindStatus, ""))
}

// registry returns the versions the cache mints into, defaulting to a private one. Callers hold c.mu.
func (c *chatStatusCache) registry() *subject.Versions {
	if c.versions == nil {
		c.versions = &subject.Versions{}
	}
	return c.versions
}

// statusStamp is the `status` stamp at version.
func statusStamp(version string) *marotte.SubjectStamp {
	return marotte.NewSubjectStamp(string(subject.KindStatus), "", version)
}

// waitingRowsLocked is the retained waiting_on_user set minus the chats in busy, in chat order. Callers hold c.mu.
func (c *chatStatusCache) waitingRowsLocked(busy map[marotte.ChatID]*Turn) []marotte.StatusRow {
	rows := make([]marotte.StatusRow, 0, len(c.byChat))
	for id, p := range c.byChat {
		if _, isBusy := busy[id]; isBusy || p.Status != marotte.ChatStatusWaitingOnUser {
			continue
		}
		rows = append(rows, marotte.StatusRow{ChatID: id, Status: p.Status, Description: p.Description})
	}
	slices.SortFunc(rows, func(a, b marotte.StatusRow) int { return cmp.Compare(a.ChatID, b.ChatID) })
	return rows
}

// SnapshotStamped is the status_snapshot payload with its `status` stamp. The counter is read
// FIRST, then the rows, so a racing mutation can only make the client's set newer than its
// stamp, never a false unchanged.
func (c *chatStatusCache) SnapshotStamped(busy map[marotte.ChatID]*Turn) (marotte.StatusSnapshotPayload, *marotte.SubjectStamp) {
	c.mu.Lock()
	defer c.mu.Unlock()
	version, _ := c.registry().Current(subject.KindStatus, "")
	rows := c.waitingRowsLocked(busy)
	return marotte.StatusSnapshotPayload{Rows: rows}, statusStamp(version)
}

// Snapshot copies every retained status, for the connect replay.
func (c *chatStatusCache) Snapshot() map[marotte.ChatID]marotte.ChatStatusPayload {
	c.mu.Lock()
	defer c.mu.Unlock()
	return maps.Clone(c.byChat)
}

// ClearAtTurnEnd drops a chat's status at turn end, EXCEPT waiting_on_user, which means a
// person still owes an answer and must survive a refresh or a second device. It is kept
// until the agent's next declaration or the chat going away.
func (c *chatStatusCache) ClearAtTurnEnd(chatID marotte.ChatID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byChat[chatID].Status == marotte.ChatStatusWaitingOnUser {
		return
	}
	delete(c.byChat, chatID)
}

// ClearWaiting drops a chat's status only when it is the retained waiting_on_user claim, and
// reports whether one went. It mints under c.mu; the discharge's own frame then bumps again,
// a spurious changed but never a false unchanged.
func (c *chatStatusCache) ClearWaiting(chatID marotte.ChatID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byChat[chatID].Status != marotte.ChatStatusWaitingOnUser {
		return false
	}
	delete(c.byChat, chatID)
	c.registry().BumpCounter(subject.KindStatus, "")
	return true
}

// Clear drops a chat's status unconditionally, for a closed or deleted chat. Mints only when
// it removes a waiting_on_user row.
func (c *chatStatusCache) Clear(chatID marotte.ChatID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	waiting := c.byChat[chatID].Status == marotte.ChatStatusWaitingOnUser
	delete(c.byChat, chatID)
	if waiting {
		c.registry().BumpCounter(subject.KindStatus, "")
	}
}
