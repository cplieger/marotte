package forges

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

const (
	githubGrantStart  = "/api/forges/oauth/github/start"
	githubGrantPoll   = "/api/forges/oauth/github/poll"
	githubGrantCancel = "/api/forges/oauth/github/cancel"
	applianceClientID = "Iv1.appliance"
)

const approvedAnswer = `{"access_token":"token-new","token_type":"bearer","scope":"repo,read:org,workflow",` +
	`"expires_in":28800,"refresh_token":"refresh-new","refresh_token_expires_in":15897600}`

func (e *grantEndpoint) startBody(clientID string) string {
	return fmt.Sprintf(`{"host":%q,"client_id":%q,"web_base_url":%q,"private_addresses":true,"plaintext_http":true}`,
		e.host(), clientID, e.srv.URL)
}

func (e *grantEndpoint) host() string { return strings.TrimPrefix(e.srv.URL, "http://") }

func (e *grantEndpoint) id() string { return MakeID(KindGitHub, e.host()) }

func (h *connectHarness) startGrant(t *testing.T, e *grantEndpoint) string {
	t.Helper()
	rec := h.do(t, http.MethodPost, githubGrantStart, e.startBody(applianceClientID))
	body := decodeBody(t, rec)
	id, _ := body["grant_id"].(string)
	if rec.Code != http.StatusOK || id == "" {
		t.Fatalf("Setup: POST %s = %d %v, want a started grant", githubGrantStart, rec.Code, body)
	}
	return id
}

func (h *connectHarness) pollGrant(t *testing.T, ctx context.Context, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, githubGrantPoll,
		strings.NewReader(fmt.Sprintf(`{"grant_id":%q}`, id)))
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec
}

// Detection and a grant start draw on one bucket, and a poll never does: a
// client polls a grant it already holds every few seconds.
func TestOutboundProbes_ShareOneBucketThatPollsDoNotDraw(t *testing.T) {
	h := newConnectHarness(t, nil)
	for i := range outboundBurst {
		if rec := h.do(t, http.MethodPost, detectPath, `{"web_base_url":"not an origin"}`); rec.Code != http.StatusBadRequest {
			t.Fatalf("Setup: detect %d with no origin = %d, want 400", i, rec.Code)
		}
	}
	e := newGrantEndpoint(t, "900", pendingAnswer)
	rec := h.do(t, http.MethodPost, githubGrantStart, e.startBody(applianceClientID))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("POST %s after %d detections = %d (Retry-After %q), want 429 with a hint",
			githubGrantStart, outboundBurst, rec.Code, rec.Header().Get("Retry-After"))
	}
	poll := h.pollGrant(t, t.Context(), strings.Repeat("0", 32))
	if poll.Code == http.StatusTooManyRequests {
		t.Errorf("POST %s with the bucket empty = 429, want polls unthrottled", githubGrantPoll)
	}
}

// A start refused by the held-grant ceiling is a 429, which the dialog reads
// as try again later rather than as a broken instance.
func TestDeviceStart_PastTheCeilingIs429(t *testing.T) {
	e := newGrantEndpoint(t, "900", pendingAnswer)
	h := newConnectHarness(t, nil)
	for range maxHeldGrants {
		startTestGrant(t, h.handler.grants, e)
	}
	rec := h.do(t, http.MethodPost, githubGrantStart, e.startBody(applianceClientID))
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("POST %s with %d grants held = %d %v, want 429", githubGrantStart, maxHeldGrants, rec.Code, decodeBody(t, rec))
	}
}

