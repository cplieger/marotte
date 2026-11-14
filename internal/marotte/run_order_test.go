package marotte

import (
	"slices"
	"strconv"
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
			name:    "a running steerable turn",
			entries: []RunEntry{turn("A", RunRunning), row("m-q1", RunPending), row("m-q2", RunPending)},
			want: want{
				send: SendSteer, cancel: "A", next: turns(3), unread: thisTurn(),
				waits:   map[string]RunWait{"A": waitOf(waitRunning), "m-q1": thisTurn(), "m-q2": turns(2)},
				arrows:  map[string]SendNowKind{"m-q1": sendNowStop, "m-q2": sendNowStop},
				carries: "m-q1",
			},
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
			v := RunViewOf(tc.entries, 7)
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

// A user's send beside wakes (PROPOSAL rules 1 to 3): it outranks a wake for the running turn, rides the running turn
// once its prompt is written and the steer offered ahead of it is answered, and is taken on an idle chat whose only
// waiting wake is held behind the user's paused row.
func TestVerbFor_AUserSendBesideWakes(t *testing.T) {
	cases := []struct {
		name    string
		entries []RunEntry
		mode    InterruptMode
		kind    SendKind
		want    SendVerb
	}{
		{
			name:    "a user send steers ahead of a waiting wake",
			entries: list(turn("A", RunRunning), d("d", RunPending)), want: SendSteer,
		},
		{
			name:    "a user send steers while the wake's steer is out",
			entries: list(turn("A", RunRunning), d("d", RunOffered)), want: SendSteer,
		},
		{
			name:    "a user send rides behind the user's steer in flight",
			entries: list(turn("A", RunRunning), u("u1", RunOffered, RunWantEither)), want: SendSteer,
		},
		{
			name: "a user send steers into the wake's turn", entries: list(dTurn("D", RunRunning)), want: SendSteer,
		},
		{
			name:    "a labelled send waits for a turn of its own behind the wake's",
			entries: list(dTurn("D", RunRunning)), kind: SendLabelled, want: SendQueue,
		},
		{
			name:    "a user send queues in queue mode beside a waiting wake",
			entries: list(turn("A", RunRunning), d("d", RunPending)), mode: InterruptQueue, want: SendQueue,
		},
		{
			name: "a user send waits behind the wake an idle chat takes first", entries: list(d("d", RunPending)),
			want: SendQueue,
		},
		{
			name:    "a user send is taken past a wake held behind the user's paused row",
			entries: list(u("u1", RunPaused, RunWantPrompt), d("d", RunPending)), want: SendPrompt,
		},
		{
			name:    "a queue_prompt past a held wake has nothing to queue behind",
			entries: list(u("u1", RunPaused, RunWantPrompt), d("d", RunPending)), kind: SendQueuePrompt,
			want: SendPrompt,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := VerbFor(tc.entries, tc.mode, tc.kind); got != tc.want {
				t.Errorf("VerbFor = %q, want %q", got, tc.want)
			}
		})
	}
}

// The header's send verb is Deliver's answer for a new user send on every delivery-table shape: steer when the
// running turn would take the user's steers once its prompt is written and the steer offered is answered, prompt when
// the send itself is taken, else queue.
func TestRunViewOf_SendIsDeliversAnswerForAUserSend(t *testing.T) {
	const probe = "the-user's-new-send"
	for _, tc := range deliverRows() {
		t.Run(tc.name, func(t *testing.T) {
			with := slices.Clone(tc.entries)
			for i := range with {
				if with[i].State == RunOffered {
					with[i].State = RunInKAS
				}
			}
			with = slices.Insert(with, insertAt(with), u(probe, RunPending, RunWantEither))
			dl := Deliver(with, HeadFacts{Mode: tc.facts.Mode, Prompted: true})
			want := SendQueue
			switch {
			case dl.Kind == DeliverSteer && userIDs(with)[dl.IDs[0]]:
				want = SendSteer
			case dl.Kind == DeliverPrompt && dl.IDs[0] == probe:
				want = SendPrompt
			}
			if got := VerbFor(tc.entries, tc.facts.Mode, SendPlain); got != want {
				t.Errorf("send = %q, want %q (Deliver: %s %v)", got, want, deliverKindName[dl.Kind], dl.IDs)
			}
		})
	}
}

