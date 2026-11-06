package trigger

import (
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/go/keyenc"
)

func TestCoalesceKeyStoredOnQueueEntry(t *testing.T) {
	h, id := baselined(t, "success")
	h.runPage(run(41, forgeapi.CheckPassing, pollStart.Add(time.Minute)))
	h.poll()
	q := h.queued(id)
	want := keyenc.Join(forgeapi.RepoRef{Selector: testRepo}.Encode(), testWorkflow, "main")
	if len(q) != 1 || q[0].CoalesceKey != want {
		t.Errorf("queued %+v, want run 41 with coalesce key %q", q, want)
	}

	t.Run("an item kind's coalesce key is its key without the last two parts", func(t *testing.T) {
		h := newHarness(t)
		r := h.create(labeledSpec())
		h.issuePage(issue(12, "alice"))
		h.issuePage(issue(12, "alice", testLabel))
		h.poll()
		h.poll()
		q := h.queued(r.ID)
		if want := keyenc.Join(r.ID, testRepo, "issue", "12"); len(q) != 1 || q[0].CoalesceKey != want {
			t.Errorf("queued %+v, want #12 with coalesce key %q", q, want)
		}
	})
}
