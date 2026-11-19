package translate

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// Frames in the shapes KAS sends with the job shell on: LiveJobCards' jobCardUpdate (the move, then the end
// with jobEndOf's record), JobUpdatePublisher's _kiro/job/update, the notice row JobEndNotifications broadcasts, and
// sendContentChunk's _kiro/tools/content_chunk.

const (
	jobID       = "7"
	jobCallID   = "tc-job"
	jobStarted  = "2026-11-18T10:00:00.000Z"
	jobEndedAt  = "2026-11-18T10:01:01.000Z"
	jobDuration = 61000
)

func jobCreate(t *testing.T) json.RawMessage {
	t.Helper()
	return mustJSON(t, map[string]any{
		"toolCallId": jobCallID, "title": "Running: sleep 60", "kind": "execute", "status": "in_progress",
		"rawInput": map[string]any{"command": "sleep 60"},
	})
}

func jobMoved(t *testing.T) json.RawMessage {
	t.Helper()
	return mustJSON(t, map[string]any{
		"toolCallId": jobCallID, "status": "in_progress",
		"_meta": map[string]any{"kiro": map[string]any{"job": map[string]any{"id": jobID, "outputFile": "/tmp/kiro-jobs/7.log"}}},
	})
}

func jobEndRecord(status string) map[string]any {
	return map[string]any{
		"status": status, "stopReason": "model", "startedAt": jobStarted, "endedAt": jobEndedAt, "durationMs": jobDuration,
	}
}

func jobEndedCard(t *testing.T, cardStatus, jobStatus string) json.RawMessage {
	t.Helper()
	return mustJSON(t, map[string]any{
		"toolCallId": jobCallID, "status": cardStatus, "rawOutput": "slept\n",
		"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "slept"}}},
		"_meta": map[string]any{"kiro": map[string]any{"job": map[string]any{
			"id": jobID, "outputFile": "/tmp/kiro-jobs/7.log", "end": jobEndRecord(jobStatus),
		}}},
	})
}

func jobNotice(t *testing.T, status string) json.RawMessage {
	t.Helper()
	text := "[JOB NOTIFICATION]\n\nA background job you started has ended.\n\n" +
		`<notification id="notification-job-7" jobId="7" tool="execute_bash" kind="shell" status="` + status +
		`" ranFor="1m 1s" description="sleep 60 &amp;&amp; echo &quot;done&quot;">` + "\n<result>\nslept\n</result>\n</notification>"
	return mustJSON(t, map[string]any{
		"sessionUpdate": "user_message_chunk",
		"content":       map[string]any{"type": "text", "text": text},
		"_meta": map[string]any{"kiro": map[string]any{
			"source": "steer", "messageId": "notification-job-7", "timestamp": jobEndedAt,
			"notification": map[string]any{
				"kind": "system-notification", "status": "success", "notifyId": "notification-job-7",
				"jobId": jobID, "jobEnd": jobEndRecord(status),
			},
		}},
	})
}

func jobUpdateMsg(t *testing.T, session, id, status string) *marotte.RPCResponse {
	t.Helper()
	p := map[string]any{
		"sessionId": session, "id": id, "kind": "shell", "description": "sleep 60", "status": status, "startedAt": jobStarted,
	}
	if status != "running" {
		p["endedAt"] = jobEndedAt
	}
	return &marotte.RPCResponse{Method: "_kiro/job/update", Params: mustJSON(t, p)}
}

func jobEndedEntries(entries []marotte.Entry) []marotte.Entry {
	return slices.DeleteFunc(slices.Clone(entries), func(e marotte.Entry) bool { return e.Kind != marotte.EntryKindJobEnded })
}

