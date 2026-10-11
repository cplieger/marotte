package marotte

import "testing"

// TestCompactionEntryID pins the derivation both sides of the replay merge mint
// from: a fixed summary gives a fixed id, two summaries differ, and an empty
// summary numbers by its ordinal rather than by its (identical) bytes.
func TestCompactionEntryID(t *testing.T) {
	const want = "compaction-2cf24dba5fb0a30e"
	if got := CompactionEntryID([]byte("hello"), 1); got != want {
		t.Errorf("CompactionEntryID(hello, 1) = %q, want %q", got, want)
	}
	if got := CompactionEntryID([]byte("hello"), 7); got != want {
		t.Errorf("CompactionEntryID(hello, 7) = %q, want %q: the ordinal must not reach a non-empty summary", got, want)
	}
	if a, b := CompactionEntryID([]byte("hello"), 1), CompactionEntryID([]byte("hellp"), 1); a == b {
		t.Errorf("CompactionEntryID gave %q for two different summaries", a)
	}
	if got := CompactionEntryID(nil, 1); got != "compaction-empty-1" {
		t.Errorf("CompactionEntryID(nil, 1) = %q, want %q", got, "compaction-empty-1")
	}
	if got := CompactionEntryID([]byte{}, 2); got != "compaction-empty-2" {
		t.Errorf("CompactionEntryID(empty, 2) = %q, want %q", got, "compaction-empty-2")
	}
}

// TestSayIDOf_InvertsSaySegmentID pins the pair the projection and the live path
// address a split say through, and that a say id carrying its own `#` survives.
func TestSayIDOf_InvertsSaySegmentID(t *testing.T) {
	cases := []struct {
		say string
		k   int
	}{
		{"abc-say", 2},
		{"abc-say", 10},
		{"weird#name-say", 3},
	}
	for _, c := range cases {
		seg := SaySegmentID(c.say, c.k)
		if got := SayIDOf(seg); got != c.say {
			t.Errorf("SayIDOf(SaySegmentID(%q, %d) = %q) = %q, want %q", c.say, c.k, seg, got, c.say)
		}
	}
	for _, id := range []string{"abc-say", "weird#name-say", "abc-say#", "abc-say#2x"} {
		if got := SayIDOf(id); got != id {
			t.Errorf("SayIDOf(%q) = %q, want it unchanged: no `#<digits>` suffix", id, got)
		}
	}
	if got := SaySegmentID("abc-say", 2); got != "abc-say#2" {
		t.Errorf("SaySegmentID(abc-say, 2) = %q, want %q", got, "abc-say#2")
	}
}

// TestEntryIDHelpers pins the two suffix spellings the pairing rules read.
func TestEntryIDHelpers(t *testing.T) {
	if got := ToolResultID("call-1"); got != "call-1:result" {
		t.Errorf("ToolResultID(call-1) = %q, want %q", got, "call-1:result")
	}
	if got := SteerAckID("steer-1"); got != "steer-1:ack" {
		t.Errorf("SteerAckID(steer-1) = %q, want %q", got, "steer-1:ack")
	}
}

// TestTurnOpenSource_NameIsTotal walks every declared member so a source added to
// the int enum without a wire spelling fails here rather than opening a turn whose
// turn_open carries "".
func TestTurnOpenSource_NameIsTotal(t *testing.T) {
	count := turnSourceCount(t)
	seen := make(map[TurnOpenSourceName]TurnOpenSource, int(count))
	for s := range count {
		name := s.Name()
		if name == "" {
			t.Errorf("TurnOpenSource(%d).Name() is empty", s)
			continue
		}
		if prev, dup := seen[name]; dup {
			t.Errorf("TurnOpenSource(%d).Name() = %q, already claimed by TurnOpenSource(%d)", s, name, prev)
		}
		seen[name] = s
	}
	if got := count.Name(); got != "" {
		t.Errorf("TurnOpenSource(%d).Name() = %q, want empty for an out-of-range value", count, got)
	}
}
