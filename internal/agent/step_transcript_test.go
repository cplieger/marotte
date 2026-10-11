package agent

// The fixture is the measured `inspect` shape: two repeat iterations holding the same step id, which makes path addressing necessary.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

// stepInspect is one run's trimmed inspect reply: two iterations each with step `build`, plus a session-less
// pending step. The tree spells iterations `loop#0`/`loop#1` while paths read `iter-0`/`iter-1`
// (workflow.pathSegment).
const stepInspect = `{
  "workflowId": "wf_1",
  "state": {
    "workflowId": "wf_1",
    "status": "completed",
    "root": {
      "nodeId": "wf_1", "type": "sequence", "status": "completed",
      "children": [
        {"nodeId": "loop", "type": "repeat", "status": "completed", "children": [
          {"nodeId": "loop#0", "type": "sequence", "status": "completed", "iteration": 0, "children": [
            {"nodeId": "build", "type": "step", "status": "completed", "iteration": 0, "sessionId": "sess_pass0"}
          ]},
          {"nodeId": "loop#1", "type": "sequence", "status": "completed", "iteration": 1, "children": [
            {"nodeId": "build", "type": "step", "status": "completed", "iteration": 1, "sessionId": "sess_pass1"}
          ]}
        ]},
        {"nodeId": "later", "type": "step", "status": "pending"}
      ]
    }
  }
}`

func promptTextsOf(t *testing.T, entries []marotte.Entry) []string {
	t.Helper()
	var out []string
	for i := range entries {
		if entries[i].Kind != marotte.EntryKindTurnOpen {
			continue
		}
		var open marotte.EntryTurnOpen
		if err := json.Unmarshal(entries[i].Payload, &open); err != nil {
			t.Fatalf("decode turn_open %q: %v", entries[i].ID, err)
		}
		if open.Prompt != nil {
			out = append(out, open.Prompt.Text)
		}
	}
	return out
}

func armStepInspect(br *fakeBridge) {
	br.mu.Lock()
	defer br.mu.Unlock()
	if br.callResults == nil {
		br.callResults = map[string]json.RawMessage{}
	}
	br.callResults[methodKiroWorkflowInspect] = json.RawMessage(stepInspect)
}

// armStepReplay answers `session/load` with a replay of texts stamped with the loaded session id, foreign to the utility session.
func armStepReplay(br *fakeBridge, sessionID string, texts ...string) {
	frames := make([]*marotte.RPCResponse, 0, len(texts))
	for _, tx := range texts {
		frames = append(frames, newSessionChunkMsg(sessionID, tx))
	}
	br.mu.Lock()
	defer br.mu.Unlock()
	if br.notifsOnCall == nil {
		br.notifsOnCall = map[string][]*marotte.RPCResponse{}
	}
	br.notifsOnCall[marotte.MethodSessionLoad] = frames
}

func shortStepBudget(t *testing.T, d time.Duration) {
	t.Helper()
	prev := stepTranscriptBudget
	stepTranscriptBudget = d
	t.Cleanup(func() { stepTranscriptBudget = prev })
}

func TestStepTranscript_AStepsFramesProject(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	armStepInspect(br)
	armStepReplay(br, "sess_pass0", "first half ", "second half")

	got, err := h.Runs().stepTranscript(t.Context(), "wf_1", "wf_1:loop:iter-0:build")
	if err != nil {
		t.Fatalf("StepTranscript: %v", err)
	}
	if got.State != marotte.RunStepTranscriptReady {
		t.Fatalf("state = %q, want ready", got.State)
	}
	if texts := textsOf(t, got.Entries); !slices.Equal(texts, []string{"first half second half"}) {
		t.Errorf("projected texts = %q, want the two deltas coalesced into one entry", texts)
	}

	if got.WorkflowID != "wf_1" || got.NodePath != "wf_1:loop:iter-0:build" {
		t.Errorf("echoed identity = %q/%q, want wf_1/wf_1:loop:iter-0:build",
			got.WorkflowID, got.NodePath)
	}
}

