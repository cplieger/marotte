package forges

import (
	"context"
	"errors"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/route"
)

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

// codeStatus is the one status each forgeapi code is answered with, by its remedy, whatever Kind
// forgeapi built it with: a code has one status across the endpoint table. A code-less error falls
// back to its Kind.
var codeStatus = map[string]int{
	forgeapi.CodeRepoRefInvalid:        http.StatusBadRequest,
	forgeapi.CodeRepoRefStale:          http.StatusNotFound,
	forgeapi.CodeRepoOrPRNotVisible:    http.StatusNotFound,
	forgeapi.CodePRNotFound:            http.StatusNotFound,
	forgeapi.CodeOwnerUnresolved:       http.StatusNotFound,
	forgeapi.CodeNotMergeable:          http.StatusConflict,
	forgeapi.CodeMissingSHA:            http.StatusBadRequest,
	forgeapi.CodeNoMergePermission:     http.StatusForbidden,
	forgeapi.CodeStrategyNotAllowed:    http.StatusBadRequest,
	forgeapi.CodeStaleHead:             http.StatusConflict,
	forgeapi.CodeValidation:            http.StatusBadGateway,
	forgeapi.CodeLabelPageTruncated:    http.StatusBadGateway,
	forgeapi.CodeBranchCannotMerge:     http.StatusConflict,
	forgeapi.CodeRepoArchived:          http.StatusForbidden,
	forgeapi.CodeScopeInsufficient:     http.StatusForbidden,
	forgeapi.CodeAPIVersionRetired:     http.StatusBadGateway,
	forgeapi.CodeMutationsDisabled:     http.StatusForbidden,
	forgeapi.CodeRedirectHopCap:        http.StatusBadGateway,
	forgeapi.CodeRedirectPortRefused:   http.StatusBadGateway,
	forgeapi.CodeTLSRefused:            http.StatusBadGateway,
	forgeapi.CodeUnreachable:           http.StatusServiceUnavailable,
	forgeapi.CodeResponseTooLarge:      http.StatusBadGateway,
	forgeapi.CodeConnectionInvalid:     http.StatusBadRequest,
	forgeapi.CodeAnonymousRefused:      http.StatusBadRequest,
	forgeapi.CodePlaintextRefused:      http.StatusBadRequest,
	forgeapi.CodePrivateAddressRefused: http.StatusBadRequest,
	forgeapi.CodeBudgetInvalid:         http.StatusBadRequest,
	forgeapi.CodePageBoundInvalid:      http.StatusBadRequest,
	forgeapi.CodeListStateInvalid:      http.StatusBadRequest,
	forgeapi.CodeListOwnerInvalid:      http.StatusBadRequest,
	forgeapi.CodeListSinceInvalid:      http.StatusBadRequest,
	forgeapi.CodeListMergeStateInvalid: http.StatusBadRequest,
	forgeapi.CodeRunUndated:            http.StatusBadGateway,
	forgeapi.CodeCursorInvalid:         http.StatusBadRequest,
	forgeapi.CodeRefInvalid:            http.StatusBadRequest,
	forgeapi.CodeHeaderReserved:        http.StatusBadRequest,
	// capability_unsupported arrives as KindForbidden, but it says the instance cannot do this,
	// which must not read as a permission refusal.
	forgeapi.CodeCapabilityUnsupported: http.StatusNotImplemented,
	forgeapi.CodeGrantUnsupported:      http.StatusNotImplemented,
	// family_undetected carries the last family's kind, but its remedy is a corrected address.
	forgeapi.CodeFamilyUndetected:  http.StatusUnprocessableEntity,
	forgeapi.CodeReconnectRequired: http.StatusConflict,
	forgeapi.CodeGrantDenied:       http.StatusForbidden,
	forgeapi.CodeGrantExpired:      http.StatusUnauthorized,
	forgeapi.CodeAppKeyInvalid:     http.StatusBadRequest,
	forgeapi.CodeAppGrantInvalid:   http.StatusBadRequest,
}

// notErrorCodes are forgeapi codes that ride a success value, never an *Error: already_enqueued
// is a merge outcome's.
var notErrorCodes = map[string]bool{forgeapi.CodeAlreadyEnqueued: true}

// ForgeAPICodes are the forgeapi codes this package relays in its error envelope, each with its
// codeStatus; a code Codes lists is this package's own.
var ForgeAPICodes = func() route.CodeSet {
	var out route.CodeSet
	for _, c := range forgeapi.Codes() {
		if notErrorCodes[c] || slices.ContainsFunc(Codes, func(o route.Code) bool { return o.Code == c }) {
			continue
		}
		out = append(out, route.Code{Code: c, Status: codeStatus[c]})
	}
	return out
}()

func statusFor(e *forgeapi.Error) int {
	if status, ok := codeStatus[e.Code]; ok {
		return status
	}
	if status, ok := kindStatus[e.Kind]; ok {
		return status
	}
	return http.StatusInternalServerError
}

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

// The message carries upstream text, so it is sanitized and bounded before it reaches a client.
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

// The wait also travels as Retry-After, but the client's action layer cannot read a header off a
// non-2xx, so the body carries it too.
func writeForgeAPIError(w http.ResponseWriter, e *forgeapi.Error) {
	env := envelopeFor(e)
	if env.RetryAfterS > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt(env.RetryAfterS, 10))
	}
	httpserver.WriteJSONStatus(w, statusFor(e), env)
}

// writeContextError answers a context sentinel the library returned on a live
// request, reporting whether err was one. Only the request's own context says
// the client walked away, so this is reached after that check.
func writeContextError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		httpserver.WriteJSONStatus(w, http.StatusGatewayTimeout,
			httpreply.ErrorJSON("the forge did not answer in time"))
	case errors.Is(err, context.Canceled):
		httpserver.WriteJSONStatus(w, http.StatusServiceUnavailable,
			httpreply.ErrorJSON("the forge request was cancelled"))
	default:
		return false
	}
	return true
}

// statusOf is a failed check's status, decided from forgeapi's Kind and Code
// alone: a dead token is reconnect, the forge refusing the account is refused,
// and every other answer (an outage, a rate limit, a TLS or address-policy
// refusal, anything forgeapi cannot classify) is retrying. at is the check's
// end, which a Retry-After counts from.
func statusOf(err error, at time.Time) status {
	fe, ok := errors.AsType[*forgeapi.Error](err)
	if !ok {
		return status{kind: statusRetrying, checkedAt: at, fault: &fault{message: err.Error()}}
	}
	f := &fault{message: fe.Error(), code: fe.Code, kind: fe.Kind.String()}
	if fe.RetryAfter > 0 {
		f.retryAt = at.Add(fe.RetryAfter)
	}
	s := status{kind: statusRetrying, checkedAt: at, fault: f}
	switch {
	case fe.Kind == forgeapi.KindUnauthorized, fe.Code == forgeapi.CodeReconnectRequired:
		s.kind = statusReconnect
	case fe.Kind == forgeapi.KindForbidden, fe.Kind == forgeapi.KindNotFound:
		s.kind = statusRefused
	}
	return s
}
