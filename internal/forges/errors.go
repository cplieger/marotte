package forges

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/webhttp/v3"
)

// kindStatus is the HTTP status of each forgeapi failure class.
var kindStatus = map[forgeapi.ErrorKind]int{
	forgeapi.KindUnauthorized: http.StatusUnauthorized,
	forgeapi.KindForbidden:    http.StatusForbidden,
	forgeapi.KindNotFound:     http.StatusNotFound,
	forgeapi.KindNotMergeable: http.StatusConflict,
	forgeapi.KindConflict:     http.StatusConflict,
	forgeapi.KindRateLimited:  http.StatusTooManyRequests,
	forgeapi.KindTransient:    http.StatusServiceUnavailable,
	forgeapi.KindUpstream:     http.StatusBadGateway,
}

// localRefusals are the library's refusals of a request it never sent because
// the caller's input was wrong.
var localRefusals = map[string]bool{
	forgeapi.CodeRepoRefInvalid:        true,
	forgeapi.CodeRefInvalid:            true,
	forgeapi.CodeMissingSHA:            true,
	forgeapi.CodeStrategyNotAllowed:    true,
	forgeapi.CodeCursorInvalid:         true,
	forgeapi.CodeListStateInvalid:      true,
	forgeapi.CodeListOwnerInvalid:      true,
	forgeapi.CodePageBoundInvalid:      true,
	forgeapi.CodeConnectionInvalid:     true,
	forgeapi.CodeHeaderReserved:        true,
	forgeapi.CodePlaintextRefused:      true,
	forgeapi.CodePrivateAddressRefused: true,
	forgeapi.CodeAnonymousRefused:      true,
	forgeapi.CodeBudgetInvalid:         true,
}

// statusFor is the HTTP status a forgeapi failure answers.
func statusFor(e *forgeapi.Error) int {
	switch e.Code {
	// capability_unsupported arrives as KindForbidden, but it says the instance
	// cannot do this, which must not read as a permission refusal.
	case forgeapi.CodeCapabilityUnsupported, forgeapi.CodeGrantUnsupported:
		return http.StatusNotImplemented
	// family_undetected carries the last family's kind, but its remedy is a
	// corrected address, not whatever that one read failed on.
	case forgeapi.CodeFamilyUndetected:
		return http.StatusUnprocessableEntity
	}
	if status, ok := kindStatus[e.Kind]; ok {
		return status
	}
	if localRefusals[e.Code] {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

// errorEnvelope is the body of every forge route's failure answer.
type errorEnvelope struct {
	Successor   *RepoSuccessor  `json:"successor,omitempty"`
	Capability  *capabilityWire `json:"capability,omitempty"`
	Error       string          `json:"error"`
	Code        string          `json:"code"`
	Kind        string          `json:"kind"`
	DiagID      string          `json:"diag_id,omitempty"`
	RetryAfterS int64           `json:"retry_after_s,omitempty"`
}

// capabilityWire is the capability a refused operation needed and what
// detection read it from.
type capabilityWire struct {
	Name    string `json:"name"`
	Support string `json:"support"`
	Source  string `json:"source"`
	Detail  string `json:"detail"`
}

// envelopeFor builds e's envelope. The message carries upstream text, so it is
// sanitized and bounded before it reaches a client.
func envelopeFor(e *forgeapi.Error) errorEnvelope {
	env := errorEnvelope{
		Error:       logsafe.Field(e.Error()),
		Code:        e.Code,
		Kind:        e.Kind.String(),
		DiagID:      e.DiagID,
		RetryAfterS: retryAfterSeconds(e),
		Successor:   successorOf(e.Successor),
	}
	if e.Code == forgeapi.CodeCapabilityUnsupported {
		env.Capability = &capabilityWire{
			Name:    string(e.Capability),
			Support: forgeapi.SupportNo.String(),
			Source:  e.Evidence.Source.String(),
			Detail:  e.Evidence.Detail,
		}
	}
	return env
}

// retryAfterSeconds is the wait e asks for in whole seconds, rounded up so a
// client never retries early.
func retryAfterSeconds(e *forgeapi.Error) int64 {
	if e.RetryAfter <= 0 {
		return 0
	}
	return int64(math.Ceil(e.RetryAfter.Seconds()))
}

// writeForgeAPIError answers e. The wait also travels as Retry-After, but the
// client's action layer cannot read a header off a non-2xx, so the body carries
// it too.
func writeForgeAPIError(w http.ResponseWriter, e *forgeapi.Error) {
	env := envelopeFor(e)
	if env.RetryAfterS > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt(env.RetryAfterS, 10))
	}
	webhttp.WriteJSONStatus(w, statusFor(e), env)
}

// writeContextError answers a context sentinel the library returned on a live
// request, reporting whether err was one. Only the request's own context says
// the client walked away, so this is reached after that check.
func writeContextError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		webhttp.WriteJSONStatus(w, http.StatusGatewayTimeout,
			httpreply.ErrorJSON("the forge did not answer in time"))
	case errors.Is(err, context.Canceled):
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable,
			httpreply.ErrorJSON("the forge request was cancelled"))
	default:
		return false
	}
	return true
}
