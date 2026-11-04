package chat

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/chat/archive"
	"github.com/cplieger/marotte/internal/marotte"
)

const purgeWindow = 24 * time.Hour

// rewriteChat edits a record on disk behind the store, as a previous boot or a hand edit would leave it.
func rewriteChat(t *testing.T, s *Store, id marotte.ChatID, fn func(c *marotte.Chat)) {
	t.Helper()
	path := filepath.Join(s.dir, string(id), headerFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read chat %s: %v", id, err)
	}
	var c marotte.Chat
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("unmarshal chat %s: %v", id, err)
	}
	fn(&c)
	out, err := json.Marshal(&c)
	if err != nil {
		t.Fatalf("marshal chat %s: %v", id, err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatalf("write chat %s: %v", id, err)
	}
}

func holdKey(t *testing.T, s *Store, id marotte.ChatID, key string, scope KeepScope) {
	t.Helper()
	if _, err := s.Hold(t.Context(), id, key, scope); err != nil {
		t.Fatalf("Hold(%s, %s): %v", id, key, err)
	}
}

func runKey(t *testing.T, kind func(string) (string, bool)) string {
	t.Helper()
	k, ok := kind("wf_0a1b")
	if !ok {
		t.Fatal("Setup: the workflow id was refused")
	}
	return k
}