func TestDeviceStart_AnswersAGrantIDAndNoDeviceCode(t *testing.T) {
	e := newGrantEndpoint(t, "900", pendingAnswer)
	h := newConnectHarness(t, nil)

	rec := h.do(t, http.MethodPost, githubGrantStart, e.startBody(applianceClientID))

	body := decodeBody(t, rec)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST %s = %d %v, want 200", githubGrantStart, rec.Code, body)
	}
	if id, _ := body["grant_id"].(string); !grantIDShape.MatchString(id) {
		t.Errorf("start grant_id = %v, want a 16-byte hex id", body["grant_id"])
	}
	if _, ok := body["device_code"]; ok || strings.Contains(rec.Body.String(), testDeviceCode) {
		t.Errorf("start body = %s, which carries the device code", rec.Body)
	}
	if body["user_code"] != "ABCD-1234" || body["verification_uri"] != "https://github.com/login/device" ||
		body["interval"] != float64(5) || body["expires_in"] != float64(900) {
		t.Errorf("start body = %v, want the code answer's user code, URI, interval 5 and expires_in 900", body)
	}
	e.mu.Lock()
	form := e.codeForm
	e.mu.Unlock()
	if form.Get("client_id") != applianceClientID || form.Get("scope") != "repo read:org workflow" {
		t.Errorf("the device-code request named client %q scopes %q, want the appliance's application and repo read:org workflow",
			form.Get("client_id"), form.Get("scope"))
	}
}

func TestDeviceGrant_CompleteSavesTheRecordWithItsAccount(t *testing.T) {
	e := newGrantEndpoint(t, "900", approvedAnswer)
	h := newConnectHarness(t, nil)
	grant := h.startGrant(t, e)

	rec := h.pollGrant(t, t.Context(), grant)

	if body := decodeBody(t, rec); rec.Code != http.StatusOK || body["status"] != stateComplete {
		t.Fatalf("poll of an approved grant = %d %v, want 200 complete", rec.Code, body)
	}
	cred, ok, err := h.m.store.Load(e.id())
	if err != nil || !ok {
		t.Fatalf("store.Load(%s) = %v, %v; want the saved credential", e.id(), ok, err)
	}
	if cred.Kind != forgeapi.CredKindRotatingOAuth || cred.Token != "token-new" || cred.RefreshToken != "refresh-new" ||
		cred.Account != "alice" || cred.ClientID != applianceClientID || cred.WebBaseURL != e.srv.URL ||
		cred.Family != forgeapi.FamilyGitHub {
		t.Errorf("stored credential = %+v, want the rotating grant for alice from the appliance's application", cred)
	}
	e.mu.Lock()
	auth := e.userAuth
	e.mu.Unlock()
	if auth != "Bearer token-new" {
		t.Errorf("the identity read carried Authorization %q, want the grant's token", auth)
	}
	recs, err := h.m.conns.load()
	if err != nil || len(recs) != 1 || recs[0].OAuthClientID != applianceClientID || recs[0].WebBaseURL != e.srv.URL ||
		!recs[0].PlaintextHTTP || !recs[0].PrivateAddresses {
		t.Errorf("connection records = %+v, %v; want the appliance's record naming its application", recs, err)
	}
	if got, want := helperValuesFor(t, e.srv.URL), []string{"", h.helperValue(t)}; !slices.Equal(got, want) {
		t.Errorf("credential.%s.helper = %q, want %q", e.srv.URL, got, want)
	}
	if row := h.m.get(e.id()); row == nil || !row.Connected || row.Username != "alice" {
		t.Errorf("row after the grant = %+v, want alice connected", row)
	}
	if again := decodeBody(t, h.pollGrant(t, t.Context(), grant)); again["code"] != codeGrantNotFound {
		t.Errorf("a second poll of an answered grant = %v, want %s", again, codeGrantNotFound)
	}
}

// The token is spent once the grant answers it, so a client walking away while
// the credential is verified must not lose the connection it approved.
func TestDeviceGrant_ApprovalIsSavedWhenTheClientWalksAway(t *testing.T) {
	e := newGrantEndpoint(t, "900", approvedAnswer)
	h := newConnectHarness(t, nil)
	grant := h.startGrant(t, e)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	e.mu.Lock()
	e.onUser = cancel
	e.mu.Unlock()

	rec := h.pollGrant(t, ctx, grant)

	if rec.Body.Len() != 0 {
		t.Errorf("poll answered %q to a client that walked away, want nothing", rec.Body)
	}
	if cred, ok, err := h.m.store.Load(e.id()); err != nil || !ok || cred.Token != "token-new" {
		t.Errorf("store.Load(%s) = %+v, %v, %v; want the approved credential saved", e.id(), cred, ok, err)
	}
}

