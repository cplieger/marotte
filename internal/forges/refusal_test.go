package forges

// A refusal is durable: it survives a restart, git's helper declines the
// credential, and no reader sends a request through the row until the remedy
// its status names revives it.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

// refusalLeg is one forge refusing a signed-in account's identity read.
type refusalLeg struct {
	name, user, repos string
	refuse            func(s *stubForge)
	kind              Kind
	status            ForgeStatus
	userBody          string
}

func TestRefusal_SurvivesARestartAndGitDeclines(t *testing.T) {
	legs := []refusalLeg{
		{
			name: "a bare 401", kind: KindGitHub, user: "/user", repos: "/user/repos", userBody: `{"login":"alice"}`,
			status: ForgeReconnect, refuse: func(s *stubForge) { s.answer("/user", http.StatusUnauthorized, "") },
		},
		{
			name: "a scope refusal", kind: KindGitLab, user: "/api/v4/user", repos: "/api/v4/projects",
			userBody: `{"id":1,"username":"alice"}`, status: ForgeRefused,
			refuse: func(s *stubForge) {
				s.answer("/api/v4/user", http.StatusForbidden,
					`{"error":"insufficient_scope","error_description":"The request requires higher privileges."}`)
			},
		},
		{
			name: "a 404", kind: KindGitHub, user: "/user", repos: "/user/repos", userBody: `{"login":"alice"}`,
			status: ForgeRefused, refuse: func(s *stubForge) { s.answer("/user", http.StatusNotFound, `{"message":"Not Found"}`) },
		},
	}
	for _, leg := range legs {
		t.Run(leg.name, func(t *testing.T) { refusalSurvives(t, &leg) })
	}
}

