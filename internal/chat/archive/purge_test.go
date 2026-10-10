package archive

import (
	"bytes"
	"context"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestPurge_RetentionCutoff asserts that files older than (now - maxAge) are purged, newer ones survive.
func TestPurge_RetentionCutoff(t *testing.T) {
	var rec purgeRecorder
	svc, _, dir := newPurgeTestService(t, WithOnPurge(rec.recordPurge))

	olderPath := writeAgedChat(t, dir, "older", 25*time.Hour)
	newerPath := writeAgedChat(t, dir, "newer", 23*time.Hour)

	svc.Purge(t.Context(), 24*time.Hour)

	if exists(t, olderPath) {
		t.Errorf("file older than cutoff (25h > 24h) survived: %s", olderPath)
	}
	if !exists(t, newerPath) {
		t.Errorf("file newer than cutoff (23h < 24h) was purged: %s", newerPath)
	}
	if got := rec.sorted(); !slices.Equal(got, []string{"older"}) {
		t.Errorf("onPurge fired for %v, want [older]", got)
	}
}

// TestPurge_SkipsNonChatFiles asserts that a plain file, a non-chat-id directory and a headerless chat
// directory are left alone even when old.
func TestPurge_SkipsNonChatFiles(t *testing.T) {
	var rec purgeRecorder
	svc, _, dir := newPurgeTestService(t, WithOnPurge(rec.recordPurge))

	chatPath := writeAgedChat(t, dir, "valid01", 48*time.Hour)

	notesPath := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notesPath, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write notes: %v", err)
	}
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(notesPath, old, old); err != nil {
		t.Fatalf("chtimes notes: %v", err)
	}

	badIDPath := filepath.Join(dir, "bad.id")
	if err := os.MkdirAll(badIDPath, 0o700); err != nil {
		t.Fatalf("mkdir bad-id dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(badIDPath, headerFileName), []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write bad-id header: %v", err)
	}
	if err := os.Chtimes(filepath.Join(badIDPath, headerFileName), old, old); err != nil {
		t.Fatalf("chtimes bad-id: %v", err)
	}

	headerlessPath := filepath.Join(dir, "headerless01")
	if err := os.MkdirAll(headerlessPath, 0o700); err != nil {
		t.Fatalf("mkdir headerless: %v", err)
	}
	if err := os.Chtimes(headerlessPath, old, old); err != nil {
		t.Fatalf("chtimes headerless: %v", err)
	}

	svc.Purge(t.Context(), 24*time.Hour)

	if exists(t, chatPath) {
		t.Errorf("valid old chat was not purged: %s", chatPath)
	}
	if !exists(t, notesPath) {
		t.Errorf("plain file was purged: %s", notesPath)
	}
	if !exists(t, badIDPath) {
		t.Errorf("invalid-id directory was purged: %s", badIDPath)
	}
	if !exists(t, headerlessPath) {
		t.Errorf("headerless chat directory was purged: %s", headerlessPath)
	}
	if got := rec.sorted(); !slices.Equal(got, []string{"valid01"}) {
		t.Errorf("onPurge fired for %v, want [valid01]", got)
	}
}

// TestPurge_EmptyAndMissingDir verifies Purge is a safe no-op when the
// chat directory is empty or absent.
func TestPurge_EmptyAndMissingDir(t *testing.T) {
	t.Run("empty chat dir", func(t *testing.T) {
		var rec purgeRecorder
		svc, _, _ := newPurgeTestService(t, WithOnPurge(rec.recordPurge))
		svc.Purge(t.Context(), 24*time.Hour)
		if got := rec.sorted(); len(got) != 0 {
			t.Errorf("onPurge fired %v on empty dir, want none", got)
		}
	})

	t.Run("missing chat dir", func(t *testing.T) {
		var rec purgeRecorder
		dir := t.TempDir()
		svc := New(newFakeStore(dir), WithOnPurge(rec.recordPurge))
		svc.Purge(t.Context(), 24*time.Hour)
		if got := rec.sorted(); len(got) != 0 {
			t.Errorf("onPurge fired %v on missing dir, want none", got)
		}
	})
}

// TestPurge_NilOnPurgeCallback verifies Purge does not panic when no
// onPurge callback is registered.
func TestPurge_NilOnPurgeCallback(t *testing.T) {
	svc, _, dir := newPurgeTestService(t)
	chatPath := writeAgedChat(t, dir, "nocb", 48*time.Hour)

	svc.Purge(t.Context(), 24*time.Hour)

	if exists(t, chatPath) {
		t.Errorf("old chat survived purge with nil callback: %s", chatPath)
	}
}

