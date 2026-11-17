package command

import (
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
			f.Name, f.Bot = c.Name, c.Bot != nil
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
	m.wakeRecent()
	if after == nil {
		after = []marotte.RecentClose{}
	}
	return &marotte.RecentList{Items: after}
}

// forgetRecent drops a removed chat's entry, shown or only held, and announces the version, with the list when it
// changed: a hidden entry's removal still bumps the version, and a version no frame announces sends every client into
// a re-list. A failed write leaves the entry to the boot prune, which drops an entry whose record is gone. Caller
// holds mu.
func (m *Membership) forgetRecent(ctx context.Context, chatID marotte.ChatID, opID string) {
	before := m.tabs.Recent()
	_, was := m.tabs.List()
	version, err := m.tabs.Forget(ctx, string(chatID))
	if err != nil {
		slog.Warn("tabs: a removed chat's recently closed entry could not be dropped", "chat_id", chatID, keyError, err)
		return
	}
	if version != was {
		m.emit(ctx, &marotte.TabsChangedPayload{Recent: m.recentChange(before), Version: version, OpID: opID})
	}
}

// HistoryWindowChanged tells RunRecentExpiry to read the History window setting again and apply it. A coordinator
// built with no HistoryWindow reader ignores it.
func (m *Membership) HistoryWindowChanged() {
	if m.historyWindow == nil {
		return
	}
	m.windowStale.Store(true)
	m.wakeRecent()
}

func (m *Membership) wakeRecent() {
	select {
	case m.recentWake <- struct{}{}:
	default:
	}
}

// RunRecentExpiry applies the History window at its start and after each HistoryWindowChanged, hides each Recently
// closed entry when its window ends, and announces each new list, sleeping until the oldest shown entry's expiry or a
// change. A failed write is retried a minute later. It blocks until ctx ends.
func (m *Membership) RunRecentExpiry(ctx context.Context) {
	if m.tabs == nil {
		<-ctx.Done()
		return
	}
	var retryAt time.Time
	for {
		if retryAt.IsZero() {
			retryAt = m.applyStaleWindow(ctx)
		}
		due, armed := retryAt, !retryAt.IsZero()
		if !armed {
			due, armed = m.tabs.RecentExpiry()
		}
		switch m.sleepRecent(ctx, due, armed) {
		case sleepEnded:
			return
		case sleepWoken:
			continue
		case sleepDue:
		}
		retried := !retryAt.IsZero()
		retryAt = time.Time{}
		if retried && m.windowStale.Load() {
			continue
		}
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

// applyStaleWindow applies the History window when HistoryWindowChanged marked it stale, and answers when to retry a
// failed apply (zero when none failed); a failed one stays stale.
func (m *Membership) applyStaleWindow(ctx context.Context) (retryAt time.Time) {
	if !m.windowStale.Swap(false) {
		return time.Time{}
	}
	if err := m.applyHistoryWindow(ctx); err != nil {
		m.windowStale.Store(true)
		slog.Warn("tabs: the History window could not be applied; retrying", keyError, err)
		return time.Now().Add(recentExpiryRetry)
	}
	return time.Time{}
}

// applyHistoryWindow reads the setting outside mu, since it reads config.json, then applies it under mu and emits the
// list alone when it changed.
func (m *Membership) applyHistoryWindow(ctx context.Context) error {
	window := m.historyWindow(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	before := m.tabs.Recent()
	version, err := m.tabs.SetRecentWindow(ctx, window)
	if err != nil {
		return err
	}
	if recent := m.recentChange(before); recent != nil {
		m.emit(ctx, &marotte.TabsChangedPayload{Recent: recent, Version: version})
	}
	return nil
}

// expireRecent hides the expired entries under mu and emits the list alone when it changed.
func (m *Membership) expireRecent(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	before := m.tabs.Recent()
	version, err := m.tabs.ExpireRecent(ctx)
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
