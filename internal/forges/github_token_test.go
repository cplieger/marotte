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

func dotcomRecord() connectionSpec { return specFor(KindGitHub, "github.com") }

func patFor(webBase, token string) creds.Record {
	return creds.Record{
		Family: forgeapi.FamilyGitHub, WebBaseURL: webBase, Kind: forgeapi.CredKindStaticPAT,
		Token: token, Issued: time.Now().Truncate(time.Second), Account: "alice",
	}
}

// storedManager is a loaded manager over a fresh config directory holding
// specs, each with a static token named after its id, and the store it reads.
func storedManager(t *testing.T, specs ...connectionSpec) (*Manager, *creds.FileStore) {
	t.Helper()
	cfg := t.TempDir()
	seedConnections(t, cfg, "alice", specs...)
	store := openTestStore(t, cfg)
	for i := range specs {
		if err := store.Save(slotKey(specs[i].ID, 0), patFor(specs[i].webBase(), "token-of-"+specs[i].ID)); err != nil {
			t.Fatalf("Setup: save the credential: %v", err)
		}
	}
	m, _ := loadedManager(t, cfg, nil)
	return m, store
}

func TestGitHubToken_NoConnectionIsAnonymous(t *testing.T) {
	m, _ := storedManager(t)
	if got := m.GitHubToken(t.Context()); got != "" {
		t.Errorf("GitHubToken() with no connection = %q, want \"\"", got)
	}
}

func TestGitHubToken_TheGitHubComConnectionGivesItsToken(t *testing.T) {
	rec := dotcomRecord()
	m, _ := storedManager(t, rec)
	if got, want := m.GitHubToken(t.Context()), "token-of-"+rec.ID; got != want {
		t.Errorf("GitHubToken() with a github.com connection = %q, want %q", got, want)
	}
}

func TestGitHubToken_OtherConnectionsAreAnonymous(t *testing.T) {
	ghes := specFor(KindGitHub, "ghe.example")
	gitlab := specFor(KindGitLab, "gitlab.com")
	gitea := specFor(KindGitea, "github.com")
	proxied := dotcomRecord()
	proxied.APIBaseURL = "https://api.ghe.example"
	cases := []struct {
		name string
		rec  connectionSpec
	}{
		{"enterprise host", ghes},
		{"another forge", gitlab},
		{"another forge kind on github.com's host", gitea},
		{"github.com record pointed at another API", proxied},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := storedManager(t, tc.rec)
			if got := m.GitHubToken(t.Context()); got != "" {
				t.Errorf("GitHubToken() with only %s = %q, want \"\": the engine sends it to api.github.com", tc.rec.ID, got)
			}
		})
	}
}

func TestGitHubToken_ExplicitDotcomAPIBaseStillAnswers(t *testing.T) {
	rec := dotcomRecord()
	rec.APIBaseURL = "https://api.github.com/"
	m, _ := storedManager(t, rec)
	if got, want := m.GitHubToken(t.Context()), "token-of-"+rec.ID; got != want {
		t.Errorf("GitHubToken() with api_base_url %q = %q, want %q", rec.APIBaseURL, got, want)
	}
}

// The host field is typed and the id keeps the typed spelling, while forgeapi
// sends a "GitHub.com" connection's token to api.github.com all the same.
func TestGitHubToken_AHostSpelledInAnotherCaseIsGitHubCom(t *testing.T) {
	rec := specFor(KindGitHub, "GitHub.com")
	m, _ := storedManager(t, rec)
	if got, want := m.GitHubToken(t.Context()), "token-of-"+rec.ID; got != want {
		t.Errorf("GitHubToken() with only %s = %q, want %q", rec.ID, got, want)
	}
}

// A credential the library refuses to use answers anonymous, so the request
// is sent rather than failed and the row's status tells the user. Each is read
// from the store per call, so it needs no reload.
func TestGitHubToken_AnUnusableCredentialIsAnonymous(t *testing.T) {
	rec := dotcomRecord()
	key := slotKey(rec.ID, 0)
	cases := []struct {
		name  string
		setup func(t *testing.T, store *creds.FileStore)
	}{
		{"no stored credential", func(t *testing.T, store *creds.FileStore) {
			if err := store.Delete(key); err != nil {
				t.Fatalf("Setup: Delete() = %v", err)
			}
		}},
		{"marked reconnect-required", func(t *testing.T, store *creds.FileStore) {
			cred := patFor(rec.webBase(), dotcomPAT)
			cred.Usability = creds.UsabilityReconnectRequired
			if err := store.Save(key, cred); err != nil {
				t.Fatalf("Setup: Save() = %v", err)
			}
		}},
		{"expired token", func(t *testing.T, store *creds.FileStore) {
			cred := patFor(rec.webBase(), dotcomPAT)
			cred.Expiry = time.Now().Add(-time.Hour)
			if err := store.Save(key, cred); err != nil {
				t.Fatalf("Setup: Save() = %v", err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, store := storedManager(t, rec)
			tc.setup(t, store)
			if got := m.GitHubToken(t.Context()); got != "" {
				t.Errorf("GitHubToken() over a credential with %s = %q, want \"\"", tc.name, got)
			}
		})
	}
}

// A sign-in and a sign-out reach the next request with no reload and no restart.
func TestGitHubToken_FollowsSignInAndSignOut(t *testing.T) {
	isolateGit(t)
	m, _ := loadedManager(t, t.TempDir(), &fakeCore{})
	if got := m.GitHubToken(t.Context()); got != "" {
		t.Fatalf("Setup: GitHubToken() before the sign-in = %q, want \"\"", got)
	}
	rec := dotcomRecord()
	if _, err := m.signIn(t.Context(), m.deadlines.from(time.Now()), &rec, new(patFor(rec.webBase(), dotcomPAT))); err != nil {
		t.Fatalf("signIn() = %v", err)
	}
	if got := m.GitHubToken(t.Context()); got != dotcomPAT {
		t.Errorf("GitHubToken() after the sign-in = %q, want %q", got, dotcomPAT)
	}
	if err := m.signOut(t.Context(), m.deadlines.from(time.Now()), rec.ID); err != nil {
		t.Fatalf("signOut() = %v", err)
	}
	if got := m.GitHubToken(t.Context()); got != "" {
		t.Errorf("GitHubToken() after the sign-out = %q, want \"\"", got)
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
	m, store := storedManager(t, rec)
	if err := store.Save(slotKey(rec.ID, 0), patFor(rec.webBase(), dotcomPAT)); err != nil {
		t.Fatalf("Setup: Save() = %v", err)
	}
	if got := m.GitHubToken(t.Context()); got != dotcomPAT {
		t.Fatalf("GitHubToken() = %q, want %q", got, dotcomPAT)
	}
	refused := patFor(rec.webBase(), dotcomPAT)
	refused.Usability = creds.UsabilityReconnectRequired
	if err := store.Save(slotKey(rec.ID, 0), refused); err != nil {
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
