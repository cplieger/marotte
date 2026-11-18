package httpreply

import (
	"net/http"
	"strconv"
	"time"

	"github.com/cplieger/go/httpserver"
)

// FieldError is one refused field of a request body, named by its JSON path.
type FieldError struct {
	Field string `json:"field"`
	Error string `json:"error"`
}

type fieldsBody struct {
	Error  string       `json:"error"`
	Fields []FieldError `json:"fields"`
}

// BadRequestFields writes a 400 with {"error": msg, "fields": [...]}, the fields in the order given.
func BadRequestFields(w http.ResponseWriter, msg string, fields []FieldError) {
	httpserver.WriteJSONStatus(w, http.StatusBadRequest, fieldsBody{Error: msg, Fields: fields})
}

type retryBody struct {
	Error        string `json:"error"`
	RetryAfterMS int64  `json:"retry_after_ms"`
}

// TooManyRequests writes a 429 with Retry-After in whole seconds rounded up (RFC 9110 §10.2.3, which allows no
// fraction) and the wait in milliseconds, rounded up, in the body.
func TooManyRequests(w http.ResponseWriter, msg string, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.FormatInt(ceilDiv(wait, time.Second), 10))
	httpserver.WriteJSONStatus(w, http.StatusTooManyRequests, retryBody{Error: msg, RetryAfterMS: ceilDiv(wait, time.Millisecond)})
}

// ceilDiv is d in whole units, rounded up, at least 1.
func ceilDiv(d, unit time.Duration) int64 {
	n := int64((d + unit - 1) / unit)
	return max(n, 1)
}