// TestStepTranscript_ARepeatsIterationsAreDistinct pins that only the path separates two `build` steps, each reaching its own session.
func TestStepTranscript_ARepeatsIterationsAreDistinct(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	armStepInspect(br)

	for _, tc := range []struct {
		path    string
		session string
		want    string
	}{
		{path: "wf_1:loop:iter-0:build", session: "sess_pass0", want: "pass zero"},
		{path: "wf_1:loop:iter-1:build", session: "sess_pass1", want: "pass one"},
	} {
		armStepReplay(br, tc.session, tc.want)
		got, err := h.Runs().stepTranscript(t.Context(), "wf_1", tc.path)
		if err != nil {
			t.Fatalf("StepTranscript(%s): %v", tc.path, err)
		}
		if got.State != marotte.RunStepTranscriptReady {
			t.Fatalf("%s: state = %q, want ready", tc.path, got.State)
		}
		if texts := textsOf(t, got.Entries); !slices.Equal(texts, []string{tc.want}) {
			t.Errorf("%s projected texts %q, want one %q", tc.path, texts, tc.want)
		}
		// The loaded session id is the other half: the fake would answer the same frames either way.
		if id := br.lastParamsFor(marotte.MethodSessionLoad)[marotte.KeySessionID]; id != tc.session {
			t.Errorf("%s loaded session %v, want %s", tc.path, id, tc.session)
		}
	}
}

// TestStepTranscript_AStepThatNeverRanIsGone pins that no session to load, and the step is real, so `gone`, not 404.
func TestStepTranscript_AStepThatNeverRanIsGone(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	armStepInspect(br)

	got, err := h.Runs().stepTranscript(t.Context(), "wf_1", "wf_1:later")
	if err != nil {
		t.Fatalf("StepTranscript: %v", err)
	}
	if got.State != marotte.RunStepTranscriptGone {
		t.Errorf("state = %q, want gone", got.State)
	}
	if len(got.Entries) != 0 {
		t.Errorf("entries = %+v, want none", got.Entries)
	}
	// No session/load goes on the wire.
	if br.called(marotte.MethodSessionLoad) {
		t.Error("a step with no session issued a session/load")
	}
}

// TestStepTranscript_AnUnknownPathIsAClientError separates the step-gone verdict from no-such-step.
func TestStepTranscript_AnUnknownPathIsAClientError(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	armStepInspect(br)

	for _, path := range []string{
		"wf_1:loop:iter-2:build", // an iteration that never ran
		"wf_1:loop:build",        // the tree's own shape, missing the iteration
		"wf_1:loop#0:build",      // the STATE-TREE spelling, which is not the wire's
		"wf_1/loop/iter-0/build", // segments joined with "/" rather than keyed
		"wf_1:nope",
		"wf_other:loop:iter-0:build", // another run's step
		"",                           // no key at all
	} {
		got, err := h.Runs().stepTranscript(t.Context(), "wf_1", path)
		if err == nil {
			t.Errorf("StepTranscript(%q) = %+v, want errStepUnknown", path, got)
			continue
		}
		if !strings.Contains(err.Error(), "no step at that path") {
			t.Errorf("StepTranscript(%q) error = %v, want errStepUnknown", path, err)
		}
	}
}

// TestStepTranscript_AFailedLoadIsUnavailable pins that KAS's unknown-id and transient answers look alike, so never `gone`.
func TestStepTranscript_AFailedLoadIsUnavailable(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	armStepInspect(br)
	br.mu.Lock()
	br.callRPCErrs = map[string]*marotte.RPCError{
		marotte.MethodSessionLoad: {Code: -32603, Message: "Internal error"},
	}
	br.mu.Unlock()

	got, err := h.Runs().stepTranscript(t.Context(), "wf_1", "wf_1:loop:iter-0:build")
	if err != nil {
		t.Fatalf("a refused load must not be an error the handler 500s on: %v", err)
	}
	if got.State != marotte.RunStepTranscriptUnavailable {
		t.Errorf("state = %q, want unavailable", got.State)
	}
	if len(got.Entries) != 0 {
		t.Errorf("entries = %+v, want none", got.Entries)
	}
}

