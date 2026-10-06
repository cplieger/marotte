package agent

// Knowledge-base management over KAS's _kiro/knowledge. The store lives at
// $KIRO_HOME/.kiro/knowledge_bases/default, shared by every kiro-cli on that KIRO_HOME, so calls
// go through the utility bridge WITHOUT a sessionId, which targets the global store. `add` is
// async (kiro-cli 2.12.0): progress appears in `show` as indexing:true, which the client polls.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/webhttp/v3"
)

// keySubcommand is the _kiro/knowledge dispatch field naming the operation.
const keySubcommand = "subcommand"

// knowledgeCallTimeout bounds one _kiro/knowledge round-trip; only the first call starts the utility bridge.
const knowledgeCallTimeout = 45 * time.Second

// kasKnowledgeResult is the _kiro/knowledge reply: `show` fills Entries, mutations fill Message.
// Success is false for a usage error, a missing remove target or an unknown subcommand.
type kasKnowledgeResult struct {
	Message string              `json:"message"`
	Entries []kasKnowledgeEntry `json:"entries"`
	Success bool                `json:"success"`
}

// kasKnowledgeEntry is one `show` entry: an indexed context, or an in-flight operation (indexing + items_display).
type kasKnowledgeEntry struct {
	Name         string `json:"name"`
	ID           string `json:"id"`
	Description  string `json:"description"`
	Path         string `json:"path"`
	ItemsDisplay string `json:"items_display"`
	ItemCount    int    `json:"item_count"`
	Indexing     bool   `json:"indexing"`
}

// knowledgeContext is one GET /api/knowledge entry; the client keys rows by ID and draws progress from Indexing and ItemsDisplay.
type knowledgeContext struct {
	Name         string `json:"name"`
	ID           string `json:"id"`
	Description  string `json:"description,omitempty"`
	Path         string `json:"path,omitempty"`
	ItemsDisplay string `json:"items_display,omitempty"`
	ItemCount    int    `json:"item_count"`
	Indexing     bool   `json:"indexing,omitempty"`
}

// knowledgeListResponse is the GET /api/knowledge body.
type knowledgeListResponse struct {
	Contexts []knowledgeContext `json:"contexts"`
}

// knowledgeMessageResponse is the POST /api/knowledge success body: KAS's confirmation, verbatim.
type knowledgeMessageResponse struct {
	Message string `json:"message"`
}

// knowledgeCall issues one bounded _kiro/knowledge request on the utility bridge, never with a sessionId.
func (st *Settings) knowledgeCall(ctx context.Context, params map[string]any) (json.RawMessage, error) {
	u := st.utility()
	cctx, cancel := context.WithTimeout(ctx, knowledgeCallTimeout)
	defer cancel()
	return u.session.knowledgeRaw(cctx, params)
}

// parseKnowledgeResult decodes the raw _kiro/knowledge JSON-RPC result.
func parseKnowledgeResult(raw json.RawMessage) (*kasKnowledgeResult, error) {
	if len(raw) == 0 {
		return nil, errors.New("knowledge: empty result")
	}
	var r kasKnowledgeResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// knowledgeShow lists the store's contexts and in-flight operations.
func (st *Settings) knowledgeShow(ctx context.Context) ([]knowledgeContext, error) {
	raw, err := st.knowledgeCall(ctx, map[string]any{keySubcommand: "show"})
	if err != nil {
		return nil, err
	}
	res, err := parseKnowledgeResult(raw)
	if err != nil {
		return nil, err
	}
	if !res.Success {
		return nil, errors.New(cleanKnowledgeMsg(res.Message))
	}
	// Identical field layout, so a direct conversion suffices.
	out := make([]knowledgeContext, 0, len(res.Entries))
	for _, e := range res.Entries {
		out = append(out, knowledgeContext(e))
	}
	return out, nil
}

// resolveKnowledgePath resolves a relative path against the workspace dir and cleans an absolute one.
func (st *Settings) resolveKnowledgePath(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(st.lifecycle.workDir, p)
}

// knowledgeNameUnaddressable reports why the per-base routes could never address a name, or "".
// "." and ".." are unroutable; a "/" survives only as %2F through every proxy, so it is refused.
func knowledgeNameUnaddressable(name string) string {
	switch {
	case name == "." || name == "..":
		return `a knowledge base cannot be named "." or "..": the request path would not be canonical, so no route can address it`
	case strings.ContainsRune(name, '/'):
		return `a knowledge base name cannot contain "/": the routes that address one take a single path segment`
	case strings.TrimSpace(name) != name:
		return "a knowledge base name cannot begin or end with whitespace: the routes that address one trim it, so the trimmed name would match nothing"
	}
	return ""
}

// cleanKnowledgeMsg trims a KAS message for an HTTP error, with a sentinel when empty.
func cleanKnowledgeMsg(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "knowledge operation failed"
	}
	return s
}

