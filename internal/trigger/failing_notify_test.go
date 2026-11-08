package trigger

import (
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// An event rule's launches notify once as they start failing, and none while they keep failing: the triggered
// counterpart of a schedule's first failed slot.
func TestFailingRuleNotifiesOnce(t *testing.T) {
	t.Run("the first failed attempt notifies, the block after it does not", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			tc.run()
			tc.engineReady()
			tc.enqueue(r.ID, held("k1", "c1"))
			abandon(tc, r.ID, "wf_1")
			want := []string{notConfirmedText("wf_1")}
			if got := tc.fx.notices(); !slices.Equal(got, want) {
				t.Fatalf("notices after the first abandon = %q, want %q", got, want)
			}
			abandon(tc, r.ID, "wf_2")
			if _, st := tc.memory(r.ID); st.Blocked == "" {
				t.Fatalf("status %+v, want the second abandon to block the rule", st)
			}
			if got := tc.fx.notices(); !slices.Equal(got, want) {
				t.Errorf("notices after the block = %q, want the first one only", got)
			}
		})
	})
	t.Run("a confirmed launch ends the streak", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			tc.run()
			tc.engineReady()
			tc.enqueue(r.ID, held("k1", "c1"))
			abandon(tc, r.ID, "wf_1")
			tc.name(r.ID, "k1", "wf_2")
			tc.ConfirmEvent(r.ID, "k1", "wf_2")
			tc.enqueue(r.ID, held("k2", "c2"))
			tc.name(r.ID, "k2", "wf_3")
			tc.LaunchReturned(r.ID, "wf_3", nil)
			advance(launchConfirmBound + launchBackoff[0])
			if got := tc.fx.notices(); len(got) != 2 {
				t.Errorf("notices = %q, want one per streak", got)
			}
		})
	})
	t.Run("a launch blocked at once notifies", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			tc.run()
			tc.engineReady()
			tc.enqueue(r.ID, held("k1", "c1"))
			tc.name(r.ID, "k1", "wf_1")
			tc.LaunchReturned(r.ID, "wf_1", &marotte.RPCError{Code: -32602, Message: "input selection is required"})
			advance(0)
			if got := tc.fx.notices(); !slices.Equal(got, []string{"input selection is required"}) {
				t.Errorf("notices = %q, want the block's sentence once", got)
			}
		})
	})
	t.Run("a retryable failure notifies nothing", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			tc.run()
			tc.engineReady()
			tc.enqueue(r.ID, held("k1", "c1"))
			tc.launchEnded(r.ID, "k1", ErrLaunchRetryable)
			advance(0)
			if got := tc.fx.notices(); len(got) != 0 {
				t.Errorf("notices = %q, want none for a wait the backoff rides out", got)
			}
			if _, st := tc.memory(r.ID); !st.FailingSince.IsZero() {
				t.Errorf("status %+v, want no 24-hour clock", st)
			}
		})
	})
}