func TestDeviceGrant_EnterpriseHostWithoutClientIDIsRefused(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"another instance naming no application", `{"host":"ghe.example.com"}`},
		{"the public instance naming an application", `{"client_id":"Iv1.someone-else"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newConnectHarness(t, nil)

			rec := h.do(t, http.MethodPost, githubGrantStart, tc.body)

			if body := decodeBody(t, rec); rec.Code != http.StatusBadRequest || body["code"] != codeClientIDInvalid {
				t.Errorf("POST %s %s = %d %v, want 400 %s", githubGrantStart, tc.body, rec.Code, body, codeClientIDInvalid)
			}
			if n := h.handler.grants.held(); n != 0 {
				t.Errorf("the registry holds %d grants after a refused start, want 0", n)
			}
		})
	}
}

func TestDeviceStart_DegradedStoreAnswers503(t *testing.T) {
	e := newGrantEndpoint(t, "900", pendingAnswer)
	h := newConnectHarness(t, nil)
	h.m.store, h.m.storeReason = nil, "the forge credential store /x cannot be used"

	rec := h.do(t, http.MethodPost, githubGrantStart, e.startBody(applianceClientID))

	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "/x cannot be used") {
		t.Errorf("start over a degraded store = %d %s, want 503 naming the reason", rec.Code, rec.Body)
	}
	e.mu.Lock()
	form := e.codeForm
	e.mu.Unlock()
	if form != nil {
		t.Errorf("a start the server cannot save sent the device-code request %v", form)
	}
}

func TestDevicePoll_ConcurrentRouteCallsAreSerialized(t *testing.T) {
	e := newGrantEndpoint(t, "900", slowDownAnswer)
	h := newConnectHarness(t, nil)
	grant := h.startGrant(t, e)
	held, release := e.holdFirstToken(t)
	entered := secondPollEntered(h.handler.grants)

	answers := make(chan *httptest.ResponseRecorder, 2)
	go func() { answers <- h.pollGrant(t, t.Context(), grant) }()
	<-held
	go func() { answers <- h.pollGrant(t, t.Context(), grant) }()
	<-entered
	release()
	for range 2 {
		rec := <-answers
		if body := decodeBody(t, rec); rec.Code != http.StatusOK || body["status"] != statePending {
			t.Errorf("poll = %d %v, want 200 pending", rec.Code, body)
		}
	}

	if got := e.tokens.Load(); got != 2 {
		t.Errorf("two polls sent %d token requests, want 2", got)
	}
}

func TestDevicePoll_DenialAndExpiryAreTerminalStatuses(t *testing.T) {
	cases := []struct {
		answer, status, code string
	}{
		{`{"error":"access_denied"}`, grantDenied, forgeapi.CodeGrantDenied},
		{`{"error":"expired_token"}`, grantExpired, forgeapi.CodeGrantExpired},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			e := newGrantEndpoint(t, "900", tc.answer)
			h := newConnectHarness(t, nil)
			grant := h.startGrant(t, e)

			rec := h.pollGrant(t, t.Context(), grant)

			if body := decodeBody(t, rec); rec.Code != http.StatusOK || body["status"] != tc.status || body["code"] != tc.code {
				t.Errorf("poll answered by %s = %d %v, want 200 status %s code %s", tc.answer, rec.Code, body, tc.status, tc.code)
			}
		})
	}
}

// A failure the grant survives is a non-2xx, which the client backs off from
// and polls again; a terminal status there would end a sign-in that can still
// complete.
func TestDevicePoll_ServerFailureLeavesTheGrantPollable(t *testing.T) {
	e := newGrantEndpoint(t, "900", `{"error":"server_error"}`)
	e.tokenStatus = http.StatusServiceUnavailable
	h := newConnectHarness(t, nil)
	grant := h.startGrant(t, e)

	first := h.pollGrant(t, t.Context(), grant)
	if first.Code < 500 {
		t.Errorf("poll answered by a server failure = %d %s, want a 5xx the client retries", first.Code, first.Body)
	}
	h.pollGrant(t, t.Context(), grant)

	if got := e.tokens.Load(); got != 2 {
		t.Errorf("two polls of a grant the server failure left open sent %d token requests, want 2", got)
	}
}

func TestDeviceCancel_LaterPollIsNotFound(t *testing.T) {
	e := newGrantEndpoint(t, "900", pendingAnswer)
	h := newConnectHarness(t, nil)
	grant := h.startGrant(t, e)

	cancelled := h.do(t, http.MethodPost, githubGrantCancel, fmt.Sprintf(`{"grant_id":%q}`, grant))
	if cancelled.Code != http.StatusNoContent {
		t.Errorf("POST %s = %d %s, want 204", githubGrantCancel, cancelled.Code, cancelled.Body)
	}
	rec := h.pollGrant(t, t.Context(), grant)

	if body := decodeBody(t, rec); rec.Code != http.StatusOK || body["status"] != statusError || body["code"] != codeGrantNotFound {
		t.Errorf("poll after cancel = %d %v, want 200 status error code %s", rec.Code, body, codeGrantNotFound)
	}
	if got := e.tokens.Load(); got != 0 {
		t.Errorf("a cancelled grant sent %d token requests, want none", got)
	}
}

func TestForgesList_OffersDeviceSignInOnGitHubAndGitLab(t *testing.T) {
	h := newConnectHarness(t, nil)

	body := decodeBody(t, h.do(t, http.MethodGet, "/api/forges", ""))

	if got, _ := body["oauth"].(map[string]any); got["github"] != true || got["gitlab"] != true {
		t.Errorf("GET /api/forges oauth = %v, want github true and gitlab true", got)
	}
}

func TestGitLabGrant_PublicInstanceUsesMarottesApplication(t *testing.T) {
	h := newConnectHarness(t, nil)

	rec, req, err := h.handler.grantFor(KindGitLab, &grantBody{})
	if err != nil {
		t.Fatalf("grantFor(gitlab, no host) = %v, want Marotte's application on gitlab.com", err)
	}

	const marotteGitLabApp = "55c83c54905fe86fc799b972eed5fe8c3af32f59bf7ac35d70b92240d45403ea"
	if rec.Host != "gitlab.com" || rec.OAuthClientID != "" {
		t.Errorf("grantFor(gitlab) record host %q client %q, want gitlab.com with no client of its own", rec.Host, rec.OAuthClientID)
	}
	if req.ClientID != marotteGitLabApp || !slices.Equal(req.Scopes, []string{"api"}) {
		t.Errorf("grantFor(gitlab) request = client %q scopes %v, want %s with scope api", req.ClientID, req.Scopes, marotteGitLabApp)
	}
}

func TestDeviceGrant_FamilyWithoutOneIsNotFound(t *testing.T) {
	for _, kind := range []string{"gitea", "codeberg", "bogus"} {
		t.Run(kind, func(t *testing.T) {
			h := newConnectHarness(t, nil)

			rec := h.do(t, http.MethodPost, "/api/forges/oauth/"+kind+"/start", `{}`)

			if rec.Code != http.StatusNotFound || h.handler.grants.held() != 0 {
				t.Errorf("POST /api/forges/oauth/%s/start = %d with %d grants held, want 404 and none", kind, rec.Code, h.handler.grants.held())
			}
		})
	}
}

func TestGitLabGrant_SelfManagedInstanceNeedsItsOwnClientID(t *testing.T) {
	h := newConnectHarness(t, nil)

	if _, _, err := h.handler.grantFor(KindGitLab, &grantBody{Host: "gitlab.example.com"}); !errors.Is(err, errClientIDNeeded) {
		t.Errorf("grantFor(gitlab.example.com, no client id) = %v, want errClientIDNeeded", err)
	}
	rec, req, err := h.handler.grantFor(KindGitLab, &grantBody{Host: "gitlab.example.com", ClientID: "admin-app"})
	if err != nil || rec.OAuthClientID != "admin-app" || req.ClientID != "admin-app" || !slices.Equal(req.Scopes, []string{"api"}) {
		t.Errorf("grantFor(gitlab.example.com, admin-app) = record client %q, request %+v, %v; want admin-app with scope api",
			rec.OAuthClientID, req, err)
	}
}
