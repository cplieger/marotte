package translate

import (
	"strings"
	"testing"
)

// liveRefusalTitle is a real refusal adopted as a chat name: 77 characters plus KAS's own
// ellipsis, exactly Ete's 80-rune cap, so it arrives UNDER the rune cap.
const liveRefusalTitle = "I need more context to generate a title. Could you share the user's first mes..."

// The per-case reason stops a later edit collapsing the rules into one string. The live
// agent titles at the bottom guard against over-filtering.
func TestTitleRefusal(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  string
	}{
		{
			// It breaks two rules at once, so the sentence-break rule is checked on the case below.
			name:  "the_live_refusal_is_refused",
			title: liveRefusalTitle,
			want:  refusalTruncated,
		},
		{
			// Under the rune cap; KAS strips only TRAILING punctuation, so the break survives.
			name:  "a_multi_sentence_title_under_the_rune_cap",
			title: "I cannot title this. Please provide the first message",
			want:  refusalMultiSentence,
		},
		{
			// A question mid-string, the other half of the sentence-break rule.
			name:  "an_internal_question_mark_is_a_sentence_break",
			title: "Which file did you mean? I could not tell",
			want:  refusalMultiSentence,
		},
		{
			// Not Latin-only: the model can follow the user's language.
			name:  "an_ideographic_full_stop_is_a_sentence_break",
			title: "。 more context please",
			want:  refusalMultiSentence,
		},
		{
			name:  "a_thirteen_word_title_is_too_long_to_be_a_title",
			title: "Add a retry with exponential backoff to the upload path in the worker",
			want:  refusalTooManyWords,
		},
		{
			// Inclusive at 12, already double upstream's own 3-to-6-word instruction.
			name:  "exactly_twelve_words_is_adopted",
			title: "one two three four five six seven eight nine ten eleven twelve",
			want:  "",
		},
		{
			name:  "a_truncated_title_is_never_a_title",
			title: "Fix the flaky retry test in the scheduler package and also the...",
			want:  refusalTruncated,
		},
		{
			name:  "exactly_at_the_rune_cap_is_adopted",
			title: strings.Repeat("t", maxTitleRunes),
			want:  "",
		},
		{
			name:  "one_rune_past_the_cap_is_refused",
			title: strings.Repeat("t", maxTitleRunes+1),
			want:  refusalTooLong,
		},
		{
			// Well-shaped; its own arm keeps "placeholder" apart from the shape rules.
			name:  "kas_s_placeholder_is_refused_as_a_placeholder",
			title: KASDefaultSessionTitle,
			want:  refusalKASPlaceholder,
		},
		{
			// A trailing period is not a BREAK, and KAS's sanitize strips it anyway.
			name:  "a_trailing_period_alone_is_not_a_break",
			title: "Fix the retry test.",
			want:  "",
		},
		{
			// A version or a package name carries a period with no whitespace after
			// it, so the break rule must not fire on one.
			name:  "a_mid_word_period_is_not_a_break",
			title: "Upgrade Node.js to v22.1 in the builder",
			want:  "",
		},
		{name: "a_live_agent_title_title_case", title: "Fix ResizeObserver Error In Safari", want: ""},
		{name: "a_live_agent_title_sentence_case", title: "Safari ResizeObserver loop in marotte", want: ""},
		{name: "a_live_agent_title_seven_words", title: "Fix Race Condition In Marotte Page Titles", want: ""},
		{
			// The scan needs a three-rune window. Unreachable from the focus door, but both doors
			// call this from another package.
			name:  "a_title_too_short_to_hold_a_sentence_break",
			title: "Go",
			want:  "",
		},
		{name: "the_empty_string_carries_no_break", title: "", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TitleRefusal(tc.title); got != tc.want {
				t.Errorf("TitleRefusal(%q) = %q, want %q", tc.title, got, tc.want)
			}
		})
	}
}

// Both doors run this before any rule, so a title is compared and stored clean. The stored
// rung needs it too: KAS persisted that string unsanitized.
func TestSanitizeTitle(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "plain_text_is_unchanged", raw: "Fix the retry test", want: "Fix the retry test"},
		{name: "ansi_is_stripped", raw: "\x1b[31mRelease\x1b[0m check", want: "Release check"},
		{name: "an_embedded_newline_becomes_a_space", raw: "Release\ncheck", want: "Release check"},
		{name: "surrounding_whitespace_goes", raw: "  Release check  ", want: "Release check"},
		{
			// sanitize.Output DELETES a hidden rune before displayText's replace-with-space, so the
			// reversed text reads as ordinary words with no extra spaces.
			name: "a_bidi_override_is_deleted",
			raw:  "Run \u202Ednuof-eman\u202C now",
			want: "Run dnuof-eman now",
		},
		{
			// Nothing on the wire limits this field.
			name: "an_unbounded_title_is_capped_and_marked",
			raw:  strings.Repeat("x", 700),
			want: strings.Repeat("x", maxDisplayTextBytes) + "...",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeTitle(tc.raw); got != tc.want {
				t.Errorf("SanitizeTitle(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