// TestPurge_BroadcastsChatDeletedStampedFromRemove pins the chat_deleted frame a purge owes every
// client, stamped with the version Remove minted rather than a later read.
func TestPurge_BroadcastsChatDeletedStampedFromRemove(t *testing.T) {
	var (
		mu     sync.Mutex
		frames []marotte.ServerEvent
	)
	svc, store, dir := newPurgeTestService(t, WithBroadcaster(func(_ context.Context, evt marotte.ServerEvent) {
		mu.Lock()
		defer mu.Unlock()
		frames = append(frames, evt)
	}))
	writeAgedChat(t, dir, "gone", 48*time.Hour)
	writeAgedChat(t, dir, "kept", 1*time.Hour)

	svc.Purge(t.Context(), 24*time.Hour)

	mu.Lock()
	defer mu.Unlock()
	if len(frames) != 1 {
		t.Fatalf("Purge broadcast %d frames, want exactly 1 chat_deleted", len(frames))
	}
	got := frames[0]
	if got.Type != marotte.EventChatDeleted || got.ChatID != "gone" {
		t.Errorf("frame = %s for %q, want chat_deleted for \"gone\"", got.Type, got.ChatID)
	}
	store.mu.Lock()
	want := marotte.SubjectStamp{Kind: "chats", Version: strconv.Itoa(store.removals)}
	store.mu.Unlock()
	if got.Subject == nil || *got.Subject != want {
		t.Errorf("chat_deleted Subject = %+v, want %+v", got.Subject, want)
	}
}

// TestPurgeScheduler_InitialEvaluationPurges verifies Start runs an
// initial purge evaluation that removes an over-retention chat.
func TestPurgeScheduler_InitialEvaluationPurges(t *testing.T) {
	purged := make(chan marotte.ChatID, 8)
	svc, _, dir := newPurgeTestService(t,
		WithOnPurge(func(id marotte.ChatID, _ []string) { purged <- id }))
	writeAgedChat(t, dir, "sched1", 48*time.Hour)

	sched := NewPurgeScheduler(svc,
		func() time.Duration { return 24 * time.Hour })
	sched.Start(t.Context())
	defer sched.Stop()

	if got := recvWithin(t, purged, 3*time.Second); got != "sched1" {
		t.Errorf("initial evaluation purged %q, want sched1", got)
	}
}

// TestPurgeScheduler_ReArmsAndProcessesSecondTrigger verifies the loop
// keeps processing triggers after the first pass (it is not one-shot).
func TestPurgeScheduler_ReArmsAndProcessesSecondTrigger(t *testing.T) {
	purged := make(chan marotte.ChatID, 8)
	svc, _, dir := newPurgeTestService(t,
		WithOnPurge(func(id marotte.ChatID, _ []string) { purged <- id }))
	writeAgedChat(t, dir, "first", 48*time.Hour)

	sched := NewPurgeScheduler(svc,
		func() time.Duration { return 24 * time.Hour })
	sched.Start(t.Context())
	defer sched.Stop()

	if got := recvWithin(t, purged, 3*time.Second); got != "first" {
		t.Fatalf("first pass purged %q, want first", got)
	}

	writeAgedChat(t, dir, "second", 48*time.Hour)
	sched.Trigger()
	if got := recvWithin(t, purged, 3*time.Second); got != "second" {
		t.Errorf("second trigger purged %q, want second", got)
	}
}

// TestPurgeScheduler_ZeroRetentionSkipsPurge verifies a retention of 0
// ("keep forever") disables purging entirely. A bug here would delete
// every chat, at any age.
func TestPurgeScheduler_ZeroRetentionSkipsPurge(t *testing.T) {
	purged := make(chan marotte.ChatID, 8)
	svc, _, dir := newPurgeTestService(t,
		WithOnPurge(func(id marotte.ChatID, _ []string) { purged <- id }))
	chatPath := writeAgedChat(t, dir, "keepforever", 9000*time.Hour)

	sched := NewPurgeScheduler(svc,
		func() time.Duration { return 0 })
	sched.Start(t.Context())
	sched.Stop()

	select {
	case id := <-purged:
		t.Errorf("retention 0 purged %q, want nothing purged", id)
	default:
	}
	if !exists(t, chatPath) {
		t.Errorf("retention 0 deleted a chat: %s", chatPath)
	}
}

