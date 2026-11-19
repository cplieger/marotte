// The code index's status and controls. KAS broadcasts _kiro/codeIntelligence/indexStatus to every session on a
// workspace root and replays it on session/new and attach, so the latest per root is held in memory only.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/go/textsafe"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
)

// codeIndexCallBudget bounds one Build or Cancel, its observer pass's door included.
const codeIndexCallBudget = time.Minute

// maxCodeIndexText bounds each line of kiro-cli's text the row shows.
const maxCodeIndexText = 512

// maxCodeIndexBuilds bounds the ended build ids a root remembers; a lane lagging further behind than this many
// builds could show a long-ended one again until the next frame arrives.
const maxCodeIndexBuilds = 16

// codeIndexStatuses is the latest status per workspace root.
type codeIndexStatuses struct {
	byRoot map[string]*codeIndexRoot
	mu     sync.Mutex
}

// codeIndexRoot is a root's shown status, the rank of its build's shown state, and the ids of the builds whose
// terminal frame was accepted, oldest first.
type codeIndexRoot struct {
	shown marotte.CodeIndexStatusPayload
	ended []string
	rank  int
}

// set keeps st as its root's latest and reports whether it changed what is shown. Every lane delivers its own copy
// of KAS's ordered sequence and build ids are random, so the order between builds is unknown: set refuses a frame of
// an ended build, an earlier state of the shown build, and a build-less frame while the shown build runs, and keeps
// any other build's frame. KAS sends a lane an older build's final frame before the latest build's, so the view
// converges on the latest build.
func (c *codeIndexStatuses) set(st *marotte.CodeIndexStatusPayload) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.byRoot[st.WorkspaceRoot]
	if r == nil {
		if c.byRoot == nil {
			c.byRoot = map[string]*codeIndexRoot{}
		}
		r = &codeIndexRoot{}
		c.byRoot[st.WorkspaceRoot] = r
	} else if codeIndexStatusEqual(&r.shown, st) {
		return false
	}
	return r.take(st)
}

// take shows st unless one of set's rules refuses it, and reports whether it did.
func (r *codeIndexRoot) take(st *marotte.CodeIndexStatusPayload) bool {
	shown, b := r.shown.Build, st.Build
	if b == nil {
		if shown != nil && !codeIndexBuildEnded(shown.State) {
			return false
		}
		r.shown = *st
		return true
	}
	rank := codeIndexBuildRank(b)
	if slices.Contains(r.ended, b.BuildID) || (shown != nil && shown.BuildID == b.BuildID && rank < r.rank) {
		return false
	}
	if codeIndexBuildEnded(b.State) {
		if len(r.ended) == maxCodeIndexBuilds {
			r.ended = slices.Delete(r.ended, 0, 1)
		}
		r.ended = append(r.ended, b.BuildID)
	}
	r.shown, r.rank = *st, rank
	return true
}

// codeIndexPhases are KAS's running phases (indexPhase), in the order a build passes them.
var codeIndexPhases = []string{"scan", "parse", "graph", "save"}

// codeIndexBuildRank orders a build's states: queued, then running by phase, then any terminal state.
func codeIndexBuildRank(b *marotte.CodeIndexBuild) int {
	switch {
	case codeIndexBuildEnded(b.State):
		return len(codeIndexPhases) + 1
	case b.State == codeIndexBuildQueued:
		return 0
	}
	return max(slices.Index(codeIndexPhases, b.Phase), 0) + 1
}

// view is every root's latest, sorted by root.
func (c *codeIndexStatuses) view() []marotte.CodeIndexStatusPayload {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]marotte.CodeIndexStatusPayload, 0, len(c.byRoot))
	for _, root := range slices.Sorted(maps.Keys(c.byRoot)) {
		out = append(out, c.byRoot[root].shown)
	}
	return out
}

// KAS's queued and terminal build states.
const (
	codeIndexBuildQueued    = "queued"
	codeIndexBuildSucceeded = "succeeded"
	codeIndexBuildFailed    = "failed"
	codeIndexBuildCanceled  = "canceled"
)

var codeIndexBuildEndStates = []string{codeIndexBuildSucceeded, codeIndexBuildFailed, codeIndexBuildCanceled}

func codeIndexBuildEnded(state string) bool {
	return slices.Contains(codeIndexBuildEndStates, state)
}

func codeIndexStatusEqual(a, b *marotte.CodeIndexStatusPayload) bool {
	if a.WorkspaceRoot != b.WorkspaceRoot || a.Store.State != b.Store.State ||
		(a.Store.Summary == nil) != (b.Store.Summary == nil) || (a.Build == nil) != (b.Build == nil) {
		return false
	}
	if a.Store.Summary != nil && *a.Store.Summary != *b.Store.Summary {
		return false
	}
	return a.Build == nil || *a.Build == *b.Build
}

