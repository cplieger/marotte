package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// A cancel whose answer could not be read took if a fresh read finds the run ended: the stop is recorded and nothing
// re-attempts it. A run still running after it is re-attempted, as a refused cancel is.
func TestFinishTermination_AnUnreadCancelIsSettledByAFreshRead(t *testing.T) {
	cases := map[string]struct {
		status     marotte.RunStatus
		wantErr    bool
		wantReason string
	}{
		"the run reads ended":       {marotte.RunStatusAborted, false, runEndOverran},
		"the run reads still going": {marotte.RunStatusRunning, true, ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h, br := heldRun(t, "wf_1", "publish")
			h.runs.cancelRetryBase = time.Hour
			h.runs.armDeadline(t.Context(), "wf_1")
			br.mu.Lock()
			br.callResults[methodKiroWorkflowInspect] = inspectReply(t, "wf_1", c.status, "")
			br.callResults[methodKiroWorkflowList] = json.RawMessage(`{"runs":[]}`)
			br.mu.Unlock()
			br.setCallErr(methodKiroWorkflowCancel, undecodedReply())
			if !h.runs.claimTermination("wf_1") {
				t.Fatal("the termination claim was not free")
			}

			err := h.runs.finishTermination(t.Context(), "wf_1", runEndOverran, runStop{},
				func(context.Context) error { return nil })

			if (err != nil) != c.wantErr {
				t.Errorf("finishTermination = %v, want an error %v", err, c.wantErr)
			}
			if got := h.runs.endReason("wf_1"); got != c.wantReason {
				t.Errorf("the recorded end = %q, want %q", got, c.wantReason)
			}
			h.runs.mu.Lock()
			retries := h.runs.bounds.cancelRetries["wf_1"]
			h.runs.mu.Unlock()
			if want := map[bool]int{true: 1, false: 0}[c.wantErr]; retries != want {
				t.Errorf("cancel re-attempts scheduled = %d, want %d", retries, want)
			}
			if c.wantErr != h.runs.bounded("wf_1") {
				t.Errorf("bounded = %v, want %v", h.runs.bounded("wf_1"), c.wantErr)
			}
		})
	}
}
