package forges

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
	"github.com/cplieger/keyenc"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/webhttp/v3"
)

const codeNotSupported = "not_supported"

// HTTPHandler exposes the forges package over HTTP.
type HTTPHandler struct {
	manager *Manager
	// grants are the device grants in progress, which only this process can
	// poll: a restart loses them and the sign-in starts again.
	grants *grantRegistry
	// poller holds the pull-request inventory and runs its cycles.
	poller *PRStatusPoller
}

// NewHTTPHandler builds an HTTPHandler over m, whose notify announces every
// change the routes make.
func NewHTTPHandler(m *Manager) *HTTPHandler {
	return &HTTPHandler{manager: m, grants: newGrantRegistry()}
}

// SetPoller wires the poller whose inventory the inventory routes answer from.
// Called once at composition, before the routes serve.
func (h *HTTPHandler) SetPoller(p *PRStatusPoller) { h.poller = p }

// Detection, a token sign-in and a device-grant start share one bucket: any
// caller can send each, and each sends requests to a forge.
const (
	outboundBurst    = 6
	outboundInterval = 10 * time.Second
)

// RegisterRoutes installs the /api/forges/* mux entries.
func (h *HTTPHandler) RegisterRoutes(mux *http.ServeMux) {
	outbound := webhttp.RateLimiter(outboundBurst, outboundInterval,
		webhttp.WithRateLimitWhen(sendsOutbound),
		webhttp.WithRateLimitError("rate_limited", "Too many sign-ins or detections were started. Wait a moment and try again"))
	mux.HandleFunc("/api/forges", h.handleForgesList)
	mux.HandleFunc("/api/forges/", h.handleForgeItem)
	mux.Handle("/api/forges/login", outbound(http.HandlerFunc(h.handleLogin)))
	mux.Handle("/api/forges/detect", outbound(http.HandlerFunc(h.handleDetect)))
	mux.HandleFunc("/api/forges/inventory", h.handleInventory)
	mux.HandleFunc("/api/forges/inventory/refresh", h.handleInventoryRefresh)
	mux.HandleFunc("/api/forges/inventory/watch", h.handleInventoryWatch)
	mux.Handle("/api/forges/oauth/", outbound(http.HandlerFunc(h.handleDeviceGrant)))
}

func sendsOutbound(r *http.Request) bool {
	return r.Method == http.MethodPost &&
		(r.URL.Path == "/api/forges/detect" || r.URL.Path == "/api/forges/login" || strings.HasSuffix(r.URL.Path, "/start"))
}

const (
	clientTagHeader = "SSE-Client"
	// codeWatchInvalid answers a watch whose tag or body is not one.
	codeWatchInvalid = "watch_invalid"
)

type watchBody struct {
	Watching *bool  `json:"watching"`
	Page     string `json:"page"`
}

// handleInventoryWatch is POST /api/forges/inventory/watch {watching}: the
// client behind the SSE-Client tag shows the pull-request view or stopped. The
// tag is held to the grammar the stream's presence table keys on.
func (h *HTTPHandler) handleInventoryWatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	if h.poller == nil {
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON(errNoInventory))
		return
	}
	tag := r.Header.Get(clientTagHeader)
	if !webhttp.ValidRequestID(tag) {
		webhttp.WriteJSONStatus(w, http.StatusBadRequest,
			httpreply.ErrorJSONWithCode("SSE-Client header absent or outside [A-Za-z0-9_-]{1,64}", codeWatchInvalid))
		return
	}
	var body watchBody
	webhttp.LimitBody(w, r, webhttp.MaxJSONBody)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Watching == nil ||
		(body.Page != "" && !webhttp.ValidRequestID(body.Page)) {
		webhttp.WriteJSONStatus(w, http.StatusBadRequest,
			httpreply.ErrorJSONWithCode("the body must be {watching: true|false, page?: [A-Za-z0-9_-]{1,64}}", codeWatchInvalid))
		return
	}
	h.poller.viewers.watch(tag, body.Page, *body.Watching)
	w.WriteHeader(http.StatusNoContent)
}