// A moved call's card outlives its turn: the close seals it completed with the job's mark, never aborted, and the
// job's end, arriving after the turn closed, is filed as the chat's job_ended entry.
func TestJobCardSurvivesTurnClose(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	tr.HandleToolCall(t.Context(), "c1", jobCreate(t), FrameAttribution{})
	tr.HandleToolCallUpdate(t.Context(), "c1", jobMoved(t), FrameAttribution{})
	closeChatTurn(t, deps, "c1")

	var res marotte.EntryToolResult
	for _, e := range deps.chatEntries("c1") {
		if e.Kind == marotte.EntryKindToolResult && e.ID == marotte.ToolResultID(jobCallID) {
			res = decodePayload[marotte.EntryToolResult](t, &e)
		}
	}
	if res.Status != marotte.ToolCompleted || res.Job == nil || res.Job.ID != jobID || res.Job.End != nil {
		t.Fatalf("the close sealed %+v (job %+v), want completed with job 7 still running", res, res.Job)
	}

	tr.HandleToolCallUpdate(t.Context(), "c1", jobEndedCard(t, "completed", "completed"), FrameAttribution{})

	ends := jobEndedEntries(deps.between["c1"])
	if len(ends) != 1 {
		t.Fatalf("between-turns entries = %+v, want one job_ended", deps.between["c1"])
	}
	got := decodePayload[marotte.EntryJobEnded](t, &ends[0])
	want := marotte.EntryJobEnded{
		JobID: jobID, ToolCallID: jobCallID, Description: "", Output: "slept\n",
		End: marotte.BackgroundJobEnd{
			Status: marotte.BackgroundJobCompleted, StopReason: "model", StartedAt: jobStarted, EndedAt: jobEndedAt,
			DurationMs: jobDuration,
		},
	}
	if got != want || ends[0].ID != marotte.JobEndedID(jobID) {
		t.Errorf("job_ended %s = %+v, want %+v", ends[0].ID, got, want)
	}
}

// One job_ended per job, from whichever of the card's end and KAS's notice arrives first; the notice is never a
// steer, and its row names the job's description and its card.
func TestJobEnd_FiledOnceFromTheCardOrTheNotice(t *testing.T) {
	for _, tc := range []struct {
		name        string
		noticeFirst bool
	}{
		{name: "card end first"},
		{name: "notice first", noticeFirst: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, _ := newEventCaptureDeps()
			tr := New(rolesOf(deps))
			tr.HandleToolCall(t.Context(), "c1", jobCreate(t), FrameAttribution{})
			tr.HandleToolCallUpdate(t.Context(), "c1", jobMoved(t), FrameAttribution{})
			closeChatTurn(t, deps, "c1")
			card := func() {
				tr.HandleToolCallUpdate(t.Context(), "c1", jobEndedCard(t, "failed", "timed_out"), FrameAttribution{})
			}
			notice := func() { tr.HandleJobNotice(t.Context(), "c1", jobNotice(t, "timed_out"), FrameAttribution{}) }
			if tc.noticeFirst {
				notice()
				card()
			} else {
				card()
				notice()
			}

			ends := jobEndedEntries(deps.between["c1"])
			if len(ends) != 1 || len(deps.between["c1"]) != 1 {
				t.Fatalf("between-turns entries = %+v, want exactly one job_ended and no steer", deps.between["c1"])
			}
			got := decodePayload[marotte.EntryJobEnded](t, &ends[0])
			if got.End.Status != marotte.BackgroundJobTimedOut || got.ToolCallID != jobCallID {
				t.Errorf("job_ended = %+v, want timed_out on card %s", got, jobCallID)
			}
			if tc.noticeFirst && got.Description != `sleep 60 && echo "done"` {
				t.Errorf("description = %q, want the notice's, unescaped", got.Description)
			}
		})
	}
}

// A frame arriving after the job's end was filed (KAS's terminal job/update, a late move) keeps nothing for the
// filed row: settleEnd dropped the description and the card, and they stay dropped.
func TestJobEnd_LateFramesDoNotRestoreTheFiledRowsFacts(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	tr.HandleToolCall(t.Context(), "c1", jobCreate(t), FrameAttribution{})
	tr.HandleToolCallUpdate(t.Context(), "c1", jobMoved(t), FrameAttribution{})
	tr.HandleJobUpdate(t.Context(), "c1", jobUpdateMsg(t, "", jobID, "running"))
	closeChatTurn(t, deps, "c1")
	tr.HandleToolCallUpdate(t.Context(), "c1", jobEndedCard(t, "failed", "timed_out"), FrameAttribution{})
	if len(jobEndedEntries(deps.between["c1"])) != 1 {
		t.Fatalf("between-turns entries = %+v, want the card's job_ended", deps.between["c1"])
	}

	tr.HandleJobUpdate(t.Context(), "c1", jobUpdateMsg(t, "", jobID, "timed_out"))
	tr.jobs.noteCard("c1", jobID, jobCallID)
	if description, card := tr.jobs.known("c1", jobID); description != "" || card != "" {
		t.Errorf("known() after the filed end = (%q, %q), want both empty", description, card)
	}
}

