package forges

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/webhttp/v3"
)

// DeviceFlowResponse is a started device grant as a client renders it. The
// grant is named by an id the server minted; the device code never leaves the
// server.
type DeviceFlowResponse struct {
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	GrantID         string `json:"grant_id"`
	Interval        int    `json:"interval"`
	ExpiresIn       int    `json:"expires_in"`
}

// PollResult is one poll of a device grant: pending, complete, expired, denied
// or error, with the refusal's code where there is one. It never carries a
// token.
type PollResult struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	Code   string `json:"code,omitempty"`
}

// The poll statuses. A token connect answers stateComplete too, and a failed
// probe names its reason under statusError.
const (
	statePending  = "pending"
	stateComplete = "complete"
	statusError   = "error"
	grantDenied   = "denied"
	grantExpired  = "expired"
)

const (
	// codeGrantNotFound answers a poll for a grant this server does not hold:
	// never started, already answered, cancelled, or swept past its expiry.
	codeGrantNotFound = "grant_not_found"
	// codeClientIDInvalid answers a start whose client id does not fit its host.
	codeClientIDInvalid = "client_id_invalid"
)

// grantSaveBudget bounds saving an approved grant, which outlives the request:
// the token is spent once the grant answers it.
const grantSaveBudget = 30 * time.Second

var (
	errClientIDNeeded = errors.New("forges: an instance other than the public one signs in with its own " +
		"OAuth application, named by client_id")
	errClientIDPublic = errors.New("forges: the public instance signs in with Marotte's own OAuth application, " +
		"so client_id names none")
)

// grantBody is a device-grant start: the instance, and the OAuth application
// it signs in with when the instance is not the public one.
type grantBody struct {
	Host     string `json:"host"`
	ClientID string `json:"client_id"`
	connectionFields
}

// handleDeviceGrant serves /api/forges/oauth/{github|gitlab}/{start|poll|cancel}.
func (h *HTTPHandler) handleDeviceGrant(w http.ResponseWriter, r *http.Request) {
	name, op, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/api/forges/oauth/"), "/")
	kind := Kind(name)
	if _, ok := marotteApp(kind.family()); !ok {
		httpreply.NotFound(w, "no device grant for that forge")
		return
	}
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	switch op {
	case "start":
		h.handleGrantStart(w, r, kind)
	case "poll":
		h.handleGrantPoll(w, r)
	case "cancel":
		h.handleGrantCancel(w, r)
	default:
		httpreply.NotFound(w, "unknown device-grant operation")
	}
}

func (h *HTTPHandler) handleGrantStart(w http.ResponseWriter, r *http.Request, kind Kind) {
	var body grantBody
	webhttp.LimitBody(w, r, webhttp.MaxJSONBody)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		httpreply.BadRequest(w, "invalid json")
		return
	}
	rec, req, err := h.grantFor(kind, &body)
	if err == nil {
		_, err = h.manager.writableRecord(rec.ID)
	}
	var started startedGrant
	if err == nil {
		started, err = h.grants.start(r.Context(), &rec, req)
	}
	if err != nil {
		writeGrantStartError(w, r, err)
		return
	}
	webhttp.WriteJSON(w, DeviceFlowResponse{
		UserCode: started.UserCode, VerificationURI: started.VerificationURI, GrantID: started.GrantID,
		Interval:  int(started.Interval / time.Second),
		ExpiresIn: int(time.Until(started.Expires).Round(time.Second) / time.Second),
	})
}

// grantFor is the connection a start signs in and the application it signs in
// with: Marotte's own on the public instance, the body's anywhere else.
func (h *HTTPHandler) grantFor(kind Kind, body *grantBody) (connectionRecord, creds.GrantRequest, error) {
	rec, err := body.record(kind, body.Host)
	if err != nil {
		return connectionRecord{}, creds.GrantRequest{}, err
	}
	if rec.Host != kind.DefaultHost() {
		if body.ClientID == "" {
			return connectionRecord{}, creds.GrantRequest{}, errClientIDNeeded
		}
		rec.OAuthClientID = body.ClientID
		return rec, creds.GrantRequest{ClientID: body.ClientID, Scopes: grantScopes(kind.family())}, nil
	}
	if body.ClientID != "" {
		return connectionRecord{}, creds.GrantRequest{}, errClientIDPublic
	}
	// handleDeviceGrant admits only a family marotteApp names.
	req, _ := marotteApp(kind.family())
	return rec, req, nil
}