// TestPurgeIdle_DecidesFromTheRecordAlone walks every step of the compare-and-delete over a chat idle past the window
// (or inside it, where named): only a record with no live keep fact, no unsent words, a reconcile behind it and a last
// use before the cutoff is removed, and KeptBy names the first reason only past the cutoff.
func TestPurgeIdle_DecidesFromTheRecordAlone(t *testing.T) {
	cases := []struct {
		setup       func(t *testing.T, s *Store)
		name        string
		wantKeptBy  string
		wantOutcome archive.PurgeOutcome
		wantErr     bool
	}{
		{name: "no_fact_idle_past_the_window", wantOutcome: archive.PurgePurged},
		{
			name:        "open_tab",
			setup:       func(t *testing.T, s *Store) { holdKey(t, s, "c1", marotte.KeyTab, KeepDurable) },
			wantOutcome: archive.PurgeKept, wantKeptBy: archive.KeptByTab,
		},
		{
			name: "live_run",
			setup: func(t *testing.T, s *Store) {
				holdKey(t, s, "c1", runKey(t, marotte.KeyRun), KeepDurable)
			},
			wantOutcome: archive.PurgeKept, wantKeptBy: archive.KeptByRun,
		},
		{
			name: "open_run_tab",
			setup: func(t *testing.T, s *Store) {
				holdKey(t, s, "c1", runKey(t, marotte.KeyRunTab), KeepDurable)
			},
			wantOutcome: archive.PurgeKept, wantKeptBy: archive.KeptByRunTab,
		},
		{
			name:        "live_turn_this_boot",
			setup:       func(t *testing.T, s *Store) { holdKey(t, s, "c1", marotte.KeyTurn, keepProcess) },
			wantOutcome: archive.PurgeKept, wantKeptBy: archive.KeptByTurn,
		},
		{
			name: "turn_of_a_previous_boot_is_dead",
			setup: func(t *testing.T, s *Store) {
				rewriteChat(t, s, "c1", func(c *marotte.Chat) {
					c.Keep = map[string]marotte.KeepFact{marotte.KeyTurn: {Boot: "old-boot", Process: true}}
				})
			},
			wantOutcome: archive.PurgePurged,
		},
		{
			name: "durable_fact_of_a_previous_boot_is_live",
			setup: func(t *testing.T, s *Store) {
				rewriteChat(t, s, "c1", func(c *marotte.Chat) {
					c.Keep = map[string]marotte.KeepFact{marotte.KeyTab: {Boot: "old-boot"}}
				})
			},
			wantOutcome: archive.PurgeKept, wantKeptBy: archive.KeptByTab,
		},
		{
			name: "unrecognised_key_is_live",
			setup: func(t *testing.T, s *Store) {
				rewriteChat(t, s, "c1", func(c *marotte.Chat) {
					c.Keep = map[string]marotte.KeepFact{"future:x": {Boot: "old-boot", Process: true}}
				})
			},
			wantOutcome: archive.PurgeKept, wantKeptBy: archive.KeptByUnknown,
		},
		{
			name: "draft",
			setup: func(t *testing.T, s *Store) {
				rewriteChat(t, s, "c1", func(c *marotte.Chat) { c.Draft = "half a thought" })
			},
			wantOutcome: archive.PurgeKept, wantKeptBy: archive.KeptByWords,
		},
		{
			name: "queued_prompt",
			setup: func(t *testing.T, s *Store) {
				rewriteChat(t, s, "c1", func(c *marotte.Chat) {
					c.QueuedPrompts = []marotte.QueuedPrompt{{ID: "m-1", Text: "x"}}
				})
			},
			wantOutcome: archive.PurgeKept, wantKeptBy: archive.KeptByWords,
		},
		{
			name:        "not_reconciled_this_boot",
			setup:       func(t *testing.T, s *Store) { s.forgetReconciled("c1") },
			wantOutcome: archive.PurgeKept, wantKeptBy: archive.KeptByUnreconciled,
		},
		{
			name:        "a_run_no_chat_could_be_named_for",
			setup:       func(t *testing.T, s *Store) { s.HoldUnattributed("wf_0a1b") },
			wantOutcome: archive.PurgeKept, wantKeptBy: archive.KeptByUnattributed,
		},
		{
			name: "pending_hold",
			setup: func(t *testing.T, s *Store) {
				repair := failHeaderWrites(t, "c1")
				if _, err := s.HoldOrPend(t.Context(), "c1", runKey(t, marotte.KeyRun), KeepDurable); err != nil {
					t.Fatalf("HoldOrPend: %v", err)
				}
				repair()
			},
			wantOutcome: archive.PurgeKept, wantKeptBy: archive.KeptByPending,
		},
		{
			name: "no_last_use",
			setup: func(t *testing.T, s *Store) {
				rewriteChat(t, s, "c1", func(c *marotte.Chat) { c.UpdatedAt, c.KeepFilledAt = 0, 0 })
			},
			wantOutcome: archive.PurgeKept,
		},
		{
			name: "fill_in_inside_the_window",
			setup: func(t *testing.T, s *Store) {
				rewriteChat(t, s, "c1", func(c *marotte.Chat) { c.KeepFilledAt = time.Now().UnixMilli() })
			},
			wantOutcome: archive.PurgeKept,
		},
		{
			name: "fact_inside_the_window_is_not_counted",
			setup: func(t *testing.T, s *Store) {
				holdKey(t, s, "c1", marotte.KeyTab, KeepDurable)
				rewriteChat(t, s, "c1", func(c *marotte.Chat) { c.UpdatedAt = time.Now().UnixMilli() })
			},
			wantOutcome: archive.PurgeKept,
		},
		{
			name:        "run_mark_older_than_the_window",
			setup:       func(t *testing.T, s *Store) { holdRunMarkSince(t, s, time.Now().Add(-3*purgeWindow)) },
			wantOutcome: archive.PurgePurged,
		},
		{
			name: "run_mark_inside_the_refresh_slack",
			setup: func(t *testing.T, s *Store) {
				holdRunMarkSince(t, s, time.Now().Add(-purgeWindow-RunMarkRefresh/2))
			},
			wantOutcome: archive.PurgeKept, wantKeptBy: archive.KeptByRun,
		},
		{
			name:        "run_mark_with_no_since",
			setup:       func(t *testing.T, s *Store) { holdRunMarkSince(t, s, time.UnixMilli(0)) },
			wantOutcome: archive.PurgeKept, wantKeptBy: archive.KeptByRun,
		},
		{
			name:        "unreadable",
			setup:       func(t *testing.T, s *Store) { corruptHeader(t, s, "c1") },
			wantOutcome: archive.PurgeKept, wantErr: true,
		},
		{
			name:        "another_chats_id",
			setup:       func(t *testing.T, s *Store) { rewriteChat(t, s, "c1", func(c *marotte.Chat) { c.ID = "c2" }) },
			wantOutcome: archive.PurgeKept, wantErr: true,
		},
		{
			name: "absent",
			setup: func(t *testing.T, s *Store) {
				if _, err := s.Delete(t.Context(), "c1"); err != nil {
					t.Fatalf("Delete: %v", err)
				}
			},
			wantOutcome: archive.PurgeAbsent,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestStore(t)
			seedChat(t, s, "c1", "sess_a")
			ageChat(t, s, "c1", 3*purgeWindow)
			if tc.setup != nil {
				tc.setup(t, s)
			}

			v, err := s.PurgeIdle(t.Context(), "c1", time.Now().Add(-purgeWindow))

			if (err != nil) != tc.wantErr {
				t.Errorf("PurgeIdle error = %v, want error %v", err, tc.wantErr)
			}
			if v.Outcome != tc.wantOutcome {
				t.Errorf("Outcome = %v, want %v", v.Outcome, tc.wantOutcome)
			}
			if v.KeptBy != tc.wantKeptBy {
				t.Errorf("KeptBy = %q, want %q", v.KeptBy, tc.wantKeptBy)
			}
			_, statErr := os.Stat(filepath.Join(s.dir, "c1"))
			if gone := errors.Is(statErr, os.ErrNotExist); gone != (tc.wantOutcome != archive.PurgeKept) {
				t.Errorf("record removed = %v, want %v", gone, tc.wantOutcome != archive.PurgeKept)
			}
		})
	}
}