const errNoInventory = "the pull-request inventory is not running"

func (h *HTTPHandler) handleInventory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	if h.poller == nil {
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON(errNoInventory))
		return
	}
	webhttp.WriteJSON(w, h.poller.inventoryFor(h.manager.readers()))
}

func (h *HTTPHandler) handleInventoryRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	if h.poller == nil {
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON(errNoInventory))
		return
	}
	webhttp.WriteJSONStatus(w, http.StatusAccepted, InventoryRefresh{CycleID: h.poller.refresh()})
}

func (h *HTTPHandler) handleForgesList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	forges := h.manager.List()
	_, github := marotteApp(forgeapi.FamilyGitHub)
	_, gitlab := marotteApp(forgeapi.FamilyGitLab)
	webhttp.WriteJSON(w, map[string]any{
		"forges": forges,
		"kinds":  allKinds(),
		// Device sign-in with Marotte's own application, per public instance.
		"oauth": map[string]bool{string(KindGitHub): github, string(KindGitLab): gitlab},
	})
}

func (h *HTTPHandler) handleForgeItem(w http.ResponseWriter, r *http.Request) {
	tail := strings.TrimPrefix(r.URL.Path, "/api/forges/")
	if tail == "" {
		httpreply.NotFound(w, "missing forge id")
		return
	}
	id, sub, _ := splitFirst(tail)
	if sub == "" && r.Method == http.MethodDelete {
		h.handleDisconnect(w, r, id)
		return
	}
	row, ok := h.manager.view(id)
	if !ok {
		httpreply.NotFound(w, "unknown forge id")
		return
	}
	if sub == "" {
		if r.Method != http.MethodGet {
			httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodDelete)
			return
		}
		webhttp.WriteJSON(w, row)
		return
	}
	op, rest, _ := splitFirst(sub)
	switch op {
	case "probe":
		h.handleProbe(w, r, id)
	case "capabilities":
		h.handleCapabilities(w, r, id)
	case "owners":
		h.handleOwners(w, r, id)
	case "repos":
		h.handleRepos(w, r, id, rest)
	default:
		httpreply.NotFound(w, "unknown forge sub-resource")
	}
}

// handleDisconnect is DELETE /api/forges/{id}: a sign-out of any id parseID
// reads, a row or not, so a repeat finishes a cleanup a first one left.
func (h *HTTPHandler) handleDisconnect(w http.ResponseWriter, r *http.Request, id string) {
	deadline := h.manager.deadlines.from(time.Now())
	if _, _, ok := parseID(id); !ok {
		httpreply.BadRequest(w, "invalid forge id")
		return
	}
	err := h.manager.signOut(r.Context(), deadline, id)
	switch {
	case err == nil:
		webhttp.Ok(w)
	case r.Context().Err() != nil, writeMutationError(w, err):
	case errors.Is(err, errCleanupPending):
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable,
			httpreply.ErrorJSONWithCode(logsafe.Field(err.Error()), codeCleanupPending))
	default:
		httpreply.ServerError(w, "the sign-out could not be saved", err)
	}
}

// writeMutationError answers the refusals every mutation of a connection
// shares, and reports whether err was one.
func writeMutationError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, errConnectionBusy):
		webhttp.WriteJSONStatus(w, http.StatusConflict, httpreply.ErrorJSONWithCode(err.Error(), codeConnectionBusy))
	case errors.Is(err, errConnectionUnusable):
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON(err.Error()))
	default:
		return false
	}
	return true
}

const (
	// codeOwnersInvalid answers an owners body that is not a list of strings.
	codeOwnersInvalid = "owners_invalid"
	codeOwnersTooMany = "owners_too_many"
)

// An empty list clears them.
type ownersBody struct {
	Owners *[]string `json:"owners"`
}

