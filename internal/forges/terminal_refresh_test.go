package forges

// A refresh the forge answers terminally makes the connection reconnect, and
// it shows reconnect only once a file a restart and git's helper read says so:
// forgeapi's mark on the credential may be held in this process alone when the
// store refused it, so the records file carries the standing too.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/creds"
)

// refusingTokenServer is a TLS stand-in for github.com whose token endpoint
// answers invalid_grant, first running beforeAnswer, and whose identity read
// answers alice. It counts the token requests.
type refusingTokenServer struct {
	*tokenServer
	beforeAnswer atomic.Pointer[func()]
}

func newRefusingTokenServer(t *testing.T) *refusingTokenServer {
	t.Helper()
	ts := &refusingTokenServer{tokenServer: &tokenServer{}}
	ts.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/oauth/access_token":
			ts.refreshes.Add(1)
			if f := ts.beforeAnswer.Load(); f != nil {
				(*f)()
			}
			_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"the refresh token is revoked"}`)
		case "/api/v3/user":
			ts.users.Add(1)
			_, _ = io.WriteString(w, `{"login":"alice"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.srv.Close)
	return ts
}

// failTheMark makes the credential store refuse its next write while the token
// endpoint answers: the directory is widened, which forgeapi refuses on its
// next access. The returned func narrows it again.
func (ts *refusingTokenServer) failTheMark(t *testing.T, configDir string) (narrow func()) {
	t.Helper()
	dir := filepath.Join(configDir, credentialStoreDir)
	widen := func() {
		if err := os.Chmod(dir, 0o750); err != nil {
			t.Errorf("widen the store: %v", err)
		}
	}
	ts.beforeAnswer.Store(&widen)
	narrow = func() {
		ts.beforeAnswer.Store(nil)
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Errorf("narrow the store: %v", err)
		}
	}
	t.Cleanup(narrow)
	return narrow
}

// storedUsability is the mark the credential file holds for spec's active slot.
func storedUsability(t *testing.T, configDir string, spec *connectionSpec) creds.Usability {
	t.Helper()
	rec, ok, err := openTestStore(t, configDir).Load(slotKey(spec.ID, spec.Slot))
	if err != nil || !ok {
		t.Fatalf("read the credential: %v (present %v)", err, ok)
	}
	return rec.Usability
}

func TestCheck_ATerminalRefreshIsDurableBeforeItShows(t *testing.T) {
	t.Run("the mark and the records write both fail", func(t *testing.T) {
		ts := newRefusingTokenServer(t)
		spec := ts.record()
		cred := rotatingCredential(ts.srv.URL, time.Minute)
		m, _, _ := keeperManager(t, &spec, &cred)
		narrow := ts.failTheMark(t, m.configDir)
		restore := failingWrites(t, m)

		NewKeeper(m).pass(t.Context())
		narrow()
		got := viewFor(t, m, spec.ID)
		if got.Status != ForgeRetrying || got.Error == nil || !strings.Contains(got.Error.Message, "disk refused the write") {
			t.Fatalf("row after a terminal refresh neither file took = %+v, want retrying naming the write failure", got)
		}
		if u := storedUsability(t, m.configDir, &spec); u != creds.UsabilityUnmarked {
			t.Fatalf("Setup: the credential file holds mark %v, want the mark owed and unwritten", u)
		}

		restore()
		NewKeeper(m).pass(t.Context())
		if n := ts.refreshes.Load(); n != 1 {
			t.Errorf("the forge saw %d token requests, want 1: the next pass settles the owed verdict without asking again", n)
		}
		if got := viewFor(t, m, spec.ID); got.Status != ForgeReconnect {
			t.Errorf("row after the next pass = %+v, want reconnect", got)
		}
		if recs := readRecords(t, m.configDir); len(recs) != 1 || recs[0].Standing != standingReconnect {
			t.Errorf("records = %+v, want standing reconnect", recs)
		}
		if u := storedUsability(t, m.configDir, &spec); u != creds.UsabilityReconnectRequired {
			t.Errorf("the credential file holds mark %v, want the owed mark written", u)
		}
		if out := helperAnswer(t, m.configDir, strings.TrimPrefix(ts.srv.URL, "https://")); out != "" {
			t.Errorf("the git helper answered %q, want nothing", out)
		}
		if row := viewFor(t, restarted(t, m), spec.ID); row.Status != ForgeReconnect {
			t.Errorf("a restart loads %+v, want reconnect", row)
		}
	})

	t.Run("only the mark fails", func(t *testing.T) {
		ts := newRefusingTokenServer(t)
		spec := ts.record()
		cred := rotatingCredential(ts.srv.URL, time.Minute)
		m, _, _ := keeperManager(t, &spec, &cred)
		narrow := ts.failTheMark(t, m.configDir)

		NewKeeper(m).pass(t.Context())
		narrow()
		if got := viewFor(t, m, spec.ID); got.Status != ForgeReconnect {
			t.Fatalf("row after a terminal refresh = %+v, want reconnect", got)
		}
		if out := helperAnswer(t, m.configDir, strings.TrimPrefix(ts.srv.URL, "https://")); out != "" {
			t.Errorf("the git helper answered %q, want nothing although the credential file holds no mark", out)
		}
		again := restarted(t, m)
		if row := viewFor(t, again, spec.ID); row.Status != ForgeReconnect {
			t.Errorf("a restart loads %+v, want reconnect from the records file", row)
		}
	})

	t.Run("a Check of a refused row", func(t *testing.T) {
		ts := newRefusingTokenServer(t)
		spec := ts.record()
		cred := rotatingCredential(ts.srv.URL, time.Minute)
		m, _, _ := keeperManager(t, &spec, &cred)
		writeRecords(t, m.configDir, connectionRecord{connectionSpec: spec, Standing: standingRefused})
		m.Load(t.Context())
		restore := failingWrites(t, m)

		if _, err := m.check(t.Context(), spec.ID, checkBudget); err != nil {
			t.Fatalf("Check() = %v", err)
		}
		if got := viewFor(t, m, spec.ID); got.Status != ForgeRefused {
			t.Errorf("row after a Check whose verdict the file refused = %+v, want still refused, as the file says", got)
		}
		if out := helperAnswer(t, m.configDir, strings.TrimPrefix(ts.srv.URL, "https://")); out != "" {
			t.Errorf("the git helper answered %q, want nothing", out)
		}
		restore()
		if _, err := m.check(t.Context(), spec.ID, checkBudget); err != nil {
			t.Fatalf("Check() = %v", err)
		}
		if got := viewFor(t, m, spec.ID); got.Status != ForgeReconnect || ts.refreshes.Load() != 1 {
			t.Errorf("row after a Check with the file writable = %+v after %d token requests, want reconnect after 1",
				got, ts.refreshes.Load())
		}
		if row := viewFor(t, restarted(t, m), spec.ID); row.Status != ForgeReconnect {
			t.Errorf("a restart loads %+v, want reconnect", row)
		}
	})
}
