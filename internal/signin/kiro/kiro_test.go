package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/go/slogx/capture"
	"github.com/cplieger/marotte/internal/signin"
)

func resultAt(t *testing.T, r record, now time.Time) answer {
	t.Helper()
	res, err := New().Result(credOf(t, r), now)
	if err != nil {
		t.Fatal(err)
	}
	var a answer
	if err := json.Unmarshal(res, &a); err != nil {
		t.Fatal(err)
	}
	return a
}

func advertised(t *testing.T, a answer) time.Time {
	t.Helper()
	exp, err := time.Parse(time.RFC3339, a.ExpiresAt)
	if err != nil {
		t.Fatalf("expiresAt %q is not ISO-8601: %v", a.ExpiresAt, err)
	}
	return exp
}

func TestAnswer_LeaseCapsAdvertisedExpiry(t *testing.T) {
	r := testRecord(providerBuilderID, "")
	now := r.ExpiresAt.Add(-time.Hour)
	if got := advertised(t, resultAt(t, r, now)); !got.Equal(now.Add(leaseCap)) {
		t.Errorf("an hour-long token advertised %v, want now + 240 s", got)
	}
	// Every expiry Fresh or Servable accepts is advertised more than 180 s out, which KAS's
	// doRefresh requires.
	b := New()
	for left := 211 * time.Second; left <= 2*time.Hour; left += 7 * time.Second {
		at := r.ExpiresAt.Add(-left)
		c := credOf(t, r)
		if !b.Fresh(c, at) && !b.Servable(c, at) {
			continue
		}
		if got := advertised(t, resultAt(t, r, at)); !got.After(at.Add(180 * time.Second)) {
			t.Fatalf("with %v left the answer advertised %v, not more than 180 s out", left, got.Sub(at))
		}
	}
}

func TestAnswer_RefreshesInsideThreeHundredSeconds(t *testing.T) {
	r := testRecord(providerBuilderID, "")
	c := credOf(t, r)
	b := New()
	if !b.Fresh(c, r.ExpiresAt.Add(-301*time.Second)) {
		t.Error("301 s left is not fresh")
	}
	if b.Fresh(c, r.ExpiresAt.Add(-300*time.Second)) {
		t.Error("300 s left is fresh, want a renewal")
	}
	if !b.Servable(c, r.ExpiresAt.Add(-211*time.Second)) || b.Servable(c, r.ExpiresAt.Add(-210*time.Second)) {
		t.Error("the stale floor is not 210 s")
	}
}

// kiroCLIBuilderIDArn is the profile kiro-cli answers for every Builder ID sign-in.
const kiroCLIBuilderIDArn = "arn:aws:codewhisperer:us-east-1:638616132270:profile/AAAACCCCXXXX"

func TestAnswer_ProfileArnIsTheClassConstantForBuilderIDAndTheRecordsOtherwise(t *testing.T) {
	now := testRecord(providerBuilderID, "").ExpiresAt.Add(-time.Hour)
	for _, tc := range []struct {
		r    record
		name string
		want string
	}{
		{name: "builder_id_without_arn", r: testRecord(providerBuilderID, ""), want: kiroCLIBuilderIDArn},
		{name: "builder_id_with_arn", r: testRecord(providerBuilderID, "arn:b"), want: kiroCLIBuilderIDArn},
		{name: "internal", r: testRecord(providerInternal, "arn:i"), want: "arn:i"},
		{name: "enterprise", r: testRecord(providerEnterprise, "arn:e"), want: "arn:e"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := New().Result(credOf(t, tc.r), now)
			if err != nil {
				t.Fatal(err)
			}
			var raw map[string]any
			if err := json.Unmarshal(res, &raw); err != nil {
				t.Fatal(err)
			}
			if raw["profileArn"] != tc.want {
				t.Errorf("profileArn = %v, want %q", raw["profileArn"], tc.want)
			}
			if raw["provider"] != string(tc.r.Provider) {
				t.Errorf("provider = %v, want %s", raw["provider"], tc.r.Provider)
			}
			if _, has := raw["authMethod"]; has {
				t.Error("the answer carries authMethod")
			}
		})
	}
}