func refusalSurvives(t *testing.T, leg *refusalLeg) {
	stub := newStubForge()
	stub.answer(leg.user, http.StatusOK, leg.userBody)
	stub.answer(leg.repos, http.StatusOK, `[]`)
	h := newConnectHarness(t, stub)
	id := h.signedIn(t, leg.kind, "", "pat-secret", "")
	host := viewFor(t, h.m, id).Host
	leg.refuse(stub)
	if leg.name == "a bare 401" {
		core, err := verifier(h.m, &connectionSpec{ID: id, Kind: leg.kind, Host: host}, "pat-secret")
		if err != nil {
			t.Fatal(err)
		}
		_, werr := core.Whoami(t.Context())
		if fe, ok := errors.AsType[*forgeapi.Error](werr); !ok || fe.Kind != forgeapi.KindUnauthorized || fe.Code != "" {
			t.Fatalf("Setup: a bare 401 reads %v, want KindUnauthorized with no code", werr)
		}
	}
	NewKeeper(h.m).pass(t.Context())
	if got := viewFor(t, h.m, id); got.Status != leg.status {
		t.Fatalf("row after the refusal = %+v, want %s", got, leg.status)
	}

	again := restarted(t, h.m)
	if got := viewFor(t, again, id); got.Status != leg.status {
		t.Errorf("row after a restart = %+v, want %s", got, leg.status)
	}
	sent := len(stub.requests())
	NewKeeper(again).pass(t.Context())
	if out := helperAnswer(t, h.cfgDir, host); out != "" {
		t.Errorf("the git helper answered %q for a refused account, want nothing", out)
	}
	if leg.kind == KindGitHub {
		if token := again.GitHubToken(t.Context()); token != "" {
			t.Errorf("GitHubToken() = %q, want nothing for a refused account", token)
		}
	}
	mux := repoRoutes(again)
	for _, path := range []string{"/api/forges/" + id + "/repos", "/api/forges/" + id + "/capabilities"} {
		if rec := getRoute(t, mux, path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d %s, want 404", path, rec.Code, rec.Body)
		}
	}
	if got := stub.requests()[sent:]; len(got) != 0 {
		t.Errorf("a pass, the helper and the routes sent %q to the forge, want nothing", got)
	}

	stub.answer(leg.user, http.StatusOK, leg.userBody)
	probe := httptest.NewRecorder()
	mux.ServeHTTP(probe, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/forges/"+id+"/probe", nil))
	if leg.status == ForgeReconnect {
		if probe.Code != http.StatusConflict || decodeBody(t, probe)["code"] != codeReconnectRequired {
			t.Errorf("Check of a reconnect row = %d %s, want 409 reconnect_required", probe.Code, probe.Body)
		}
		if got := stub.requests()[sent:]; len(got) != 0 {
			t.Errorf("the Check sent %q, want nothing", got)
		}
		h.signedIn(t, leg.kind, "", "pat-new", "")
		again = restarted(t, h.m)
		mux = repoRoutes(again)
	} else if probe.Code != http.StatusOK {
		t.Errorf("Check with the forge fixed = %d %s, want 200", probe.Code, probe.Body)
	}
	if got := viewFor(t, again, id); got.Status != ForgeConnected {
		t.Errorf("row after its remedy = %+v, want connected", got)
	}
	sent = len(stub.requests())
	if rec := getRoute(t, mux, "/api/forges/"+id+"/repos"); rec.Code != http.StatusOK || len(stub.requests()) == sent {
		t.Errorf("GET repos after the remedy = %d %s with %d forge requests, want 200 through the forge",
			rec.Code, rec.Body, len(stub.requests())-sent)
	}
}

func TestRefused_NoReaderSendsARequest(t *testing.T) {
	core := &whoCore{}
	core.answer(errUnauthorized)
	m, _ := checkManager(t, core)
	if _, err := m.check(t.Context(), ghID, checkBudget); err != nil {
		t.Fatalf("check() = %v", err)
	}
	if got := viewFor(t, m, ghID); got.Status != ForgeReconnect {
		t.Fatalf("Setup: row after a 401 = %+v, want reconnect", got)
	}
	src := NewManagerPRSource(m, fixedOrigins(RepoOrigin{Dir: "app", WebBase: "https://github.com", Slug: "bob/app"}))
	p := NewPRStatusPoller(src, &fakeNotifier{}, (&fakeGate{present: true}).Open)
	p.sweep(t.Context())
	mux := inventoryRoutes(m, p)

	if reads := src.Read(t.Context(), true, firstPage); len(reads) != 0 {
		t.Errorf("a present read = %+v, want no connection read", reads)
	}
	if token := m.GitHubToken(t.Context()); token != "" {
		t.Errorf("GitHubToken() = %q, want nothing", token)
	}
	for _, path := range []string{"/api/forges/" + ghID + "/repos", githubCapsPath} {
		if rec := getRoute(t, mux, path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}
	if got := readInventory(t, mux); len(got) != 0 {
		t.Errorf("inventory = %+v, want the refused row left out", got)
	}
	if core.listCalls != 0 || core.capsRead != 0 {
		t.Errorf("the client was asked %d lists and %d capability reads, want none", core.listCalls, core.capsRead)
	}
}

// A verdict the records file cannot take installs what a restart would load
// from the files as they stand, so the live row, git's helper and a restart
// agree; the keeper repeats the whole verdict once the file is writable.
func TestCheck_AFailedStandingWriteKeepsTheDurableView(t *testing.T) {
	t.Run("a refusal", func(t *testing.T) {
		core := &whoCore{}
		m, _ := checkManager(t, core)
		core.answer(errUnauthorized)
		restore := failingWrites(t, m)

		NewKeeper(m).pass(t.Context())
		got := viewFor(t, m, ghID)
		if got.Status != ForgeRetrying || got.Error == nil || !strings.Contains(got.Error.Message, "refused") ||
			!strings.Contains(got.Error.Message, "disk refused the write") {
			t.Fatalf("row after an unsavable refusal = %+v, want retrying naming the refusal and the write failure", got)
		}
		if recs := readRecords(t, m.configDir); len(recs) != 1 || recs[0].Standing != standingUsable {
			t.Errorf("records = %+v, want the file still usable", recs)
		}
		if _, err := m.reader(ghID); err != nil {
			t.Errorf("reader() = %v, want the row still serving as the files say", err)
		}
		if out := helperAnswer(t, m.configDir, "github.com"); !strings.Contains(out, "password=test-token") {
			t.Errorf("the git helper answered %q, want the credential the files still hold", out)
		}
		if row := viewFor(t, restarted(t, m), ghID); row.Status != ForgeConnected {
			t.Errorf("a restart loads %+v, want connected as the files say", row)
		}

		restore()
		NewKeeper(m).pass(t.Context())
		if got := viewFor(t, m, ghID); got.Status != ForgeReconnect {
			t.Errorf("row after the next pass with the file writable = %+v, want reconnect", got)
		}
		if recs := readRecords(t, m.configDir); len(recs) != 1 || recs[0].Standing != standingReconnect {
			t.Errorf("records = %+v, want standing reconnect", recs)
		}
		if out := helperAnswer(t, m.configDir, "github.com"); out != "" {
			t.Errorf("the git helper answered %q, want nothing", out)
		}
		if row := viewFor(t, restarted(t, m), ghID); row.Status != ForgeReconnect {
			t.Errorf("a restart loads %+v, want reconnect", row)
		}
	})

	t.Run("a revival", func(t *testing.T) {
		isolateGit(t)
		cfg := t.TempDir()
		seedConnections(t, cfg, "bob", githubRecord())
		writeRecords(t, cfg, connectionRecord{connectionSpec: githubRecord(), Standing: standingRefused})
		core := &whoCore{}
		m, _ := loadedManager(t, cfg, core)
		restore := failingWrites(t, m)

		if _, err := m.check(t.Context(), ghID, checkBudget); err != nil {
			t.Fatalf("Check() = %v", err)
		}
		got := viewFor(t, m, ghID)
		if got.Status != ForgeRefused || got.Error == nil || !strings.Contains(got.Error.Message, "disk refused the write") ||
			!strings.Contains(got.Error.Message, "the forge accepted this account") {
			t.Fatalf("row after an unsavable revival = %+v, want still refused naming the acceptance and the write failure", got)
		}
		if _, err := m.reader(ghID); err == nil {
			t.Error("reader() of a row the file still refuses = a handle, want refused")
		}
		if out := helperAnswer(t, cfg, "github.com"); out != "" {
			t.Errorf("the git helper answered %q, want nothing", out)
		}
		restore()
		if _, err := m.check(t.Context(), ghID, checkBudget); err != nil {
			t.Fatalf("Check() = %v", err)
		}
		if got := viewFor(t, m, ghID); got.Status != ForgeConnected {
			t.Errorf("row after a Check with the file writable = %+v, want connected", got)
		}
	})

	t.Run("a revival the forge did not answer", func(t *testing.T) {
		isolateGit(t)
		cfg := t.TempDir()
		seedConnections(t, cfg, "bob", githubRecord())
		writeRecords(t, cfg, connectionRecord{connectionSpec: githubRecord(), Standing: standingRefused})
		core := &whoCore{}
		core.answer(errTransient)
		m, _ := loadedManager(t, cfg, core)
		failingWrites(t, m)

		if _, err := m.check(t.Context(), ghID, checkBudget); err != nil {
			t.Fatalf("Check() = %v", err)
		}
		got := viewFor(t, m, ghID)
		if got.Status != ForgeRefused || got.Error == nil {
			t.Fatalf("row after an unsavable check the forge did not answer = %+v, want still refused", got)
		}
		if msg := got.Error.Message; strings.Contains(msg, "accepted") || !strings.Contains(msg, "did not answer the check (") ||
			!strings.Contains(msg, "no route to host") ||
			!strings.Contains(msg, "disk refused the write") {
			t.Errorf("message = %q, want it to name the forge's missing answer and the write failure, never an acceptance", msg)
		}
	})

	t.Run("a moved cursor", func(t *testing.T) {
		core := &whoCore{}
		m, _ := checkManager(t, core)
		NewKeeper(m).pass(t.Context())
		core.mu.Lock()
		core.cursor = "repo-7:12"
		core.mu.Unlock()
		restore := failingWrites(t, m)

		NewKeeper(m).pass(t.Context())
		if recs := readRecords(t, m.configDir); recs[0].RotationCursor != "" {
			t.Errorf("stored cursor after a failed write = %q, want the file's own", recs[0].RotationCursor)
		}
		restore()
		NewKeeper(m).pass(t.Context())
		if recs := readRecords(t, m.configDir); recs[0].RotationCursor != "repo-7:12" {
			t.Errorf("stored cursor after the next pass = %q, want repo-7:12", recs[0].RotationCursor)
		}
	})
}
