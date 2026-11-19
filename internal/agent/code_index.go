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

// codeIndexRoot is a root's shown status, the rank of its build's shown state, the latest startedAt accepted, and
// the ids of the builds refused from now on (ended, or started before started), oldest first.
type codeIndexRoot struct {
	started time.Time
	shown   marotte.CodeIndexStatusPayload
	ended   []string
	rank    int
}

// set keeps st, whose build started at started (zero when KAS sent none), as its root's latest and reports whether
// it changed what is shown. Every lane delivers its own copy of KAS's ordered sequence and build ids are random, so
// builds are ordered by startedAt, KAS running one build per store at a time. set refuses a frame of an ended
// build, an earlier state of the shown build, another build's frame that started before the latest accepted, and a
// build-less frame while any build is shown: KAS never clears its finished build, so that frame is a lagging copy.
// A frame without startedAt (queued, or canceled before it started) of a build not seen before is taken as newer.
func (c *codeIndexStatuses) set(st *marotte.CodeIndexStatusPayload, started time.Time) bool {
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
	return r.take(st, started)
}

// reset forgets every root.
func (c *codeIndexStatuses) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byRoot = nil
}

func (r *codeIndexRoot) take(st *marotte.CodeIndexStatusPayload, started time.Time) bool {
	shown, b := r.shown.Build, st.Build
	if b == nil {
		if shown != nil {
			return false
		}
		r.shown = *st
		return true
	}
	rank := codeIndexBuildRank(b)
	same := shown != nil && shown.BuildID == b.BuildID
	if slices.Contains(r.ended, b.BuildID) || (same && rank < r.rank) {
		return false
	}
	if !same && !started.IsZero() && started.Before(r.started) {
		r.refuse(b.BuildID)
		return false
	}
	if started.After(r.started) {
		r.started = started
	}
	if codeIndexBuildEnded(b.State) {
		r.refuse(b.BuildID)
	}
	r.shown, r.rank = *st, rank
	return true
}

// refuse remembers id among the last maxCodeIndexBuilds builds whose frames are refused.
func (r *codeIndexRoot) refuse(id string) {
	if len(r.ended) == maxCodeIndexBuilds {
		r.ended = slices.Delete(r.ended, 0, 1)
	}
	r.ended = append(r.ended, id)
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
		BuildID   string `json:"buildId"`
		State     string `json:"state"`
		Phase     string `json:"phase"`
		Message   string `json:"message"`
		Reason    string `json:"reason"`
		Detail    string `json:"detail"`
		StartedAt string `json:"startedAt"`
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
	var started time.Time
	if b := p.Build; b != nil {
		st.Build = &marotte.CodeIndexBuild{
			BuildID: b.BuildID, State: codeIndexText(b.State), Phase: codeIndexText(b.Phase),
			Message: codeIndexText(b.Message), Reason: codeIndexText(b.Reason), Detail: codeIndexText(b.Detail),
		}
		if t, err := time.Parse(time.RFC3339Nano, b.StartedAt); err == nil {
			started = t
		}
	}
	if rt.codeIndex.set(&st, started) {
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
