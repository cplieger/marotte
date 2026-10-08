package marotte

import (
	"encoding/json"
	"os"
	"testing"
)

// severityFixture mirrors testdata/turn_severity.json, the table shared with the
// TypeScript side.
type severityFixture struct {
	Cases []struct {
		Outcome  string `json:"outcome"`
		Severity string `json:"severity"`
		Reason   string `json:"default_reason"`
	} `json:"cases"`
}

// everyTurnOutcome is hand-listed because a Go const block is not enumerable at
// runtime; the coverage test below is what keeps it in step with the type.
var everyTurnOutcome = []TurnOutcome{
	TurnOutcomeRunning,
	TurnOutcomeCompleted,
	TurnOutcomeCancelled,
	TurnOutcomeInterrupted,
	TurnOutcomeFailed,
	TurnOutcomeRefused,
	TurnOutcomeUnknown,
	TurnOutcomeEmpty,
}

func loadSeverityFixture(t *testing.T) severityFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/turn_severity.json")
	if err != nil {
		t.Fatalf("read testdata/turn_severity.json: %v", err)
	}
	var fx severityFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("decode testdata/turn_severity.json: %v", err)
	}
	if len(fx.Cases) == 0 {
		t.Fatal("testdata/turn_severity.json has no cases")
	}
	return fx
}

// TestTurnSeverityContract is one half of a cross-language pin: turn-severity.node.test.ts
// runs the same table against the TypeScript implementation, so a rule changed in only one
// language fails in the other.
func TestTurnSeverityContract(t *testing.T) {
	for _, c := range loadSeverityFixture(t).Cases {
		t.Run(c.Outcome, func(t *testing.T) {
			o := TurnOutcome(c.Outcome)
			if got := SeverityOf(o); string(got) != c.Severity {
				t.Errorf("SeverityOf(%q) = %q, want %q", c.Outcome, got, c.Severity)
			}
			if got := DefaultFailureReason(o); got != c.Reason {
				t.Errorf("DefaultFailureReason(%q) = %q, want %q", c.Outcome, got, c.Reason)
			}
		})
	}
}

// TestTurnSeverityContract_CoversEveryOutcome makes the table a contract rather than a
// sample: an outcome added without a fixture row fails here instead of reaching the client
// surfaces with no case for it.
func TestTurnSeverityContract_CoversEveryOutcome(t *testing.T) {
	inFixture := make(map[string]bool)
	for _, c := range loadSeverityFixture(t).Cases {
		if inFixture[c.Outcome] {
			t.Errorf("outcome %q appears twice in the fixture", c.Outcome)
		}
		inFixture[c.Outcome] = true
	}
	for _, o := range everyTurnOutcome {
		if !inFixture[string(o)] {
			t.Errorf("TurnOutcome %q has no row in testdata/turn_severity.json", o)
		}
		delete(inFixture, string(o))
	}
	for extra := range inFixture {
		t.Errorf("fixture row %q names no declared TurnOutcome", extra)
	}
}

// TestSeverityOf_GradesEveryOutcomeAndNeverClean pins two properties the table's rows
// cannot state between them: every outcome grades to one of the four declared severities
// (a missing case arm would answer the zero value, which no surface has a branch for), and
// no outcome but `completed` may grade `clean` — a broken turn must never paint the mark
// that means nothing is wrong.
func TestSeverityOf_GradesEveryOutcomeAndNeverClean(t *testing.T) {
	declared := map[TurnSeverity]bool{
		TurnSeverityRunning: true,
		TurnSeverityClean:   true,
		TurnSeverityStopped: true,
		TurnSeverityBroken:  true,
	}
	for _, o := range everyTurnOutcome {
		sev := SeverityOf(o)
		if !declared[sev] {
			t.Errorf("SeverityOf(%q) = %q, which is not a declared TurnSeverity", o, sev)
		}
		if sev == TurnSeverityClean && o != TurnOutcomeCompleted {
			t.Errorf("SeverityOf(%q) = clean; only `completed` may read as a turn that worked", o)
		}
	}
}

// TestDefaultFailureReason_SpeaksWhereverThereIsSomethingToSay is keyed on the OUTCOME
// rather than the severity, because `SeverityOf` grades `cancelled` and `unknown` alike and
// only one of them still speaks. A `broken` outcome and `unknown` must have an account: the
// footer renders a mark, and a mark with nothing beside it tells the reader nothing. A
// `cancelled` turn is silent because the reader caused the stop and the footer's own outcome
// word already reads "Cancelled" a row away.
func TestDefaultFailureReason_SpeaksWhereverThereIsSomethingToSay(t *testing.T) {
	for _, o := range everyTurnOutcome {
		reason := DefaultFailureReason(o)
		switch o {
		case TurnOutcomeInterrupted, TurnOutcomeFailed, TurnOutcomeRefused, TurnOutcomeUnknown:
			if reason == "" {
				t.Errorf("DefaultFailureReason(%q) is empty; a turn that ended badly must say something", o)
			}
		case TurnOutcomeCancelled, TurnOutcomeCompleted, TurnOutcomeRunning, TurnOutcomeEmpty:
			if reason != "" {
				t.Errorf("DefaultFailureReason(%q) = %q, want empty: there is nothing to add", o, reason)
			}
		}
	}
}

// A tool_use stop is kiro-cli's per-turn model-call limit cutting a turn that asked for more work, so it must read
// broken with its own remedy, never completed or truncated.
func TestConcludeStopReason_AToolUseStopIsTheModelCallLimit(t *testing.T) {
	got := ConcludeStopReason(StopReasonToolUse)
	want := TurnConclusion{
		Outcome:     TurnOutcomeFailed,
		Reason:      ModelCallLimitTurnReason,
		RawStop:     StopReasonToolUse,
		FailureKind: FailureKindModelCallLimit,
		Known:       true,
	}
	if got != want {
		t.Errorf("ConcludeStopReason(%q) = %+v, want %+v", StopReasonToolUse, got, want)
	}
	if sev := SeverityOf(got.Outcome); sev != TurnSeverityBroken {
		t.Errorf("SeverityOf(%q) = %q, want broken: the step's work may be unfinished", got.Outcome, sev)
	}
}
