package trigger

import (
	"errors"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/launchrule"
)

func TestViewShowsUnwrittenState(t *testing.T) {
	t.Run("a busy recipe's row text", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			tc.busy(r.ID, "wf_blocker")
			st, err := tc.View(r.ID)
			if err != nil || st.Status.RowText != "waiting for run wf_blocker to finish" {
				t.Errorf("View = row text %q, %v; want the busy row's text", st.Status.RowText, err)
			}
			if n := tc.written(); n != 0 {
				t.Errorf("%d writes landed, want none: the view reads memory", n)
			}
		})
	})
	t.Run("a write-failure block over the row's own", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			tc.enqueue(r.ID, held("k1", "c1"))
			tc.setStatus(r.ID, func(st *launchrule.Status) { st.Blocked = "run wf_1 did not report its start twice" })
			tc.failWrites(errors.New("disk full"))
			tc.persist(r.ID)
			st, err := tc.View(r.ID)
			want := "could not write launch-state/" + r.ID + ".json: disk full"
			if err != nil || st.Status.Blocked != want {
				t.Errorf("View = blocked %q, %v; want %q", st.Status.Blocked, err, want)
			}
			if st.Held() != 1 {
				t.Errorf("View holds %d events, want the one queued in memory", st.Held())
			}
		})
	})
}

func TestViewRefusesWhatItCannotShow(t *testing.T) {
	t.Run("an unreadable state file", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(openedSpec("alice"))
			writeStateBytes(tc, r.ID, `{"cursor":"x","status":{}}`)
			tc.restart()
			_, err := tc.View(r.ID)
			ue, ok := errors.AsType[*launchrule.UnreadableStateError](err)
			if !ok || ue.RuleID != r.ID || !strings.HasPrefix(err.Error(), "could not read launch-state/"+r.ID+".json: cursor") {
				t.Errorf("View = %v, want the rule's *UnreadableStateError", err)
			}
		})
	})
	t.Run("a refused definition", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			tc.persist(r.ID)
			refuseOnDisk(t, tc, r.ID)
			tc.restart()
			want := tc.store.Refused(r.ID)
			if _, err := tc.View(r.ID); want == nil || !errors.Is(err, want) {
				t.Errorf("View = %v, want the store's refusal %v", err, want)
			}
		})
	})
	t.Run("an unknown id or a clock rule", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			clock, err := tc.store.Create(t.Context(), &launchrule.Rule{Source: "bundled://c", When: launchrule.When{
				Clock: &launchrule.Spec{Freq: launchrule.FreqDaily, Hour: 2},
			}})
			if err != nil {
				t.Fatalf("Setup: Create: %v", err)
			}
			for _, id := range []string{"missing", clock.ID} {
				if _, err := tc.View(id); !errors.Is(err, launchrule.ErrNotFound) {
					t.Errorf("View(%s) = %v, want ErrNotFound", id, err)
				}
			}
		})
	})
}