func userIDs(entries []RunEntry) map[string]bool {
	out := map[string]bool{}
	for i := range entries {
		if entries[i].ByUser() {
			out[entries[i].ID] = true
		}
	}
	return out
}

// A wake's row can be discarded but never sent now (PROPOSAL rule 4). A steer offered or in kiro-cli, and a user steer
// waiting only for the turn's prompt or the one steer offered, ride the running turn: no queue row, no arrow, and no
// turn counted for what waits behind them. A wake behind the user's paused row waits for it.
func TestRunViewOf_WakesAndSteers(t *testing.T) {
	type want struct {
		waits    map[string]RunWait
		arrows   map[string]SendNowKind
		inFlight []string
		next     RunWait
	}
	cases := []struct {
		name    string
		entries []RunEntry
		want    want
	}{
		{
			name:    "a wake's row has no send-now arrow",
			entries: list(turn("A", RunRunning), u("u", RunPending, RunWantPrompt), d("d", RunPending)),
			want: want{
				waits:  map[string]RunWait{"u": thisTurn(), "d": turns(2)},
				arrows: map[string]SendNowKind{"u": sendNowStop}, next: turns(3),
			},
		},
		{
			name:    "a wake waiting for the running turn has no arrow either",
			entries: list(turn("A", RunRunning), d("d", RunPending)),
			want:    want{waits: map[string]RunWait{"d": thisTurn()}, arrows: map[string]SendNowKind{}, next: turns(2)},
		},
		{
			name: "R42 a user send behind the steer in flight is a sending steer",
			entries: list(
				turn("A", RunRunning), u("u1", RunOffered, RunWantEither), u("u2", RunPending, RunWantEither),
			),
			want: want{
				waits:  map[string]RunWait{"u1": thisTurn(), "u2": thisTurn()},
				arrows: map[string]SendNowKind{}, inFlight: []string{"u2"}, next: thisTurn(),
			},
		},
		{
			name: "R43 a steer in kiro-cli counts no turn for the row behind it",
			entries: list(
				turn("A", RunRunning), u("u1", RunInKAS, RunWantEither), u("u2", RunPending, RunWantPrompt),
			),
			want: want{
				waits:  map[string]RunWait{"u1": thisTurn(), "u2": thisTurn()},
				arrows: map[string]SendNowKind{"u2": sendNowStop}, next: turns(2),
			},
		},
		{
			name:    "R22 a user steer waiting for the wake's turn to be prompted is a sending steer",
			entries: list(dTurn("D", RunRunning), u("u", RunPending, RunWantEither)),
			want: want{
				waits:  map[string]RunWait{"u": thisTurn()},
				arrows: map[string]SendNowKind{}, inFlight: []string{"u"}, next: thisTurn(),
			},
		},
		{
			name: "rows the reader's stop paused wait for the reader, the user's with an arrow",
			entries: list(
				turn("A", RunStopping), stopPaused(u("u1", RunPaused, RunWantPrompt)), stopPaused(d("d", RunPaused)),
			),
			want: want{
				waits:  map[string]RunWait{"u1": waitOf(waitPaused), "d": waitOf(waitPaused)},
				arrows: map[string]SendNowKind{"u1": sendNowNow}, next: waitOf(waitNext),
			},
		},
		{
			name:    "R12 a wake waits for the user's paused row",
			entries: list(turn("A", RunStopping), u("u1", RunPaused, RunWantPrompt), d("d", RunPending)),
			want: want{
				waits:  map[string]RunWait{"u1": turns(1), "d": turns(1)},
				arrows: map[string]SendNowKind{"u1": sendNowNow}, next: waitOf(waitNext),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := RunViewOf(tc.entries, 0)
			var inFlight []string
			for _, e := range v.Entries {
				if w, ok := tc.want.waits[e.ID]; ok && e.Wait != w {
					t.Errorf("%s wait: got %+v, want %+v", e.ID, e.Wait, w)
				}
				if e.SendNow != tc.want.arrows[e.ID] {
					t.Errorf("%s send_now: got %q, want %q", e.ID, e.SendNow, tc.want.arrows[e.ID])
				}
				if e.SteerInFlight {
					inFlight = append(inFlight, e.ID)
				}
			}
			if !slices.Equal(inFlight, tc.want.inFlight) {
				t.Errorf("steers in flight: got %v, want %v", inFlight, tc.want.inFlight)
			}
			if v.NextWait != tc.want.next {
				t.Errorf("next_wait: got %+v, want %+v", v.NextWait, tc.want.next)
			}
		})
	}
}

