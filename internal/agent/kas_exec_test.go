package agent

import (
	"fmt"
	"testing"
)

// Every cell of the KAS execution state machine, stated independently of kasTransitions: the hold belongs to turn 7,
// another closer is turn 9, and a new grace claim is turn 11. A row names the state before, the input and the seq it
// carries, and what the step must leave.
func TestKASExec_EveryTransition(t *testing.T) {
	const own, other, claim = 7, 9, 11
	held := func(s kasState) kasExec { return kasExec{state: s, holdSeq: own} }
	cases := []struct {
		name      string
		before    kasExec
		in        kasInput
		seq       uint64
		wantSeq   uint64
		want      kasState
		wantDrain bool
	}{
		{name: "idle report active", before: kasExec{}, in: kasReportActive, want: kasRunning},
		{name: "idle report idle", before: kasExec{}, in: kasReportIdle, want: kasIdle},
		{name: "idle forward reset", before: kasExec{}, in: kasForwardReset, want: kasIdle},
		{name: "idle grace claim", before: kasExec{}, in: kasGraceClaim, seq: claim, want: kasHeldIdle, wantSeq: claim},
		{name: "idle release own", before: kasExec{}, in: kasReleaseOwn, want: kasIdle, wantDrain: true},
		{name: "idle release other", before: kasExec{}, in: kasReleaseOwn, seq: other, want: kasIdle, wantDrain: true},
		{name: "idle stand down own", before: kasExec{}, in: kasStandDownOwn, want: kasIdle},
		{name: "idle stand down other", before: kasExec{}, in: kasStandDownOwn, seq: other, want: kasIdle},

		{name: "running report active", before: kasExec{state: kasRunning}, in: kasReportActive, want: kasRunning},
		{name: "running report idle", before: kasExec{state: kasRunning}, in: kasReportIdle, want: kasIdle},
		{name: "running forward reset", before: kasExec{state: kasRunning}, in: kasForwardReset, want: kasIdle},
		{name: "running grace claim", before: kasExec{state: kasRunning}, in: kasGraceClaim, seq: claim, want: kasHeldRunning, wantSeq: claim},
		{name: "running release own", before: kasExec{state: kasRunning}, in: kasReleaseOwn, want: kasRunning},
		{name: "running release other", before: kasExec{state: kasRunning}, in: kasReleaseOwn, seq: other, want: kasRunning},
		{name: "running stand down own", before: kasExec{state: kasRunning}, in: kasStandDownOwn, want: kasRunning},
		{name: "running stand down other", before: kasExec{state: kasRunning}, in: kasStandDownOwn, seq: other, want: kasRunning},

		{name: "held running report active", before: held(kasHeldRunning), in: kasReportActive, want: kasHeldRunning, wantSeq: own},
		{name: "held running report idle", before: held(kasHeldRunning), in: kasReportIdle, want: kasHeldIdle, wantSeq: own},
		{name: "held running forward reset", before: held(kasHeldRunning), in: kasForwardReset, want: kasHeldIdle, wantSeq: own},
		{name: "held running grace claim", before: held(kasHeldRunning), in: kasGraceClaim, seq: claim, want: kasHeldRunning, wantSeq: claim},
		{name: "held running release own", before: held(kasHeldRunning), in: kasReleaseOwn, seq: own, want: kasHeldRunning, wantSeq: own},
		{name: "held running release other", before: held(kasHeldRunning), in: kasReleaseOwn, seq: other, want: kasHeldRunning, wantSeq: own},
		{name: "held running stand down own", before: held(kasHeldRunning), in: kasStandDownOwn, seq: own, want: kasRunning},
		{name: "held running stand down other", before: held(kasHeldRunning), in: kasStandDownOwn, seq: other, want: kasHeldRunning, wantSeq: own},

		{name: "held idle report active", before: held(kasHeldIdle), in: kasReportActive, want: kasHeldRunning, wantSeq: own},
		{name: "held idle report idle", before: held(kasHeldIdle), in: kasReportIdle, want: kasHeldIdle, wantSeq: own},
		{name: "held idle forward reset", before: held(kasHeldIdle), in: kasForwardReset, want: kasHeldIdle, wantSeq: own},
		{name: "held idle grace claim", before: held(kasHeldIdle), in: kasGraceClaim, seq: claim, want: kasHeldIdle, wantSeq: claim},
		{
			name: "held idle release own", before: held(kasHeldIdle), in: kasReleaseOwn, seq: own,
			want: kasIdle, wantDrain: true,
		},
		{name: "held idle release other", before: held(kasHeldIdle), in: kasReleaseOwn, seq: other, want: kasHeldIdle, wantSeq: own, wantDrain: true},
		{name: "held idle stand down own", before: held(kasHeldIdle), in: kasStandDownOwn, seq: own, want: kasIdle},
		// No hold of its own: a closer whose hold stood down, as after KAS opened a turn of its own.
		{
			name: "no hold idle release other", before: kasExec{}, in: kasReleaseOwn, seq: other,
			want: kasIdle, wantDrain: true,
		},
		{
			name: "no hold running release other", before: kasExec{state: kasRunning},
			in: kasReleaseOwn, seq: other, want: kasRunning,
		},
		{
			name: "no hold running grace claim", before: kasExec{state: kasRunning},
			in: kasGraceClaim, seq: claim, want: kasHeldRunning, wantSeq: claim,
		},
		{name: "held idle stand down other", before: held(kasHeldIdle), in: kasStandDownOwn, seq: other, want: kasHeldIdle, wantSeq: own},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k := tc.before
			_, drain := k.step(tc.in, tc.seq)
			if k.state != tc.want || k.holdSeq != tc.wantSeq {
				t.Errorf("state = %v hold %d, want %v hold %d", k.state, k.holdSeq, tc.want, tc.wantSeq)
			}
			if drain != tc.wantDrain {
				t.Errorf("released = %v, want %v", drain, tc.wantDrain)
			}
		})
	}
	t.Run("every cell is a case", func(t *testing.T) {
		seen := map[string]bool{}
		for _, tc := range cases {
			k := tc.before
			in := tc.in
			if tc.seq != k.holdSeq {
				in = otherHold(in)
			}
			seen[fmt.Sprint(k.state, in)] = true
		}
		for s := range nKASStates {
			for in := range nKASInputs {
				if !seen[fmt.Sprint(s, in)] {
					t.Errorf("no case for state %v input %v", s, in)
				}
			}
		}
	})
}

