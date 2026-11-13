package applogin

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"time"

	"github.com/cplieger/access"
	"github.com/cplieger/access/account"
	"github.com/cplieger/access/oidc"
	"github.com/cplieger/marotte/internal/route"
)

// storePolicy is the Sign-in policy record.
type storePolicy struct {
	Provider storeProvider `json:"provider"`
	Idle     time.Duration `json:"idle"`
	Absolute time.Duration `json:"absolute"`
	// Generation numbers the record: every write that changes a field adds 1 in the same
	// publication, so a Settings form built on an older value is refused.
	Generation int64         `json:"generation"`
	Methods    policyMethods `json:"methods"`
	PlainHTTP  bool          `json:"plain_http"`
	Breach     bool          `json:"breach"`
}

type policyMethods struct {
	Password bool `json:"password"`
	Passkey  bool `json:"passkey"`
	OIDC     bool `json:"oidc"`
}

func (m policyMethods) get(method access.Method) bool {
	switch method {
	case access.MethodPassword:
		return m.Password
	case access.MethodPasskey:
		return m.Passkey
	case access.MethodOIDC:
		return m.OIDC
	}
	return false
}

// policyMethodOrder is the order a delta lists allowance changes in.
var policyMethodOrder = []access.Method{access.MethodPassword, access.MethodPasskey, access.MethodOIDC}

// storeProvider is the saved OIDC provider; the zero value is none.
type storeProvider struct {
	Pinned       map[string][]netip.Addr `json:"pinned,omitempty"`
	ClientSecret access.Secret           `json:"-"`
	Origins      ProviderOrigins         `json:"origins"`
	Issuer       string                  `json:"issuer,omitempty"`
	ClientID     string                  `json:"client_id,omitempty"`
	DisplayName  string                  `json:"display_name,omitempty"`
}

// ProviderOrigins are the endpoint origins the owner approved at Test.
//
//deadset:ignore DS1501 -- a field of PolicyView and PolicyWriteRequest, which the endpoint table names once the policy routes are mounted
type ProviderOrigins struct {
	Issuer        string `json:"issuer"`
	Authorization string `json:"authorization"`
	Token         string `json:"token"`
	JWKS          string `json:"jwks"`
}

func (p *storeProvider) isZero() bool { return p.Issuer == "" }

const (
	defaultIdle     = 7 * 24 * time.Hour
	defaultAbsolute = 30 * 24 * time.Hour
	minIdle         = time.Hour
	maxIdle         = 30 * 24 * time.Hour
	minAbsolute     = 24 * time.Hour
	maxAbsolute     = 90 * 24 * time.Hour
)

func defaultPolicy() storePolicy {
	return storePolicy{
		Methods:    policyMethods{Password: true, Passkey: true, OIDC: true},
		Idle:       defaultIdle,
		Absolute:   defaultAbsolute,
		Generation: 1,
		PlainHTTP:  true,
	}
}

// clone deep-copies the record; the secret is immutable and shared.
func (p storePolicy) clone() storePolicy { //nolint:gocritic // hugeParam: a value copy is the point
	p.Provider.Pinned = clonePins(p.Provider.Pinned)
	return p
}

func clonePins(m map[string][]netip.Addr) map[string][]netip.Addr {
	if m == nil {
		return nil
	}
	out := make(map[string][]netip.Addr, len(m))
	for host, addrs := range m {
		out[host] = slices.Clone(addrs)
	}
	return out
}

// sameRecord reports whether two records hold the same values, the secret excepted.
func sameRecord(a, b *storePolicy) bool {
	return a.Methods == b.Methods && a.Idle == b.Idle && a.Absolute == b.Absolute &&
		a.PlainHTTP == b.PlainHTTP && a.Breach == b.Breach &&
		!providerFieldsDiffer(&a.Provider, &b.Provider) && a.Provider.DisplayName == b.Provider.DisplayName
}

