package kascap

import (
	"regexp"
	"strings"
	"testing"
)

// zodObjectRe matches `<name>=<zod>.object({<members>})<tail>` up to the declaration's own `,` or `;`, tail being
// an optional `.method()`, so a match never runs on into the next declaration of the same var statement.
var zodObjectRe = regexp.MustCompile(`[\w$]+=[\w$]+\.object\(\{([^;]*?)\}\)(\.\w+\(\))?[,;]`)

// TestPromptMetaSchemaStillPassesUnknownKeys pins the premise of the callerLabel row: KAS parses session/prompt's
// _meta.kiro with a .passthrough() schema, so a key it does not name is kept on the user row. A .strip() would drop
// the label; a .strict() would reject the whole block, displayText included.
func TestPromptMetaSchemaStillPassesUnknownKeys(t *testing.T) {
	forEachBundle(t, func(t *testing.T, b bundle) {
		var tails []string
		for _, m := range zodObjectRe.FindAllStringSubmatch(b.src, -1) {
			if strings.Contains(m[1], "displayText:") && strings.Contains(m[1], "agentInitiatedReason:") {
				tails = append(tails, m[2])
			}
		}
		if len(tails) != 1 {
			t.Fatalf("found %d zod objects naming displayText and agentInitiatedReason, want exactly 1; re-anchor on KAS's session/prompt _meta.kiro schema", len(tails))
		}
		if tails[0] != ".passthrough()" {
			t.Errorf("KAS's session/prompt _meta.kiro schema ends in %q, want \".passthrough()\": the callerLabel row no longer reaches the transcript, so withhold or re-home it", tails[0])
		}
	})
}
