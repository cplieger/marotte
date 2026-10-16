package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/signin"
)

const (
	keyClientID     = "clientId"
	keyClientSecret = "clientSecret"
	clientName      = "marotte"
	deviceGrant     = "urn:ietf:params:oauth:grant-type:device_code"
	refreshGrant    = "refresh_token"
	maxBodyBytes    = 1 << 20
	httpTimeout     = 30 * time.Second
	defaultTokenTTL = time.Hour
	// maxTokenTTL bounds a server-given lifetime, so a hostile expiresIn cannot overflow a deadline.
	maxTokenTTL = 7 * 24 * time.Hour
	// maxPollTransportFailures is how many consecutive network failures a device poll rides out.
	maxPollTransportFailures = 3
	// slowDownSteps is RFC 8628 section 3.5: slow_down adds five seconds to the interval.
	slowDownSteps = 5
)

var oidcScopes = []string{"codewhisperer:completions", "codewhisperer:analysis", "codewhisperer:conversations"}

// User text for the device flow's failures.
const (
	textOIDCUnreachable = "marotte could not reach the AWS sign-in service. Try again."
	textDenied          = "The sign-in was declined in the browser."
	textExpired         = "The sign-in code expired before it was approved. Start the sign-in again."
	textOIDCRefused     = "The AWS sign-in service refused the sign-in."
)

// newHTTPClient never follows a redirect: a 3xx is the response, classified as a transport failure,
// so a body carrying a refresh token or client secret is never replayed to another host.
func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout:       httpTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// response is one HTTP answer. body is read only for a 2xx or 4xx; any other status is judged by
// its status line alone.
type response struct {
	header http.Header
	body   []byte
	status int
}

func (b *Backend) do(req *http.Request) (response, error) {
	req.Header.Set("User-Agent", clientName)
	resp, err := b.client.Do(req)
	if err != nil {
		return response{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	out := response{status: resp.StatusCode, header: resp.Header}
	if resp.StatusCode/100 != 2 && resp.StatusCode/100 != 4 {
		return out, nil
	}
	out.body, err = io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return response{}, err
	}
	if len(out.body) > maxBodyBytes {
		return response{}, fmt.Errorf("kiro: a response body exceeded %d bytes", maxBodyBytes)
	}
	return out, nil
}

func (b *Backend) postJSON(ctx context.Context, url string, payload any) (response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return response{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	return b.do(req)
}

type clientRegistration struct {
	ClientID              string `json:"clientId"`
	ClientSecret          string `json:"clientSecret"`
	ClientSecretExpiresAt int64  `json:"clientSecretExpiresAt"`
}

func (b *Backend) registerClient(ctx context.Context, oidcBase string) (clientRegistration, error) {
	resp, err := b.postJSON(ctx, oidcBase+"/client/register", map[string]any{
		"clientName": clientName,
		"clientType": "public",
		"scopes":     oidcScopes,
	})
	if err != nil {
		return clientRegistration{}, err
	}
	if resp.status != http.StatusOK {
		return clientRegistration{}, fmt.Errorf("kiro: client registration answered %d", resp.status)
	}
	var reg clientRegistration
	if err := json.Unmarshal(resp.body, &reg); err != nil {
		return clientRegistration{}, fmt.Errorf("kiro: client registration: %w", err)
	}
	if reg.ClientID == "" || reg.ClientSecret == "" {
		return clientRegistration{}, errors.New("kiro: client registration returned no client")
	}
	return reg, nil
}

type deviceAuthorization struct {
	DeviceCode              string `json:"deviceCode"`
	UserCode                string `json:"userCode"`
	VerificationURI         string `json:"verificationUri"`
	VerificationURIComplete string `json:"verificationUriComplete"`
	ExpiresIn               int64  `json:"expiresIn"`
	Interval                int64  `json:"interval"`
}

func (b *Backend) authorizeDevice(ctx context.Context, oidcBase string, reg clientRegistration, startURL string) (deviceAuthorization, error) {
	resp, err := b.postJSON(ctx, oidcBase+"/device_authorization", map[string]string{
		keyClientID:     reg.ClientID,
		keyClientSecret: reg.ClientSecret,
		"startUrl":      startURL,
	})
	if err != nil {
		return deviceAuthorization{}, err
	}
	if resp.status != http.StatusOK {
		return deviceAuthorization{}, fmt.Errorf("kiro: device authorization answered %d", resp.status)
	}
	return decodeDeviceAuthorization(resp.body)
}

func decodeDeviceAuthorization(body []byte) (deviceAuthorization, error) {
	var d deviceAuthorization
	if err := json.Unmarshal(body, &d); err != nil {
		return deviceAuthorization{}, fmt.Errorf("kiro: device authorization: %w", err)
	}
	if d.DeviceCode == "" || d.UserCode == "" || (d.VerificationURIComplete == "" && d.VerificationURI == "") {
		return deviceAuthorization{}, errors.New("kiro: device authorization is missing its codes")
	}
	if validStartURL(d.verificationURL()) != nil || !displayable(d.UserCode, 64) {
		return deviceAuthorization{}, errors.New("kiro: device authorization returned an unusable URL or code")
	}
	return d, nil
}

func (d *deviceAuthorization) verificationURL() string {
	if d.VerificationURIComplete != "" {
		return d.VerificationURIComplete
	}
	return d.VerificationURI
}

type tokenResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int64  `json:"expiresIn"`
}

// ttl is the server-given lifetime clamped to (0, maxTokenTTL], an absent one being an hour.
func (t tokenResponse) ttl() time.Duration {
	if t.ExpiresIn <= 0 {
		return defaultTokenTTL
	}
	return time.Duration(min(t.ExpiresIn, int64(maxTokenTTL/time.Second))) * time.Second
}

func decodeTokenResponse(body []byte) (tokenResponse, error) {
	var t tokenResponse
	if err := json.Unmarshal(body, &t); err != nil {
		return tokenResponse{}, fmt.Errorf("kiro: token response: %w", err)
	}
	if t.AccessToken == "" {
		return tokenResponse{}, errors.New("kiro: token response has no access token")
	}
	return t, nil
}

// errorCodeRe bounds an OAuth error code to what it may be, so a hostile one never reaches a log.
var errorCodeRe = regexp.MustCompile(`^[A-Za-z_]{1,64}$`)

// oauthError is the 4xx token answer's error code: the OAuth "error" member, else the AWS error
// type header (AuthorizationPendingException is authorization_pending).
func oauthError(resp response) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(resp.body, &e) == nil && errorCodeRe.MatchString(e.Error) {
		return e.Error
	}
	kind, _, _ := strings.Cut(resp.header.Get("X-Amzn-Errortype"), ":")
	switch kind {
	case "AuthorizationPendingException":
		return "authorization_pending"
	case "SlowDownException":
		return "slow_down"
	case "ExpiredTokenException":
		return "expired_token"
	case "AccessDeniedException":
		return "access_denied"
	}
	return "unknown"
}

