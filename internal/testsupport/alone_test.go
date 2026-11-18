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

// TestAloneFixture is TestAlone_ReportsTheChildsVerdict's subject and skips when run on its own.
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
	if err := os.WriteFile(os.Getenv(aloneFixtureOut), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if mode == "fail" {
		t.Error("the child fails")
	}
}

func TestAlone_ReportsTheChildsVerdict(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		wantPass bool
	}{{"pass", true}, {"fail", false}, {"skip", false}} {
		t.Run(tc.mode, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "pid")
			fixture := exec.Command(os.Args[0], "-test.run=^TestAloneFixture$", "-test.v=true")
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
			pid, err := os.ReadFile(out)
			if err != nil {
				t.Fatalf("the body never ran: %v\n%s", err, got)
			}
			if string(pid) == strconv.Itoa(fixture.Process.Pid) {
				t.Errorf("the body ran in the test's own process %s, want a child of it", pid)
			}
		})
	}
}
