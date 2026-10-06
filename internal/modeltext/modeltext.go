// Package modeltext reads strings a MODEL authored and returns the usable part: whether a catalog
// description marks the model hidden, and a completion with its markdown wrapper off. Its own
// package because five packages call it and none owns the others; HiddenTags is a function so no
// importer can mutate it.
package modeltext

import (
	"slices"
	"strings"
)

// hiddenTags are the bracketed markers used by the UI model picker and the
// bridge to hide end-of-life models from the user.
//
// Deliberately NARROWER than internal/agent's ambient-selection set, which adds
// [internal], [experimental], [eol] and the prose preview marker: those models
// are SHOWN in the picker and merely excluded from ambient-task selection, so
// the two policies are not the same list and must not be merged into one.
var hiddenTags = []string{
	"[deprecated]",
	"[legacy]",
}

// HiddenTags returns the hidden-model markers as a fresh slice, for a caller
// composing a wider policy on top. A caller that only needs the standard rule
// should use Hidden instead.
func HiddenTags() []string {
	return slices.Clone(hiddenTags)
}

// Hidden reports whether a model's description marks it hidden from the user.
// This is the rule the picker and the bridge apply; it is a named policy rather
// than a tag list four call sites happen to pass the same way.
func Hidden(description string) bool {
	return HasAnyTag(description, hiddenTags)
}

// HasAnyTag reports whether text contains any of the given bracketed tags.
// The tags must already be lowercase; text is folded before comparison.
func HasAnyTag(text string, tags []string) bool {
	if text == "" {
		return false
	}
	low := strings.ToLower(text)
	for _, tag := range tags {
		if strings.Contains(low, tag) {
			return true
		}
	}
	return false
}

// StripCodeFence removes one wrapping markdown fence (the outermost) from model output, which goes
// straight into an editor buffer or commit message; fences inside the content are preserved.
func StripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop the opening fence line (```lang or ```).
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	} else {
		return s
	}
	// Drop trailing ``` (optionally followed by whitespace).
	s = strings.TrimRight(s, " \t\n\r")
	if strings.HasSuffix(s, "```") {
		s = strings.TrimRight(s[:len(s)-3], " \t\n\r")
	}
	return s
}