func (b *Backend) createToken(ctx context.Context, oidcBase string, payload map[string]string) (response, error) {
	return b.postJSON(ctx, oidcBase+"/token", payload)
}

// pollToken waits for the user to approve st's device code, at the server's interval.
func (b *Backend) pollToken(ctx context.Context, st *deviceState) (tokenResponse, error) {
	interval := st.interval
	failures := 0
	for {
		if err := wait(ctx, interval); err != nil {
			return tokenResponse{}, err
		}
		if !b.now().Before(st.expiresAt) {
			return tokenResponse{}, &signin.FailureError{Text: textExpired}
		}
		tok, step, err := b.pollOnce(ctx, st)
		switch step {
		case stepDone:
			return tok, err
		case stepTransient:
			failures++
			if failures >= maxPollTransportFailures {
				return tokenResponse{}, &signin.FailureError{Text: textOIDCUnreachable, Err: err}
			}
			continue
		case stepSlowDown:
			interval += slowDownSteps * b.pollUnit
		case stepWait:
		}
		failures = 0
	}
}

type pollStep int

const (
	stepDone pollStep = iota
	stepWait
	stepSlowDown
	stepTransient
)

// pollOnce asks the token endpoint once: the token or the sign-in's failure (stepDone), keep
// polling (slower after slow_down), or a transport failure the poll rides out a few times.
func (b *Backend) pollOnce(ctx context.Context, st *deviceState) (tokenResponse, pollStep, error) {
	resp, err := b.createToken(ctx, st.ep.oidc, map[string]string{
		keyClientID:     st.clientID,
		keyClientSecret: st.clientSecret,
		"grantType":     deviceGrant,
		"deviceCode":    st.deviceCode,
	})
	if ctx.Err() != nil {
		return tokenResponse{}, stepDone, ctx.Err()
	}
	if err != nil || (resp.status != http.StatusOK && resp.status/100 != 4) {
		return tokenResponse{}, stepTransient, transportError(resp, err)
	}
	if resp.status == http.StatusOK {
		t, dErr := decodeTokenResponse(resp.body)
		if dErr != nil {
			return tokenResponse{}, stepDone, &signin.FailureError{Text: textOIDCRefused, Err: dErr}
		}
		return t, stepDone, nil
	}
	switch code := oauthError(resp); code {
	case "authorization_pending":
		return tokenResponse{}, stepWait, nil
	case "slow_down":
		return tokenResponse{}, stepSlowDown, nil
	case "expired_token":
		return tokenResponse{}, stepDone, &signin.FailureError{Text: textExpired}
	case "access_denied":
		return tokenResponse{}, stepDone, &signin.FailureError{Text: textDenied}
	default:
		return tokenResponse{}, stepDone, &signin.FailureError{Text: textOIDCRefused, Err: fmt.Errorf("kiro: the token endpoint answered %d %s", resp.status, code)}
	}
}

func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func transportError(resp response, err error) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("kiro: the sign-in service answered %d", resp.status)
}

// refreshToken renews r in place. A 400, 401 or 403 means the grant is dead; anything else is
// transient.
func (b *Backend) refreshToken(ctx context.Context, r *record) error {
	ep, err := b.resolve(r.Region)
	if err != nil {
		return err
	}
	resp, err := b.createToken(ctx, ep.oidc, map[string]string{
		keyClientID:     r.ClientID,
		keyClientSecret: r.ClientSecret,
		"grantType":     refreshGrant,
		"refreshToken":  r.RefreshToken,
	})
	if err != nil {
		return err
	}
	switch resp.status {
	case http.StatusOK:
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: the token endpoint answered %d %s", signin.ErrRejected, resp.status, oauthError(resp))
	default:
		return fmt.Errorf("kiro: the token endpoint answered %d", resp.status)
	}
	t, err := decodeTokenResponse(resp.body)
	if err != nil {
		return err
	}
	r.AccessToken = t.AccessToken
	r.ExpiresAt = b.now().Add(t.ttl()).UTC()
	if t.RefreshToken != "" {
		r.RefreshToken = t.RefreshToken
	}
	return nil
}
