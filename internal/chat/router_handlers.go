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

// RegisterRoutes wires GET /api/chats (list) and GET /api/chats/{id}
// (one chat with a paged window of turns).
func (s *Store) RegisterRoutes(mux *http.ServeMux) {
	rt := NewRouter(s)
	rt.Register(mux)
}

// handleList returns all chat headers.
func (rt *Router) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	headers, stamp := rt.store.ListStamped(r.Context())
	webhttp.WriteJSON(w, map[string]any{"chats": headers, keySubject: stamp})
}

// handleOne serves GET /api/chats/{id}?limit=<turns>&before=<turn_id> and routes
// /api/chats/{id}/<sub-resource> requests to their handlers.
func (rt *Router) handleOne(w http.ResponseWriter, r *http.Request) {
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

// routeChatSubResource dispatches /api/chats/{id}/<sub> to its handler.
func (rt *Router) routeChatSubResource(w http.ResponseWriter, r *http.Request, cid marotte.ChatID, sub string) {
	// The two sub-resources that are themselves addressed: /tools/{toolCallID} and
	// /turns/{turn}.
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

// serveChatPage serves the paged single-chat GET for /api/chats/{id}: a window of
// whole turns off the log, the open tails inside it, the registry's liveness
// verdict and the stamps certifying exactly those bytes. `?limit=` counts TURNS and
// `?before=<turn_id>` pages older (no tails, the `chat` stamp alone). Tool payloads
// are bounded to the PREVIEW budget with has_full marking what the bulk fetch
// holds; nothing bounds the page by bytes, because a turn is never split.
func (rt *Router) serveChatPage(w http.ResponseWriter, r *http.Request, id marotte.ChatID) {
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
	page, ok, err := rt.store.Page(r.Context(), id, parseLimitParam(r), before)
	if err != nil {
		if before != "" {
			// The one caller-caused failure: a cursor naming a turn this log does not
			// hold, which a rewind can produce under a reader mid-scroll.
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

// nonNilEntries is entries, never nil: a nil slice marshals as `null` and the
// generated decoder rejects `null` for an array.
func nonNilEntries(entries []marotte.Entry) []marotte.Entry {
	if entries == nil {
		return []marotte.Entry{}
	}
	return entries
}

// handleTurnRange serves GET /api/chats/{id}/turns/{turn}?after=<seq>: one turn's
// entries past `after` plus its open tails, the repair read a client runs on a seq
// hole or a frame naming a turn it has never seen. `after` omitted is the whole turn.
func (rt *Router) handleTurnRange(w http.ResponseWriter, r *http.Request, chatID marotte.ChatID, turn string) {
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
		// Only an id the log holds no turn for is the caller's answer. A log that cannot be
		// READ is this server's fault, and answering 404 for it told a client to stop asking
		// about a turn that exists.
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

// parseAfterParam reads ?after= as the seq the caller already holds and translates it
// to the log's INCLUSIVE lower bound: from = after + 1 when present, 0 when absent,
// which asks for the whole turn, turn_open included — no seq value can ask for that,
// since the turn_open IS seq 0. The wire spelling stays exclusive. Anything
// non-numeric, negative, or at the type's ceiling (where the translation would wrap
// back onto the whole turn) is refused.
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

// handleTurns serves GET /api/chats/{id}/turns: the rail index, one row per DRAWN
// turn and no bodies, read off the log's offset index. Server-side because the
// client's transcript store holds a paginated window, so a rail built from resident
// turns would grow markers as the reader scrolled up.
func (rt *Router) handleTurns(w http.ResponseWriter, r *http.Request, chatID marotte.ChatID) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	if !chatIDPattern(chatID) {
		httpreply.BadRequest(w, ids.ErrMsgInvalidChatID)
		return
	}
	rows, err := rt.store.RailRows(r.Context(), chatID)
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

// handleSearch serves GET /api/chats/{id}/search?q=: a session-wide lexical scan
// over the log's SEALED entries. Server-side because the client's store is a
// paginated window. An open entry's text is not searched: the DOM holds it, so the
// client's own pass covers it, which is why the two counts are reported side by
// side rather than subtracted.
func (rt *Router) handleSearch(w http.ResponseWriter, r *http.Request, chatID marotte.ChatID) {
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
	// Both halves of the in-chat search must agree on the match-case toggle.
	caseSensitive := r.URL.Query().Get("case") == "1"
	webhttp.WriteJSON(w, Search(entries, drawn, r.URL.Query().Get("q"), caseSensitive))
}

// defaultPageTurns is the turns a page carries when the caller names no limit: a
// few screens of an ordinary conversation, and one read for the common chat.
const defaultPageTurns = 20

// The two envelope keys every page shares; the rest are per handler.
const (
	keyEntries = "entries"
	keySubject = "subject"
)

// parseLimitParam returns the ?limit= page size in TURNS, honouring 1..200
// inclusive; anything else (absent, non-numeric, out of range) falls back to the
// default.
func parseLimitParam(r *http.Request) int {
	return clampedQueryInt(r, "limit", defaultPageTurns, 1, 200)
}

// clampedQueryInt returns the named query parameter when it parses as an integer
// inside the inclusive [lo, hi] range, and def for anything else. Out of range
// falls back to the DEFAULT rather than clamping, so a caller asking for something
// unserveable cannot keep believing the number it sent.
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

// exportFormat is the requested export serialization.
type exportFormat int

const (
	exportFormatMarkdown exportFormat = iota
	exportFormatJSON
)

// handleExport serves GET /api/chats/{id}/export?format=md|json as a downloadable
// Markdown transcript (the default) or the header plus every entry as JSON.
func (rt *Router) handleExport(w http.ResponseWriter, r *http.Request, chatID marotte.ChatID) {
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

// parseExportFormat maps ?format= to an exportFormat: absent/md/markdown to
// Markdown, json to raw JSON, anything else rejected so a typo fails loudly.
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

// loadForExport returns the chat's header and every entry of its log; false for a
// chat with no header.
func (rt *Router) loadForExport(ctx context.Context, chatID marotte.ChatID) (*marotte.Chat, []marotte.Entry, bool, error) {
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

// dispositionAttachment builds an attachment Content-Disposition value via
// mime.FormatMediaType, which escapes anything the sanitiser left in.
func dispositionAttachment(filename string) string {
	return mime.FormatMediaType("attachment", map[string]string{"filename": filename})
}

// exportFilename builds a filesystem-safe "<name>-<id><ext>", falling back to
// "<id><ext>" when the name is empty and "chat<ext>" when both are. The stem is
// rune-capped, so a very long chat title cannot produce an unwieldy filename.
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

// sanitizeFilenamePart replaces control and filename-unsafe characters with
// '_', then trims surrounding whitespace.
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
