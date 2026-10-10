package server

import (
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/cplieger/marotte/internal/steering"
)

// Both scans are checked against steering.DedupeAgentFiles' answer rather than restated
// expectations.
var agentDoorFixture = map[string]*fstest.MapFile{
	"agents/.hidden.md":     {Data: []byte("# hidden\n")},
	"agents/.md":            {Data: []byte("")},
	"agents/README.txt":     {Data: []byte("not an agent\n")},
	"agents/deploy.json":    {Data: []byte(`{"name":"deploy"}`)},
	"agents/notes.md":       {Data: []byte("# notes\n")},
	"agents/reviewer.json":  {Data: []byte(`{"name":"reviewer"}`)},
	"agents/reviewer.md":    {Data: []byte("---\nname: reviewer\n---\n# reviewer\n")},
	"agents/nested/keep.md": {Data: []byte("# inside a subdirectory\n")},
}

// ruleFor returns the shared rule's answer for the fixture, so expected files are DERIVED.
func ruleFor(t *testing.T, root fs.FS) []steering.AgentFile {
	t.Helper()
	entries, err := fs.ReadDir(root, "agents")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return steering.DedupeAgentFiles(entries)
}

// TestAgentScanDoorsAgreeWithTheSharedRule pins that both REST scans resolve one listing to
// the files the rule names.
func TestAgentScanDoorsAgreeWithTheSharedRule(t *testing.T) {
	root := fstest.MapFS(agentDoorFixture)
	want := ruleFor(t, root)
	if len(want) == 0 {
		t.Fatal("the fixture resolves to no agents; the assertions below would be vacuous")
	}

	t.Run("entity scan (kiro_config)", func(t *testing.T) {
		items := scanAgents(t.Context(), root, "ws/.kiro")
		if len(items) != len(want) {
			t.Fatalf("scanAgents = %+v (len %d), want the rule's %+v (len %d)", items, len(items), want, len(want))
		}
		for i, a := range want {
			if items[i].Name != a.Base {
				t.Errorf("scanAgents[%d].Name = %q, the rule says %q", i, items[i].Name, a.Base)
			}
			if wantPath := "ws/.kiro/agents/" + a.File; items[i].Path != wantPath {
				t.Errorf("scanAgents[%d].Path = %q, want %q", i, items[i].Path, wantPath)
			}
			if items[i].Type != "agent" {
				t.Errorf("scanAgents[%d].Type = %q, want %q", i, items[i].Type, "agent")
			}
		}
	})

	t.Run("document scan (kiro_docs)", func(t *testing.T) {
		// A nil guard admits everything: this asserts the dedupe, not the provenance rules.
		docs := scanDocsAgents(t.Context(), root, "ws/.kiro", nil).docs
		if len(docs) != len(want) {
			t.Fatalf("scanDocsAgents = %+v (len %d), want the rule's %+v (len %d)", docs, len(docs), want, len(want))
		}
		for i, a := range want {
			if wantPath := "ws/.kiro/agents/" + a.File; docs[i].Path != wantPath {
				t.Errorf("scanDocsAgents[%d].Path = %q, want %q", i, docs[i].Path, wantPath)
			}
			if docs[i].Category != catAgent {
				t.Errorf("scanDocsAgents[%d].Category = %q, want %q", i, docs[i].Category, catAgent)
			}
		}
	})
}

// TestAgentScanDoorsSkipTheSameNonAgents pins that entries the rule refuses appear in
// neither door's output.
func TestAgentScanDoorsSkipTheSameNonAgents(t *testing.T) {
	root := fstest.MapFS(agentDoorFixture)
	refused := []string{".hidden", "", "README", "nested", "reviewer.json"}

	items := scanAgents(t.Context(), root, "ws/.kiro")
	docs := scanDocsAgents(t.Context(), root, "ws/.kiro", nil).docs

	for _, name := range refused {
		for i, it := range items {
			if it.Name == name {
				t.Errorf("scanAgents[%d] listed %q, which the rule refuses", i, name)
			}
		}
		for i, d := range docs {
			// reviewer.json is refused as a FILE (its .md pair wins), so the path is asserted.
			if d.Path == "ws/.kiro/agents/"+name {
				t.Errorf("scanDocsAgents[%d] listed %q, which the rule refuses", i, name)
			}
		}
	}
}
