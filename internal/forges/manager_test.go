package forges

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

// TestManagerList_SortsByKindThenHost pins List's ordering contract:
// Kind first, Host as tiebreaker. Two GitHub hosts exercise the
// same-kind tiebreaker; a GitLab host that sorts before both proves the
// primary key is Kind.
func TestManagerList_SortsByKindThenHost(t *testing.T) {
	isolateGit(t)
	stubPath(t)
	cfg := t.TempDir()
	seedConnections(t, cfg, "alice",
		specFor(KindGitHub, "ccc.example"), specFor(KindGitHub, "bbb.example"), specFor(KindGitLab, "aaa.example"))
	m, _ := loadedManager(t, cfg, &fakeCore{})

	list := m.List()

	got := make([]string, len(list))
	for i, f := range list {
		got[i] = string(f.Kind) + "|" + f.Host
	}
	want := []string{
		"github|bbb.example", // Kind primary: all github before gitlab...
		"github|ccc.example", // ...Host secondary: bbb before ccc
		"gitlab|aaa.example", // gitlab last despite its host sorting first
	}
	if len(got) != len(want) {
		t.Fatalf("List() returned %d forges, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("List()[%d] = %q, want %q (full order: %v)", i, got[i], want[i], got)
		}
	}
}

func TestManagerList_AnUnopenableStoreMakesEveryRowUnusable(t *testing.T) {
	stubPath(t)
	cfg := t.TempDir()
	store := filepath.Join(cfg, credentialStoreDir)
	if err := os.Mkdir(store, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store, 0o750); err != nil {
		t.Fatal(err)
	}
	writeRecords(t, cfg, connectionRecord{connectionSpec: githubRecord()})
	m, _ := loadedManager(t, cfg, nil)

	list := m.List()
	if len(list) != 1 {
		t.Fatalf("List() = %+v, want the one record row", list)
	}
	if list[0].Status != ForgeUnusable || list[0].Error == nil {
		t.Fatalf("row over an unopenable store = %+v, want unusable with its reason", list[0])
	}
	if msg := list[0].Error.Message; !strings.Contains(msg, store) || !strings.Contains(msg, "0700") {
		t.Errorf("row message = %q, want the store directory and its required mode", msg)
	}
	if list[0].Error.Code != codeConnectionUnusable {
		t.Errorf("row code = %q, want %q: no check, sign-in or sign-out can succeed until the mode is fixed",
			list[0].Error.Code, codeConnectionUnusable)
	}
}

func TestManagerList_ARecordWithNoCredentialIsReconnect(t *testing.T) {
	isolateGit(t)
	stubPath(t)
	cfg := t.TempDir()
	seedConnections(t, cfg, "bob", githubRecord())
	writeRecords(t, cfg, connectionRecord{connectionSpec: githubRecord()}, connectionRecord{connectionSpec: gitlabRecord()})
	m, _ := loadedManager(t, cfg, &fakeCore{})

	if got := viewFor(t, m, "github:github.com"); got.Status != ForgeConnected || got.Username != "bob" || got.Error != nil {
		t.Errorf("github row = %+v, want connected as bob", got)
	}
	if got := viewFor(t, m, "gitlab:gitlab.com"); got.Status != ForgeReconnect || got.Error == nil ||
		got.Error.Message != emptySlotReason {
		t.Errorf("a record with no stored credential = %+v, want reconnect naming the missing credential", got)
	}
}

// minePRs is a connection's ListMyPRs answer: #9 in the tracked bob/app and #10
// in a repository no clone tracks.
func minePRs(family forgeapi.Family, check forgeapi.CheckState) []forgeapi.PullRequest {
	mine := prIn(family, "bob/app", 9, check)
	mine.Title = "Mine"
	return []forgeapi.PullRequest{mine, prIn(family, "bob/elsewhere", 10, forgeapi.CheckFailing)}
}

func pollerView(ctx context.Context, m *Manager, webBase string) ([]ConnectionRead, PRSource) {
	src := NewManagerPRSource(m, fixedOrigins(RepoOrigin{Dir: "app", WebBase: webBase, Slug: "bob/app"}))
	return src.Read(ctx, false, firstPage), src
}

// mineRead answers how reads differ from one connection read by one authored
// page whose first row is #9 Mine in bob/app, or nil.
func mineRead(reads []ConnectionRead) error {
	if len(reads) != 1 || len(reads[0].Pages) != 1 || reads[0].Pages[0].Err != nil {
		return fmt.Errorf("reads = %+v, want one connection read by one authored page", reads)
	}
	rows := reads[0].Pages[0].Rows
	if len(rows) != 2 || rows[0].RepoID != repoIDOf("bob/app") || rows[0].Number != 9 || rows[0].Title != "Mine" {
		return fmt.Errorf("authored rows = %+v, want #9 Mine in bob/app first", rows)
	}
	return nil
}

// A token sign-in to each family is durable: a restart over the same directory
// loads it connected as the same account, and the poller reads it.
func TestPATSignIn_SurvivesARestartAndIsVisibleToThePoller(t *testing.T) {
	for _, tc := range []struct {
		kind         Kind
		host, origin string
		user         string
		family       forgeapi.Family
	}{
		{KindGitHub, "", "https://github.com", `{"login":"alice"}`, forgeapi.FamilyGitHub},
		{KindGitLab, "", "https://gitlab.com", `{"id":1,"username":"alice"}`, forgeapi.FamilyGitLab},
		{KindGitea, "gitea.example", "https://gitea.example", `{"id":1,"login":"alice"}`, forgeapi.FamilyGitea},
		{KindCodeberg, "", "https://codeberg.org", `{"id":1,"login":"alice"}`, forgeapi.FamilyGitea},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			h := newConnectHarness(t, &userWire{status: http.StatusOK, body: tc.user})
			id := h.signedIn(t, tc.kind, tc.host, "pat-secret", "")
			cred, ok, err := openTestStore(t, h.cfgDir).Load(slotKey(id, 0))
			if err != nil || !ok || cred.Family != tc.family || cred.Kind != forgeapi.CredKindStaticPAT ||
				cred.Account != "alice" || cred.WebBaseURL != tc.origin {
				t.Errorf("stored credential = %+v (present %v, err %v), want a static PAT for alice on %s",
					cred, ok, err, tc.origin)
			}
			if got := helperValuesFor(t, tc.origin); len(got) != 2 || got[1] != h.helperValue(t) {
				t.Errorf("credential.%s.helper = %q, want the reset then Marotte's value", tc.origin, got)
			}

			restarted, _ := newManager(t, h.cfgDir, &fakeCore{prs: minePRs(tc.family, forgeapi.CheckFailing)})
			restarted.Load(t.Context())
			got := viewFor(t, restarted, id)
			if got.Status != ForgeConnected || got.Username != "alice" || got.Account != viewFor(t, h.m, id).Account {
				t.Errorf("row after a restart = %+v, want connected as alice under the same sign-in", got)
			}
			reads, src := pollerView(t.Context(), restarted, tc.origin)
			if err := mineRead(reads); err != nil {
				t.Fatalf("Read() after a restart: %v", err)
			}
			want := map[string]string{subjectKey(id, "bob/app", 9): checkFailing}
			if got := trackedVerdicts(t, src); !maps.Equal(got, want) {
				t.Errorf("tracked verdicts after a restart = %v, want %v", got, want)
			}
		})
	}
}
