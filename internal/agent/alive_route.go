package agent

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/go/sse/v2"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
)

// presenceTable is the push presence table: hub connect/disconnect events plus keepalive acks. *push.Presence satisfies it.
type presenceTable interface {
	Observe(ev *sse.PresenceEvent)
	Alive(tag string)
}

// aliveInvalidCode is answered when the SSE-Client header is absent or outside the tag grammar.
const aliveInvalidCode httpserver.ErrorCode = "alive_invalid"

// forwardPresence hands every presence event to the table WithPresence wired, or drops it when none was.
func (b *bus) forwardPresence(ev *sse.PresenceEvent) {
	if b.presence != nil {
		b.presence.Observe(ev)
	}
}

// The tag is validated against the hub's WithClientTag grammar, so a hostile header puts no bytes
// in the table. Answers 204.
func (rt *Runtime) handleAlive(w http.ResponseWriter, r *http.Request) {
	tag, ok := reportTag(w, r)
	if !ok {
		return
	}
	if rt.bus.presence != nil {
		rt.bus.presence.Alive(tag)
	}
	w.WriteHeader(http.StatusNoContent)
}

// eventReportInvalidCode is answered when a view or notice report's body is malformed or out of grammar.
const eventReportInvalidCode httpserver.ErrorCode = "event_report_invalid"

const (
	// maxEventReportBytes caps a view or notice report body; a well-formed one is under 300 bytes.
	maxEventReportBytes = 1 << 10
	// maxRunTabRefBytes caps a run tab's ref in a view report, well above a workflow id.
	maxRunTabRefBytes = 256
)

// handleView records a page's view report (marotte.PageView) under its SSE-Client tag. Answers 204,
// or 400 on a bad tag or body.
func (rt *Runtime) handleView(w http.ResponseWriter, r *http.Request) {
	tag, ok := reportTag(w, r)
	if !ok {
		return
	}
	var v marotte.PageView
	if err := decodeEventReport(w, r, &v); err != nil || !validPageView(&v) {
		httpserver.WriteError(w, r, http.StatusBadRequest, eventReportInvalidCode, "malformed view report")
		return
	}
	if rt.push != nil {
		rt.push.View(tag, v)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleNotice records a page's report about a notice (marotte.NoticeReport) under its SSE-Client
// tag. Answers 204, or 400 on a bad tag or body.
func (rt *Runtime) handleNotice(w http.ResponseWriter, r *http.Request) {
	tag, ok := reportTag(w, r)
	if !ok {
		return
	}
	var n marotte.NoticeReport
	if err := decodeEventReport(w, r, &n); err != nil || !httpserver.ValidRequestID(n.ID) ||
		(n.Outcome != marotte.NoticeShown && n.Outcome != marotte.NoticeDeclined) {
		httpserver.WriteError(w, r, http.StatusBadRequest, eventReportInvalidCode, "malformed notice report")
		return
	}
	if rt.push == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if n.Outcome == marotte.NoticeShown {
		rt.push.Shown(tag, n.ID)
	} else {
		rt.push.Declined(tag, n.ID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// reportTag reads the SSE-Client tag a report is filed under, answering 400 when it is absent or out of grammar.
func reportTag(w http.ResponseWriter, r *http.Request) (string, bool) {
	tag := r.Header.Get(clientTagHeader)
	if !httpserver.ValidRequestID(tag) {
		httpserver.WriteError(w, r, http.StatusBadRequest, aliveInvalidCode,
			"SSE-Client header absent or outside [A-Za-z0-9_-]{1,64}")
		return "", false
	}
	return tag, true
}

// decodeEventReport decodes exactly one JSON object of known fields from a body of at most maxEventReportBytes.
func decodeEventReport(w http.ResponseWriter, r *http.Request, v any) error {
	httpserver.LimitBody(w, r, maxEventReportBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return httpserver.ErrTrailingData
	}
	return nil
}

// validPageView reports whether v names a page by id and, unless it is a close, a tab the ledger
// can match: a chat by its id or a run by a bounded ref, or none.
func validPageView(v *marotte.PageView) bool {
	if !httpserver.ValidRequestID(v.Page) {
		return false
	}
	if v.Closed || v.Tab == nil {
		return true
	}
	switch v.Tab.Kind {
	case marotte.TabKindChat:
		return ids.ValidChatID(v.Tab.Ref)
	case marotte.TabKindRun:
		return v.Tab.Ref != "" && len(v.Tab.Ref) <= maxRunTabRefBytes && utf8.ValidString(v.Tab.Ref)
	default:
		return false
	}
}
