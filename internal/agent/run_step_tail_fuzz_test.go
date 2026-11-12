package agent

import "testing"

// A read carries a streaming entry's dockText in place of its text, and the client appends the entry's later deltas
// to it, so the line read off it with any text appended is the line the whole text gives.
func FuzzDockText_readsAsTheWholeTextWhateverFollows(f *testing.F) {
	for _, seed := range [][2]string{
		{"## Plan\n- first point\nsec", "ond"},
		{"intro\n``", "`go"},
		{"intro\n-", "--\n"},
		{"intro\n*", "**"},
		{"one\n\n\n", ""},
		{"no newline", " yet"},
		{"\n", "x"},
		{"", ""},
		{"```\ncode line\npart", "ial\n```"},
		{"a\n|---|\n", "| b |"},
		{"prose\n> \n   \n", "#"},
	} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, text, more string) {
		kept := dockText(text)
		if len(kept) > len(text) || text[len(text)-len(kept):] != kept {
			t.Fatalf("dockText(%q) = %q, not an end of the text", text, kept)
		}
		if got, want := lastProseLine(kept+more), lastProseLine(text+more); got != want {
			t.Errorf("dockText(%q) = %q reads %q with %q appended, want %q", text, kept, got, more, want)
		}
	})
}

// A long streamed reply is carried from its newest finished line that prints, not whole.
func TestDockText_KeepsTheEndALineReads(t *testing.T) {
	long := ""
	for range 1000 {
		long += "a line of the reply\n"
	}
	if got := dockText(long + "```\n\npart"); got != "a line of the reply\n```\n\npart" {
		t.Errorf("dockText = %q, want the last prose line onwards", got)
	}
}