// providerFieldsDiffer compares every field a built provider is made from, the secret excepted.
func providerFieldsDiffer(a, b *storeProvider) bool {
	return a.Issuer != b.Issuer || a.ClientID != b.ClientID || a.Origins != b.Origins ||
		!maps.EqualFunc(a.Pinned, b.Pinned, slices.Equal)
}

// providerChanged is the one place marotte asks whether a saved provider changed: a replacement
// secret always is a change, whatever its text, and the display name never is.
func providerChanged(stored, next storeProvider, replacement bool) bool { //nolint:gocritic // hugeParam: the two records compared
	return replacement || providerFieldsDiffer(&stored, &next)
}

// providerEdit is a request's change to the provider: its record and, for anything but a removal,
// the candidate the handler built from it before the write.
type providerEdit struct {
	built  *oidc.Provider
	record storeProvider
}

// ErrClientSecretRequired refuses a provider whose issuer or client ID changed with no
// replacement secret.
var ErrClientSecretRequired = errors.New("applogin: a changed issuer or client ID needs its client secret")

// planProviderEdit decides from base, the provider of the handler's one read, what the request
// does to the provider: nil for untouched, a refusal, or an edit whose record carries the
// replacement secret or else base's own.
func planProviderEdit(base, next storeProvider, replacement access.Secret) (*providerEdit, error) { //nolint:gocritic // hugeParam: the two records compared
	if !providerChanged(base, next, !replacement.IsZero()) || (next.isZero() && base.isZero()) {
		return nil, nil //nolint:nilnil // untouched is no edit and no error
	}
	if next.isZero() {
		return &providerEdit{}, nil
	}
	if replacement.IsZero() && (next.Issuer != base.Issuer || next.ClientID != base.ClientID) {
		return nil, ErrClientSecretRequired
	}
	rec := next
	rec.Pinned = clonePins(next.Pinned)
	rec.ClientSecret = base.ClientSecret
	if !replacement.IsZero() {
		rec.ClientSecret = replacement
	}
	return &providerEdit{record: rec}, nil
}

// ErrPolicyConflict refuses a Settings save built on a generation the record no longer holds.
var ErrPolicyConflict = errors.New("applogin: the sign-in policy changed since the form was loaded")

// policyInvalidError refuses a policy whose values are out of range; Reason is the envelope's.
type policyInvalidError struct {
	Reason string
}

func (e *policyInvalidError) Error() string { return "applogin: invalid sign-in policy: " + e.Reason }

// validatePolicy checks a candidate record before any build or write.
func validatePolicy(p *storePolicy) error {
	switch {
	case p.Idle < minIdle || p.Idle > maxIdle:
		return &policyInvalidError{Reason: "idle_out_of_range"}
	case p.Absolute < minAbsolute || p.Absolute > maxAbsolute:
		return &policyInvalidError{Reason: "absolute_out_of_range"}
	}
	if err := rulesOf(p, nil).Validate(); err != nil {
		return fmt.Errorf("%w: %w", &policyInvalidError{Reason: "rules_invalid"}, err)
	}
	return nil
}

// PlainCaps is Rules.SessionCaps: a plain request, or one the gate never saw, gets the plain caps;
// an HTTPS request none.
func PlainCaps(ctx context.Context, _ account.Client) (idle, absolute time.Duration) { //nolint:gocritic // hugeParam: Rules.SessionCaps's signature
	if plain, ok := transportOf(ctx); ok && !plain {
		return 0, 0
	}
	return 30 * time.Minute, 12 * time.Hour
}

// rulesOf derives the library's Rules from a policy record; usableFn is the store's
// Rules.MethodUsable, nil only for validation.
func rulesOf(p *storePolicy, usableFn func(context.Context, account.Tx, *access.User, access.Method) bool) account.Rules {
	breach := account.BreachOff
	if p.Breach {
		breach = account.BreachFailOpen
	}
	return account.Rules{
		MethodUsable: usableFn,
		SessionCaps:  PlainCaps,
		Scopes:       route.KeyScopes(),
		RecentAuth:   10 * time.Minute,
		Breach:       breach,
	}
}

