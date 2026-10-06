package translate

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// promptStore is an InMemoryChatStore whose PromptTexts answers staged texts, the
// prompt-class user rows an entry log would hold; the records are the store's own.
type promptStore struct {
	*testsupport.InMemoryChatStore
	prompts map[marotte.ChatID][]string
}

func (s *promptStore) PromptTexts(_ context.Context, id marotte.ChatID) ([]string, error) {
	return s.prompts[id], nil
}

// withPrompts hands deps the store wrapped so chatID's prompts read as texts.
func withPrompts(deps *baseDeps, store *testsupport.InMemoryChatStore, chatID marotte.ChatID, texts ...string) {
	deps.store = &promptStore{InMemoryChatStore: store, prompts: map[marotte.ChatID][]string{chatID: texts}}
}

// focusFrame builds a session_info_update raw payload carrying a
// focus_update block, the shape probe-verified on the live 2.12.1 wire.
func focusFrame(t *testing.T, focus map[string]any) []byte {
	t.Helper()
	return mustJSON(t, map[string]any{
		"sessionUpdate": "session_info_update",
		"_meta": map[string]any{
			"kiro": map[string]any{
				"kind":  "focus_update",
				"focus": focus,
			},
		},
	})
}

// chatStatusPayloads filters the captured events down to chat_status
// payloads.
func chatStatusPayloads(t *testing.T, events *[]marotte.ServerEvent) []marotte.ChatStatusPayload {
	t.Helper()
	var out []marotte.ChatStatusPayload
	for _, e := range *events {
		if e.Type != marotte.EventChatStatus {
			continue
		}
		p, ok := e.Payload.(marotte.ChatStatusPayload)
		if !ok {
			t.Fatalf("chat_status payload type = %T", e.Payload)
		}
		out = append(out, p)
	}
	return out
}

// An agent-authored focus update adopts the title onto the chat record and
// broadcasts the status/description as an ephemeral chat_status event.
func TestHandleSessionInfoUpdate_FocusAdoptsTitleAndStatus(t *testing.T) {
	deps, events, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", focusFrame(t, map[string]any{
		"title":       "Photo organizer CLI setup",
		"description": "Planning module layout and creating the stub main.",
		"status":      "in_progress",
	}), FrameAttribution{})

	c, ok := store.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat c1 missing")
	}
	if c.Name != "Photo organizer CLI setup" {
		t.Errorf("chat name = %q, want the focus title", c.Name)
	}
	got := chatStatusPayloads(t, events)
	if len(got) != 1 || got[0].Status != "in_progress" || got[0].Description == "" {
		t.Fatalf("chat_status payloads = %+v, want one in_progress with description", got)
	}
}

// A status/description-only focus update (the turn-completion shape) leaves
// the title untouched and still broadcasts chat_status.
func TestHandleSessionInfoUpdate_FocusStatusOnly(t *testing.T) {
	deps, events, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", focusFrame(t, map[string]any{
		"description": "Step 1 complete.",
		"status":      "completed",
	}), FrameAttribution{})

	c, _ := store.Get(t.Context(), "c1")
	if c.Name != "A" {
		t.Errorf("chat name = %q, want the seeded name untouched", c.Name)
	}
	got := chatStatusPayloads(t, events)
	if len(got) != 1 || got[0].Status != "completed" {
		t.Fatalf("chat_status payloads = %+v, want one completed", got)
	}
}

// Subagent focus frames are dropped by the parent-only gate.
func TestHandleSessionInfoUpdate_FocusDropsSubagent(t *testing.T) {
	deps, events, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", focusFrame(t, map[string]any{
		"title": "Sub focus", "status": "in_progress",
	}), FrameAttribution{SubSessionID: "sub-1"})

	c, _ := store.Get(t.Context(), "c1")
	if c.Name != "A" {
		t.Errorf("chat name = %q, want untouched", c.Name)
	}
	if got := chatStatusPayloads(t, events); len(got) != 0 {
		t.Fatalf("chat_status from a subagent frame: %+v", got)
	}
}

