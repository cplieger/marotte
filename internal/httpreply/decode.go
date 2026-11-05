package httpreply

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/logsafe"
)

// DecodeJSON is the shared three-step JSON decode prelude: check
// Content-Type, cap body at httpserver.MaxJSONBody via MaxBytesReader, and
// distinguish an oversize request (413) from a malformed body (400).
// Returns true when v was populated successfully; false when an error
// response has already been written to w.
func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if ct := r.Header.Get("Content-Type"); ct != "" &&
		!strings.HasPrefix(ct, MIMETypeJSON) {
		slog.Debug("httpreply: decode bad content-type",
			"method", r.Method, "path", logsafe.Field(r.URL.Path), "content_type", logsafe.Field(ct))
		BadRequest(w, "expected "+MIMETypeJSON)
		return false
	}
	// httpserver.DecodeJSONInto caps, decodes and rejects trailing data but writes
	// nothing, so the 413/400 split and the envelope are written here.
	if err := httpserver.DecodeJSONInto(w, r, v, httpserver.MaxJSONBody); err != nil {
		if refuseTooLarge(w, r, err) {
			return false
		}
		slog.Debug("httpreply: decode invalid json",
			"method", r.Method, "path", logsafe.Field(r.URL.Path), "error", logsafe.Field(err.Error()))
		BadRequest(w, "invalid json")
		return false
	}
	return true
}

// refuseTooLarge reports whether err is the body-size refusal
// (*http.MaxBytesError), writing the 413 and the Warn line when it is and
// nothing when it is not. Every decode door shares it so the oversize taxonomy
// — status, client-visible message and log line — has one owner; what the doors
// disagree about is the malformed-body policy, which stays at each of them.
func refuseTooLarge(w http.ResponseWriter, r *http.Request, err error) bool {
	maxErr, ok := errors.AsType[*http.MaxBytesError](err)
	if !ok {
		return false
	}
	slog.Warn("httpreply: decode body too large",
		"method", r.Method, "path", logsafe.Field(r.URL.Path),
		"limit", httpserver.MaxJSONBody, "error", maxErr)
	httpserver.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
		map[string]string{jsonKeyError: "request body too large"})
	return true
}
