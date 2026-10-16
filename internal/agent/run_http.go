package agent

// The run surface: two reads, and routes that forward to KAS's own control verbs. The run read
// passes `state` and `nodePlan` through verbatim.

// The step read loads a step's own KAS session and projects the replay: `inspect` carries only what a step declared.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
	"github.com/cplieger/marotte/internal/subject"
	"github.com/cplieger/marotte/internal/workflow"
	"github.com/cplieger/webhttp/v3"
)

// handleRun serves GET /api/runs/{workflowId}: the run's full state. It grades a failed read three ways,
// since the client treats 404 as final.
func (rr *runRoutes) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httpreply.BadRequest(w, "missing workflow id")
		return
	}
	read, err := rr.runs.positionedRead(r.Context(), id)
	if err != nil {
		writeRunReadErr(w, id, err)
		return
	}
	raw := read.raw
	position, err := read.position(id)
	if err != nil {
		slog.Warn("workflow inspect: the run's read position could not be computed",
			"workflow_id", logsafe.Field(id), "error", err)
	}
	var ends map[string]marotte.RunStepEnd
	var starts map[string]marotte.RunStepStart
	if rr.runs.log != nil {
		ends, err = rr.runs.log.stepEnds(r.Context(), id)
		if err != nil {
			slog.Warn("workflow inspect: the run log's step ends could not be read",
				"workflow_id", logsafe.Field(id), "error", err)
		}
		starts, err = rr.runs.log.stepStarts(r.Context(), id)
		if err != nil {
			slog.Warn("workflow inspect: the run log's step starts could not be read",
				"workflow_id", logsafe.Field(id), "error", err)
		}
	}
	var plan *marotte.RunPlanUpdate
	if u, ok := rr.runs.planUpdate(id); ok {
		plan = &u
	}
	out, err := withRunExtras(raw, runExtras{
		asks: read.asks, asksReachable: read.reachable, ends: ends, starts: starts, plan: plan, readPosition: position,
	})
	if err != nil {
		slog.Warn("workflow inspect: the reply could not carry the run's open asks, step ends, step starts and plan update",
			"workflow_id", logsafe.Field(id), "error", err)
		httpreply.WriteRawJSON(w, raw)
		return
	}
	httpreply.WriteRawJSON(w, out)
}

// writeInspectErr grades a failed `inspect` three ways, since a client treats 404 as final: the engine
// missing (503), the engine unreached (502), the engine answering that it has no such run (404).
func writeInspectErr(w http.ResponseWriter, id string, err error) {
	if errors.Is(err, workflow.ErrUnknownMethod) {
		slog.Warn("workflow inspect: engine not available on this kiro-cli",
			"workflow_id", logsafe.Field(id), "detail", rpcerr.Details(err))
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable,
			map[string]string{"error": "the workflow engine is not available on this kiro-cli build"})
		return
	}
	// Only an *marotte.RPCError is the engine answering; anything else says nothing about whether the run exists, so it is not a 404.
	if _, answered := errors.AsType[*marotte.RPCError](err); !answered {
		slog.Warn("workflow inspect did not reach the engine", "workflow_id", logsafe.Field(id),
			"error", err, "detail", rpcerr.Details(err))
		webhttp.WriteJSONStatus(w, http.StatusBadGateway,
			map[string]string{"error": "the workflow run could not be read"})
		return
	}
	slog.Warn("workflow inspect failed", "workflow_id", logsafe.Field(id),
		"error", err, "detail", rpcerr.Details(err))
	httpreply.NotFound(w, "workflow run not found")
}

// errRunUnhosted means no process holding the run could be reached, so neither its state nor its
// asks were read.
var errRunUnhosted = errors.New("the workflow run could not be read")

// runRead is GET /api/runs/{id}'s reads: the run's state, and its asks with whether they were read.
type runRead struct {
	raw       json.RawMessage
	asks      []marotte.RunAsk
	reachable bool
}

