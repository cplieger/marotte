package kascap

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/translate"
)

// toolIDMetaSetRe matches the set of action types KAS stamps `_meta.kiro.toolId` for, declared
// beside the disclose_context reader.
var toolIDMetaSetRe = regexp.MustCompile(`new Set\(\[((?:"[a-z_]+",?)+)\]\);function [A-Za-z0-9_$]+\(t\)\{return [A-Za-z0-9_$]+\.has\(t\)\}function [A-Za-z0-9_$]+\(t,e\)\{if\(t!=="disclose_context"`)

// The pinned agent server titles a built-in from its tool class (`static id="x";static title="Y"`)
// or definition object (`{id:"x",title:"Y"`), else falls back to the bare id when it carries no
// `remote_` or `subagent_` prefix. Each row of the translator's table must be one of those.
func TestBuiltinToolTitlesMatchTheBundle(t *testing.T) {
	forEachBundle(t, func(t *testing.T, b bundle) {
		for title, id := range translate.BuiltinToolTitles() {
			class := `static id="` + id + `";static title="` + title + `"`
			object := `{id:"` + id + `",title:"` + title + `"`
			bare := title == id && !strings.HasPrefix(id, "remote_") && !strings.HasPrefix(id, "subagent_") &&
				strings.Contains(b.src, `"`+id+`"`) && !strings.Contains(b.src, `static id="`+id+`"`)
			if !strings.Contains(b.src, class) && !strings.Contains(b.src, object) && !bare {
				t.Errorf("%s: no tool titled %q with id %q; fix builtinToolByTitle in internal/translate/tool_identity.go", b.name, title, id)
			}
		}
	})
}

// builtinToolName prefers `_meta.kiro.toolId`, and keeps a title table only because KAS stamps it on
// so few tools. When the set grows, key on it and shrink builtinToolByTitle.
func TestToolIDMetaIsStampedOnTheKnownTools(t *testing.T) {
	forEachBundle(t, func(t *testing.T, b bundle) {
		m := toolIDMetaSetRe.FindStringSubmatch(b.src)
		if m == nil {
			t.Fatalf("%s: no toolId action-type set beside the disclose_context reader; re-anchor this test", b.name)
		}
		got := strings.Split(strings.ReplaceAll(m[1], `"`, ""), ",")
		want := []string{"disclose_context", "create_diagram"}
		if !slices.Equal(got, want) {
			t.Errorf("%s: KAS stamps toolId on %v, want %v", b.name, got, want)
		}
	})
}
