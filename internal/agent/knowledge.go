package agent

// Knowledge-base management: list / add / remove workspace knowledge
// contexts over the v3 (KAS) _kiro/knowledge C→A request.
//
// The store is disk-backed at $KIRO_HOME/.kiro/knowledge_bases/default and
// shared by every kiro-cli process on the same KIRO_HOME, so these ops
// route through the long-lived UTILITY bridge — always available with no
// chat open — and _kiro/knowledge is issued WITHOUT a sessionId so it
// deterministically targets that global default store (a custom "wire"
// agent session would otherwise get its own in-memory store).
//
// Verified live against kiro-cli acp v3 2.12.0:
//   - subcommands: show / add / remove / update / clear / cancel; an
//     unknown subcommand returns {success:false, message:"Unknown
//     subcommand: …"}.
//   - `add` is ASYNC: returns {success:true, message:"Indexing … in
//     background"} immediately. Progress shows up in `show` as an entry
//     with indexing:true + items_display; the client polls `show` for
//     user-add progress since no notification path exists for it.

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

// knowledgeCallTimeout bounds one _kiro/knowledge round-trip. The only slow
// path is the first call, which lazily spins up the utility bridge.
const knowledgeCallTimeout = 45 * time.Second

// kasKnowledgeResult is the _kiro/knowledge reply. `show` fills Entries;
// add/remove/update/clear/cancel fill Message. Success is false for a
// usage error, a not-found remove target, or an unknown subcommand.
type kasKnowledgeResult struct {
	Message string              `json:"message"`
	Entries []kasKnowledgeEntry `json:"entries"`
	Success bool                `json:"success"`
}

// kasKnowledgeEntry is one entry in a `show` reply: either an indexed
// context (item_count + path, no indexing flag) or an in-flight operation
// (indexing true + items_display progress).
type kasKnowledgeEntry struct {
	Name         string `json:"name"`
	ID           string `json:"id"`
	Description  string `json:"description"`
	Path         string `json:"path"`
	ItemsDisplay string `json:"items_display"`
	ItemCount    int    `json:"item_count"`
	Indexing     bool   `json:"indexing"`
}

// knowledgeContext is one entry in the GET /api/knowledge response. Field
// set mirrors kasKnowledgeEntry; the client keys rows by ID and drives the
// live progress ring from Indexing + ItemsDisplay.
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

// knowledgeMessageResponse is the POST /api/knowledge (add) success body:
// the KAS background-indexing confirmation, surfaced verbatim.
type knowledgeMessageResponse struct {
	Message string `json:"message"`
}

// knowledgeCall lazily constructs the utility bridge and issues one
// _kiro/knowledge request with a bounded timeout. sessionId is
// intentionally never set (global default store).
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

// knowledgeShow lists the global store's contexts + any in-flight indexing
// operations.
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
	// kasKnowledgeEntry and knowledgeContext have identical field layout
	// (only json tags differ), so a direct conversion suffices.
	out := make([]knowledgeContext, 0, len(res.Entries))
	for _, e := range res.Entries {
		out = append(out, knowledgeContext(e))
	}
	return out, nil
}

// resolveKnowledgePath makes a user-supplied knowledge path absolute: a
// relative path resolves against the workspace dir; an absolute path is
// cleaned and used as-is.
func (st *Settings) resolveKnowledgePath(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(st.lifecycle.workDir, p)
}

// knowledgeNameUnaddressable reports why a name could never be addressed by
// the per-base routes, or "" when it can. Both of them take ONE path segment,
// and canonicalAPIPath 400s a request whose decoded path is not the path
// ServeMux would route it as, so "." and ".." are unreachable however they are
// encoded. A name holding "/" IS reachable as %2F (measured on go1.27.1: Go
// unescapes per segment, so the name arrives whole), but only while the client
// encodes it, no proxy decodes it, and the decoded form stays canonical — two
// of those three are outside marotte, so the name is refused instead.
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

// cleanKnowledgeMsg trims a KAS message for surfacing as an HTTP error,
// falling back to a generic sentinel when empty.
func cleanKnowledgeMsg(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "knowledge operation failed"
	}
	return s
}

// --- HTTP handlers (registered by registerKnowledgeRoutes) ---

// handleKnowledge dispatches the collection: GET lists, POST adds. Anything
// else answers 405 with an Allow header naming the two, which is what a
// per-method route pattern cannot do — ServeMux hands a refused method to the
// /api/ fallback, whose 404 sends the caller looking for a route that is
// registered and healthy and carries no Allow.
func (st *Settings) handleKnowledge(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		st.handleKnowledgeList(w, r)
	case http.MethodPost:
		st.handleKnowledgeAdd(w, r)
	default:
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

// handleKnowledgeOne dispatches one base by name: DELETE removes it.
func (st *Settings) handleKnowledgeOne(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		httpreply.MethodNotAllowed(w, http.MethodDelete)
		return
	}
	st.handleKnowledgeRemove(w, r)
}

// handleKnowledgeList: GET /api/knowledge → the global store's contexts +
// in-flight indexing operations. The client polls this while any entry is
// still indexing.
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

// handleKnowledgeAdd: POST /api/knowledge {path, name?} → start a background
// index of a directory. Name defaults to the path's base name. Returns the
// KAS confirmation message on success (200); a usage / bad-path failure is a
// 400 with the KAS message.
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
	// After the default, because a derived name is as unaddressable as a
	// supplied one and a stored row nothing can remove is worse than a refusal.
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

// handleKnowledgeRemove: DELETE /api/knowledge/{name} → drop a context by name
// (KAS matches path first, then name). A missing target is a 404.
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

// handleKnowledgeReindex: POST /api/knowledge/{name}/reindex → re-index a
// settled base from the directory it was added from. ASYNC like add, so the
// client polls the list for progress.
//
// The name is resolved to a path HERE because KAS's `update` requires `path`
// and matches it against sourcePath exactly — unlike `remove`, which accepts a
// name. Resolving in the client would leak that keying onto the wire and lose
// the 404 for a name nothing holds.
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

// knowledgeSourcePath answers the source path of the settled base carrying
// name, or "" when none does. An in-flight entry is skipped: its path is the one
// already being indexed, and re-indexing it would race that add.
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

// knowledgeMutate issues a mutating subcommand (add/remove/…) and parses the
// {success, message} reply.
func (st *Settings) knowledgeMutate(ctx context.Context, params map[string]any) (*kasKnowledgeResult, error) {
	raw, err := st.knowledgeCall(ctx, params)
	if err != nil {
		return nil, err
	}
	return parseKnowledgeResult(raw)
}

// writeKnowledgeErr maps a bridge / kiro-cli failure to 502 with a generic
// message (details logged, not leaked). No errNoLiveBridge case here: the
// utility bridge is auto-started, so a failure is a backend fault.
func writeKnowledgeErr(w http.ResponseWriter, err error) {
	slog.Warn("knowledge op failed", "error", err)
	webhttp.WriteJSONStatus(w, http.StatusBadGateway, httpreply.ErrorJSON("knowledge request failed"))
}

// registerKnowledgeRoutes wires the knowledge-base management endpoints. The
// patterns carry no method on purpose: each handler dispatches on r.Method so a
// refused one answers 405 + Allow, where a per-method pattern leaves it to the
// /api/ fallback's 404.
func (st *Settings) registerKnowledgeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/knowledge", st.handleKnowledge)
	mux.HandleFunc("/api/knowledge/{name}", st.handleKnowledgeOne)
	mux.HandleFunc("/api/knowledge/{name}/reindex", st.handleKnowledgeReindex)
}
