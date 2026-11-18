package git

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/testsupport"
)

// statusCosts is what the git status summary costs the server per scan of tests/perf/synth's repositories, 3 at x1
// with 20 changed files each. A change that moves one by design rewrites its entry in the same diff.
var statusCosts = map[string]testsupport.Cost{
	"git status summary x1":  {Allocs: 597, Bytes: 126453},
	"git status summary x10": {Allocs: 5723, Bytes: 1248147},
}

// writeSynthRepos writes fixtures.py's repositories under work: each 40 tracked files, then 20 changes, a quarter
// each modified, modified and staged, untracked and deleted.
func writeSynthRepos(t *testing.T, work string, repos int) {
	t.Helper()
	body := "package internal\n\n" + strings.Repeat("// the server reads each repository once\n", 30)
	for n := range repos {
		dir := filepath.Join(work, fmt.Sprintf("repo-%02d", n))
		if err := os.MkdirAll(filepath.Join(dir, "internal"), 0o755); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		for i := range 40 {
			if err := os.WriteFile(filepath.Join(dir, "internal", fmt.Sprintf("f%03d.go", i)), []byte(body), 0o644); err != nil {
				t.Fatalf("Setup: %v", err)
			}
		}
		runGit(t, dir, "init", "--initial-branch=main", "-q")
		runGit(t, dir, "add", "internal")
		runGit(t, dir, "commit", "-q", "-m", "chore: initial")
		var staged []string
		for i := range 20 {
			tracked := filepath.Join("internal", fmt.Sprintf("f%03d.go", i))
			var err error
			switch i % 4 {
			case 0, 1:
				err = os.WriteFile(filepath.Join(dir, tracked), []byte(body+strings.Repeat("// change\n", 3+i)), 0o644)
				if i%4 == 1 {
					staged = append(staged, tracked)
				}
			case 2:
				err = os.WriteFile(filepath.Join(dir, fmt.Sprintf("new-%03d.ts", i)), fmt.Appendf(nil, "export const x = %d;\n", i), 0o644)
			default:
				err = os.Remove(filepath.Join(dir, "internal", fmt.Sprintf("f%03d.go", 39-i)))
			}
			if err != nil {
				t.Fatalf("Setup: %v", err)
			}
		}
		runGit(t, dir, append([]string{"add"}, staged...)...)
	}
}

func TestStatusCost_StatusSummaryHoldsItsBudget(t *testing.T) {
	testsupport.RequireCostBuild(t)
	skipNoGit(t)
	// exec.Cmd hands each git it starts the whole environment, so the scan would cost what the host exports: a
	// GitHub runner's variables more than doubled its bytes.
	home := testsupport.FixedLengthDir(t)
	testsupport.EmptyEnv(t)
	t.Setenv("PATH", "/usr/local/bin:/usr/bin:/bin")
	t.Setenv("HOME", home)
	for _, scale := range []int{1, 10} {
		op := fmt.Sprintf("git status summary x%d", scale)
		t.Run(op, func(t *testing.T) {
			work := testsupport.FixedLengthDir(t)
			writeSynthRepos(t, work, 3*scale)
			h := newTestHandler(t, work)
			got := testsupport.MeasureCost(11, func() {
				st := h.scanRepos(t.Context(), false, nil)
				if len(st) != 3*scale || len(st[0].Files) != 20 {
					t.Fatalf("the scan read %d repositories, the first with %d changes; want %d with 20", len(st), len(st[0].Files), 3*scale)
				}
			})
			testsupport.HoldCost(t, op, statusCosts[op], got)
		})
	}
}
