// Package kiro is the sign-in backend for the Kiro agent server (KAS): an AWS SSO-OIDC device flow
// for Builder ID and IAM Identity Center, the Kiro profile lookup, and KAS's host-callback policy.
// KAS asks for a credential on _kiro/auth/getAccessToken when spawned with --auth-method client,
// and serves the token it was answered until 180 s before the expiry the answer advertised.
package kiro

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/signin"
)

// ID is the kiro backend's id.
const ID signin.BackendID = "kiro"

const (
	hostCallback     = "_kiro/auth/getAccessToken"
	builderIDRegion  = "us-east-1"
	defaultDeviceTTL = 10 * time.Minute
)

// The host-callback lease. KAS takes an answer only when its advertised expiry is more than 180 s
// out (doRefresh), and asks again at its first call inside those 180 s; advertising at most 240 s
// makes that call come 60 s or more after an answer, so an account change reaches it with no
// restart. A token is renewed when 300 s or less remain, so every answer advertises more than
// 180 s; after a failed renewal an unrenewed token is still served while more than 210 s remain.
const (
	leaseCap    = 240 * time.Second
	renewWithin = 300 * time.Second
	staleFloor  = 210 * time.Second
)

// Backend is the kiro sign-in backend.
type Backend struct {
	client       *http.Client
	resolve      func(region string) (endpoints, error)
	now          func() time.Time
	pollUnit     time.Duration
	emailTimeout time.Duration
}

var _ signin.Backend = (*Backend)(nil)

// New returns the kiro backend.
func New() *Backend {
	return &Backend{
		client:       newHTTPClient(),
		resolve:      endpointsFor,
		now:          time.Now,
		pollUnit:     time.Second,
		emailTimeout: emailTimeout,
	}
}

// Declaration is the kiro backend's.
func (*Backend) Declaration() signin.Declaration {
	return signin.Declaration{
		Backend:      ID,
		HostCallback: hostCallback,
		SpawnArgs:    []string{"--auth-method", "client"},
		Flows:        []signin.FlowKind{signin.FlowDevice},
	}
}

// deviceState is a begun device flow's continuation.
type deviceState struct {
	expiresAt             time.Time
	clientSecretExpiresAt time.Time
	ep                    endpoints
	region                string
	startURL              string
	clientID              string
	clientSecret          string
	deviceCode            string
	provider              provider
	interval              time.Duration
}

// Begin registers a client and starts a device authorization. An empty StartURL is Builder ID,
// which always signs in through us-east-1; otherwise Region ("" for us-east-1) is the portal's. A
// region endpointsFor refuses is refused before any request.
func (b *Backend) Begin(ctx context.Context, p signin.FlowParams) (signin.DeviceStart, error) {
	st := &deviceState{region: builderIDRegion, startURL: builderIDStartURL, provider: providerBuilderID}
	if p.StartURL != "" {
		if err := validStartURL(p.StartURL); err != nil {
			return signin.DeviceStart{}, err
		}
		if p.Region != "" {
			st.region = p.Region
		}
		st.startURL = p.StartURL
		st.provider = classifyStartURL(p.StartURL)
	}
	ep, err := b.resolve(st.region)
	if err != nil {
		return signin.DeviceStart{}, err
	}
	st.ep = ep
	reg, err := b.registerClient(ctx, ep.oidc)
	if err != nil {
		return signin.DeviceStart{}, &signin.FailureError{Text: textOIDCUnreachable, Err: err}
	}
	da, err := b.authorizeDevice(ctx, ep.oidc, reg, st.startURL)
	if err != nil {
		return signin.DeviceStart{}, &signin.FailureError{Text: textOIDCUnreachable, Err: err}
	}
	ttl := defaultDeviceTTL
	if da.ExpiresIn > 0 {
		ttl = time.Duration(min(da.ExpiresIn, int64(maxSignInTTL/time.Second))) * time.Second
	}
	st.clientID, st.clientSecret, st.deviceCode = reg.ClientID, reg.ClientSecret, da.DeviceCode
	if reg.ClientSecretExpiresAt > 0 {
		st.clientSecretExpiresAt = time.Unix(reg.ClientSecretExpiresAt, 0).UTC()
	}
	st.interval = time.Duration(max(min(da.Interval, maxIntervalSteps), 1)) * b.pollUnit
	st.expiresAt = b.now().Add(ttl)
	return signin.DeviceStart{
		State:           st,
		VerificationURL: da.verificationURL(),
		UserCode:        da.UserCode,
		ExpiresIn:       ttl,
	}, nil
}

const (
	// maxSignInTTL bounds a server-given device code lifetime.
	maxSignInTTL = time.Hour
	// maxIntervalSteps bounds a server-given poll interval.
	maxIntervalSteps = 60
)

// Poll waits for approval, then looks up the Kiro profile and the account email with the new token,
// all before the credential exists, so a SignOut cancelling ctx commits nothing.
func (b *Backend) Poll(ctx context.Context, d signin.DeviceStart) (signin.Credential, error) {
	st, ok := d.State.(*deviceState)
	if !ok {
		return signin.Credential{}, errors.New("kiro: Poll was handed another backend's device flow")
	}
	tok, err := b.pollToken(ctx, st)
	if err != nil {
		return signin.Credential{}, err
	}
	r := record{
		StartURL:              st.startURL,
		Region:                st.region,
		ClientID:              st.clientID,
		ClientSecret:          st.clientSecret,
		ClientSecretExpiresAt: st.clientSecretExpiresAt,
		RefreshToken:          tok.RefreshToken,
		AccessToken:           tok.AccessToken,
		ExpiresAt:             b.now().Add(tok.ttl()).UTC(),
		Provider:              st.provider,
	}
	if r.RefreshToken == "" {
		return signin.Credential{}, &signin.FailureError{Text: textOIDCRefused, Err: errors.New("kiro: the token response has no refresh token")}
	}
	if err := b.attachProfile(ctx, st.ep.management, &r); err != nil {
		return signin.Credential{}, err
	}
	r.Email = b.readEmail(ctx, st.ep.management, r.AccessToken, r.ProfileArn)
	if err := ctx.Err(); err != nil {
		return signin.Credential{}, err
	}
	return r.credential()
}