// safariPrompt and safariDerivedTitle are a byte-exact live case: a real first prompt and the
// title KAS derived from it, verbatim rather than computed.
const (
	safariPrompt = "safari on Mac throws this console error for marotte: " +
		"[Error] ResizeObserver loop completed\u2026"
	safariDerivedTitle = "Safari on Mac throws this console error for marotte: " +
		"[Error] ResizeObserver l..."
)

// TestHandleSessionInfoUpdate_FocusFiltersDerivedTitle pins that KAS's first-prompt derivation
// does not clobber the chat name. The first two cases are byte-exact live ones; the next four
// take one normalization each, so deleting a step names its case.
func TestHandleSessionInfoUpdate_FocusFiltersDerivedTitle(t *testing.T) {
	longPrompt := strings.Repeat("Fix the flaky retry test in the scheduler package. ", 4)
	cases := []struct {
		name    string
		userMsg string
		title   string
		adopt   bool
	}{
		{"short prompt verbatim", "Fix the retry test", "Fix the retry test", false},
		// Live: the poisoned chat's own first prompt was the single word "test".
		{"live case, first-rune case fold", "test", "Test", false},
		// Live: an ellipsized derivation, refused by the door's truncation rule, not this filter.
		{"live case, 80-rune truncation", safariPrompt, safariDerivedTitle, false},
		{"normalization: politeness filler", "can you fix the retry test", "Fix the retry test", false},
		{"normalization: leading markup", "> fix the retry test", "Fix the retry test", false},
		{"normalization: trailing punctuation", "fix the retry test.", "Fix the retry test", false},
		{"normalization: first non-blank line", "fix the retry test\n\nit fails on CI", "Fix the retry test", false},
		{"long prompt truncation", longPrompt, strings.TrimSpace(longPrompt)[:77] + "...", false},
		{"agent-authored", "Fix the retry test", "Scheduler retry flake", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, _, store := depsWithStore(t, "c1")
			if tc.userMsg != "" {
				withPrompts(deps, store, "c1", tc.userMsg)
			}
			tr := New(rolesOf(deps))

			tr.HandleSessionInfoUpdate(t.Context(), "c1", focusFrame(t, map[string]any{"title": tc.title}), FrameAttribution{})

			c, _ := store.Get(t.Context(), "c1")
			if tc.adopt && c.Name != tc.title {
				t.Errorf("chat name = %q, want adopted title %q", c.Name, tc.title)
			}
			if !tc.adopt && c.Name != "A" {
				t.Errorf("chat name = %q, want seeded name (derived title filtered)", c.Name)
			}
		})
	}
}

// TestTitleIsPromptDerived covers edge cases. A title that is the prompt VERBATIM with its
// lowercase opening is not KAS's derivation (KAS capitalizes), so it is not filtered.
func TestTitleIsPromptDerived(t *testing.T) {
	prompts := []string{"  padded prompt text  "}
	cases := []struct {
		name  string
		title string
		want  bool
	}{
		{"trims user message before compare", "Padded prompt text", true},
		{"the prompt's own lowercase opening is not KAS's derivation", "padded prompt text", false},
		{"prefix of the derivation is NOT derived", "Padded prompt", false},
		// A truncated derivation is the door's business, not a prefix comparison here.
		{"an ellipsized prefix is the door's business, not this one", "Padded prompt te...", false},
		{"unrelated", "Photo organizer CLI setup", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := titleIsPromptDerived(tc.title, prompts); got != tc.want {
				t.Errorf("titleIsPromptDerived(%q) = %v, want %v", tc.title, got, tc.want)
			}
		})
	}
}

