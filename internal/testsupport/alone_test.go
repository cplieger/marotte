package testsupport

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/testsupport/testchild"
)

const (
	aloneFixtureMode = "MAROTTE_ALONE_FIXTURE"
	aloneFixtureOut  = "MAROTTE_ALONE_FIXTURE_OUT"
)

// TestAloneFixture is TestAlone_ReportsTheChildsVerdict's subject and skips when run on its own. Its body writes the
// pid it ran in, and each subtest that ran appends its name.
func TestAloneFixture(t *testing.T) {
	mode := os.Getenv(aloneFixtureMode)
	if mode == "" {
		t.Skip("runs only under TestAlone_ReportsTheChildsVerdict")
	}
	// The fixture's own child skips, as a child whose pattern matched nothing would run nothing.
	if mode == "skip" && os.Getenv(aloneEnv) != "" {
		t.Skip("the child skips")
	}
	if !Alone(t) {
		return
	}
	out := os.Getenv(aloneFixtureOut)
	if err := os.WriteFile(out, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b"} {
		t.Run(name, func(t *testing.T) {
			f, err := os.OpenFile(out, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			if _, err := f.WriteString(" " + name); err != nil {
				t.Fatal(err)
			}
		})
	}
	if mode == "fail" {
		t.Error("the child fails")
	}
}

// TestAloneFixtureSerial follows TestAloneFixture as a serial test of the fixture's parent process and waits for the
// child to have run, which only a child started before the parallel phase can have done.
func TestAloneFixtureSerial(t *testing.T) {
	if os.Getenv(aloneFixtureMode) != "early" || os.Getenv(aloneEnv) != "" {
		t.Skip("runs only under TestAlone_ReportsTheChildsVerdict")
	}
	for range 200 {
		if ran, err := os.ReadFile(os.Getenv(aloneFixtureOut)); err == nil && bytes.HasSuffix(ran, []byte(" b")) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the child had not run 10s into the serial tests after TestAloneFixture, want it started at once")
}

func TestAlone_ReportsTheChildsVerdict(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     string
		run      string
		wantSubs string
		wantPass bool
	}{
		{name: "a pass", mode: "pass", run: "^TestAloneFixture$", wantPass: true, wantSubs: " a b"},
		{name: "a subtest filter", mode: "pass", run: "^TestAloneFixture$/^b$", wantPass: true, wantSubs: " b"},
		{name: "a subtest filter in an alternation", mode: "pass", run: "^TestAloneFixture$/^b$|^TestNone$", wantPass: true, wantSubs: " b"},
		{name: "a failure", mode: "fail", run: "^TestAloneFixture$", wantSubs: " a b"},
		{name: "a child that runs nothing", mode: "skip", run: "^TestAloneFixture$"},
		{name: "a child beside the serial tests", mode: "early", run: "^TestAloneFixture(Serial)?$", wantPass: true, wantSubs: " a b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "ran")
			fixture := exec.Command(os.Args[0], "-test.run="+tc.run, "-test.v=true")
			fixture.Env = testchild.Env(aloneFixtureMode+"="+tc.mode, aloneFixtureOut+"="+out)
			got, err := fixture.CombinedOutput()
			if passed := err == nil; passed != tc.wantPass {
				t.Fatalf("the fixture passed %v, want %v: %v\n%s", passed, tc.wantPass, err, got)
			}
			if !tc.wantPass && !bytes.Contains(got, []byte("TestAloneFixture in its own process")) {
				t.Errorf("the fixture's failure does not carry the child's output:\n%s", got)
			}
			if tc.mode == "skip" {
				return
			}
			ran, err := os.ReadFile(out)
			if err != nil {
				t.Fatalf("the body never ran: %v\n%s", err, got)
			}
			pid, subs, _ := bytes.Cut(ran, []byte(" "))
			if string(pid) == strconv.Itoa(fixture.Process.Pid) {
				t.Errorf("the body ran in the test's own process %s, want a child of it", pid)
			}
			if got := " " + string(subs); got != tc.wantSubs {
				t.Errorf("the child ran the subtests %q, want %q", got, tc.wantSubs)
			}
		})
	}
}

// The oracle is testing's splitRegexp and matcher (src/testing/match.go): alternatives split at a top-level '|',
// elements at a top-level '/', and an alternative applies to a test when its first element matches the test's name.
func TestChildRun_RunsWhatTheParentsFilterRunsUnderTheTest(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  string
		want string
	}{
		{"no subtest element", "^TestX$", "^TestX$"},
		{"one subtest element", "^TestX$/^row$", "^TestX$/^row$"},
		{"two subtest elements", "TestX/row/cell", "^TestX$/row/cell"},
		{"a slash in brackets", "Test[X/]/row", "^TestX$/row"},
		{"a slash and an alternation in parentheses", "Test(X|a/b)/row", "^TestX$/row"},
		{"an escaped slash", `TestX\/?/row`, "^TestX$/row"},
		{"an unmatched bracket", "TestX]?/row", "^TestX$/row"},
		{"an alternation naming another test", "TestX/a|TestY/b", "^TestX$/a"},
		{"an alternation after the slash", "TestX/a|b", "^TestX$/a"},
		{"two alternatives naming the test", "TestX/a/c|TestX/b", "^TestX$/a/c|^TestX$/b"},
		{"an alternative with no subtest element", "TestX/a|TestX", "^TestX$"},
		{"an alternation before the slash", "TestX|TestY/b", "^TestX$"},
		{"an alternation in parentheses", "TestX/(a|b)", "^TestX$/(a|b)"},
		{"empty", "", "^TestX$"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := childRun("TestX", tc.run); got != tc.want {
				t.Errorf("childRun(%q, %q) = %q, want %q", "TestX", tc.run, got, tc.want)
			}
		})
	}
}