// openRun reads a leased run's state and asks together, through the process its lease records and
// never the utility, whose read can stamp it as the run's owner: one reply can then never pair a
// step paused on its question with no asks. Opening a dormant parentless run starts its carrier,
// closed again once the read shows the run stopped. An unleased run (a TUI launch) has no recorded
// host, so its state is the utility's and its asks are none, read only when the run is over.
func (rs *Runs) openRun(ctx context.Context, workflowID string) (runRead, error) {
	c := rs.cellFor(workflowID)
	if c == nil {
		raw, err := rs.rawInspect(ctx, workflowID)
		if err != nil {
			return runRead{}, err
		}
		in, dErr := workflow.DecodeInspect(workflowID, raw)
		return runRead{raw: raw, reachable: dErr == nil && marotte.RunStatus(in.State.Status).Terminal()}, nil
	}
	host, err := rs.acquireHost(ctx, workflowID, rs.leaseOrigin)
	if err != nil {
		rs.markUnreadable(ctx, c, nil)
		return runRead{}, fmt.Errorf("%w: %w", errRunUnhosted, err)
	}
	r, err := rs.readAsks(ctx, c, host.sb)
	host.release(nil)
	rs.closeIfStopped(host, r)
	switch {
	case err == nil:
		return runRead{raw: r.raw, asks: wireAsks(r.asks), reachable: true}, nil
	case r.raw != nil:
		// The host answered something this build cannot read: the state goes out as KAS gave it.
		return runRead{raw: r.raw, asks: rs.lastAsks(c), reachable: false}, nil
	}
	return runRead{}, err
}

func (rs *Runs) lastAsks(c *askCell) []marotte.RunAsk {
	rs.asks.mu.Lock()
	defer rs.asks.mu.Unlock()
	return c.last.Asks
}

// closeIfStopped ends a carrier a read started once that read shows its run not executing, or KAS
// answered it has no such run. Any other unreadable read says nothing about that, and closing a
// carrier kills the run it drives, so the carrier is kept and read again (rereadKeptCarrier).
func (rs *Runs) closeIfStopped(host *runHost, r askRead) {
	if !host.started {
		return
	}
	switch {
	case r.gone:
		rs.closeStoppedCarrier(host.chatID, host.workflowID, host.sb)
	case !r.in.OK:
		rs.rereadKeptCarrier(host.chatID, host.workflowID, host.sb)
	case r.in.State.Status != string(marotte.RunStatusRunning):
		rs.closeStoppedCarrier(host.chatID, host.workflowID, host.sb)
	}
}

// unreadCarrierReread is how long a carrier kept on an unreadable read waits before its run is read
// through it again. A var for tests.
var unreadCarrierReread = askRequery

// rereadKeptCarrier reads kept's run through kept after unreadCarrierReread, and decides again as
// closeIfStopped, while kept is still the run's carrier and the run still leased.
func (rs *Runs) rereadKeptCarrier(chatID marotte.ChatID, workflowID string, kept *sharedBridge) {
	time.AfterFunc(unreadCarrierReread, func() {
		rs.lifecycle.goUnlessDraining(func() {
			c := rs.cellFor(workflowID)
			if c == nil || rs.bridges.get(chatID) != kept {
				return
			}
			ctx, cancel := rs.lifecycle.derivedContext()
			defer cancel()
			rs.carriers.hold(kept)
			r, _ := rs.readAsks(ctx, c, kept)
			rs.carriers.leave(kept)
			rs.closeIfStopped(&runHost{rs: rs, sb: kept, chatID: chatID, workflowID: workflowID, started: true}, r)
		})
	})
}

// writeRunReadErr grades a failed run read; one that reached no host says its asks are unknown too.
func writeRunReadErr(w http.ResponseWriter, id string, err error) {
	if !errors.Is(err, errRunUnhosted) {
		writeInspectErr(w, id, err)
		return
	}
	slog.Warn("workflow inspect: no process holding the run could be reached",
		"workflow_id", logsafe.Field(id), "error", err)
	webhttp.WriteJSONStatus(w, http.StatusBadGateway, struct {
		Error         string `json:"error"`
		AsksReachable bool   `json:"asks_reachable"`
	}{Error: errRunUnhosted.Error()})
}

// runExtras are marotte's top-level keys on GET /api/runs/{id}.
type runExtras struct {
	ends          map[string]marotte.RunStepEnd
	starts        map[string]marotte.RunStepStart
	plan          *marotte.RunPlanUpdate
	asks          []marotte.RunAsk
	readPosition  uint64
	asksReachable bool
}

// runReadPosition is a run's read position: the turns its own log holds plus one once KAS reports
// it terminal, else 0. The plus one gives a run that settled with no step turn a position, so it
// lights once; a retry's step turns raise it, so a retried run lights again when it settles.
func runReadPosition(status marotte.RunStatus, turns uint64) uint64 {
	if !status.Terminal() {
		return 0
	}
	return turns + 1
}

// positionedRead is a run read with its log's turn count, taken BEFORE the state: a retry's step
// turn opened after a terminal state was read cannot pair with that state into a position no reply
// describes.
type positionedRead struct {
	turnsErr error
	runRead
	turns uint64
}

