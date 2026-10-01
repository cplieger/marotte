package marotte

import (
	"encoding/json"
	"testing"
)

// TestRevertVocabulary pins the two kinds R2 adds and the source name the
// no-survivor carrier opens under. `revert` is a TurnOpenSourceName with no
// TurnOpenSource behind it, like `event`, so Name never answers it.
func TestRevertVocabulary(t *testing.T) {
	if got := EntryKindTurnRevert; got != "turn_revert" {
		t.Errorf("EntryKindTurnRevert = %q, want %q", got, "turn_revert")
	}
	if got := EntryKindReconciled; got != "reconciled" {
		t.Errorf("EntryKindReconciled = %q, want %q", got, "reconciled")
	}
	if got := TurnOpenNameRevert; got != "revert" {
		t.Errorf("TurnOpenNameRevert = %q, want %q", got, "revert")
	}
	for s := range TurnOpenSource(32) {
		if got := s.Name(); got == TurnOpenNameRevert {
			t.Errorf("TurnOpenSource(%d).Name() = %q, want no source to answer it", s, got)
		}
	}
	if got := TurnRevertCauseRewind; got != "rewind" {
		t.Errorf("TurnRevertCauseRewind = %q, want %q", got, "rewind")
	}
}

// TestEntryTurnRevertWire pins all five fields as REQUIRED on the wire. Through
// is the one that carries the window's upper bound, so a zero value still has to
// serialise it: a reader bounding the skip rule by the record's own file offset
// is the defect the stated bound exists to make unreachable.
func TestEntryTurnRevertWire(t *testing.T) {
	b, err := json.Marshal(EntryTurnRevert{})
	if err != nil {
		t.Fatalf("Marshal(EntryTurnRevert{}): %v", err)
	}
	const want = `{"from":"","through":"","kas_message_id":"","cause":"","from_n":0}`
	if got := string(b); got != want {
		t.Errorf("Marshal(EntryTurnRevert{}) = %s, want %s", got, want)
	}
	b, err = json.Marshal(EntryTurnRevert{
		From:         "t-5",
		FromN:        5,
		Through:      "t-7",
		KASMessageID: "kas-5",
		Cause:        TurnRevertCauseRewind,
	})
	if err != nil {
		t.Fatalf("Marshal(EntryTurnRevert{…}): %v", err)
	}
	const wantFull = `{"from":"t-5","through":"t-7","kas_message_id":"kas-5","cause":"rewind","from_n":5}`
	if got := string(b); got != wantFull {
		t.Errorf("Marshal(EntryTurnRevert{…}) = %s, want %s", got, wantFull)
	}
}

// TestEntryReconciledWire pins that exactly one field is ever on the wire: the
// turn form clears a per-turn signal and the session form clears condition (i),
// and a record carrying both says neither.
func TestEntryReconciledWire(t *testing.T) {
	cases := []struct {
		name string
		rec  EntryReconciled
		want string
	}{
		{"turn", EntryReconciled{Turn: "t-3"}, `{"turn":"t-3"}`},
		{"session", EntryReconciled{Session: "sess-1"}, `{"session":"sess-1"}`},
		{"neither", EntryReconciled{}, `{}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := json.Marshal(c.rec)
			if err != nil {
				t.Fatalf("Marshal(%+v): %v", c.rec, err)
			}
			if got := string(b); got != c.want {
				t.Errorf("Marshal(%+v) = %s, want %s", c.rec, got, c.want)
			}
		})
	}
}
