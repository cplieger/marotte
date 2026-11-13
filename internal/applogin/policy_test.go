package applogin

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/access"
	"github.com/cplieger/access/account"
)

func TestPolicyWriteRequest_ZeroSecretIsOmitted(t *testing.T) {
	data, err := json.Marshal(PolicyWriteRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "client_secret") {
		t.Errorf("the zero request marshals %s, want no client_secret key", data)
	}
}

func TestValidatePolicy_TimeoutRanges(t *testing.T) {
	ok := defaultPolicy()
	if err := validatePolicy(&ok); err != nil {
		t.Fatalf("the default policy is refused: %v", err)
	}
	for _, c := range []struct {
		edit   func(*storePolicy)
		name   string
		reason string
	}{
		{func(p *storePolicy) { p.Idle = minIdle - 1 }, "idle_low", "idle_out_of_range"},
		{func(p *storePolicy) { p.Idle = maxIdle + 1 }, "idle_high", "idle_out_of_range"},
		{func(p *storePolicy) { p.Absolute = minAbsolute - 1 }, "absolute_low", "absolute_out_of_range"},
		{func(p *storePolicy) { p.Absolute = maxAbsolute + 1 }, "absolute_high", "absolute_out_of_range"},
	} {
		p := defaultPolicy()
		c.edit(&p)
		var pe *PolicyInvalidError
		err := validatePolicy(&p)
		if !asPolicyInvalid(err, &pe) || pe.Reason != c.reason {
			t.Errorf("%s: validatePolicy = %v, want reason %s", c.name, err, c.reason)
		}
	}
}

func asPolicyInvalid(err error, target **PolicyInvalidError) bool {
	pe, ok := err.(*PolicyInvalidError) //nolint:errorlint // validatePolicy returns the type itself for range refusals
	*target = pe
	return ok
}

