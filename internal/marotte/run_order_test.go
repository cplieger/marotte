package marotte

import (
	"slices"
	"testing"
)

func row(id string, s RunEntryState) RunEntry {
	return RunEntry{ID: id, Kind: RunKindRow, State: s, Origin: RunFromQueue, Row: &QueuedPrompt{ID: id, Text: id}}
}

func turn(id string, s RunEntryState) RunEntry {
	return RunEntry{
		ID: id, Kind: RunKindRow, State: s, Origin: RunFromPrompt, Turn: id, Steerable: true,
		Row: &QueuedPrompt{ID: id, Text: id},
	}
}

func wire(id string, s RunEntryState) RunEntry {
	return RunEntry{ID: id, Kind: RunKindTurn, State: s, Origin: RunFromWire, Turn: id, Steerable: true}
}

func fold(id string, admits bool) RunEntry {
	return RunEntry{
		ID: id, Kind: RunKindTurn, State: RunRunning, Origin: RunFromWire, Turn: id, Yields: true,
		Admits: admits,
	}
}

func compaction(s RunEntryState) RunEntry {
	return RunEntry{ID: "compact-1", Kind: RunKindCompaction, State: s, Origin: RunFromRequest}
}

func runEntry() RunEntry {
	return RunEntry{ID: "run-1", Kind: RunKindTurn, State: RunRunning, Origin: RunFromRun}
}

func turns(n int) RunWait           { return RunWait{After: waitTurns, Turns: n} }
func thisTurn() RunWait             { return RunWait{After: waitTurns, Turns: 1, This: true} }
func waitOf(a RunWaitAfter) RunWait { return RunWait{After: a} }

