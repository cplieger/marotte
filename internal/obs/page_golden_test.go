package obs

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/testsupport/testchild"
)

// The marotte families' page through one fixed sequence of states, as the shipped alert rules and users' dashboards
// read it. The Go and process families are left out: their values change between scrapes, and the library pins them.
const (
	pageGoldenPath = "testdata/metrics_page.golden"
	pageGoldenCmd  = "UPDATE_GOLDEN=1 go test ./internal/obs/ -run TestMetricsPage_MatchesTheGolden"
	pageGoldenEnv  = "MAROTTE_OBS_GOLDEN_OUT"
)

// TestMetricsPage_MatchesTheGolden runs the sequence in a fresh process, since the series are process-wide and the
// other tests here leave theirs on the page.
func TestMetricsPage_MatchesTheGolden(t *testing.T) {
	out := filepath.Join(t.TempDir(), "page.txt")
	child := exec.Command(os.Args[0], "-test.run=^TestMetricsPage_GoldenChild$")
	child.Env = testchild.Env(pageGoldenEnv + "=" + out)
	if o, err := child.CombinedOutput(); err != nil {
		t.Fatalf("the child test binary: %v\n%s", err, o)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(pageGoldenPath, got, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(pageGoldenPath)
	if err != nil {
		t.Fatalf("%v (write it with %s)", err, pageGoldenCmd)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the metrics page differs from %s (rewrite it with %s); a rule or dashboard reading it may break."+
			"\ngot:\n%s\nwant:\n%s", pageGoldenPath, pageGoldenCmd, got, want)
	}
}

// TestMetricsPage_GoldenChild is TestMetricsPage_MatchesTheGolden's child process and skips when run on its own.
func TestMetricsPage_GoldenChild(t *testing.T) {
	out := os.Getenv(pageGoldenEnv)
	if out == "" {
		t.Skip("runs only as the child of TestMetricsPage_MatchesTheGolden")
	}
	var page strings.Builder
	stage := func(name string) {
		rec := scrape()
		page.WriteString("== " + name + " ==\n")
		page.WriteString("Content-Type: " + rec.Header().Get("Content-Type") + "\n")
		for line := range strings.Lines(rec.Body.String()) {
			name := strings.TrimPrefix(strings.TrimPrefix(line, "# HELP "), "# TYPE ")
			if strings.HasPrefix(name, "marotte_") {
				page.WriteString(line)
			}
		}
	}

	stage("start")

	due := time.Date(2026, time.November, 9, 2, 0, 0, 0, time.UTC)
	SignInBackend("golden-backend")
	SignInSaveFailed("golden-backend", SignInWriteFailed)
	EngineUp(true)
	EngineExited("crash")
	PromptFailed("auth")
	UnattendedRunFailed("scheduled", RunStalled)
	UnattendedApproved("triggered")
	ScheduleSlotNotStarted(SlotEngineBusy)
	EngineMemory(3<<20, 8<<20)
	Schedule("rule-a", 2, due)
	ScheduleBlocked("rule-b", due.Add(-time.Hour))
	TriggerRule("rule-c", &TriggerRuleState{UnnamedRun: true, QueueFull: true, PollFailures: 4, CatchUpGaps: 1})
	stage("recorded")

	EngineMemory(4<<20, 0)
	Schedule("rule-a", 0, time.Time{})
	stage("limit and next slot unknown")

	ScheduleGone("rule-a")
	ScheduleGone("rule-b")
	TriggerRuleGone("rule-c")
	EngineMemoryGone()
	EngineUp(false)
	stage("gone")

	if err := os.WriteFile(out, []byte(page.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}