func TestAnswer_NeverContainsTheRefreshToken(t *testing.T) {
	r := testRecord(providerInternal, "arn:i")
	r.RefreshToken, r.ClientSecret = "refresh-secret-zzz", "client-secret-zzz"
	b := New()
	res, err := b.Result(credOf(t, r), r.ExpiresAt.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, why := range []signin.Refusal{signin.RefusedSignedOut, signin.RefusedRejected, signin.RefusedUnreachable} {
		e := b.Refusal(why)
		res = append(append(res, e.Message...), e.Data...)
	}
	for _, secret := range []string{r.RefreshToken, r.ClientSecret} {
		if bytes.Contains(res, []byte(secret)) {
			t.Errorf("an answer contains %q", secret)
		}
	}
}

func TestAnswer_ErrorShapes(t *testing.T) {
	b := New()
	for why, details := range map[signin.Refusal]string{
		signin.RefusedSignedOut:   "unauthenticated",
		signin.RefusedRejected:    "access-denied",
		signin.RefusedUnreachable: "transport",
	} {
		e := b.Refusal(why)
		var data struct {
			Details string `json:"details"`
		}
		if err := json.Unmarshal(e.Data, &data); err != nil || e.Code != -32000 || data.Details != details {
			t.Errorf("Refusal(%v) = %d %s, want -32000 with details %q", why, e.Code, e.Data, details)
		}
	}

	t.Run("no_record", func(t *testing.T) {
		h := newVaultHarness(t, "alice")
		if _, rerr := h.v.Answer(t.Context(), ID); rerr == nil || !strings.Contains(string(rerr.Data), "unauthenticated") {
			t.Errorf("answer with no record = %v, want unauthenticated", rerr)
		}
	})
	for _, tc := range []struct {
		name    string
		ttl     string
		details string
		status  int
	}{
		{name: "transient_with_time_left_serves_the_old_token", status: 500, ttl: "250"},
		{name: "network_error_shape_under_the_floor", status: 503, ttl: "200", details: "transport"},
		{name: "rejected_400", status: 400, ttl: "200", details: "access-denied"},
		{name: "rejected_401", status: 401, ttl: "200", details: "access-denied"},
		{name: "rejected_403", status: 403, ttl: "200", details: "access-denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newVaultHarness(t, "alice")
			h.aws.set(func(f *fakeAWS) {
				f.ttl = tc.ttl
				f.refresh = scripted{status: tc.status}
			})
			h.signIn("alice", builderID(), profilesOf("alice"))
			// A transient failure is retried until the asker's deadline, so this one keeps the case short.
			ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
			defer cancel()
			res, rerr := h.v.Answer(ctx, ID)
			switch {
			case tc.details == "" && (rerr != nil || !bytes.Contains(res, []byte(`"alice-access"`))):
				t.Errorf("answer = %s, %v; want the old token", res, rerr)
			case tc.details != "" && (rerr == nil || !strings.Contains(string(rerr.Data), tc.details)):
				t.Errorf("answer = %s, %v; want details %q", res, rerr, tc.details)
			}
			if lapsed := h.v.Status(ID).Kind == signin.StatusSignedOut; lapsed != (tc.details == "access-denied") {
				t.Errorf("lapsed = %v, want it exactly on a rejection", lapsed)
			}
		})
	}
}

func TestRecord_RefusesAnEmptyProfileArnForIdCClasses(t *testing.T) {
	for _, tc := range []struct {
		p    provider
		want bool
	}{
		{providerBuilderID, true},
		{providerInternal, false},
		{providerEnterprise, false},
		{provider("Google"), false},
	} {
		r := testRecord(tc.p, "")
		if err := validRecord(&r); (err == nil) != tc.want {
			t.Errorf("validRecord(%s without ARN) = %v, want accepted %v", tc.p, err, tc.want)
		}
	}
}

