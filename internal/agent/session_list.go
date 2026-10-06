package agent

// Previous-session picker: GET /api/sessions serves the workspace's KAS sessions over ACP `session/list`.
// The shell-out picker's `source` is a format tag ("v3"); ACP's `_meta.kiro.source` is a locality tag.

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/runesafe/v2"
	"github.com/cplieger/webhttp/v3"
)

// sessionListTimeout bounds the round-trip, which may start the utility bridge (configTemplateTimeout's value).
const sessionListTimeout = 45 * time.Second

// maxSessionDescBytes bounds the agent's self-declared description for the History row: 512, as translate's
// maxDisplayTextBytes, since it is the same upstream field read back from session.json.
const maxSessionDescBytes = 512

// kasSessionList is the session/list result shape.
type kasSessionList struct {
	Sessions []kasSessionRow `json:"sessions"`
}

// kasSessionRow is one stored session as session/list reports it.
type kasSessionRow struct {
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	Title     string `json:"title"`
	UpdatedAt string `json:"updatedAt"`
	Meta      struct {
		Kiro struct {
			CreatedAt string `json:"createdAt"`
			AgentMode string `json:"agentMode"`
			Status    string `json:"status"`
			// Description is the agent's self-declared "what I'm working on".
			Description string `json:"description"`
			// Workflow is present only on a workflow-step session: the discriminator.
			Workflow json.RawMessage `json:"workflow"`
		} `json:"kiro"`
	} `json:"_meta"`
}

// handleSessionList serves GET /api/sessions, newest first. A failure still answers 200 with an empty list,
// but says which list failed.
func (rt *Runtime) handleSessionList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	// Chats and runs are separate verbs, so each list gets its own verdict.
	claimed := rt.claimedSessions(r.Context())
	out := marotte.SessionListResponse{SessionsState: marotte.ReadReady, RunsState: marotte.ReadReady}
	rows, err := rt.resumableSessions(r.Context(), claimed)
	if err != nil {
		slog.Warn("session list failed", "error", err)
		rows = []marotte.ResumableSession{}
		out.SessionsState = marotte.ReadUnavailable
	}
	runs, rErr := rt.runs.list(r.Context(), claimed)
	if rErr != nil {
		slog.Warn("workflow run list failed", "error", rErr)
		runs = []marotte.WorkflowRun{}
		out.RunsState = marotte.ReadUnavailable
	}
	out.Sessions = rows
	out.Runs = runs
	webhttp.WriteJSON(w, out)
}

func (rt *Runtime) resumableSessions(ctx context.Context, claimed map[string]marotte.ChatID) ([]marotte.ResumableSession, error) {
	u := rt.utility.get()
	cctx, cancel := context.WithTimeout(ctx, sessionListTimeout)
	defer cancel()

	// cwd scopes the answer to this workspace; unscoped it returned 399 rows across 55 directories. The "user"
	// scope is that unscoped answer.
	raw, err := u.session.rawCall(cctx, "session list call", marotte.MethodSessionList,
		callerParams(map[string]any{"cwd": rt.lifecycle.workDir}))
	if err != nil {
		return nil, err
	}
	var list kasSessionList
	if uErr := json.Unmarshal(raw, &list); uErr != nil {
		return nil, uErr
	}
	return toResumable(claimed, list.Sessions), nil
}

// claimedSessions maps every KAS session a chat owns to that chat, keyed on the whole session chain so retired sessions stay owned.
func (rt *Runtime) claimedSessions(ctx context.Context) map[string]marotte.ChatID {
	claimed := map[string]marotte.ChatID{}
	// Indexed: marotte.ChatHeader is 304 bytes (gocritic rangeValCopy).
	headers := rt.chatStore.List(ctx)
	for i := range headers {
		for _, sid := range headers[i].SessionChain() {
			claimed[sid] = marotte.ChatID(headers[i].ID)
		}
	}
	return claimed
}

// toResumable keeps only rows a marotte chat claims, dropping step, utility and subagent sessions. The chat
// store decides what is offered; session/list whether KAS can resume it.
func toResumable(claimed map[string]marotte.ChatID, rows []kasSessionRow) []marotte.ResumableSession {
	out := make([]marotte.ResumableSession, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		if row.SessionID == "" {
			continue
		}
		if len(row.Meta.Kiro.Workflow) > 0 {
			continue
		}
		chatID := claimed[row.SessionID]
		if chatID == "" {
			continue
		}
		out = append(out, marotte.ResumableSession{
			SessionID:   row.SessionID,
			Title:       row.Title,
			UpdatedAt:   parseKASTime(row.UpdatedAt),
			CreatedAt:   parseKASTime(row.Meta.Kiro.CreatedAt),
			AgentMode:   row.Meta.Kiro.AgentMode,
			Status:      row.Meta.Kiro.Status,
			Description: runesafe.SanitizeSingleLineBounded(row.Meta.Kiro.Description, maxSessionDescBytes),
			ChatID:      string(chatID),
		})
	}
	out = collapseClaimedByChat(out)
	// Stable, or the list reshuffles between polls.
	slices.SortStableFunc(out, func(a, b marotte.ResumableSession) int {
		return cmp.Compare(b.UpdatedAt, a.UpdatedAt)
	})
	return out
}

