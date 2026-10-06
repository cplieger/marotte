package agent

// Hooks state: list .kiro/hooks/*.json and flip their enabled flag over KAS's _kiro/hooks/*
// requests. Hooks are workspace-global, so they route through the utility bridge, which enables
// KAS's v2 hook engine (_meta.kiro.hooks={enabled,v2}); without it _kiro/hooks/list throws.
// Chat bridges enable the same engine, and KAS runs runCommand hooks itself.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/pathinside/v2"
	"github.com/cplieger/webhttp/v3"
)

// hookCallTimeout bounds a list or setEnabled round-trip; only the first call starts the utility bridge.
const hookCallTimeout = 45 * time.Second

// actionRunCommand / actionAskAgent are the two KAS hook action types.
const (
	actionRunCommand = "runCommand"
	actionAskAgent   = "askAgent"
)

type kasHookAction struct {
	Type    string `json:"type"` // runCommand | askAgent
	Command string `json:"command"`
	Prompt  string `json:"prompt"`
}

type kasHookMeta struct {
	Trigger        string `json:"trigger"`
	Source         string `json:"source"`
	Matcher        string `json:"matcher"`
	FilePath       string `json:"filePath"`
	DisabledReason string `json:"disabledReason"`
	Enabled        bool   `json:"enabled"`
}

type kasHook struct {
	ID     string        `json:"id"`
	Name   string        `json:"name"`
	Action kasHookAction `json:"action"`
	Meta   kasHookMeta   `json:"_meta"`
}

type kasHooksListResult struct {
	Hooks []kasHook `json:"hooks"`
}

// kasHookResult is setEnabled's {success, code?, error?} reply.
type kasHookResult struct {
	Code    string `json:"code"`
	Error   string `json:"error"`
	Success bool   `json:"success"`
}

// hookInfo is one hook in GET /api/hooks. ID is a base64url handle of the KAS id. Scope is
// "workspace" or "global" ($HOME/.kiro/hooks, kiro-cli 2.13+, derived from _meta.filePath).
// A global FilePath is a ~-prefixed display path only; filebrowse blocks that tree.
type hookInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Trigger    string `json:"trigger"`
	ActionType string `json:"action_type"` // runCommand | askAgent
	Scope      string `json:"scope"`       // workspace | global
	Command    string `json:"command,omitempty"`
	Prompt     string `json:"prompt,omitempty"`
	Matcher    string `json:"matcher,omitempty"`
	// MatcherWarning is derived by marotte.ClassifyHookMatcher: `missing_tool_matcher` for a tool
	// trigger without matcher, `ineffective` for a matcher on a trigger with nothing to match. It
	// covers hand-written files create_hook would refuse.
	MatcherWarning string `json:"matcher_warning,omitempty"`
	FilePath       string `json:"file_path,omitempty"`
	DisabledReason string `json:"disabled_reason,omitempty"`
	Enabled        bool   `json:"enabled"`
}

// Hook scopes for hookInfo.Scope.
const (
	hookScopeWorkspace = "workspace"
	hookScopeGlobal    = "global"
)

type hooksListResponse struct {
	Hooks []hookInfo `json:"hooks"`
}

// encodeHookID and decodeHookID map a KAS hook id (an absolute path plus "#hook-N") to a
// URL-safe base64url handle.
func encodeHookID(kasID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(kasID))
}

func decodeHookID(encoded string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// hookScopeAndPath classifies a KAS hook path: under the workspace it is workspace-relative,
// otherwise global, shown ~-prefixed under HOME. pathinside.RelEscapes keeps a "..drafts" directory workspace-scoped.
func (st *Settings) hookScopeAndPath(abs string) (scope, path string) {
	if abs == "" {
		return hookScopeWorkspace, ""
	}
	if rel, err := filepath.Rel(st.lifecycle.workDir, abs); err == nil && !pathinside.RelEscapes(rel) {
		return hookScopeWorkspace, filepath.ToSlash(rel)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, abs); err == nil && !pathinside.RelEscapes(rel) {
			return hookScopeGlobal, "~/" + filepath.ToSlash(rel)
		}
	}
	return hookScopeGlobal, abs
}

// hooksListRaw issues _kiro/hooks/list on the utility bridge, passing workspacePaths explicitly.
func (st *Settings) hooksListRaw(ctx context.Context) ([]kasHook, error) {
	u := st.utility()
	cctx, cancel := context.WithTimeout(ctx, hookCallTimeout)
	defer cancel()
	raw, err := u.session.hooksRaw(cctx, methodKiroHooksList, map[string]any{
		keyWorkspacePaths: []string{st.lifecycle.workDir},
		// includeDisabled, or a disabled hook vanishes and can never be re-enabled from the UI.
		"includeDisabled": true,
	})
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return []kasHook{}, nil
	}
	var res kasHooksListResult
	if uErr := json.Unmarshal(raw, &res); uErr != nil {
		return nil, uErr
	}
	return res.Hooks, nil
}