// TestStepTranscript_AnUnreadableRunIsUnavailable pins that a 200 `unavailable`, for both arms: an empty result (rawCall
// drops in-band errors) and undecodable bytes.
func TestStepTranscript_AnUnreadableRunIsUnavailable(t *testing.T) {
	for _, tc := range []struct {
		desc  string
		arm   func(*fakeBridge)
		wantN int
	}{
		{
			desc: "KAS refused the inspect, which arrives as an empty result",
			arm: func(br *fakeBridge) {
				br.callRPCErrs = map[string]*marotte.RPCError{
					methodKiroWorkflowInspect: {Code: -32603, Message: "Internal error"},
				}
			},
		},
		{
			desc: "the reply is not JSON at all",
			arm: func(br *fakeBridge) {
				br.callResults = map[string]json.RawMessage{
					methodKiroWorkflowInspect: json.RawMessage(`not json`),
				}
			},
		},
		{
			desc: "the reply is JSON of the wrong shape",
			arm: func(br *fakeBridge) {
				br.callResults = map[string]json.RawMessage{
					methodKiroWorkflowInspect: json.RawMessage(`{"state":"a string, not an object"}`),
				}
			},
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			h, _, br := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			br.mu.Lock()
			tc.arm(br)
			br.mu.Unlock()

			got, err := h.Runs().stepTranscript(t.Context(), "wf_1", "wf_1:loop:iter-0:build")
			if err != nil {
				t.Fatalf("an unreadable run must not be a client error: %v", err)
			}
			if got.State != marotte.RunStepTranscriptUnavailable {
				t.Errorf("state = %q, want unavailable", got.State)
			}
		})
	}
}

// TestStepTranscript_TheBudgetBoundsTheBarrier pins that the utility forward goroutine is not wired to the registry, the shape of a wedged replay.
func TestStepTranscript_TheBudgetBoundsTheBarrier(t *testing.T) {
	shortStepBudget(t, 40*time.Millisecond)
	br := newFakeBridge()
	armStepInspect(br)
	armStepReplay(br, "sess_pass0", "never drained")
	rs := unwiredStepRuns(t, br)

	start := time.Now()
	got, err := rs.stepTranscript(t.Context(), "wf_1", "wf_1:loop:iter-0:build")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("StepTranscript: %v", err)
	}
	if got.State != marotte.RunStepTranscriptUnavailable {
		t.Errorf("state = %q, want unavailable", got.State)
	}
	// It answers; the ceiling is generous.
	if elapsed > 5*time.Second {
		t.Errorf("answered after %v, want inside the budget", elapsed)
	}
}

// TestStepTranscript_ARefusedLoadLeavesNoReplayOpen pins that every exit takes the replay, so a retry is not refused by a corpse.
func TestStepTranscript_ARefusedLoadLeavesNoReplayOpen(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	armStepInspect(br)
	br.mu.Lock()
	br.callRPCErrs = map[string]*marotte.RPCError{
		marotte.MethodSessionLoad: {Code: -32603, Message: "Internal error"},
	}
	br.mu.Unlock()

	if _, err := h.Runs().stepTranscript(t.Context(), "wf_1", "wf_1:loop:iter-0:build"); err != nil {
		t.Fatalf("first read: %v", err)
	}
	// The retry succeeds; without the deferred take the registry would answer `unavailable` forever.
	br.mu.Lock()
	br.callRPCErrs = nil
	br.mu.Unlock()
	armStepReplay(br, "sess_pass0", "second time")

	got, err := h.Runs().stepTranscript(t.Context(), "wf_1", "wf_1:loop:iter-0:build")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got.State != marotte.RunStepTranscriptReady {
		t.Fatalf("retry state = %q, want ready — the first read left a replay open", got.State)
	}
	if texts := textsOf(t, got.Entries); !slices.Equal(texts, []string{"second time"}) {
		t.Errorf("retry projected texts %q, want one %q", texts, "second time")
	}
}