// TestPurgeScheduler_StopClosesDone verifies Stop drains the scheduler
// goroutine (its done channel closes) and is safe to call more than once.
func TestPurgeScheduler_StopClosesDone(t *testing.T) {
	svc, _, _ := newPurgeTestService(t)
	sched := NewPurgeScheduler(svc,
		func() time.Duration { return 24 * time.Hour })
	sched.Start(t.Context())
	sched.Stop()

	select {
	case <-sched.done:
	default:
		t.Error("done channel not closed after Stop()")
	}

	sched.Stop()
}

// TestPurgeScheduler_ContextCancellationStopsLoop pins the ctx exit arm: asserting through Stop
// would pass even if that arm were gone.
func TestPurgeScheduler_ContextCancellationStopsLoop(t *testing.T) {
	svc, _, _ := newPurgeTestService(t)
	ctx, cancel := context.WithCancel(t.Context())
	sched := NewPurgeScheduler(svc, func() time.Duration { return 24 * time.Hour })
	sched.Start(ctx)

	cancel()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-sched.done:
	case <-timer.C:
		t.Fatal("scheduler goroutine did not exit after context cancellation")
	}
}

// TestPurgeScheduler_TriggerAfterStopIsNoop verifies Trigger is a safe
// no-op once the scheduler is stopped.
func TestPurgeScheduler_TriggerAfterStopIsNoop(t *testing.T) {
	svc, _, _ := newPurgeTestService(t)
	sched := NewPurgeScheduler(svc,
		func() time.Duration { return 24 * time.Hour })
	sched.Start(t.Context())
	sched.Stop()
	sched.Trigger()
}

// TestPurgeScheduler_StopWithoutStart verifies Stop is safe before Start
// (it must not block waiting on a goroutine that never launched).
func TestPurgeScheduler_StopWithoutStart(t *testing.T) {
	svc, _, _ := newPurgeTestService(t)
	sched := NewPurgeScheduler(svc,
		func() time.Duration { return 24 * time.Hour })
	sched.Stop()
}

func recvWithin(t *testing.T, ch <-chan marotte.ChatID, d time.Duration) marotte.ChatID {
	t.Helper()
	select {
	case id := <-ch:
		return id
	case <-time.After(d):
		t.Fatalf("timeout waiting for purge callback")
		return ""
	}
}

// TestPurge_SkipsVanishedEntryWithoutAborting asserts that an entry ReadDir lists but whose header fails to
// stat is skipped, and the old chats beside it are still purged.
func TestPurge_SkipsVanishedEntryWithoutAborting(t *testing.T) {
	var rec purgeRecorder
	svc, _, dir := newPurgeTestService(t, WithOnPurge(rec.recordPurge))

	if err := os.MkdirAll(filepath.Join(dir, "vanished"), 0o700); err != nil {
		t.Fatalf("mkdir vanished: %v", err)
	}
	realOld := writeAgedChat(t, dir, "realold", 48*time.Hour)

	svc.Purge(t.Context(), 24*time.Hour)

	if exists(t, realOld) {
		t.Errorf("a genuinely-old chat was not purged when a vanished sibling entry was present: %s", realOld)
	}
	if got := rec.sorted(); !slices.Equal(got, []string{"realold"}) {
		t.Errorf("onPurge fired for %v, want [realold] (the vanished entry must be skipped, not counted)", got)
	}
}

// TestPurgeScheduler_RescheduleWithZeroRetentionDoesNotPurge drives purgeAndReschedule directly: a
// retention of 0 skips the purge entirely.
func TestPurgeScheduler_RescheduleWithZeroRetentionDoesNotPurge(t *testing.T) {
	var rec purgeRecorder
	svc, _, dir := newPurgeTestService(t, WithOnPurge(rec.recordPurge))
	chatPath := writeAgedChat(t, dir, "keepforever", 9000*time.Hour)

	sched := NewPurgeScheduler(svc,
		func() time.Duration { return 0 })
	timer, _ := sched.purgeAndReschedule(t.Context())
	if timer != nil {
		timer.Stop()
	}

	if !exists(t, chatPath) {
		t.Errorf("retention 0 purged a chat during reschedule: %s", chatPath)
	}
	if got := rec.sorted(); len(got) != 0 {
		t.Errorf("retention 0 fired onPurge for %v, want nothing purged", got)
	}
}

