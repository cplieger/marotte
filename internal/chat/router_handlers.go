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

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/route"
)

// routeMux is what a route registration needs: the server's registrar, or a test's mux. An alias, so
// RegisterRoutes has the server's routeHandler signature.
type routeMux = interface{ Bind(route.Binding) }

// RegisterRoutes binds GET /api/chats (list), GET /api/chats/{id} (one chat with a paged window of turns) and
// the chat's read sub-resources.
func (s *Store) RegisterRoutes(m routeMux) {
	newRouter(s).register(m)
}

// List is GET /api/chats' body.
type List struct {
	Subject *marotte.SubjectStamp `json:"subject"`
	Chats   []marotte.ChatHeader  `json:"chats"`
}

// PageQuery is GET /api/chats/{id}'s query: how many turns, and the turn to page older from.
type PageQuery struct {
	Before string `json:"before,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// Page is GET /api/chats/{id}'s body.
type Page struct {
	Draft       string                  `json:"draft"`
	Entries     []marotte.Entry         `json:"entries"`
	OpenEntries []marotte.OpenEntry     `json:"open_entries"`
	Subject     []*marotte.SubjectStamp `json:"subject"`
	Chat        marotte.ChatHeader      `json:"chat"`
	HasMore     bool                    `json:"has_more"`
	Live        bool                    `json:"live"`
}

// TurnQuery is GET /api/chats/{id}/turns/{turn}'s query: the exclusive sequence to read after.
type TurnQuery struct {
	After string `json:"after,omitempty"`
}

// TurnRange is GET /api/chats/{id}/turns/{turn}'s body.
type TurnRange struct {
	Entries     []marotte.Entry         `json:"entries"`
	OpenEntries []marotte.OpenEntry     `json:"open_entries"`
	Subject     []*marotte.SubjectStamp `json:"subject"`
}

// Turns is GET /api/chats/{id}/turns' body.
type Turns struct {
	Turns []marotte.TurnSummary `json:"turns"`
}

// InChatSearchQuery is GET /api/chats/{id}/search's query; Case "1" matches case.
type InChatSearchQuery struct {
	Q    string `json:"q,omitempty"`
	Case string `json:"case,omitempty"`
}

// SearchAllQuery is GET /api/chats/search's query.
type SearchAllQuery struct {
	Q string `json:"q,omitempty"`
}

// ExportQuery is GET /api/chats/{id}/export's query: md (the default) or json.
type ExportQuery struct {
	Format string `json:"format,omitempty"`
}

// jsonExport is the json export's body.
type jsonExport struct {
	Chat    *marotte.Chat   `json:"chat"`
	Entries []marotte.Entry `json:"entries"`
}

func (rt *router) handleList(w http.ResponseWriter, r *http.Request) {
	headers, stamp := rt.store.listStamped(r.Context())
	httpserver.WriteJSON(w, List{Chats: headers, Subject: stamp})
}

// serveChatPage serves /api/chats/{id}: a window of whole turns, its open tails, the liveness verdict and the stamps
// certifying those bytes. `?limit=` counts turns; `?before=<turn_id>` pages older (no tails, the `chat` stamp only).
// Tool payloads are bounded to the preview budget with has_full; a page is at most `limit` whole turns, sized by
// pageBytes.
func (rt *router) serveChatPage(w http.ResponseWriter, r *http.Request) {
	id := marotte.ChatID(r.PathValue("id"))
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
	httpserver.WriteJSON(w, Page{
		Chat:        page.Chat,
		Entries:     previewEntries(nonNilEntries(page.Entries)),
		OpenEntries: page.OpenEntries,
		HasMore:     page.HasMore,
		Live:        page.Live,
		Subject:     page.Subject,
		Draft:       page.Draft,
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
func (rt *router) handleTurnRange(w http.ResponseWriter, r *http.Request) {
	chatID, turn := marotte.ChatID(r.PathValue("id")), r.PathValue("turn")
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
		if errors.Is(err, errTurnNotInLog) {
			httpreply.NotFound(w, "unknown turn")
			return
		}
		httpreply.ServerError(w, "chat read failed",
			fmt.Errorf("chat turn range %s/%s: %w", logsafe.Field(string(chatID)), logsafe.Field(turn), err))
		return
	}
	httpserver.WriteJSON(w, TurnRange{
		Entries:     previewEntries(nonNilEntries(page.Entries)),
		OpenEntries: page.OpenEntries,
		Subject:     page.Subject,
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
func (rt *router) handleTurns(w http.ResponseWriter, r *http.Request) {
	chatID := marotte.ChatID(r.PathValue("id"))
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
	httpserver.WriteJSON(w, Turns{Turns: rows})
}

// handleSearch serves GET /api/chats/{id}/search?q=: a lexical scan of the log's sealed entries, server-side for
// the same reason. Open entries are in the DOM, searched by the client, so the two counts are reported side by side.
func (rt *router) handleSearch(w http.ResponseWriter, r *http.Request) {
	chatID := marotte.ChatID(r.PathValue("id"))
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
	httpserver.WriteJSON(w, search(entries, drawn, r.URL.Query().Get("q"), caseSensitive))
}

// defaultPageTurns is a page's turns when no limit is named: a few screens, one read for the common chat.
const defaultPageTurns = 20

// Anything else gets the default.
func parseLimitParam(r *http.Request) int {
	return clampedInt(r.URL.Query().Get("limit"), defaultPageTurns, 1, 200)
}

// clampedInt returns v when it parses inside [lo, hi], else def. Out of range takes the default rather than
// clamping, so the caller does not keep believing its number.
func clampedInt(v string, def, lo, hi int) int {
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
func (rt *router) handleExport(w http.ResponseWriter, r *http.Request) {
	chatID := marotte.ChatID(r.PathValue("id"))
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
		httpserver.WriteJSON(w, jsonExport{Chat: c, Entries: nonNilEntries(entries)})
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
