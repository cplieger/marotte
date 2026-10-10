package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/webhttp/v3"
)

// RegisterRoutes wires GET /api/chats (list) and GET /api/chats/{id} (one chat with a paged window of turns).
func (s *Store) RegisterRoutes(mux *http.ServeMux) {
	rt := newRouter(s)
	rt.register(mux)
}

func (rt *router) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	headers, stamp := rt.store.listStamped(r.Context())
	webhttp.WriteJSON(w, map[string]any{"chats": headers, keySubject: stamp})
}

// handleOne serves GET /api/chats/{id}?limit=<turns>&before=<turn_id> and routes /api/chats/{id}/<sub> requests.
func (rt *router) handleOne(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/chats/")
	if rest == "" || strings.HasPrefix(rest, "/") {
		httpreply.BadRequest(w, ids.ErrMsgInvalidChatID)
		return
	}
	if id, sub, ok := strings.Cut(rest, "/"); ok {
		rt.routeChatSubResource(w, r, marotte.ChatID(id), sub)
		return
	}
	rt.serveChatPage(w, r, marotte.ChatID(rest))
}

func (rt *router) routeChatSubResource(w http.ResponseWriter, r *http.Request, cid marotte.ChatID, sub string) {
	// The two addressed sub-resources: /tools/{toolCallID} and /turns/{turn}.
	if rest, ok := strings.CutPrefix(sub, "tools/"); ok {
		rt.handleToolCall(w, r, cid, rest)
		return
	}
	if rest, ok := strings.CutPrefix(sub, "turns/"); ok {
		rt.handleTurnRange(w, r, cid, rest)
		return
	}
	switch sub {
	case "export":
		rt.handleExport(w, r, cid)
	case "turns":
		rt.handleTurns(w, r, cid)
	case "search":
		rt.handleSearch(w, r, cid)
	default:
		httpreply.NotFound(w, "unknown chat sub-resource")
	}
}

// serveChatPage serves /api/chats/{id}: a window of whole turns, its open tails, the liveness verdict and the stamps
// certifying those bytes. `?limit=` counts turns; `?before=<turn_id>` pages older (no tails, the `chat` stamp only).
// Tool payloads are bounded to the preview budget with has_full; a page is never bounded by bytes, since turns are
// never split.
func (rt *router) serveChatPage(w http.ResponseWriter, r *http.Request, id marotte.ChatID) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	if !chatIDPattern(id) {
		httpreply.BadRequest(w, ids.ErrMsgInvalidChatID)
		return
	}
	before := r.URL.Query().Get("before")
	if before != "" && !ids.ValidMessageID(before) {
		httpreply.BadRequest(w, "invalid before turn id")
		return
	}
	page, ok, err := rt.store.page(r.Context(), id, parseLimitParam(r), before)
	if err != nil {
		if before != "" {
			// The one caller-caused failure: a cursor naming a turn this log lacks, which a rewind can cause mid-scroll.
			httpreply.BadRequest(w, "unknown before turn id")
			return
		}
		httpreply.ServerError(w, "chat read failed", fmt.Errorf("chat page %s: %w", logsafe.Field(string(id)), err))
		return
	}
	if !ok {
		httpreply.NotFound(w, errMsgChatNotFound)
		return
	}
	webhttp.WriteJSON(w, map[string]any{
		"chat":         page.Chat,
		keyEntries:     previewEntries(nonNilEntries(page.Entries)),
		"open_entries": page.OpenEntries,
		"has_more":     page.HasMore,
		"live":         page.Live,
		keySubject:     page.Subject,
		"draft":        page.Draft,
	})
}

// nonNilEntries returns entries, never nil: nil marshals as `null`, which the generated decoder rejects.
func nonNilEntries(entries []marotte.Entry) []marotte.Entry {
	if entries == nil {
		return []marotte.Entry{}
	}
	return entries
}

