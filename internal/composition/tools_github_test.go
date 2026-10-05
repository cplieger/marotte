package composition

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
	"github.com/cplieger/marotte/internal/forges"
	"github.com/cplieger/toolbelt/v3"
)

// connectGitHubDotcom writes what a github.com connect leaves under configDir:
// its connection record and its stored token.
func connectGitHubDotcom(t *testing.T, configDir, token string) {
	t.Helper()
	id := forges.MakeID(forges.KindGitHub, "github.com")
	doc, err := json.Marshal(map[string]any{"connections": []map[string]string{{"id": id, "kind": "github", "host": "github.com"}}})
	if err != nil {
		t.Fatalf("Setup: marshal the record: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "forge-connections.json"), doc, 0o600); err != nil {
		t.Fatalf("Setup: write the record: %v", err)
	}
	store, err := creds.OpenFileStore(filepath.Join(configDir, "forge-store"))
	if err != nil {
		t.Fatalf("Setup: open the credential store: %v", err)
	}
	if err := store.Save(id, creds.Record{
		Family: forgeapi.FamilyGitHub, WebBaseURL: "https://github.com", Kind: forgeapi.CredKindStaticPAT,
		Token: token, Issued: time.Now().Truncate(time.Second), Account: "alice",
	}); err != nil {
		t.Fatalf("Setup: save the credential: %v", err)
	}
}

func TestGitHubTokenFor_NoConnectionIsAnonymousWithoutFailing(t *testing.T) {
	token, err := githubTokenFor(forges.NewManager(t.TempDir()))(t.Context())
	if token != "" || err != nil {
		t.Errorf("githubTokenFor() with no connection = (%q, %v), want (\"\", nil)", token, err)
	}
}

func TestGitHubTokenFor_TheConnectionGivesItsToken(t *testing.T) {
	dir := t.TempDir()
	connectGitHubDotcom(t, dir, "ghp-from-sources")
	token, err := githubTokenFor(forges.NewManager(dir))(t.Context())
	if token != "ghp-from-sources" || err != nil {
		t.Errorf("githubTokenFor() with a github.com connection = (%q, %v), want (\"ghp-from-sources\", nil)", token, err)
	}
}

func rateLimitedJob(rl *toolbelt.GitHubRateLimit) *toolbelt.Job {
	return &toolbelt.Job{
		ID: "j1", Kind: toolbelt.JobKindReconcile, State: toolbelt.JobFailed,
		Error: "GitHub API rate limit reached", ErrorCode: toolbelt.ErrorCodeGitHubRateLimited, RateLimit: rl,
	}
}

func TestWarnIfGitHubRateLimited_NamesTheResetAndTheFix(t *testing.T) {
	reset := time.Date(2026, 10, 5, 21, 30, 0, 0, time.UTC)
	cases := []struct {
		name string
		rl   toolbelt.GitHubRateLimit
		hint string
	}{
		{"without an account", toolbelt.GitHubRateLimit{ResetAt: reset.UnixMilli(), Limit: 60}, "connect a GitHub account in Git -> Sources"},
		{"with an account", toolbelt.GitHubRateLimit{ResetAt: reset.UnixMilli(), Limit: 5000, Authenticated: true}, "retry after the reset"},
		// An account does not raise the secondary limit, so the fix is waiting.
		{"secondary without an account", toolbelt.GitHubRateLimit{ResetAt: reset.UnixMilli(), Secondary: true}, "GitHub's secondary rate limit was reached; retry after the reset"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureDefaultLogger(t)
			warnIfGitHubRateLimited(rateLimitedJob(&tc.rl))
			lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
			if len(lines) != 1 {
				t.Fatalf("warnIfGitHubRateLimited logged %d lines, want one:\n%s", len(lines), logs)
			}
			var rec map[string]any
			if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
				t.Fatalf("decode the line %q: %v", lines[0], err)
			}
			if rec["level"] != "WARN" {
				t.Errorf("level = %v, want WARN", rec["level"])
			}
			if got, want := rec["resets_at"], time.UnixMilli(reset.UnixMilli()).Format(time.RFC3339); got != want {
				t.Errorf("resets_at = %v, want %v", got, want)
			}
			if hint, _ := rec["hint"].(string); !strings.Contains(hint, tc.hint) {
				t.Errorf("hint = %q, want it to name %q", hint, tc.hint)
			}
			if got, _ := rec["secondary"].(bool); got != tc.rl.Secondary {
				t.Errorf("secondary = %v, want %v", rec["secondary"], tc.rl.Secondary)
			}
		})
	}
}

func TestWarnIfGitHubRateLimited_SilentForAnyOtherJob(t *testing.T) {
	other := rateLimitedJob(&toolbelt.GitHubRateLimit{})
	other.ErrorCode, other.RateLimit = "", nil
	done := rateLimitedJob(nil)
	done.State, done.ErrorCode = toolbelt.JobDone, ""
	cases := map[string]*toolbelt.Job{"nil": nil, "another failure": other, "a finished job": done}
	for name, j := range cases {
		t.Run(name, func(t *testing.T) {
			logs := captureDefaultLogger(t)
			warnIfGitHubRateLimited(j)
			if logs.Len() != 0 {
				t.Errorf("warnIfGitHubRateLimited(%s) logged:\n%s", name, logs)
			}
		})
	}
}
