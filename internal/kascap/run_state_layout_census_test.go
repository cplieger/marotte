package kascap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"testing"
)

var (
	runBaseDirRe   = regexp.MustCompile(`workflowBaseDirFor\(\w+\)\{return \w+\.join\(this\.host\.sessionsPath,\w+\(\[\.\.\.\w+\]\),([\w$]+)\)\}`)
	runStateFileRe = regexp.MustCompile(`function [\w$]+\(\w+,\w+\)\{return \w+\.join\(([\w$]+)\(\w+,\w+\),"workflow-state\.json"\)\}`)
)

// reaperRunsDirName reads kirosession's workflowsDirName, the directory the reaper and the keep-state reconcile
// list for a bucket's run state.
func reaperRunsDirName(t *testing.T) string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "../kirosession/reaper.go", nil, 0)
	if err != nil {
		t.Fatalf("parse reaper.go: %v", err)
	}
	var name string
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || spec.Names[0].Name != "workflowsDirName" || len(spec.Values) != 1 {
			return true
		}
		if lit, ok := spec.Values[0].(*ast.BasicLit); ok {
			name, _ = strconv.Unquote(lit.Value)
		}
		return false
	})
	if name == "" {
		t.Fatal("reaper.go declares no workflowsDirName string constant")
	}
	return name
}

// TestRunStateLayoutMatchesBundle pins the layout kirosession.Reaper.RunStates reads: KAS keeps a run's state in
// sessions/<bucket>/<workflowsDirName>/<workflowId>/. If a kiro-cli release moves it, RunStates answers nothing, the
// fill-in reads every run record as stateless, and the step-session spare fails the same way.
func TestRunStateLayoutMatchesBundle(t *testing.T) {
	want := reaperRunsDirName(t)
	forEachBundle(t, func(t *testing.T, b bundle) {
		base := runBaseDirRe.FindAllStringSubmatch(b.src, -1)
		if len(base) != 1 {
			t.Fatalf("workflowBaseDirFor: found %d definitions joining sessionsPath, a bucket and a name; want 1", len(base))
		}
		nameRe := regexp.MustCompile(`[,;\s]` + regexp.QuoteMeta(base[0][1]) + `="([^"]+)"`)
		name := submatches(b.src, findAround(nameRe, base[0][1]+`="`, b.src))
		if name == nil {
			t.Fatalf("the runs directory variable %s has no string assignment in the bundle", base[0][1])
		}
		if name[1] != want {
			t.Errorf("bundle runs directory = %q, reaper workflowsDirName = %q", name[1], want)
		}

		state := runStateFileRe.FindAllStringSubmatch(b.src, -1)
		if len(state) != 1 {
			t.Fatalf("workflow-state.json: found %d path builders, want 1", len(state))
		}
		dirRe := regexp.MustCompile(`function ` + regexp.QuoteMeta(state[0][1]) + `\((\w+),(\w+)\)\{return \w+\.join\((\w+),(\w+)\)\}`)
		dir := dirRe.FindStringSubmatch(b.src)
		if dir == nil || dir[1] != dir[3] || dir[2] != dir[4] {
			t.Errorf("the run state directory builder %s is not join(base, workflowId): %v", state[0][1], dir)
		}
	})
}

func TestRunStateLayoutExtractors(t *testing.T) {
	const src = `var zV="workflows";class X{workflowBaseDirFor(e){return ME.join(this.host.sessionsPath,Pw([...e]),zV)}}` +
		`function kv(t,e){return ICe.join(t,e)}function RP(t,e){return ICe.join(kv(t,e),"workflow-state.json")}`
	if m := runBaseDirRe.FindStringSubmatch(src); m == nil || m[1] != "zV" {
		t.Errorf("runBaseDirRe = %v, want the planted variable zV", m)
	}
	if m := runStateFileRe.FindStringSubmatch(src); m == nil || m[1] != "kv" {
		t.Errorf("runStateFileRe = %v, want the planted builder kv", m)
	}
}
