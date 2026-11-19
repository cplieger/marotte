package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
)

const landTitle = "Loaded skill: land"

var landLoaded = []marotte.LoadedResource{{ID: "skill::land", Type: marotte.LoadedSkill, Name: "land"}}

func loadSkillCall() *marotte.EntryToolCall {
	return &marotte.EntryToolCall{
		ID: "tc-1", Title: "Tool Load", Kind: marotte.ToolKindOther, Status: marotte.ToolPending,
		ToolName: "tool_load", Input: json.RawMessage(`{"id":"skill::land"}`),
	}
}

// servedCard is the card_title and loaded of the one served entry of kind.
func servedCard(t *testing.T, entries []marotte.Entry, kind marotte.EntryKind) (string, []marotte.LoadedResource) {
	t.Helper()
	type card struct {
		CardTitle string                   `json:"card_title"`
		Loaded    []marotte.LoadedResource `json:"loaded"`
	}
	var got []card
	for i := range entries {
		if entries[i].Kind != kind {
			continue
		}
		var v card
		if err := json.Unmarshal(entries[i].Payload, &v); err != nil {
			t.Fatalf("decode the served %s: %v", kind, err)
		}
		got = append(got, v)
	}
	if len(got) != 1 {
		t.Fatalf("%d served %s entries in %v, want 1", len(got), kind, kinds(entries))
	}
	return got[0].CardTitle, got[0].Loaded
}

func checkLandCard(t *testing.T, entries []marotte.Entry, kind marotte.EntryKind) {
	t.Helper()
	if title, loaded := servedCard(t, entries, kind); title != landTitle || !reflect.DeepEqual(loaded, landLoaded) {
		t.Errorf("served %s card_title %q loaded %+v, want %q %+v", kind, title, loaded, landTitle, landLoaded)
	}
}

func TestStepTranscript_ServesTheCardTitle(t *testing.T) {
	t.Parallel()
	const id = "wf1"
	nodePath := workflow.PathKey([]string{id, "build"})
	h, _, _ := runLoggingHub(t)
	h.runs.RunNodeStart(t.Context(), &translate.RunStep{RunID: id, NodePath: nodePath, SessionID: "step-session-1"}, "")
	turn := h.runs.log.turn(id, nodePath)
	if turn == nil {
		t.Fatalf("no open turn for %s", nodePath)
	}
	if _, err := turn.ToolCall(t.Context(), "", loadSkillCall()); err != nil {
		t.Fatal(err)
	}
	if _, err := turn.ToolResult(t.Context(), "", "tc-1", &marotte.EntryToolResult{Status: marotte.ToolCompleted}); err != nil {
		t.Fatal(err)
	}

	out, err := h.runs.stepTranscript(t.Context(), id, nodePath)
	if err != nil || out.Source != marotte.RunStepTranscriptSourceLog {
		t.Fatalf("stepTranscript = source %q, err %v; want the run log's", out.Source, err)
	}
	checkLandCard(t, out.Entries, marotte.EntryKindToolCall)
	checkLandCard(t, out.Entries, marotte.EntryKindToolResult)
}

func TestStepTranscript_ServesAReplayedCallsCardTitle(t *testing.T) {
	t.Parallel()
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	armStepInspect(br)
	br.mu.Lock()
	br.notifsOnCall = map[string][]*marotte.RPCResponse{marotte.MethodSessionLoad: {
		replayFrame("sess_pass0", `{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"Tool Load","kind":"other",`+
			`"status":"pending","rawInput":{"id":"skill::land"}}`),
		replayFrame("sess_pass0", `{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"completed"}`),
	}}
	br.mu.Unlock()

	out, err := h.Runs().stepTranscript(t.Context(), "wf_1", "wf_1:loop:iter-0:build")
	if err != nil || out.State != marotte.RunStepTranscriptReady {
		t.Fatalf("stepTranscript = state %q, err %v; want ready", out.State, err)
	}
	checkLandCard(t, out.Entries, marotte.EntryKindToolCall)
	checkLandCard(t, out.Entries, marotte.EntryKindToolResult)
}

// A repair read past the call titles the result from the call the read does not serve.
func TestRunTurnRange_StampsAResultWhoseCallPrecedesAfter(t *testing.T) {
	t.Parallel()
	r := newRunLog(t.TempDir())
	turn, _, err := r.open(t.Context(), &translate.RunStep{RunID: "wf1", NodePath: "root/step", SessionID: "sess-a"}, "c-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := turn.ToolCall(t.Context(), "", loadSkillCall()); err != nil {
		t.Fatal(err)
	}
	if _, err := turn.ToolResult(t.Context(), "", "tc-1", &marotte.EntryToolResult{Status: marotte.ToolCompleted}); err != nil {
		t.Fatal(err)
	}

	entries, _, stamps, found, err := r.turnRange(t.Context(), "wf1", turn.ID(), 2)
	if err != nil || !found {
		t.Fatalf("turnRange(from=2) = found %v, err %v", found, err)
	}
	if len(entries) != 1 || entries[0].Kind != marotte.EntryKindToolResult || entries[0].Seq != 2 {
		t.Fatalf("turnRange(from=2) entries = %v, want the tool_result at seq 2 alone", kinds(entries))
	}
	checkLandCard(t, entries, marotte.EntryKindToolResult)
	if want := turnVersion(turn.ID(), 2); len(stamps) != 1 || stamps[0].Version != want {
		t.Errorf("stamp versions = %v, want one at %q", stampVersions(stamps), want)
	}
}
