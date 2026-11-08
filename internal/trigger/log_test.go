package trigger

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/marotte/internal/launchrule"
)

// canary marks every piece of forge text the test's items and runs carry.
const canary = "canary"

func canaryIssue(number int, labels ...string) forgeapi.Issue {
	it := issue(number, canary+"-author", labels...)
	it.Title, it.Body = canary+" title", canary+" body"
	return it
}

func canaryPull(number int, labels ...string) forgeapi.PullRequest {
	p := pull(number, canary+"-author", "main", labels...)
	p.Title, p.Body, p.SourceBranch = canary+" title", canary+" body", canary+"-branch"
	return p
}

// No trigger line carries a forge's text: polls that match an issue, a pull request and a run whose titles, bodies,
// branches and authors are canaries, the launches they start, a confirmation and the bound's abandons log only the
// rules' own fields and ids.
func TestLogLinesCarryNoForgeText(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		issues := tc.createRule(fifo(labeledSpec(), 10))
		prSpec := fifo(labeledSpec(), 10)
		prSpec.Kind = launchrule.KindPRLabeled
		prs := tc.createRule(prSpec)
		runs := tc.createRule(fifo(runSpec("success"), 10))
		src := &fakeSource{}
		src.issues = []forgeapi.Page[forgeapi.Issue]{
			{Items: []forgeapi.Issue{canaryIssue(7)}}, {Items: []forgeapi.Issue{canaryIssue(7, testLabel)}},
		}
		src.prs = []forgeapi.Page[forgeapi.PullRequest]{
			{Items: []forgeapi.PullRequest{canaryPull(8)}}, {Items: []forgeapi.PullRequest{canaryPull(8, testLabel)}},
		}
		r := run(41, forgeapi.CheckPassing, time.Now().Add(time.Minute))
		r.Name = canary + " run"
		src.runs = []forgeapi.Page[forgeapi.Run]{{}, {Items: []forgeapi.Run{r}}}
		for range 2 {
			for _, rule := range []launchrule.Rule{issues, prs, runs} {
				beginPoll(t, tc.Confirmer, rule.ID, src)()
			}
			advance(2 * time.Minute)
		}
		f := tc.startLauncher(nil)
		tc.letLaunch(nil)
		advance(time.Second)
		calls := f.taken()
		if len(calls) != 3 {
			t.Fatalf("%d launches, want one per rule: %+v", len(calls), calls)
		}
		tc.ConfirmEvent(calls[0].ruleID, calls[0].key, "wf_1")
		advance(launchConfirmBound + time.Second)

		for _, msg := range []string{logMsgEventMatched, logMsgRunStarted, logMsgLaunchAbandoned} {
			if tc.log.CountExact(msg) == 0 {
				t.Errorf("no %q line, want the poll, the launch and the abandon logged", msg)
			}
		}
		for _, rec := range tc.log.Records() {
			text := rec.Message
			rec.Attrs(func(a slog.Attr) bool {
				text += fmt.Sprintf(" %s=%v", a.Key, a.Value.Any())
				return true
			})
			if strings.Contains(strings.ToLower(text), canary) {
				t.Errorf("a line carries forge text: %s", text)
			}
		}
	})
}

// drain takes the queue's head off as a confirmed launch does, in place, so the rule's in-memory state is kept.
func (h *harness) drain(ruleID string) {
	h.t.Helper()
	h.c.update(ruleID, anyGeneration, func(es *eventState) bool {
		es.queue = es.queue[1:]
		return true
	})
}

func TestQueueFullLoggedOnce(t *testing.T) {
	logs := captureLogs(t)
	h := newHarness(t)
	r := h.create(fifo(labeledSpec(), 2))
	labeled := func(numbers ...int) []forgeapi.Issue {
		out := make([]forgeapi.Issue, 0, len(numbers))
		for _, n := range numbers {
			out = append(out, issue(n, "a", testLabel))
		}
		return out
	}
	full := func() int { return strings.Count(logs.String(), `msg="trigger queue full"`) }
	h.issuePage(issue(1, "a"), issue(2, "a"), issue(3, "a"))
	h.poll()

	h.issuePage(labeled(1, 2, 3)...)
	h.poll()
	if n := full(); n != 1 {
		t.Fatalf("%d queue full lines after the first full poll, want 1", n)
	}
	h.issuePage(labeled(1, 2, 3)...)
	h.poll()
	if n := full(); n != 1 {
		t.Errorf("%d queue full lines after a second full poll with no room between, want still 1", n)
	}
	h.drain(r.ID)
	h.issuePage(labeled(1, 2, 3)...)
	h.poll()
	if n := full(); n != 1 {
		t.Errorf("%d queue full lines after a poll that queued #3 into the room, want still 1", n)
	}
	h.drain(r.ID)
	h.drain(r.ID)
	h.issuePage(labeled(1, 2, 3, 4, 5, 6)...)
	h.poll()
	if n := full(); n != 2 {
		t.Errorf("%d queue full lines after the queue had room and overflowed again, want 2", n)
	}
	h.issuePage(labeled(1, 2, 3, 4, 5, 6)...)
	h.poll()
	if n := full(); n != 2 {
		t.Errorf("%d queue full lines after another full poll with no room, want still 2", n)
	}
}