// A queue row is a row waiting to be sent as its own turn: pending, paused or held, the user's or a wake. A steer
// offered, in kiro-cli or in flight is drawn as a steer, and a live or non-row entry is no queue row.
func TestRunEntryView_WaitingToSend(t *testing.T) {
	inFlight := RunEntryView{RunEntry: u("u", RunPending, RunWantEither), SteerInFlight: true}
	for _, tc := range []struct {
		name string
		view RunEntryView
		want bool
	}{
		{name: "a pending row", view: RunEntryView{RunEntry: u("u", RunPending, RunWantPrompt)}, want: true},
		{name: "a paused row", view: RunEntryView{RunEntry: u("u", RunPaused, RunWantPrompt)}, want: true},
		{name: "a held row", view: RunEntryView{RunEntry: u("u", RunHeld, RunWantPrompt)}, want: true},
		{name: "a pending wake", view: RunEntryView{RunEntry: d("d", RunPending)}, want: true},
		{name: "a steer offered", view: RunEntryView{RunEntry: u("u", RunOffered, RunWantEither)}, want: false},
		{name: "a steer in kiro-cli", view: RunEntryView{RunEntry: d("d", RunInKAS)}, want: false},
		{name: "a steer in flight", view: inFlight, want: false},
		{name: "a starting row", view: RunEntryView{RunEntry: u("u", RunStarting, RunWantPrompt)}, want: false},
		{name: "a pending compaction", view: RunEntryView{RunEntry: compaction(RunPending)}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.view.WaitingToSend(); got != tc.want {
				t.Errorf("WaitingToSend = %v, want %v", got, tc.want)
			}
		})
	}
}

// A row offered to kiro-cli as a steer, or held in its buffer, is a send not yet delivered: it never holds the chat.
func TestLivePrefix_EndsBeforeASteerInFlight(t *testing.T) {
	for _, s := range []RunEntryState{RunOffered, RunInKAS} {
		t.Run(string(s), func(t *testing.T) {
			got := LivePrefix([]RunEntry{turn("A", RunRunning), row("m-s", s)})
			if len(got) != 1 || got[0].ID != "A" {
				t.Errorf("live prefix = %+v, want only the running turn A", got)
			}
		})
	}
}