// TestPurgeScheduler_APassWithNothingToPurgeBacksOff: an exempt chat must not pin the wake-up in
// the past, or the loop re-scans every few seconds forever.
func TestPurgeScheduler_APassWithNothingToPurgeBacksOff(t *testing.T) {
	svc, _, dir := newPurgeTestService(t,
		WithOpenTabs(func(marotte.ChatID) bool { return true }))
	writeAgedChat(t, dir, "pinned", 500*time.Hour)
	sched := NewPurgeScheduler(svc, func() time.Duration { return time.Hour })

	first := sched.armWait(time.Hour, svc.Purge(t.Context(), time.Hour))
	if first < idleBase {
		t.Fatalf("first idle wait = %v, want at least %v: an exempt chat must not pull the "+
			"wake-up down to the floor", first, idleBase)
	}
	second := sched.armWait(time.Hour, svc.Purge(t.Context(), time.Hour))
	if second <= first {
		t.Errorf("second idle wait = %v, want longer than the first (%v): consecutive passes "+
			"that purge nothing must back off", second, first)
	}
	if !exists(t, filepath.Join(dir, "pinned", headerFileName)) {
		t.Error("the exempt chat was purged; the premise of this test is gone")
	}
}

// Retention off is not a reason to go dark, and not a reason to spin either: the
// settings path does not Trigger, so the loop re-checks on the ceiling.
func TestPurgeScheduler_RetentionOffWaitsTheCeiling(t *testing.T) {
	svc, _, dir := newPurgeTestService(t)
	writeAgedChat(t, dir, "present", 1*time.Hour)
	sched := NewPurgeScheduler(svc, func() time.Duration { return 0 })

	if got := sched.armWait(0, PurgeResult{}); got != maxWait {
		t.Errorf("armWait(retention off) = %v, want the %v ceiling", got, maxWait)
	}
}

// TestPurgeScheduler_ArmsTheEarliestAgeKeptDeadline: a chat kept on age arms a timer at its own
// deadline (activity stamp plus window), earliest wins.
func TestPurgeScheduler_ArmsTheEarliestAgeKeptDeadline(t *testing.T) {
	svc, store, dir := newPurgeTestService(t)
	retention := time.Hour
	store.header = &RetentionHeader{UpdatedAt: time.Now().Add(-50 * time.Minute).UnixMilli()}
	writeAgedChat(t, dir, "nearer", 0)
	sched := NewPurgeScheduler(svc, func() time.Duration { return retention })

	res := svc.Purge(t.Context(), retention)
	if res.NextDeadline.IsZero() {
		t.Fatal("a pass that kept a chat on age reported no deadline; the loop has nothing to arm")
	}
	got := sched.armWait(retention, res)
	if got < 9*time.Minute || got > 11*time.Minute {
		t.Errorf("armWait = %v, want ~10m (a chat 50m into a 1h window)", got)
	}
}

// A deadline moments away, or already reached, arms the floor rather than a
// zero-length timer: an age-kept deadline is in the future by construction, but a
// boundary case must not turn the loop into a hot spin either.
func TestPurgeScheduler_FloorsADeadlineThatIsUponUs(t *testing.T) {
	svc, _, _ := newPurgeTestService(t)
	sched := NewPurgeScheduler(svc, func() time.Duration { return time.Hour })

	got := sched.armWait(time.Hour, PurgeResult{Kept: 1, NextDeadline: time.Now()})
	if got != minWait {
		t.Errorf("armWait for a deadline that is upon us = %v, want the %v floor", got, minWait)
	}
}

// A pass that PURGED something resets the back-off: work happening is evidence
// the store is in use, so the next idle pass starts from the base again rather
// than inheriting an hour-long wait.
func TestPurgeScheduler_APurgeResetsTheBackOff(t *testing.T) {
	svc, _, _ := newPurgeTestService(t)
	sched := NewPurgeScheduler(svc, func() time.Duration { return time.Hour })

	for range 4 {
		sched.armWait(time.Hour, PurgeResult{})
	}
	grown := sched.idleWait
	if grown <= idleBase {
		t.Fatalf("idle wait after four empty passes = %v, want more than %v", grown, idleBase)
	}
	if got := sched.armWait(time.Hour, PurgeResult{Purged: 1}); got != idleBase {
		t.Errorf("armWait after a pass that purged = %v, want the %v base (grown wait was %v)",
			got, idleBase, grown)
	}
}

