package marotte

import (
	"slices"
	"testing"
)

// u is a user entry: a row with no caller.
func u(id string, s RunEntryState, w RunWant) RunEntry {
	return RunEntry{
		ID: id, Kind: RunKindRow, State: s, Origin: RunFromPrompt, Want: w,
		Row: &QueuedPrompt{ID: id, Text: id},
	}
}

// d is a door entry from grelot, which is always placed Want either.
func d(id string, s RunEntryState) RunEntry {
	return RunEntry{
		ID: id, Kind: RunKindRow, State: s, Origin: RunFromNotify, Want: RunWantEither,
		Row: &QueuedPrompt{ID: id, Text: "1 new; run grelot inbox", Caller: &Caller{Label: "grelot"}},
	}
}

func unread(e RunEntry) RunEntry { e.Unread = true; return e }
func next(e RunEntry) RunEntry   { e.Next = true; return e }

func notSteerable(e RunEntry) RunEntry { e.Steerable = false; return e }

func list(es ...RunEntry) []RunEntry { return es }

var deliverKindName = map[DeliverKind]string{
	DeliverNothing: "nothing", DeliverSteer: "steer", DeliverPrompt: "prompt",
}

func nothing() Delivery               { return Delivery{} }
func steer(id string) Delivery        { return Delivery{Kind: DeliverSteer, IDs: []string{id}} }
func prompt(ids ...string) Delivery   { return Delivery{Kind: DeliverPrompt, IDs: ids} }
func userNext(dl Delivery) Delivery   { dl.UserNext = true; return dl }
func prompted() HeadFacts             { return HeadFacts{Prompted: true} }
func stopped() HeadFacts              { return HeadFacts{Stopped: true} }
func promptedUserNext() HeadFacts     { return HeadFacts{Prompted: true, UserNext: true} }
func refused(ids ...string) HeadFacts { return HeadFacts{Prompted: true, SteerRefused: ids} }

type deliverRow struct {
	name    string
	entries []RunEntry
	facts   HeadFacts
	want    Delivery
}