func TestRecord_RefusesWhatTheAccountMenuCouldNotShow(t *testing.T) {
	for _, tc := range []struct {
		edit func(r *record)
		name string
	}{
		{name: "email_with_a_newline", edit: func(r *record) { r.Email = "a@b.c\nforged" }},
		{name: "email_with_a_bidi_control", edit: func(r *record) { r.Email = "a\u202e@b.c" }},
		{name: "email_over_320_bytes", edit: func(r *record) { r.Email = strings.Repeat("a", 321) }},
		{name: "start_url_not_https", edit: func(r *record) { r.StartURL = "http://view.awsapps.com/start" }},
		{name: "start_url_with_userinfo", edit: func(r *record) { r.StartURL = "https://u:p@view.awsapps.com/start" }},
		{name: "region_not_a_region", edit: func(r *record) { r.Region = "us east 1" }},
		{name: "no_refresh_token", edit: func(r *record) { r.RefreshToken = "" }},
		{name: "no_expiry", edit: func(r *record) { r.ExpiresAt = time.Time{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := testRecord(providerBuilderID, "")
			tc.edit(&r)
			if err := validRecord(&r); err == nil {
				t.Error("validRecord accepted it")
			}
		})
	}
	r := testRecord(providerBuilderID, "")
	r.Email = "someone@example.com"
	if err := validRecord(&r); err != nil {
		t.Errorf("a plain email was refused: %v", err)
	}
}

func TestLabel_IsTheRecordsMethodStartURLRegionAndEmail(t *testing.T) {
	r := testRecord(providerEnterprise, "arn:e")
	r.StartURL, r.Region, r.Email = "https://d-1234.awsapps.com/start", "eu-west-1", "someone@example.com"
	got := New().Label(credOf(t, r))
	want := signin.Identity{Method: "Logged in with IAM Identity Center", AccountType: "IamIdentityCenter", StartURL: r.StartURL, Region: r.Region, Email: r.Email}
	if got != want {
		t.Errorf("Label = %+v, want %+v", got, want)
	}
	if got := New().Label(credOf(t, testRecord(providerBuilderID, ""))); got.Method != "Logged in with Builder ID" || got.AccountType != "BuilderId" {
		t.Errorf("Builder ID label = %+v", got)
	}
}

func TestLiveSwitchable(t *testing.T) {
	for _, tc := range []struct {
		from, to record
		name     string
		want     bool
	}{
		{name: "same_idc_class", from: testRecord(providerInternal, "arn:a"), to: testRecord(providerInternal, "arn:b"), want: true},
		{name: "builder_id_arn_to_no_arn", from: testRecord(providerBuilderID, "arn:a"), to: testRecord(providerBuilderID, ""), want: true},
		{name: "class_change", from: testRecord(providerBuilderID, ""), to: testRecord(providerEnterprise, "arn:b"), want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := New().LiveSwitchable(credOf(t, tc.from), credOf(t, tc.to)); got != tc.want {
				t.Errorf("LiveSwitchable = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSignIn_IdCWithoutProfileStoresNothing(t *testing.T) {
	for _, tc := range []struct {
		profiles scripted
		name     string
		want     string
	}{
		{name: "no_profile", profiles: noProfiles, want: textNoProfile},
		{name: "control_plane_error", profiles: scripted{status: 500}, want: textProfileUnreachable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newVaultHarness(t, "alice")
			st := h.signIn("alice", idcParams, tc.profiles)
			if st.Kind != signin.StatusSignedOut || st.SignIn.Error != tc.want {
				t.Errorf("Status = %+v, want signed_out with %q", st, tc.want)
			}
		})
	}
}

func TestSignIn_BuilderIDAnswersTheFixedProfileArnWithoutAProfileLookup(t *testing.T) {
	h := newVaultHarness(t, "alice")
	if st := h.signIn("alice", builderID(), profilesOf("alice")); st.Kind != signin.StatusSignedIn {
		t.Fatalf("Status = %+v, want signed_in", st)
	}
	a, ok := h.answer()
	if !ok || a.ProfileArn != kiroCLIBuilderIDArn || a.Provider != string(providerBuilderID) {
		t.Errorf("answer = %+v, %v; want a Builder ID answer with kiro-cli's Builder ID profile", a, ok)
	}
	if n := len(h.aws.servedPath("/")); n != 0 {
		t.Errorf("profile lookups = %d, want none for Builder ID", n)
	}
}

func TestSignIn_IdCStoresTheProfileAndTheClass(t *testing.T) {
	h := newVaultHarness(t, "alice")
	h.aws.set(func(f *fakeAWS) {
		f.profiles = scripted{status: 200, body: `{"profiles":[{"arn":"arn:first","profileName":"one"},{"arn":"arn:second","profileName":"two"}]}`}
	})
	if _, err := h.v.StartSignIn(t.Context(), ID, signin.FlowParams{Flow: signin.FlowDevice, StartURL: "https://AMZN.awsapps.com/start#/", Region: "us-east-1"}); err != nil {
		t.Fatal(err)
	}
	h.settle()
	a, ok := h.answer()
	if !ok || a.ProfileArn != "arn:first" || a.Provider != string(providerInternal) {
		t.Errorf("answer = %+v, %v; want the first profile and Internal", a, ok)
	}
}

// switchCase signs in as alice, then as bob through bobParams.
func switchCase(t *testing.T, aliceParams, bobParams signin.FlowParams, aliceProfiles, bobProfiles scripted) *vaultHarness {
	t.Helper()
	h := newVaultHarness(t, "alice")
	h.signIn("alice", aliceParams, aliceProfiles)
	if _, ok := h.answer(); !ok {
		t.Fatal("alice was not answered")
	}
	if st := h.signIn("bob", bobParams, bobProfiles); st.Kind != signin.StatusSignedIn {
		t.Fatalf("bob's sign-in = %+v", st)
	}
	return h
}

func TestSignIn_BuilderIDSwitchNeverRestarts(t *testing.T) {
	h := switchCase(t, builderID(), builderID(), profilesOf("alice"), noProfiles)
	if h.v.AccountPending(ID) {
		t.Error("AccountPending after a Builder ID to Builder ID switch")
	}
	if a, ok := h.answer(); !ok || a.AccessToken != "bob-access" || a.ProfileArn != kiroCLIBuilderIDArn {
		t.Errorf("answer = %+v, %v; want bob's at once, with kiro-cli's Builder ID profile", a, ok)
	}
}

func TestSignIn_SameClassSwitchWithArnNeverRestarts(t *testing.T) {
	h := switchCase(t, idcParams, idcParams, profilesOf("alice"), profilesOf("bob"))
	if h.v.AccountPending(ID) {
		t.Error("AccountPending after a same-class switch with an ARN")
	}
	if a, ok := h.answer(); !ok || a.AccessToken != "bob-access" {
		t.Errorf("answer = %+v, want bob's at once", a)
	}
}

func TestSignIn_ClassChangeMakesRestartPendingAndKeepsTheOldProvider(t *testing.T) {
	h := switchCase(t, builderID(), idcParams, profilesOf("alice"), profilesOf("bob"))
	if !h.v.AccountPending(ID) {
		t.Error("AccountPending = false after Builder ID to IAM Identity Center")
	}
	if a, ok := h.answer(); !ok || a.Provider != string(providerBuilderID) || a.AccessToken != "alice-access" {
		t.Errorf("answer = %+v, want alice's Builder ID answer until the restart", a)
	}
	h.v.EngineSpawned(ID)
	if a, ok := h.answer(); !ok || a.Provider != string(providerEnterprise) {
		t.Errorf("after the restart answer = %+v, want Enterprise", a)
	}
}

const emailWarning = "sign-in: the account email could not be read"

// emailWarnings counts the email WARNs and fails the test when any record carries a secret.
func emailWarnings(t *testing.T, rec *capture.Recorder, secrets ...string) int {
	t.Helper()
	n := 0
	for _, r := range rec.Records() {
		if r.Message == emailWarning && r.Level == slog.LevelWarn {
			n++
		}
		for _, s := range secrets {
			if strings.Contains(r.Message, s) {
				t.Errorf("the record %q carries %q in its message", r.Message, s)
			}
		}
		r.Attrs(func(a slog.Attr) bool {
			for _, s := range secrets {
				if strings.Contains(a.Value.String(), s) {
					t.Errorf("the record %q carries %q in %s", r.Message, s, a.Key)
				}
			}
			return true
		})
	}
	return n
}

// TestSignIn_EmailReadInsideTheSignIn: a read that yields no displayable email stores none and
// logs exactly one WARN, with neither the token nor the response body.
func TestSignIn_EmailReadInsideTheSignIn(t *testing.T) {
	for _, tc := range []struct {
		email     scripted
		name      string
		params    signin.FlowParams
		wantEmail string
		wantArn   string
	}{
		{name: "present", email: scripted{status: 200, body: `{"userInfo":{"email":"alice@example.com"},"note":"body-canary"}`}, params: builderID(), wantEmail: "alice@example.com", wantArn: kiroCLIBuilderIDArn},
		{name: "present_idc", email: scripted{status: 200, body: `{"userInfo":{"email":"alice@example.com"},"note":"body-canary"}`}, params: idcParams, wantEmail: "alice@example.com", wantArn: "arn:aws:codewhisperer:us-east-1:111:profile/alice"},
		{name: "absent", email: scripted{status: 200, body: `{"userInfo":{},"note":"body-canary"}`}, params: builderID(), wantArn: kiroCLIBuilderIDArn},
		{name: "forbidden", email: scripted{status: 403, body: `{"message":"body-canary"}`}, params: builderID(), wantArn: kiroCLIBuilderIDArn},
		{name: "not_displayable", email: scripted{status: 200, body: `{"userInfo":{"email":"a@b\u001b[31m"},"note":"body-canary"}`}, params: builderID(), wantArn: kiroCLIBuilderIDArn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := capture.Default(t)
			h := newVaultHarness(t, "alice")
			h.aws.set(func(f *fakeAWS) { f.email = tc.email })
			st := h.signIn("alice", tc.params, profilesOf("alice"))
			if st.Kind != signin.StatusSignedIn || st.Label.Email != tc.wantEmail {
				t.Errorf("Status = %+v, want signed in with email %q", st, tc.wantEmail)
			}
			wantWarnings := 0
			if tc.wantEmail == "" {
				wantWarnings = 1
			}
			if n := emailWarnings(t, rec, "alice-access", "body-canary"); n != wantWarnings {
				t.Errorf("email warnings = %d, want %d", n, wantWarnings)
			}
			calls := h.aws.servedPath("/Get-Usage-Limits")
			if len(calls) != 1 {
				t.Fatalf("email reads = %d, want 1", len(calls))
			}
			c := calls[0]
			if c.query.Get("isEmailRequired") != "true" || c.auth != "Bearer alice-access" || c.query.Get("profileArn") != tc.wantArn {
				t.Errorf("email request = %+v, want isEmailRequired, alice's bearer and profileArn %q", c, tc.wantArn)
			}
		})
	}
	t.Run("timeout", func(t *testing.T) {
		rec := capture.Default(t)
		h := newVaultHarness(t, "alice")
		h.b.emailTimeout = 20 * time.Millisecond
		h.aws.set(func(f *fakeAWS) { f.emailGate = make(chan struct{}) })
		if st := h.signIn("alice", builderID(), profilesOf("alice")); st.Kind != signin.StatusSignedIn || st.Label.Email != "" {
			t.Errorf("Status = %+v, want signed in with no email", st)
		}
		if n := emailWarnings(t, rec, "alice-access"); n != 1 {
			t.Errorf("email warnings = %d, want 1", n)
		}
	})
	t.Run("sign_out_during_the_read", func(t *testing.T) {
		h := newVaultHarness(t, "alice")
		gone := make(chan struct{})
		h.aws.set(func(f *fakeAWS) { f.emailGate, f.emailGone = make(chan struct{}), gone })
		if _, err := h.v.StartSignIn(t.Context(), ID, builderID()); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for len(h.aws.servedPath("/Get-Usage-Limits")) == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if err := h.v.SignOut(ID); err != nil {
			t.Fatal(err)
		}
		select {
		case <-gone:
		case <-time.After(5 * time.Second):
			t.Fatal("the sign-out did not cancel the email read")
		}
		if st := h.v.Status(ID); st.Kind != signin.StatusSignedOut {
			t.Errorf("Status = %v, want nothing stored", st.Kind)
		}
	})
}

func TestSignOut_LeaseBoundIsTheAdvertisedExpiry(t *testing.T) {
	h := newVaultHarness(t, "alice")
	h.signIn("alice", builderID(), profilesOf("alice"))
	handedOut := time.Now()
	a, ok := h.answer()
	if !ok {
		t.Fatal("no answer")
	}
	if exp := advertised(t, a); exp.After(handedOut.Add(leaseCap + time.Second)) {
		t.Errorf("the last token was advertised to %v, past 240 s after it was handed out", exp.Sub(handedOut))
	}
	if err := h.v.SignOut(ID); err != nil {
		t.Fatal(err)
	}
	if a, ok := h.answer(); ok {
		t.Errorf("after the sign-out the engine was answered %+v", a)
	}
}

// TestRefresh_AShortRefreshIsNeverAnswered: KAS takes no answer advertised 180 s or less out, so a
// refresh the provider gave a lifetime the stale floor refuses is no answer, while the token the
// sign-in got (200 s, too short to serve as well) is not served either.
func TestRefresh_AShortRefreshIsNeverAnswered(t *testing.T) {
	for _, tc := range []struct {
		name      string
		expiresIn string
		served    bool
	}{
		{name: "at_the_kas_floor", expiresIn: "180"},
		{name: "at_the_stale_floor", expiresIn: "210"},
		{name: "an_hour", expiresIn: "3600", served: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newVaultHarness(t, "alice")
			h.aws.set(func(f *fakeAWS) { f.ttl = "200" })
			h.signIn("alice", builderID(), profilesOf("alice"))
			h.aws.set(func(f *fakeAWS) {
				f.refresh = scripted{status: 200, body: `{"accessToken":"alice-access-2","expiresIn":` + tc.expiresIn + `}`}
			})
			asked := time.Now()
			a, ok := h.answer()
			if ok != tc.served {
				t.Fatalf("answered = %v (%+v), want %v", ok, a, tc.served)
			}
			if ok && (a.AccessToken != "alice-access-2" || !advertised(t, a).After(asked.Add(180*time.Second))) {
				t.Errorf("answer = %+v, want the renewal advertised more than 180 s out", a)
			}
			if n := len(h.aws.servedPath("/token")); n != 2 {
				t.Errorf("token calls = %d, want the device grant and one refresh", n)
			}
		})
	}
}

func TestRefresh_RotatesTheRefreshTokenWhenReturned(t *testing.T) {
	aws := newFakeAWS(t, "alice")
	b := backendFor(aws)
	r := testRecord(providerBuilderID, "")
	aws.set(func(f *fakeAWS) {
		f.refresh = scripted{status: 200, body: `{"accessToken":"access-2","refreshToken":"refresh-2","expiresIn":3600}`}
	})
	c, err := b.Refresh(t.Context(), credOf(t, r))
	if err != nil {
		t.Fatal(err)
	}
	nr, err := decodeRecord(c)
	if err != nil || nr.AccessToken != "access-2" || nr.RefreshToken != "refresh-2" {
		t.Errorf("renewed = %+v, %v", nr, err)
	}
	calls := aws.servedPath("/token")
	if len(calls) != 1 || !strings.Contains(calls[0].body, `"refreshToken":"refresh-1"`) || !strings.Contains(calls[0].body, `"clientSecret":"client-secret"`) {
		t.Errorf("refresh request = %+v", calls)
	}
	aws.set(func(f *fakeAWS) {
		f.refresh = scripted{status: 200, body: `{"accessToken":"access-3","expiresIn":3600}`}
	})
	c, err = b.Refresh(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	if nr, _ := decodeRecord(c); nr.RefreshToken != "refresh-2" {
		t.Errorf("refresh token = %q, want the previous one kept when none is returned", nr.RefreshToken)
	}
}

func TestRefresh_RejectionClasses(t *testing.T) {
	aws := newFakeAWS(t, "alice")
	b := backendFor(aws)
	for status, rejected := range map[int]bool{400: true, 401: true, 403: true, 404: false, 429: false, 500: false, 307: false} {
		aws.set(func(f *fakeAWS) { f.refresh = scripted{status: status, body: `{"error":"invalid_grant"}`} })
		_, err := b.Refresh(t.Context(), credOf(t, testRecord(providerBuilderID, "")))
		if err == nil || errors.Is(err, signin.ErrRejected) != rejected {
			t.Errorf("status %d: error %v, want rejected %v", status, err, rejected)
		}
	}
}

func TestRefresh_AnExpiredClientRegistrationIsARejection(t *testing.T) {
	aws := newFakeAWS(t, "alice")
	b := backendFor(aws)
	r := testRecord(providerBuilderID, "")
	r.ClientSecretExpiresAt = time.Now().Add(-time.Minute)
	if _, err := b.Refresh(t.Context(), credOf(t, r)); !errors.Is(err, signin.ErrRejected) {
		t.Errorf("error = %v, want ErrRejected", err)
	}
	if n := len(aws.servedPath("/token")); n != 0 {
		t.Errorf("token calls = %d, want none", n)
	}
}

func TestBegin_RefusesBadParams(t *testing.T) {
	aws := newFakeAWS(t, "alice")
	b := backendFor(aws)
	for _, tc := range []struct {
		text string
		p    signin.FlowParams
	}{
		{p: signin.FlowParams{Flow: signin.FlowDevice, StartURL: "http://d-1.awsapps.com/start"}, text: textStartURLShape},
		{p: signin.FlowParams{Flow: signin.FlowDevice, StartURL: "https://user:pw@d-1.awsapps.com/start"}, text: textStartURLShape},
		{p: signin.FlowParams{Flow: signin.FlowDevice, StartURL: "https://d-1.awsapps.com/start", Region: "--region"}},
	} {
		_, err := b.Begin(t.Context(), tc.p)
		if !errors.Is(err, signin.ErrInvalidParams) {
			t.Errorf("Begin(%+v) = %v, want ErrInvalidParams", tc.p, err)
		}
		if fe, ok := errors.AsType[*signin.FailureError](err); tc.text != "" && (!ok || fe.Text != tc.text) {
			t.Errorf("Begin(%+v) = %v, want the user text %q", tc.p, err, tc.text)
		}
	}
	if n := len(aws.served()); n != 0 {
		t.Errorf("calls = %d, want none for refused params", n)
	}
}

func TestBegin_SendsTheCanonicalStartURL(t *testing.T) {
	aws := newFakeAWS(t, "alice")
	r, err := signInDirect(t, backendFor(aws), signin.FlowParams{Flow: signin.FlowDevice, StartURL: "acme.awsapps.com/#/", Region: "eu-west-1"})
	if err != nil {
		t.Fatal(err)
	}
	const want = "https://acme.awsapps.com/start"
	auth := aws.servedPath("/device_authorization")
	if len(auth) != 1 || !strings.Contains(auth[0].body, `"startUrl":"`+want+`"`) {
		t.Errorf("device authorization = %+v, want startUrl %q", auth, want)
	}
	if r.StartURL != want {
		t.Errorf("record StartURL = %q, want %q", r.StartURL, want)
	}
}

// TestBegin_RefusalAdvice: AWS SSO-OIDC answers a start URL it does not know with 400
// invalid_request, "Invalid start url provided", and a portal asked of the wrong region with a bare
// "Invalid request".
func TestBegin_RefusalAdvice(t *testing.T) {
	invalid := func(desc string) scripted {
		return scripted{status: 400, body: `{"error":"invalid_request","error_description":"` + desc + `"}`}
	}
	for _, tc := range []struct {
		answer      scripted
		params      signin.FlowParams
		name        string
		want        string
		wantRegion  bool
		wantInvalid bool
	}{
		{name: "start_url_refused", answer: invalid("Invalid start url provided"), params: idcParams, want: textStartURLRefused, wantInvalid: true},
		{name: "region_refused", answer: invalid("Invalid request"), params: idcParams, wantRegion: true, wantInvalid: true},
		{name: "region_refused_by_header", answer: scripted{status: 400, header: http.Header{"X-Amzn-Errortype": {"InvalidRequestException:http://internal.amazon.com/coral/com.amazonaws.ssooidc/"}}}, params: idcParams, wantRegion: true, wantInvalid: true},
		{name: "forbidden", answer: scripted{status: 403, body: `{"error":"access_denied"}`}, params: idcParams, want: textOIDCRefused},
		{name: "server_error", answer: scripted{status: 500}, params: idcParams, want: textOIDCUnreachable},
		{name: "builder_id", answer: invalid("Invalid request"), params: builderID(), want: textOIDCRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aws := newFakeAWS(t, "alice")
			aws.set(func(f *fakeAWS) { f.authorization = tc.answer })
			_, err := backendFor(aws).Begin(t.Context(), tc.params)
			fe, ok := errors.AsType[*signin.FailureError](err)
			if !ok {
				t.Fatalf("Begin = %v, want a FailureError", err)
			}
			if tc.wantRegion && (!strings.Contains(fe.Text, tc.params.Region) || fe.Text == textOIDCRefused) {
				t.Errorf("text = %q, want the region advice naming %s", fe.Text, tc.params.Region)
			}
			if !tc.wantRegion && fe.Text != tc.want {
				t.Errorf("text = %q, want %q", fe.Text, tc.want)
			}
			if errors.Is(err, signin.ErrInvalidParams) != tc.wantInvalid {
				t.Errorf("ErrInvalidParams = %v, want %v", !tc.wantInvalid, tc.wantInvalid)
			}
			if strings.Contains(fe.Error(), "Invalid start url") {
				t.Errorf("the error carries AWS's description: %v", fe)
			}
		})
	}
}

func TestBegin_RegistrationRefusalIsNotUnreachable(t *testing.T) {
	aws := newFakeAWS(t, "alice")
	aws.set(func(f *fakeAWS) { f.register = scripted{status: 400, body: `{"error":"invalid_client"}`} })
	_, err := backendFor(aws).Begin(t.Context(), idcParams)
	if fe, ok := errors.AsType[*signin.FailureError](err); !ok || fe.Text != textOIDCRefused {
		t.Errorf("Begin = %v, want %q", err, textOIDCRefused)
	}
}

func TestBegin_RegistersAsMarotteWithTheScopes(t *testing.T) {
	aws := newFakeAWS(t, "alice")
	b := backendFor(aws)
	d, err := b.Begin(t.Context(), builderID())
	if err != nil {
		t.Fatal(err)
	}
	if d.VerificationURL != "https://device.sso.example/?user_code=ABCD-EFGH" || d.UserCode != "ABCD-EFGH" || d.ExpiresIn != 10*time.Minute {
		t.Errorf("Pending = %+v", d.Pending)
	}
	reg := aws.servedPath("/client/register")
	if len(reg) != 1 || !strings.Contains(reg[0].body, `"clientName":"marotte"`) || !strings.Contains(reg[0].body, "codewhisperer:conversations") {
		t.Errorf("registration = %+v", reg)
	}
	auth := aws.servedPath("/device_authorization")
	if len(auth) != 1 || !strings.Contains(auth[0].body, `"startUrl":"`+builderIDStartURL+`"`) {
		t.Errorf("device authorization = %+v", auth)
	}
}