func (s *Store) rulesFor(snap *snapshot) account.Rules { return rulesOf(&snap.policy, s.methodUsable) }

// Rules is account.Config.Rules, derived from the published policy on every call.
func (s *Store) Rules() account.Rules { return s.rulesFor(s.cur.Load()) }

// PolicyTimeouts is access.Config.TimeoutSource, read from the published policy.
func (s *Store) PolicyTimeouts() access.SessionTimeouts {
	p := &s.cur.Load().policy
	return access.SessionTimeouts{Idle: p.Idle, Absolute: p.Absolute}
}

// PolicyView is the policy as the Settings form reads it; it carries no secret.
type PolicyView struct {
	Provider        *ProviderView `json:"provider,omitempty"`
	IdleSeconds     int64         `json:"idle_seconds"`
	AbsoluteSeconds int64         `json:"absolute_seconds"`
	Generation      int64         `json:"generation"`
	Methods         PolicyMethods `json:"methods"`
	PlainHTTP       bool          `json:"plain_http"`
	Breach          bool          `json:"breach"`
}

// PolicyMethods is each sign-in method's allowance.
type PolicyMethods struct {
	Password bool `json:"password"`
	Passkey  bool `json:"passkey"`
	OIDC     bool `json:"oidc"`
}

// ProviderView is the saved provider without its secret.
type ProviderView struct {
	Pinned            map[string][]string `json:"pinned"`
	Origins           ProviderOrigins     `json:"origins"`
	Issuer            string              `json:"issuer"`
	ClientID          string              `json:"client_id"`
	DisplayName       string              `json:"display_name"`
	ClientSecretSaved bool                `json:"client_secret_saved"`
}

// ProviderSettings is the provider a Settings save asks for.
type ProviderSettings struct {
	Pinned      map[string][]string `json:"pinned"`
	Origins     ProviderOrigins     `json:"origins"`
	Issuer      string              `json:"issuer"`
	ClientID    string              `json:"client_id"`
	DisplayName string              `json:"display_name"`
}

// PolicyWriteRequest is PUT /api/access/policy's body. ClientSecret is a replacement only when
// non-empty; the handler turns that text into an access.Secret before the save.
type PolicyWriteRequest struct {
	Provider        *ProviderSettings `json:"provider,omitempty"`
	ClientSecret    string            `json:"client_secret,omitzero"`
	IdleSeconds     int64             `json:"idle_seconds"`
	AbsoluteSeconds int64             `json:"absolute_seconds"`
	Generation      int64             `json:"generation"`
	Methods         PolicyMethods     `json:"methods"`
	PlainHTTP       bool              `json:"plain_http"`
	Breach          bool              `json:"breach"`
}

func viewOf(p *storePolicy) PolicyView {
	v := PolicyView{
		Methods:         PolicyMethods(p.Methods),
		IdleSeconds:     int64(p.Idle / time.Second),
		AbsoluteSeconds: int64(p.Absolute / time.Second),
		Generation:      p.Generation,
		PlainHTTP:       p.PlainHTTP,
		Breach:          p.Breach,
	}
	if !p.Provider.isZero() {
		pins := make(map[string][]string, len(p.Provider.Pinned))
		for host, addrs := range p.Provider.Pinned {
			for _, a := range addrs {
				pins[host] = append(pins[host], a.String())
			}
		}
		v.Provider = &ProviderView{
			Pinned: pins, Origins: p.Provider.Origins, Issuer: p.Provider.Issuer, ClientID: p.Provider.ClientID,
			DisplayName: p.Provider.DisplayName, ClientSecretSaved: !p.Provider.ClientSecret.IsZero(),
		}
	}
	return v
}