// TestKASDerivedTitle_MirrorsUpstream pins recorded upstream answers: every case was verified
// byte-identical against the real SV under node (41 inputs), so a failure means the mirror drifted.
func TestKASDerivedTitle_MirrorsUpstream(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "first_rune_is_upper_cased", in: "test", want: "Test"},
		{name: "leading_markup_is_stripped", in: "> fix the retry test", want: "Fix the retry test"},
		{
			name: "several_markup_runes_are_stripped",
			in:   "*** > \"quoted heading",
			want: "Quoted heading",
		},
		{name: "trailing_punctuation_is_stripped", in: "fix the retry test.", want: "Fix the retry test"},
		{name: "one_politeness_filler", in: "can you fix the retry test", want: "Fix the retry test"},
		{
			// dtc loops, so a chain of fillers is stripped in one derivation.
			name: "a_chain_of_fillers",
			in:   "hello please can you help me to fix the retry test",
			want: "Fix the retry test",
		},
		{
			// The apostrophe is optional in KAS's alternation: both spellings strip.
			name: "an_apostrophe_optional_filler_without_it",
			in:   "lets fix it",
			want: "Fix it",
		},
		{
			name: "an_apostrophe_optional_filler_with_it",
			in:   "let's fix it",
			want: "Fix it",
		},
		{
			// The lookahead: "hi" must not eat the head of a word.
			name: "a_filler_that_is_only_a_word_prefix_is_not_stripped",
			in:   "hidden bug in the parser",
			want: "Hidden bug in the parser",
		},
		{
			// A filler followed by punctuation still counts, and the punctuation
			// goes with it.
			name: "a_filler_followed_by_punctuation",
			in:   "please. fix the retry test",
			want: "Fix the retry test",
		},
		{
			// SV's fallback: a filler-only prompt keeps its text.
			name: "a_prompt_that_is_only_a_filler_keeps_its_text",
			in:   "hi",
			want: "Hi",
		},
		{
			name: "the_first_non_blank_line_wins",
			in:   "fix the retry test\n\nit fails on CI",
			want: "Fix the retry test",
		},
		{
			name: "surrounding_whitespace_is_trimmed",
			in:   "  padded prompt text  ",
			want: "Padded prompt text",
		},
		{
			// The mirror stops short of SV's 80-rune cut (the shape gate refuses truncated titles).
			name: "an_over_long_derivation_is_NOT_truncated_here",
			in:   safariPrompt,
			want: "Safari on Mac throws this console error for marotte: " +
				"[Error] ResizeObserver loop completed\u2026",
		},
		{name: "an_empty_prompt_derives_nothing", in: "   ", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := kasDerivedTitle(tc.in); got != tc.want {
				t.Errorf("kasDerivedTitle(%q) =\n\t%q, want\n\t%q", tc.in, got, tc.want)
			}
		})
	}
}

// TestHandleSessionInfoUpdate_FocusRefusesRefusalShapedTitle pins the end-to-end defect: a
// model's request for more context must not be adopted as the chat's name.
func TestHandleSessionInfoUpdate_FocusRefusesRefusalShapedTitle(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	withPrompts(deps, store, "c1", "test")
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1",
		focusFrame(t, map[string]any{"title": liveRefusalTitle}), FrameAttribution{})

	c, _ := store.Get(t.Context(), "c1")
	if c.Name != "A" {
		t.Errorf("chat name = %q, want the seeded name: a model's refusal is not a title", c.Name)
	}
}

// TestHandleSessionInfoUpdate_FocusOnAContentFreeConversation pins, at the adoption door,
// that a non-title-shaped reply never becomes the name of a content-free conversation.
func TestHandleSessionInfoUpdate_FocusOnAContentFreeConversation(t *testing.T) {
	tests := []struct {
		name     string
		userMsg  string
		title    string
		wantName string
	}{
		{
			// Nothing persisted at all: the window before the first user message is visible.
			name:     "an_empty_conversation_refuses_a_refusal",
			userMsg:  "",
			title:    liveRefusalTitle,
			wantName: "A",
		},
		{
			name:     "a_one_word_conversation_refuses_a_refusal",
			userMsg:  "test",
			title:    "I need more context. What is the task",
			wantName: "A",
		},
		{
			// Not a blanket refusal: a real title for a one-word prompt is adopted.
			name:     "a_one_word_conversation_still_takes_a_real_title",
			userMsg:  "test",
			title:    "Scheduler retry flake",
			wantName: "Scheduler retry flake",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps, _, store := depsWithStore(t, "c1")
			if tc.userMsg != "" {
				withPrompts(deps, store, "c1", tc.userMsg)
			}
			tr := New(rolesOf(deps))

			tr.HandleSessionInfoUpdate(t.Context(), "c1",
				focusFrame(t, map[string]any{"title": tc.title}), FrameAttribution{})

			c, _ := store.Get(t.Context(), "c1")
			if c.Name != tc.wantName {
				t.Errorf("chat name after focus title %q = %q, want %q", tc.title, c.Name, tc.wantName)
			}
		})
	}
}