// TestStepTranscript_IncludesReaderInterventions pins the row filter: the pane owns the instruction, the transcript a later human answer, and event rows have no card.
func TestStepTranscript_IncludesReaderInterventions(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	armStepInspect(br)
	replay := func(update string) *marotte.RPCResponse {
		params, _ := json.Marshal(map[string]any{
			"sessionId": "sess_pass0",
			"update":    json.RawMessage(update),
		})
		return &marotte.RPCResponse{Method: marotte.MethodSessionUpdate, Params: params}
	}
	br.mu.Lock()
	br.notifsOnCall = map[string][]*marotte.RPCResponse{
		marotte.MethodSessionLoad: {
			replay(`{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"the step's own instruction"},"_meta":{"kiro":{"messageId":"instruction"}}}`),
			replay(`{"sessionUpdate":"session_info_update","_meta":{"kiro":{"kind":"turn_start","turnStart":true}}}`),
			replay(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Which target?"},"_meta":{"kiro":{"messageId":"question"}}}`),
			replay(`{"sessionUpdate":"session_info_update","_meta":{"kiro":{"kind":"turn_end"}}}`),
			replay(`{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"main"},"_meta":{"kiro":{"messageId":"reader-answer"}}}`),
			replay(`{"sessionUpdate":"session_info_update","_meta":{"kiro":{"kind":"turn_start","turnStart":true}}}`),
			replay(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Using main."},"_meta":{"kiro":{"messageId":"result"}}}`),
			replay(`{"sessionUpdate":"session_info_update","_meta":{"kiro":{"kind":"turn_end"}}}`),
			replay(`{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"step notice"},"_meta":{"kiro":{"messageId":"notify-1","notification":{"kind":"system-notification"}}}}`),
		},
	}
	br.mu.Unlock()

	got, err := h.Runs().stepTranscript(t.Context(), "wf_1", "wf_1:loop:iter-0:build")
	if err != nil {
		t.Fatalf("StepTranscript: %v", err)
	}
	// The reader's answer opens its own turn as an ordinary prompt, between question and reply.
	if texts := textsOf(t, got.Entries); !slices.Equal(texts, []string{"Which target?", "Using main."}) {
		t.Errorf("StepTranscript texts = %q, want the question and the reply after the answer", texts)
	}
	question, answer, reply := -1, -1, -1
	for i := range got.Entries {
		switch e := got.Entries[i]; {
		case e.Kind == marotte.EntryKindText && question < 0:
			question = i
		case e.Kind == marotte.EntryKindText:
			reply = i
		case e.Kind == marotte.EntryKindTurnOpen && slices.Contains(promptTextsOf(t, []marotte.Entry{e}), "main"):
			answer = i
		}
	}
	if answer < 0 {
		t.Fatalf("StepTranscript prompt turn_opens = %q, want the reader's answer among them", promptTextsOf(t, got.Entries))
	}
	if question >= answer || answer >= reply {
		t.Errorf("StepTranscript order: question at %d, answer at %d, reply at %d; want the answer's turn_open between the two texts", question, answer, reply)
	}
}

// TestStepTranscript_TheUtilitySessionKeepsItsOwnIdentity pins that a raw `session/load` must not rebind the utility's
// session id, or the reaper deletes a live subprocess's state.
func TestStepTranscript_TheUtilitySessionKeepsItsOwnIdentity(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	armStepInspect(br)
	armStepReplay(br, "sess_pass0", "hello")

	// Settle the utility's id before the read.
	if err := h.utility.get().session.ensureStarted(t.Context()); err != nil {
		t.Fatalf("start utility session: %v", err)
	}
	before := h.utility.get().session.liveID()
	if before == "" {
		t.Fatal("the utility session reports no id")
	}

	if _, err := h.Runs().stepTranscript(t.Context(), "wf_1", "wf_1:loop:iter-0:build"); err != nil {
		t.Fatalf("StepTranscript: %v", err)
	}

	if after := h.utility.get().session.liveID(); after != before {
		t.Errorf("the utility session's id moved from %q to %q across a step read — "+
			"the orphan reaper's keep-list now names the wrong session", before, after)
	}
	if after := before; after == "sess_pass0" {
		t.Error("the utility session adopted the step's id")
	}
}

func getStepTranscript(t *testing.T, h *Runtime, target string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	(&runRoutes{runs: h.Runs()}).register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestHandleStepTranscript_HTTP(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	armStepInspect(br)
	armStepReplay(br, "sess_pass0", "served")

	rec := getStepTranscript(t, h, "/api/runs/wf_1/steps/wf_1%3Aloop%3Aiter-0%3Abuild")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var out marotte.RunStepTranscript
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if out.State != marotte.RunStepTranscriptReady {
		t.Errorf("state = %q, want ready", out.State)
	}
	if texts := textsOf(t, out.Entries); !slices.Equal(texts, []string{"served"}) {
		t.Errorf("texts = %q, want one %q", texts, "served")
	}
	// The verdict must be present: a client finding it absent would assume ready.
	if !strings.Contains(rec.Body.String(), `"state"`) {
		t.Error("the reply omits `state`")
	}
}

// TestHandleStepTranscript_AGoneVerdictIsA200 pins that only a path the run does not name is an HTTP error.
func TestHandleStepTranscript_AGoneVerdictIsA200(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	armStepInspect(br)

	rec := getStepTranscript(t, h, "/api/runs/wf_1/steps/wf_1%3Alater")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var out marotte.RunStepTranscript
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.State != marotte.RunStepTranscriptGone {
		t.Errorf("state = %q, want gone", out.State)
	}
}

func TestHandleStepTranscript_Refusals(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	armStepInspect(br)

	for _, tc := range []struct {
		desc   string
		method string
		target string
		want   int
	}{
		{
			desc: "a path this run does not name is a 404",
			// The one HTTP error: the caller's path.
			method: http.MethodGet,
			target: "/api/runs/wf_1/steps/wf_1%3Anope",
			want:   http.StatusNotFound,
		},
		{
			desc:   "no key at all is a 404",
			method: http.MethodGet,
			target: "/api/runs/wf_1/steps/",
			want:   http.StatusNotFound,
		},
		{
			// A 307 to the slash form would be a hop canonicalAPIPath cannot see.
			desc:   "the bare collection is a 404, never a redirect",
			method: http.MethodGet,
			target: "/api/runs/wf_1/steps",
			want:   http.StatusNotFound,
		},
		{
			desc:   "another run's step is a 404",
			method: http.MethodGet,
			target: "/api/runs/wf_1/steps/wf_other%3Aloop%3Aiter-0%3Abuild",
			want:   http.StatusNotFound,
		},
		{
			// {path} is one segment, so two after steps/ are a step verb's address, which only POST serves.
			desc:   "the run id repeated ahead of the key is no step to GET",
			method: http.MethodGet,
			target: "/api/runs/wf_1/steps/wf_1/wf_1%3Aloop%3Aiter-0%3Abuild",
			want:   http.StatusMethodNotAllowed,
		},
		{
			desc:   "a non-GET method is refused",
			method: http.MethodPost,
			target: "/api/runs/wf_1/steps/wf_1%3Aloop%3Aiter-0%3Abuild",
			want:   http.StatusMethodNotAllowed,
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			mux := http.NewServeMux()
			(&runRoutes{runs: h.Runs()}).register(mux)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.target, nil))
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// TestHandleStepTranscript_APercentEncodedSegmentDecodes pins that the key is one encodeURIComponent segment after the
// run id, which ServeMux's `{path}` hands over decoded.
func TestHandleStepTranscript_APercentEncodedSegmentDecodes(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	// `loop#0` is a repeat child's fallback id when KAS sends no `iteration`.
	br.mu.Lock()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowInspect: json.RawMessage(`{"state":{"workflowId":"wf_1","root":{` +
			`"nodeId":"wf_1","type":"sequence","children":[` +
			`{"nodeId":"a b#0","type":"step","status":"completed","sessionId":"sess_odd"}]}}}`),
	}
	br.mu.Unlock()
	armStepReplay(br, "sess_odd", "odd id")

	rec := getStepTranscript(t, h, "/api/runs/wf_1/steps/wf_1%3Aa%20b%230")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var out marotte.RunStepTranscript
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.State != marotte.RunStepTranscriptReady {
		t.Fatalf("state = %q, want ready", out.State)
	}
	if texts := textsOf(t, out.Entries); !slices.Equal(texts, []string{"odd id"}) {
		t.Errorf("texts = %q, want one %q", texts, "odd id")
	}
}

func TestHandleStepTranscript_SlashBearingNodeIDsStayDistinct(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	br.mu.Lock()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowInspect: json.RawMessage(`{"state":{"workflowId":"wf_1","root":{` +
			`"nodeId":"wf_1","type":"sequence","children":[` +
			`{"nodeId":"a/b","type":"sequence","children":[` +
			`{"nodeId":"c","type":"step","status":"completed","sessionId":"sess_ab_c"}]},` +
			`{"nodeId":"a","type":"sequence","children":[` +
			`{"nodeId":"b/c","type":"step","status":"completed","sessionId":"sess_a_bc"}]}]}}}`),
	}
	br.mu.Unlock()

	for _, tc := range []struct {
		target  string
		session string
	}{
		{target: "/api/runs/wf_1/steps/wf_1%3Aa%2Fb%3Ac", session: "sess_ab_c"},
		{target: "/api/runs/wf_1/steps/wf_1%3Aa%3Ab%2Fc", session: "sess_a_bc"},
	} {
		armStepReplay(br, tc.session, "from "+tc.session)
		rec := getStepTranscript(t, h, tc.target)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200: %s", tc.target, rec.Code, rec.Body.String())
			continue
		}
		if id := br.lastParamsFor(marotte.MethodSessionLoad)[marotte.KeySessionID]; id != tc.session {
			t.Errorf("GET %s loaded session %v, want %s", tc.target, id, tc.session)
		}
	}
}

// TestStepReplays_TheSettleSpansTheDrain pins that a returned load settles only once the consumer folded everything
// before its result: frames precede it and the channel is buffered.
func TestStepReplays_TheSettleSpansTheDrain(t *testing.T) {
	var sr stepReplays
	if !sr.open("sess_a", translate.NewEntryProjection(newMessageID, "")) {
		t.Fatal("open reported a duplicate on an empty registry")
	}
	if closedNow(sr.barrier("sess_a")) {
		t.Fatal("a fresh replay's barrier is already closed")
	}
	// The load has not returned, so its position is unknown.
	sr.settleConsumed(atFrame(testLoadSeq), false)
	if closedNow(sr.barrier("sess_a")) {
		t.Fatal("settled before the load returned")
	}
	sr.markLoadedAt("sess_a", drainPoint{gen: testFwdGen, seq: testLoadSeq + 2})
	// Returned, but the consumer is behind.
	sr.settleConsumed(atFrame(testLoadSeq+1), false)
	if closedNow(sr.barrier("sess_a")) {
		t.Fatal("settled while frames were still unfolded: the drain was not spanned")
	}
	sr.settleConsumed(atFrame(testLoadSeq+2), false)
	if !closedNow(sr.barrier("sess_a")) {
		t.Error("did not settle with the load returned and its position reached")
	}
}

// TestStepReplays_ADrainedReplaySettlesWhenTheLoadReturns pins that a replay fully folded before the RPC returns must
// settle on the load's own attempt, or the read waits out 60s and answers `unavailable`.
func TestStepReplays_ADrainedReplaySettlesWhenTheLoadReturns(t *testing.T) {
	var sr stepReplays
	if !sr.open("sess_a", translate.NewEntryProjection(newMessageID, "")) {
		t.Fatal("open reported a duplicate on an empty registry")
	}
	// Every frame folded while the RPC was in flight.
	sr.settleConsumed(atFrame(testLoadSeq), false)
	if closedNow(sr.barrier("sess_a")) {
		t.Fatal("settled before the load returned")
	}

	sr.markLoadedAt("sess_a", atLoad())

	if !closedNow(sr.barrier("sess_a")) {
		t.Error("the barrier is still open on a replay the consumer had already " +
			"folded: nothing else will close it, so the read waits out its budget " +
			"and answers unavailable for a transcript it has in hand")
	}
}

// TestStepReplays_AStragglerFromAPreviousAttachmentSettlesNothing pins that the utility session is recycled (20 prompts,
// 30 min idle), and an old forward's positions run far ahead of a restarted sequence.
func TestStepReplays_AStragglerFromAPreviousAttachmentSettlesNothing(t *testing.T) {
	var sr stepReplays
	if !sr.open("sess_a", translate.NewEntryProjection(newMessageID, "")) {
		t.Fatal("open reported a duplicate on an empty registry")
	}
	const live = testFwdGen + 1
	sr.markLoadedAt("sess_a", drainPoint{gen: live, seq: testLoadSeq})

	sr.settleConsumed(drainPoint{gen: testFwdGen, seq: 900}, false)
	if closedNow(sr.barrier("sess_a")) {
		t.Fatal("a straggling observation from the previous subprocess settled a " +
			"replay loaded on the live one")
	}
	// Nor its exit seal.
	sr.settleConsumed(drainPoint{gen: testFwdGen}, true)
	if closedNow(sr.barrier("sess_a")) {
		t.Fatal("the previous subprocess's exit seal settled the live replay")
	}
	// Nor its position stored: the live session's first frame would then settle on nothing folded.
	sr.settleConsumed(drainPoint{gen: live, seq: 1}, false)
	if closedNow(sr.barrier("sess_a")) {
		t.Fatal("the live subprocess's FIRST frame settled the replay: the straggler's " +
			"position was adopted")
	}

	sr.settleConsumed(drainPoint{gen: live, seq: testLoadSeq}, false)
	if !closedNow(sr.barrier("sess_a")) {
		t.Error("the live attachment reaching the load position did not settle it")
	}
}

// TestStepReplays_NoReplayOpenAnswersImmediately pins that never wait on a load that is not happening.
func TestStepReplays_NoReplayOpenAnswersImmediately(t *testing.T) {
	var sr stepReplays
	if !closedNow(sr.barrier("nobody")) {
		t.Error("an unknown session's barrier is not already closed")
	}
	if got := sr.take("nobody"); got != nil {
		t.Errorf("take of an unknown session = %+v, want nil", got)
	}
	// The drain loop calls settleConsumed on every frame, read or not.
	sr.markLoadedAt("nobody", atLoad())
	sr.settleConsumed(atFrame(testLoadSeq), false)
}

// TestStepReplays_ASecondReadOfOneStepIsRefused, without disturbing the first.
func TestStepReplays_ASecondReadOfOneStepIsRefused(t *testing.T) {
	var sr stepReplays
	if !sr.open("sess_a", translate.NewEntryProjection(newMessageID, "")) {
		t.Fatal("the first open was refused")
	}
	if sr.open("sess_a", translate.NewEntryProjection(newMessageID, "")) {
		t.Error("a second open of the same session was accepted")
	}
	sr.markLoadedAt("sess_a", atLoad())
	sr.settleConsumed(atFrame(testLoadSeq), false)
	if !closedNow(sr.barrier("sess_a")) {
		t.Error("the refused second open disturbed the first replay")
	}
	// Once taken, the session is readable again.
	_ = sr.take("sess_a")
	if !sr.open("sess_a", translate.NewEntryProjection(newMessageID, "")) {
		t.Error("a session cannot be read again after its replay was taken")
	}
}

// TestStepReplays_TakeIsIdempotentAndClosesTheBarrier pins that take runs on timeout too.
func TestStepReplays_TakeIsIdempotentAndClosesTheBarrier(t *testing.T) {
	var sr stepReplays
	sr.open("sess_a", translate.NewEntryProjection(newMessageID, ""))
	b := sr.barrier("sess_a")
	_ = sr.take("sess_a") // the abandoned path: never settled
	if !closedNow(b) {
		t.Error("take left a barrier nothing will ever close")
	}
	if got := sr.take("sess_a"); got != nil {
		t.Errorf("a second take = %+v, want nil", got)
	}
	// A settle on a taken entry must not double-close.
	sr.settleConsumed(atFrame(testLoadSeq), false)
}

// TestStepReplays_IngestReportsWhetherItConsumed lets the utility session tell a replay frame from a foreign one.
func TestStepReplays_IngestReportsWhetherItConsumed(t *testing.T) {
	var sr stepReplays
	raw := json.RawMessage(`{"content":{"type":"text","text":"x"}}`)
	if sr.ingest("sess_a", marotte.ACPUpdateAgentChunk, raw) {
		t.Error("ingest claimed a frame with no replay open")
	}
	sr.open("sess_a", translate.NewEntryProjection(newMessageID, ""))
	if !sr.ingest("sess_a", marotte.ACPUpdateAgentChunk, raw) {
		t.Error("ingest dropped a frame for an open replay")
	}
	if sr.ingest("sess_b", marotte.ACPUpdateAgentChunk, raw) {
		t.Error("ingest claimed a frame for a session nobody is reading")
	}
	turns := sr.take("sess_a")
	if len(turns) != 1 {
		t.Fatalf("projected %d turns, want one", len(turns))
	}
	if texts := textsOf(t, turns[0].Entries); !slices.Equal(texts, []string{"x"}) {
		t.Errorf("projected texts %q, want one %q", texts, "x")
	}
}

// TestStepTranscript_SettlesOnTheBarrierRatherThanTheBudget pins that at 50ms only a closed barrier answers `ready`, so
// position, attachment and the per-frame report must agree.
func TestStepTranscript_SettlesOnTheBarrierRatherThanTheBudget(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	armStepInspect(br)
	armStepReplay(br, "sess_pass0", "settled ", "in time")
	shortStepBudget(t, 50*time.Millisecond)

	got, err := h.Runs().stepTranscript(t.Context(), "wf_1", "wf_1:loop:iter-0:build")
	if err != nil {
		t.Fatalf("StepTranscript: %v", err)
	}
	if got.State != marotte.RunStepTranscriptReady {
		t.Fatalf("state = %q, want ready — the replay did not settle inside a 50ms "+
			"budget, so the read is waiting out its clock rather than the drain", got.State)
	}
	if texts := textsOf(t, got.Entries); !slices.Equal(texts, []string{"settled in time"}) {
		t.Errorf("texts = %q, want one %q", texts, "settled in time")
	}
}

// TestUtilityRawCallAt_CarriesTheResponsePosition pins that a zero would make the condition trivially true. It also
// names the attachment, since recycled sessions restart at zero.
func TestUtilityRawCallAt_CarriesTheResponsePosition(t *testing.T) {
	br := newFakeBridge()
	armStepReplay(br, "sess_pass0", "one ", "two ", "three")
	rs := unwiredStepRuns(t, br)

	raw, at, err := rs.utility().session.rawCallAt(t.Context(), "step transcript load",
		marotte.MethodSessionLoad,
		callerParams(map[string]any{marotte.KeySessionID: "sess_pass0"}))
	if err != nil {
		t.Fatalf("rawCallAt: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("rawCallAt returned an empty result, so the load did not answer")
	}
	if at.seq != 3 {
		t.Errorf("load position = %d, want 3 — the three replay frames all precede "+
			"the result on the wire, so the consumer has to reach their position", at.seq)
	}
	if at.gen == 0 {
		t.Error("the load position names no attachment, so a straggling observation " +
			"from a recycled subprocess could satisfy it")
	}
}

// closedNow reports whether a barrier is already closed, without waiting.
func closedNow(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// unwiredStepRuns builds a Runs whose utility session has no forward goroutine feeding the registry: a wedged
// replay, driving the budget deterministically.
func unwiredStepRuns(t *testing.T, br *fakeBridge) *Runs {
	t.Helper()
	session := &utilitySession{
		shutdownCtx:   context.Background(),
		bridgeFactory: func() ACPBridge { return br },
		models:        func() []marotte.SessionModel { return nil },
	}
	ur := &utilityRuntime{session: session, textgen: newUtilityAgent(session)}
	t.Cleanup(session.Stop)
	return &Runs{
		translate: noopRunTranslator{},
		utility:   func() *utilityRuntime { return ur },
	}
}

func (b *fakeBridge) lastParamsFor(method string) map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastParams[method]
}

func (b *fakeBridge) called(method string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Contains(b.calls, method)
}