// handleTurnRange serves GET /api/chats/{id}/turns/{turn}?after=<seq>: one turn's entries past `after` plus its
// open tails, the client's repair read for a seq hole or an unknown turn. No `after` is the whole turn.
func (rt *router) handleTurnRange(w http.ResponseWriter, r *http.Request, chatID marotte.ChatID, turn string) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	if !chatIDPattern(chatID) {
		httpreply.BadRequest(w, ids.ErrMsgInvalidChatID)
		return
	}
	if !ids.ValidMessageID(turn) {
		httpreply.BadRequest(w, "invalid turn id")
		return
	}
	from, ok := parseAfterParam(r)
	if !ok {
		httpreply.BadRequest(w, "invalid after seq")
		return
	}
	page, err := rt.store.TurnPage(r.Context(), chatID, turn, from)
	if err != nil {
		if errors.Is(err, ErrChatNotFound) {
			httpreply.NotFound(w, errMsgChatNotFound)
			return
		}
		// Only a missing turn is the caller's 404; an unreadable log is the server's fault.
		if errors.Is(err, ErrTurnNotInLog) {
			httpreply.NotFound(w, "unknown turn")
			return
		}
		httpreply.ServerError(w, "chat read failed",
			fmt.Errorf("chat turn range %s/%s: %w", logsafe.Field(string(chatID)), logsafe.Field(turn), err))
		return
	}
	webhttp.WriteJSON(w, map[string]any{
		keyEntries:     previewEntries(nonNilEntries(page.Entries)),
		"open_entries": page.OpenEntries,
		keySubject:     page.Subject,
	})
}

// parseAfterParam reads ?after= as the seq the caller holds and returns the log's inclusive bound: after + 1, or 0
// when absent for the whole turn, turn_open (seq 0) included. Non-numeric, negative, or ceiling values (where +1 would
// wrap) are refused.
func parseAfterParam(r *http.Request) (from uint64, ok bool) {
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

// handleTurns serves GET /api/chats/{id}/turns: the rail index, one row per drawn turn, from the offset index.
// Server-side because the client holds only a paginated window.
func (rt *router) handleTurns(w http.ResponseWriter, r *http.Request, chatID marotte.ChatID) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	if !chatIDPattern(chatID) {
		httpreply.BadRequest(w, ids.ErrMsgInvalidChatID)
		return
	}
	rows, err := rt.store.railRows(r.Context(), chatID)
	if err != nil {
		if errors.Is(err, ErrChatNotFound) {
			httpreply.NotFound(w, errMsgChatNotFound)
			return
		}
		httpreply.ServerError(w, "chat read failed", fmt.Errorf("chat turns %s: %w", logsafe.Field(string(chatID)), err))
		return
	}
	if rows == nil {
		rows = []marotte.TurnSummary{}
	}
	webhttp.WriteJSON(w, map[string]any{"turns": rows})
}

// handleSearch serves GET /api/chats/{id}/search?q=: a lexical scan of the log's sealed entries, server-side for
// the same reason. Open entries are in the DOM, searched by the client, so the two counts are reported side by side.
func (rt *router) handleSearch(w http.ResponseWriter, r *http.Request, chatID marotte.ChatID) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	if !chatIDPattern(chatID) {
		httpreply.BadRequest(w, ids.ErrMsgInvalidChatID)
		return
	}
	entries, drawn, err := rt.store.searchable(r.Context(), chatID)
	if err != nil {
		if errors.Is(err, ErrChatNotFound) {
			httpreply.NotFound(w, errMsgChatNotFound)
			return
		}
		httpreply.ServerError(w, "chat read failed", fmt.Errorf("chat search %s: %w", logsafe.Field(string(chatID)), err))
		return
	}
	// Both halves of in-chat search share the match-case toggle.
	caseSensitive := r.URL.Query().Get("case") == "1"
	webhttp.WriteJSON(w, search(entries, drawn, r.URL.Query().Get("q"), caseSensitive))
}

// defaultPageTurns is a page's turns when no limit is named: a few screens, one read for the common chat.
const defaultPageTurns = 20

// The two envelope keys every page shares.
const (
	keyEntries = "entries"
	keySubject = "subject"
)

// Anything else gets the default.
func parseLimitParam(r *http.Request) int {
	return clampedQueryInt(r, "limit", defaultPageTurns, 1, 200)
}