func (rs *Runs) positionedRead(ctx context.Context, workflowID string) (positionedRead, error) {
	var p positionedRead
	if rs.log != nil {
		p.turns, p.turnsErr = rs.log.turnHighWater(ctx, workflowID)
	}
	read, err := rs.openRun(ctx, workflowID)
	p.runRead = read
	return p, err
}

// position is the read's runReadPosition; an unreadable state or turn count is an error.
func (p *positionedRead) position(workflowID string) (uint64, error) {
	in, decodeErr := workflow.DecodeInspect(workflowID, p.raw)
	if err := errors.Join(decodeErr, p.turnsErr); err != nil {
		return 0, err
	}
	return runReadPosition(marotte.RunStatus(in.State.Status), p.turns), nil
}

// RunReadBound is the run's read position now, read as GET /api/runs/{id} reads it: the clamp for an
// acknowledgement (command.runOwner). It errors when the run's state or log cannot be read.
func (rs *Runs) RunReadBound(ctx context.Context, workflowID string) (uint64, error) {
	read, err := rs.positionedRead(ctx, workflowID)
	if err != nil {
		return 0, err
	}
	return read.position(workflowID)
}

// withRunExtras splices marotte's top-level keys into KAS's reply, decoding to raw values so future
// keys survive and nested values stay byte-identical: `open_asks`, `step_ends` and `step_starts`, never
// null because an agent could not tell "none" from "unsupported", `asks_reachable`, false when
// `open_asks` is not a statement that nothing waits, `read_position` (runReadPosition), and
// `plan_update` when a revision was seen.
func withRunExtras(raw json.RawMessage, x runExtras) (json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	asks, ends, starts, plan := x.asks, x.ends, x.starts, x.plan
	if asks == nil {
		asks = []marotte.RunAsk{}
	}
	if ends == nil {
		ends = map[string]marotte.RunStepEnd{}
	}
	if starts == nil {
		starts = map[string]marotte.RunStepStart{}
	}
	encodedAsks, err := json.Marshal(asks)
	if err != nil {
		return nil, err
	}
	encodedEnds, err := json.Marshal(ends)
	if err != nil {
		return nil, err
	}
	encodedStarts, err := json.Marshal(starts)
	if err != nil {
		return nil, err
	}
	if obj == nil {
		obj = make(map[string]json.RawMessage, 4)
	}
	obj["open_asks"] = encodedAsks
	if obj["asks_reachable"], err = json.Marshal(x.asksReachable); err != nil {
		return nil, err
	}
	obj["step_ends"] = encodedEnds
	obj["step_starts"] = encodedStarts
	if obj["read_position"], err = json.Marshal(x.readPosition); err != nil {
		return nil, err
	}
	if plan != nil {
		if obj["plan_update"], err = json.Marshal(plan); err != nil {
			return nil, err
		}
	}
	return json.Marshal(obj)
}

// handleStepTranscript serves GET /api/runs/{id}/steps/{path}: one step's transcript, {path} one
// percent-encoded workflow.PathKey. A step outside run {id} is a 404 (StepTranscript). Every 4xx is
// settled, so `gone` and `unavailable` are 200s.
func (rr *runRoutes) handleStepTranscript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httpreply.BadRequest(w, "missing workflow id")
		return
	}
	nodePath := r.PathValue("path")
	out, err := rr.runs.stepTranscript(r.Context(), id, nodePath)
	if err != nil {
		if errors.Is(err, errStepUnknown) {
			httpreply.NotFound(w, errStepUnknown.Error())
			return
		}
		slog.Warn("step transcript failed", "workflow_id", logsafe.Field(id), "node_path", logsafe.Field(nodePath),
			"error", err, "detail", rpcerr.Details(err))
		httpreply.InternalError(w, errors.New("step transcript unavailable"))
		return
	}
	webhttp.WriteJSON(w, out)
}