// writeGrantStartError answers a refused start. Every refusal is a non-2xx: a
// 2xx is read as a started grant.
func writeGrantStartError(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil {
		return
	}
	switch {
	case errors.Is(err, errTooManyGrants):
		webhttp.WriteJSONStatus(w, http.StatusTooManyRequests, httpreply.ErrorJSON(err.Error()))
	case errors.Is(err, errClientIDNeeded), errors.Is(err, errClientIDPublic):
		webhttp.WriteJSONStatus(w, http.StatusBadRequest, httpreply.ErrorJSONWithCode(err.Error(), codeClientIDInvalid))
	case errors.Is(err, errWebBaseInvalid):
		webhttp.WriteJSONStatus(w, http.StatusBadRequest, httpreply.ErrorJSONWithCode(err.Error(), codeWebBaseInvalid))
	case errors.Is(err, errHostInvalid):
		httpreply.BadRequest(w, err.Error())
	case errors.Is(err, errConnectionUnusable):
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON(err.Error()))
	default:
		writeOpsError(w, r, err)
	}
}

func (h *HTTPHandler) handleGrantPoll(w http.ResponseWriter, r *http.Request) {
	id, ok := grantIDOf(w, r)
	if !ok {
		return
	}
	g, err := h.grants.poll(r.Context(), id)
	if err == nil && g.Done {
		res := h.completeGrant(r.Context(), &g)
		if r.Context().Err() == nil {
			webhttp.WriteJSON(w, res)
		}
		return
	}
	if r.Context().Err() != nil {
		return
	}
	if err != nil {
		writeGrantPollError(w, r, err)
		return
	}
	webhttp.WriteJSON(w, PollResult{Status: statePending})
}

// completeGrant saves an approved grant through the connect path. It does not
// end with the request, because the token cannot be asked for again.
func (h *HTTPHandler) completeGrant(ctx context.Context, g *polledGrant) PollResult {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grantSaveBudget)
	defer cancel()
	if err := h.manager.connectCredential(ctx, &g.Conn, &g.Record); err != nil {
		res := PollResult{Status: statusError, Error: logsafe.Field(err.Error())}
		if ferr, ok := errors.AsType[*forgeapi.Error](err); ok {
			res.Code = ferr.Code
		}
		return res
	}
	h.manager.Invalidate()
	_ = h.manager.Refresh(ctx)
	h.NotifyChanged(ctx)
	return PollResult{Status: stateComplete}
}

// writeGrantPollError answers a failed poll. A refusal that ends the grant is
// a terminal status, which the client stops on; a failure the grant survives is
// a non-2xx, which the client backs off from and polls again.
func writeGrantPollError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errGrantNotFound) {
		webhttp.WriteJSON(w, PollResult{Status: statusError, Error: err.Error(), Code: codeGrantNotFound})
		return
	}
	ferr, ok := errors.AsType[*forgeapi.Error](err)
	if !ok || ferr.Retryable || ferr.Kind == forgeapi.KindTransient || ferr.Kind == forgeapi.KindRateLimited {
		writeOpsError(w, r, err)
		return
	}
	status := statusError
	switch ferr.Code {
	case forgeapi.CodeGrantDenied:
		status = grantDenied
	case forgeapi.CodeGrantExpired:
		status = grantExpired
	}
	webhttp.WriteJSON(w, PollResult{Status: status, Error: logsafe.Field(ferr.Error()), Code: ferr.Code})
}

// handleGrantCancel withdraws and ends a grant. It answers 204 whether or not
// the id named one, so a repeated cancel is not an error.
func (h *HTTPHandler) handleGrantCancel(w http.ResponseWriter, r *http.Request) {
	id, ok := grantIDOf(w, r)
	if !ok {
		return
	}
	h.grants.cancel(id)
	w.WriteHeader(http.StatusNoContent)
}

// grantIDOf reads a {grant_id} body, answering a malformed one itself.
func grantIDOf(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		GrantID string `json:"grant_id"`
	}
	webhttp.LimitBody(w, r, webhttp.MaxJSONBody)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpreply.BadRequest(w, "invalid json")
		return "", false
	}
	if body.GrantID == "" {
		httpreply.BadRequest(w, "missing grant_id")
		return "", false
	}
	return body.GrantID, true
}