// handleKnowledge dispatches GET list, POST add, DELETE clear; anything else answers 405 with
// Allow, which a per-method pattern cannot (the /api/ fallback would 404).
func (st *Settings) handleKnowledge(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		st.handleKnowledgeList(w, r)
	case http.MethodPost:
		st.handleKnowledgeAdd(w, r)
	case http.MethodDelete:
		st.handleKnowledgeClear(w, r)
	default:
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodPost, http.MethodDelete)
	}
}

func (st *Settings) handleKnowledgeClear(w http.ResponseWriter, r *http.Request) {
	res, err := st.knowledgeMutate(r.Context(), map[string]any{keySubcommand: "clear"})
	if err != nil {
		writeKnowledgeErr(w, err)
		return
	}
	if !res.Success {
		httpreply.ServerError(w, "clear failed", errors.New(cleanKnowledgeMsg(res.Message)))
		return
	}
	webhttp.Ok(w)
}

// handleKnowledgeCancel resolves the name to the operation's short id, which only `show` reports.
func (st *Settings) handleKnowledgeCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		httpreply.BadRequest(w, "name required")
		return
	}
	opID, err := st.knowledgeOperationID(r.Context(), name)
	if err != nil {
		writeKnowledgeErr(w, err)
		return
	}
	if opID == "" {
		httpreply.NotFound(w, "no indexing in progress under that name")
		return
	}
	res, err := st.knowledgeMutate(r.Context(), map[string]any{
		keySubcommand: "cancel",
		"operationId": opID,
	})
	if err != nil {
		writeKnowledgeErr(w, err)
		return
	}
	if !res.Success {
		httpreply.NotFound(w, cleanKnowledgeMsg(res.Message))
		return
	}
	webhttp.WriteJSON(w, knowledgeMessageResponse{Message: res.Message})
}

func (st *Settings) knowledgeOperationID(ctx context.Context, name string) (string, error) {
	ctxs, err := st.knowledgeShow(ctx)
	if err != nil {
		return "", err
	}
	for _, c := range ctxs {
		if c.Name == name && c.Indexing && c.ID != "" {
			return c.ID, nil
		}
	}
	return "", nil
}

// handleKnowledgeOne dispatches one base by name: DELETE removes it.
func (st *Settings) handleKnowledgeOne(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		httpreply.MethodNotAllowed(w, http.MethodDelete)
		return
	}
	st.handleKnowledgeRemove(w, r)
}

// handleKnowledgeList serves GET /api/knowledge; the client polls it while anything indexes.
func (st *Settings) handleKnowledgeList(w http.ResponseWriter, r *http.Request) {
	ctxs, err := st.knowledgeShow(r.Context())
	if err != nil {
		writeKnowledgeErr(w, err)
		return
	}
	webhttp.WriteJSON(w, knowledgeListResponse{Contexts: ctxs})
}

type knowledgeAddReq struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

