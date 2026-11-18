package server

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/testsupport"
)

// docsCosts is what the docs answer costs the server per request for tests/perf/synth's .kiro tree at x1 and x10:
// a refetch of a tree the cache holds, and the scan after a change. A change that moves one by design rewrites its
// entry in the same diff.
var docsCosts = map[string]testsupport.Cost{
	"docs answer x1":          {Allocs: 1233, Bytes: 330504},
	"docs answer x10":         {Allocs: 10949, Bytes: 2972293},
	"docs after a change x1":  {Allocs: 12369, Bytes: 3243058},
	"docs after a change x10": {Allocs: 118333, Bytes: 31604462},
}

var docsWords = strings.Fields("the server reads each doc once and the client renders every row it receives " +
	"steering skill hook spec agent prompt review gate lane step build test lint branch")

func docsPara(r *rand.Rand, lo, hi int) string {
	w := make([]string, lo+r.IntN(hi-lo+1))
	for i := range w {
		w[i] = docsWords[r.IntN(len(docsWords))]
	}
	return strings.Join(w, " ") + "."
}

func docsParas(r *rand.Rand, n int) string {
	p := make([]string, n)
	for i := range p {
		p[i] = docsPara(r, 40, 90)
	}
	return strings.Join(p, "\n\n")
}

// writeSynthDocs writes fixtures.py's .kiro tree under work: per x1, 40 steering docs, 30 skills, 10 hooks, 5 spec
// features, 10 agents and 5 prompts.
func writeSynthDocs(t *testing.T, work string, scale int) {
	t.Helper()
	r := rand.New(rand.NewPCG(11, uint64(scale)))
	kiro := filepath.Join(work, ".kiro")
	for i := range 40 * scale {
		inc := [...]string{"always", "fileMatch", "manual"}[i%3]
		head := fmt.Sprintf("---\ninclusion: %s\ndescription: %q\n", inc, docsPara(r, 10, 25))
		if inc == "fileMatch" {
			head += fmt.Sprintf("fileMatchPattern: \"internal/pkg%02d/**\"\n", i)
		}
		writeFile(t, kiro, fmt.Sprintf("steering/doc-%03d.md", i), head+"---\n"+fmt.Sprintf("# Doc %d\n\n", i)+docsParas(r, 6))
	}
	for i := range 30 * scale {
		head := fmt.Sprintf("---\nname: skill-%03d\ndescription: %q\n---\n", i, docsPara(r, 15, 30))
		writeFile(t, kiro, fmt.Sprintf("skills/skill-%03d/SKILL.md", i), head+fmt.Sprintf("# Skill %d\n\n", i)+docsParas(r, 8))
	}
	for i := range 10 * scale {
		hook, err := json.Marshal(map[string]any{"version": "v1", "hooks": []any{map[string]any{
			"name": fmt.Sprintf("hook %d", i), "trigger": "PostFileSave", "matcher": `.*\.go$`, "timeout": 60,
			"action": map[string]string{"type": "command", "command": fmt.Sprintf("go vet ./internal/pkg%02d/...", i)},
		}}})
		if err != nil {
			t.Fatalf("Setup: %v", err)
		}
		writeFile(t, kiro, fmt.Sprintf("hooks/hook-%03d.json", i), string(hook))
	}
	for i := range 5 * scale {
		for _, name := range []string{"requirements", "design", "tasks"} {
			writeFile(t, kiro, fmt.Sprintf("specs/feature-%03d/%s.md", i, name), fmt.Sprintf("# %s %d\n\n", name, i)+docsParas(r, 10))
		}
	}
	for i := range 10 * scale {
		head := fmt.Sprintf("---\nname: agent-%03d\ndescription: %q\nmodel: model-%c\n---\n", i, docsPara(r, 8, 20), 'a'+rune(i%3))
		writeFile(t, kiro, fmt.Sprintf("agents/agent-%03d.md", i), head+docsPara(r, 60, 120))
	}
	for i := range 5 * scale {
		writeFile(t, kiro, fmt.Sprintf("prompts/prompt-%03d.md", i), fmt.Sprintf("# Prompt %d\n\n%s\n", i, docsPara(r, 20, 60)))
	}
}

func TestKiroDocsCost_DocsAnswerHoldsItsBudget(t *testing.T) {
	testsupport.RequireCostBuild(t)
	if !testsupport.Alone(t) {
		return
	}
	for _, scale := range []int{1, 10} {
		work := testsupport.FixedLengthDir(t)
		writeSynthDocs(t, work, scale)
		srv := &Server{workDir: work, kiroDocs: &docsCache{}}
		get := func() {
			rec := httptest.NewRecorder()
			srv.handleKiroDocs(rec, httptest.NewRequest(http.MethodGet, "/api/workspace/kiro-docs", http.NoBody))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET kiro-docs = %d: %s", rec.Code, rec.Body.String())
			}
		}
		if n := len(srv.collectKiroDocs(t.Context()).Docs); n < 100*scale {
			t.Fatalf("Setup: the docs answer lists %d rows, want every steering doc, skill, hook, spec, agent and prompt", n)
		}

		edits := 0
		for _, c := range []struct {
			op      string
			runs    int
			prepare func()
		}{
			{"docs answer", 101, func() {}},
			// One doc grows by a byte, which moves the tree's signature, as an edit does.
			{"docs after a change", testsupport.CostRuns(scale, 51, 11), func() {
				edits++
				writeFile(t, filepath.Join(work, ".kiro"), "steering/doc-000.md", "---\ninclusion: manual\n---\n"+strings.Repeat("x", edits))
			}},
		} {
			op := fmt.Sprintf("%s x%d", c.op, scale)
			t.Run(op, func(t *testing.T) {
				got := testsupport.MeasureCostAfter(c.runs, c.prepare, get)
				testsupport.HoldCost(t, op, docsCosts[op], got)
			})
		}
	}
}