// PolicyView projects the published policy.
func (s *Store) PolicyView() PolicyView { return viewOf(&s.cur.Load().policy) }

// candidateOf builds the record a request asks for; its provider is the request's, without a
// secret, which planProviderEdit decides, with its origins and pin keys in canonical form. A
// malformed pinned address, a non-https origin or two pin keys naming one host is a refusal.
func candidateOf(req *PolicyWriteRequest) (storePolicy, error) {
	// Range-checked as seconds: a Duration product past the int64 range wraps into range.
	switch {
	case req.IdleSeconds < int64(minIdle/time.Second) || req.IdleSeconds > int64(maxIdle/time.Second):
		return storePolicy{}, &policyInvalidError{Reason: "idle_out_of_range"}
	case req.AbsoluteSeconds < int64(minAbsolute/time.Second) || req.AbsoluteSeconds > int64(maxAbsolute/time.Second):
		return storePolicy{}, &policyInvalidError{Reason: "absolute_out_of_range"}
	}
	p := storePolicy{
		Methods:   policyMethods(req.Methods),
		Idle:      time.Duration(req.IdleSeconds) * time.Second,
		Absolute:  time.Duration(req.AbsoluteSeconds) * time.Second,
		PlainHTTP: req.PlainHTTP,
		Breach:    req.Breach,
	}
	if req.Provider == nil || req.Provider.Issuer == "" {
		return p, nil
	}
	origins, err := canonicalOrigins(req.Provider.Origins)
	if err != nil {
		return storePolicy{}, err
	}
	pins := make(map[string][]netip.Addr, len(req.Provider.Pinned))
	hosts := make(map[string]bool, len(req.Provider.Pinned))
	for raw, addrs := range req.Provider.Pinned {
		host := canonicalHost(raw)
		if hosts[host] {
			return storePolicy{}, &policyInvalidError{Reason: "pinned_host_duplicate"}
		}
		hosts[host] = true
		for _, a := range addrs {
			addr, err := netip.ParseAddr(a)
			if err != nil {
				return storePolicy{}, &policyInvalidError{Reason: "pinned_address_invalid"}
			}
			pins[host] = append(pins[host], addr)
		}
	}
	p.Provider = storeProvider{
		Pinned: pins, Origins: origins, Issuer: req.Provider.Issuer,
		ClientID: req.Provider.ClientID, DisplayName: req.Provider.DisplayName,
	}
	return p, nil
}

// canonicalOrigins stores each approved origin in its canonical form, so the record holds one
// spelling and the change test compares like with like; an origin that is not https is a refusal.
func canonicalOrigins(raw ProviderOrigins) (ProviderOrigins, error) {
	out := ProviderOrigins{
		Issuer: canonicalOrigin(raw.Issuer), Authorization: canonicalOrigin(raw.Authorization),
		Token: canonicalOrigin(raw.Token), JWKS: canonicalOrigin(raw.JWKS),
	}
	if out.Issuer == "" || out.Authorization == "" || out.Token == "" || out.JWKS == "" {
		return ProviderOrigins{}, &policyInvalidError{Reason: "origin_invalid"}
	}
	return out, nil
}

// policyDelta is what an own policy write changed on the private copy.
type policyDelta struct {
	methods      map[access.Method]bool
	deletedPlain []string
	plainOn      bool
	plainOff     bool
}

func (d *policyDelta) empty() bool { return !d.plainOn && !d.plainOff && len(d.methods) == 0 }

func deltaOf(cur, next *storePolicy) policyDelta {
	var d policyDelta
	if cur.PlainHTTP != next.PlainHTTP {
		d.plainOn, d.plainOff = next.PlainHTTP, !next.PlainHTTP
	}
	for _, m := range policyMethodOrder {
		if v := next.Methods.get(m); cur.Methods.get(m) != v {
			if d.methods == nil {
				d.methods = map[access.Method]bool{}
			}
			d.methods[m] = v
		}
	}
	return d
}