// handleTurnRange serves GET /api/runs/{id}/turns/{turn}?after=<seq>: one step turn's tail from the run's
// log, its open tails and its run_turn stamp. Addressed by turn: a parallel node holds several open turns.
// An unknown turn is 404.
func (rr *runRoutes) handleTurnRange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httpreply.BadRequest(w, "missing workflow id")
		return
	}
	turn := r.PathValue("turn")
	if turn == "" {
		httpreply.BadRequest(w, "missing turn id")
		return
	}
	from, ok := parseAfterSeq(r)
	if !ok {
		httpreply.BadRequest(w, "invalid after seq")
		return
	}
	entries, open, stamps, found, err := rr.runs.turnRange(r.Context(), id, turn, from)
	if err != nil {
		slog.Warn("run turn range failed", "workflow_id", logsafe.Field(id),
			"turn", logsafe.Field(turn), "error", err)
		httpreply.InternalError(w, errors.New("run turn unavailable"))
		return
	}
	if !found {
		httpreply.NotFound(w, "this run has no turn with that id")
		return
	}
	webhttp.WriteJSON(w, map[string]any{
		"entries":      nonNilRunEntries(entries),
		"open_entries": nonNilOpenEntries(open),
		"subject":      nonNilStamps(stamps),
	})
}

// parseAfterSeq maps the exclusive ?after= to the log's inclusive lower bound (after+1, or 0 for the
// whole turn including turn_open). Twin of the chat router's parseAfterParam. The type's ceiling is
// refused: it would wrap.
func parseAfterSeq(r *http.Request) (from uint64, ok bool) {
	v := r.URL.Query().Get("after")
	if v == "" {
		return 0, true
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil || n == math.MaxUint64 {
		return 0, false
	}
	return n + 1, true
}

// The three lists travel as `[]`: a null fails the client's array decode and loses the page.
func nonNilRunEntries(e []marotte.Entry) []marotte.Entry {
	if e == nil {
		return []marotte.Entry{}
	}
	return e
}

func nonNilOpenEntries(o []marotte.OpenEntry) []marotte.OpenEntry {
	if o == nil {
		return []marotte.OpenEntry{}
	}
	return o
}

func nonNilStamps(s []*marotte.SubjectStamp) []*marotte.SubjectStamp {
	if s == nil {
		return []*marotte.SubjectStamp{}
	}
	return s
}

// handleLiveRuns serves GET /api/runs/live: every live lease as `{workflow_id, chat_id, executing}`.
// Presence over local state, no KAS call; staleness errs to keeping.
func (rr *runRoutes) handleLiveRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	rows, stamp := rr.runs.liveRunRowsStamped()
	stamp.Epoch = rr.epoch()
	webhttp.WriteJSON(w, marotte.LiveRunsResponse{Runs: rows, Subject: stamp})
}

// liveRunRows projects every held lease, shared by this route and the SSE connect so they agree. Cannot fail.
func (rs *Runs) liveRunRows() []marotte.LiveRun {
	rows, _ := rs.liveRunRowsStamped()
	return rows
}

// liveRunRowsStamped is liveRunRows with the lease store's `runs` version, paired under its lock.
func (rs *Runs) liveRunRowsStamped() ([]marotte.LiveRun, *marotte.SubjectStamp) {
	held, version := rs.leaseStore().ListStamped()
	out := make([]marotte.LiveRun, 0, len(held))
	for i := range held {
		out = append(out, marotte.LiveRun{
			WorkflowID: held[i].WorkflowID,
			ChatID:     held[i].ChatID,
			Executing:  held[i].Bounded(),
		})
	}
	return out, marotte.NewSubjectStamp(string(subject.KindRuns), "", version)
}

// status reads one run's current status, "" for an unknown run (the caller's 404): control decisions use the status as of now.
func (rr *runRoutes) status(ctx context.Context, workflowID string) (string, error) {
	raw, err := rr.runs.rawInspect(ctx, workflowID)
	if err != nil {
		// Both mean no status to gate on.
		if errors.Is(err, workflow.ErrUnknownMethod) {
			return "", nil
		}
		return "", err
	}
	var res struct {
		State struct {
			Status string `json:"status"`
		} `json:"state"`
	}
	if uErr := json.Unmarshal(raw, &res); uErr != nil {
		return "", uErr
	}
	return res.State.Status, nil
}

// handleRecipes: GET /api/recipes → the launchable recipe list, bundled + workspace.
func (rr *runRoutes) handleRecipes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	recipes, err := rr.runs.listRecipes(r.Context())
	if err != nil {
		slog.Warn("recipe list failed", "error", err, "detail", rpcerr.Details(err))
		httpreply.InternalError(w, errors.New("recipe list unavailable"))
		return
	}
	webhttp.WriteJSON(w, marotte.RecipesResponse{Recipes: recipes})
}

