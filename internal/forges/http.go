package forges

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/webhttp/v3"
)

const codeNotSupported = "not_supported"

// *agent.Runtime satisfies it.
type broadcaster interface {
	Broadcast(ctx context.Context, evt marotte.ServerEvent)
}

// HTTPHandler exposes the forges package over HTTP.
type HTTPHandler struct {
	manager     *Manager
	broadcaster broadcaster
	onChange    func()
	// grants are the device grants in progress, which only this process can
	// poll: a restart loses them and the sign-in starts again.
	grants *grantRegistry
	// poller holds the pull-request inventory and runs its cycles.
	poller       *PRStatusPoller
	probeTimeout time.Duration
}

// NewHTTPHandler builds an HTTPHandler with the given Manager.
// broadcaster may be nil; when nil, forge change events are silently dropped.
func NewHTTPHandler(m *Manager, b broadcaster) *HTTPHandler {
	return &HTTPHandler{
		manager:      m,
		broadcaster:  b,
		probeTimeout: 20 * time.Second,
		grants:       newGrantRegistry(),
	}
}

// SetOnChange wires a callback fired whenever a forge connection changes (login, OAuth completion,
// disconnect, refresh), used to refresh the steering forge-snapshot cache. It must not block or
// capture the request context: it runs on the request path.
func (h *HTTPHandler) SetOnChange(fn func()) { h.onChange = fn }

// SetPoller wires the poller whose inventory the inventory routes answer from.
// Called once at composition, before the routes serve.
func (h *HTTPHandler) SetPoller(p *PRStatusPoller) { h.poller = p }

// NotifyChanged broadcasts a forges_changed event so the forge panels
// refresh, then fires the onChange callback. No-op parts are skipped when
// unwired.
func (h *HTTPHandler) NotifyChanged(ctx context.Context) {
	if h.broadcaster != nil {
		h.broadcaster.Broadcast(ctx, marotte.NewEvent(marotte.EventForgesChanged, "", marotte.ForgesChangedPayload{}))
	}
	if h.onChange != nil {
		h.onChange()
	}
}

// Detection and a device-grant start share one bucket: any caller can send
// either, and each sends requests to a forge.
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
	mux.Handle("/api/forges/detect", outbound(http.HandlerFunc(h.handleDetect)))
	mux.HandleFunc("/api/forges/inventory", h.handleInventory)
	mux.HandleFunc("/api/forges/inventory/refresh", h.handleInventoryRefresh)
	mux.HandleFunc("/api/forges/inventory/watch", h.handleInventoryWatch)
	mux.Handle("/api/forges/oauth/", outbound(http.HandlerFunc(h.handleDeviceGrant)))
}

func sendsOutbound(r *http.Request) bool {
	return r.Method == http.MethodPost &&
		(r.URL.Path == "/api/forges/detect" || strings.HasSuffix(r.URL.Path, "/start"))
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
	webhttp.WriteJSON(w, h.poller.inventoryFor(h.manager.List(r.Context())))
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
	forges := h.manager.List(r.Context())
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
	op, rest, _ := splitFirst(sub)
	// A login is how a forge that has no row yet gets one.
	if op == "login" {
		h.handleLogin(w, r, id, rest)
		return
	}
	if h.manager.get(id) == nil {
		httpreply.NotFound(w, "unknown forge id")
		return
	}
	if sub == "" {
		switch r.Method {
		case http.MethodGet:
			f := h.manager.get(id)
			webhttp.WriteJSON(w, f)
		case http.MethodDelete:
			h.handleDisconnect(w, r, id)
		default:
			httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodDelete)
		}
		return
	}
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

func (h *HTTPHandler) handleDisconnect(w http.ResponseWriter, r *http.Request, id string) {
	f := h.manager.get(id)
	if f == nil {
		httpreply.NotFound(w, "unknown forge")
		return
	}
	if err := h.manager.disconnect(r.Context(), f); err != nil {
		if errors.Is(err, errConnectionUnusable) {
			webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON(err.Error()))
			return
		}
		httpreply.ServerError(w, "disconnect failed", err)
		return
	}
	h.manager.invalidate()
	_ = h.manager.Refresh(r.Context())
	h.NotifyChanged(r.Context())
	webhttp.Ok(w)
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
	var body ownersBody
	webhttp.LimitBody(w, r, webhttp.MaxJSONBody)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Owners == nil {
		webhttp.WriteJSONStatus(w, http.StatusBadRequest,
			httpreply.ErrorJSONWithCode("the body must be {owners: [string, ...]}", codeOwnersInvalid))
		return
	}
	f := h.manager.get(id)
	if f == nil {
		httpreply.NotFound(w, "unknown forge")
		return
	}
	stored, err := h.manager.setOwnerScopes(r.Context(), f, *body.Owners)
	if err != nil {
		writeOwnersError(w, r, err)
		return
	}
	h.NotifyChanged(r.Context())
	// The inventory's added scopes are the stored owners, so the change shows at
	// the next cycle rather than a poll interval later.
	h.askCycle()
	webhttp.WriteJSON(w, OwnerScopes{Owners: stored})
}

// writeOwnersError answers a refused owners write. An owner the library refuses
// is its own envelope, with the message naming which owner it was.
func writeOwnersError(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil {
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
	case errors.Is(err, errConnectionUnusable):
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON(err.Error()))
	case errors.Is(err, errNoRecord):
		httpreply.NotFound(w, err.Error())
	default:
		httpreply.ServerError(w, "storing the owners failed", err)
	}
}

