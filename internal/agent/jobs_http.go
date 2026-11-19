package agent

// The background jobs' routes: the running list the jobs tracker and the chat list read, Move to background on a
// running command's card, and a job's Stop and Show output, each sent on the chat's own session (KAS keys jobs by
// session).

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
	"github.com/cplieger/marotte/internal/sanitize"
	"github.com/cplieger/marotte/internal/translate"
)

// jobCallBudget bounds one job verb; KAS answers each from memory or a 128 KiB log tail.
const jobCallBudget = 15 * time.Second

// KAS's default and ceiling for _kiro/job/output's line count.
const (
	jobOutputDefaultLines = 100
	jobOutputMaxLines     = 1000
)

// rpcInvalidParams is JSON-RPC's invalid-params code, KAS's answer for a job id its session does not hold.
const rpcInvalidParams = -32602

// maxJobRefBytes bounds a job or tool call id; KAS's ids are short.
const maxJobRefBytes = 256

// jobMoveFailed is the answer when kiro-cli could not move the call or answered outside its outcomes.
const jobMoveFailed = "kiro-cli could not move the command to the background."

func (rt *Runtime) registerJobRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/jobs", rt.handleJobs)
	mux.HandleFunc("POST /api/chats/{id}/jobs", rt.handleJobMove)
	mux.HandleFunc("POST /api/chats/{id}/jobs/{job}/cancel", rt.handleJobCancel)
	mux.HandleFunc("GET /api/chats/{id}/jobs/{job}/output", rt.handleJobOutput)
}

// handleJobs answers every chat's running jobs whose session is still the chat's: a chat's jobs end with its session.
func (rt *Runtime) handleJobs(w http.ResponseWriter, _ *http.Request) {
	out := marotte.BackgroundJobsView{Jobs: []marotte.BackgroundJobPayload{}}
	for chatID, c := range rt.translator.RunningJobs() {
		sb := rt.coord.bridgeFor(chatID)
		if sb == nil || !sb.hasSession() || string(sb.SessionID()) != c.Session {
			continue
		}
		for _, j := range c.Jobs {
			j.ChatID = chatID
			out.Jobs = append(out.Jobs, j)
		}
	}
	slices.SortStableFunc(out.Jobs, func(a, b marotte.BackgroundJobPayload) int { return cmp.Compare(a.ChatID, b.ChatID) })
	httpserver.WriteJSON(w, out)
}

type kasJobCancelResult struct {
	Jobs []struct {
		JobID      string `json:"jobId"`
		WasRunning bool   `json:"wasRunning"`
	} `json:"jobs"`
}

type kasJobMoveResult struct {
	Outcome string `json:"outcome"`
	JobID   string `json:"jobId"`
}

// handleJobMove asks kiro-cli to move a running tool call into a job now. moved, ended and not_movable are kiro-cli's
// answers and answer 200; the moved card repaints through the frames kiro-cli sends for the move.
func (rt *Runtime) handleJobMove(w http.ResponseWriter, r *http.Request) {
	chatID, sb, ok := rt.chatSession(w, r)
	if !ok {
		return
	}
	var body marotte.BackgroundJobMoveRequest
	if !httpreply.DecodeJSON(w, r, &body) {
		return
	}
	if !validJobRef(body.ToolCallID) {
		httpreply.BadRequest(w, "The tool call id is not valid.")
		return
	}
	var out kasJobMoveResult
	err := jobCall(r.Context(), sb, methodKiroJobMove, map[string]any{"toolCallId": body.ToolCallID}, &out)
	if err != nil {
		writeJobCallError(w, r, err, jobMoveFailed, "chat_id", chatID, "tool_call_id", body.ToolCallID)
		return
	}
	switch outcome := marotte.BackgroundJobMoveOutcome(out.Outcome); outcome {
	case marotte.BackgroundJobMoved:
		if out.JobID != "" {
			httpserver.WriteJSON(w, marotte.BackgroundJobMove{Outcome: outcome, JobID: out.JobID})
			return
		}
	case marotte.BackgroundJobMoveEnded, marotte.BackgroundJobNotMovable:
		httpserver.WriteJSON(w, marotte.BackgroundJobMove{Outcome: outcome})
		return
	}
	slog.Warn("background job: "+jobMoveFailed, "chat_id", chatID, "tool_call_id", body.ToolCallID, "outcome", out.Outcome)
	httpserver.WriteJSONStatus(w, http.StatusBadGateway, httpreply.ErrorJSON(jobMoveFailed))
}