// A claim during a hold reports a change, so the older closer's waiters wake to find the hold no longer theirs; a step
// that leaves the state as it was wakes no one.
func TestKASExec_TheChangeReportWakesOnlyOnAChange(t *testing.T) {
	k := kasExec{state: kasHeldRunning, holdSeq: 7}
	if changed, _ := k.step(kasGraceClaim, 11); !changed {
		t.Error("claim over a hold reported no change, want one")
	}
	if changed, _ := k.step(kasReportActive, 0); changed {
		t.Error("a repeated active report reported a change, want none")
	}
	if changed, _ := k.step(kasReportIdle, 0); !changed {
		t.Error("an idle report while held running reported no change, want one")
	}
}

// What marotte may do in each state: admit a prompt only with KAS idle and no hold, run session calls whenever KAS
// reports no run, and take a prompt as a steer only outside a hold.
func TestKASExec_EachStatesPermits(t *testing.T) {
	cases := []struct {
		state kasState
		want  kasPermits
	}{
		{state: kasIdle, want: kasPermits{admit: true, sessionCall: true, steerHolder: true}},
		{state: kasRunning, want: kasPermits{steerHolder: true}},
		{state: kasHeldRunning, want: kasPermits{}},
		{state: kasHeldIdle, want: kasPermits{sessionCall: true}},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.state), func(t *testing.T) {
			k := kasExec{state: tc.state, holdSeq: 7}
			if got := k.may(); got != tc.want {
				t.Errorf("may() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