// handleOwners is PUT /api/forges/{id}/owners {owners}: the stored list is
// replaced whole, every owner checked before anything is stored.
func (h *HTTPHandler) handleOwners(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPut {
		httpreply.MethodNotAllowed(w, http.MethodPut)
		return
	}
	deadline := h.manager.deadlines.from(time.Now())
	var body ownersBody
	webhttp.LimitBody(w, r, webhttp.MaxJSONBody)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Owners == nil {
		webhttp.WriteJSONStatus(w, http.StatusBadRequest,
			httpreply.ErrorJSONWithCode("the body must be {owners: [string, ...]}", codeOwnersInvalid))
		return
	}
	stored, err := h.manager.setOwners(r.Context(), deadline, id, *body.Owners)
	if err != nil {
		writeOwnersError(w, r, err)
		return
	}
	// The inventory's added scopes are the stored owners, so the change shows at
	// the next cycle rather than a poll interval later.
	h.askCycle()
	webhttp.WriteJSON(w, OwnerScopes{Owners: stored})
}

// writeOwnersError answers a refused owners write. An owner the library refuses
// is its own envelope, with the message naming which owner it was.
func writeOwnersError(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil || writeMutationError(w, err) {
		return
	}
	if ferr, ok := errors.AsType[*forgeapi.Error](err); ok {
		env := envelopeFor(ferr)
		env.Error = logsafe.Field(err.Error())
		webhttp.WriteJSONStatus(w, statusFor(ferr), env)
		return
	}
	switch {
	case errors.Is(err, errTooManyOwners):
		webhttp.WriteJSONStatus(w, http.StatusBadRequest, httpreply.ErrorJSONWithCode(err.Error(), codeOwnersTooMany))
	case errors.Is(err, errNotLoggedIn):
		httpreply.NotFound(w, err.Error())
	default:
		httpreply.ServerError(w, "storing the owners failed", err)
	}
}

// handle answers connection id's read handle, or writes why there is none:
// persistence the server cannot use is a 503, anything else a 404.
func (h *HTTPHandler) handle(w http.ResponseWriter, id string) (readHandle, bool) {
	rh, err := h.manager.reader(id)
	if errors.Is(err, errConnectionUnusable) {
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON(err.Error()))
		return readHandle{}, false
	}
	if err != nil {
		httpreply.NotFound(w, err.Error())
		return readHandle{}, false
	}
	return rh, true
}

// A failed read of either scope answers its envelope, never a partial verdict.
func (h *HTTPHandler) handleCapabilities(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	rh, ok := h.handle(w, id)
	if !ok {
		return
	}
	caps, err := h.manager.capabilities(r.Context(), &rh, r.URL.Query().Get("refresh") == "1")
	if err != nil {
		writeOpsError(w, r, err)
		return
	}
	webhttp.WriteJSON(w, caps)
}

const (
	codeCheckBusy         = "check_busy"
	codeReconnectRequired = "reconnect_required"
)

// handleProbe is the Check: one check of the connection under the request's
// context, answering the row it left.
func (h *HTTPHandler) handleProbe(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	_, err := h.manager.check(r.Context(), id, checkBudget)
	switch {
	case r.Context().Err() != nil, writeMutationError(w, err):
		return
	case errors.Is(err, errCheckBusy):
		webhttp.WriteJSONStatus(w, http.StatusConflict, httpreply.ErrorJSONWithCode(err.Error(), codeCheckBusy))
		return
	case errors.Is(err, errReconnectRequired):
		webhttp.WriteJSONStatus(w, http.StatusConflict, httpreply.ErrorJSONWithCode(err.Error(), codeReconnectRequired))
		return
	case err != nil:
		httpreply.NotFound(w, err.Error())
		return
	}
	row, ok := h.manager.view(id)
	if !ok {
		httpreply.NotFound(w, "unknown forge id")
		return
	}
	webhttp.WriteJSON(w, ProbeResult{Forge: &row})
}

