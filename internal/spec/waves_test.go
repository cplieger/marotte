package spec

import (
	"strconv"
	"strings"
	"testing"
)

// graph wraps a JSON body in the section shape a real tasks.md carries.
func graph(body string) string {
	return "# Tasks\n\n- [ ] 1. one\n- [ ] 2. two\n\n## Task Dependency Graph\n\n```json\n" + body + "\n```\n"
}

func TestParseWaves_ReadsTheFencedSection(t *testing.T) {
	got := parseWaves(splitLines([]byte(graph(`{"waves":[{"id":0,"tasks":["1.1","1.2"]},{"id":1,"tasks":["2.1"]}]}`))))
	want := map[string]int{"1.1": 0, "1.2": 0, "2.1": 1}
	if len(got) != len(want) {
		t.Fatalf("parseWaves(a two-wave graph) = %v, want %v", got, want)
	}
	for id, wave := range want {
		if got[id] != wave {
			t.Errorf("parseWaves(a two-wave graph)[%q] = %d, want %d", id, got[id], wave)
		}
	}
}

func TestParseWaves_AnswersNilOnEveryMalformedShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{"no section at all", "# Tasks\n\n- [ ] 1. one\n"},
		{"a section with no fence", "## Task Dependency Graph\n\n{\"waves\":[{\"id\":0,\"tasks\":[\"1.1\"]}]}\n"},
		{"a fence the decoder refuses", graph(`{"waves":[{"id":0,`)},
		{"a body that is not an object", graph(`["1.1"]`)},
		{"no waves key", graph(`{"batches":[{"id":0,"tasks":["1.1"]}]}`)},
		{"an empty waves list", graph(`{"waves":[]}`)},
		{"a wave naming no task", graph(`{"waves":[{"id":0,"tasks":[]}]}`)},
		{"a non-integer id", graph(`{"waves":[{"id":"first","tasks":["1.1"]}]}`)},
		{"a negative id", graph(`{"waves":[{"id":-1,"tasks":["1.1"]}]}`)},
		{"one task claimed by two waves", graph(`{"waves":[{"id":0,"tasks":["1.1"]},{"id":1,"tasks":["1.1"]}]}`)},
		{"an unclosed fence", "## Task Dependency Graph\n\n```json\n{\"waves\":[{\"id\":0,\"tasks\":[\"1.1\"]}]}\n"},
		{"prose before the fence", "## Task Dependency Graph\n\nThe graph:\n\n```json\n{\"waves\":[{\"id\":0,\"tasks\":[\"1.1\"]}]}\n```\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parseWaves(splitLines([]byte(test.src))); got != nil {
				t.Errorf("parseWaves(%s) = %v, want nil", test.name, got)
			}
		})
	}
}

func TestParseWaves_RefusesABodyOverTheCap(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"waves":[{"id":0,"tasks":[`)
	for i := 0; b.Len() <= maxWaveBytes; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"` + strconv.Itoa(i) + `.1"`)
	}
	b.WriteString(`]}]}`)
	if got := parseWaves(splitLines([]byte(graph(b.String())))); got != nil {
		t.Errorf("parseWaves(a body over %d bytes) returned %d ids, want nil", maxWaveBytes, len(got))
	}
}

func TestParseWaves_TakesTheFirstSectionAndAnyHeadingLevel(t *testing.T) {
	src := "### task dependency GRAPH\n\n```json\n" +
		`{"waves":[{"id":3,"tasks":["1.1"]}]}` + "\n```\n\n## Task Dependency Graph\n\n```json\n" +
		`{"waves":[{"id":9,"tasks":["1.1"]}]}` + "\n```\n"
	got := parseWaves(splitLines([]byte(src)))
	if got["1.1"] != 3 {
		t.Errorf("parseWaves(two sections, case-varied ###)[\"1.1\"] = %d, want 3 (the first section)", got["1.1"])
	}
}

func TestParse_AssignsWavesByDottedNumber(t *testing.T) {
	src := "# Tasks\n\n- [ ] 1. parent\n  - [ ] 1.1 child\n- [ ] unnumbered\n\n## Task Dependency Graph\n\n```json\n" +
		`{"waves":[{"id":0,"tasks":["1"]},{"id":4,"tasks":["1.1","9.9"]}]}` + "\n```\n"
	p := Parse([]byte(src))
	if len(p.Tasks) != 2 {
		t.Fatalf("Parse(a numbered parent plus an unnumbered task) returned %d roots, want 2", len(p.Tasks))
	}
	parent := p.Tasks[0]
	if parent.Wave == nil || *parent.Wave != 0 {
		t.Errorf("Parse(...).Tasks[0] (%q) wave = %v, want 0 — a real wave id of 0 must survive the pointer", parent.Text, parent.Wave)
	}
	if len(parent.Children) != 1 {
		t.Fatalf("Parse(...).Tasks[0] has %d children, want 1", len(parent.Children))
	}
	if child := parent.Children[0]; child.Wave == nil || *child.Wave != 4 {
		t.Errorf("Parse(...).Tasks[0].Children[0] (%q) wave = %v, want 4 — a child row carries its own wave", child.Text, child.Wave)
	}
	if unnumbered := p.Tasks[1]; unnumbered.Wave != nil {
		t.Errorf("Parse(...).Tasks[1] (%q) wave = %v, want nil — a task line carrying no number gets no wave", unnumbered.Text, *unnumbered.Wave)
	}
}

func TestParse_LeavesEveryWaveNilWhenTheGraphIsBroken(t *testing.T) {
	src := "# Tasks\n\n- [ ] 1. one\n- [ ] 2. two\n\n## Task Dependency Graph\n\n```json\n" +
		`{"waves":[{"id":0,"tasks":["1","2"]},{"id":1,"tasks":["2"]}]}` + "\n```\n"
	p := Parse([]byte(src))
	for _, n := range p.Tasks {
		if n.Wave != nil {
			t.Errorf("Parse(a graph claiming task 2 twice).Tasks %q wave = %d, want nil on EVERY row: a contradictory graph cannot say which wave any task is in", n.Text, *n.Wave)
		}
	}
}
