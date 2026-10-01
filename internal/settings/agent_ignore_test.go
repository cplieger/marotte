package settings

import (
	"errors"
	"testing"
)

// TestValidAgentIgnoreEntry_WhitespaceMirrorsJavaScript pins the whitespace arm
// against ECMAScript's trim set rather than Go's, because that is the set KAS
// compares against. The two divergent runes are the reason the arm cannot use
// strings.TrimSpace: U+FEFF diverges in the ACCEPTING direction, so TrimSpace
// persists an entry KAS silently skips, which is precisely what this validator
// exists to prevent. The two controls are one rune from each agreeing class, so
// a fix that merely inverted the divergence would still fail here.
func TestValidAgentIgnoreEntry_WhitespaceMirrorsJavaScript(t *testing.T) {
	tests := []struct {
		name        string
		entry       string
		wantRefusal bool
	}{
		{
			// Divergent, and it runs the UNSAFE way: JavaScript trims U+FEFF and
			// Go does not, so strings.TrimSpace accepts this and KAS then drops it.
			name:        "U+FEFF is refused, because KAS trims it and would skip the entry",
			entry:       "\uFEFFgitignore",
			wantRefusal: true,
		},
		{
			// Divergent the other way: Go trims U+0085 and JavaScript does not, so
			// KAS accepts this entry and marotte must not be stricter than KAS.
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
			// Control from the class neither trims: U+200B left Unicode's
			// White_Space property in 4.0.1 and is not in ECMAScript's set either.
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

// TestValidAgentIgnoreEntry_CoversKASsWholeRefusalSet walks every arm of KAS's
// own `Bvt`, because a validator that is narrower than KAS accepts a name KAS
// SKIPS — which persists an entry the panel then claims is enforced — and one that
// is wider refuses a name KAS would have honoured.
//
// The accepted rows are half the point: each is a character adjacent to a refused
// one, so a rule widened by one class fails here rather than only in the direction
// the refusals cover.
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
		// KAS's rule is a substring test, not a prefix test, so a dot pair in the
		// MIDDLE is refused too — the row that tells the two rules apart.
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

// TestAgentIgnoreList_DropsAnEntryKASWouldSkip is the other half of the same
// property, at the surface that puts the list on the wire: what marotte sends
// must equal what KAS enforces, and config.json is hand-editable, so a name that
// never reached the PATCH validator can still be in the document.
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