// TestHandleSessionInfoUpdate_FocusRefusesKASPlaceholder pins that KAS's placeholder,
// re-emitted after a revert empties the transcript, is never adopted.
func TestHandleSessionInfoUpdate_FocusRefusesKASPlaceholder(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1",
		focusFrame(t, map[string]any{"title": KASDefaultSessionTitle}), FrameAttribution{})

	c, _ := store.Get(t.Context(), "c1")
	if c.Name != "A" {
		t.Errorf("chat name = %q, want the seeded name: %q is KAS's placeholder",
			c.Name, KASDefaultSessionTitle)
	}
}

// TestHandleSessionInfoUpdate_FocusRefusalLevel pins the refusal's log level by whether it is
// expected traffic (truncations are).
func TestHandleSessionInfoUpdate_FocusRefusalLevel(t *testing.T) {
	tests := []struct {
		name      string
		title     string
		wantLevel string
	}{
		{
			name:      "a_truncated_derivation_is_routine",
			title:     "Fix the flaky retry test in the sched...",
			wantLevel: "DEBUG",
		},
		{
			name:      "a_refusal_shaped_reply_is_worth_reading",
			title:     "I cannot title this. Please provide the first message",
			wantLevel: "WARN",
		},
		{name: "kas_s_placeholder_is_worth_reading", title: KASDefaultSessionTitle, wantLevel: "WARN"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			t.Cleanup(captureSlog(&buf))
			deps, _, _ := depsWithStore(t, "c1")
			tr := New(rolesOf(deps))

			tr.HandleSessionInfoUpdate(t.Context(), "c1",
				focusFrame(t, map[string]any{"title": tc.title}), FrameAttribution{})

			line := buf.String()
			if !strings.Contains(line, `msg="focus title refused"`) {
				t.Fatalf("refusal of %q logged %q, want the refusal line", tc.title, line)
			}
			if !strings.Contains(line, "level="+tc.wantLevel) {
				t.Errorf("refusal of %q logged %q, want level=%s", tc.title, line, tc.wantLevel)
			}
		})
	}
}

// TestHandleSessionInfoUpdate_FocusTitleRuneCapIsInclusive pins that a title exactly at the
// rune cap is adopted and one past it is not.
func TestHandleSessionInfoUpdate_FocusTitleRuneCapIsInclusive(t *testing.T) {
	tests := []struct {
		name     string
		title    string
		wantName string
	}{
		{name: "exactly_at_the_cap", title: strings.Repeat("t", maxTitleRunes), wantName: strings.Repeat("t", maxTitleRunes)},
		{name: "one_rune_past_the_cap", title: strings.Repeat("t", maxTitleRunes+1), wantName: "A"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps, _, store := depsWithStore(t, "c1")
			tr := New(rolesOf(deps))

			tr.HandleSessionInfoUpdate(t.Context(), "c1", focusFrame(t, map[string]any{
				"title": tc.title, "status": "in_progress",
			}), FrameAttribution{})

			c, _ := store.Get(t.Context(), "c1")
			if c.Name != tc.wantName {
				t.Errorf("chat name after a %d-rune focus title = %q, want %q",
					len([]rune(tc.title)), c.Name, tc.wantName)
			}
		})
	}
}

// TestHandleSessionInfoUpdate_FocusBroadcastsOnlyWhenItHasSomethingToSay pins chat_status only
// when either half is present (two empty fields would blank the status line).
func TestHandleSessionInfoUpdate_FocusBroadcastsOnlyWhenItHasSomethingToSay(t *testing.T) {
	tests := []struct {
		name          string
		focus         map[string]any
		wantBroadcast bool
		wantStatus    string
	}{
		{
			name:          "status_without_a_description",
			focus:         map[string]any{"status": "completed"},
			wantBroadcast: true,
			wantStatus:    "completed",
		},
		{
			name:          "description_without_a_status",
			focus:         map[string]any{"description": "Step 1 complete."},
			wantBroadcast: true,
		},
		{
			name:          "neither_status_nor_description",
			focus:         map[string]any{"title": "Some title"},
			wantBroadcast: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps, events, _ := depsWithStore(t, "c1")
			tr := New(rolesOf(deps))

			tr.HandleSessionInfoUpdate(t.Context(), "c1", focusFrame(t, tc.focus), FrameAttribution{})

			got := chatStatusPayloads(t, events)
			if !tc.wantBroadcast {
				if len(got) != 0 {
					t.Fatalf("chat_status payloads = %+v, want none for %v", got, tc.focus)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("chat_status payloads = %+v, want exactly one for %v", got, tc.focus)
			}
			if got[0].Status != tc.wantStatus {
				t.Errorf("chat_status status = %q, want %q", got[0].Status, tc.wantStatus)
			}
		})
	}
}

