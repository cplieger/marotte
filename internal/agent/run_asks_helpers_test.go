package agent

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/keyenc"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

const stepNodeType = "step"

// askStepNode is one step node of a test inspect reply.
type askStepNode struct {
	id, status, session, signal string
	awaiting                    bool
}

// parkedStep is a step waiting on a person, as KAS reports one after send_message warning.
func parkedStep(id, session string) askStepNode {
	return askStepNode{id: id, status: "paused", session: session, signal: "need_input", awaiting: true}
}

// inspectAsk is an inspect reply of workflowID: one step under a sequence root, several under a
// parallel one.
func inspectAsk(t *testing.T, workflowID string, run marotte.RunStatus, steps ...askStepNode) json.RawMessage {
	t.Helper()
	kids := make([]any, 0, len(steps))
	rootStatus := string(run)
	for _, s := range steps {
		n := map[string]any{"nodeId": s.id, "type": stepNodeType, "status": s.status, "agentName": "asker"}
		if s.session != "" {
			n["sessionId"] = s.session
		}
		if s.signal != "" {
			n["completionSignal"] = s.signal
		}
		if s.awaiting {
			n["awaitingUserInput"] = true
		}
		kids = append(kids, n)
	}
	rootType := "sequence"
	if len(steps) > 1 {
		rootType = "parallel"
	}
	raw, err := json.Marshal(map[string]any{
		"workflowId": workflowID,
		"state": map[string]any{
			"workflowId": workflowID, "status": run,
			"root": map[string]any{"nodeId": "root", "type": rootType, "status": rootStatus, "children": kids},
		},
	})
	if err != nil {
		t.Fatalf("Setup: marshalling the inspect reply: %s", err)
	}
	return raw
}

// markerlessAskID is the id of an ask whose step marotte saw no turn_start for this boot.
func markerlessAskID(h *Runtime, workflowID, nodePath, session string) string {
	return keyenc.Join(keyenc.Join(workflowID, nodePath, session), keyenc.Join(h.Epoch(), "0"))
}

// leaseRun gives a run the lease its cell lives by.
func leaseRun(t *testing.T, h *Runtime, l runlease.Lease) {
	t.Helper()
	if l.Origin == "" {
		l.Origin = runlease.OriginManual
	}
	if err := h.runs.leaseStore().Put(t.Context(), &l); err != nil {
		t.Fatalf("Setup: lease: %v", err)
	}
}

// runAsksFrames is every run_asks frame the hub published for workflowID, oldest first.
func runAsksFrames(t *testing.T, h *Runtime, workflowID string) []marotte.RunAsksPayload {
	t.Helper()
	var out []marotte.RunAsksPayload
	for _, e := range bufferedEvents(h) {
		if e.Type != string(marotte.EventRunAsks) {
			continue
		}
		var p marotte.RunAsksPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("a run_asks payload did not decode: %v", err)
		}
		if p.WorkflowID == workflowID {
			out = append(out, p)
		}
	}
	return out
}

// lastAsksOf is the cell's current snapshot, the one every reconnect replays.
func lastAsksOf(t *testing.T, h *Runtime, workflowID string) marotte.RunAsksPayload {
	t.Helper()
	c := h.runs.cellFor(workflowID)
	if c == nil {
		t.Fatalf("no cell for %s: is it leased?", workflowID)
	}
	h.runs.asks.mu.Lock()
	defer h.runs.asks.mu.Unlock()
	return c.last
}

// readNow is an explicit read of a leased run through its own carrier.
func readNow(t *testing.T, h *Runtime, workflowID string) askRead {
	t.Helper()
	c := h.runs.cellFor(workflowID)
	if c == nil {
		t.Fatalf("no cell for %s: is it leased?", workflowID)
	}
	host := h.runs.askHost(workflowID)
	if host == nil {
		t.Fatalf("no host for %s", workflowID)
	}
	r, err := h.runs.readAsks(t.Context(), c, host)
	if err != nil {
		t.Fatalf("readAsks: %v", err)
	}
	return r
}

func eventsOfType(h *Runtime, typ marotte.EventType) []bufferedEvent {
	var out []bufferedEvent
	for _, e := range bufferedEvents(h) {
		if e.Type == string(typ) {
			out = append(out, e)
		}
	}
	return out
}