const (
	codeKindInvalid   = "kind_invalid"
	codeTokenRequired = "token_required"
)

// patLoginBody is a token sign-in: the forge's kind and host, the token, and
// for a self-managed instance how to reach it.
type patLoginBody struct {
	Kind  Kind   `json:"kind"`
	Host  string `json:"host"`
	Token string `json:"token"`
	connectionFields
}

// handleLogin is POST /api/forges/login: a token sign-in, answering the id the
// server minted for the saved connection.
func (h *HTTPHandler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	deadline := h.manager.deadlines.from(time.Now())
	var body patLoginBody
	if err := webhttp.DecodeJSONInto(w, r, &body, webhttp.MaxJSONBody); err != nil {
		httpreply.BadRequest(w, "invalid json")
		return
	}
	if !body.Kind.valid() {
		webhttp.WriteJSONStatus(w, http.StatusBadRequest,
			httpreply.ErrorJSONWithCode("forges: kind must be one of the supported forge kinds", codeKindInvalid))
		return
	}
	spec, err := body.record(body.Kind, body.Host)
	if err == nil && body.Token == "" {
		err = errTokenRequired
	}
	if err != nil {
		writeFieldError(w, err)
		return
	}
	id, err := h.manager.signIn(r.Context(), deadline, &spec,
		&creds.Record{Kind: forgeapi.CredKindStaticPAT, Token: body.Token, Issued: time.Now()})
	if err != nil {
		writeLoginError(w, r, err)
		return
	}
	webhttp.WriteJSON(w, LoginResult{ForgeID: id})
}

// errTokenRequired refuses a token sign-in with no token.
var errTokenRequired = errors.New("forges: a token is required")

// writeFieldError answers a sign-in body field marotte refuses, a 400 naming
// the field.
func writeFieldError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errWebBaseInvalid):
		webhttp.WriteJSONStatus(w, http.StatusBadRequest, httpreply.ErrorJSONWithCode(err.Error(), codeWebBaseInvalid))
	case errors.Is(err, errTokenRequired):
		webhttp.WriteJSONStatus(w, http.StatusBadRequest, httpreply.ErrorJSONWithCode(err.Error(), codeTokenRequired))
	default:
		httpreply.BadRequest(w, err.Error())
	}
}

type connectionFields struct {
	WebBaseURL string `json:"web_base_url"`
	APIBaseURL string `json:"api_base_url"`
	trustFields
}

type trustFields struct {
	CAPEM            string `json:"ca_pem"`
	ClientCertPEM    string `json:"client_cert_pem"`
	ClientKeyPEM     string `json:"client_key_pem"`
	Proxy            string `json:"proxy"`
	PrivateAddresses bool   `json:"private_addresses"`
	PlaintextHTTP    bool   `json:"plaintext_http"`
}

// codeWebBaseInvalid answers a web base URL that is not the scheme and host the
// connection names.
const codeWebBaseInvalid = "web_base_url_invalid"

var (
	errWebBaseInvalid = errors.New("forges: web_base_url must be the scheme and host:port of the host")
	errHostInvalid    = errors.New("forges: the host is not a host[:port]")
)

// maxHostBytes bounds a host: a DNS name is at most 253 octets (RFC 1035
// section 2.3.4) and a port adds at most 6 (":65535"). It also keeps every id
// reversible, since keyenc.Join hashes components over MaxComponentBytes into an
// id keyenc.Split refuses, and the longest kind plus this bound is far below it.
const maxHostBytes = 259

// validHost is the one host rule of the sign-in boundary, the records file and
// parseID: a bare host[:port], bounded.
func validHost(host string) bool {
	return host != "" && len(host) <= maxHostBytes && originOn("https://"+host, host)
}

