package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

// throttleCode is the pauseDetail.code KAS stamps on a model throttle (ClientThrottleError).
const throttleCode = "ClientThrottleError"

// throttleRig is an agent-launched run KAS keeps pausing on a model throttle, with the pause and resume verbs
// marotte writes of it recorded.
type throttleRig struct {
	h      *Runtime
	br     *fakeBridge
	pauses []map[string]any
	// steers are the `_session/steer` calls marotte wrote, on any session.
	steers []map[string]any
	// resumes counts a resume once the run reads running again, so a later throttle cannot be overwritten by it.
	resumes atomic.Int32
	mu      sync.Mutex
}

func newThrottleRig(t *testing.T, leases *runlease.Store, opts ...Option) *throttleRig {
	t.Helper()
	prev := healBaseDelay
	healBaseDelay = time.Millisecond
	t.Cleanup(func() { healBaseDelay = prev })
	h, br := agentRunOnChat(t, gateRunID, opts...)
	t.Cleanup(func() { shutdownHub(t, h) })
	if leases != nil {
		h.runs.leases = leases
	}
	if _, held := h.runs.lease(gateRunID); !held {
		h.runs.grantLease(t.Context(), gateRunID, "publish", agentLaunch(), hostSec())
		h.runs.recordParentSession(t.Context(), gateRunID, parkSession)
	}
	g := &throttleRig{h: h, br: br}
	running := inspectReply(t, gateRunID, marotte.RunStatusRunning, "")
	br.mu.Lock()
	defer br.mu.Unlock()
	br.onCall = func(method string, params map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		switch method {
		case methodKiroWorkflowPause:
			g.mu.Lock()
			g.pauses = append(g.pauses, params)
			g.mu.Unlock()
			return json.RawMessage(`{"paused":true}`), nil, true
		case methodKiroWorkflowResume:
			br.setCallResult(methodKiroWorkflowInspect, running)
			g.resumes.Add(1)
			return json.RawMessage(`{}`), nil, true
		case marotte.MethodSessionSteer:
			g.mu.Lock()
			g.steers = append(g.steers, params)
			g.mu.Unlock()
			return json.RawMessage(`{"queued":true}`), nil, true
		}
		return nil, nil, false
	}
	return g
}

// throttled is KAS pausing the run on a model throttle: the run reads paused and the paused frame reaches the heal.
func (g *throttleRig) throttled(t *testing.T) {
	t.Helper()
	g.br.setCallResult(methodKiroWorkflowInspect, inspectPausedWithDetail(t, gateRunID, modelServiceReason,
		pauseDetail{Class: transientErrorClass, Code: throttleCode}))
	heal := g.h.runs.healPaused(func(context.Context, marotte.ChatID, *marotte.RPCResponse) {})
	heal(t.Context(), parkChat, runNotif(methodWFPaused, map[string]any{
		"workflowId": gateRunID, "pauseReason": modelServiceReason,
		"pauseDetail": map[string]any{"class": transientErrorClass, "code": throttleCode},
	}))
}

// stepFrame is a frame of the run's step session reaching the chat, as KAS streams it.
func (g *throttleRig) stepFrame(t *testing.T, update map[string]any) {
	t.Helper()
	update["_meta"] = map[string]any{"kiro": map[string]any{"workflow": map[string]any{
		"workflowId": gateRunID, "nodeId": "review", "nodePath": []string{gateRunID, "review"},
	}}}
	g.h.handleSessionUpdate(t.Context(), parkChat, stepUpdate(t, "step-session-a", update))
}

// streamed is the step producing model output again after a resume.
func (g *throttleRig) streamed(t *testing.T) {
	t.Helper()
	g.stepFrame(t, map[string]any{
		"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "working"},
	})
}

// metered is the step session's usage frame, which carries no model output.
func (g *throttleRig) metered(t *testing.T) {
	t.Helper()
	g.stepFrame(t, map[string]any{"sessionUpdate": "usage_update", "used": 10, "size": 1000})
}

func (g *throttleRig) steerParams() []map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]map[string]any(nil), g.steers...)
}

func (g *throttleRig) pauseParams() []map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]map[string]any(nil), g.pauses...)
}

// spend throttles the run past its budget with no progress between the pauses, waiting out each resume.
func (g *throttleRig) spend(t *testing.T) {
	t.Helper()
	for i := range int32(maxAutoHeals) {
		g.throttled(t)
		waitUntil(t, "the automatic resume", func() bool { return g.resumes.Load() == i+1 })
	}
	g.throttled(t)
	waitUntil(t, "the run's stop recorded", func() bool { return autoResumeStoppedAt(t, g.h) != "" })
}

// stoppedRead is GET /api/runs/{id}'s `auto_resume_stopped_at`, "" when absent, and its offered verbs.
type stoppedRead struct {
	AutoResumeStoppedAt string `json:"auto_resume_stopped_at"`
	Controls            struct {
		Verbs []string `json:"verbs"`
	} `json:"controls"`
}

