package agent

// The MCP OAuth loopback relay. KAS runs the flow and binds its redirect listener on
// `http://localhost:<ephemeral>/oauth/callback` in the container, which a remote browser cannot
// reach. The user pastes the dead page's address and marotte replays the GET from inside.
// The pasted address is untrusted; the stored authorization URL KAS wrote is the anchor: the
// dial target comes from its `redirect_uri` and `state` must match, or this unauthenticated
// route injects authorization codes. Rewriting redirect_uri fails RFC 6749 §4.1.3.

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/ssrf/v4"
	"github.com/cplieger/webhttp/v3"
)

const (
	// relayURLCap bounds the pasted address.
	relayURLCap = 4096

	// relayMinPort refuses a privileged port: KAS's listener is ephemeral.
	relayMinPort = 1024

	// relayDialTimeout and relayTotalTimeout bound the replay to a listener in this container.
	relayDialTimeout  = 3 * time.Second
	relayTotalTimeout = 8 * time.Second

	// relayBodyCap bounds the response read and discarded.
	relayBodyCap = 64 << 10
)

// relayQueryKeys allowlists the forwarded query keys: `code`/`state`, `scope` (RFC 6749
// §4.1.2), `iss` (RFC 9207) and OIDC `session_state`. An error redirect carries no code, so it is absent.
var relayQueryKeys = map[string]struct{}{
	"code":          {},
	"state":         {},
	"scope":         {},
	"iss":           {},
	"session_state": {},
}

// Refusal reasons, one value each so the handler maps a status and tests assert the reason.
var (
	errRelayNoFlow      = errors.New("no sign-in is waiting for this server")
	errRelayAlreadyDone = errors.New("this sign-in's callback was already delivered")
	errRelayTooLong     = errors.New("that address is too long to be a sign-in callback")
	errRelayBadBytes    = errors.New("that address contains characters a URL cannot carry")
	errRelayUnparsable  = errors.New("that does not look like a URL")
	errRelayNotHTTP     = errors.New("a sign-in callback is an http address")
	errRelayHasCreds    = errors.New("that address carries a username or password")
	errRelayHasFragment = errors.New("that address carries a #fragment")
	errRelayNotLoopback = errors.New("that address is not a local callback address")
	errRelayBadPort     = errors.New("that address has no usable port number")
	errRelayNoCode      = errors.New("that address carries no authorization code, so the sign-in did not complete")
	errRelayBadQuery    = errors.New("that address carries a query parameter this callback does not use")
	errRelayNoRedirect  = errors.New("this sign-in did not advertise a callback address, so there is nothing to relay to")
	errRelayNoState     = errors.New("this sign-in carries no state value, so a pasted callback cannot be verified against it")
	errRelayTargetDrift = errors.New("that address is not the callback address this sign-in asked for")
	errRelayStateDrift  = errors.New("that address belongs to a different sign-in")
)

// relayClientFor returns a client pinned to one validated callback's port, built per attempt.
// ssrf.SafeTransport re-validates the connected address, closing DNS rebinding via `localhost`.
func relayClientFor(target *url.URL) (*http.Client, error) {
	// parseLoopbackCallback already accepted this port; a mismatch is refused.
	port, err := strconv.ParseUint(target.Port(), 10, 16)
	if err != nil || port < relayMinPort {
		return nil, errRelayBadPort
	}
	tr := ssrf.SafeTransport(
		ssrf.WithAddressPolicy(func(a netip.Addr) bool { return a.IsLoopback() }),
		ssrf.WithAllowedPorts(uint16(port)),
		ssrf.WithDialer(&net.Dialer{Timeout: relayDialTimeout}),
	)
	tr.DisableKeepAlives = true
	return &http.Client{
		Timeout:   relayTotalTimeout,
		Transport: tr,
		// Never follow a redirect, so KAS's handler cannot steer the relay onward.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

type mcpOAuthRelayReq struct {
	Server      string `json:"server"`
	RedirectURL string `json:"redirect_url"`
}

type mcpOAuthRelayResp struct {
	// Status is the loopback listener's HTTP status, so the UI says what answered.
	Status int `json:"status"`
}

// handleOAuthRelay serves POST /api/mcp/oauth-relay {server, redirect_url}, replaying a stranded
// callback into KAS's listener. Not LoopbackOnly: a remote browser must reach it; the loopback
// constraint is on the outbound dial.
func (reg *mcpRegistry) handleOAuthRelay(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	var body mcpOAuthRelayReq
	if !httpreply.DecodeJSON(w, req, &body) {
		return
	}

	// Reserve first and derive everything from the reservation: recordOAuth can replace the record
	// meanwhile, and a code validated against attempt A must never replay under B. An unknown server is refused here too.
	attempt, err := reg.beginOAuthRelay(body.Server)
	if err != nil {
		httpreply.Conflict(w, err.Error())
		return
	}

	target, err := validateRelayAddress(body.RedirectURL, attempt.authURL)
	if err != nil {
		reg.releaseOAuthRelay(attempt)
		httpreply.BadRequest(w, err.Error())
		return
	}

	status, err := replayCallback(req.Context(), target)
	if err != nil {
		reg.releaseOAuthRelay(attempt)
		slog.Warn("mcp oauth relay: could not reach the loopback callback listener",
			"server", body.Server, "port", target.Port(), "error", dialErrWithoutURL(err))
		webhttp.WriteJSONStatus(w, http.StatusBadGateway,
			httpreply.ErrorJSON("the local sign-in listener did not answer, so the code was not delivered. The sign-in may have timed out"))
		return
	}
	if status >= http.StatusBadRequest {
		// Refused: give the reservation back so a corrected paste can retry.
		reg.releaseOAuthRelay(attempt)
		slog.Warn("mcp oauth relay: the loopback listener refused the callback",
			"server", body.Server, "port", target.Port(), "status", status)
		webhttp.WriteJSONStatus(w, http.StatusBadGateway,
			httpreply.ErrorJSON("the local sign-in listener rejected that callback with HTTP "+strconv.Itoa(status)+". Start the sign-in again"))
		return
	}

	slog.Info("mcp oauth relay: delivered a stranded callback to the loopback listener",
		"server", body.Server, "port", target.Port(), "status", status)
	// KAS reports the connected state over _kiro/mcp/status; the client waits for that frame.
	webhttp.WriteJSON(w, mcpOAuthRelayResp{Status: status})
}

// replayCallback performs the one GET and returns the listener's status.
func replayCallback(ctx context.Context, target *url.URL) (int, error) {
	client, err := relayClientFor(target)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), http.NoBody)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	// Drain a bounded prefix so a hostile listener cannot stream forever; keep-alives are off.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, relayBodyCap))
	return resp.StatusCode, nil
}