// A job whose first frame is terminal keeps its description until its end is filed.
func TestJobUpdate_TerminalFirstFrameKeepsTheDescription(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	tr.HandleJobUpdate(t.Context(), "c1", jobUpdateMsg(t, "", jobID, "completed"))
	if description, _ := tr.jobs.known("c1", jobID); description != "sleep 60" {
		t.Errorf("known() description = %q, want sleep 60", description)
	}
}

// A write that fails leaves the job's end unfiled, so the other source, arriving later, files it.
func TestJobEnd_FailedWriteLeavesTheEndToTheOtherSource(t *testing.T) {
	for _, tc := range []struct {
		name        string
		noticeFirst bool
	}{
		{name: "card end fails"},
		{name: "notice fails", noticeFirst: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, _ := newEventCaptureDeps()
			tr := New(rolesOf(deps))
			tr.HandleToolCall(t.Context(), "c1", jobCreate(t), FrameAttribution{})
			tr.HandleToolCallUpdate(t.Context(), "c1", jobMoved(t), FrameAttribution{})
			closeChatTurn(t, deps, "c1")
			card := func() {
				tr.HandleToolCallUpdate(t.Context(), "c1", jobEndedCard(t, "failed", "timed_out"), FrameAttribution{})
			}
			notice := func() { tr.HandleJobNotice(t.Context(), "c1", jobNotice(t, "timed_out"), FrameAttribution{}) }
			first, second := card, notice
			if tc.noticeFirst {
				first, second = notice, card
			}

			deps.turns.appendErr = errRefused
			first()
			deps.turns.appendErr = nil
			second()

			ends := jobEndedEntries(deps.between["c1"])
			if len(ends) != 1 {
				t.Fatalf("between-turns entries = %+v, want the second source's job_ended", deps.between["c1"])
			}
			if got := decodePayload[marotte.EntryJobEnded](t, &ends[0]); got.End.Status != marotte.BackgroundJobTimedOut {
				t.Errorf("job_ended = %+v, want timed_out", got)
			}
		})
	}
}

// A job/update announces job_update and keeps the chat's running list; an end leaves it, and a replay from a new
// session (KAS replays the running jobs on attach) replaces the old session's rows.
func TestJobUpdateBroadcastsAndReplays(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	tr.HandleJobUpdate(t.Context(), "c1", jobUpdateMsg(t, "sess-a", "7", "running"))
	tr.HandleJobUpdate(t.Context(), "c1", jobUpdateMsg(t, "sess-a", "8", "running"))
	tr.HandleJobUpdate(t.Context(), "c1", jobUpdateMsg(t, "sess-a", "8", "completed"))

	var updates []marotte.BackgroundJobPayload
	for _, e := range *events {
		if e.Type == marotte.EventJobUpdate && e.ChatID == "c1" {
			updates = append(updates, e.Payload.(marotte.BackgroundJobPayload))
		}
	}
	if len(updates) != 3 || updates[2].ID != "8" || updates[2].Status != marotte.BackgroundJobCompleted {
		t.Fatalf("job_update events = %+v, want three, the last job 8 completed", updates)
	}
	running := tr.RunningJobs()["c1"]
	if running.Session != "sess-a" || len(running.Jobs) != 1 || running.Jobs[0].ID != "7" {
		t.Errorf("running = %+v, want job 7 on sess-a", running)
	}

	tr.HandleJobUpdate(t.Context(), "c1", jobUpdateMsg(t, "sess-b", "9", "running"))

	running = tr.RunningJobs()["c1"]
	if running.Session != "sess-b" || len(running.Jobs) != 1 || running.Jobs[0].ID != "9" {
		t.Errorf("running after the reload = %+v, want only sess-b's job 9", running)
	}
}