// handleJobCancel asks kiro-cli to stop a job and answers 202: the end arrives as job_update. A job kiro-cli no longer
// runs or holds is dropped from the list.
func (rt *Runtime) handleJobCancel(w http.ResponseWriter, r *http.Request) {
	chatID, jobID, sb, ok := rt.jobTarget(w, r)
	if !ok {
		return
	}
	var out kasJobCancelResult
	err := jobCall(r.Context(), sb, methodKiroJobCancel, map[string]any{"jobId": jobID}, &out)
	if err != nil {
		rt.answerJobCallError(w, r, chatID, jobID, err, "kiro-cli did not stop the job.")
		return
	}
	for _, j := range out.Jobs {
		if j.JobID == jobID && !j.WasRunning {
			rt.translator.DropJob(r.Context(), chatID, jobID)
			httpreply.Conflict(w, "The job had already ended.")
			return
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

type kasJobOutput struct {
	Gap    *json.RawMessage `json:"gap"`
	Status string           `json:"status"`
	Lines  []string         `json:"lines"`
}

// handleJobOutput answers a job's last lines; lines is clamped to 1..1000 before the call, 100 when absent.
func (rt *Runtime) handleJobOutput(w http.ResponseWriter, r *http.Request) {
	chatID, jobID, sb, ok := rt.jobTarget(w, r)
	if !ok {
		return
	}
	lines, ok := jobOutputLineCount(r.URL.Query().Get("lines"))
	if !ok {
		httpreply.BadRequest(w, "lines is a whole number.")
		return
	}
	var out kasJobOutput
	err := jobCall(r.Context(), sb, methodKiroJobOutput, map[string]any{"jobId": jobID, "lines": lines}, &out)
	if err != nil {
		rt.answerJobCallError(w, r, chatID, jobID, err, "kiro-cli did not read the job's output.")
		return
	}
	view := marotte.BackgroundJobOutput{
		Status: translate.JobState(out.Status),
		Lines:  make([]string, 0, len(out.Lines)),
		Gap:    out.Gap != nil,
	}
	for _, l := range out.Lines {
		view.Lines = append(view.Lines, sanitize.Output(l))
	}
	httpserver.WriteJSON(w, view)
}

func jobOutputLineCount(raw string) (int, bool) {
	if raw == "" {
		return jobOutputDefaultLines, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return min(max(n, 1), jobOutputMaxLines), true
}

// jobTarget validates the path's chat and job and finds the chat's session; false once it answered.
func (rt *Runtime) jobTarget(w http.ResponseWriter, r *http.Request) (marotte.ChatID, string, *sharedBridge, bool) {
	jobID := r.PathValue("job")
	if !validJobRef(jobID) {
		httpreply.BadRequest(w, "The job id is not valid.")
		return "", "", nil, false
	}
	chatID, sb, ok := rt.chatSession(w, r)
	return chatID, jobID, sb, ok
}

// chatSession validates the path's chat and finds its open session; false once it answered.
func (rt *Runtime) chatSession(w http.ResponseWriter, r *http.Request) (marotte.ChatID, *sharedBridge, bool) {
	id := r.PathValue("id")
	if !ids.ValidChatID(id) {
		httpreply.BadRequest(w, ids.ErrMsgInvalidChatID)
		return "", nil, false
	}
	chatID := marotte.ChatID(id)
	sb := rt.coord.bridgeFor(chatID)
	if sb == nil || !sb.hasSession() {
		httpreply.Conflict(w, "The chat has no open session, so its jobs have ended.")
		return "", nil, false
	}
	return chatID, sb, true
}

// validJobRef is a job or tool call id marotte passes to kiro-cli: non-empty, bounded, trimmed, no control characters.
func validJobRef(ref string) bool {
	return ref != "" && len(ref) <= maxJobRefBytes && !strings.ContainsFunc(ref, unicode.IsControl) &&
		strings.TrimSpace(ref) == ref
}

func jobCall(ctx context.Context, sb *sharedBridge, method string, extra map[string]any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, jobCallBudget)
	defer cancel()
	return sb.use(ctx, func(ctx context.Context, s engineSession) error {
		raw, err := observerCall(ctx, s, method, method, extra)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, out)
	})
}

// answerJobCallError answers a job verb's failed call; a refusal naming a job the session does not hold also drops
// its row.
func (rt *Runtime) answerJobCallError(w http.ResponseWriter, r *http.Request, chatID marotte.ChatID, jobID string, err error, failed string) {
	rpc := writeJobCallError(w, r, err, failed, "chat_id", chatID, "job_id", jobID)
	if rpc != nil && rpc.Code == rpcInvalidParams {
		rt.translator.DropJob(r.Context(), chatID, jobID)
	}
}

// writeJobCallError answers a failed job call: kiro-cli's own refusal as a 409, else 502 with failed, logged with
// logAttrs. It returns the refusal, nil for any other failure.
func writeJobCallError(w http.ResponseWriter, r *http.Request, err error, failed string, logAttrs ...any) *marotte.RPCError {
	if rpc, ok := errors.AsType[*marotte.RPCError](err); ok {
		httpreply.Conflict(w, rpcerr.Text(rpc))
		return rpc
	}
	if r.Context().Err() != nil {
		return nil
	}
	slog.Warn("background job: "+failed, append(logAttrs, "error", err)...)
	httpserver.WriteJSONStatus(w, http.StatusBadGateway, httpreply.ErrorJSON(failed))
	return nil
}
