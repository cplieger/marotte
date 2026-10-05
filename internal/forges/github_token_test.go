package forges

import (
	"bytes"
	"os"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
	"github.com/cplieger/slogx/capture"
)

const dotcomPAT = "ghp-dotcom-token"

// dotcomRecord is the github.com connection as a connect writes it.
func dotcomRecord() connectionRecord {
	return connectionRecord{ID: MakeID(KindGitHub, "github.com"), Kind: KindGitHub, Host: "github.com"}
}

// patFor is a static token credential for webBase.
func patFor(webBase, token string) creds.Record {
	return creds.Record{
		Family: forgeapi.FamilyGitHub, WebBaseURL: webBase, Kind: forgeapi.CredKindStaticPAT,
		Token: token, Issued: time.Now().Truncate(time.Second), Account: "alice",
	}
}

// storedManager is a manager over a fresh config directory holding recs, each
// with a static token named after its id.
func storedManager(t *testing.T, recs ...connectionRecord) *Manager {
	t.Helper()
	m := NewManager(t.TempDir())
	if m.store == nil {
		t.Fatalf("Setup: open the credential store: %s", m.storeReason)
	}
	for i := range recs {
		if err := m.store.Save(recs[i].ID, patFor(recs[i].webBase(), "token-of-"+recs[i].ID)); err != nil {
			t.Fatalf("Setup: save the credential: %v", err)
		}
	}
	saveRecords(t, m.conns, recs...)
	return m
}

func TestGitHubToken_NoConnectionIsAnonymous(t *testing.T) {
	m := storedManager(t)
	if got := m.GitHubToken(t.Context()); got != "" {
		t.Errorf("GitHubToken() with no connection = %q, want \"\"", got)
	}
}

func TestGitHubToken_TheGitHubComConnectionGivesItsToken(t *testing.T) {
	rec := dotcomRecord()
	m := storedManager(t, rec)
	if got, want := m.GitHubToken(t.Context()), "token-of-"+rec.ID; got != want {
		t.Errorf("GitHubToken() with a github.com connection = %q, want %q", got, want)
	}
}

func TestGitHubToken_OtherConnectionsAreAnonymous(t *testing.T) {
	ghes := connectionRecord{ID: MakeID(KindGitHub, "ghe.example"), Kind: KindGitHub, Host: "ghe.example"}
	gitlab := connectionRecord{ID: MakeID(KindGitLab, "gitlab.com"), Kind: KindGitLab, Host: "gitlab.com"}
	gitea := connectionRecord{ID: MakeID(KindGitea, "github.com"), Kind: KindGitea, Host: "github.com"}
	proxied := dotcomRecord()
	proxied.APIBaseURL = "https://api.ghe.example"
	cases := []struct {
		name string
		rec  connectionRecord
	}{
		{"enterprise host", ghes},
		{"another forge", gitlab},
		{"another forge kind on github.com's host", gitea},
		{"github.com record pointed at another API", proxied},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := storedManager(t, tc.rec)
			if got := m.GitHubToken(t.Context()); got != "" {
				t.Errorf("GitHubToken() with only %s = %q, want \"\": the engine sends it to api.github.com", tc.rec.ID, got)
			}
		})
	}
}

func TestGitHubToken_ExplicitDotcomAPIBaseStillAnswers(t *testing.T) {
	rec := dotcomRecord()
	rec.APIBaseURL = "https://api.github.com/"
	m := storedManager(t, rec)
	if got, want := m.GitHubToken(t.Context()), "token-of-"+rec.ID; got != want {
		t.Errorf("GitHubToken() with api_base_url %q = %q, want %q", rec.APIBaseURL, got, want)
	}
}

// The host field is typed and the id keeps the typed spelling, while forgeapi
// sends a "GitHub.com" connection's token to api.github.com all the same.
func TestGitHubToken_AHostSpelledInAnotherCaseIsGitHubCom(t *testing.T) {
	rec := connectionRecord{ID: MakeID(KindGitHub, "GitHub.com"), Kind: KindGitHub, Host: "GitHub.com"}
	m := storedManager(t, rec)
	if got, want := m.GitHubToken(t.Context()), "token-of-"+rec.ID; got != want {
		t.Errorf("GitHubToken() with only %s = %q, want %q", rec.ID, got, want)
	}
}