// connectionClient answers connection id's client, or writes why there is none:
// a record file or store the server cannot use is a 503, anything else a 404.
func (h *HTTPHandler) connectionClient(w http.ResponseWriter, id string) (forgeClient, bool) {
	fc, err := h.manager.client(id)
	if errors.Is(err, errConnectionUnusable) {
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON(err.Error()))
		return forgeClient{}, false
	}
	if err != nil {
		httpreply.NotFound(w, err.Error())
		return forgeClient{}, false
	}
	return fc, true
}

// A failed read of either scope answers its envelope, never a partial verdict.
func (h *HTTPHandler) handleCapabilities(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	fc, ok := h.connectionClient(w, id)
	if !ok {
		return
	}
	conn, err := fc.core.ConnectionCaps(r.Context())
	if err != nil {
		writeOpsError(w, r, err)
		return
	}
	grant, err := fc.core.GrantCaps(r.Context())
	if err != nil {
		writeOpsError(w, r, err)
		return
	}
	webhttp.WriteJSON(w, capabilitiesOf(conn, grant))
}

func (h *HTTPHandler) handleProbe(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.probeTimeout)
	defer cancel()
	err := h.manager.probeConnection(ctx, id)
	f := h.manager.get(id)
	res := ProbeResult{Forge: f, Connected: f != nil && f.Connected}
	switch {
	case err == nil:
	case f != nil && f.LastError != "":
		res.Error = f.LastError
	default:
		res.Error = logsafe.Field(err.Error())
	}
	webhttp.WriteJSON(w, res)
}

// handleLogin handles POST /api/forges/{id}/login/pat — the user-
// supplied PAT path. id is the forge ID we're logging into; the
// kind+host are derived from id.
func (h *HTTPHandler) handleLogin(w http.ResponseWriter, r *http.Request, id, sub string) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	op, _, _ := splitFirst(sub)
	if op != "pat" {
		httpreply.NotFound(w, "unknown login method")
		return
	}
	kind, host := splitID(id)
	if !kind.valid() {
		httpreply.BadRequest(w, "invalid forge id")
		return
	}
	var body patBody
	webhttp.LimitBody(w, r, webhttp.MaxJSONBody)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpreply.BadRequest(w, "invalid json")
		return
	}
	if err := h.loginPAT(r.Context(), kind, host, &body); err != nil {
		writeLoginError(w, r, err)
		return
	}
	h.manager.invalidate()
	_ = h.manager.Refresh(r.Context())
	h.NotifyChanged(r.Context())
	webhttp.WriteJSON(w, map[string]string{"status": stateComplete})
}

// patBody is a token connect: the token and, for a self-managed instance, how
// to reach it.
type patBody struct {
	Token string `json:"token"`
	connectionFields
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

func (h *HTTPHandler) loginPAT(ctx context.Context, kind Kind, host string, body *patBody) error {
	rec, err := body.record(kind, host)
	if err != nil {
		return err
	}
	return h.manager.connect(ctx, &rec, body.Token)
}

// codeWebBaseInvalid answers a web base URL that is not the scheme and host the
// connection id names.
const codeWebBaseInvalid = "web_base_url_invalid"

var (
	errWebBaseInvalid = errors.New("forges: web_base_url must be the scheme and host:port the forge id names")
	errHostInvalid    = errors.New("forges: the forge id's host is not a host[:port]")
)

// The id's host, and the web base URL when one is given, must each be a bare origin on that host
// and port, so a token is never sent to an instance the id does not name.
func (b *connectionFields) record(kind Kind, host string) (connectionRecord, error) {
	if host == "" {
		host = kind.defaultHost()
	}
	if !originOn("https://"+host, host) {
		return connectionRecord{}, errHostInvalid
	}
	rec := b.recordOn(host)
	rec.ID, rec.Kind, rec.APIBaseURL = MakeID(kind, host), kind, b.APIBaseURL
	if b.WebBaseURL == "" {
		return rec, nil
	}
	if !originOn(b.WebBaseURL, host) {
		return connectionRecord{}, errWebBaseInvalid
	}
	u, _ := url.Parse(b.WebBaseURL)
	rec.WebBaseURL = u.Scheme + "://" + host
	return rec, nil
}

func (f *trustFields) recordOn(host string) connectionRecord {
	return connectionRecord{
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

// writeLoginError answers a refused login. A refusal of the credential or of
// the address is a 2xx {error, code} envelope: the client's action layer
// collapses any non-2xx to a generic network error, which would hide the reason
// from the PAT form. A store or record file the server cannot write is a 503.
func writeLoginError(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil {
		return
	}
	if errors.Is(err, errConnectionUnusable) {
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON(err.Error()))
		return
	}
	if ferr, ok := errors.AsType[*forgeapi.Error](err); ok {
		webhttp.WriteJSON(w, envelopeFor(ferr))
		return
	}
	if errors.Is(err, errWebBaseInvalid) {
		webhttp.WriteJSON(w, httpreply.ErrorJSONWithCode(err.Error(), codeWebBaseInvalid))
		return
	}
	if errors.Is(err, errHostInvalid) {
		httpreply.BadRequest(w, err.Error())
		return
	}
	webhttp.WriteJSON(w, httpreply.ErrorJSON(err.Error()))
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

// splitID parses "kind:host" → (kind, host). Returns ("", "") for
// malformed input — the caller should validate Kind.valid().
func splitID(id string) (kind Kind, ref string) {
	k, host, found := strings.Cut(id, ":")
	if !found {
		return "", ""
	}
	return Kind(k), host
}

// splitFirst splits s at the first '/' separator, returning
// (head, tail, found). If '/' is not present, returns (s, "", false).
func splitFirst(s string) (head, tail string, found bool) {
	return strings.Cut(s, "/")
}