func focusActivityFrame(t *testing.T, activity map[string]any) json.RawMessage {
	t.Helper()
	return mustJSON(t, map[string]any{
		"sessionUpdate": "session_info_update",
		"_meta": map[string]any{"kiro": map[string]any{
			"kind": "focus_update", "status": "idle", "focus": map[string]any{"status": "idle"}, "activity": activity,
		}},
	})
}

// activity.unattended never reaches the turn lifecycle, and its false clears the rows a disposed session leaves.
func TestFocusUnattendedDoesNotHoldTurn(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	tr.HandleJobUpdate(t.Context(), "c1", jobUpdateMsg(t, "sess-a", "7", "running"))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", focusActivityFrame(t, map[string]any{
		"turnActive": false, "runningWorkflows": 0, "pausedWorkflows": 0, "unattended": true,
	}), FrameAttribution{})

	if len(deps.brackets) != 1 || deps.brackets[0].kind != "idle" {
		t.Fatalf("turn brackets = %+v, want one idle: a running job holds no turn open", deps.brackets)
	}
	if len(tr.RunningJobs()["c1"].Jobs) != 1 {
		t.Fatalf("running = %+v, want job 7 kept while unattended", tr.RunningJobs())
	}

	*events = nil
	tr.HandleSessionInfoUpdate(t.Context(), "c1", focusActivityFrame(t, map[string]any{
		"turnActive": false, "runningWorkflows": 0, "pausedWorkflows": 0, "unattended": false,
	}), FrameAttribution{})

	if got := tr.RunningJobs(); len(got) != 0 {
		t.Errorf("running = %+v, want none once KAS says no job runs", got)
	}
	var ended []marotte.BackgroundJobPayload
	for _, e := range *events {
		if e.Type == marotte.EventJobUpdate {
			ended = append(ended, e.Payload.(marotte.BackgroundJobPayload))
		}
	}
	if len(ended) != 1 || ended[0].ID != "7" || ended[0].Status != marotte.BackgroundJobOutcomeUnknown {
		t.Errorf("job_update events = %+v, want job 7 ended with no known outcome", ended)
	}
}

func contentChunk(t *testing.T, callID, text string) *marotte.RPCResponse {
	t.Helper()
	return &marotte.RPCResponse{Method: "_kiro/tools/content_chunk", Params: mustJSON(t, map[string]any{
		"sessionId": "", "toolCallId": callID,
		"content": map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": text}},
	})}
}

// A streamed chunk joins the open card by its toolCallId; the terminal update's content restates the stream, so the
// settled output holds it once (a content segment ends on a line break). A chunk naming no open call is dropped.
func TestContentChunkJoinsCardByToolCallID(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	tr.HandleToolCall(t.Context(), "c1", jobCreate(t), FrameAttribution{})
	*events = nil

	tr.HandleToolContentChunk(t.Context(), "c1", contentChunk(t, jobCallID, "a\n"))
	tr.HandleToolContentChunk(t.Context(), "c1", contentChunk(t, jobCallID, "b\n"))
	tr.HandleToolContentChunk(t.Context(), "c1", contentChunk(t, "tc-other", "x\n"))

	var deltas []string
	for _, e := range *events {
		if p, ok := e.Payload.(marotte.ToolProgressPayload); ok && e.Type == marotte.EventToolProgress {
			if p.ToolCallID != jobCallID {
				t.Errorf("tool_progress for %q, want only the joined card", p.ToolCallID)
			}
			deltas = append(deltas, p.OutputDelta)
		}
	}
	if !slices.Equal(deltas, []string{"a\n", "b\n"}) {
		t.Errorf("output deltas = %q, want the two chunks in order", deltas)
	}

	tr.HandleToolCallUpdate(t.Context(), "c1", mustJSON(t, map[string]any{
		"toolCallId": jobCallID, "status": "completed", "rawOutput": "a\nb\n",
		"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "a\nb"}}},
	}), FrameAttribution{})

	for _, e := range deps.chatEntries("c1") {
		if e.Kind == marotte.EntryKindToolResult {
			if res := decodePayload[marotte.EntryToolResult](t, &e); res.Output != "a\nb\n" {
				t.Errorf("settled output = %q, want the stream once", res.Output)
			}
			return
		}
	}
	t.Fatal("the call settled no tool_result")
}