// handleLaunch serves POST /api/runs: one parentless run, 409 when the recipe already has a live run.
func (rr *runRoutes) handleLaunch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	var req marotte.RunLaunchRequest
	if !httpreply.DecodeJSON(w, r, &req) {
		return
	}
	defaults := marotte.RunDefaults{Model: req.Model, Effort: req.Effort}
	if err := rr.runs.checkRunDefaults(r.Context(), defaults); writeDefaultsErr(w, err) {
		return
	}
	id, name, err := rr.runs.launch(r.Context(), req.Source, req.Inputs, defaults)
	if err != nil {
		if errors.Is(err, errRecipeBusy) {
			httpreply.Conflict(w, errRecipeBusy.Error())
			return
		}
		slog.Warn("run launch failed", "source", logsafe.Field(req.Source), "error", err, "detail", rpcerr.Details(err))
		// KAS's launch validation names the problem, and the fix is the user's.
		httpreply.BadRequest(w, rpcerr.Text(err))
		return
	}
	webhttp.WriteJSON(w, marotte.RunLaunchedResponse{WorkflowID: id, Name: name})
}

// The reply confirms the ask; the terminal frame follows the stop.
func (rr *runRoutes) handleCancel(w http.ResponseWriter, r *http.Request) {
	rr.controlHandler(w, r, runVerbCancel)
}

// handleControls serves GET /api/runs/{id}/controls: what may be done and why not
// (marotte.RunControlsResponse). Its own route so the passthrough stays verbatim.
func (rr *runRoutes) handleControls(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httpreply.BadRequest(w, "missing workflow id")
		return
	}
	status, err := rr.status(r.Context(), id)
	if err != nil {
		slog.Warn("run controls: status read failed",
			"workflow_id", logsafe.Field(id), "error", err, "detail", rpcerr.Details(err))
		httpreply.InternalError(w, errors.New("run controls unavailable"))
		return
	}
	if status == "" {
		httpreply.NotFound(w, "run not found")
		return
	}
	aff := rr.runs.affordance(r.Context(), id, status)
	webhttp.WriteJSON(w, marotte.RunControlsResponse{
		Verbs:        aff.Verbs,
		Refused:      aff.Refused,
		ParentChatID: string(aff.origin.parent.chat),
		PauseNodeID:  aff.PauseNodeID,
	})
}

func (rr *runRoutes) handleExtend(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		httpreply.BadRequest(w, "missing workflow id")
		return
	}
	var body marotte.RunExtendRequest
	if !httpreply.DecodeJSON(w, r, &body) {
		return
	}
	if body.NodeID == "" {
		httpreply.BadRequest(w, "missing node id")
		return
	}
	if body.Iterations < 1 || body.Iterations > maxExtendIterations {
		httpreply.BadRequest(w, "iterations must be between 1 and 1000")
		return
	}
	if _, ok := rr.permits(w, r, verbExtend, id); !ok {
		return
	}
	rr.writeRepeatResult(w, verbExtend, id,
		rr.runs.extendRepeat(r.Context(), id, body.NodeID, body.Iterations))
}

func (rr *runRoutes) handleFinishLoop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		httpreply.BadRequest(w, "missing workflow id")
		return
	}
	var body marotte.RunFinishLoopRequest
	if !httpreply.DecodeJSON(w, r, &body) {
		return
	}
	if body.NodeID == "" {
		httpreply.BadRequest(w, "missing node id")
		return
	}
	if _, ok := rr.permits(w, r, verbFinishLoop, id); !ok {
		return
	}
	rr.writeRepeatResult(w, verbFinishLoop, id, rr.runs.finishRepeat(r.Context(), id, body.NodeID))
}

// writeRepeatResult answers the repeat verbs: failed spawn 500, an {updated:false} decline 409 with KAS's reason.
func (rr *runRoutes) writeRepeatResult(w http.ResponseWriter, verb, id string, err error) {
	switch {
	case err == nil:
		webhttp.Ok(w)
	case errors.Is(err, errRunHostStart):
		slog.Warn("run control failed", "verb", verb, "workflow_id", logsafe.Field(id), "error", err)
		httpreply.InternalError(w, errors.New(verb+" failed"))
	case errors.Is(err, errStepStatusRefused):
		httpreply.Conflict(w, rpcerr.Text(err))
	default:
		rr.writeControlErr(w, verb, id, err)
	}
}

// handlePause and handleResume share cancel's shape; the affordance table gates them.
func (rr *runRoutes) handlePause(w http.ResponseWriter, r *http.Request) {
	rr.controlHandler(w, r, runVerbPause)
}

func (rr *runRoutes) handleResume(w http.ResponseWriter, r *http.Request) {
	rr.controlHandler(w, r, runVerbResume)
}

