package command

import "testing"

// The cases are the boundary of KAS's own parseGoalCommand, transcribed in
// static-src/chat-options.test.ts. Every `want: false` row is text KAS hands to
// the MODEL, so a turn from it carries content and empty-turn recovery is the
// right behaviour; every `want: true` row is answered by KAS with end_turn and
// no content, which recovery must not touch.
func TestKASClaimsPromptText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"plain prose", "make the tests pass", false},
		{"another command", "/compact", false},
		{"bare verb", "/goal", false},
		{"bare verb with trailing space", "/goal   ", false},
		{"objective", "/goal make the test suite pass", true},
		{"leading whitespace", "   /goal make the test suite pass", true},
		{"objective with bound", "/goal make the test suite pass --max 12", true},
		// KAS's `--max` regexp requires leading whitespace on the trimmed body: `/goal --max 5`
		// names a goal "--max 5".
		{"bound with no objective", "/goal --max 5", true},
		{"bound mid-text", "/goal raise --max 5 in the docs", true},
		{"non-numeric bound", "/goal tidy up --max soon", true},
		{"prefix is a longer word", "/goalpost check", false},
		{"substring, not a prefix", "please /goal something", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := kasClaimsPromptText(tc.text); got != tc.want {
				t.Errorf("kasClaimsPromptText(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}