func readStopped(t *testing.T, h *Runtime) stoppedRead {
	t.Helper()
	rec := httptest.NewRecorder()
	h.runRoutes.handleRun(rec, runReq(gateRunID))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/runs/%s = %d: %s", gateRunID, rec.Code, rec.Body.String())
	}
	var out stoppedRead
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding the run reply: %v", err)
	}
	return out
}

func autoResumeStoppedAt(t *testing.T, h *Runtime) string {
	t.Helper()
	return readStopped(t, h).AutoResumeStoppedAt
}

// A long step that keeps making progress between throttles is resumed after every one of them.
func TestHealBudget_ProgressBetweenThrottlesResumesEveryOne(t *testing.T) {
	g := newThrottleRig(t, nil)
	for i := range int32(maxAutoHeals + 1) {
		g.throttled(t)
		waitUntil(t, "the automatic resume", func() bool { return g.resumes.Load() >= i+1 })
		g.streamed(t)
	}
	awaitResumes(t, &g.resumes, maxAutoHeals+1)
	if p := g.pauseParams(); len(p) != 0 {
		t.Errorf("marotte wrote pauses %v of a run it resumes; KAS records each as the user's", p)
	}
}

// Throttles with no model output between them stop the automatic resumes after the budget, with no pause written:
// a pause marotte writes reads as the user's. A usage frame is not output.
func TestHealBudget_ThrottlesWithNoProgressStopAutoResumeWithNoPause(t *testing.T) {
	g := newThrottleRig(t, nil)
	for i := range int32(maxAutoHeals) {
		g.throttled(t)
		waitUntil(t, "the automatic resume", func() bool { return g.resumes.Load() == i+1 })
		g.metered(t)
	}
	g.throttled(t)
	g.throttled(t)
	awaitResumes(t, &g.resumes, maxAutoHeals)
	if p := g.pauseParams(); len(p) != 0 {
		t.Errorf("stopping the automatic resumes wrote pauses %v; KAS records each as the user's", p)
	}
	read := readStopped(t, g.h)
	if read.AutoResumeStoppedAt == "" {
		t.Error("the run read carries no auto_resume_stopped_at, so the run view cannot say marotte stopped resuming it")
	}
	if !slices.Contains(read.Controls.Verbs, runVerbResume.name) {
		t.Errorf("the stopped run offers verbs %v, want %q among them", read.Controls.Verbs, runVerbResume.name)
	}
}

// A budget spent by marotte's own failed resume writes, not by the run pausing again, stops the resumes with an
// ERROR naming those writes. Serial: it captures the process's logs.
func TestHealBudget_FailedResumeWritesStopAutoResumeLoudly(t *testing.T) {
	logs := captureLogs(t)
	g := newThrottleRig(t, nil)
	g.br.setCallErr(methodKiroWorkflowResume, errors.New("the engine did not answer"))
	for range maxAutoHeals + 1 {
		g.throttled(t)
	}
	waitUntil(t, "the run's stop recorded", func() bool { return autoResumeStoppedAt(t, g.h) != "" })

	var stops []string
	for line := range strings.Lines(logs.String()) {
		var rec struct{ Level, Msg string }
		if json.Unmarshal([]byte(line), &rec) == nil && strings.Contains(rec.Msg, "stopped resuming it") {
			stops = append(stops, rec.Level+" "+rec.Msg)
		}
	}
	if want := "ERROR " + healWritesSpentLog; !slices.Contains(stops, want) {
		t.Errorf("the stop logged %q, want %q", stops, want)
	}
	if p := g.pauseParams(); len(p) != 0 {
		t.Errorf("stopping the automatic resumes wrote pauses %v", p)
	}
}

// The stop is marotte's own record, so a marotte restart, which forgets the in-memory heal count, neither resumes
// the run nor forgets why it waits.
func TestHealBudget_TheStopSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	before, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	newThrottleRig(t, before).spend(t)

	reopened, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	g := newThrottleRig(t, reopened)
	g.br.setCallResult(methodKiroWorkflowInspect, inspectPausedWithDetail(t, gateRunID, modelServiceReason,
		pauseDetail{Class: transientErrorClass, Code: throttleCode}))
	g.h.runs.resumeIfInterrupted(t.Context(), parkChat, gateRunID)
	awaitResumes(t, &g.resumes, 0)
	if autoResumeStoppedAt(t, g.h) == "" {
		t.Error("after a restart the run read carries no auto_resume_stopped_at")
	}
	if p := g.pauseParams(); len(p) != 0 {
		t.Errorf("the restarted process wrote pauses %v", p)
	}
}

// stopFrame is a run-level `paused` frame the run's chat got, with whether the run read stopped as it was sent.
type stopFrame struct {
	chat    marotte.ChatID
	stopped bool
}

// stopFrames records the run-level `paused` frames marotte sends of gateRunID, then passes every event on.
type stopFrames struct {
	next   runBroadcaster
	rs     *Runs
	frames []stopFrame
	mu     sync.Mutex
}