// handleRetry serves POST /api/runs/{id}/retry and reports what was reset, so its own handler: runVerb
// answers `error` alone.
func (rr *runRoutes) handleRetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httpreply.BadRequest(w, "missing workflow id")
		return
	}
	aff, ok := rr.permits(w, r, verbRetry, id)
	if !ok {
		return
	}
	// The gate's answer carries the parent chat the verb needs.
	out, err := rr.runs.retry(r.Context(), id, aff)
	if err != nil {
		rr.writeControlErr(w, verbRetry, id, err)
		return
	}
	webhttp.WriteJSON(w, out)
}

// handleDelete serves DELETE /api/runs/{id}: removes the run from KAS and drops marotte's lease, timer and reason. Unrecoverable.
func (rr *runRoutes) handleDelete(w http.ResponseWriter, r *http.Request) {
	rr.controlHandler(w, r, runVerbDelete)
}

// REST: a parentless run's ask has no chat id.
func (rr *runRoutes) handleAnswer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httpreply.BadRequest(w, "missing workflow id")
		return
	}
	var body marotte.RunAnswerRequest
	if !httpreply.DecodeJSON(w, r, &body) {
		return
	}
	err := rr.runs.answerInput(r.Context(), id, body.AskID, body.Text)
	switch {
	case err == nil:
		webhttp.Ok(w)
	case writeStatusErr(w, err):
	case errors.Is(err, errStepUnrecorded):
		// placeStepMessage logged the fault.
		httpreply.ServerError(w, errStepUnrecorded.Error(), nil)
	case errors.Is(err, errStepUnconfirmed):
		// sendStepWords logged the fault.
		httpreply.ServerError(w, errStepUnconfirmed.Error(), nil)
	case errors.Is(err, errRunNotListed):
		httpreply.NotFound(w, errRunNotListed.Error())
	case errors.Is(err, errLaunchSessionUnavailable):
		httpreply.Conflict(w, launchSessionUnavailableText)
	case errors.Is(err, errRunHostStart):
		// A failed spawn is this server's fault.
		slog.Warn("run answer: could not host the run", "workflow_id", logsafe.Field(id),
			"ask_id", logsafe.Field(body.AskID), "error", err)
		httpreply.InternalError(w, errors.New("answer failed"))
	default:
		slog.Warn("run answer failed", "workflow_id", logsafe.Field(id), "ask_id", logsafe.Field(body.AskID),
			"error", err, "detail", rpcerr.Details(err))
		httpreply.BadRequest(w, rpcerr.Text(err))
	}
}

// handleStepPost serves POST /api/runs/{id}/steps/{path}/{message,steer-remove,steer-clear}, {path}
// one percent-encoded workflow.PathKey. The workflow id and the node path are independent: the run's
// own inspect, read by each verb, decides whether the path is one of its steps.
func (rr *runRoutes) handleStepPost(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	nodePath, verb := r.PathValue("path"), r.PathValue("verb")
	if id == "" || nodePath == "" {
		httpreply.BadRequest(w, "missing workflow id or step path")
		return
	}
	switch verb {
	case "message":
		var body marotte.RunStepMessageRequest
		if !httpreply.DecodeJSON(w, r, &body) {
			return
		}
		out, err := rr.runs.messageStep(r.Context(), id, nodePath, body.Text, body.MessageID, body.AskID)
		if err != nil {
			rr.writeStepErr(w, "message", id, nodePath, err)
			return
		}
		webhttp.WriteJSON(w, out)
	case "steer-remove":
		var body marotte.RunStepSteerRequest
		if !httpreply.DecodeJSON(w, r, &body) {
			return
		}
		if err := rr.runs.removeStepSteer(r.Context(), id, nodePath, body.SteerID, body.Edit); err != nil {
			rr.writeStepErr(w, "steer remove", id, nodePath, err)
			return
		}
		webhttp.WriteJSON(w, map[string]string{"deleted": body.SteerID})
	case "steer-clear":
		rr.handleStepSteerClear(w, r, id, nodePath)
	default:
		// The step itself, which only GET reads.
		httpreply.MethodNotAllowed(w, http.MethodGet)
	}
}