func TestRunViewOf(t *testing.T) {
	type want struct {
		send    SendVerb
		cancel  string
		next    RunWait
		unread  RunWait
		waits   map[string]RunWait
		arrows  map[string]SendNowKind
		carries string
	}
	cases := []struct {
		name    string
		mode    InterruptMode
		entries []RunEntry
		want    want
	}{
		{name: "idle", entries: nil, want: want{send: SendPrompt, next: waitOf(waitNext), unread: waitOf(waitNext)}},
		{
			name: "idle behind a pending row not taken", entries: []RunEntry{row("m-q1", RunPending)},
			want: want{
				send: SendQueue, next: turns(1), unread: waitOf(waitNext),
				waits: map[string]RunWait{"m-q1": waitOf(waitNext)}, arrows: map[string]SendNowKind{}, carries: "m-q1",
			},
		},
		{
			name:    "a running steerable turn in steer mode",
			entries: []RunEntry{turn("A", RunRunning), row("m-q1", RunPending), row("m-q2", RunPending)},
			want: want{
				send: SendSteer, cancel: "A", next: turns(3), unread: thisTurn(),
				waits:   map[string]RunWait{"A": waitOf(waitRunning), "m-q1": thisTurn(), "m-q2": turns(2)},
				arrows:  map[string]SendNowKind{"m-q1": sendNowStop, "m-q2": sendNowStop},
				carries: "m-q1",
			},
		},
		{
			name: "a running turn in queue mode", mode: InterruptQueue, entries: []RunEntry{turn("A", RunRunning)},
			want: want{send: SendQueue, cancel: "A", next: thisTurn(), unread: thisTurn()},
		},
		{
			name: "a starting turn queues", entries: []RunEntry{turn("A", RunStarting)},
			want: want{
				send: SendQueue, cancel: "A", next: thisTurn(), unread: thisTurn(),
				waits: map[string]RunWait{"A": waitOf(waitStarting)}, carries: "A",
			},
		},
		{
			name:    "a stopping head counts no turn",
			entries: []RunEntry{turn("A", RunStopping), row("m-q1", RunPending), row("m-q0", RunPaused)},
			want: want{
				send: SendQueue, next: turns(1), unread: waitOf(waitNext),
				waits:   map[string]RunWait{"m-q1": waitOf(waitNext), "m-q0": turns(1)},
				arrows:  map[string]SendNowKind{"m-q0": sendNowNow},
				carries: "m-q1",
			},
		},
		{
			name: "a closing head with one row", entries: []RunEntry{turn("A", RunClosing), row("m-q1", RunPending)},
			want: want{
				send: SendQueue, next: turns(1), unread: waitOf(waitNext),
				waits: map[string]RunWait{"m-q1": waitOf(waitNext)}, carries: "m-q1",
			},
		},
		{
			name: "a closing head with a compaction pending",
			entries: []RunEntry{
				turn("A", RunClosing), compaction(RunPending), row("m-q1", RunPending),
				row("m-q2", RunPending),
			},
			want: want{
				send: SendQueue, next: turns(2), unread: waitOf(WaitCompaction),
				waits: map[string]RunWait{
					"compact-1": waitOf(waitNext), "m-q1": waitOf(WaitCompaction),
					"m-q2": turns(1),
				},
				arrows:  map[string]SendNowKind{"m-q2": SendNowAfterCompaction},
				carries: "m-q1",
			},
		},
		{
			name: "a running turn with a compaction pending",
			entries: []RunEntry{
				turn("A", RunRunning), compaction(RunPending), row("m-q1", RunPending),
				row("m-q2", RunPending),
			},
			want: want{
				send: SendSteer, cancel: "A", next: turns(3), unread: waitOf(WaitCompaction),
				waits:   map[string]RunWait{"m-q1": waitOf(WaitCompaction), "m-q2": turns(2)},
				arrows:  map[string]SendNowKind{"m-q2": SendNowStopThenCompaction},
				carries: "m-q1",
			},
		},
		{
			name:    "a running compaction",
			entries: []RunEntry{compaction(RunRunning), row("m-new", RunPending), row("m-q0", RunPaused)},
			want: want{
				send: SendQueue, next: turns(1), unread: waitOf(WaitCompaction),
				waits:   map[string]RunWait{"compact-1": waitOf(waitRunning), "m-new": waitOf(WaitCompaction), "m-q0": turns(1)},
				arrows:  map[string]SendNowKind{"m-q0": SendNowAfterCompaction},
				carries: "m-new",
			},
		},
		{
			name:    "kiro-cli's turn under a compaction reads as if the compaction stood behind it",
			entries: []RunEntry{compaction(RunRunning), wire("w", RunRunning), row("m-q1", RunPending)},
			want: want{
				send: SendQueue, cancel: "w", next: turns(2), unread: waitOf(WaitCompaction),
				waits:   map[string]RunWait{"w": waitOf(waitRunning), "m-q1": waitOf(WaitCompaction)},
				carries: "m-q1",
			},
		},
		{
			name: "a wire turn beside a taken turn counts two turns", entries: []RunEntry{
				wire("w", RunRunning), row("m-1", RunStarting),
				row("m-q1", RunPending),
			},
			want: want{
				send: SendQueue, cancel: "w", next: turns(3), unread: turns(2),
				waits:   map[string]RunWait{"m-1": waitOf(waitStarting), "m-q1": turns(2)},
				arrows:  map[string]SendNowKind{"m-q1": sendNowNow},
				carries: "m-1",
			},
		},
		{
			name: "a fold turn ahead of its compaction before the claim", entries: []RunEntry{
				fold("f", true), compaction(RunStarting),
				row("m-new", RunPending),
			},
			want: want{
				send: SendQueue, cancel: "f", next: turns(1), unread: waitOf(WaitCompaction),
				waits:   map[string]RunWait{"compact-1": waitOf(waitStarting), "m-new": waitOf(WaitCompaction)},
				carries: "m-new",
			},
		},
		{
			name: "a fold turn ahead of its compaction after the claim cancels nothing", entries: []RunEntry{func() RunEntry {
				e := fold("f", true)
				e.State = RunClosing
				return e
			}(), compaction(RunStarting)},
			want: want{send: SendQueue, next: waitOf(WaitCompaction), unread: waitOf(WaitCompaction)},
		},
		{
			name: "a yielding head that admits prompts", entries: []RunEntry{fold("f", true)},
			want: want{send: SendPrompt, cancel: "f", next: thisTurn(), unread: thisTurn()},
		},
		{
			name: "a yielding head kiro-cli keeps busy queues", entries: []RunEntry{fold("f", false)},
			want: want{send: SendQueue, cancel: "f", next: thisTurn(), unread: thisTurn()},
		},
		{
			name: "a yielding head with a row pending queues", entries: []RunEntry{
				fold("f", true),
				row("m-q1", RunPending),
			},
			want: want{
				send: SendQueue, cancel: "f", next: turns(2), unread: thisTurn(),
				waits: map[string]RunWait{"m-q1": thisTurn()}, arrows: map[string]SendNowKind{"m-q1": sendNowStop},
				carries: "m-q1",
			},
		},
		{
			name: "a run entry", entries: []RunEntry{runEntry(), row("m-new", RunPending)},
			want: want{
				send: SendQueue, cancel: "run-1", next: turns(2), unread: thisTurn(),
				waits: map[string]RunWait{"m-new": thisTurn()}, arrows: map[string]SendNowKind{"m-new": sendNowNow},
				carries: "m-new",
			},
		},
		{
			name: "a held row", entries: []RunEntry{func() RunEntry { e := row("m-h", RunHeld); return e }()},
			want: want{
				send: SendPrompt, next: waitOf(waitNext), unread: waitOf(waitNext),
				waits: map[string]RunWait{"m-h": waitOf(waitHeld)}, arrows: map[string]SendNowKind{},
			},
		},
		{
			name: "the failsafe's compaction and resume behind the turn it stopped",
			entries: []RunEntry{
				turn("A", RunStopping), func() RunEntry { e := compaction(RunPending); e.Origin = RunFromFailsafe; return e }(),
				failsafeResume(), row("m-q1", RunPaused), row("m-q2", RunPaused),
			},
			want: want{
				send: SendQueue, next: turns(1), unread: waitOf(WaitCompaction),
				waits: map[string]RunWait{
					"compact-1": waitOf(waitNext), "m-resume": waitOf(WaitCompaction), "m-q1": turns(1), "m-q2": turns(2),
				},
				arrows: map[string]SendNowKind{
					"m-q1": SendNowAfterCompaction, "m-q2": SendNowAfterCompaction,
				},
				carries: "m-resume",
			},
		},
		{
			name:    "a next row carries the unread steers",
			entries: []RunEntry{turn("A", RunRunning), func() RunEntry { e := row("m-q1", RunPending); e.Next = true; return e }()},
			want:    want{send: SendSteer, cancel: "A", next: turns(2), unread: thisTurn(), carries: "m-q1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := RunViewOf(tc.entries, 7, tc.mode)
			if v.Version != 7 {
				t.Errorf("version: got %d", v.Version)
			}
			if v.Send != tc.want.send {
				t.Errorf("send: got %q, want %q", v.Send, tc.want.send)
			}
			if v.Cancel != tc.want.cancel {
				t.Errorf("cancel: got %q, want %q", v.Cancel, tc.want.cancel)
			}
			if v.NextWait != tc.want.next {
				t.Errorf("next_wait: got %+v, want %+v", v.NextWait, tc.want.next)
			}
			if v.UnreadWait != tc.want.unread {
				t.Errorf("unread_wait: got %+v, want %+v", v.UnreadWait, tc.want.unread)
			}
			for _, e := range v.Entries {
				if w, ok := tc.want.waits[e.ID]; ok && e.Wait != w {
					t.Errorf("%s wait: got %+v, want %+v", e.ID, e.Wait, w)
				}
				if tc.want.arrows != nil && e.SendNow != tc.want.arrows[e.ID] {
					t.Errorf("%s send_now: got %q, want %q", e.ID, e.SendNow, tc.want.arrows[e.ID])
				}
				if e.CarriesUnread != (e.ID == tc.want.carries) {
					t.Errorf("%s carries_unread: got %v", e.ID, e.CarriesUnread)
				}
			}
		})
	}
}

