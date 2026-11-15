package kascap

import (
	"regexp"
	"testing"
)

var (
	// sessionListFilterSchemaRe is session/list's `_meta.kiro.filter` schema, the one narrowing KAS reads there.
	sessionListFilterSchemaRe = regexp.MustCompile(
		`filter:` + jsIdent + `\.object\(\{workflowSessions:` + jsIdent + `\.enum\(\["include","exclude"\]\)`,
	)
	// sessionListExcludeRe is listSessions dropping a step row when the filter says "exclude".
	sessionListExcludeRe = regexp.MustCompile(
		`\?\.filter\?\.workflowSessions==="exclude"\?` + jsIdent + `\.filter\(`,
	)
)

// TestSessionListStillExcludesStepSessions pins the premise of agent's workspaceSessionRows: KAS's session/list
// takes `_meta.kiro.filter.workflowSessions: "exclude"` and leaves step sessions out of the answer, which keeps a
// step-heavy workspace's answer under the engine's frame cap.
func TestSessionListStillExcludesStepSessions(t *testing.T) {
	forEachBundle(t, func(t *testing.T, b bundle) {
		if n := len(sessionListFilterSchemaRe.FindAllStringIndex(b.src, -1)); n != 1 {
			t.Errorf("found %d session/list filter.workflowSessions include/exclude schemas, want exactly 1; "+
				"without it History's list carries every step session again", n)
		}
		if n := len(sessionListExcludeRe.FindAllStringIndex(b.src, -1)); n != 1 {
			t.Errorf("found %d listSessions filter.workflowSessions===\"exclude\" arms, want exactly 1; "+
				"re-anchor on KAS's listSessions handler", n)
		}
	})
}
