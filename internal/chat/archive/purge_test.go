package archive

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/slogx/capture"
)

// TestPurge_RetentionCutoff asserts that chats last used before (now - maxAge) are purged, newer ones survive.
func TestPurge_RetentionCutoff(t *testing.T) {
	var rec purgeRecorder
	svc, store := newPurgeTestService(t, rec.recordPurge)

	olderPath := store.aged(t, "older", 25*time.Hour)
	newerPath := store.aged(t, "newer", 23*time.Hour)

	svc.Purge(t.Context(), 24*time.Hour)

	if exists(t, olderPath) {
		t.Errorf("chat older than cutoff (25h > 24h) survived: %s", olderPath)
	}
	if !exists(t, newerPath) {
		t.Errorf("chat newer than cutoff (23h < 24h) was purged: %s", newerPath)
	}
	if got := rec.sorted(); !slices.Equal(got, []string{"older"}) {
		t.Errorf("onPurge fired for %v, want [older]", got)
	}
}

// TestPurge_AsksOnlyForChatDirectories asserts that a plain file and a directory whose name is not a chat id are
// never handed to the store.
func TestPurge_AsksOnlyForChatDirectories(t *testing.T) {
	var rec purgeRecorder
	svc, store := newPurgeTestService(t, rec.recordPurge)
	store.aged(t, "valid01", 48*time.Hour)
	if err := os.WriteFile(filepath.Join(store.dir, "notes.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatalf("write notes: %v", err)
	}
	store.aged(t, "bad.id", 48*time.Hour)

	svc.Purge(t.Context(), 24*time.Hour)

	if got := rec.sorted(); !slices.Equal(got, []string{"valid01"}) {
		t.Errorf("onPurge fired for %v, want [valid01]", got)
	}
	if n := store.callCount(); n != 1 {
		t.Errorf("PurgeIdle called %d times, want 1 (only the valid chat directory)", n)
	}
}

// TestPurge_EmptyAndMissingDir verifies Purge is a safe no-op when the chat directory is empty or absent.
func TestPurge_EmptyAndMissingDir(t *testing.T) {
	t.Run("empty_chat_dir", func(t *testing.T) {
		var rec purgeRecorder
		svc, _ := newPurgeTestService(t, rec.recordPurge)
		svc.Purge(t.Context(), 24*time.Hour)
		if got := rec.sorted(); len(got) != 0 {
			t.Errorf("onPurge fired %v on empty dir, want none", got)
		}
	})

	t.Run("missing_chat_dir", func(t *testing.T) {
		var rec purgeRecorder
		svc := New(newFakeStore(filepath.Join(t.TempDir(), "absent")), rec.recordPurge)
		svc.Purge(t.Context(), 24*time.Hour)
		if got := rec.sorted(); len(got) != 0 {
			t.Errorf("onPurge fired %v on missing dir, want none", got)
		}
	})
}

// TestPurge_AnUnreadableChatIsCountedAsAnErrorAndKept: the store's error is the pass's error count, and onPurge never
// hears of the chat.
func TestPurge_AnUnreadableChatIsCountedAsAnErrorAndKept(t *testing.T) {
	var rec purgeRecorder
	svc, store := newPurgeTestService(t, rec.recordPurge)
	store.set(t, "corrupt", fakeChat{lastUse: time.Now().Add(-72 * time.Hour), err: errors.New("decode")})

	res := svc.Purge(t.Context(), 24*time.Hour)

	if res.Errors != 1 || res.Purged != 0 {
		t.Errorf("Purge = %+v, want one error and nothing purged", res)
	}
	if got := rec.sorted(); len(got) != 0 {
		t.Errorf("onPurge fired for %v, want none", got)
	}
}

// TestPurge_HandsTheSessionChainToOnPurge asserts that onPurge receives the chain the store read from the record it
// removed.
func TestPurge_HandsTheSessionChainToOnPurge(t *testing.T) {
	var rec purgeRecorder
	svc, store := newPurgeTestService(t, rec.recordPurge)
	store.set(t, "chained", fakeChat{lastUse: time.Now().Add(-48 * time.Hour), chain: []string{"sess_old", "sess_new"}})

	svc.Purge(t.Context(), 24*time.Hour)

	if got, want := rec.chainFor("chained"), []string{"sess_old", "sess_new"}; !slices.Equal(got, want) {
		t.Errorf("onPurge received chain %v, want %v", got, want)
	}
}

// TestPurge_AChatKeptByAFactContributesNoDeadline: its age deadline is already past, so a timer aimed at it would fire
// immediately, purge nothing and re-arm forever.
func TestPurge_AChatKeptByAFactContributesNoDeadline(t *testing.T) {
	svc, store := newPurgeTestService(t, nil)
	store.set(t, "held", fakeChat{lastUse: time.Now().Add(-72 * time.Hour), keptBy: KeptByTab})

	res := svc.Purge(t.Context(), 24*time.Hour)

	if res.Kept != 1 {
		t.Fatalf("kept = %d, want 1", res.Kept)
	}
	if !res.NextDeadline.IsZero() {
		t.Errorf("NextDeadline = %v, want none", res.NextDeadline)
	}
	if res.KeptBy[KeptByTab] != 1 {
		t.Errorf("KeptBy = %v, want tab counted once", res.KeptBy)
	}
}

// capturePurgeLogs redirects the slog default into a buffer for one test; tests using it must not run in parallel.
func capturePurgeLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	capture.SwapDefault(t, slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return buf
}

// TestPurge_PassSummaryReportsWhatThePassDid: the summary is the operator's only record of a purge, and a pass with no
// failure is not announced as one.
func TestPurge_PassSummaryReportsWhatThePassDid(t *testing.T) {
	svc, store := newPurgeTestService(t, nil)
	store.aged(t, "gone0001", 48*time.Hour)
	store.aged(t, "kept0001", 1*time.Hour)
	buf := capturePurgeLogs(t)

	svc.Purge(t.Context(), 24*time.Hour)

	got := buf.String()
	for _, want := range []string{"purged=1", "kept=1"} {
		if !strings.Contains(got, want) {
			t.Errorf("purge summary = %q, want it to contain %q (one stale chat, one fresh one)", got, want)
		}
	}
	if strings.Contains(got, "with errors") {
		t.Errorf("purge summary = %q, want no error summary: nothing failed in this pass", got)
	}
}

// TestPurge_CountLineNamesEveryKindOnEveryPass: the count line is the series an alert reads, so a pass logs every kind,
// zeros included, and counts each chat past retention under the reason that kept it.
func TestPurge_CountLineNamesEveryKindOnEveryPass(t *testing.T) {
	const line = `msg="chat purge: chats past retention kept by a keep fact"`
	t.Run("zeros", func(t *testing.T) {
		svc, _ := newPurgeTestService(t, nil)
		buf := capturePurgeLogs(t)

		svc.Purge(t.Context(), 24*time.Hour)

		got := buf.String()
		if !strings.Contains(got, line) {
			t.Fatalf("logs = %q, want the count line", got)
		}
		for _, k := range []string{"tab", "run", "runtab", "turn", "unknown", "pending", "unreconciled", "unattributed", "words"} {
			if !strings.Contains(got, " "+k+"=0") {
				t.Errorf("count line lacks %s=0: %q", k, got)
			}
		}
	})
	t.Run("one_each", func(t *testing.T) {
		svc, store := newPurgeTestService(t, nil)
		old := time.Now().Add(-72 * time.Hour)
		store.set(t, "a", fakeChat{lastUse: old, keptBy: KeptByRun})
		store.set(t, "b", fakeChat{lastUse: old, keptBy: KeptByWords})
		store.set(t, "c", fakeChat{lastUse: time.Now(), keptBy: KeptByTab})
		buf := capturePurgeLogs(t)

		svc.Purge(t.Context(), 24*time.Hour)

		got := buf.String()
		for _, want := range []string{" run=1", " words=1", " tab=0"} {
			if !strings.Contains(got, want) {
				t.Errorf("count line lacks %q (a chat inside the window is not counted): %q", want, got)
			}
		}
	})
}

// TestPurgeScheduler_InitialEvaluationPurges verifies Start runs an initial purge evaluation that removes an
// over-retention chat.
func TestPurgeScheduler_InitialEvaluationPurges(t *testing.T) {
	purged := make(chan marotte.ChatID, 8)
	svc, store := newPurgeTestService(t, func(id marotte.ChatID, _ []string) { purged <- id })
	store.aged(t, "sched1", 48*time.Hour)

	sched := NewPurgeScheduler(svc, func() time.Duration { return 24 * time.Hour })
	sched.Start(t.Context())
	defer sched.Stop()

	if got := recvWithin(t, purged, 3*time.Second); got != "sched1" {
		t.Errorf("initial evaluation purged %q, want sched1", got)
	}
}

// TestPurgeScheduler_ReArmsAndProcessesSecondTrigger verifies the loop keeps processing triggers after the first pass.
func TestPurgeScheduler_ReArmsAndProcessesSecondTrigger(t *testing.T) {
	purged := make(chan marotte.ChatID, 8)
	svc, store := newPurgeTestService(t, func(id marotte.ChatID, _ []string) { purged <- id })
	store.aged(t, "first", 48*time.Hour)

	sched := NewPurgeScheduler(svc, func() time.Duration { return 24 * time.Hour })
	sched.Start(t.Context())
	defer sched.Stop()

	if got := recvWithin(t, purged, 3*time.Second); got != "first" {
		t.Fatalf("first pass purged %q, want first", got)
	}

	store.aged(t, "second", 48*time.Hour)
	sched.Trigger()
	if got := recvWithin(t, purged, 3*time.Second); got != "second" {
		t.Errorf("second trigger purged %q, want second", got)
	}
}

// TestPurgeScheduler_ZeroRetentionSkipsPurge verifies a retention of 0 ("keep forever") disables purging entirely.
func TestPurgeScheduler_ZeroRetentionSkipsPurge(t *testing.T) {
	purged := make(chan marotte.ChatID, 8)
	svc, store := newPurgeTestService(t, func(id marotte.ChatID, _ []string) { purged <- id })
	chatPath := store.aged(t, "keepforever", 9000*time.Hour)

	sched := NewPurgeScheduler(svc, func() time.Duration { return 0 })
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

// TestPurgeScheduler_StopClosesDone verifies Stop drains the scheduler goroutine and is safe to call more than once.
func TestPurgeScheduler_StopClosesDone(t *testing.T) {
	svc, _ := newPurgeTestService(t, nil)
	sched := NewPurgeScheduler(svc, func() time.Duration { return 24 * time.Hour })
	sched.Start(t.Context())
	sched.Stop()

	select {
	case <-sched.done:
	default:
		t.Error("done channel not closed after Stop()")
	}

	sched.Stop()
}

// TestPurgeScheduler_ContextCancellationStopsLoop pins the ctx exit arm: asserting through Stop would pass even if
// that arm were gone.
func TestPurgeScheduler_ContextCancellationStopsLoop(t *testing.T) {
	svc, _ := newPurgeTestService(t, nil)
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

// TestPurgeScheduler_TriggerAfterStopIsNoop verifies Trigger is a safe no-op once the scheduler is stopped.
func TestPurgeScheduler_TriggerAfterStopIsNoop(t *testing.T) {
	svc, _ := newPurgeTestService(t, nil)
	sched := NewPurgeScheduler(svc, func() time.Duration { return 24 * time.Hour })
	sched.Start(t.Context())
	sched.Stop()
	sched.Trigger()
}

// TestPurgeScheduler_StopWithoutStart verifies Stop is safe before Start.
func TestPurgeScheduler_StopWithoutStart(t *testing.T) {
	svc, _ := newPurgeTestService(t, nil)
	sched := NewPurgeScheduler(svc, func() time.Duration { return 24 * time.Hour })
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

// TestPurgeScheduler_RescheduleWithZeroRetentionDoesNotPurge drives purgeAndReschedule directly: a retention of 0
// skips the purge entirely.
func TestPurgeScheduler_RescheduleWithZeroRetentionDoesNotPurge(t *testing.T) {
	var rec purgeRecorder
	svc, store := newPurgeTestService(t, rec.recordPurge)
	chatPath := store.aged(t, "keepforever", 9000*time.Hour)

	sched := NewPurgeScheduler(svc, func() time.Duration { return 0 })
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

// TestPurgeScheduler_APassWithNothingToPurgeBacksOff: a kept chat must not pin the wake-up in the past, or the loop
// re-scans every few seconds forever.
func TestPurgeScheduler_APassWithNothingToPurgeBacksOff(t *testing.T) {
	svc, store := newPurgeTestService(t, nil)
	pinned := store.set(t, "pinned", fakeChat{lastUse: time.Now().Add(-500 * time.Hour), keptBy: KeptByTab})
	sched := NewPurgeScheduler(svc, func() time.Duration { return time.Hour })

	first := sched.armWait(time.Hour, svc.Purge(t.Context(), time.Hour))
	if first < idleBase {
		t.Fatalf("first idle wait = %v, want at least %v: a kept chat must not pull the "+
			"wake-up down to the floor", first, idleBase)
	}
	second := sched.armWait(time.Hour, svc.Purge(t.Context(), time.Hour))
	if second <= first {
		t.Errorf("second idle wait = %v, want longer than the first (%v): consecutive passes "+
			"that purge nothing must back off", second, first)
	}
	if !exists(t, pinned) {
		t.Error("the kept chat was purged; the premise of this test is gone")
	}
}

// Retention off is not a reason to go dark, and not a reason to spin either: the settings path does not Trigger, so
// the loop re-checks on the ceiling.
func TestPurgeScheduler_RetentionOffWaitsTheCeiling(t *testing.T) {
	svc, _ := newPurgeTestService(t, nil)
	sched := NewPurgeScheduler(svc, func() time.Duration { return 0 })

	if got := sched.armWait(0, PurgeResult{}); got != maxWait {
		t.Errorf("armWait(retention off) = %v, want the %v ceiling", got, maxWait)
	}
}

// TestPurgeScheduler_ArmsTheEarliestAgeKeptDeadline: a chat kept on age arms a timer at its own deadline (last use
// plus window).
func TestPurgeScheduler_ArmsTheEarliestAgeKeptDeadline(t *testing.T) {
	svc, store := newPurgeTestService(t, nil)
	retention := time.Hour
	store.aged(t, "nearer", 50*time.Minute)
	store.aged(t, "farther", 10*time.Minute)
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

// A deadline moments away, or already reached, arms the floor rather than a zero-length timer.
func TestPurgeScheduler_FloorsADeadlineThatIsUponUs(t *testing.T) {
	svc, _ := newPurgeTestService(t, nil)
	sched := NewPurgeScheduler(svc, func() time.Duration { return time.Hour })

	got := sched.armWait(time.Hour, PurgeResult{Kept: 1, NextDeadline: time.Now()})
	if got != minWait {
		t.Errorf("armWait for a deadline that is upon us = %v, want the %v floor", got, minWait)
	}
}

// A pass that PURGED something resets the back-off, so the next idle pass starts from the base again.
func TestPurgeScheduler_APurgeResetsTheBackOff(t *testing.T) {
	svc, _ := newPurgeTestService(t, nil)
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

// TestPurgeScheduler_AlwaysArmsATimer: purgeAndReschedule never returns a nil timer, or after an empty dir or "keep
// forever" the loop has no wake-up but Trigger.
func TestPurgeScheduler_AlwaysArmsATimer(t *testing.T) {
	cases := map[string]struct {
		aged      bool
		retention time.Duration
	}{
		"empty_directory_retention_on":  {aged: false, retention: 24 * time.Hour},
		"chats_present_retention_off":   {aged: true, retention: 0},
		"empty_directory_retention_off": {aged: false, retention: 0},
		"keep_forever":                  {aged: true, retention: -1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc, store := newPurgeTestService(t, nil)
			if tc.aged {
				store.aged(t, "aged", 48*time.Hour)
			}
			sched := NewPurgeScheduler(svc, func() time.Duration { return tc.retention })

			timer, timerC := sched.purgeAndReschedule(t.Context())
			if timer == nil || timerC == nil {
				t.Fatalf("purgeAndReschedule() = (%v, %v), want an armed timer", timer, timerC)
			}
			timer.Stop()
		})
	}
}

// TestPurgeScheduler_CapsTheArmedWait asserts that without the cap a 30-day retention sleeps ~30 days and no settings
// change can shorten it. The premise (deadline beyond the ceiling) is asserted first.
func TestPurgeScheduler_CapsTheArmedWait(t *testing.T) {
	svc, store := newPurgeTestService(t, nil)
	store.aged(t, "fresh", 0)
	retention := 30 * 24 * time.Hour
	sched := NewPurgeScheduler(svc, func() time.Duration { return retention })

	res := svc.Purge(t.Context(), retention)
	natural := time.Until(res.NextDeadline)
	if natural <= maxWait {
		t.Fatalf("natural wait %v does not exceed the %v ceiling, so this test's "+
			"premise no longer holds; pick a longer retention", natural, maxWait)
	}
	if armed := sched.armWait(retention, res); armed != maxWait {
		t.Errorf("armWait = %v, want the %v ceiling (natural wait was %v)", armed, maxWait, natural)
	}
}

// TestPurgeScheduler_ATriggerEndsTheIdleBackOff: a pass where every chat is kept lands on the idle wait (ceiling an
// hour), so the end of the last keep reason must Trigger a pass or the chat outlives its window by up to that hour.
func TestPurgeScheduler_ATriggerEndsTheIdleBackOff(t *testing.T) {
	purged := make(chan marotte.ChatID, 8)
	svc, store := newPurgeTestService(t, func(id marotte.ChatID, _ []string) { purged <- id })
	old := time.Now().Add(-500 * time.Hour)
	pinned := store.set(t, "pinned", fakeChat{lastUse: old, keptBy: KeptByTab})

	sched := NewPurgeScheduler(svc, func() time.Duration { return time.Hour })
	sched.Start(t.Context())
	defer sched.Stop()
	deadline := time.Now().Add(3 * time.Second)
	for store.callCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the first pass never asked the store about the chat")
		}
		time.Sleep(time.Millisecond)
	}
	if !exists(t, pinned) {
		t.Fatal("Setup: the kept chat was purged on the first pass, so there is no back-off to end")
	}

	store.set(t, "pinned", fakeChat{lastUse: old})
	sched.Trigger()

	if got := recvWithin(t, purged, 3*time.Second); got != "pinned" {
		t.Errorf("the pass a Trigger ran purged %q, want pinned", got)
	}
}

// TestPurgeScheduler_SidePassRunsWithChatRetentionOff: a side pass reads its own setting, where a zero window means
// "now", not "never".
func TestPurgeScheduler_SidePassRunsWithChatRetentionOff(t *testing.T) {
	svc, _ := newPurgeTestService(t, nil)
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

// A side pass's deadline arms the timer even with chat retention off; otherwise a run kept on age would wait the hour
// ceiling past its own deadline.
func TestPurgeScheduler_ASidePassDeadlineArmsUnderRetentionOff(t *testing.T) {
	svc, _ := newPurgeTestService(t, nil)
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
		{desc: "first_earlier", a: PurgeResult{NextDeadline: early}, b: PurgeResult{NextDeadline: late}, want: early},
		{desc: "second_earlier", a: PurgeResult{NextDeadline: late}, b: PurgeResult{NextDeadline: early}, want: early},
		{desc: "first_zero", b: PurgeResult{NextDeadline: late}, want: late},
		{desc: "second_zero", a: PurgeResult{NextDeadline: late}, want: late},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			tc.a.Purged, tc.a.Kept, tc.a.Errors = 1, 2, 3
			tc.b.Purged, tc.b.Kept, tc.b.Errors = 10, 20, 30
			tc.a.KeptBy = map[string]int{KeptByTab: 1, KeptByRun: 2}
			tc.b.KeptBy = map[string]int{KeptByTab: 4}
			got := mergePurgeResults(tc.a, tc.b)
			if !got.NextDeadline.Equal(tc.want) {
				t.Errorf("mergePurgeResults().NextDeadline = %v, want %v", got.NextDeadline, tc.want)
			}
			if got.Purged != 11 || got.Kept != 22 || got.Errors != 33 {
				t.Errorf("mergePurgeResults() counts = %d/%d/%d, want 11/22/33", got.Purged, got.Kept, got.Errors)
			}
			if got.KeptBy[KeptByTab] != 5 || got.KeptBy[KeptByRun] != 2 {
				t.Errorf("mergePurgeResults().KeptBy = %v, want tab 5, run 2", got.KeptBy)
			}
		})
	}
}
