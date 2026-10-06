package command

// Prompt text KAS claims before the model: `session/prompt` answers a few verbs itself with
// `stopReason: end_turn` and no content, which the empty-turn recovery would misread as a dead turn
// and re-send, launching a second run (measured on `/goal`, kiro-cli 2.18.1). `/goal` is the only
// member, because marotte sends the `goal` setting at the connection door. Not a general
// slash-command table.

import (
	"regexp"
	"strings"
)

// goalPrefix is the only spelling KAS's parser accepts before an objective.
const goalPrefix = "/goal "

// goalMaxSuffix matches the trailing `--max N` bound on a goal objective.
// Anchored at the end with leading whitespace required, exactly as KAS's
// own parser.
var goalMaxSuffix = regexp.MustCompile(`\s+--max\s+(\d+)$`)

// kasClaimsPromptText reports whether KAS's prompt path answers this text without invoking the
// model. Its client twin is static-src/chat-options.ts, whose test transcribes KAS's parser; the
// three must agree or the empty-turn bug returns.
func kasClaimsPromptText(text string) bool {
	return parsesAsGoalCommand(text)
}

// parsesAsGoalCommand mirrors KAS's parseGoalCommand, reduced to whether it
// returns a goal rather than nil. The bound itself is deliberately not
// returned — KAS owns the clamp and default.
func parsesAsGoalCommand(text string) bool {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, goalPrefix) {
		return false
	}
	body := strings.TrimSpace(strings.TrimPrefix(trimmed, goalPrefix))
	if body == "" {
		return false
	}
	// The regexp runs on the trimmed body, so a bound with nothing before it stays part of the
	// objective.
	if m := goalMaxSuffix.FindStringIndex(body); m != nil {
		return strings.TrimSpace(body[:m[0]]) != ""
	}
	return true
}