// A credential the library refuses to use answers anonymous, so the request
// is sent rather than failed and the row's reconnect state tells the user.
func TestGitHubToken_AnUnusableCredentialIsAnonymous(t *testing.T) {
	rec := dotcomRecord()
	cases := []struct {
		name  string
		setup func(t *testing.T, m *Manager)
	}{
		{"no stored credential", func(t *testing.T, m *Manager) {
			if err := m.store.Delete(rec.ID); err != nil {
				t.Fatalf("Setup: Delete() = %v", err)
			}
		}},
		{"marked reconnect-required", func(t *testing.T, m *Manager) {
			cred := patFor(rec.webBase(), dotcomPAT)
			cred.Usability = creds.UsabilityReconnectRequired
			if err := m.store.Save(rec.ID, cred); err != nil {
				t.Fatalf("Setup: Save() = %v", err)
			}
		}},
		{"expired token", func(t *testing.T, m *Manager) {
			cred := patFor(rec.webBase(), dotcomPAT)
			cred.Expiry = time.Now().Add(-time.Hour)
			if err := m.store.Save(rec.ID, cred); err != nil {
				t.Fatalf("Setup: Save() = %v", err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := storedManager(t, rec)
			tc.setup(t, m)
			if got := m.GitHubToken(t.Context()); got != "" {
				t.Errorf("GitHubToken() over a credential with %s = %q, want \"\"", tc.name, got)
			}
		})
	}
}

// The record file and the store are read per call, so a connect and a
// disconnect reach the next request with no refresh and no restart.
func TestGitHubToken_FollowsConnectAndDisconnect(t *testing.T) {
	m := storedManager(t)
	rec := dotcomRecord()
	if got := m.GitHubToken(t.Context()); got != "" {
		t.Fatalf("Setup: GitHubToken() before the connect = %q, want \"\"", got)
	}
	if err := m.store.Save(rec.ID, patFor(rec.webBase(), dotcomPAT)); err != nil {
		t.Fatalf("Setup: Save() = %v", err)
	}
	saveRecords(t, m.conns, rec)
	if got := m.GitHubToken(t.Context()); got != dotcomPAT {
		t.Errorf("GitHubToken() after the connect = %q, want %q", got, dotcomPAT)
	}
	// What a disconnect leaves on disk: no record and no stored credential.
	saveRecords(t, m.conns)
	if err := m.store.Delete(rec.ID); err != nil {
		t.Fatalf("Setup: Delete() = %v", err)
	}
	if got := m.GitHubToken(t.Context()); got != "" {
		t.Errorf("GitHubToken() after the disconnect = %q, want \"\"", got)
	}
}

// The token leaves only through the return value: no log line at any level and
// no process environment variable carries it, whether the read succeeds or not.
func TestGitHubToken_TheTokenReachesOnlyTheCaller(t *testing.T) {
	for _, env := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		t.Setenv(env, "")
		if err := os.Unsetenv(env); err != nil {
			t.Fatalf("Setup: Unsetenv(%s) = %v", env, err)
		}
	}
	logs := capture.Default(t)
	rec := dotcomRecord()
	m := storedManager(t, rec)
	if err := m.store.Save(rec.ID, patFor(rec.webBase(), dotcomPAT)); err != nil {
		t.Fatalf("Setup: Save() = %v", err)
	}
	if got := m.GitHubToken(t.Context()); got != dotcomPAT {
		t.Fatalf("GitHubToken() = %q, want %q", got, dotcomPAT)
	}
	refused := patFor(rec.webBase(), dotcomPAT)
	refused.Usability = creds.UsabilityReconnectRequired
	if err := m.store.Save(rec.ID, refused); err != nil {
		t.Fatalf("Setup: Save() = %v", err)
	}
	if got := m.GitHubToken(t.Context()); got != "" {
		t.Fatalf("GitHubToken() over a refused credential = %q, want \"\"", got)
	}
	if out := rendered(t, logs); bytes.Contains(out, []byte(dotcomPAT)) {
		t.Errorf("the logs carry the token:\n%s", out)
	}
	for _, env := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if v, ok := os.LookupEnv(env); ok {
			t.Errorf("%s = %q after GitHubToken, want it unset", env, v)
		}
	}
}
