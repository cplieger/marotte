package settings

import (
	"errors"
	"testing"
)

// TestValidAgentIgnoreEntry_WhitespaceMirrorsJavaScript pins the whitespace arm against
// ECMAScript's trim set, with one control from each agreeing class.
func TestValidAgentIgnoreEntry_WhitespaceMirrorsJavaScript(t *testing.T) {
	tests := []struct {
		name        string
		entry       string
		wantRefusal bool
	}{
		{
			// JavaScript trims U+FEFF and Go does not, so TrimSpace would accept it and KAS drop it.
			name:        "U+FEFF is refused, because KAS trims it and would skip the entry",
			entry:       "\uFEFFgitignore",
			wantRefusal: true,
		},
		{
			// Go trims U+0085 and JavaScript does not: marotte must not be stricter than KAS.
			name:        "U+0085 is accepted, because KAS does not trim it",
			entry:       "\u0085gitignore",
			wantRefusal: false,
		},
		{
			// Control from the class both trim.
			name:        "a leading space is refused, because both sides trim it",
			entry:       " .gitignore",
			wantRefusal: true,
		},
		{
			// Neither side trims U+200B.
			name:        "U+200B is accepted, because neither side trims it",
			entry:       "\u200Bgitignore",
			wantRefusal: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidAgentIgnoreEntry(tt.entry)
			switch {
			case tt.wantRefusal && err == nil:
				t.Errorf("ValidAgentIgnoreEntry(%q) = nil, want a refusal", tt.entry)
			case !tt.wantRefusal && err != nil:
				t.Errorf("ValidAgentIgnoreEntry(%q) = %v, want nil", tt.entry, err)
			case tt.wantRefusal && !errors.Is(err, ErrAgentIgnoreEntry):
				t.Errorf("ValidAgentIgnoreEntry(%q) = %v, want it to wrap ErrAgentIgnoreEntry", tt.entry, err)
			}
		})
	}
}

// TestValidAgentIgnoreEntry_CoversKASsWholeRefusalSet walks every arm of KAS's `Bvt`; each
// accepted row sits next to a refused one, so a widened rule fails too.
func TestValidAgentIgnoreEntry_CoversKASsWholeRefusalSet(t *testing.T) {
	tests := []struct {
		name        string
		entry       string
		wantRefusal bool
	}{
		{"empty", "", true},
		{"a lone dot is not a filename", ".", true},
		{"trailing whitespace", ".gitignore ", true},
		{"a forward slash makes it a path", "sub/.gitignore", true},
		{"a backslash makes it a path", `sub\.gitignore`, true},
		{"a leading parent reference", "..gitignore", true},
		{"a bare parent reference", "..", true},
		// A substring test, not a prefix test: this row tells the two apart.
		{"a parent reference anywhere in the name", "ignore..list", true},
		{"a star is a pattern, not a filename", "*.ignore", true},
		{"a question mark is a pattern", "?ignore", true},
		{"a bracket is a pattern", "[a].ignore", true},
		{"a closing bracket is a pattern", "a].ignore", true},
		{"a brace is a pattern", "{a}.ignore", true},
		{"a closing brace is a pattern", "a}.ignore", true},

		{"a dotfile at the workspace root", ".gitignore", false},
		{"the floor itself is a valid entry", AgentIgnoreFloor, false},
		{"a dot INSIDE the name is not a parent reference", ".env.ignore", false},
		{"a hyphen and an underscore are ordinary characters", "my_ignore-list", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidAgentIgnoreEntry(tt.entry)
			switch {
			case tt.wantRefusal && err == nil:
				t.Errorf("ValidAgentIgnoreEntry(%q) = nil, want a refusal", tt.entry)
			case !tt.wantRefusal && err != nil:
				t.Errorf("ValidAgentIgnoreEntry(%q) = %v, want nil", tt.entry, err)
			case tt.wantRefusal && !errors.Is(err, ErrAgentIgnoreEntry):
				t.Errorf("ValidAgentIgnoreEntry(%q) = %v, want it to wrap ErrAgentIgnoreEntry", tt.entry, err)
			}
		})
	}
}

// TestAgentIgnoreList_DropsAnEntryKASWouldSkip pins that what marotte sends equals what KAS
// enforces.
func TestAgentIgnoreList_DropsAnEntryKASWouldSkip(t *testing.T) {
	got := AgentIgnoreList([]string{"\uFEFFgitignore", ".gitignore"})
	want := []string{AgentIgnoreFloor, ".gitignore"}
	if len(got) != len(want) {
		t.Fatalf("AgentIgnoreList = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AgentIgnoreList = %v, want %v", got, want)
		}
	}
}
