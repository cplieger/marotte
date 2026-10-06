package translate

// v3 focus updates (session_info_update, kind "focus_update"). THREE writers share the
// channel with no field naming the speaker (the agent's tool, KAS's first-prompt derivation,
// KAS's LLM title), so the filters go on shape, BEFORE the write: adoption is a one-way latch.

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
)

// focusUpdate is the _meta.kiro.focus block of a focus_update
// session_info_update. All fields are optional partial updates.
type focusUpdate struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"`
}

// handleFocusUpdate adopts the title onto the chat record unless a rule refuses it, and
// broadcasts status/description as an ephemeral chat_status. Parent-only. The door rule runs
// here; the derivation filter needs the chat's prompts and runs in applyFocusTitle.
func (t *Translator) handleFocusUpdate(ctx context.Context, chatID marotte.ChatID, f *focusUpdate) {
	if title := SanitizeTitle(f.Title); title != "" {
		t.adoptOrRefuseTitle(ctx, chatID, title)
	}
	status := strings.TrimSpace(f.Status)
	// displayText alone (see display_text.go), and BEFORE the both-empty return: a control-only
	// description empties here, and chatStatusCache.Merge reads both-empty as a clear.
	desc := strings.TrimSpace(displayText(f.Description))
	if status == "" && desc == "" {
		return
	}
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventChatStatus, chatID, marotte.ChatStatusPayload{
		Status:      status,
		Description: desc,
	}))
}

// adoptOrRefuseTitle writes a sanitized focus title to the chat, or says why not; refusing
// leaves the existing name, always the better answer.
func (t *Translator) adoptOrRefuseTitle(ctx context.Context, chatID marotte.ChatID, title string) {
	reason := TitleRefusal(title)
	switch reason {
	case "":
		t.applyFocusTitle(ctx, chatID, title)
	// A truncated title is KAS's own derivation (expected traffic), so it logs quieter.
	case refusalTruncated:
		slog.Debug("focus title refused", "chat_id", chatID, "title", title, "reason", reason)
	default:
		slog.Warn("focus title refused", "chat_id", chatID, "title", title, "reason", reason)
	}
}

// applyFocusTitle writes an agent-authored title onto the chat. The derivation
// filter reads the log's prompts ahead of the header write; Mutate broadcasts
// chat_updated on change, which is what flips the tab label live.
func (t *Translator) applyFocusTitle(ctx context.Context, chatID marotte.ChatID, title string) {
	prompts, err := t.chats.PromptTexts(ctx, chatID)
	if errors.Is(err, chat.ErrTombstoned) || errors.Is(err, chat.ErrChatNotFound) {
		return
	}
	if err != nil {
		slog.Error("focus title: prompts unreadable, title dropped", "chat_id", chatID, "error", err)
		return
	}
	if titleIsPromptDerived(title, prompts) {
		return
	}
	renamed := false
	_, err = t.chats.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
		// A user-named chat takes no lower rung (this also swallows KAS's echo of the rename).
		if !exists || c.NameSetByUser || c.Name == title {
			return false
		}
		c.Name = title
		renamed = true
		return true
	})
	if errors.Is(err, chat.ErrTombstoned) {
		return
	}
	if err != nil {
		slog.Error("focus title: persist", "chat_id", chatID, "error", err)
		return
	}
	if renamed {
		slog.Info("chat titled by agent focus update", "chat_id", chatID, "title", title)
	}
}

// titleIsPromptDerived reports whether title is KAS's first-prompt derivation rather than an
// agent-authored name (rung 1 beating rung 2). It compares against kasDerivedTitle because SV
// normalizes five ways before truncating; a byte-exact filter missed real derivations.
func titleIsPromptDerived(title string, prompts []string) bool {
	for _, text := range prompts {
		if kasDerivedTitle(text) == title {
			return true
		}
	}
	return false
}

