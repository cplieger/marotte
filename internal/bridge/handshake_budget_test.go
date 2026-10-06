package bridge

// The startup handshake's deadline. Without it an unanswered initialize or session/new blocked Start forever, with
// every Send answering 409 and no error. Expiry is driven through the shipped budgets, shortened, because a short
// parent context would pass with the budget deleted; so these must not run in parallel with any bridge start. Not
// synctest: live subprocess pipes stop the fake clock. Deleting the timer makes them hang to the test timeout, the
// only failure an unbounded wait allows.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// stallingFake writes a fake kiro-cli that answers every request except method, which it reads and ignores.
func stallingFake(t *testing.T, dir, stallOn string) string {
	t.Helper()
	script := `#!/bin/sh
while IFS= read -r line; do
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(echo "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  if [ "$method" = "` + stallOn + `" ]; then
    continue
  fi
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"serverInfo":{"name":"fake"}}}\n' "$id"
      ;;
    session/new)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"sess-stall","configOptions":[{"id":"model","currentValue":"engine-default"}]}}\n' "$id"
      ;;
    session/load)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"sess-stall"}}\n' "$id"
      ;;
    *)
      if [ -n "$id" ]; then
        printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"
      fi
      ;;
  esac
done
`
	scriptPath := filepath.Join(dir, "stalling-kiro-cli")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}
	return scriptPath
}

// budgetProbe is the handshake's budget in tests: long enough for the fake to answer the requests before the stall.
const budgetProbe = 750 * time.Millisecond

// shortenBudgets sets both shipped budgets to budgetProbe for one test, restored via t.Cleanup.
func shortenBudgets(t *testing.T) {
	t.Helper()
	origHandshake, origReplay := handshakeBudget, replayBudget
	handshakeBudget, replayBudget = budgetProbe, budgetProbe
	t.Cleanup(func() { handshakeBudget, replayBudget = origHandshake, origReplay })
}

// TestStart_ExpiresOnAnUnansweredHandshake runs once per phase so the message names it. kiro-cli stays alive and
// never answers; dead bridges are b.done's case.
func TestStart_ExpiresOnAnUnansweredHandshake(t *testing.T) {
	cases := map[string]struct {
		stallOn   string
		sessionID string
		wantPhase string
	}{
		"initialize never answers": {
			stallOn:   "initialize",
			wantPhase: "session start",
		},
		"session/new never answers": {
			stallOn:   "session/new",
			wantPhase: "session start",
		},
		// A resume gets replayBudget, so the message must name the phase.
		"session/load never answers": {
			stallOn:   "session/load",
			sessionID: "01HXYZ0000000000000000000A",
			wantPhase: "session resume",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			shortenBudgets(t)
			dir := t.TempDir()
			scriptPath := stallingFake(t, dir, tc.stallOn)

			b := New(scriptPath, dir)
			t.Cleanup(b.Stop)

			start := time.Now()
			// A parent with no deadline, so the budget is what fires.
			err := b.Start(context.Background(), &marotte.StartOpts{
				Lifetime: context.Background(), SessionID: tc.sessionID,
			})
			elapsed := time.Since(start)

			if err == nil {
				t.Fatal("Start returned nil on a handshake that was never answered")
			}
			// The classification survives the wrap.
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("Start error = %v, want it to wrap context.DeadlineExceeded", err)
			}
			if elapsed > 10*time.Second {
				t.Errorf("Start took %v to give up, want about %v", elapsed, budgetProbe)
			}
			// Each is something the raw "context deadline exceeded" does not say.
			for _, want := range []string{tc.wantPhase, "Send again", "/api/health"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Start error %q does not mention %q", err, want)
				}
			}
		})
	}
}

// TestStart_ReapsTheSubprocessOnExpiry pins that a start that gives up must not leak the kiro-cli tree. Asserted through
// NotifCh: Stop's cmd.Wait leaves no process entry to probe.
func TestStart_ReapsTheSubprocessOnExpiry(t *testing.T) {
	shortenBudgets(t)
	dir := t.TempDir()
	scriptPath := stallingFake(t, dir, "initialize")

	b := New(scriptPath, dir)
	t.Cleanup(b.Stop)

	if err := b.Start(context.Background(), &marotte.StartOpts{Lifetime: context.Background()}); err == nil {
		t.Fatal("Start returned nil on a handshake that was never answered")
	}

	select {
	case <-b.NotifCh():
	case <-time.After(5 * time.Second):
		t.Fatal("an expired handshake left the bridge up; Stop did not run")
	}
}

// TestStart_FailsClosedWhenTheBudgetExpiresInsideTheAppliers pins that the appliers are best-effort, so an expiry would leave
// the wrong model or autopilot for a supervised chat while newSession returns nil. The fake stalls on the model
// applier's config call.
func TestStart_FailsClosedWhenTheBudgetExpiresInsideTheAppliers(t *testing.T) {
	shortenBudgets(t)
	dir := t.TempDir()
	scriptPath := stallingFake(t, dir, "session/set_config_option")

	b := New(scriptPath, dir)
	t.Cleanup(b.Stop)

	// A different model makes applyInitialModel call and stall; Supervised carries the applier that matters.
	err := b.Start(context.Background(), &marotte.StartOpts{
		Lifetime: context.Background(), Model: "claude-opus-5", Supervised: true,
	})

	if err == nil {
		t.Fatal("Start succeeded with a session whose appliers never landed; " +
			"a supervised chat would be running in autopilot with only a log line to say so")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Start error = %v, want it to wrap context.DeadlineExceeded", err)
	}
}

// TestHandshakeBudgets_ResumeIsNotTighterThanStart pins that a resume streams the whole transcript in its window.
func TestHandshakeBudgets_ResumeIsNotTighterThanStart(t *testing.T) {
	if replayBudget <= handshakeBudget {
		t.Errorf("replayBudget (%v) must exceed handshakeBudget (%v): a resume replays the "+
			"whole transcript inside its budget, which a fresh session never pays for",
			replayBudget, handshakeBudget)
	}
}

// TestHandshakeTimeout_LeavesOtherFailuresAlone pins that only an expiry is wrapped; other errors already name their cause.
func TestHandshakeTimeout_LeavesOtherFailuresAlone(t *testing.T) {
	for name, err := range map[string]error{
		"nil":            nil,
		"plain":          errors.New("session/new: Invalid params"),
		"cancelled":      context.Canceled,
		"bridge exited":  marotte.ErrBridgeExited,
		"wrapped cancel": errors.New("session/load: " + context.Canceled.Error()),
	} {
		t.Run(name, func(t *testing.T) {
			got := handshakeTimeout(err, "session start", handshakeBudget)
			if got != err {
				t.Errorf("handshakeTimeout(%v) = %v, want the error returned unchanged", err, got)
			}
		})
	}
}
