package agent

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// stepSteerFrame is the steer_queued frame for steerID the step's dock row went out as.
func stepSteerFrame(t *testing.T, h *Runtime, steerID string) marotte.SteerQueuedPayload {
	t.Helper()
	for _, e := range bufferedEvents(h) {
		if e.Type != string(marotte.EventSteerQueued) {
			continue
		}
		var p marotte.SteerQueuedPayload
		raw, _ := json.Marshal(e.Payload)
		if json.Unmarshal(raw, &p) == nil && p.SteerID == steerID {
			return p
		}
	}
	t.Fatalf("no steer_queued frame for %s", steerID)
	return marotte.SteerQueuedPayload{}
}

// A step message with no Sec-Fetch-Site and no Origin is a program's: its steer row names an API caller, and the
// page's own message stays the user's.
func TestStepMessageRoute_AHeaderlessSteerIsAnAPICallers(t *testing.T) {
	tc := stepRouteCases(t)[0]
	cases := []struct {
		name       string
		headers    map[string]string
		wantCaller bool
	}{
		{name: "a header-less request", wantCaller: true},
		{name: "the page", headers: map[string]string{"Sec-Fetch-Site": "same-origin"}, wantCaller: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, _, mux := stepRouteHub(t, tc)
			raw, _ := json.Marshal(marotte.RunStepMessageRequest{Text: "check the logs", MessageID: "m-1"})
			req := httptest.NewRequest(http.MethodPost, tc.StepURL+"/message", bytes.NewReader(raw))
			for k, v := range c.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("POST %s/message = %d %s, want 200", tc.StepURL, rec.Code, rec.Body.String())
			}
			frame := stepSteerFrame(t, h, "steer-m-1")
			if got := frame.Caller != nil; got != c.wantCaller {
				t.Errorf("steer row caller = %+v, want a caller: %v", frame.Caller, c.wantCaller)
			}
		})
	}
}

// The words that resume a paused step keep their API caller in the step's record.
func TestMessageStep_AnAPICallersResumeIsRecordedAsTheirs(t *testing.T) {
	h, _ := messageHub(t, marotte.RunStatusPaused, marotte.RunNodeStatusPaused)

	if _, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "carry on", "m-1", "", &marotte.Caller{}); err != nil {
		t.Fatalf("MessageStep = %v, want nil", err)
	}
	got := stepSteersIn(t, h)
	if len(got) != 1 || got[0].Caller == nil {
		t.Errorf("run log steers = %+v, want the one entry naming an API caller", got)
	}
}

// A run that ends before KAS reopens the step records the resuming words as their API caller's.
func TestStepMessage_ARunEndingBeforeTheResumedTurnOpensKeepsTheCaller(t *testing.T) {
	h, _ := messageHub(t, marotte.RunStatusPaused, marotte.RunNodeStatusPaused)
	h.coord.runs = h.runs.log
	h.coord.closeHostedRuns(t.Context(), runChatID("wf_1"), marotte.StopReasonInterrupted)
	from := &marotte.Caller{Label: "Agent Mail"}
	if _, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "carry on", "m-14", "", from); err != nil {
		t.Fatalf("MessageStep = %v, want nil", err)
	}

	h.runs.closeRun(t.Context(), "wf_1", string(marotte.RunStatusCancelled))

	got := stepSteersIn(t, h)
	want := []marotte.EntrySteer{{Text: "carry on", Origin: marotte.SteerOriginUser, State: marotte.SteerStateRead, Caller: from}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("run log steers = %+v, want %+v after the step's last turn", got, want)
	}
}