func (b *stopFrames) Broadcast(ctx context.Context, evt marotte.ServerEvent) {
	if p, ok := evt.Payload.(marotte.RunProgressPayload); ok && evt.Type == marotte.EventRunProgress &&
		p.WorkflowID == gateRunID && p.Kind == marotte.RunProgressPaused && p.NodeID == "" {
		b.mu.Lock()
		b.frames = append(b.frames, stopFrame{chat: evt.ChatID, stopped: b.rs.autoResumeStopped(gateRunID)})
		b.mu.Unlock()
	}
	b.next.Broadcast(ctx, evt)
}

func (b *stopFrames) sent() []stopFrame {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.frames)
}

func watchStopFrames(g *throttleRig) *stopFrames {
	b := &stopFrames{next: g.h.runs.bus, rs: g.h.runs}
	g.h.runs.bus = b
	return b
}

// The stop lands after KAS's pause frame, so the run's chat gets a run-level `paused` frame once it is recorded: the
// run view reads the run again on it and shows marotte's stop instead of KAS's wording.
func TestHealBudget_TheStopIsAnnouncedOnceRecorded(t *testing.T) {
	g := newThrottleRig(t, nil)
	frames := watchStopFrames(g)
	g.spend(t)
	waitUntil(t, "the stop's frame", func() bool { return len(frames.sent()) > 0 })
	if got, want := frames.sent(), []stopFrame{{chat: parkChat, stopped: true}}; !slices.Equal(got, want) {
		t.Errorf("paused frames = %+v, want %+v", got, want)
	}
}

// A stop whose write to disk fails is still held in memory, and the run read shows it, so it is announced too.
// Serial: it captures the process's logs.
func TestHealBudget_AStopTheDiskRefusedIsStillAnnounced(t *testing.T) {
	logs := captureLogs(t)
	g := newThrottleRig(t, undurableLeaseStore(t))
	frames := watchStopFrames(g)
	g.spend(t)
	waitUntil(t, "the stop's frame", func() bool { return len(frames.sent()) > 0 })
	if got, want := frames.sent(), []stopFrame{{chat: parkChat, stopped: true}}; !slices.Equal(got, want) {
		t.Errorf("paused frames = %+v, want %+v", got, want)
	}
	if !strings.Contains(logs.String(), "a run marotte stopped resuming could not be recorded as such") {
		t.Error("the failed stop write logged no warning")
	}
}

// A person's pause of a stopped run keeps it paused, and KAS's frame for it can precede the cleared stop, so the
// clear is announced too. Called below the page's route, whose status gate refuses a pause of a run it reads paused:
// this is the pause whose gate read the run running just before the stop.
func TestHealBudget_APauseByHandOfAStoppedRunIsAnnouncedOnceCleared(t *testing.T) {
	g := newThrottleRig(t, nil)
	frames := watchStopFrames(g)
	g.spend(t)
	waitUntil(t, "the stop's frame", func() bool { return len(frames.sent()) > 0 })

	if err := g.h.runs.pause(t.Context(), gateRunID, callerPage); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if p := g.pauseParams(); len(p) != 1 {
		t.Fatalf("the person's pause wrote pauses %v, want one", p)
	}
	want := []stopFrame{{chat: parkChat, stopped: true}, {chat: parkChat, stopped: false}}
	if got := frames.sent(); !slices.Equal(got, want) {
		t.Errorf("paused frames = %+v, want %+v", got, want)
	}
}

// A person's resume ends the stop: the run read drops it, and the next throttle is resumed automatically again.
func TestHealBudget_AResumeByHandClearsTheStop(t *testing.T) {
	g := newThrottleRig(t, nil)
	g.spend(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/runs/"+gateRunID+"/resume", nil)
	req.SetPathValue("id", gateRunID)
	g.h.runRoutes.handleResume(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST resume = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := autoResumeStoppedAt(t, g.h); got != "" {
		t.Errorf("after a resume by hand auto_resume_stopped_at = %q, want absent", got)
	}

	g.throttled(t)
	awaitResumes(t, &g.resumes, maxAutoHeals+2)
	if p := g.pauseParams(); len(p) != 0 {
		t.Errorf("marotte wrote pauses %v", p)
	}
}

// A heal's resume whose answer could not be read may have re-driven the run: it is bounded as a resume that took,
// and the write is not counted among marotte's failed ones.
func TestHealBudget_AResumeWhoseAnswerWasUnreadBoundsTheRun(t *testing.T) {
	g := newThrottleRig(t, nil)
	g.br.setCallErr(methodKiroWorkflowResume, undecodedReply())

	g.throttled(t)

	waitUntil(t, "the run bounded after its unread resume", func() bool { return g.h.runs.bounded(gateRunID) })
	if g.h.runs.healWritesFailed(gateRunID) {
		t.Error("an unread resume was counted as a failed write, so a budget it spends is blamed on marotte")
	}
}
