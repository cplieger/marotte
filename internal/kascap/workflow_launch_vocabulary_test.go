package kascap

import (
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestWorkflowLaunchVocabularyPresent pins the workflowLaunch option id and its three values in the pinned agent
// server: the session doors assert them by literal, and KAS ignores a value it does not know.
func TestWorkflowLaunchVocabularyPresent(t *testing.T) {
	active := activeKASVersion(t)
	literals := []string{
		marotte.ConfigOptionWorkflowLaunch,
		marotte.WorkflowLaunchWhenAsked,
		marotte.WorkflowLaunchLargeTasks,
		marotte.WorkflowLaunchDelegateAll,
	}
	forEachBundle(t, func(t *testing.T, b bundle) {
		for _, lit := range literals {
			if !strings.Contains(b.src, `"`+lit+`"`) {
				t.Errorf("kiro-cli %s does not carry the workflowLaunch literal %q; the Workflow use setting would assert a value KAS ignores. Bundle: %s",
					active, lit, b.path)
			}
		}
	})
}