// attachProfile stores the account's first Kiro profile. IAM Identity Center needs one, so its
// absence fails the sign-in; for Builder ID the lookup is best effort and KAS is answered with no
// profile ARN.
func (b *Backend) attachProfile(ctx context.Context, management string, r *record) error {
	p, err := b.firstProfile(ctx, management, r.AccessToken)
	if err == nil {
		r.ProfileArn, r.ProfileName = p.Arn, p.ProfileName
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if r.Provider == providerBuilderID {
		slog.Warn("sign-in: the Kiro profile lookup failed; signing in without a profile", "error", err)
		return nil
	}
	if errors.Is(err, errNoProfile) {
		return &signin.FailureError{Text: textNoProfile, Err: err}
	}
	return &signin.FailureError{Text: textProfileUnreachable, Err: err}
}

// Refresh renews c. A client registration past its secret's expiry cannot renew, so it is a
// rejection with no network call.
func (b *Backend) Refresh(ctx context.Context, c signin.Credential) (signin.Credential, error) {
	r, err := decodeRecord(c)
	if err != nil {
		return signin.Credential{}, err
	}
	if !r.ClientSecretExpiresAt.IsZero() && !b.now().Before(r.ClientSecretExpiresAt) {
		return signin.Credential{}, fmt.Errorf("%w: the client registration expired", signin.ErrRejected)
	}
	if err := b.refreshToken(ctx, &r); err != nil {
		return signin.Credential{}, err
	}
	return r.credential()
}

// Validate refuses a credential that is not a Kiro record KAS can use and the account menu can show.
func (*Backend) Validate(c signin.Credential) error {
	_, err := decodeRecord(c)
	return err
}

// Label is the account as the account menu shows it.
func (*Backend) Label(c signin.Credential) signin.Identity {
	r, err := decodeRecord(c)
	if err != nil {
		return signin.Identity{}
	}
	id := signin.Identity{StartURL: r.StartURL, Region: r.Region, Email: r.Email}
	if r.Provider == providerBuilderID {
		id.Method, id.AccountType = "Logged in with Builder ID", "BuilderId"
	} else {
		id.Method, id.AccountType = "Logged in with IAM Identity Center", "IamIdentityCenter"
	}
	return id
}

// LiveSwitchable is false when to has no profile ARN but from had one (KAS keeps a cached ARN when
// an answer omits it, so it would pair the new token with the old profile) and when the provider
// class changes (KAS reads governance from the class, and a running engine re-reading it is not
// established).
func (*Backend) LiveSwitchable(from, to signin.Credential) bool {
	f, err := decodeRecord(from)
	if err != nil {
		return false
	}
	t, err := decodeRecord(to)
	if err != nil {
		return false
	}
	return (t.ProfileArn != "" || f.ProfileArn == "") && f.Provider == t.Provider
}

// Fresh is true while more than renewWithin remains.
func (*Backend) Fresh(c signin.Credential, now time.Time) bool {
	r, err := decodeRecord(c)
	return err == nil && r.ExpiresAt.After(now.Add(renewWithin))
}

// Servable is true while more than staleFloor remains.
func (*Backend) Servable(c signin.Credential, now time.Time) bool {
	r, err := decodeRecord(c)
	return err == nil && r.ExpiresAt.After(now.Add(staleFloor))
}

// answer is the _kiro/auth/getAccessToken result. authMethod is omitted, so KAS uses its own
// acp_callback default; the refresh token never leaves marotte.
type answer struct {
	AccessToken string `json:"accessToken"`
	ExpiresAt   string `json:"expiresAt"`
	ProfileArn  string `json:"profileArn,omitempty"`
	Provider    string `json:"provider"`
}

// Result advertises min(real expiry, now + leaseCap).
func (*Backend) Result(c signin.Credential, now time.Time) (json.RawMessage, error) {
	r, err := decodeRecord(c)
	if err != nil {
		return nil, err
	}
	exp := now.Add(leaseCap)
	if r.ExpiresAt.Before(exp) {
		exp = r.ExpiresAt
	}
	return json.Marshal(answer{ //nolint:gosec // G117: this answer is the access token handed to KAS

		AccessToken: r.AccessToken,
		ExpiresAt:   exp.UTC().Format("2006-01-02T15:04:05.000Z"),
		ProfileArn:  r.ProfileArn,
		Provider:    string(r.Provider),
	})
}

// Refusal is KAS's callback error: code -32000 with the reason in data.details, which KAS joins to
// the message in what it logs and returns.
func (*Backend) Refusal(why signin.Refusal) *marotte.RPCError {
	details, msg := "unauthenticated", "Not signed in to Kiro in marotte. Sign in from the account menu."
	switch why {
	case signin.RefusedRejected:
		details, msg = "access-denied", "The identity provider refused to renew the Kiro sign-in. Sign in again from the account menu."
	case signin.RefusedUnreachable:
		details, msg = "transport", "marotte could not reach the identity provider to renew the Kiro sign-in; it will try again."
	case signin.RefusedSignedOut:
	}
	return &marotte.RPCError{Code: -32000, Message: msg, Data: json.RawMessage(`{"details":"` + details + `"}`)}
}