// A user's send may later be steered only when it is a plain send that meets a running turn taking steers (DESIGN
// section 2 "Want"); every send that would queue behind anything else stays a turn of its own ("two prompts, in
// order"), even once a steerable turn runs.
func TestWantFor(t *testing.T) {
	notSteerable := turn("A", RunRunning)
	notSteerable.Steerable = false
	cases := []struct {
		name    string
		entries []RunEntry
		mode    InterruptMode
		kind    SendKind
		want    RunWant
	}{
		{name: "a plain send against a running steerable turn", entries: list(turn("A", RunRunning)), want: RunWantEither},
		{name: "a plain send against kiro-cli's bracketed turn", entries: list(wire("w", RunRunning)), want: RunWantEither},
		{name: "a plain send on an idle list", want: RunWantPrompt},
		{name: "a labelled send", entries: list(turn("A", RunRunning)), kind: SendLabelled, want: RunWantPrompt},
		{name: "a queue_prompt", entries: list(turn("A", RunRunning)), kind: SendQueuePrompt, want: RunWantPrompt},
		{name: "queue mode", entries: list(turn("A", RunRunning)), mode: InterruptQueue, want: RunWantPrompt},
		{name: "behind a running compaction", entries: list(compaction(RunRunning)), want: RunWantPrompt},
		{name: "against a stopping turn", entries: list(turn("A", RunStopping)), want: RunWantPrompt},
		{name: "against a starting turn", entries: list(turn("A", RunStarting)), want: RunWantPrompt},
		{name: "against a turn that takes no steers", entries: list(notSteerable), want: RunWantPrompt},
		{name: "against kiro-cli's run with no turn here", entries: list(runEntry()), want: RunWantPrompt},
		{name: "beside a yielding fold turn", entries: list(fold("f", true)), want: RunWantPrompt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WantFor(tc.entries, tc.mode, tc.kind); got != tc.want {
				t.Errorf("WantFor = %q, want %q", got, tc.want)
			}
		})
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
		// A kind an earlier build stored and this one no longer writes.
		{ID: "u-1", Kind: "unread", State: RunPending, Row: &QueuedPrompt{ID: "u-1"}},
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

// A wake from before a restart is never sent (grelot sends again while it stays unread); a later life of the chat in
// the same process keeps a waiting wake as it was: pending, or paused by a stop, which a close never undoes. A steer
// kiro-cli holds or was handed is no queue row in any life.
func TestSanitizeRun_DropsNotifyRowsOnAProcessStart(t *testing.T) {
	in := list(
		u("m-u1", RunPending, RunWantPrompt), d("n-1", RunPending), unread(u("m-u2", RunPending, RunWantPrompt)),
		u("m-k", RunInKAS, RunWantEither), u("m-o", RunOffered, RunWantEither), d("n-k", RunInKAS),
		caller(d("n-o", RunOffered), "ci"), caller(d("n-p", RunPaused), "ci"),
	)
	states := func(es []RunEntry) (out []string) {
		for _, e := range es {
			out = append(out, e.ID+":"+string(e.State))
		}
		return out
	}
	t.Run("a process's first life", func(t *testing.T) {
		got := states(SanitizeRun(in, true))
		want := []string{"m-u1:held", "m-u2:held"}
		if !slices.Equal(got, want) {
			t.Errorf("got %v, want %v: only the user's queued words, held", got, want)
		}
	})
	t.Run("a later life", func(t *testing.T) {
		got := states(SanitizeRun(in, false))
		want := []string{"m-u1:paused", "n-1:pending", "m-u2:paused", "n-p:paused"}
		if !slices.Equal(got, want) {
			t.Errorf("got %v, want %v: the user's rows paused, each wake as it waited", got, want)
		}
	})
}

// A row the reader's stop paused keeps the mark into a later life of the chat, so the queue stays paused there; a row
// a process start holds is no longer paused and loses it.
func TestSanitizeRun_KeepsTheStopPauseOnAPausedRow(t *testing.T) {
	in := list(stopPaused(u("m-u", RunPaused, RunWantPrompt)), caller(stopPaused(d("n-1", RunPaused)), "ci"))
	marks := func(es []RunEntry) (out []string) {
		for _, e := range es {
			out = append(out, e.ID+":"+string(e.State)+":"+strconv.FormatBool(e.StopPaused))
		}
		return out
	}
	if got, want := marks(SanitizeRun(in, false)), []string{"m-u:paused:true", "n-1:paused:true"}; !slices.Equal(got, want) {
		t.Errorf("a later life: got %v, want %v", got, want)
	}
	if got, want := marks(SanitizeRun(in, true)), []string{"m-u:held:false"}; !slices.Equal(got, want) {
		t.Errorf("a process's first life: got %v, want %v", got, want)
	}
}

func stopPaused(e RunEntry) RunEntry {
	e.StopPaused = true
	return e
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