// TestPurgeIdle_FlushesAPendingHoldOntoTheRecord: the pending fact becomes a fact on the record, so the chat stays kept
// after the in-memory entry is gone.
func TestPurgeIdle_FlushesAPendingHoldOntoTheRecord(t *testing.T) {
	s, _ := newTestStore(t)
	seedChat(t, s, "c1")
	ageChat(t, s, "c1", 3*purgeWindow)
	key := runKey(t, marotte.KeyRun)
	repair := failHeaderWrites(t, "c1")
	if _, err := s.HoldOrPend(t.Context(), "c1", key, KeepDurable); err != nil {
		t.Fatalf("HoldOrPend: %v", err)
	}
	repair()

	cutoff := time.Now().Add(-purgeWindow)
	if v, err := s.PurgeIdle(t.Context(), "c1", cutoff); err != nil || v.KeptBy != archive.KeptByPending {
		t.Fatalf("first PurgeIdle = %+v, %v; want kept by pending", v, err)
	}
	if _, ok := readChat(t, s, "c1").Keep[key]; !ok {
		t.Errorf("the pending %s was not written onto the record", key)
	}
	if v, err := s.PurgeIdle(t.Context(), "c1", cutoff); err != nil || v.KeptBy != archive.KeptByRun {
		t.Errorf("second PurgeIdle = %+v, %v; want kept by the run fact on the record", v, err)
	}
}

// TestPurgeIdle_AReleaseWhileUnreadableDropsThePendingHold: the run ended, so its pended hold must not be flushed once
// the record reads again.
func TestPurgeIdle_AReleaseWhileUnreadableDropsThePendingHold(t *testing.T) {
	s, _ := newTestStore(t)
	seedChat(t, s, "c1")
	ageChat(t, s, "c1", 3*purgeWindow)
	key := runKey(t, marotte.KeyRun)
	repair := failHeaderWrites(t, "c1")
	if _, err := s.HoldOrPend(t.Context(), "c1", key, KeepDurable); err != nil {
		t.Fatalf("HoldOrPend: %v", err)
	}
	repair()
	restore := corruptHeader(t, s, "c1")
	if err := s.Release(t.Context(), "c1", key, EvidenceRunCancelled); err == nil {
		t.Fatal("Setup: Release over an unreadable record answered nil")
	}
	restore()

	v, err := s.PurgeIdle(t.Context(), "c1", time.Now().Add(-purgeWindow))

	if err != nil || v.Outcome != archive.PurgePurged {
		t.Errorf("PurgeIdle = %+v, %v; want purged", v, err)
	}
}