// handleStepSteerClear discards every unread row of the step. The body is optional: with Edit set,
// SteerID names the row taken back to edit, which leaves no entry.
func (rr *runRoutes) handleStepSteerClear(w http.ResponseWriter, r *http.Request, id, nodePath string) {
	var body marotte.RunStepSteerRequest
	if r.ContentLength != 0 && !httpreply.DecodeJSON(w, r, &body) {
		return
	}
	edit := ""
	if body.Edit {
		edit = body.SteerID
	}
	if err := rr.runs.clearStepSteers(r.Context(), id, nodePath, edit); err != nil {
		rr.writeStepErr(w, "steer clear", id, nodePath, err)
		return
	}
	webhttp.Ok(w)
}

// writeStatusErr answers an error that names its own status, with its machine reason as `code`,
// reporting whether it did.
func writeStatusErr(w http.ResponseWriter, err error) bool {
	status, reason, ok := command.StatusOf(err)
	if !ok {
		return false
	}
	body := httpreply.ErrorJSON(rpcerr.Text(err))
	if reason != "" {
		// `code`, the key a REST client's error reads its machine reason from.
		body = httpreply.ErrorJSONWithCode(rpcerr.Text(err), reason)
	}
	webhttp.WriteJSONStatus(w, status, body)
	return true
}

// A refusal keeps its status and reason; a failed read or re-host is graded as the run verbs grade it.
func (rr *runRoutes) writeStepErr(w http.ResponseWriter, verb, id, nodePath string, err error) {
	if writeStatusErr(w, err) {
		return
	}
	switch {
	case errors.Is(err, errStepUnknown):
		httpreply.NotFound(w, errStepUnknown.Error())
	case errors.Is(err, errStepUnreadable):
		writeInspectErr(w, id, err)
	case errors.Is(err, errStepUnrecorded):
		// placeStepMessage logged the fault.
		httpreply.ServerError(w, errStepUnrecorded.Error(), nil)
	case errors.Is(err, errStepUnconfirmed):
		httpreply.ServerError(w, errStepUnconfirmed.Error(), nil)
	default:
		slog.Info("run step "+verb+" failed", "workflow_id", logsafe.Field(id), "node_path", logsafe.Field(nodePath))
		rr.writeControlErr(w, verb, id, err)
	}
}

// runVerb describes one run-control verb: how to issue it and its legal statuses. The gate exists because
// KAS refuses with -32603 throws; it is one trip stale.
type runVerb struct {
	name  string
	issue func(*Runs, context.Context, string) error
	// method is explicit on every verb.
	method string
	// from lists legal statuses; empty is unrestricted.
	from []marotte.RunStatus
}

var (
	runVerbCancel = runVerb{
		name: verbCancel,
		// Unrestricted: cancel is the tab-close gesture; KAS is idempotent on a terminal run.
		issue:  (*Runs).cancel,
		method: http.MethodPost,
	}
	runVerbPause = runVerb{
		name:   verbPause,
		issue:  (*Runs).pause,
		method: http.MethodPost,
		from:   []marotte.RunStatus{marotte.RunStatusRunning},
	}
	runVerbResume = runVerb{
		name:   verbResume,
		issue:  (*Runs).resume,
		method: http.MethodPost,
		from:   []marotte.RunStatus{marotte.RunStatusPaused},
	}
	// Unrestricted, and the only way a row leaves History.
	runVerbDelete = runVerb{
		name:   verbDelete,
		issue:  (*Runs).delete,
		method: http.MethodDelete,
	}
)

// verbDelete is the History row's delete, kept out of run_affordance.go's table since it is never drawn as a control.
const verbDelete = "delete"

// permits gates one verb on the run's affordance, writing the refusal (its sentence, else the status), and
// returns the affordance for the verb to act on.
func (rr *runRoutes) permits(
	w http.ResponseWriter, r *http.Request, verb, id string,
) (*runAffordance, bool) {
	status, err := rr.status(r.Context(), id)
	if err != nil {
		slog.Warn("run control: status read failed",
			"verb", verb, "workflow_id", logsafe.Field(id), "error", err, "detail", rpcerr.Details(err))
		httpreply.InternalError(w, errors.New(verb+" failed"))
		return nil, false
	}
	if status == "" {
		httpreply.NotFound(w, "run not found")
		return nil, false
	}
	aff := rr.runs.affordance(r.Context(), id, status)
	if aff.permits(verb) {
		return aff, true
	}
	slog.Info("run control refused", "verb", verb, "workflow_id", logsafe.Field(id), "status", status)
	if sentence := aff.refusal(verb); sentence != "" {
		httpreply.Conflict(w, sentence)
		return aff, false
	}
	httpreply.Conflict(w, verb+" is not available for a "+status+" run")
	return aff, false
}

const runClaimInFlightText = "Another action on this run is already under way. Refresh to see the result."

