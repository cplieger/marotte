package testsupport

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

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
		{name: "a failure", mode: "fail", run: "^TestAloneFixture$", wantSubs: " a b"},
		{name: "a child that runs nothing", mode: "skip", run: "^TestAloneFixture$"},
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

func TestSubtestPattern_SplitsAsTheTestingPackageDoes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		run    string
		want   string
		wantOK bool
	}{
		{"no subtest element", "^TestX$", "", false},
		{"one subtest element", "^TestX$/^row$", "^row$", true},
		{"two subtest elements", "TestX/row/cell", "row/cell", true},
		{"a slash in brackets", "Test[/]X/row", "row", true},
		{"a slash in parentheses", "Test(a/b)/row", "row", true},
		{"an escaped slash", `Test\/X/row`, "row", true},
		{"an unmatched bracket", "Test]X/row", "row", true},
		{"an alternation after the slash", "TestX/a|TestY/b", "", false},
		{"an alternation before the slash", "TestX|TestY/b", "", false},
		{"an alternation in parentheses", "TestX/(a|b)", "(a|b)", true},
		{"empty", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := subtestPattern(tc.run); got != tc.want || ok != tc.wantOK {
				t.Errorf("subtestPattern(%q) = %q, %v; want %q, %v", tc.run, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}
