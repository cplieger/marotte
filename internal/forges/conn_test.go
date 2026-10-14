package forges

// One forgeapi client per loaded sign-in: every reader shares it, so its read
// cache saves the requests it was adopted for, and only a sign-in, a sign-out
// or a Load replaces it.

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

const githubRepoRow = `[{"id":1,"name":"app","full_name":"alice/app","owner":{"login":"alice"},` +
	`"html_url":"https://github.com/alice/app","default_branch":"main"}]`

// revalidatingRepos answers the repository list with ETag v1, and a request
// carrying it with 304.
func revalidatingRepos(s *stubForge) {
	s.handle("/user/repos", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte(githubRepoRow))
	})
}

func TestConn_ASecondReadReusesTheConn(t *testing.T) {
	stub := newStubForge()
	stub.answer("/user", http.StatusOK, `{"login":"alice"}`)
	revalidatingRepos(stub)
	h := newConnectHarness(t, stub)
	id := h.signedIn(t, KindGitHub, "", "ghp_secret", "")
	sent := len(stub.validatorsOf())

	for range 2 {
		if rec := h.do(t, http.MethodGet, "/api/forges/"+id+"/repos?refresh=1", ""); rec.Code != http.StatusOK {
			t.Fatalf("GET repos = %d %s", rec.Code, rec.Body)
		}
	}

	if got := stub.validatorsOf()[sent:]; len(got) != 2 || got[0] != "" || got[1] != `"v1"` {
		t.Errorf("the two reads carried validators %q, want none then the first's ETag", got)
	}
	rh, err := h.m.reader(id)
	if err != nil {
		t.Fatal(err)
	}
	if hits := rh.core.BudgetState().ReadCache.Hits; hits != 1 {
		t.Errorf("the connection's read cache served %d hits, want 1: both reads went through one client", hits)
	}
}

// builtClients counts the clients newCore builds for loaded sign-ins, which
// read through the stored token, and keeps each.
type builtClients struct {
	built []*whoCore
	mu    sync.Mutex
}

func (b *builtClients) newCore(_ *connectionSpec, opts []forgeapi.Option) (forgeapi.Core, error) {
	c := &whoCore{}
	if _, held := forgeapi.Resolve(opts...).Credential.(storedToken); held {
		b.mu.Lock()
		b.built = append(b.built, c)
		b.mu.Unlock()
	}
	return c, nil
}

func (b *builtClients) all() []*whoCore {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]*whoCore(nil), b.built...)
}

func TestConn_SignInReplacesTheConnOnce(t *testing.T) {
	isolateGit(t)
	b := &builtClients{}
	m, _ := newManager(t, t.TempDir(), nil)
	m.newCore = b.newCore
	m.Load(t.Context())
	spec := githubRecord()
	for _, token := range []string{"token-a", "token-b"} {
		if _, err := m.signIn(t.Context(), m.deadlines.from(time.Now()), &spec, new(patFor(spec.webBase(), token))); err != nil {
			t.Fatalf("signIn(%s) = %v", token, err)
		}
	}

	built := b.all()
	if len(built) != 2 {
		t.Fatalf("two sign-ins built %d clients, want one each", len(built))
	}
	if built[0].closed.Load() != 1 || built[1].closed.Load() != 0 {
		t.Errorf("closes: A %d, B %d; want A closed once by the replacing sign-in and B open",
			built[0].closed.Load(), built[1].closed.Load())
	}
	if rh, err := m.reader(spec.ID); err != nil || rh.core != built[1] {
		t.Errorf("reader() answers %v (%v), want B's client", rh.core, err)
	}

	t.Run("the new client holds nothing", func(t *testing.T) {
		stub := newStubForge()
		stub.answer("/user", http.StatusOK, `{"login":"alice"}`)
		revalidatingRepos(stub)
		h := newConnectHarness(t, stub)
		id := h.signedIn(t, KindGitHub, "", "ghp_a", "")
		if rec := h.do(t, http.MethodGet, "/api/forges/"+id+"/repos?refresh=1", ""); rec.Code != http.StatusOK {
			t.Fatalf("GET repos = %d %s", rec.Code, rec.Body)
		}
		h.signedIn(t, KindGitHub, "", "ghp_b", "")
		sent := len(stub.validatorsOf())
		if rec := h.do(t, http.MethodGet, "/api/forges/"+id+"/repos?refresh=1", ""); rec.Code != http.StatusOK {
			t.Fatalf("GET repos = %d %s", rec.Code, rec.Body)
		}
		if got := stub.validatorsOf()[sent:]; len(got) != 1 || got[0] != "" {
			t.Errorf("the first read after the sign-in carried %q, want no validator: the new account starts empty", got)
		}
	})
}

func TestConn_SignOutClosesTheConn(t *testing.T) {
	isolateGit(t)
	b := &builtClients{}
	m, _ := newManager(t, t.TempDir(), nil)
	m.newCore = b.newCore
	m.Load(t.Context())
	spec := githubRecord()
	if _, err := m.signIn(t.Context(), m.deadlines.from(time.Now()), &spec, new(patFor(spec.webBase(), "token"))); err != nil {
		t.Fatal(err)
	}
	if err := m.signOut(t.Context(), m.deadlines.from(time.Now()), spec.ID); err != nil {
		t.Fatal(err)
	}
	if built := b.all(); len(built) != 1 || built[0].closed.Load() != 1 {
		t.Errorf("after a sign-out the client was closed %d times, want once", built[0].closed.Load())
	}
}

func TestConn_ATokenRefreshKeepsTheConn(t *testing.T) {
	ts := newTokenServer(t, rotatedAnswer)
	spec := ts.record()
	cred := rotatingCredential(ts.srv.URL, time.Minute)
	isolateGit(t)
	cfg := t.TempDir()
	seedConnections(t, cfg, "alice", spec)
	if err := openTestStore(t, cfg).Save(slotKey(spec.ID, 0), cred); err != nil {
		t.Fatal(err)
	}
	m, _ := newManager(t, cfg, nil)
	builds := 0
	m.newCore = func(s *connectionSpec, opts []forgeapi.Option) (forgeapi.Core, error) {
		builds++
		return newFamilyClient(s, opts)
	}
	m.Load(t.Context())
	before, err := m.reader(spec.ID)
	if err != nil {
		t.Fatal(err)
	}

	NewKeeper(m).pass(t.Context())

	if ts.refreshes.Load() != 1 {
		t.Fatalf("Setup: the pass sent %d refreshes, want 1", ts.refreshes.Load())
	}
	after, err := m.reader(spec.ID)
	if err != nil || after.core != before.core || builds != 1 {
		t.Errorf("after a refresh the client is %v (was %v) over %d builds, want the same one built once", after.core, before.core, builds)
	}
}
