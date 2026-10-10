// Package rpcerr turns a JSON-RPC error from kiro-cli into text a person can read.
//
// It is shared by internal/command, internal/agent and internal/workflow, none of which
// owns the rule. It imports no other marotte package: the one shape it needs is reached
// through detailer, so errors.AsType finds it at any wrapping depth.
package rpcerr

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/cplieger/runesafe/v2"
)

// maxTextBytes bounds one error string on its way to a user surface: Details' last
// fallback returns the raw `error.data`, which is unbounded on a Zod failure.
const maxTextBytes = 2048

// detailer is satisfied by an error carrying KAS's `error.data`; an interface rather than
// *RPCError so a wrapped error is found at any depth. It embeds error because
// errors.AsType's type parameter is constrained to error.
type detailer interface {
	error
	ErrorData() json.RawMessage
}

// A future widening of detailer past error fails here.
var _ error = detailer(nil)

// Details extracts the text KAS put in `error.data`, or "" when there is none. Callers
// want Text; Details stays exported for workflow.Classify, which wants the data half only.
func Details(err error) string {
	d, ok := errors.AsType[detailer](err)
	if !ok {
		return ""
	}
	raw := d.ErrorData()
	if len(raw) == 0 {
		return ""
	}
	// A typed KAS error's data is a machine envelope; its prose is in message.
	if _, ok := mappedOf(raw); ok {
		return ""
	}
	var obj struct {
		Details string `json:"details"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Details != "" {
		return obj.Details
	}
	// The other shape: a Zod issue array. Only Message is decoded.
	var issues []struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &issues) == nil && len(issues) > 0 {
		msgs := make([]string, 0, len(issues))
		for _, is := range issues {
			if is.Message != "" {
				msgs = append(msgs, is.Message)
			}
		}
		if len(msgs) > 0 {
			return strings.Join(msgs, "; ")
		}
	}
	// Neither shape parsed: the raw JSON beats an empty string. maxTextBytes exists for this.
	return string(raw)
}

// Text is the one function a user-facing surface should call on an ACP error: the most
// specific text it carries (`error.data`, else `error.message`), sanitized single-line
// and capped on a rune boundary at maxTextBytes.
func Text(err error) string {
	if err == nil {
		return ""
	}
	text := Details(err)
	if text == "" {
		text = err.Error()
		if d, ok := errors.AsType[detailer](err); ok {
			if _, mapped := mappedOf(d.ErrorData()); mapped {
				text = d.Error()
			}
		}
	}
	return sanitize(text, maxTextBytes)
}

// Mapped is KAS's typed-error envelope, `error.data` = {errorType,
// retryErrorType, requestId}. It is independent of the JSON-RPC code, and its
// prose is always in `error.message`.
type Mapped struct {
	ErrorType      string `json:"errorType"`
	RetryErrorType string `json:"retryErrorType"`
	RequestID      string `json:"requestId"`
}

// The KAS class names and retry types marotte keys a remedy on.
const (
	RetryThrottling                = "THROTTLING"
	ContextWindowExceededError     = "ContextWindowExceededError"
	modelRegistryUnavailableError  = "ModelRegistryUnavailableError"
	mappedProseCap                 = 1024
	mappedRequestIDCap             = 128
	contextLimitSentence           = "This chat reached the model's context limit. Compact the context, then send the prompt again."
	modelRegistryUnavailableRemedy = " Run `kiro-cli login`, then send the prompt again."
	throttledRemedy                = " kiro-cli already retried. Wait a moment and send again, or switch to another model."
	mappedFallbackProse            = "the model backend refused the request"
)

// knownClasses are KAS error class names worth showing a reader: the classes
// marotte keys a remedy on, plus the ones measured on real session logs. Any
// other errorType is an unmapped error whose class name the bundle minified.
var knownClasses = map[string]struct{}{
	ContextWindowExceededError:          {},
	modelRegistryUnavailableError:       {},
	"ModelRegistryUnauthenticatedError": {},
	"ModelRegistryAccessDeniedError":    {},
	"ClientThrottleError":               {},
	"ModelThrottleError":                {},
	"ServiceThrottleError":              {},
	"OverageLimitReachedError":          {},
	"InternalServerException":           {},
	"ServerConnectionResetError":        {},
	"ConnectionResetError":              {},
	"StreamIdleTimeoutError":            {},
	"ModelRefreshTimeoutError":          {},
	"GenericValidationError":            {},
	"BedrockValidationError":            {},
	"InvalidModelError":                 {},
	"TokenInvalidError":                 {},
	"TokenExpiredError":                 {},
	"AuthRefreshFailedError":            {},
	"AccessDeniedError":                 {},
	"AbortedError":                      {},
}

// KnownClass reports whether name is a KAS error class a reader can be shown.
func KnownClass(name string) bool {
	_, ok := knownClasses[name]
	return ok
}

// MappedOf finds the typed-error envelope at any wrapping depth. ok is false for
// an error with no data, or whose data is not an object naming a class.
func MappedOf(err error) (Mapped, bool) {
	d, ok := errors.AsType[detailer](err)
	if !ok {
		return Mapped{}, false
	}
	return mappedOf(d.ErrorData())
}

func mappedOf(raw json.RawMessage) (Mapped, bool) {
	if len(raw) == 0 {
		return Mapped{}, false
	}
	var m Mapped
	if json.Unmarshal(raw, &m) != nil || (m.ErrorType == "" && m.RetryErrorType == "") {
		return Mapped{}, false
	}
	return m, true
}

// Account is what a typed KAS error says on a turn card: KAS's message plus
// marotte's remedy for its class, keyed on exact class names only, so a minified
// errorType gets the message alone. Each half has its own budget, so a long
// message cannot cut off the remedy or the request id.
func Account(m Mapped, message string) string {
	if m.ErrorType == ContextWindowExceededError {
		// KAS's own sentence names a setting the reader cannot reach.
		return withRequestID(contextLimitSentence, m.RequestID)
	}
	msg := strings.TrimSpace(sanitize(message, mappedProseCap))
	if m.ErrorType == modelRegistryUnavailableError {
		return msg + modelRegistryUnavailableRemedy
	}
	if msg == "" {
		msg = strings.TrimSpace(sanitize(m.ErrorType, mappedProseCap))
		if msg == "" {
			msg = mappedFallbackProse
		}
	}
	if m.RetryErrorType == RetryThrottling {
		msg += throttledRemedy
	}
	return withRequestID(msg, m.RequestID)
}

func withRequestID(msg, id string) string {
	if id = strings.TrimSpace(sanitize(id, mappedRequestIDCap)); id != "" {
		msg += " (request " + id + ")"
	}
	return msg
}

// sanitize is Text's treatment without the compose: one upstream string made safe for a
// user surface and bounded to maxBytes (a parameter so a caller can leave room for a remedy).
func sanitize(s string, maxBytes int) string {
	if s == "" {
		return ""
	}
	// Capped, not Bounded: Bounded puts the marker OUTSIDE the cap (n+3 bytes).
	capped, _ := runesafe.SanitizeSingleLineCapped(s, maxBytes, "...")
	return capped
}