// TestPurge_HandsTheSessionChainToOnPurge asserts that onPurge fires AFTER the chat file is removed, so the
// chain must be read before it goes.
func TestPurge_HandsTheSessionChainToOnPurge(t *testing.T) {
	var rec purgeRecorder
	svc, store, dir := newPurgeTestService(t, WithOnPurge(rec.recordPurge))

	chatPath := writeAgedChat(t, dir, "chained", 48*time.Hour)
	store.header = &RetentionHeader{SessionChain: []string{"sess_old", "sess_new"}}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(chatPath, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	svc.Purge(t.Context(), 24*time.Hour)

	if exists(t, chatPath) {
		t.Fatalf("expired chat survived the purge: %s", chatPath)
	}
	got := rec.chainFor("chained")
	want := []string{"sess_old", "sess_new"}
	if !slices.Equal(got, want) {
		t.Errorf("onPurge received chain %v, want %v — the purge cannot reap what it was not told about", got, want)
	}
}

// TestPurge_NeverPurgesALiveChat asserts that the purge scans the directory live chats live in, so age alone is
// not grounds to delete.
func TestPurge_NeverPurgesALiveChat(t *testing.T) {
	var rec purgeRecorder
	live := map[marotte.ChatID]bool{"open": true}
	svc, _, dir := newPurgeTestService(t,
		WithOnPurge(rec.recordPurge),
		WithLiveChats(func(id marotte.ChatID) bool { return live[id] }),
	)

	openPath := writeAgedChat(t, dir, "open", 72*time.Hour)
	abandonedPath := writeAgedChat(t, dir, "abandoned", 72*time.Hour)

	svc.Purge(t.Context(), 24*time.Hour)

	if !exists(t, openPath) {
		t.Error("a LIVE chat was purged for being old — retention deleted work in progress")
	}
	if exists(t, abandonedPath) {
		t.Error("an abandoned chat past the window survived")
	}
	if got := rec.sorted(); !slices.Equal(got, []string{"abandoned"}) {
		t.Errorf("onPurge fired for %v, want [abandoned]", got)
	}
}

// TestPurge_WithoutTheLivePredicateStillPurges asserts that a construction path that forgets isLive degrades to
// age-only purging, not a nil-call panic.
func TestPurge_WithoutTheLivePredicateStillPurges(t *testing.T) {
	var rec purgeRecorder
	svc, _, dir := newPurgeTestService(t, WithOnPurge(rec.recordPurge))
	p := writeAgedChat(t, dir, "old", 72*time.Hour)

	svc.Purge(t.Context(), 24*time.Hour)

	if exists(t, p) {
		t.Error("purge did nothing with no live predicate wired")
	}
}

// TestPurgeScheduler_AlwaysArmsATimer: purgeAndReschedule never returns a nil timer, or after an
// empty dir or "keep forever" the loop has no wake-up but Trigger. Asserted on the returned timer
// because the poll ceiling is an hour.
func TestPurgeScheduler_AlwaysArmsATimer(t *testing.T) {
	cases := map[string]struct {
		aged      bool
		retention time.Duration
	}{
		"empty directory, retention on":  {aged: false, retention: 24 * time.Hour},
		"chats present, retention off":   {aged: true, retention: 0},
		"empty directory, retention off": {aged: false, retention: 0},
		"keep forever":                   {aged: true, retention: -1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc, _, dir := newPurgeTestService(t)
			if tc.aged {
				writeAgedChat(t, dir, "aged", 48*time.Hour)
			}
			sched := NewPurgeScheduler(svc,
				func() time.Duration { return tc.retention })

			timer, timerC := sched.purgeAndReschedule(t.Context())
			if timer == nil || timerC == nil {
				t.Fatalf("purgeAndReschedule() = (%v, %v), want an armed timer: "+
					"a nil channel leaves the loop with no wake-up and it never purges again",
					timer, timerC)
			}
			timer.Stop()
		})
	}
}