// realFocusDescription is the longest live focus description measured (303 bytes): honest
// text the bound and sanitizer must not mangle.
const realFocusDescription = "Investigated marotte's empty agent-initiated turns. Root cause is " +
	"workflow-step frames folding onto the launching chat and opening a turn whose blocks " +
	"the client drops. Found an uncommitted fix already in the tree; now measuring whether " +
	"it eliminates the empty cards without costing the auto-wake label."

// TestHandleSessionInfoUpdate_FocusSanitizesAndBoundsTheDescription pins displayText at the
// door for an untrusted description with several rendering surfaces.
func TestHandleSessionInfoUpdate_FocusSanitizesAndBoundsTheDescription(t *testing.T) {
	tests := []struct {
		name string
		desc string
		want string
	}{
		{
			// U+202E reverses the rendered tooltip; U+202C pops it. Both become spaces,
			// so the deception is on screen rather than deleted with its evidence.
			name: "a_bidi_override_becomes_a_space",
			desc: "Running \u202Ednuof-eman- ecapskrow/ fr- mr\u202C now",
			want: "Running  dnuof-eman- ecapskrow/ fr- mr  now",
		},
		{
			// The two zero-width runes the policy DOES cover, because they are
			// Bidi_Control: U+200E LEFT-TO-RIGHT MARK and U+200F RIGHT-TO-LEFT MARK.
			name: "zero_width_bidi_marks_become_spaces",
			desc: "Write \u200Eshalom\u200F now",
			want: "Write  shalom  now",
		},
		{
			// C0, ESC and DEL each become a space (sequence stripping is sanitize.Output's job).
			name: "control_characters_become_spaces",
			desc: "Done\x07 with\x1b[31m the\x7f pass",
			want: "Done  with [31m the  pass",
		},
		{
			// C1 controls: JSON encoders emit them raw.
			name: "c1_controls_become_spaces",
			desc: "Step\u0085one\u009Btwo",
			want: "Step one two",
		},
		{
			// The tooltip renders \n as <br>, breaking the single-line contract.
			name: "an_embedded_newline_becomes_a_space",
			desc: "line one\nline two\r\n",
			want: "line one line two",
		},
		{
			// Legal unescaped in JSON, line terminators to a JS viewer.
			name: "paragraph_separators_become_spaces",
			desc: "a\u2028b\u2029c",
			want: "a b c",
		},
		{
			name: "a_real_description_passes_through_byte_identical",
			desc: realFocusDescription,
			want: realFocusDescription,
		},
		{
			// The marker rides OUTSIDE the cap (bound plus three); the exact string pins the number.
			name: "an_over_long_description_is_marked_at_the_bound",
			desc: strings.Repeat("x", 700),
			want: strings.Repeat("x", maxDisplayTextBytes) + "...",
		},
		{
			// A non-Bidi zero-width rune survives by design: rewriting it breaks ZWJ emoji.
			name: "a_non_bidi_zero_width_rune_survives_by_design",
			desc: "read\u200Bme \U0001F468\u200D\U0001F469\u200D\U0001F467",
			want: "read\u200Bme \U0001F468\u200D\U0001F469\u200D\U0001F467",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps, events, _ := depsWithStore(t, "c1")
			tr := New(rolesOf(deps))

			tr.HandleSessionInfoUpdate(t.Context(), "c1", focusFrame(t, map[string]any{
				"status": "in_progress", "description": tc.desc,
			}), FrameAttribution{})

			got := chatStatusPayloads(t, events)
			if len(got) != 1 {
				t.Fatalf("chat_status payloads = %+v, want exactly one", got)
			}
			if got[0].Description != tc.want {
				t.Errorf("broadcast description for %q =\n\t%q, want\n\t%q", tc.desc, got[0].Description, tc.want)
			}
		})
	}
}