// clampedQueryInt returns the named query parameter when it parses inside [lo, hi], else def. Out of range takes the
// default rather than clamping, so the caller does not keep believing its number.
func clampedQueryInt(r *http.Request, name string, def, lo, hi int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return def
	}
	return n
}

type exportFormat int

const (
	exportFormatMarkdown exportFormat = iota
	exportFormatJSON
)

// handleExport serves GET /api/chats/{id}/export?format=md|json as a download: Markdown (default) or the header plus
// every entry as JSON.
func (rt *router) handleExport(w http.ResponseWriter, r *http.Request, chatID marotte.ChatID) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	if !chatIDPattern(chatID) {
		httpreply.BadRequest(w, ids.ErrMsgInvalidChatID)
		return
	}
	format, ok := parseExportFormat(r.URL.Query().Get("format"))
	if !ok {
		httpreply.BadRequest(w, "unsupported export format (use md or json)")
		return
	}
	c, entries, found, err := rt.loadForExport(r.Context(), chatID)
	if err != nil {
		httpreply.ServerError(w, "chat read failed", fmt.Errorf("chat export %s: %w", logsafe.Field(string(chatID)), err))
		return
	}
	if !found {
		httpreply.NotFound(w, errMsgChatNotFound)
		return
	}
	if format == exportFormatJSON {
		w.Header().Set("Content-Disposition",
			dispositionAttachment(exportFilename(c.Name, string(chatID), ".json")))
		webhttp.WriteJSON(w, map[string]any{"chat": c, keyEntries: nonNilEntries(entries)})
		return
	}
	w.Header().Set("Content-Disposition",
		dispositionAttachment(exportFilename(c.Name, string(chatID), ".md")))
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	if _, err := io.WriteString(w, renderChatMarkdown(c, entries)); err != nil {
		slog.Debug("chat export: markdown write failed",
			"chat_id", logsafe.Field(string(chatID)), "error", err)
	}
}

// parseExportFormat maps ?format= to an exportFormat: absent/md/markdown is Markdown, json is JSON, anything else is
// rejected.
func parseExportFormat(v string) (exportFormat, bool) {
	switch strings.ToLower(v) {
	case "", "md", "markdown":
		return exportFormatMarkdown, true
	case "json":
		return exportFormatJSON, true
	default:
		return exportFormatMarkdown, false
	}
}

// False for a chat with no header.
func (rt *router) loadForExport(ctx context.Context, chatID marotte.ChatID) (*marotte.Chat, []marotte.Entry, bool, error) {
	c, ok := rt.store.Get(ctx, chatID)
	if !ok {
		return nil, nil, false, nil
	}
	entries, err := rt.store.All(ctx, chatID)
	if err != nil {
		if errors.Is(err, ErrChatNotFound) {
			return nil, nil, false, nil
		}
		return nil, nil, false, err
	}
	return c, entries, true, nil
}

func dispositionAttachment(filename string) string {
	return mime.FormatMediaType("attachment", map[string]string{"filename": filename})
}

// ExportDisposition is the attachment Content-Disposition for downloading chat name/id with ext appended
// (".kiro-session.zip"), on the transcript exports' filename rule.
func ExportDisposition(name string, id marotte.ChatID, ext string) string {
	return dispositionAttachment(exportFilename(name, string(id), ext))
}

// exportFilename builds a filesystem-safe "<name>-<id><ext>", else "<id><ext>", else "chat<ext>". The stem is
// rune-capped.
func exportFilename(name, id, ext string) string {
	const maxStem = 80
	stem := sanitizeFilenamePart(name)
	if r := []rune(stem); len(r) > maxStem {
		stem = strings.TrimSpace(string(r[:maxStem]))
	}
	safeID := sanitizeFilenamePart(id)
	switch {
	case stem == "" && safeID == "":
		return "chat" + ext
	case stem == "":
		return safeID + ext
	case safeID == "":
		return stem + ext
	default:
		return stem + "-" + safeID + ext
	}
}

func sanitizeFilenamePart(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f:
			b.WriteByte('_')
		case r == '"', r == '\\', r == '/', r == ':', r == '*',
			r == '?', r == '<', r == '>', r == '|':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}