// dialErrWithoutURL strips the request URL from a client error before logging: *url.Error's
// message embeds the URL, whose query is the authorization code.
func dialErrWithoutURL(err error) error {
	// `go fix -errorsastype` and gocritic skip this site: a clause of a boolean expression.
	if ue, ok := errors.AsType[*url.Error](err); ok && ue.Err != nil {
		return ue.Err
	}
	return err
}

// validateRelayAddress checks a pasted callback against KAS's advertised authorization URL and
// returns the URL to replay: the advertised scheme, host and path with the pasted query, so the
// host is never read off the paste. Pure, and it only refuses, never repairs.
func validateRelayAddress(pasted, authURL string) (*url.URL, error) {
	u, err := parseLoopbackCallback(strings.TrimSpace(pasted))
	if err != nil {
		return nil, err
	}
	q, err := validateCallbackQuery(u.RawQuery)
	if err != nil {
		return nil, err
	}

	advertised, err := matchAdvertisedCallback(u, q.Get("state"), authURL)
	if err != nil {
		return nil, err
	}
	advertised.RawQuery = u.RawQuery
	return advertised, nil
}

// parseLoopbackCallback checks for a plain-http, credential-free, fragment-free loopback URL on
// an unprivileged port; the paste and the advertised redirect_uri share it.
func parseLoopbackCallback(raw string) (*url.URL, error) {
	if len(raw) > relayURLCap {
		return nil, errRelayTooLong
	}
	// Before parsing: url.Parse accepts control bytes, and percent-decoding can reintroduce them.
	if !isPrintableASCII(raw) {
		return nil, errRelayBadBytes
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errRelayUnparsable
	}
	if !strings.EqualFold(u.Scheme, "http") {
		return nil, errRelayNotHTTP
	}
	if u.User != nil {
		return nil, errRelayHasCreds
	}
	if u.Fragment != "" || u.RawFragment != "" || strings.Contains(raw, "#") {
		return nil, errRelayHasFragment
	}
	if !isLoopbackHost(u.Hostname()) {
		return nil, errRelayNotLoopback
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < relayMinPort || port > 65535 {
		return nil, errRelayBadPort
	}
	return u, nil
}

// validateCallbackQuery requires every key allowlisted and a non-empty code.
func validateCallbackQuery(rawQuery string) (url.Values, error) {
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return nil, errRelayUnparsable
	}
	for k := range q {
		if _, allowed := relayQueryKeys[k]; !allowed {
			return nil, errRelayBadQuery
		}
	}
	if q.Get("code") == "" {
		return nil, errRelayNoCode
	}
	return q, nil
}

// matchAdvertisedCallback binds a paste to KAS's authorization URL and returns the advertised
// callback to dial: the route's whole security argument. Port and path must agree; host is
// the advertisement's. A missing stored `state` is a refusal, never a waiver.
func matchAdvertisedCallback(pasted *url.URL, pastedState, authURL string) (*url.URL, error) {
	auth, err := url.Parse(authURL)
	if err != nil {
		return nil, errRelayNoRedirect
	}
	aq := auth.Query()

	redirect := aq.Get("redirect_uri")
	if redirect == "" {
		return nil, errRelayNoRedirect
	}
	// Held to the paste's shape because the dial goes here; any refusal is errRelayNoRedirect, which the user cannot fix.
	want, err := parseLoopbackCallback(redirect)
	if err != nil {
		return nil, errRelayNoRedirect
	}
	if want.Port() != pasted.Port() || want.EscapedPath() != pasted.EscapedPath() {
		return nil, errRelayTargetDrift
	}

	wantState := aq.Get("state")
	if wantState == "" {
		return nil, errRelayNoState
	}
	// Constant-time: an early exit would leak a secret-equivalent value to a retrying caller.
	if len(wantState) != len(pastedState) ||
		subtle.ConstantTimeCompare([]byte(wantState), []byte(pastedState)) != 1 {
		return nil, errRelayStateDrift
	}
	// url.Clone: the caller overwrites RawQuery, and a struct copy would share User.
	return want.Clone(), nil
}

// isLoopbackHost reports whether host is one of the three loopback spellings. A fixed set, not a
// lookup: relayClientFor re-validates at socket time.
func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return false
}

// isPrintableASCII reports whether s is printable ASCII without space: any other byte could split the request line.
func isPrintableASCII(s string) bool {
	for i := range len(s) {
		if s[i] <= 0x20 || s[i] >= 0x7f {
			return false
		}
	}
	return true
}
