package forges

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// signOutHarness is a harness signed in to github.com whose store and notify
// log into one sequence, with a cleartext git-credentials file holding a line
// for github.com beside another host's.
func signOutHarness(t *testing.T) (h *connectHarness, s *storeLog, id, creds string) {
	t.Helper()
	h = newConnectHarness(t, aliceWire())
	id = h.signedIn(t, KindGitHub, "", "ghp_secret", "")
	s = logStore(t, h.m)
	h.m.notify = func(context.Context, Change) { s.note("notify") }
	creds = filepath.Join(h.home, ".git-credentials")
	writeFixture(t, creds, "https://alice:old@github.com\nhttps://bob:tok@other.example/\n")
	return h, s, id, creds
}

func TestSignOut_ACleanupFailureFinishesItsEffectsAndRetries(t *testing.T) {
	t.Run("a failed delete", func(t *testing.T) {
		h, s, id, credFile := signOutHarness(t)
		k0, k1 := slotKey(id, 0), slotKey(id, 1)
		failed := false
		s.onDelete = func(key string) error {
			if key == k0 && !failed {
				failed = true
				return errors.New("the disk refused the delete")
			}
			return nil
		}

		rec := h.do(t, http.MethodDelete, "/api/forges/"+id, "")
		if body := decodeBody(t, rec); rec.Code != http.StatusServiceUnavailable || body["code"] != codeCleanupPending {
			t.Fatalf("DELETE with a failing delete = %d %v, want 503 %s", rec.Code, body, codeCleanupPending)
		}
		if got, want := s.log(), []string{"notify", "delete " + k0, "delete " + k1}; !slices.Equal(got, want) {
			t.Errorf("the sign-out did %q, want %q: notified once, then both deletes attempted", got, want)
		}
		if forges := h.m.List(); len(forges) != 0 {
			t.Errorf("rows after the sign-out = %+v, want none", forges)
		}
		if out := helperAnswer(t, h.cfgDir, "github.com"); out != "" {
			t.Errorf("the git helper answered %q, want nothing for a signed-out account", out)
		}
		writeFixture(t, credFile, "https://alice:kept@github.com\n")

		rec = h.do(t, http.MethodDelete, "/api/forges/"+id, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("the repeated DELETE = %d %s, want 200", rec.Code, rec.Body)
		}
		if keys := h.storedKeys(t); len(keys) != 0 {
			t.Errorf("store keys after the repeat = %q, want the residual one gone", keys)
		}
		if got := s.log()[3:]; !slices.Equal(got, []string{"delete " + k0, "delete " + k1}) {
			t.Errorf("the repeat did %q, want both deletes and no notify", got)
		}
		if data := fileBytes(t, credFile); !strings.Contains(data, "alice:kept@github.com") {
			t.Errorf("~/.git-credentials after a sign-out of no row = %q, want nothing scrubbed", data)
		}
	})

	t.Run("the request ends right after the commit", func(t *testing.T) {
		h, s, id, credFile := signOutHarness(t)
		ctx, cancel := context.WithCancel(t.Context())
		real := h.m.records.writeFile
		h.m.records.writeFile = func(wctx context.Context, path string, data []byte) error {
			err := real(wctx, path, data)
			cancel()
			return err
		}
		rec := httptest.NewRecorder()
		h.mux.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodDelete, "/api/forges/"+id, nil))

		if got := s.log(); !slices.Equal(got, []string{"notify", "delete " + slotKey(id, 0), "delete " + slotKey(id, 1)}) {
			t.Errorf("the sign-out did %q, want notify then both deletes although the request ended", got)
		}
		if data := fileBytes(t, credFile); strings.Contains(data, "github.com") {
			t.Errorf("~/.git-credentials = %q, want github.com scrubbed", data)
		}
		if got := helperValuesFor(t, githubOrigin); len(got) != 0 {
			t.Errorf("credential.%s.helper = %q, want the registration removed", githubOrigin, got)
		}
	})

	t.Run("a delete that outlasts the deadline", func(t *testing.T) {
		h, s, id, credFile := signOutHarness(t)
		h.m.deadlines = mutationDeadlines{total: 300 * time.Millisecond, reserve: 100 * time.Millisecond}
		s.onDelete = func(key string) error {
			if key == slotKey(id, 0) {
				time.Sleep(400 * time.Millisecond) // a stalled disk, the fixture
			}
			return nil
		}

		if rec := h.do(t, http.MethodDelete, "/api/forges/"+id, ""); rec.Code != http.StatusOK {
			t.Fatalf("DELETE = %d %s, want 200", rec.Code, rec.Body)
		}
		if got := s.log(); len(got) == 0 || got[0] != "notify" {
			t.Errorf("the sign-out did %q, want notify before the slow delete", got)
		}
		if data := fileBytes(t, credFile); strings.Contains(data, "github.com") || !strings.Contains(data, "other.example") {
			t.Errorf("~/.git-credentials = %q, want github.com scrubbed and the other kept", data)
		}
		if got := helperValuesFor(t, githubOrigin); len(got) == 0 {
			t.Fatalf("Setup: credential.%s.helper = %q, want the registration the expired reconcile could not remove", githubOrigin, got)
		}
		NewKeeper(h.m).pass(t.Context())
		if got := helperValuesFor(t, githubOrigin); len(got) != 0 {
			t.Errorf("credential.%s.helper after a pass = %q, want it removed", githubOrigin, got)
		}
	})
}
