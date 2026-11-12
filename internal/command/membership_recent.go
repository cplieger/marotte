package command

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
)

// recentExpiryRetry is how long RunRecentExpiry waits after a failed write before it tries again.
const recentExpiryRetry = time.Minute

// recentFacts reads what each chat's Recently closed entry carries from its record, since the tab store cannot read
// records; a record Get cannot load gives an empty name. Caller holds mu, so no rename lands between read and close.
func (m *Membership) recentFacts(ctx context.Context, chats []marotte.ChatID) map[string]tabs.RecentFacts {
	facts := make(map[string]tabs.RecentFacts, len(chats))
	for _, id := range chats {
		var f tabs.RecentFacts
		if c, ok := m.chats.Get(ctx, id); ok {
			f.Name = c.Name
		}
		facts[string(id)] = f
	}
	return facts
}

// recentChange is what a frame carries for the list after a mutation: the whole list when it differs from before,
// nil when it does not, since an absent list means unchanged. A change wakes RunRecentExpiry. Caller holds mu
// across the mutation and both reads, so no other mutation's change is attributed to this frame.
func (m *Membership) recentChange(before []marotte.RecentClose) *marotte.RecentList {
	after := m.tabs.Recent()
	if slices.Equal(before, after) {
		return nil
	}
	select {
	case m.recentWake <- struct{}{}:
	default:
	}
	if after == nil {
		after = []marotte.RecentClose{}
	}
	return &marotte.RecentList{Items: after}
}

// forgetRecent drops a removed chat's entry and announces the list. A failed write leaves the entry to the boot
// prune, which drops an entry whose record is gone. Caller holds mu.
func (m *Membership) forgetRecent(ctx context.Context, chatID marotte.ChatID, opID string) {
	before := m.tabs.Recent()
	version, err := m.tabs.Forget(ctx, string(chatID))
	if err != nil {
		slog.Warn("tabs: a removed chat's recently closed entry could not be dropped", "chat_id", chatID, keyError, err)
		return
	}
	if recent := m.recentChange(before); recent != nil {
		m.emit(ctx, &marotte.TabsChangedPayload{Recent: recent, Version: version, OpID: opID})
	}
}

// RunRecentExpiry removes each Recently closed entry when its window ends and announces the new list, sleeping
// until the oldest entry's expiry or a change to the list. A failed write is retried a minute later. It blocks
// until ctx ends.
func (m *Membership) RunRecentExpiry(ctx context.Context) {
	if m.tabs == nil {
		<-ctx.Done()
		return
	}
	var retryAt time.Time
	for {
		due, armed := retryAt, !retryAt.IsZero()
		if !armed {
			due, armed = m.nextRecentExpiry()
		}
		switch m.sleepRecent(ctx, due, armed) {
		case sleepEnded:
			return
		case sleepWoken:
			continue
		case sleepDue:
		}
		retryAt = time.Time{}
		if err := m.expireRecent(ctx); err != nil {
			slog.Warn("tabs: an expired recently closed entry could not be removed; retrying", keyError, err)
			retryAt = time.Now().Add(recentExpiryRetry)
		}
	}
}

type sleepOutcome int

const (
	sleepDue sleepOutcome = iota
	sleepWoken
	sleepEnded
)

// sleepRecent waits until due (forever when not armed), a wake, or ctx's end.
func (m *Membership) sleepRecent(ctx context.Context, due time.Time, armed bool) sleepOutcome {
	var fire <-chan time.Time
	if armed {
		timer := time.NewTimer(time.Until(due))
		defer timer.Stop()
		fire = timer.C
	}
	select {
	case <-ctx.Done():
		return sleepEnded
	case <-m.recentWake:
		return sleepWoken
	case <-fire:
		return sleepDue
	}
}

// nextRecentExpiry is when the oldest entry's window ends, false with an empty list.
func (m *Membership) nextRecentExpiry() (time.Time, bool) {
	recent := m.tabs.Recent()
	if len(recent) == 0 {
		return time.Time{}, false
	}
	oldest := slices.MinFunc(recent, func(a, b marotte.RecentClose) int { return cmp.Compare(a.ClosedAt, b.ClosedAt) })
	return time.UnixMilli(oldest.ClosedAt + tabs.RecentWindow.Milliseconds()), true
}

// expireRecent drops the expired entries under mu and emits the list alone when it changed.
func (m *Membership) expireRecent(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	before := m.tabs.Recent()
	version, err := m.tabs.ExpireRecent(ctx, time.Now())
	if err != nil {
		return err
	}
	if recent := m.recentChange(before); recent != nil {
		m.emit(ctx, &marotte.TabsChangedPayload{Recent: recent, Version: version})
	}
	return nil
}

// chatRefs is ids as the tab store's refs.
func chatRefs(ids []marotte.ChatID) []string {
	refs := make([]string, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, string(id))
	}
	return refs
}