func TestVerbFor_Labelled(t *testing.T) {
	running := []RunEntry{turn("A", RunRunning)}
	if got := VerbFor(running, InterruptSteer, SendLabelled); got != SendQueue {
		t.Errorf("labelled send mid-turn: got %q, want queue", got)
	}
	if got := VerbFor(nil, InterruptSteer, SendQueuePrompt); got != SendPrompt {
		t.Errorf("queue_prompt on an idle list: got %q, want prompt (the caller's no_turn)", got)
	}
	if got := VerbFor([]RunEntry{fold("f", true)}, InterruptSteer, SendQueuePrompt); got != SendQueue {
		t.Errorf("queue_prompt beside a yielding head: got %q, want queue", got)
	}
	notSteerable := turn("A", RunRunning)
	notSteerable.Steerable = false
	if got := VerbFor([]RunEntry{notSteerable}, InterruptSteer, SendPlain); got != SendQueue {
		t.Errorf("a turn that is not steerable: got %q, want queue", got)
	}
}

func TestInsertAt_BeforeTheFirstPausedRow(t *testing.T) {
	entries := []RunEntry{turn("A", RunStopping), row("m-q1", RunPending), row("m-q0", RunPaused)}
	if got := insertAt(entries); got != 2 {
		t.Errorf("got %d, want 2", got)
	}
}