// TestPurgeScheduler_CapsTheArmedWait asserts that without the cap a 30-day retention sleeps ~30 days and no
// settings change can shorten it. The premise (deadline beyond the ceiling) is asserted first.
func TestPurgeScheduler_CapsTheArmedWait(t *testing.T) {
	svc, _, dir := newPurgeTestService(t)
	writeAgedChat(t, dir, "fresh", 0)
	retention := 30 * 24 * time.Hour
	sched := NewPurgeScheduler(svc, func() time.Duration { return retention })

	res := svc.Purge(t.Context(), retention)
	natural := time.Until(res.NextDeadline)
	if natural <= maxWait {
		t.Fatalf("natural wait %v does not exceed the %v ceiling, so this test's "+
			"premise no longer holds; pick a longer retention", natural, maxWait)
	}
	if armed := sched.armWait(retention, res); armed != maxWait {
		t.Errorf("armWait = %v, want the %v ceiling (natural wait was %v)",
			armed, maxWait, natural)
	}

	timer, timerC := sched.purgeAndReschedule(t.Context())
	if timer == nil || timerC == nil {
		t.Fatal("purgeAndReschedule returned no timer")
	}
	timer.Stop()
}

// TestPurge_ChatWithoutAnActivityTimestampAgesFromMtime: a zero UpdatedAt dated to the epoch would
// purge a file written seconds ago.
func TestPurge_ChatWithoutAnActivityTimestampAgesFromMtime(t *testing.T) {
	svc, store, dir := newPurgeTestService(t)
	store.header = &RetentionHeader{}

	freshPath := writeAgedChat(t, dir, "fresh01", 0)
	stalePath := writeAgedChat(t, dir, "stale01", 48*time.Hour)

	svc.Purge(t.Context(), 24*time.Hour)

	if !exists(t, freshPath) {
		t.Errorf("chat with UpdatedAt=0 and a fresh mtime was purged: %s", freshPath)
	}
	if exists(t, stalePath) {
		t.Errorf("chat with UpdatedAt=0 and a 48h-old mtime survived a 24h retention: %s", stalePath)
	}
}

