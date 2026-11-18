package command

// An outside caller's merge_tangent is a message into the tangent and its parent: neither a tangent nor a parent the
// user closed takes it, and it reopens neither.

import (
	"net/http"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// callerMergeFixture is the merge fixture behind the real message door, with the tangent and the parent tabs open.
func callerMergeFixture(t *testing.T) (f *mergeFixture, tangentTab, parentTab marotte.TabSubject) {
	t.Helper()
	f = newMergeFixture(t)
	f.roles.door = f.mem
	return f, openChatTab(t, f.mem, "c-tangent", ""), openChatTab(t, f.mem, "c-parent", "")
}

func closeTabOf(t *testing.T, f *mergeFixture, tab marotte.TabSubject) {
	t.Helper()
	if _, _, err := f.mem.closeTab(t.Context(), tab.ID, "op-close-"+tab.Ref, true, nil); err != nil {
		t.Fatalf("close %s: %v", tab.Ref, err)
	}
}

func hasChatTab(f *mergeFixture, ref marotte.ChatID) bool {
	open, _ := f.tabs.List()
	for _, s := range open {
		if s.Kind == marotte.TabKindChat && s.Ref == string(ref) {
			return true
		}
	}
	return false
}

var findingsOfTheCache = &fakeTangents{parent: "c-parent", reply: "The cache halves the load."}

func TestMergeTangent_FromAnAPICallerIntoAClosedChatIsSessionClosed(t *testing.T) {
	for _, closed := range []marotte.ChatID{"c-tangent", "c-parent"} {
		t.Run(string(closed), func(t *testing.T) {
			f, tangentTab, parentTab := callerMergeFixture(t)
			closeTabOf(t, f, map[marotte.ChatID]marotte.TabSubject{"c-tangent": tangentTab, "c-parent": parentTab}[closed])

			_, err := f.mergeFrom(t, findingsOfTheCache, `{"op_id":"op-1"}`, grelotCaller)

			if statusOf(err) != http.StatusGone || reasonOf(err) != reasonSessionClosed {
				t.Errorf("merge = %d %q (%v), want 410 %s", statusOf(err), reasonOf(err), err, reasonSessionClosed)
			}
			if n := f.host.promptCount(); n != 0 {
				t.Errorf("prompts opened = %d, want none", n)
			}
			if _, ok := f.merges.record(t.Context(), tangentOp1); ok {
				t.Error("a merge record was written, want none for a refused merge")
			}
			if hasChatTab(f, closed) {
				t.Errorf("%s has a tab after the merge, want it left closed", closed)
			}
		})
	}
}

// A parent the user closes while the summary runs takes no findings from an outside caller's merge, which fails with
// the findings kept in the tangent, and the parent stays closed.
func TestMergeTangent_FromAnAPICallerFailsWhenTheParentClosesDuringTheSummary(t *testing.T) {
	f, _, parentTab := callerMergeFixture(t)
	f.host.summaryEnded = func() { closeTabOf(t, f, parentTab) }

	if _, err := f.mergeFrom(t, findingsOfTheCache, `{"op_id":"op-1"}`, grelotCaller); err != nil {
		t.Fatalf("CmdMergeTangent = %v, want accepted", err)
	}
	rec := awaitMergeSettled(t, f)

	if rec.State != marotte.TangentMergeFailed || rec.Message != deliveryFailure(mergeParentClosed) {
		t.Errorf("merge = %+v, want failed as the parent closed", rec)
	}
	if n := f.host.promptCount(); n != 1 {
		t.Errorf("prompts opened = %d, want the summary alone", n)
	}
	if rows := f.queue.queued(); len(rows) != 0 {
		t.Errorf("queued rows = %+v, want no findings row in the parent", rows)
	}
	if hasChatTab(f, "c-parent") {
		t.Error("c-parent has a tab after the merge, want it left closed")
	}
}

// A landed merge reopens a closed parent only for the page's merge.
func TestTangentMerges_OnlyThePagesLandedMergeReopensTheParent(t *testing.T) {
	for name, tc := range map[string]struct {
		from   *marotte.Caller
		reopen bool
	}{
		"the page":      {reopen: true},
		"an API caller": {from: grelotCaller},
	} {
		t.Run(name, func(t *testing.T) {
			f, _, parentTab := callerMergeFixture(t)
			closeTabOf(t, f, parentTab)
			if err := f.merges.admit(t.Context(), tangentOp1, "c-parent", "msg-delivery", tc.from); err != nil {
				t.Fatal(err)
			}

			f.merges.conclude(t.Context(), tangentOp1, "c-parent", tc.from, "")

			if got := hasChatTab(f, "c-parent"); got != tc.reopen {
				t.Errorf("c-parent tab open = %v, want %v", got, tc.reopen)
			}
		})
	}
}

// The record keeps who asked, so a merge a later process resumes still answers to an outside caller's rules.
func TestMergeTangent_RecordsAnAPICallersMerge(t *testing.T) {
	f, _, _ := callerMergeFixture(t)
	f.host.summaryEnded = func() {
		rec, ok := f.merges.record(t.Context(), tangentOp1)
		if !ok || rec.From == nil || rec.From.Label != grelotCaller.Label {
			t.Errorf("running record = %+v (found %v), want it to name grelot", rec, ok)
		}
	}

	if _, err := f.mergeFrom(t, findingsOfTheCache, `{"op_id":"op-1"}`, grelotCaller); err != nil {
		t.Fatalf("CmdMergeTangent = %v, want accepted", err)
	}
	if rec := awaitMergeSettled(t, f); rec.State != marotte.TangentMergeSucceeded {
		t.Errorf("merge = %+v, want succeeded into the open parent", rec)
	}
}
