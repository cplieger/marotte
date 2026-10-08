package agent

// The run surface: two reads, and routes that forward to KAS's own control verbs. The run read
// passes `state` and `nodePlan` through verbatim.

// The step read loads a step's own KAS session and projects the replay: `inspect` carries only what a step declared.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
	"github.com/cplieger/marotte/internal/subject"
	"github.com/cplieger/marotte/internal/workflow"
	"github.com/cplieger/webhttp/v3"
)

// handleRun serves GET /api/runs/{workflowId}: the run's full state. It records the tree's step sessions
// (the only attribution recovery after a restart) and grades a failed read three ways, since the
// client treats 404 as final.
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
	raw, err := rr.runs.rawInspect(r.Context(), id)
	if err != nil {
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
		return
	}
	rr.runs.translate.RecordRunSteps(raw)
	// A run parked on a person with no ask here gets one reconstructed (the restart path); the response stays
	// verbatim and the ask travels on `run_input_needed`.
	rr.runs.reconcileNeedInput(r.Context(), id, raw)
	var ends map[string]marotte.RunStepEnd
	if rr.runs.log != nil {
		ends, err = rr.runs.log.StepEnds(r.Context(), id)
		if err != nil {
			slog.Warn("workflow inspect: the run log's step ends could not be read",
				"workflow_id", logsafe.Field(id), "error", err)
		}
	}
	// After the reconcile, which mints the restart-recovered ask.
	out, err := withRunLogFacts(raw, rr.runs.asks.SnapshotRun(id), ends)
	if err != nil {
		slog.Warn("workflow inspect: the reply could not carry the run's open asks and step ends",
			"workflow_id", logsafe.Field(id), "error", err)
		httpreply.WriteRawJSON(w, raw)
		return
	}
	httpreply.WriteRawJSON(w, out)
}

// withRunLogFacts splices the top-level `open_asks` and `step_ends` keys into KAS's reply, decoding to raw values
// so future keys survive and nested values stay byte-identical. Never null: an agent could not tell "none" from
// "unsupported".
func withRunLogFacts(raw json.RawMessage, asks []marotte.RunOpenAsk, ends map[string]marotte.RunStepEnd) (json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	if asks == nil {
		asks = []marotte.RunOpenAsk{}
	}
	if ends == nil {
		ends = map[string]marotte.RunStepEnd{}
	}
	encodedAsks, err := json.Marshal(asks)
	if err != nil {
		return nil, err
	}
	encodedEnds, err := json.Marshal(ends)
	if err != nil {
		return nil, err
	}
	if obj == nil {
		obj = make(map[string]json.RawMessage, 2)
	}
	obj["open_asks"] = encodedAsks
	obj["step_ends"] = encodedEnds
	return json.Marshal(obj)
}

// handleStepTranscript serves GET /api/runs/{id}/steps/{path...}: one step's transcript. The path must
// match the joined StepSession.Path and begin with the workflow id, so a node id containing `/` is
// not addressable. Every 4xx is settled, so `gone` and `unavailable` are 200s.
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
	if nodePath == "" {
		httpreply.BadRequest(w, "missing step path")
		return
	}
	if first, _, _ := strings.Cut(nodePath, "/"); first != id {
		httpreply.BadRequest(w, "the step path does not belong to this run")
		return
	}
	out, err := rr.runs.StepTranscript(r.Context(), id, nodePath)
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
	entries, open, stamps, found, err := rr.runs.TurnRange(r.Context(), id, turn, from)
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
	id, name, err := rr.runs.Launch(r.Context(), req.Source, req.Inputs)
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

// handleCancel serves POST /api/runs/{id}/cancel. The reply confirms the ask; the terminal frame follows the stop.
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
		rr.runs.ExtendRepeat(r.Context(), id, body.NodeID, body.Iterations))
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
	rr.writeRepeatResult(w, verbFinishLoop, id, rr.runs.FinishRepeat(r.Context(), id, body.NodeID))
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
	out, err := rr.runs.Retry(r.Context(), id, aff)
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

// handleStepStatus serves POST /api/runs/{id}/step: mark a step completed, failed or running. Its own handler: it has a body.
func (rr *runRoutes) handleStepStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httpreply.BadRequest(w, "missing workflow id")
		return
	}
	var body struct {
		NodeID string `json:"node_id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpreply.BadRequest(w, "invalid step-status payload")
		return
	}
	// errRunHostStart is a server fault (500), errStepStatusRefused and errLaunchSessionUnavailable are run states (409), anything else 400.
	err := rr.runs.SetStepStatus(r.Context(), id, body.NodeID, body.Status)
	switch {
	case err == nil:
		webhttp.Ok(w)
	case errors.Is(err, errRunNotListed):
		httpreply.NotFound(w, errRunNotListed.Error())
	case errors.Is(err, errLaunchSessionUnavailable):
		httpreply.Conflict(w, launchSessionUnavailableText)
	case errors.Is(err, errRunHostStart):
		slog.Warn("run step status: could not host the run", "workflow_id", logsafe.Field(id),
			"node_id", logsafe.Field(body.NodeID), "error", err)
		httpreply.InternalError(w, errors.New("step status update failed"))
	case errors.Is(err, errStepStatusRefused):
		httpreply.Conflict(w, rpcerr.Text(err))
	default:
		httpreply.BadRequest(w, rpcerr.Text(err))
	}
}

// handleAnswer serves POST /api/runs/{id}/answer. REST: a parentless run's ask has no chat id.
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
	err := rr.runs.AnswerInput(r.Context(), id, body.AskID, body.Text)
	switch {
	case err == nil:
		webhttp.Ok(w)
	case errors.Is(err, errAskAlreadySettled):
		// Another surface got there first.
		httpreply.Conflict(w, err.Error())
	case errors.Is(err, errRunNotParked):
		// The retryable refusal: the card is back, so the 409 carries the retry sentence.
		httpreply.Conflict(w, err.Error())
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
		issue:  (*Runs).Cancel,
		method: http.MethodPost,
	}
	runVerbPause = runVerb{
		name:   verbPause,
		issue:  (*Runs).Pause,
		method: http.MethodPost,
		from:   []marotte.RunStatus{marotte.RunStatusRunning},
	}
	runVerbResume = runVerb{
		name:   verbResume,
		issue:  (*Runs).Resume,
		method: http.MethodPost,
		from:   []marotte.RunStatus{marotte.RunStatusPaused},
	}
	// Unrestricted, and the only way a row leaves History.
	runVerbDelete = runVerb{
		name:   verbDelete,
		issue:  (*Runs).Delete,
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

// writeControlErr answers a verb that reached KAS and failed: a refusal forwards KAS's actionable sentence;
// InternalError is for faults that are not the reader's.
func (rr *runRoutes) writeControlErr(w http.ResponseWriter, verb, id string, err error) {
	switch {
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