// TestPolicy_DeltaNamesTheChange: what each own write changed, the
// Plain sessions a switch-off deleted, and one marotte log entry per delta entry in the same job.
func TestPolicy_DeltaNamesTheChange(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	setup := h.setupOwner(ctx)
	plain := newTestSession(h.owner().ID, epoch)
	if err := h.store.CreateSession(plainCtx(ctx), plain); err != nil {
		t.Fatal(err)
	}
	base := h.store.policy()
	delta, _, err := h.store.putPolicy(ctx, h.svc, &accountTestActor, ownerRPID, allowanceOff(base, access.MethodPasskey), base.Generation, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(delta.methods, map[access.Method]bool{access.MethodPasskey: false}) || delta.plainOn || delta.plainOff {
		t.Errorf("the allowance-off save's delta = %+v, want the passkey off alone", delta)
	}
	off, err := h.store.setPlainHTTP(ctx, h.svc, false)
	if err != nil {
		t.Fatal(err)
	}
	wantGone := slices.Sorted(slices.Values([]string{setup.Session.ID, plain.ID}))
	if !off.plainOff || !slices.Equal(off.deletedPlain, wantGone) {
		t.Errorf("the switch-off's delta = %+v, want plainOff deleting both Plain sessions %v", off, wantGone)
	}
	if on, err := h.store.setPlainHTTP(ctx, h.svc, true); err != nil || !on.plainOn {
		t.Errorf("the switch-on = (%+v, %v), want plainOn", on, err)
	}
	if again, err := h.store.setPlainHTTP(ctx, h.svc, true); err != nil || !again.empty() {
		t.Errorf("a switch already on = (%+v, %v), want an empty delta", again, err)
	}
	es, err := h.store.ListLog(ctx, account.LogRange{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range es {
		if strings.HasPrefix(string(e.Event.Kind), "marotte.") {
			kinds = append(kinds, string(e.Event.Kind)+":"+string(e.Event.Method))
		}
	}
	want := []string{kindMethodOff + ":passkey", kindPlainOff + ":", kindPlainOn + ":"}
	if !slices.Equal(kinds, want) {
		t.Errorf("own log entries %v, want %v", kinds, want)
	}
}

func TestCandidateOf_SecondsRangeCheckedBeforeConversion(t *testing.T) {
	inRange := func() PolicyWriteRequest {
		return PolicyWriteRequest{IdleSeconds: int64(minIdle / time.Second), AbsoluteSeconds: int64(maxAbsolute / time.Second)}
	}
	ok := inRange()
	p, err := candidateOf(&ok)
	if err != nil || p.Idle != minIdle || p.Absolute != maxAbsolute {
		t.Fatalf("candidateOf(in range) = (%s, %s, %v), want (%s, %s, nil)", p.Idle, p.Absolute, err, minIdle, maxAbsolute)
	}
	// 18446747674 s times time.Second wraps past the int64 range to about 3600.29 s.
	const wraps = 18446747674
	for _, c := range []struct {
		edit   func(*PolicyWriteRequest)
		name   string
		reason string
	}{
		{func(r *PolicyWriteRequest) { r.IdleSeconds = wraps }, "idle_wraps", "idle_out_of_range"},
		{func(r *PolicyWriteRequest) { r.AbsoluteSeconds = wraps }, "absolute_wraps", "absolute_out_of_range"},
		{func(r *PolicyWriteRequest) { r.IdleSeconds = -1 }, "idle_negative", "idle_out_of_range"},
		{func(r *PolicyWriteRequest) { r.AbsoluteSeconds = int64(maxAbsolute/time.Second) + 1 }, "absolute_high", "absolute_out_of_range"},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := inRange()
			c.edit(&req)
			_, err := candidateOf(&req)
			var pe *PolicyInvalidError
			if !asPolicyInvalid(err, &pe) || pe.Reason != c.reason {
				t.Errorf("candidateOf = %v, want reason %s", err, c.reason)
			}
		})
	}
}

// TestCandidateOf_OriginsAndPinKeysStoredCanonical: the record holds each origin in its RFC 6454
// serialization and each pin key as a lowercased host (RFC 3986 section 3.2.2), so two spellings
// of one provider are no change; a non-https or missing origin and two keys naming one host are
// refusals.
func TestCandidateOf_OriginsAndPinKeysStoredCanonical(t *testing.T) {
	request := func(origin, key string) *PolicyWriteRequest {
		return &PolicyWriteRequest{
			IdleSeconds: int64(minIdle / time.Second), AbsoluteSeconds: int64(maxAbsolute / time.Second),
			Provider: &ProviderSettings{
				Issuer: "https://IDP.Example.com/realms/home", ClientID: "marotte",
				Origins: ProviderOrigins{Issuer: origin, Authorization: origin, Token: origin, JWKS: origin},
				Pinned:  map[string][]string{key: {"10.0.0.7"}},
			},
		}
	}
	oneOrigin := func(set func(*ProviderOrigins)) *PolicyWriteRequest {
		req := request(idpURL, "idp.example.com")
		set(&req.Provider.Origins)
		return req
	}
	mixed, err := candidateOf(request("https://IDP.Example.com:443", "IDP.Example.com"))
	if err != nil {
		t.Fatalf("candidateOf(mixed spelling) = %v", err)
	}
	want := ProviderOrigins{Issuer: idpURL, Authorization: idpURL, Token: idpURL, JWKS: idpURL}
	if mixed.Provider.Origins != want {
		t.Errorf("origins %+v, want %+v", mixed.Provider.Origins, want)
	}
	if keys := slices.Collect(maps.Keys(mixed.Provider.Pinned)); !slices.Equal(keys, []string{"idp.example.com"}) {
		t.Errorf("pin keys %q, want [idp.example.com]", keys)
	}
	if mixed.Provider.Issuer != "https://IDP.Example.com/realms/home" {
		t.Errorf("issuer %q, want the request's identifier unchanged", mixed.Provider.Issuer)
	}
	lower, err := candidateOf(request(idpURL, "idp.example.com"))
	if err != nil {
		t.Fatalf("candidateOf(lower case) = %v", err)
	}
	if providerChanged(lower.Provider, mixed.Provider, false) {
		t.Error("two spellings of one provider count as a change")
	}
	for _, c := range []struct {
		req    *PolicyWriteRequest
		name   string
		reason string
	}{
		{request("http://idp.example.com", "idp.example.com"), "http_origin", "origin_invalid"},
		{request("", "idp.example.com"), "missing_origin", "origin_invalid"},
		{request("https://idp.example.com:0", "idp.example.com"), "port_zero", "origin_invalid"},
		{oneOrigin(func(o *ProviderOrigins) { o.Issuer = "http://idp.example.com" }), "http_issuer_origin", "origin_invalid"},
		{oneOrigin(func(o *ProviderOrigins) { o.Authorization = "http://idp.example.com" }), "http_authorization_origin", "origin_invalid"},
		{oneOrigin(func(o *ProviderOrigins) { o.Token = "http://idp.example.com" }), "http_token_origin", "origin_invalid"},
		{oneOrigin(func(o *ProviderOrigins) { o.JWKS = "http://idp.example.com" }), "http_jwks_origin", "origin_invalid"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := candidateOf(c.req)
			var pe *PolicyInvalidError
			if !asPolicyInvalid(err, &pe) || pe.Reason != c.reason {
				t.Errorf("candidateOf = %v, want reason %s", err, c.reason)
			}
		})
	}
	t.Run("two_keys_one_host", func(t *testing.T) {
		req := request(idpURL, "idp.example.com")
		req.Provider.Pinned["IDP.example.com"] = []string{"10.0.0.8"}
		_, err := candidateOf(req)
		var pe *PolicyInvalidError
		if !asPolicyInvalid(err, &pe) || pe.Reason != "pinned_host_duplicate" {
			t.Errorf("candidateOf = %v, want reason pinned_host_duplicate", err)
		}
	})
}

func TestPlanProviderEdit_NoProviderOnEitherSideIsNoEdit(t *testing.T) {
	edit, err := planProviderEdit(storeProvider{}, storeProvider{}, access.NewSecret("a stray replacement"))
	if edit != nil || err != nil {
		t.Errorf("planProviderEdit(none, none, secret) = (%+v, %v), want no edit", edit, err)
	}
	h := newHarness(t)
	ctx := t.Context()
	h.setupOwner(ctx)
	base := h.store.policy()
	if _, _, err := h.store.putPolicy(ctx, h.svc, &accountTestActor, ownerRPID, base, base.Generation, edit); err != nil {
		t.Fatalf("the no-change save = %v", err)
	}
	if got := h.store.policy().Generation; got != base.Generation {
		t.Errorf("a no-change save moved the generation %d to %d, want it unchanged", base.Generation, got)
	}
}
