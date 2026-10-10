// Package httpreply writes what marotte answers an HTTP request with: the
// named status responses, the error envelope they carry, and the request
// guards whose failure IS one of those responses.
//
// webhttp owns the headers, the status and the encode; this package owns
// marotte's error taxonomy. Every helper writes the bare {"error": "msg"}
// envelope marotte's clients decode, leaving webhttp.ErrorResponse's Code and
// RequestID empty, so a handler must use these helpers rather than hand-roll
// the envelope.
package httpreply

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/cplieger/webhttp/v3"
)

// jsonKeyError is the standard JSON error response key.
const jsonKeyError = "error"

// ErrorJSON returns the canonical error response map for JSON encoding.
// Use with webhttp.WriteJSONStatus to write error responses consistently.
func ErrorJSON(msg string) map[string]string {
	return map[string]string{jsonKeyError: msg}
}

// ErrorJSONWithCode returns an error response map with an additional
// machine-readable "code" field. Used by forge handlers that surface
// a stable error code alongside the human-readable message.
func ErrorJSONWithCode(msg, code string) map[string]string {
	return map[string]string{jsonKeyError: msg, "code": code}
}

// JSONKeyOutput is the standard JSON response key for successful
// command output. Used by git/ and server/ packages.
const JSONKeyOutput = "output"

// MIMETypeJSON is the standard MIME type for JSON content.
const MIMETypeJSON = "application/json"

const msgInternalError = "internal error"

// WriteRawJSON writes pre-marshalled JSON bytes with the standard
// Content-Type + X-Content-Type-Options headers. Use for pass-through
// of cached command replies and upstream JSON bodies (e.g. kiro-cli
// slash-command results) where json.Marshal would be a redundant
// round-trip. Write errors are best-effort (client may have hung up).
func WriteRawJSON(w http.ResponseWriter, data []byte) {
	webhttp.JSONHeaders(w)
	if _, err := w.Write(data); err != nil {
		slog.Debug("httpreply: raw json write failed", "error", err)
	}
}

// Named error responses: the bare {"error": "msg"} shape with the right status and Content-Type,
// for handlers to use instead of http.Error.

// BadRequest writes a 400 with {"error": msg}.
func BadRequest(w http.ResponseWriter, msg string) {
	webhttp.WriteJSONStatus(w, http.StatusBadRequest, webhttp.ErrorResponse{Error: msg})
}

// Forbidden writes a 403 with {"error": msg}.
func Forbidden(w http.ResponseWriter, msg string) {
	webhttp.WriteJSONStatus(w, http.StatusForbidden, webhttp.ErrorResponse{Error: msg})
}

// NotFound writes a 404 with {"error": msg}.
func NotFound(w http.ResponseWriter, msg string) {
	webhttp.WriteJSONStatus(w, http.StatusNotFound, webhttp.ErrorResponse{Error: msg})
}

// Conflict writes a 409 with {"error": msg}.
func Conflict(w http.ResponseWriter, msg string) {
	webhttp.WriteJSONStatus(w, http.StatusConflict, webhttp.ErrorResponse{Error: msg})
}

// headerAllow is the response-header name RFC 9110 §10.2.1 reserves for the
// set of methods a resource supports.
const headerAllow = "Allow"

// setAllow sets the Allow header to the methods the addressed resource supports (RFC 9110 §10.2.1):
// tokens verbatim (case-sensitive, §9.1), empty arguments dropped (§5.6.1.1), joined with ", ".
func setAllow(w http.ResponseWriter, method string, more ...string) {
	methods := make([]string, 0, 1+len(more))
	if method != "" {
		methods = append(methods, method)
	}
	for _, m := range more {
		if m != "" {
			methods = append(methods, m)
		}
	}
	if len(methods) == 0 {
		return
	}
	w.Header().Set(headerAllow, strings.Join(methods, ", "))
}

// MethodNotAllowed writes a 405 plus the Allow header RFC 9110 §15.5.6 requires. Pass exactly the
// methods the addressed resource dispatches: an over-promising Allow makes a client retry a method
// that 405s again. HEAD is not implied by GET, because these handlers compare r.Method for equality
// and HEAD also lands here.
func MethodNotAllowed(w http.ResponseWriter, method string, more ...string) {
	setAllow(w, method, more...)
	webhttp.WriteJSONStatus(w, http.StatusMethodNotAllowed, webhttp.ErrorResponse{Error: "method not allowed"})
}

// InternalError writes a 500 with {"error": "internal error"} and logs
// the actual error at slog.Error for correlation. Never exposes internal
// error details to HTTP clients.
func InternalError(w http.ResponseWriter, err error) {
	if err != nil {
		slog.Error("httpreply: internal error", "error", err)
	}
	webhttp.WriteJSONStatus(w, http.StatusInternalServerError, webhttp.ErrorResponse{Error: msgInternalError})
}

// ServerError writes a 500 with a caller-specified client-visible message
// and logs the actual error at slog.Error. Use when the handler wants to
// surface a safe, specific message (e.g. "save failed") while still
// logging the raw error for debugging.
func ServerError(w http.ResponseWriter, clientMsg string, err error) {
	if err != nil {
		slog.Error("httpreply: server error", "client_msg", clientMsg, "error", err)
	}
	webhttp.WriteJSONStatus(w, http.StatusInternalServerError, webhttp.ErrorResponse{Error: clientMsg})
}

// RequireMethod returns true if r.Method matches method; otherwise it
// writes a 405 response — with Allow set to that single method, which is
// by construction the resource's whole permitted set — and returns false.
func RequireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		MethodNotAllowed(w, method)
		return false
	}
	return true
}

// DecodeBody caps + decodes exactly one JSON value into v via
// webhttp.DecodeJSONInto (rejecting trailing data), returning true on success.
// On any decode failure it writes marotte's bare {"error":errMsg} 400 and
// returns false.
func DecodeBody(w http.ResponseWriter, r *http.Request, v any, errMsg string) bool {
	if err := webhttp.DecodeJSONInto(w, r, v, webhttp.MaxJSONBody); err != nil {
		BadRequest(w, errMsg)
		return false
	}
	return true
}

// DecodeBodyOptional caps the body and decodes JSON into v, reporting whether the caller may
// proceed. An absent or malformed body is ignored (v keeps its zero value) because these callers
// treat the body as advisory; an oversize body writes a 413 and returns false.
func DecodeBodyOptional(w http.ResponseWriter, r *http.Request, v any) bool {
	err := webhttp.DecodeJSONInto(w, r, v, webhttp.MaxJSONBody)
	if err == nil || errors.Is(err, io.EOF) {
		return true
	}
	if refuseTooLarge(w, r, err) {
		return false
	}
	return true
}
