package agent

// The background jobs' routes: the running list the jobs tracker and the chat list read, and a job's Stop and Show
// output, each sent on the chat's own session (KAS keys jobs by session).

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

// jobCallBudget bounds one job verb; KAS answers both from memory or a 128 KiB log tail.
const jobCallBudget = 15 * time.Second

// jobOutputLines is KAS's default and ceiling for _kiro/job/output's line count.
const (
	jobOutputDefaultLines = 100
	jobOutputMaxLines     = 1000
)

// rpcInvalidParams is JSON-RPC's invalid-params code, KAS's answer for a job id its session does not hold.
const rpcInvalidParams = -32602

// maxJobIDBytes bounds a job id in a path; KAS's ids are short.
const maxJobIDBytes = 256

func (rt *Runtime) registerJobRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/jobs", rt.handleJobs)
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

// handleJobCancel asks kiro-cli to stop a job and answers 202: the end arrives as job_update. A job kiro-cli no longer
// holds is dropped from the list.
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

// jobTarget validates the path and finds the chat's session; false once it answered.
func (rt *Runtime) jobTarget(w http.ResponseWriter, r *http.Request) (marotte.ChatID, string, *sharedBridge, bool) {
	id := r.PathValue("id")
	if !ids.ValidChatID(id) {
		httpreply.BadRequest(w, ids.ErrMsgInvalidChatID)
		return "", "", nil, false
	}
	jobID := r.PathValue("job")
	if jobID == "" || len(jobID) > maxJobIDBytes || strings.ContainsFunc(jobID, unicode.IsControl) || strings.TrimSpace(jobID) != jobID {
		httpreply.BadRequest(w, "The job id is not valid.")
		return "", "", nil, false
	}
	chatID := marotte.ChatID(id)
	sb := rt.coord.bridgeFor(chatID)
	if sb == nil || !sb.hasSession() {
		httpreply.Conflict(w, "The chat has no open session, so its jobs have ended.")
		return "", "", nil, false
	}
	return chatID, jobID, sb, true
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

// answerJobCallError shows kiro-cli's own refusal; one naming a job the session does not hold also drops its row.
func (rt *Runtime) answerJobCallError(w http.ResponseWriter, r *http.Request, chatID marotte.ChatID, jobID string, err error, failed string) {
	if rpc, ok := errors.AsType[*marotte.RPCError](err); ok {
		if rpc.Code == rpcInvalidParams {
			rt.translator.DropJob(r.Context(), chatID, jobID)
		}
		httpreply.Conflict(w, rpcerr.Text(rpc))
		return
	}
	if r.Context().Err() != nil {
		return
	}
	slog.Warn("background job: "+failed, "chat_id", chatID, "job_id", jobID, "error", err)
	httpserver.WriteJSONStatus(w, http.StatusBadGateway, httpreply.ErrorJSON(failed))
}