// collapseClaimedByChat keeps one row per owning chat, the most recently updated.
func collapseClaimedByChat(rows []marotte.ResumableSession) []marotte.ResumableSession {
	newestFor := map[string]int{}
	drop := map[int]bool{}
	for i := range rows {
		chatID := rows[i].ChatID
		if chatID == "" {
			continue
		}
		best, seen := newestFor[chatID]
		if !seen {
			newestFor[chatID] = i
			continue
		}
		if livelierThan(&rows[i], &rows[best]) {
			newestFor[chatID] = i
			drop[best] = true
			continue
		}
		drop[i] = true
	}
	if len(drop) == 0 {
		return rows
	}
	kept := make([]marotte.ResumableSession, 0, len(rows)-len(drop))
	for i := range rows {
		if !drop[i] {
			kept = append(kept, rows[i])
		}
	}
	return kept
}

// livelierThan orders (UpdatedAt, CreatedAt, SessionID) descending; an UpdatedAt tie is reachable since parseKASTime sinks bad values to 0.
func livelierThan(a, b *marotte.ResumableSession) bool {
	return cmp.Or(
		cmp.Compare(a.UpdatedAt, b.UpdatedAt),
		cmp.Compare(a.CreatedAt, b.CreatedAt),
		cmp.Compare(a.SessionID, b.SessionID),
	) > 0
}

// parseKASTime converts KAS's RFC3339 timestamps to epoch millis; zero on absence or failure, so a bad value sinks.
func parseKASTime(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// Runs come from _kiro/workflow/list, not session/list, whose workflow rows are step sessions always reading idle.

// kasWorkflowRuns is the _kiro/workflow/list result.
type kasWorkflowRuns struct {
	Runs []kasWorkflowRun `json:"runs"`
}

// kasWorkflowRun is one run as _kiro/workflow/list reports it.
type kasWorkflowRun struct {
	WorkflowID string `json:"workflowId"`
	// Name is the display name, `runLabel ?? workflowName`; never key a recipe on it.
	Name string `json:"name"`
	// WorkflowName is the recipe, the only field a per-recipe decision may read.
	WorkflowName string `json:"workflowName"`
	// Status is run-level, unlike a step session's.
	Status    string `json:"status"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	StartedAt string `json:"startedAt"`
	// ParentSessionID is the launching session, how the run is attributed.
	ParentSessionID string `json:"parentSessionId"`
	// PauseKind is "maxIterations" for a repeat at its cap, and PauseNodeID names it; only paused runs carry them.
	PauseKind   string `json:"pauseKind"`
	PauseNodeID string `json:"pauseNodeId"`
	// PausePending is a pause asked for and not yet landed.
	PausePending bool `json:"pausePending"`
}

// list fetches the workspace's workflow runs, newest first.
func (rs *Runs) list(ctx context.Context, claimed map[string]marotte.ChatID) ([]marotte.WorkflowRun, error) {
	u := rs.utility()
	cctx, cancel := context.WithTimeout(ctx, sessionListTimeout)
	defer cancel()

	// workspacePaths is a required array (methodKiroWorkflowList).
	raw, err := u.session.rawCall(cctx, "workflow list call", methodKiroWorkflowList,
		callerParams(map[string]any{keyWorkspacePaths: []string{rs.lifecycle.workDir}}))
	if err != nil {
		return nil, err
	}
	var list kasWorkflowRuns
	if uErr := json.Unmarshal(raw, &list); uErr != nil {
		return nil, uErr
	}
	return rs.toWire(claimed, list.Runs), nil
}

// toWire maps the run inventory to wire rows, dropping what does not belong in history.
func (rs *Runs) toWire(claimed map[string]marotte.ChatID, runs []kasWorkflowRun) []marotte.WorkflowRun {
	out := make([]marotte.WorkflowRun, 0, len(runs))
	for i := range runs {
		r := &runs[i]
		if r.WorkflowID == "" {
			continue
		}
		// Attributed through the chain.
		parentChatID := string(claimed[r.ParentSessionID])
		out = append(out, marotte.WorkflowRun{
			WorkflowID:   r.WorkflowID,
			Name:         r.Name,
			WorkflowName: r.WorkflowName,
			Status:       marotte.RunStatus(r.Status),
			CreatedAt:    parseKASTime(r.CreatedAt),
			UpdatedAt:    parseKASTime(r.UpdatedAt),
			StartedAt:    parseKASTime(r.StartedAt),
			// Empty for a manual or scheduled run; the client reads it for nesting, the outcome glyph and Retry.
			ParentChatID: parentChatID,
			// KAS has no end-reason field, and bounds cancel like a person, so the deciding host supplies it.
			EndReason: rs.endReason(r.WorkflowID),
		})
	}
	// Stable, or the list reshuffles between polls.
	slices.SortStableFunc(out, func(a, b marotte.WorkflowRun) int {
		return cmp.Compare(b.UpdatedAt, a.UpdatedAt)
	})
	return out
}