// The host, and the web base URL when one is given, must each be a bare origin on that host and
// port, so a token is never sent to an instance the connection does not name.
func (b *connectionFields) record(kind Kind, host string) (connectionSpec, error) {
	if host == "" {
		host = kind.defaultHost()
	}
	if !validHost(host) {
		return connectionSpec{}, errHostInvalid
	}
	spec := b.specOn(host)
	spec.ID, spec.Kind, spec.APIBaseURL = MakeID(kind, host), kind, b.APIBaseURL
	if b.WebBaseURL == "" {
		return spec, nil
	}
	webBase, err := canonicalWebBase(b.WebBaseURL, host)
	if err != nil {
		return connectionSpec{}, err
	}
	spec.WebBaseURL = webBase
	return spec, nil
}

// canonicalWebBase is raw as a connection stores it, scheme://host, refused
// unless raw is an origin on host.
func canonicalWebBase(raw, host string) (string, error) {
	if !originOn(raw, host) {
		return "", errWebBaseInvalid
	}
	u, _ := url.Parse(raw)
	return u.Scheme + "://" + host, nil
}

func (f *trustFields) specOn(host string) connectionSpec {
	return connectionSpec{
		Host: host, CAPEM: f.CAPEM, ClientCertPEM: f.ClientCertPEM, ClientKeyPEM: f.ClientKeyPEM, Proxy: f.Proxy,
		PrivateAddresses: f.PrivateAddresses, PlaintextHTTP: f.PlaintextHTTP,
	}
}

// originOn reports whether raw is a scheme and host:port, the host being host,
// with no userinfo, path, query or fragment.
func originOn(raw, host string) bool {
	u, err := url.Parse(raw)
	return err == nil && host != "" && strings.EqualFold(u.Host, host) && u.User == nil && u.Opaque == "" &&
		(u.Path == "" || u.Path == "/") && !u.ForceQuery && u.RawQuery == "" && u.Fragment == ""
}

// writeLoginError answers a refused sign-in with an ordinary status and the
// {error, code} body the client's action layer carries to the form.
func writeLoginError(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil || writeMutationError(w, err) {
		return
	}
	if ferr, ok := errors.AsType[*forgeapi.Error](err); ok {
		writeForgeAPIError(w, ferr)
		return
	}
	switch {
	case errors.Is(err, errSignInTimedOut):
		webhttp.WriteJSONStatus(w, http.StatusGatewayTimeout, httpreply.ErrorJSONWithCode(err.Error(), codeSignInTimedOut))
	default:
		webhttp.WriteJSONStatus(w, http.StatusInternalServerError, httpreply.ErrorJSON(logsafe.Field(err.Error())))
	}
}

// A request whose own context ended is answered with nothing: nobody is left to read it.
func writeOpsError(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil {
		return
	}
	if ferr, ok := errors.AsType[*forgeapi.Error](err); ok {
		writeForgeAPIError(w, ferr)
		return
	}
	if writeContextError(w, err) {
		return
	}
	if errors.Is(err, errNotSupported) {
		webhttp.WriteJSONStatus(w, http.StatusNotImplemented,
			httpreply.ErrorJSONWithCode(err.Error(), codeNotSupported))
		return
	}
	slog.Debug("forges: ops error", "error", logsafe.Field(err.Error()))
	webhttp.WriteJSONStatus(w, http.StatusInternalServerError,
		httpreply.ErrorJSON(err.Error()))
}

// parseID reads a connection id back into its kind and host, refusing any id
// MakeID would not mint, a hashed one included.
func parseID(id string) (Kind, string, bool) {
	parts, err := keyenc.Split(id)
	if err != nil || len(parts) != 2 {
		return "", "", false
	}
	kind, host := Kind(parts[0]), parts[1]
	if !kind.valid() || !validHost(host) || MakeID(kind, host) != id {
		return "", "", false
	}
	return kind, host, true
}

// splitFirst splits s at the first '/' separator, returning
// (head, tail, found). If '/' is not present, returns (s, "", false).
func splitFirst(s string) (head, tail string, found bool) {
	return strings.Cut(s, "/")
}
