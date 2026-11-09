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
	runBaseDirRe     = regexp.MustCompile(`workflowBaseDirFor\(\w+\)\{return \w+\.join\(this\.host\.sessionsPath,\w+\(\[\.\.\.\w+\]\),([\w$]+)\)\}`)
	runStateParentRe = regexp.MustCompile(`probeParentSessionIdFromDisk\(\w+,\w+\)\{return\(await this\.probeWorkflowStateFromDisk\(\w+,\w+\)\)\?\.state\.([\w$]+)\}`)
	runStateReadRe   = regexp.MustCompile(`let \w+=JSON\.parse\(\w+\);return \w+\.parse\(\w+\)\}[\w$]+\(\w+,"readStateFile"\)`)
)

// runStateFileRe matches the bundle's builder of a run's state-file path named file, capturing the directory builder.
func runStateFileRe(file string) *regexp.Regexp {
	return regexp.MustCompile(`function [\w$]+\(\w+,\w+\)\{return \w+\.join\(([\w$]+)\(\w+,\w+\),"` + regexp.QuoteMeta(file) + `"\)\}`)
}

// reaperStringConst answers the value of the string constant named constName in kirosession's reaper.go, the
// reaper's one declaration of that piece of KAS's run-state layout.
func reaperStringConst(t *testing.T, constName string) string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "../kirosession/reaper.go", nil, 0)
	if err != nil {
		t.Fatalf("parse reaper.go: %v", err)
	}
	var value string
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || spec.Names[0].Name != constName || len(spec.Values) != 1 {
			return true
		}
		if lit, ok := spec.Values[0].(*ast.BasicLit); ok {
			value, _ = strconv.Unquote(lit.Value)
		}
		return false
	})
	if value == "" {
		t.Fatalf("reaper.go declares no %s string constant", constName)
	}
	return value
}

// TestRunStateLayoutMatchesBundle pins the layout kirosession.Reaper.RunStates reads: KAS keeps a run's state in
// sessions/<bucket>/<workflowsDirName>/<workflowId>/<runStateFileName>. If a kiro-cli release moves it, RunStates and
// runParents answer nothing, the fill-in reads every run record as stateless, and the step-session spare fails too.
func TestRunStateLayoutMatchesBundle(t *testing.T) {
	want := reaperStringConst(t, "workflowsDirName")
	stateFile := reaperStringConst(t, "runStateFileName")
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

		state := runStateFileRe(stateFile).FindAllStringSubmatch(b.src, -1)
		if len(state) != 1 {
			t.Fatalf("%s: found %d path builders, want 1", stateFile, len(state))
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
	if m := runStateFileRe("workflow-state.json").FindStringSubmatch(src); m == nil || m[1] != "kv" {
		t.Errorf("runStateFileRe = %v, want the planted builder kv", m)
	}
}

// TestRunStateParentKeyMatchesBundle pins the one member the orphan sweep reads off a run's state: if a release
// renames or nests it, the decode leaves every parent empty with no error and every parentless run's launching
// session is reaped again. The second pattern pins that the probed state is the parsed file, so the member is
// top-level in it.
func TestRunStateParentKeyMatchesBundle(t *testing.T) {
	want := reaperStringConst(t, "stateParentKey")
	forEachBundle(t, func(t *testing.T, b bundle) {
		parent := runStateParentRe.FindAllStringSubmatch(b.src, -1)
		if len(parent) != 1 {
			t.Fatalf("probeParentSessionIdFromDisk: found %d definitions reading a member off the probed state, want 1", len(parent))
		}
		if parent[0][1] != want {
			t.Errorf("bundle reads the run's parent as state.%s, reaper stateParentKey = %q", parent[0][1], want)
		}
		if n := len(runStateReadRe.FindAllString(b.src, -1)); n != 1 {
			t.Errorf("readStateFile: found %d definitions returning the schema-parsed file, want 1", n)
		}
	})
}

func TestRunStateParentKeyExtractors(t *testing.T) {
	const src = `class W{async probeParentSessionIdFromDisk(e,n){return(await this.probeWorkflowStateFromDisk(e,n))?.state.parentSessionId}}` +
		`async function r7o(t,e){let n;try{n=await e.readFile(t)}catch(i){if(Tp(i))return null;throw i}let r=JSON.parse(n);return DK.parse(r)}a(r7o,"readStateFile");`
	if m := runStateParentRe.FindStringSubmatch(src); m == nil || m[1] != "parentSessionId" {
		t.Errorf("runStateParentRe = %v, want the planted member parentSessionId", m)
	}
	if n := len(runStateReadRe.FindAllString(src, -1)); n != 1 {
		t.Errorf("runStateReadRe matched %d times, want 1", n)
	}
}
