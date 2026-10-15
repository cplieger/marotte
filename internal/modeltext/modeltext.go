// Package modeltext reads catalog text a model provider authored: whether a description marks the
// model hidden from the user. Its own package because internal/bridge and internal/agent both apply
// the rule and neither owns the other.
package modeltext

import "strings"

// hiddenTags are the bracketed markers that hide an end-of-life model, lowercase because Hidden
// folds the description before comparing.
var hiddenTags = [...]string{
	"[deprecated]",
	"[legacy]",
}

// Hidden reports whether a model's description carries a hidden-model tag, in any case. The picker
// and the bridge's catalog both apply it.
func Hidden(description string) bool {
	low := strings.ToLower(description)
	for _, tag := range hiddenTags {
		if strings.Contains(low, tag) {
			return true
		}
	}
	return false
}