func TestRetryableLoggedOnReasonChange(t *testing.T) {
	retryable := func(tc *testConfirmer) []string {
		var out []string
		for _, rec := range tc.log.Records() {
			class, cause := "", ""
			rec.Attrs(func(a slog.Attr) bool {
				switch a.Key {
				case "class":
					class = a.Value.String()
				case "error":
					cause = a.Value.String()
				}
				return true
			})
			if rec.Message == logMsgRunNotStarted && class == "retryable" {
				out = append(out, cause)
			}
		}
		return out
	}
	t.Run("before workflow/new", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			tc.enqueue(r.ID, held("k1", "c1"))
			fail := func(err error) { tc.deliver(r.ID, &attemptEvent{kind: evLaunchEnded, key: "k1", err: err}) }
			fail(errEngineSlow)
			fail(errEngineSlow)
			fail(errEngineSlow)
			if got := retryable(tc); len(got) != 1 {
				t.Errorf("retryable lines %q after three engine-busy failures, want 1", got)
			}
			fail(errSignIn)
			fail(errSignIn)
			fail(errEngineSlow)
			if got := retryable(tc); len(got) != 3 {
				t.Errorf("retryable lines %q, want one more per change of reason", got)
			}
		})
	})
	t.Run("an invoke refused in band", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			tc.enqueue(r.ID, held("k1", "c1"))
			for _, wf := range []string{"wf_1", "wf_2"} {
				tc.name(r.ID, "k1", wf)
				tc.LaunchReturned(r.ID, wf, justClaimed)
			}
			if got := retryable(tc); len(got) != 1 {
				t.Errorf("retryable lines %q after two refusals for the same reason, want 1", got)
			}
			tc.name(r.ID, "k1", "wf_3")
			tc.ConfirmEvent(r.ID, "k1", "wf_3")
			tc.enqueue(r.ID, held("k2", "c2"))
			tc.name(r.ID, "k2", "wf_4")
			tc.LaunchReturned(r.ID, "wf_4", justClaimed)
			if got := retryable(tc); len(got) != 2 {
				t.Errorf("retryable lines %q, want the reason logged again after a confirmed launch", got)
			}
		})
	})
}

// A feed's reads that fail three polls in a row warn once, with the error's kind; the next good poll says so. A
// failure the budget answers for, a rate limit or a back-off, is not a failed read.
func TestPollFailingLoggedAfterThreePolls(t *testing.T) {
	logs := captureLogs(t)
	h := newHarness(t)
	h.create(labeledSpec())
	h.create(fifo(labeledSpec(), 5))
	h.issuePage()
	h.poll()
	count := func(msg string) int { return strings.Count(logs.String(), `msg="`+msg+`"`) }

	h.src.errs = map[string]error{listIssues: &forgeapi.Error{Kind: forgeapi.KindTransient, Status: 502}}
	h.poll()
	h.poll()
	if n := count("trigger poll failing"); n != 0 {
		t.Errorf("%d poll failing lines after two failed polls, want none", n)
	}
	if n := count("trigger poll read failed"); n != 2 {
		t.Errorf("%d read failed lines after two failed polls of a feed two rules share, want one per poll", n)
	}
	h.poll()
	h.poll()
	if n := count("trigger poll failing"); n != 1 {
		t.Fatalf("%d poll failing lines after four failed polls, want 1: %s", n, logs)
	}
	if !strings.Contains(logs.String(), "error_kind=transient") {
		t.Errorf("logs = %s, want error_kind=transient on the poll failing line", logs)
	}
	h.src.errs = nil
	h.poll()
	if n := count("trigger poll recovered"); n != 1 {
		t.Errorf("%d poll recovered lines after a good poll, want 1", n)
	}
	h.poll()
	if n := count("trigger poll recovered"); n != 1 {
		t.Errorf("%d poll recovered lines after a second good poll, want still 1", n)
	}

	h.src.errs = map[string]error{listIssues: &forgeapi.Error{Kind: forgeapi.KindRateLimited, Status: 429}}
	for range 4 {
		h.poll()
	}
	if n := count("trigger poll failing"); n != 1 {
		t.Errorf("%d poll failing lines after rate-limited polls, want still 1: a rate limit backs off", n)
	}
	if n := count("trigger poll read failed"); n != 3 {
		t.Errorf("%d read failed lines, want none for a rate limit", n)
	}
}