// handleKnowledgeAdd serves POST /api/knowledge {path, name?}: a background index, name defaulting
// to the base name. 200 with KAS's message, or 400 with it.
func (st *Settings) handleKnowledgeAdd(w http.ResponseWriter, r *http.Request) {
	var body knowledgeAddReq
	if !httpreply.DecodeJSON(w, r, &body) {
		return
	}
	path := strings.TrimSpace(body.Path)
	if path == "" {
		httpreply.BadRequest(w, "path required")
		return
	}
	abs := st.resolveKnowledgePath(path)
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = filepath.Base(abs)
	}
	// After the default: a stored row nothing can remove is worse than a refusal.
	if reason := knowledgeNameUnaddressable(name); reason != "" {
		httpreply.BadRequest(w, reason)
		return
	}
	res, err := st.knowledgeMutate(r.Context(), map[string]any{
		keySubcommand: "add",
		"name":        name,
		"path":        abs,
	})
	if err != nil {
		writeKnowledgeErr(w, err)
		return
	}
	if !res.Success {
		httpreply.BadRequest(w, cleanKnowledgeMsg(res.Message))
		return
	}
	webhttp.WriteJSON(w, knowledgeMessageResponse{Message: res.Message})
}

// handleKnowledgeRemove serves DELETE /api/knowledge/{name}; KAS matches path, then name. Missing is 404.
func (st *Settings) handleKnowledgeRemove(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		httpreply.BadRequest(w, "name required")
		return
	}
	res, err := st.knowledgeMutate(r.Context(), map[string]any{
		keySubcommand: "remove",
		"target":      name,
	})
	if err != nil {
		writeKnowledgeErr(w, err)
		return
	}
	if !res.Success {
		httpreply.NotFound(w, cleanKnowledgeMsg(res.Message))
		return
	}
	webhttp.Ok(w)
}

// handleKnowledgeReindex serves POST /api/knowledge/{name}/reindex, async like add. The name is
// resolved here because KAS's `update` matches `path` against sourcePath exactly.
func (st *Settings) handleKnowledgeReindex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		httpreply.BadRequest(w, "name required")
		return
	}
	path, err := st.knowledgeSourcePath(r.Context(), name)
	if err != nil {
		writeKnowledgeErr(w, err)
		return
	}
	if path == "" {
		httpreply.NotFound(w, "no indexed knowledge base under that name")
		return
	}
	res, err := st.knowledgeMutate(r.Context(), map[string]any{
		keySubcommand: "update",
		"path":        path,
	})
	if err != nil {
		writeKnowledgeErr(w, err)
		return
	}
	if !res.Success {
		httpreply.NotFound(w, cleanKnowledgeMsg(res.Message))
		return
	}
	webhttp.WriteJSON(w, knowledgeMessageResponse{Message: res.Message})
}

// knowledgeSourcePath answers the source path of the settled base named name, or "". In-flight
// entries are skipped: re-indexing would race their add.
func (st *Settings) knowledgeSourcePath(ctx context.Context, name string) (string, error) {
	ctxs, err := st.knowledgeShow(ctx)
	if err != nil {
		return "", err
	}
	for _, c := range ctxs {
		if c.Name == name && !c.Indexing && c.Path != "" {
			return c.Path, nil
		}
	}
	return "", nil
}

// knowledgeMutate issues a mutating subcommand and parses its {success, message} reply.
func (st *Settings) knowledgeMutate(ctx context.Context, params map[string]any) (*kasKnowledgeResult, error) {
	raw, err := st.knowledgeCall(ctx, params)
	if err != nil {
		return nil, err
	}
	return parseKnowledgeResult(raw)
}

// writeKnowledgeErr maps a bridge failure to 502 with a generic message.
func writeKnowledgeErr(w http.ResponseWriter, err error) {
	slog.Warn("knowledge op failed", "error", err)
	webhttp.WriteJSONStatus(w, http.StatusBadGateway, httpreply.ErrorJSON("knowledge request failed"))
}

// registerKnowledgeRoutes wires the knowledge endpoints. Patterns carry no method, so each
// handler answers a refused method with 405 + Allow.
func (st *Settings) registerKnowledgeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/knowledge", st.handleKnowledge)
	mux.HandleFunc("/api/knowledge/{name}", st.handleKnowledgeOne)
	mux.HandleFunc("/api/knowledge/{name}/reindex", st.handleKnowledgeReindex)
	mux.HandleFunc("/api/knowledge/{name}/cancel", st.handleKnowledgeCancel)
}