// TestPurgeIdle_AChatInsideTheWindowAnswersItsDeadline: the deadline is the last use plus the window, so the scheduler
// wakes when the chat becomes purgeable.
func TestPurgeIdle_AChatInsideTheWindowAnswersItsDeadline(t *testing.T) {
	s, _ := newTestStore(t)
	seedChat(t, s, "c1")
	ageChat(t, s, "c1", 20*time.Hour)
	last := readChat(t, s, "c1").UpdatedAt

	v, err := s.PurgeIdle(t.Context(), "c1", time.Now().Add(-purgeWindow))

	if err != nil || v.Outcome != archive.PurgeKept {
		t.Fatalf("PurgeIdle = %+v, %v; want kept", v, err)
	}
	want := time.UnixMilli(last).Add(purgeWindow)
	if d := v.Deadline.Sub(want); d < -time.Second || d > time.Second {
		t.Errorf("Deadline = %v, want about %v", v.Deadline, want)
	}
}

// holdRunMarkSince holds a run mark on c1 whose last sign of life was at.
func holdRunMarkSince(t *testing.T, s *Store, at time.Time) {
	t.Helper()
	key := runKey(t, marotte.KeyRun)
	holdKey(t, s, "c1", key, KeepDurable)
	rewriteChat(t, s, "c1", func(c *marotte.Chat) {
		f := c.Keep[key]
		f.Since = at.UnixMilli()
		c.Keep[key] = f
	})
}

// TestPurgeIdle_ARunMarkAnswersItsDeadline: a chat kept only by a run mark becomes purgeable one window after the
// mark's last sign of life plus the refresh slack, so the scheduler wakes then.
func TestPurgeIdle_ARunMarkAnswersItsDeadline(t *testing.T) {
	s, _ := newTestStore(t)
	seedChat(t, s, "c1")
	ageChat(t, s, "c1", 3*purgeWindow)
	since := time.Now().Add(-2 * time.Hour)
	holdRunMarkSince(t, s, since)

	v, err := s.PurgeIdle(t.Context(), "c1", time.Now().Add(-purgeWindow))

	if err != nil || v.Outcome != archive.PurgeKept || v.KeptBy != archive.KeptByRun {
		t.Fatalf("PurgeIdle = %+v, %v; want kept by the run mark", v, err)
	}
	want := since.Add(RunMarkRefresh + purgeWindow)
	if d := v.Deadline.Sub(want); d < -time.Second || d > time.Second {
		t.Errorf("Deadline = %v, want about %v", v.Deadline, want)
	}
}

// TestPurgeIdle_ARemovalTearsDownUntilEndTeardown: the removal answers the chain, tombstones the id, broadcasts the
// deletion with the minted version, and holds the chat and its chain in teardown until the caller ends it.
func TestPurgeIdle_ARemovalTearsDownUntilEndTeardown(t *testing.T) {
	s, b := newTestStore(t)
	seedChat(t, s, "c1", "sess_a", "sess_b")
	ageChat(t, s, "c1", 3*purgeWindow)
	b.reset()

	v, err := s.PurgeIdle(t.Context(), "c1", time.Now().Add(-purgeWindow))

	if err != nil || v.Outcome != archive.PurgePurged {
		t.Fatalf("PurgeIdle = %+v, %v; want purged", v, err)
	}
	if want := []string{"sess_a", "sess_b"}; !slices.Equal(v.SessionChain, want) {
		t.Errorf("SessionChain = %v, want %v", v.SessionChain, want)
	}
	frames := b.snapshot()
	if len(frames) != 1 || frames[0].Type != marotte.EventChatDeleted || frames[0].Subject == nil || frames[0].Subject.Version == "" {
		t.Errorf("broadcast %+v, want one chat_deleted stamped with the chats version", frames)
	}
	if _, err := s.Hold(t.Context(), "c1", marotte.KeyTab, KeepDurable); !errors.Is(err, ErrInTeardown) {
		t.Errorf("Hold during the teardown = %v, want ErrInTeardown", err)
	}
	if !s.SessionInTeardown("sess_a") {
		t.Error("the purged chain's session is not in teardown")
	}

	s.EndTeardown("c1", v.SessionChain)

	if _, err := s.Hold(t.Context(), "c1", marotte.KeyTab, KeepDurable); errors.Is(err, ErrInTeardown) || !errors.Is(err, ErrTombstoned) {
		t.Errorf("Hold after EndTeardown = %v, want ErrTombstoned and not ErrInTeardown", err)
	}
	if s.SessionInTeardown("sess_a") {
		t.Error("EndTeardown left the session in teardown")
	}
}