// capturePurgeLogs redirects the slog default into a buffer for one test; tests using it must not
// run in parallel. The log package's writer and flags are restored too, because slog.SetDefault
// repoints log and does not point it back for the stock handler.
func capturePurgeLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prevLogger, prevWriter, prevFlags := slog.Default(), log.Writer(), log.Flags()
	t.Cleanup(func() {
		slog.SetDefault(prevLogger)
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return buf
}

// TestPurge_PassSummaryReportsWhatThePassDid: the summary is the operator's only record of a purge,
// and a pass with no failure is not announced as one.
func TestPurge_PassSummaryReportsWhatThePassDid(t *testing.T) {
	svc, _, dir := newPurgeTestService(t)
	writeAgedChat(t, dir, "gone0001", 48*time.Hour)
	writeAgedChat(t, dir, "kept0001", 1*time.Hour)
	buf := capturePurgeLogs(t)

	svc.Purge(t.Context(), 24*time.Hour)

	got := buf.String()
	for _, want := range []string{"purged=1", "kept=1"} {
		if !strings.Contains(got, want) {
			t.Errorf("purge summary = %q, want it to contain %q (one stale chat, one live one)", got, want)
		}
	}
	if !strings.Contains(got, "level=INFO") {
		t.Errorf("purge summary = %q, want it logged at INFO: nothing failed in this pass", got)
	}
	if strings.Contains(got, "with errors") {
		t.Errorf("purge summary = %q, want no error summary: nothing failed in this pass", got)
	}
}

// TestPurge_NeverPurgesAChatHoldingADraft: the age test cannot see a chat someone is typing in, and
// the draft is the only copy of the words. What counts as drafting is pinned at
// chat.TestLoadRetentionHeader_*.
func TestPurge_NeverPurgesAChatHoldingADraft(t *testing.T) {
	cases := map[string]struct {
		drafting  bool
		wantKept  bool
		wantReaps []string
	}{
		"a chat with an unsent draft is defended": {
			drafting:  true,
			wantKept:  true,
			wantReaps: nil,
		},
		"a chat with nothing unsent is not": {
			drafting:  false,
			wantKept:  false,
			wantReaps: []string{"aged"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var rec purgeRecorder
			svc, store, dir := newPurgeTestService(t, WithOnPurge(rec.recordPurge))
			p := writeAgedChat(t, dir, "aged", 72*time.Hour)
			store.header = &RetentionHeader{Drafting: tc.drafting}

			svc.Purge(t.Context(), 24*time.Hour)

			if got := exists(t, p); got != tc.wantKept {
				t.Errorf("chat survived = %v, want %v (drafting %v)", got, tc.wantKept, tc.drafting)
			}
			if got := rec.sorted(); !slices.Equal(got, tc.wantReaps) {
				t.Errorf("onPurge fired for %v, want %v", got, tc.wantReaps)
			}
		})
	}
}

// TestPurge_AnExemptChatContributesNoDeadline: a draft-kept chat's deadline would fire immediately,
// purge nothing and re-arm forever.
func TestPurge_AnExemptChatContributesNoDeadline(t *testing.T) {
	svc, store, dir := newPurgeTestService(t)
	writeAgedChat(t, dir, "drafting", 72*time.Hour)
	store.header = &RetentionHeader{Drafting: true}

	res := svc.Purge(t.Context(), 24*time.Hour)

	if res.Kept != 1 {
		t.Fatalf("kept = %d, want 1: the drafting chat must be kept", res.Kept)
	}
	if !res.NextDeadline.IsZero() {
		t.Errorf("NextDeadline = %v, want none: an exempt chat's age is not a wake-up",
			res.NextDeadline)
	}
}

// TestPurge_AnUnreadableChatIsNotDefendedByADraft: an unreadable chat reports no draft, or a
// corrupt file is kept forever.
func TestPurge_AnUnreadableChatIsNotDefendedByADraft(t *testing.T) {
	svc, _, dir := newPurgeTestService(t)
	p := writeAgedChat(t, dir, "corrupt", 72*time.Hour)

	svc.Purge(t.Context(), 24*time.Hour)

	if exists(t, p) {
		t.Error("an unreadable expired chat survived; a load failure must not read as a draft")
	}
}

// TestPurge_NeverPurgesAChatWithAnOpenTab: reading an old chat stamps nothing the age test sees, so
// an open tab exempts it.
func TestPurge_NeverPurgesAChatWithAnOpenTab(t *testing.T) {
	cases := map[string]struct {
		open      map[string]bool
		wantKept  bool
		wantReaps []string
	}{
		"a chat on the strip is defended however old": {
			open:      map[string]bool{"aged": true},
			wantKept:  true,
			wantReaps: nil,
		},
		"a chat whose tab was closed is not": {
			open:      map[string]bool{"someone-else": true},
			wantKept:  false,
			wantReaps: []string{"aged"},
		},
		"no tabs open at all defends nothing": {
			open:      nil,
			wantKept:  false,
			wantReaps: []string{"aged"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var rec purgeRecorder
			svc, _, dir := newPurgeTestService(t,
				WithOnPurge(rec.recordPurge),
				WithOpenTabs(func(id marotte.ChatID) bool { return tc.open[string(id)] }))
			p := writeAgedChat(t, dir, "aged", 72*time.Hour)

			svc.Purge(t.Context(), 24*time.Hour)

			if got := exists(t, p); got != tc.wantKept {
				t.Errorf("chat survived = %v, want %v (open tabs %v)", got, tc.wantKept, tc.open)
			}
			if got := rec.sorted(); !slices.Equal(got, tc.wantReaps) {
				t.Errorf("onPurge fired for %v, want %v", got, tc.wantReaps)
			}
		})
	}
}

// TestPurge_TheOpenTabAndDraftExemptionsAreIndependent: either alone defends a chat; a test setting
// both would pass with the predicates ANDed.
func TestPurge_TheOpenTabAndDraftExemptionsAreIndependent(t *testing.T) {
	var rec purgeRecorder
	svc, store, dir := newPurgeTestService(t,
		WithOnPurge(rec.recordPurge),
		WithOpenTabs(func(id marotte.ChatID) bool { return id == "open-no-draft" }))
	openNoDraft := writeAgedChat(t, dir, "open-no-draft", 72*time.Hour)
	draftNoTab := writeAgedChat(t, dir, "draft-no-tab", 72*time.Hour)
	store.header = &RetentionHeader{Drafting: true}

	svc.Purge(t.Context(), 24*time.Hour)

	if !exists(t, openNoDraft) {
		t.Error("a chat with an open tab and no draft was purged; the tab alone must defend it")
	}
	if !exists(t, draftNoTab) {
		t.Error("a chat with a draft and no open tab was purged; the draft alone must defend it")
	}
	if got := rec.sorted(); len(got) != 0 {
		t.Errorf("onPurge fired for %v, want nothing", got)
	}
}

// TestPurgeScheduler_ATriggerEndsTheIdleBackOff: an all-exempt pass lands on the idle wait (ceiling
// an hour), so clearing the last exemption must Trigger a pass or the chat outlives its window by
// up to that hour.
func TestPurgeScheduler_ATriggerEndsTheIdleBackOff(t *testing.T) {
	var exempt atomic.Bool
	exempt.Store(true)
	passes := make(chan struct{}, 64)
	purged := make(chan marotte.ChatID, 8)
	svc, _, dir := newPurgeTestService(t,
		WithOnPurge(func(id marotte.ChatID, _ []string) { purged <- id }),
		WithOpenTabs(func(marotte.ChatID) bool {
			// Read BEFORE the handshake: a scheduler descheduled between send and load would purge
			// on the first pass and green a run where no Trigger did anything.
			answer := exempt.Load()
			select {
			case passes <- struct{}{}:
			default:
			}
			return answer
		}))
	chat := writeAgedChat(t, dir, "pinned", 500*time.Hour)

	sched := NewPurgeScheduler(svc, func() time.Duration { return time.Hour })
	sched.Start(t.Context())
	defer sched.Stop()

	// The predicate answering is the handshake for "the first pass reached this chat"; a sleep
	// would pass whether or not it ran.
	select {
	case <-passes:
	case <-time.After(3 * time.Second):
		t.Fatal("the first pass never consulted the open-tab predicate")
	}
	if !exists(t, chat) {
		t.Fatal("Setup: the exempt chat was purged on the first pass, so there is no " +
			"back-off to end")
	}

	exempt.Store(false)
	sched.Trigger()

	if got := recvWithin(t, purged, 3*time.Second); got != "pinned" {
		t.Errorf("the pass a Trigger ran purged %q, want pinned: a cleared exemption has "+
			"to be noticed on the wake rather than at the end of an hour-long back-off", got)
	}
}

// TestPurgeScheduler_SidePassRunsWithChatRetentionOff: a side pass reads its own setting, where a
// zero window means "now", not "never".
func TestPurgeScheduler_SidePassRunsWithChatRetentionOff(t *testing.T) {
	svc, _, _ := newPurgeTestService(t)
	calls := 0
	sched := NewPurgeScheduler(svc, func() time.Duration { return 0 },
		func(context.Context) PurgeResult { calls++; return PurgeResult{Purged: 1} })

	timer, _ := sched.purgeAndReschedule(t.Context())
	if timer != nil {
		timer.Stop()
	}

	if calls != 1 {
		t.Errorf("purgeAndReschedule(retention off) ran the side pass %d times, want 1", calls)
	}
}

// A side pass's deadline arms the timer even with chat retention off; otherwise a
// run kept on age would wait the hour ceiling past its own deadline.
func TestPurgeScheduler_ASidePassDeadlineArmsUnderRetentionOff(t *testing.T) {
	svc, _, _ := newPurgeTestService(t)
	sched := NewPurgeScheduler(svc, func() time.Duration { return 0 })
	deadline := time.Now().Add(10 * time.Minute)

	got := sched.armWait(0, PurgeResult{NextDeadline: deadline})

	if got >= maxWait || got < 9*time.Minute {
		t.Errorf("armWait(retention off, deadline in 10m) = %v, want about 10m", got)
	}
}

func TestMergePurgeResults_SumsCountsAndKeepsTheEarlierDeadline(t *testing.T) {
	early := time.Now().Add(time.Minute)
	late := early.Add(time.Hour)
	cases := []struct {
		desc string
		a, b PurgeResult
		want time.Time
	}{
		{desc: "first earlier", a: PurgeResult{NextDeadline: early}, b: PurgeResult{NextDeadline: late}, want: early},
		{desc: "second earlier", a: PurgeResult{NextDeadline: late}, b: PurgeResult{NextDeadline: early}, want: early},
		{desc: "first zero", b: PurgeResult{NextDeadline: late}, want: late},
		{desc: "second zero", a: PurgeResult{NextDeadline: late}, want: late},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			tc.a.Purged, tc.a.Kept, tc.a.Errors = 1, 2, 3
			tc.b.Purged, tc.b.Kept, tc.b.Errors = 10, 20, 30
			got := mergePurgeResults(tc.a, tc.b)
			if !got.NextDeadline.Equal(tc.want) {
				t.Errorf("mergePurgeResults().NextDeadline = %v, want %v", got.NextDeadline, tc.want)
			}
			if got.Purged != 11 || got.Kept != 22 || got.Errors != 33 {
				t.Errorf("mergePurgeResults() counts = %d/%d/%d, want 11/22/33", got.Purged, got.Kept, got.Errors)
			}
		})
	}
}