// kasCodeIndexStatus is indexStatus's params.
type kasCodeIndexStatus struct {
	Build *struct {
		BuildID string `json:"buildId"`
		State   string `json:"state"`
		Phase   string `json:"phase"`
		Message string `json:"message"`
		Reason  string `json:"reason"`
		Detail  string `json:"detail"`
	} `json:"build"`
	Store struct {
		Summary *marotte.CodeIndexSummary `json:"summary"`
		State   string                    `json:"state"`
	} `json:"store"`
	WorkspaceRoot string `json:"workspaceRoot"`
}

func codeIndexText(s string) string {
	return textsafe.SanitizeSingleLineBounded(s, maxCodeIndexText)
}

// onCodeIndexStatus keeps a status and announces it when it changed what is shown.
func (rt *Runtime) onCodeIndexStatus(ctx context.Context, _ marotte.ChatID, msg *marotte.RPCResponse) {
	var p kasCodeIndexStatus
	if err := json.Unmarshal(msg.Params, &p); err != nil || p.WorkspaceRoot == "" {
		slog.Warn("code index status: kiro-cli sent one marotte cannot read, so the row keeps the last", "error", err)
		return
	}
	st := marotte.CodeIndexStatusPayload{
		WorkspaceRoot: filepath.Clean(p.WorkspaceRoot),
		Store:         marotte.CodeIndexStore{State: codeIndexText(p.Store.State), Summary: p.Store.Summary},
	}
	if b := p.Build; b != nil {
		st.Build = &marotte.CodeIndexBuild{
			BuildID: b.BuildID, State: codeIndexText(b.State), Phase: codeIndexText(b.Phase),
			Message: codeIndexText(b.Message), Reason: codeIndexText(b.Reason), Detail: codeIndexText(b.Detail),
		}
	}
	if rt.codeIndex.set(&st) {
		rt.Broadcast(ctx, marotte.NewEvent(marotte.EventCodeIndexStatus, "", st))
	}
}

func (rt *Runtime) handleCodeIndex(w http.ResponseWriter, _ *http.Request) {
	httpserver.WriteJSON(w, marotte.CodeIndexView{Root: filepath.Clean(rt.lifecycle.workDir), Statuses: rt.codeIndex.view()})
}

// handleCodeIndexBuild starts a build of the work dir's index with kiro-cli's default scope. The build is detached:
// it outlives the observer pass, and its progress arrives as statuses.
func (rt *Runtime) handleCodeIndexBuild(w http.ResponseWriter, r *http.Request) {
	err := rt.codeIndexCall(r.Context(), "code index build", methodKiroCodeIndexStartBuild, nil, nil)
	rt.answerCodeIndexCall(w, r, err, "kiro-cli did not start the build.")
}

type codeIndexCancelReq struct {
	BuildID string `json:"build_id"`
}

func (rt *Runtime) handleCodeIndexCancel(w http.ResponseWriter, r *http.Request) {
	var body codeIndexCancelReq
	if !httpreply.DecodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.BuildID) == "" {
		httpreply.BadRequest(w, "build_id names the build to cancel.")
		return
	}
	var out struct {
		Status string `json:"status"`
	}
	err := rt.codeIndexCall(r.Context(), "code index cancel", methodKiroCodeIndexCancelBuild, map[string]any{"buildId": body.BuildID}, &out)
	if err == nil && out.Status != "canceled" {
		httpserver.WriteJSONStatus(w, http.StatusConflict, httpreply.ErrorJSON("No index build is running."))
		return
	}
	rt.answerCodeIndexCall(w, r, err, "kiro-cli did not cancel the build.")
}

// codeIndexCall runs one verb on an observer pass and decodes its result into out when out is not nil.
func (rt *Runtime) codeIndexCall(ctx context.Context, label, method string, extra map[string]any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, codeIndexCallBudget)
	defer cancel()
	return rt.observe(ctx, func(ctx context.Context, s engineSession) error {
		raw, err := observerCall(ctx, s, label, method, extra)
		if err != nil || out == nil {
			return err
		}
		return json.Unmarshal(raw, out)
	})
}

// answerCodeIndexCall answers a verb: kiro-cli's own refusal (the switch is on but the engine has not restarted, no
// workspace) is shown as it wrote it.
func (*Runtime) answerCodeIndexCall(w http.ResponseWriter, r *http.Request, err error, failed string) {
	if err == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if rpc, ok := errors.AsType[*marotte.RPCError](err); ok {
		httpserver.WriteJSONStatus(w, http.StatusConflict, httpreply.ErrorJSON(rpcerr.Text(rpc)))
		return
	}
	if r.Context().Err() != nil {
		return
	}
	slog.Warn("code index: "+failed, "error", err)
	httpserver.WriteJSONStatus(w, http.StatusBadGateway, httpreply.ErrorJSON(failed))
}