// deliverRows are the rows of the run-order delivery table: each list is the shape after the row's events, and the
// expected delivery comes from PROPOSAL rules 1 to 4, the grelot sign-off and the user's report.
func deliverRows() []deliverRow {
	return []deliverRow{
		{name: "R1 an idle chat prompts a wake", entries: list(d("d", RunPending)), want: prompt("d")},
		{
			name:    "R2 a busy chat steers a wake with nothing of the user's waiting",
			entries: list(turn("A", RunRunning), d("d", RunPending)), facts: prompted(), want: steer("d"),
		},
		{
			name:    "R3 a wake waits behind the user's queued row",
			entries: list(turn("A", RunRunning), u("u", RunPending, RunWantPrompt), d("d", RunPending)),
			facts:   prompted(), want: nothing(),
		},
		{
			name:    "R4 a wake waits behind the user's steer in kiro-cli",
			entries: list(turn("A", RunRunning), u("u", RunInKAS, RunWantEither), d("d", RunPending)),
			facts:   prompted(), want: nothing(),
		},
		{
			name:    "R5 the wake is steered once kiro-cli read the user's steer",
			entries: list(turn("A", RunRunning), d("d", RunPending)), facts: prompted(), want: steer("d"),
		},
		{
			name: "R6 send-now on the only steer prompts the user's message alone",
			entries: list(
				next(unread(u("u", RunPending, RunWantPrompt))), d("d", RunPending),
			),
			facts: stopped(), want: userNext(prompt("u")),
		},
		{
			name:    "R7 no wake is steered into the turn the user's send-now opened",
			entries: list(turn("U", RunRunning), d("d", RunPending)), facts: promptedUserNext(), want: nothing(),
		},
		{
			name: "R8 the wake goes once the send-now turn closed", entries: list(d("d", RunPending)),
			want: prompt("d"),
		},
		{
			name: "R9 a stopping head delivers nothing", entries: list(turn("A", RunStopping), d("d", RunPending)),
			facts: prompted(), want: nothing(),
		},
		{
			name: "R9 the wake goes after the stopped turn closed", entries: list(d("d", RunPending)),
			want: prompt("d"),
		},
		{
			name: "R10 a wake back unread goes behind the user's queued row",
			entries: list(
				u("u", RunPending, RunWantPrompt), unread(d("d", RunPending)),
			),
			want: prompt("u"),
		},
		{
			name: "R11 the user's steer back from a stop opens the user's turn",
			entries: list(
				unread(u("u", RunPending, RunWantPrompt)), d("d", RunPending),
			),
			facts: stopped(), want: userNext(prompt("u")),
		},
		{
			name:    "R12 a wake behind the user's paused row waits on an idle chat",
			entries: list(u("u1", RunPaused, RunWantPrompt), d("d", RunPending)), want: nothing(),
		},
		{
			name:    "R12 the user's row goes first after the next clean close",
			entries: list(u("u1", RunPending, RunWantPrompt), d("d", RunPending)), want: prompt("u1"),
		},
		{
			name:    "R12 the user's send-now on the paused row goes first",
			entries: list(next(u("u1", RunPending, RunWantPrompt)), d("d", RunPending)),
			want:    userNext(prompt("u1")),
		},
		{
			name: "a send-now row behind a pending compaction leaves the row ahead of the compaction first",
			entries: list(
				u("u1", RunPending, RunWantPrompt), compaction(RunPending), next(u("u2", RunPending, RunWantPrompt)),
			),
			want: prompt("u1"),
		},
		{
			name: "R13 discarding the user's paused row frees the wake", entries: list(d("d", RunPending)),
			want: prompt("d"),
		},
		{
			name:    "R14 a wake is not paused by a stop",
			entries: list(turn("A", RunStopping), d("d", RunPending)), want: nothing(),
		},
		{
			name: "R14 the wake goes once the stopped turn closed", entries: list(d("d", RunPending)),
			want: prompt("d"),
		},
		{
			name: "R15 a wake back unread waits behind the user's paused row",
			entries: list(
				u("u", RunPaused, RunWantPrompt), unread(d("d", RunPending)),
			),
			want: nothing(),
		},
		{
			name:    "R16 the user's refused steer keeps its place ahead of a wake",
			entries: list(u("u", RunPending, RunWantPrompt), d("d", RunPending)), want: prompt("u"),
		},
		{
			name: "R18 the collapsed wake keeps its place ahead of a later user row",
			entries: list(
				notSteerable(turn("A", RunRunning)), d("d", RunPending), u("u", RunPending, RunWantPrompt),
			),
			facts: prompted(), want: nothing(),
		},
		{
			name:    "R19 a wake already in kiro-cli is not delivered again",
			entries: list(turn("A", RunRunning), d("d", RunInKAS)), facts: prompted(), want: nothing(),
		},
		{
			name:    "R20 R30 an offered wake is not taken while its steer answer is out",
			entries: list(d("d", RunOffered)), want: nothing(),
		},
		{
			name:    "R22 a user steer waits for the wake's turn to be prompted",
			entries: list(dTurn("D", RunRunning), u("u", RunPending, RunWantEither)), want: nothing(),
		},
		{
			name:    "R22 the user's steer goes into the wake's turn once prompted, under the user's name",
			entries: list(dTurn("D", RunRunning), u("u", RunPending, RunWantEither)), facts: prompted(),
			want: steer("u"),
		},
		{
			name:    "R23 nothing is steered into an unprompted turn",
			entries: list(turn("A", RunRunning), d("d", RunPending)), want: nothing(),
		},
		{
			name:    "R24 a wire head counts as prompted",
			entries: list(wire("W", RunRunning), d("d", RunPending)), want: steer("d"),
		},
		{
			name: "R25 the user's unread steers join and the wake is its own later turn",
			entries: list(
				unread(u("u1", RunPending, RunWantPrompt)), unread(u("u2", RunPending, RunWantPrompt)),
				unread(d("d", RunPending)),
			),
			want: prompt("u1", "u2"),
		},
		{
			name:    "R25 the wake's turn follows",
			entries: list(unread(d("d", RunPending))), want: prompt("d"),
		},
		{
			name:    "R26 nothing is delivered behind a running compaction",
			entries: list(compaction(RunRunning), u("u", RunPending, RunWantEither)), want: nothing(),
		},
		{
			name:    "R26 the user's row goes after the compaction",
			entries: list(u("u", RunPending, RunWantEither)), want: prompt("u"),
		},
		{
			name:    "R27 a wake is not steered into a non-steerable turn",
			entries: list(notSteerable(turn("A", RunRunning)), d("d", RunPending)), facts: prompted(),
			want: nothing(),
		},
		{
			name:    "R27 a wake is not steered in queue mode",
			entries: list(turn("A", RunRunning), d("d", RunPending)),
			facts:   HeadFacts{Prompted: true, Mode: InterruptQueue}, want: nothing(),
		},
		{
			name:    "R28 a wake that arrived first is prompted first",
			entries: list(d("d", RunPending), u("u", RunPending, RunWantPrompt)), want: prompt("d"),
		},
		{
			name:    "R29 a wake kiro-cli refused is not offered again in the same turn",
			entries: list(turn("A", RunRunning), d("d", RunPending)), facts: refused("d"), want: nothing(),
		},
		{
			name:    "R29 the refused wake is offered after the next turn opened",
			entries: list(turn("B", RunRunning), d("d", RunPending)), facts: prompted(), want: steer("d"),
		},
		{
			name:    "R30 a wake whose steer came back at a turn end is prompted once",
			entries: list(unread(d("d", RunPending))), want: prompt("d"),
		},
		{
			name:    "R31 the user's queued row is not steered into the wake's turn",
			entries: list(dTurn("D", RunRunning), u("u", RunPending, RunWantPrompt)), facts: prompted(),
			want: nothing(),
		},
		{
			name:    "R31 the user's row goes alone after the wake's turn",
			entries: list(u("u", RunPending, RunWantPrompt)), want: prompt("u"),
		},
		{
			name: "R32 two sends behind a compaction are two prompts in order",
			entries: list(
				u("u1", RunPending, RunWantPrompt), u("u2", RunPending, RunWantPrompt),
			),
			want: prompt("u1"),
		},
		{
			name:    "R32 the second send is not steered into the first one's turn",
			entries: list(turn("u1", RunRunning), u("u2", RunPending, RunWantPrompt)), facts: prompted(),
			want: nothing(),
		},
		{
			name: "R33 paused sends are two prompts in order",
			entries: list(
				u("u1", RunPending, RunWantPrompt), u("u2", RunPending, RunWantPrompt),
			),
			want: prompt("u1"),
		},
		{
			name:    "R34 send-now on the user's row outranks a wake ahead of it",
			entries: list(d("d", RunPending), next(u("u", RunPending, RunWantPrompt))),
			want:    userNext(prompt("u")),
		},
		{
			name: "R35 send-now on the user's steer outranks a wake ahead of it",
			entries: list(
				d("d", RunPending), next(unread(u("u", RunPending, RunWantPrompt))),
			),
			facts: stopped(), want: userNext(prompt("u")),
		},
		{
			name: "R36 a wake ahead of the user's later steer waits on an unprompted head",
			entries: list(
				turn("A", RunRunning), d("d", RunPending), u("u", RunPending, RunWantEither),
			),
			want: nothing(),
		},
		{
			name: "R36 the user's steer outranks the earlier wake for the running turn",
			entries: list(
				turn("A", RunRunning), d("d", RunPending), u("u", RunPending, RunWantEither),
			),
			facts: prompted(), want: steer("u"),
		},
		{
			name: "R36 the user's steer back from a stop opens the next turn ahead of the wake",
			entries: list(
				unread(u("u", RunPending, RunWantPrompt)), d("d", RunPending),
			),
			facts: stopped(), want: userNext(prompt("u")),
		},
		{
			name: "R37 the send-now path opens the same turn",
			entries: list(
				next(unread(u("u", RunPending, RunWantPrompt))), d("d", RunPending),
			),
			facts: stopped(), want: userNext(prompt("u")),
		},
		{
			name:    "R38 kiro-cli's continuation of the send-now prompt takes no wake",
			entries: list(wire("W", RunRunning), d("d", RunPending)), facts: HeadFacts{UserNext: true},
			want: nothing(),
		},
		{
			name: "R39 a wake cleared beside the user's kept steer waits behind it",
			entries: list(
				turn("A", RunRunning), u("u2", RunInKAS, RunWantEither), d("d", RunPending),
			),
			facts: prompted(), want: nothing(),
		},
		{
			name:    "R40 the user's kept steer stays in kiro-cli alone",
			entries: list(turn("A", RunRunning), u("u1", RunInKAS, RunWantEither)), facts: prompted(),
			want: nothing(),
		},
		{
			name: "R41 queue-instead keeps the user's row ahead of the cleared wake",
			entries: list(
				turn("A", RunRunning), u("u1", RunPending, RunWantPrompt), d("d", RunPending),
			),
			facts: prompted(), want: nothing(),
		},
		{
			name:    "R41 the user's row is prompted first after the turn",
			entries: list(u("u1", RunPending, RunWantPrompt), d("d", RunPending)), want: prompt("u1"),
		},
		{
			name: "R42 one steer is in flight at a time",
			entries: list(
				turn("A", RunRunning), u("u1", RunOffered, RunWantEither), u("u2", RunPending, RunWantEither),
			),
			facts: prompted(), want: nothing(),
		},
		{
			name: "the user's steer still goes into the running turn ahead of a pending compaction",
			entries: list(
				turn("A", RunRunning), compaction(RunPending), u("u", RunPending, RunWantEither),
			),
			facts: prompted(), want: steer("u"),
		},
		{
			name:    "a held close delivers nothing",
			entries: list(d("d", RunPending)), facts: HeadFacts{EndsOwed: true}, want: nothing(),
		},
		{
			name:    "a pending compaction ahead is taken by its own rule first",
			entries: list(compaction(RunPending), d("d", RunPending)), want: nothing(),
		},
		{
			name:    "a wake goes beside a fold turn kiro-cli admits a take beside",
			entries: list(fold("F", true), d("d", RunPending)), want: prompt("d"),
		},
		{
			name:    "nothing goes beside a fold turn kiro-cli does not admit a take beside",
			entries: list(fold("F", false), d("d", RunPending)), want: nothing(),
		},
		{
			name:    "nothing is steered into kiro-cli's own run",
			entries: list(runEntry(), d("d", RunPending)), facts: prompted(), want: nothing(),
		},
		{
			name:    "a held user row does not hold a wake",
			entries: list(u("u", RunHeld, RunWantPrompt), d("d", RunPending)), want: prompt("d"),
		},
		{
			name: "two callers' unread entries never join",
			entries: list(
				unread(d("d", RunPending)), unread(caller(d("e", RunPending), "ci")),
			),
			want: prompt("d"),
		},
		{
			name: "the user's unread steers after a clean close open no send-now turn",
			entries: list(
				unread(u("u1", RunPending, RunWantPrompt)), unread(u("u2", RunPending, RunWantPrompt)),
			),
			want: prompt("u1", "u2"),
		},
		{
			name: "the send-now lead goes first among the user's unread steers",
			entries: list(
				unread(u("u1", RunPending, RunWantPrompt)), next(unread(u("u2", RunPending, RunWantPrompt))),
				unread(u("u3", RunPending, RunWantPrompt)),
			),
			facts: stopped(), want: userNext(prompt("u2", "u1", "u3")),
		},
	}
}