// TestHandleSessionInfoUpdate_FocusDescriptionThatSanitizesToEmpty pins sanitize-before-the-
// both-empty-return: Merge reads a both-empty payload as a CLEAR, which would wipe a retained
// waiting_on_user.
func TestHandleSessionInfoUpdate_FocusDescriptionThatSanitizesToEmpty(t *testing.T) {
	tests := []struct {
		name          string
		focus         map[string]any
		wantBroadcast bool
		wantStatus    string
		wantDesc      string
	}{
		{
			// Under the bound, so it empties and the frame is dropped outright.
			name:          "description_only_publishes_nothing",
			focus:         map[string]any{"description": "\x07\x1b\x7f\u202E"},
			wantBroadcast: false,
		},
		{
			// The status half is real, so the frame goes out with an EMPTY description, which Merge
			// carries forward.
			name:          "with_a_status_it_publishes_the_status_alone",
			focus:         map[string]any{"status": "in_progress", "description": "\x07\x07\x07"},
			wantBroadcast: true,
			wantStatus:    "in_progress",
		},
		{
			// Past the bound the marker survives TrimSpace, so the result is non-empty (the safe direction).
			name:          "an_over_long_control_only_description_keeps_its_marker",
			focus:         map[string]any{"description": strings.Repeat("\x07", maxDisplayTextBytes+1)},
			wantBroadcast: true,
			wantDesc:      "...",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps, events, _ := depsWithStore(t, "c1")
			tr := New(rolesOf(deps))

			tr.HandleSessionInfoUpdate(t.Context(), "c1", focusFrame(t, tc.focus), FrameAttribution{})

			got := chatStatusPayloads(t, events)
			if !tc.wantBroadcast {
				if len(got) != 0 {
					// %q: a control-only description prints as empty under %+v.
					t.Fatalf("published %d chat_status frames for %v, want none (status=%q description=%q): "+
						"a both-empty payload deletes the retained entry",
						len(got), tc.focus, got[0].Status, got[0].Description)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("chat_status payloads = %+v, want exactly one", got)
			}
			if got[0].Status != tc.wantStatus {
				t.Errorf("chat_status status = %q, want %q", got[0].Status, tc.wantStatus)
			}
			if got[0].Description != tc.wantDesc {
				t.Errorf("chat_status description = %q, want %q", got[0].Description, tc.wantDesc)
			}
		})
	}
}

func TestHandleSessionInfoUpdate_FocusSanitizesTitle(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", focusFrame(t, map[string]any{
		"title": "\x1b[31mRelease\x1b[0m\ncheck",
	}), FrameAttribution{})

	c, _ := store.Get(t.Context(), "c1")
	if c.Name != "Release check" {
		t.Errorf("chat name = %q, want %q", c.Name, "Release check")
	}
}

// A user-named chat takes no agent title; a status still broadcasts. The control
// is the same frame on an agent-named chat.
func TestHandleSessionInfoUpdate_FocusTitleIgnoredOnUserNamedChat(t *testing.T) {
	for _, userNamed := range []bool{true, false} {
		deps, events, store := depsWithStore(t, "c1")
		if _, err := store.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
			c.Name = "My name"
			c.NameSetByUser = userNamed
			return true
		}); err != nil {
			t.Fatal(err)
		}
		tr := New(rolesOf(deps))

		tr.HandleSessionInfoUpdate(t.Context(), "c1", focusFrame(t, map[string]any{
			"title": "Refactor auth flow", "status": "in_progress",
		}), FrameAttribution{})

		c, _ := store.Get(t.Context(), "c1")
		want := "Refactor auth flow"
		if userNamed {
			want = "My name"
		}
		if c.Name != want {
			t.Errorf("user-named=%v: chat name = %q, want %q", userNamed, c.Name, want)
		}
		if got := chatStatusPayloads(t, events); len(got) != 1 {
			t.Errorf("user-named=%v: chat_status payloads = %+v, want one", userNamed, got)
		}
	}
}
