package trigger

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/cplieger/forgeapi/v2"
)

func TestCursorKeyedByRepoID(t *testing.T) {
	h := newHarness(t)
	r := h.create(labeledSpec())
	h.issuePage(issue(12, "alice"))
	h.poll()
	st, err := h.store.State(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(st.Cursor, &top); err != nil {
		t.Fatalf("the state file's cursor %s is not an object: %v", st.Cursor, err)
	}
	want := forgeapi.RepoRef{Selector: testRepo}.Encode()
	if got := slices.Collect(maps.Keys(top)); !eq(got, []string{want}) {
		t.Errorf("cursor keys = %v, want the one repository id %q", got, want)
	}
}

func TestStateRoundTripsThroughTheStore(t *testing.T) {
	h := newHarness(t)
	r := h.create(openedSpec("alice"))
	h.issuePage(issue(10, "alice"))
	h.issuePage(issue(10, "alice"), issue(11, "alice"))
	h.poll()
	h.poll()
	before := h.state(r.ID)
	h.reopen()
	after := h.state(r.ID)
	if !eq(keysOf(after.queue), keysOf(before.queue)) || after.cursor[Feed{Selector: testRepo}.RepoID()].Opened.PendingFloor != 11 {
		t.Errorf("after a reload: queue %v, cursor %+v; want %v and pending_floor 11",
			keysOf(after.queue), after.cursor, keysOf(before.queue))
	}
}

func TestFailedRunReadKeepsLastComplete(t *testing.T) {
	h := newHarness(t)
	r := h.create(runSpec("success"))
	h.runPage()
	h.poll()
	first := h.state(r.ID).cursor[Feed{Selector: testRepo}.RepoID()].Runs.LastComplete
	h.src.errs = map[string]error{"runs": errFake}
	h.poll()
	if got := h.cursorOf(r.ID).Runs.LastComplete; !got.Equal(first) {
		t.Errorf("a failed read moved last_complete to %v, want %v kept", got, first)
	}
}