const deleteUnconfirmedText = "The delete was sent, but its answer was lost. " +
	"The run takes no messages until marotte confirms whether it is gone."

// writeControlErr answers a verb that reached KAS and failed: a refusal forwards KAS's actionable sentence;
// InternalError is for faults that are not the reader's.
func (*runRoutes) writeControlErr(w http.ResponseWriter, verb, id string, err error) {
	switch {
	case writeStatusErr(w, err):
	case errors.Is(err, errRunHostStart):
		// First: a re-host KAS refused wraps an *RPCError under this sentinel.
		slog.Warn("run control failed",
			"verb", verb, "workflow_id", logsafe.Field(id), "error", err, "detail", rpcerr.Details(err))
		httpreply.InternalError(w, errors.New(verb+" failed"))
	case errors.Is(err, errRunNotListed):
		httpreply.NotFound(w, errRunNotListed.Error())
	case errors.Is(err, errLaunchSessionUnavailable):
		// A state of the run.
		slog.Info("run control unavailable: the run's launching session cannot be opened here",
			"verb", verb, "workflow_id", logsafe.Field(id), "error", err)
		httpreply.Conflict(w, launchSessionUnavailableText)
	case errors.Is(err, errRetryEngineSlow):
		slog.Warn("run control timed out starting an engine", "verb", verb, "workflow_id", logsafe.Field(id))
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON(err.Error()))
	case errors.Is(err, errRunDeleting):
		httpreply.Conflict(w, "This run is already being deleted.")
	case errors.Is(err, errDeleteUnconfirmed):
		slog.Warn("run control sent but its answer was lost",
			"verb", verb, "workflow_id", logsafe.Field(id), "error", err)
		webhttp.WriteJSONStatus(w, http.StatusBadGateway, httpreply.ErrorJSON(deleteUnconfirmedText))
	case errors.Is(err, errRetryOutcomeUnreadable):
		// KAS accepted the verb and only its report is unusable: 502, sending the reader to a refresh.
		slog.Warn("run control landed but its report could not be read",
			"verb", verb, "workflow_id", logsafe.Field(id), "error", err)
		webhttp.WriteJSONStatus(w, http.StatusBadGateway,
			httpreply.ErrorJSON(errRetryOutcomeUnreadable.Error()))
	case errors.Is(err, workflow.ErrJustClaimed):
		// KAS says "another process", but usually it is a second verb on this carrier (the host lock is not held across the RPC).
		slog.Info("run control refused: another claim on the run is in flight",
			"verb", verb, "workflow_id", logsafe.Field(id), "detail", rpcerr.Details(err))
		httpreply.Conflict(w, runClaimInFlightText)
	case isRPCRefusal(err):
		// KAS's sentence names the reason, and the fix is often the reader's.
		slog.Info("run control refused by the workflow engine",
			"verb", verb, "workflow_id", logsafe.Field(id), "detail", rpcerr.Details(err))
		httpreply.Conflict(w, rpcerr.Text(err))
	default:
		slog.Warn("run control failed",
			"verb", verb, "workflow_id", logsafe.Field(id), "error", err, "detail", rpcerr.Details(err))
		httpreply.InternalError(w, errors.New(verb+" failed"))
	}
}

// isRPCRefusal reports whether the failure is KAS's answer rather than a local fault, at any wrapping depth.
func isRPCRefusal(err error) bool {
	_, ok := errors.AsType[*marotte.RPCError](err)
	return ok
}

func (rr *runRoutes) controlHandler(w http.ResponseWriter, r *http.Request, verb runVerb) {
	if r.Method != verb.method {
		httpreply.MethodNotAllowed(w, verb.method)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httpreply.BadRequest(w, "missing workflow id")
		return
	}
	if len(verb.from) > 0 {
		status, err := rr.status(r.Context(), id)
		if err != nil {
			slog.Warn("run control: status read failed",
				"verb", verb.name, "workflow_id", logsafe.Field(id), "error", err, "detail", rpcerr.Details(err))
			httpreply.InternalError(w, errors.New(verb.name+" failed"))
			return
		}
		if status == "" {
			httpreply.NotFound(w, "run not found")
			return
		}
		if !slices.Contains(verb.from, marotte.RunStatus(status)) {
			httpreply.Conflict(w, verb.name+" is not available for a "+status+" run")
			return
		}
	}
	if err := verb.issue(rr.runs, r.Context(), id); err != nil {
		rr.writeControlErr(w, verb.name, id, err)
		return
	}
	webhttp.Ok(w)
}