func TestDeliver(t *testing.T) {
	for _, tc := range deliverRows() {
		t.Run(tc.name, func(t *testing.T) {
			got := Deliver(tc.entries, tc.facts)
			if got.Kind != tc.want.Kind || !slices.Equal(got.IDs, tc.want.IDs) || got.UserNext != tc.want.UserNext {
				t.Errorf("Deliver = %s %v userNext=%v, want %s %v userNext=%v",
					deliverKindName[got.Kind], got.IDs, got.UserNext,
					deliverKindName[tc.want.Kind], tc.want.IDs, tc.want.UserNext)
			}
			if err := deliveryInvariant(tc.entries, got); err != "" {
				t.Error(err)
			}
		})
	}
}

// dTurn is a running turn a door entry opened.
func dTurn(id string, s RunEntryState) RunEntry {
	e := d(id, s)
	e.Turn, e.Steerable = id, true
	return e
}

func caller(e RunEntry, label string) RunEntry {
	e.Row.Caller = &Caller{Label: label}
	return e
}

func senderOf(e *RunEntry) string {
	if e.Row == nil || e.Row.Caller == nil {
		return "the user"
	}
	return "caller " + e.Row.Caller.Label
}

func waitingState(s RunEntryState) bool {
	return s == RunPending || s == RunPaused || s == RunOffered || s == RunInKAS
}

// deliveryInvariant is the check every row makes besides its own cells: no delivery carries a door entry while a user
// entry waits ahead of it, a steer carries one entry, and no prompt joins two senders.
func deliveryInvariant(entries []RunEntry, dl Delivery) string {
	if dl.Kind == DeliverNothing {
		if len(dl.IDs) != 0 {
			return "nothing delivered names entries"
		}
		return ""
	}
	if dl.Kind == DeliverSteer && len(dl.IDs) != 1 {
		return "a steer carries more than one entry"
	}
	var first *RunEntry
	for _, id := range dl.IDs {
		i := slices.IndexFunc(entries, func(e RunEntry) bool { return e.ID == id })
		if i < 0 {
			return "delivered " + id + ", which is not on the list"
		}
		e := &entries[i]
		if first == nil {
			first = e
		} else if senderOf(first) != senderOf(e) {
			return "one prompt joins two senders"
		}
		if e.ByUser() {
			continue
		}
		for j := range i {
			a := &entries[j]
			if a.Kind == RunKindRow && a.ByUser() && waitingState(a.State) && !slices.Contains(dl.IDs, a.ID) {
				return "door entry " + id + " delivered while the user's " + a.ID + " waits ahead of it"
			}
		}
	}
	return ""
}