// toHookInfo flattens a KAS hook into the client-facing shape.
func (st *Settings) toHookInfo(k *kasHook) hookInfo {
	scope, path := st.hookScopeAndPath(k.Meta.FilePath)
	info := hookInfo{
		ID:             encodeHookID(k.ID),
		Name:           k.Name,
		Trigger:        k.Meta.Trigger,
		ActionType:     k.Action.Type,
		Scope:          scope,
		Matcher:        k.Meta.Matcher,
		MatcherWarning: string(marotte.ClassifyHookMatcher(k.Meta.Trigger, k.Meta.Matcher)),
		FilePath:       path,
		DisabledReason: k.Meta.DisabledReason,
		Enabled:        k.Meta.Enabled,
	}
	switch k.Action.Type {
	case actionRunCommand:
		info.Command = k.Action.Command
	case actionAskAgent:
		info.Prompt = k.Action.Prompt
	}
	return info
}

// handleHooksList serves GET /api/hooks, read-only.
func (st *Settings) handleHooksList(w http.ResponseWriter, r *http.Request) {
	hooks, err := st.hooksListRaw(r.Context())
	if err != nil {
		writeHookErr(w, err)
		return
	}
	out := make([]hookInfo, 0, len(hooks))
	for i := range hooks {
		out = append(out, st.toHookInfo(&hooks[i]))
	}
	// Workspace hooks first, stable within each group.
	slices.SortStableFunc(out, func(a, b hookInfo) int {
		return hookScopeRank(a.Scope) - hookScopeRank(b.Scope)
	})
	webhttp.WriteJSON(w, hooksListResponse{Hooks: out})
}

// hookScopeRank orders hook scopes for the dashboard: workspace first.
func hookScopeRank(scope string) int {
	if scope == hookScopeGlobal {
		return 1
	}
	return 0
}

type hookEnabledReq struct {
	Enabled bool `json:"enabled"`
}

// handleHookSetEnabled serves POST /api/hooks/{id}/enabled {enabled}; KAS persists the flag and
// hooks_changed is broadcast.
func (st *Settings) handleHookSetEnabled(w http.ResponseWriter, r *http.Request) {
	hookID, ok := hookIDFromPath(w, r)
	if !ok {
		return
	}
	var body hookEnabledReq
	if !httpreply.DecodeJSON(w, r, &body) {
		return
	}
	u := st.utility()
	cctx, cancel := context.WithTimeout(r.Context(), hookCallTimeout)
	defer cancel()
	raw, err := u.session.hooksRaw(cctx, methodKiroHooksSetEnabled, map[string]any{
		"hookId":  hookID,
		"enabled": body.Enabled,
	})
	if err != nil {
		writeHookErr(w, err)
		return
	}
	res := parseHookResult(raw)
	if !res.Success {
		writeHookResultErr(w, res)
		return
	}
	st.broadcastHooksChanged()
	webhttp.Ok(w)
}

// broadcastHooksChanged fans out hooks_changed after a create or toggle and on
// _kiro/hooks/didChange, which is how a hand-edited hook file reaches the UI.
func (st *Settings) broadcastHooksChanged() {
	st.broadcast(context.Background(), marotte.NewEvent(marotte.EventHooksChanged, "", marotte.HooksChangedPayload{}))
}

// hookIDFromPath decodes the {id} segment, writing a 400 and returning false on a malformed one.
func hookIDFromPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	hookID, err := decodeHookID(r.PathValue("id"))
	if err != nil || hookID == "" {
		httpreply.BadRequest(w, "invalid hook id")
		return "", false
	}
	return hookID, true
}

// parseHookResult decodes a {success, code?, error?} reply; an empty body is a failure.
func parseHookResult(raw json.RawMessage) kasHookResult {
	if len(raw) == 0 {
		return kasHookResult{}
	}
	var res kasHookResult
	if json.Unmarshal(raw, &res) != nil {
		return kasHookResult{}
	}
	return res
}

// writeHookResultErr maps a {success:false, code} reply to an HTTP status.
func writeHookResultErr(w http.ResponseWriter, res kasHookResult) {
	if res.Code == "hook_not_found" {
		httpreply.NotFound(w, "hook not found")
		return
	}
	msg := strings.TrimSpace(res.Error)
	if msg == "" {
		msg = "hook operation failed"
	}
	httpreply.BadRequest(w, msg)
}

// writeHookErr maps a bridge failure to 502 with a generic message. The utility bridge
// auto-starts, so there is no "open a chat first" case.
func writeHookErr(w http.ResponseWriter, err error) {
	slog.Warn("hooks op failed", "error", err)
	webhttp.WriteJSONStatus(w, http.StatusBadGateway, httpreply.ErrorJSON("hooks request failed"))
}

// registerHooksRoutes wires the hooks endpoints. There is deliberately no trigger route: it
// would restore the executeHook shell path.
func (st *Settings) registerHooksRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/hooks", st.handleHooksList)
	mux.HandleFunc("POST /api/hooks/{id}/enabled", st.handleHookSetEnabled)
}
