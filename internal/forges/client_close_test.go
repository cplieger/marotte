package forges

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/cplieger/forgeapi"
)

// A client Marotte stops holding is released with Close, which frees its pool:
// the obligation the library publishes beside each family client and Core.

// countingFactory is a factory building a fresh fakeCore per call, every one
// kept so a test can read which were released.
func countingFactory() (*clientFactory, *[]*fakeCore) {
	f := newClientFactory()
	built := &[]*fakeCore{}
	f.newCore = func(*connectionRecord, []forgeapi.Option) (forgeapi.Core, error) {
		c := &fakeCore{}
		*built = append(*built, c)
		return c, nil
	}
	return f, built
}

func TestClientFor_ClosesTheClientARebuildReplaces(t *testing.T) {
	store := testStore(t)
	rec := githubRecord()
	saveStatic(t, store, &rec, "tok")
	f, built := countingFactory()

	if _, err := f.clientFor(store, &rec); err != nil {
		t.Fatalf("Setup: clientFor() = %v", err)
	}
	moved := rec
	moved.WebBaseURL = "https://github.example.com"
	if _, err := f.clientFor(store, &moved); err != nil {
		t.Fatalf("Setup: clientFor(re-addressed) = %v", err)
	}
	if len(*built) != 2 {
		t.Fatalf("Setup: a re-addressed record built %d clients, want 2", len(*built))
	}
	if got := (*built)[0].closed.Load(); got != 1 {
		t.Errorf("the replaced client was closed %d times, want 1", got)
	}
	if got := (*built)[1].closed.Load(); got != 0 {
		t.Errorf("the client now answering was closed %d times, want 0", got)
	}
}

func TestRetain_ClosesTheClientOfAConnectionThatWent(t *testing.T) {
	store := testStore(t)
	rec := githubRecord()
	saveStatic(t, store, &rec, "tok")
	f, built := countingFactory()
	if _, err := f.clientFor(store, &rec); err != nil {
		t.Fatalf("Setup: clientFor() = %v", err)
	}

	f.retain(map[string]connectionRecord{rec.ID: rec})
	if got := (*built)[0].closed.Load(); got != 0 {
		t.Errorf("a retained connection's client was closed %d times, want 0", got)
	}
	f.retain(map[string]connectionRecord{})
	if got := (*built)[0].closed.Load(); got != 1 {
		t.Errorf("a dropped connection's client was closed %d times, want 1", got)
	}
}

// The identity read a connect makes before storing anything runs on a client
// nothing keeps.
func TestConnect_ClosesTheVerifierClient(t *testing.T) {
	isolateGit(t)
	stubPath(t)
	m := NewManager(t.TempDir())
	m.executable = func() (string, error) { return testHelperBin, nil }
	f, built := countingFactory()
	m.clients.newCore = f.newCore
	rec := connectionRecord{ID: "github:github.com", Kind: KindGitHub, Host: "github.com"}

	if err := m.connect(t.Context(), &rec, "ghp_secret"); err != nil {
		t.Fatalf("connect(github.com) = %v", err)
	}
	if len(*built) == 0 {
		t.Fatal("connect built no client")
	}
	if got := (*built)[0].closed.Load(); got != 1 {
		t.Errorf("the verifier client connect built was closed %d times, want 1", got)
	}
}

// Detection keeps no client, so every connection it opened is released by the
// time it answers: the rejected candidates' by families.Open, the answering
// family's by Marotte.
func TestDetect_ReleasesEveryConnectionItOpened(t *testing.T) {
	var opened, closed atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := giteaReads[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		switch s {
		case http.StateNew:
			opened.Add(1)
		case http.StateClosed:
			closed.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	h := newConnectHarness(t, nil)

	rec := h.do(t, http.MethodPost, detectPath, detectRequest(srv.URL, bothOptIns))
	if got := decodeBody(t, rec); rec.Code != http.StatusOK || got["kind"] != string(KindGitea) {
		t.Fatalf("detect %s = %d %v, want 200 with kind gitea", srv.URL, rec.Code, got)
	}
	waitFor(t, "every connection detection opened to close", func() bool {
		return opened.Load() > 0 && closed.Load() == opened.Load()
	})
}