// kasFillerPhrases mirrors KAS's utc alternation, IN ITS ORIGINAL ORDER: a
// JavaScript regex alternation is leftmost-first, so "help me to" must be
// tried before "help me" or the shorter phrase wins and this mirror strips
// less than KAS did. The `'?`-optional spellings are expanded in place.
var kasFillerPhrases = []string{
	"hi", "hey", "hello", "please", "pls", "plz",
	"can you", "could you", "would you", "will you", "can u", "could u",
	"i want to", "i wanna", "i'd like to", "id like to",
	"i would like to", "i need to", "i need you to",
	"i'm trying to", "im trying to", "i am trying to",
	"help me to", "help me", "let's", "lets", "let me",
	"we need to", "we should",
}

// kasMaxFillerStrips is KAS's ltc: dtc strips at most six leading fillers.
const kasMaxFillerStrips = 6

// kasDerivedTitle returns the title KAS's SV would derive from text, read off the pinned
// bundle (2.21.2-f6262ea4…, `function SV(e)`); re-read it after a kiro-cli bump. It skips SV's
// 80-rune truncation (the door refuses truncated titles); any divergence is a MISS, never an
// over-filter.
func kasDerivedTitle(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	line := firstNonBlankLine(trimmed)
	stripped := trimLeadingMarkup(line)
	out := stripFillerPhrases(stripped)
	// SV's fallback: a prompt of nothing but fillers keeps the pre-strip text.
	if out == "" {
		if stripped != "" {
			out = stripped
		} else {
			out = line
		}
	}
	out = trimTrailingPunctuation(out)
	if out == "" {
		out = line
	}
	return upperFirstRune(out)
}

// firstNonBlankLine mirrors SV's line selection: split on \r?\n and take the
// first line with non-space content, trimmed.
func firstNonBlankLine(s string) string {
	for line := range strings.Lines(s) {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// isKASMarkup reports membership of SV's leading-markup class.
func isKASMarkup(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune(">#*`\"'", r)
}

// isKASTrailingPunctuation reports membership of SV's trailing class.
func isKASTrailingPunctuation(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune(".,;:!?", r)
}

func trimLeadingMarkup(s string) string { return strings.TrimLeftFunc(s, isKASMarkup) }

func trimTrailingPunctuation(s string) string {
	return strings.TrimRightFunc(s, isKASTrailingPunctuation)
}

// stripFillerPhrases mirrors KAS's dtc: up to six passes, each removing one
// leading filler plus the punctuation and whitespace behind it, then
// re-stripping leading markup. It stops early when a pass changes nothing.
func stripFillerPhrases(s string) string {
	out := s
	for range kasMaxFillerStrips {
		next := stripOneFillerPhrase(out)
		if next == out {
			break
		}
		out = trimLeadingMarkup(next)
	}
	return out
}

// stripOneFillerPhrase removes the first matching filler, or returns s
// unchanged. The lookahead KAS spells `(?=[.,;:!?]*(?:\s|$))` is what stops
// "hi" eating the head of "hidden bug": a filler only counts when the next
// thing after its optional punctuation is whitespace or the end of the line.
func stripOneFillerPhrase(s string) string {
	lower := strings.ToLower(s)
	for _, phrase := range kasFillerPhrases {
		if !strings.HasPrefix(lower, phrase) {
			continue
		}
		rest := s[len(phrase):]
		if !fillerBoundaryFollows(rest) {
			continue
		}
		return strings.TrimLeftFunc(rest, isKASTrailingPunctuation)
	}
	return s
}

// fillerBoundaryFollows reports whether rest opens with the lookahead's
// `[.,;:!?]*` run followed by whitespace or end of string.
func fillerBoundaryFollows(rest string) bool {
	for _, r := range rest {
		if strings.ContainsRune(".,;:!?", r) {
			continue
		}
		return unicode.IsSpace(r)
	}
	return true
}

// upperFirstRune mirrors SV's final `charAt(0).toUpperCase()`.
func upperFirstRune(s string) string {
	for i, r := range s {
		return string(unicode.ToUpper(r)) + s[i+utf8.RuneLen(r):]
	}
	return s
}