func TestSanitizeRun(t *testing.T) {
	carried := row("m-c", RunHeld)
	carried.Row.Resends = []string{"s1"}
	taken := row("m-t", RunStarting)
	opened := row("m-o", RunStarting)
	opened.Turn = "m-o"
	next := row("m-n", RunPending)
	next.Next = true
	in := []RunEntry{
		wire("w", RunRunning), compaction(RunPending), opened, taken, row("m-q1", RunPending),
		{ID: "u-1", Kind: RunKindUnread, State: RunPending, Row: &QueuedPrompt{ID: "u-1"}},
		{ID: "r-1", Kind: RunKindRetry, State: RunPending, Row: &QueuedPrompt{ID: "r-1"}},
		next, row("m-q0", RunPaused), row("m-h", RunHeld), carried, failsafeResume(),
	}
	ids := func(es []RunEntry) (out []string) {
		for _, e := range es {
			out = append(out, e.ID+":"+string(e.State))
		}
		return out
	}
	t.Run("a later life pauses the rows and keeps held rows held", func(t *testing.T) {
		got := ids(SanitizeRun(in, false))
		want := []string{"m-t:paused", "m-q1:paused", "m-n:paused", "m-q0:paused", "m-h:held", "m-c:held"}
		if !slices.Equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
	t.Run("a process's first life holds every row", func(t *testing.T) {
		got := ids(SanitizeRun(in, true))
		want := []string{"m-t:held", "m-q1:held", "m-n:held", "m-q0:held", "m-h:held", "m-c:held"}
		if !slices.Equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
	t.Run("send-now marks do not outlive the life", func(t *testing.T) {
		for _, e := range SanitizeRun(in, false) {
			if e.Next {
				t.Errorf("%s kept Next", e.ID)
			}
		}
	})
}

// KAS re-prompts what a session/cancel cleared whoever sent the cancel, so marotte's failsafe stop reads as the
// reader's does; a teardown's cancel is followed by the session's close.
func TestTurnStopCause_KASRewakes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause TurnStopCause
		want  bool
	}{
		{name: "no stop", cause: TurnStopNone, want: false},
		{name: "the reader's stop", cause: TurnStopReader, want: true},
		{name: "a teardown", cause: TurnStopTeardown, want: false},
		{name: "the failsafe's stop", cause: TurnStopCompaction, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cause.KASRewakes(); got != tc.want {
				t.Errorf("KASRewakes = %v, want %v", got, tc.want)
			}
		})
	}
}

// The resume is marotte's prompt, never a message of the reader's: the queue's caps and drafts never count it.
func TestRunRows_LeavesTheResumeOut(t *testing.T) {
	entries := []RunEntry{failsafeResume(), row("m-q1", RunPaused)}
	rows := RunRows(entries)
	if len(rows) != 1 || rows[0].ID != "m-q1" {
		t.Errorf("rows = %+v, want only m-q1", rows)
	}
}

func failsafeResume() RunEntry {
	return RunEntry{
		ID: "m-resume", Kind: RunKindResume, State: RunPending, Origin: RunFromFailsafe,
		Row: &QueuedPrompt{ID: "m-resume", Text: "continue"},
	}
}
