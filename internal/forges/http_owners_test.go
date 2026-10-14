package forges

// The owners a present cycle reads beside a connection's own pull requests:
// replaced whole by PUT /api/forges/{id}/owners, each one checked before
// anything is stored, and written through the record file's one writer.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

func ownersRoutes(t *testing.T, recs ...connectionSpec) (*Manager, *http.ServeMux) {
	t.Helper()
	m := sourceManager(t, map[string]forgeapi.Core{}, recs...)
	mux := http.NewServeMux()
	NewHTTPHandler(m).RegisterRoutes(mux)
	return m, mux
}

func ownersPath(id string) string {
	return "/api/forges/" + strings.ReplaceAll(id, ":", "%3A") + "/owners"
}

func putOwners(t *testing.T, mux *http.ServeMux, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, ownersPath(id), strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func ownersBodyOf(owners ...string) string {
	data, err := json.Marshal(map[string][]string{"owners": append([]string{}, owners...)})
	if err != nil {
		panic(err)
	}
	return string(data)
}

func answeredOwners(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT owners = %d %s, want 200", rec.Code, rec.Body)
	}
	var got struct {
		Owners *[]string `json:"owners"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Owners == nil {
		t.Fatalf("PUT owners answered %s (%v), want {owners: [...]}", rec.Body, err)
	}
	return *got.Owners
}

func storedRecord(t *testing.T, m *Manager, id string) connectionRecord {
	t.Helper()
	recs := readRecords(t, m.configDir)
	for i := range recs {
		if recs[i].ID == id {
			return recs[i]
		}
	}
	t.Fatalf("no record for %s in %+v", id, recs)
	return connectionRecord{}
}

func rowOwners(t *testing.T, m *Manager, id string) []string {
	t.Helper()
	return viewFor(t, m, id).OwnerScopes
}

// The connection row is where a client reads the stored owners before any cycle
// has run, so the row carries them on the wire and a stored change is announced.
func TestOwnersRoute_TheRowCarriesTheOwnersAndTheChangeIsAnnounced(t *testing.T) {
	gh := githubRecord()
	m, changes := sourceManagerLogged(t, map[string]forgeapi.Core{}, gh)
	mux := http.NewServeMux()
	NewHTTPHandler(m).RegisterRoutes(mux)

	answeredOwners(t, putOwners(t, mux, gh.ID, ownersBodyOf("acme", "beta")))

	if got := changes.all(); !slices.Equal(got, []Change{{ID: gh.ID}}) {
		t.Errorf("a stored owners change notified %+v, want one change of %s", got, gh.ID)
	}
	var list struct {
		Forges []map[string]any `json:"forges"`
	}
	if err := json.Unmarshal(getRoute(t, mux, "/api/forges").Body.Bytes(), &list); err != nil || len(list.Forges) != 1 {
		t.Fatalf("GET /api/forges = %+v (%v), want the one row", list, err)
	}
	if got := list.Forges[0]["owner_scopes"]; !reflect.DeepEqual(got, []any{"acme", "beta"}) {
		t.Errorf("row owner_scopes on the wire = %v, want [acme beta]", got)
	}

	if rec := putOwners(t, mux, gh.ID, ownersBodyOf("a b")); rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT owners [a b] = %d %s, want 400", rec.Code, rec.Body)
	}
	answeredOwners(t, putOwners(t, mux, gh.ID, ownersBodyOf("acme", "beta")))
	if got := changes.all(); len(got) != 1 {
		t.Errorf("a refused write and an unchanged one notified %+v, want nothing more: nothing changed", got)
	}
}

func TestOwnersRoute_AStoredChangeAsksForAnInventoryCycle(t *testing.T) {
	gh := githubRecord()
	m := sourceManager(t, map[string]forgeapi.Core{}, gh)
	src := newHeldSource(false)
	p := NewPRStatusPoller(src, &fakeNotifier{}, (&fakeGate{}).Open)
	mux := inventoryRoutes(m, p)
	p.sweep(t.Context())
	if got := src.reads(); len(got) != 0 {
		t.Fatalf("Setup: a closed-gate sweep with nothing asked read %v, want nothing", got)
	}

	if rec := putOwners(t, mux, gh.ID, ownersBodyOf("a b")); rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT owners [a b] = %d %s, want 400", rec.Code, rec.Body)
	}
	p.sweep(t.Context())
	if got := src.reads(); len(got) != 0 {
		t.Errorf("the sweep after a refused owners write read %v, want nothing: nothing was stored", got)
	}

	answeredOwners(t, putOwners(t, mux, gh.ID, ownersBodyOf("acme")))
	p.sweep(t.Context())
	if got := src.reads(); !slices.Equal(got, []bool{true}) {
		t.Errorf("the closed-gate sweep after a stored owners change read %v, want one present read: "+
			"the inventory's scopes follow the stored owners", got)
	}
}

func TestOwnersRoute_PersistsThroughTheSingleWriter(t *testing.T) {
	gh := githubRecord()
	gh.RotationCursor = "c"
	m, mux := ownersRoutes(t, gh)

	if got := answeredOwners(t, putOwners(t, mux, gh.ID, ownersBodyOf("acme", "beta"))); !slices.Equal(got, []string{"acme", "beta"}) {
		t.Errorf("PUT owners [acme beta] answered %q, want them as stored", got)
	}
	stored := storedRecord(t, m, gh.ID)
	if !slices.Equal(stored.OwnerScopes, []string{"acme", "beta"}) || stored.RotationCursor != "c" || stored.SignIn != gh.SignIn {
		t.Errorf("stored record = %+v, want owner_scopes [acme beta] with the cursor and sign-in kept", stored)
	}
	if got := rowOwners(t, m, gh.ID); !slices.Equal(got, []string{"acme", "beta"}) {
		t.Errorf("the row the next cycle reads names owners %q, want [acme beta] without waiting for the list TTL", got)
	}
}

// A sign-in replaces the record from its body, and the owners are the user's
// choice for that connection, so the new record carries them over.
func TestOwnersRoute_ASignInKeepsTheOwners(t *testing.T) {
	h := newConnectHarness(t, aliceWire())
	h.signedIn(t, KindGitHub, "", "ghp_secret", "")
	answeredOwners(t, h.do(t, http.MethodPut, ownersPath("github:github.com"), ownersBodyOf("acme")))

	h.signedIn(t, KindGitHub, "", "ghp_rotated", "")

	if got := storedRecord(t, h.m, "github:github.com").OwnerScopes; !slices.Equal(got, []string{"acme"}) {
		t.Errorf("owner scopes after a reconnect = %q, want [acme] kept", got)
	}
}

func TestOwnersRoute_MalformedOwnerIs400AndStoresNothing(t *testing.T) {
	gh, gitea := githubRecord(), localGiteaRecord()
	gh.OwnerScopes, gitea.OwnerScopes = []string{"keep"}, []string{"keep"}
	m, mux := ownersRoutes(t, gh, gitea)
	assertKept := func(t *testing.T, id string) {
		t.Helper()
		if got := storedRecord(t, m, id).OwnerScopes; !slices.Equal(got, []string{"keep"}) {
			t.Errorf("stored owner scopes of %s after a refused PUT = %q, want [keep]", id, got)
		}
		if got := rowOwners(t, m, id); !slices.Equal(got, []string{"keep"}) {
			t.Errorf("row owners of %s after a refused PUT = %q, want [keep]", id, got)
		}
	}

	owners := []struct {
		name, id string
		owners   []string
		index    int
	}{
		{"Empty", gh.ID, []string{""}, 0},
		{"Space", gh.ID, []string{"acme", "a b"}, 1},
		{"Colon", gh.ID, []string{"acme:x"}, 0},
		{"DotDotSegment", gh.ID, []string{"ok", "fine", "../x"}, 2},
		{"EmptySegment", gh.ID, []string{"a//b"}, 0},
		{"OverTheFormBound", gh.ID, []string{strings.Repeat("a", 256)}, 0},
		{"PathOnGitHub", gh.ID, []string{"group/sub"}, 0},
		{"PathOnGitea", gitea.ID, []string{"org/sub"}, 0},
	}
	for _, tc := range owners {
		t.Run(tc.name, func(t *testing.T) {
			rec := putOwners(t, mux, tc.id, ownersBodyOf(tc.owners...))

			body := decodeBody(t, rec)
			if rec.Code != http.StatusBadRequest || body["code"] != forgeapi.CodeListOwnerInvalid || body["kind"] != "unknown" {
				t.Errorf("PUT owners %q on %s = %d %v, want 400 coded %q, kind unknown",
					tc.owners, tc.id, rec.Code, body, forgeapi.CodeListOwnerInvalid)
			}
			if msg, _ := body["error"].(string); !strings.Contains(msg, fmt.Sprintf("owners[%d]", tc.index)) {
				t.Errorf("refusal message %q, want it to name owners[%d]", msg, tc.index)
			}
			assertKept(t, tc.id)
		})
	}

	bodies := []struct{ name, body string }{
		{"NoOwners", `{}`},
		{"NullOwners", `{"owners":null}`},
		{"NotAList", `{"owners":"acme"}`},
		{"NotStrings", `{"owners":[1]}`},
		{"NotJSON", `owners`},
	}
	for _, tc := range bodies {
		t.Run(tc.name, func(t *testing.T) {
			rec := putOwners(t, mux, gh.ID, tc.body)
			if body := decodeBody(t, rec); rec.Code != http.StatusBadRequest || body["code"] != "owners_invalid" {
				t.Errorf("PUT owners with body %s = %d %v, want 400 coded owners_invalid", tc.body, rec.Code, body)
			}
			assertKept(t, gh.ID)
		})
	}

	t.Run("OnlyPUT", func(t *testing.T) {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
			req := httptest.NewRequestWithContext(t.Context(), method, ownersPath(gh.ID), strings.NewReader(ownersBodyOf("acme")))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s owners = %d %s, want 405", method, rec.Code, rec.Body)
			}
		}
		assertKept(t, gh.ID)
	})

	t.Run("UnknownConnection", func(t *testing.T) {
		if rec := putOwners(t, mux, "github:example.com", ownersBodyOf("acme")); rec.Code != http.StatusNotFound {
			t.Errorf("PUT owners on a connection with no row = %d %s, want 404", rec.Code, rec.Body)
		}
	})

	t.Run("UnusablePersistence", func(t *testing.T) {
		restore := widenStore(t, m)
		rec := putOwners(t, mux, gh.ID, ownersBodyOf("acme"))
		restore()
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "credential store") {
			t.Errorf("PUT owners over an unreadable store = %d %s, want 503 naming the store", rec.Code, rec.Body)
		}
		if got := storedRecord(t, m, gh.ID).OwnerScopes; !slices.Equal(got, []string{"keep"}) {
			t.Errorf("stored owner scopes after a refused PUT = %q, want [keep]", got)
		}
	})
}

func TestOwnersRoute_DeduplicatesAndBounds(t *testing.T) {
	gh, gl := githubRecord(), gitlabRecord()
	m, mux := ownersRoutes(t, gh, gl)
	numbered := func(n int) []string {
		out := make([]string, 0, n)
		for i := range n {
			out = append(out, fmt.Sprintf("org-%02d", i))
		}
		return out
	}

	t.Run("FirstSpellingWins", func(t *testing.T) {
		got := answeredOwners(t, putOwners(t, mux, gh.ID, ownersBodyOf("Acme", "acme", "beta", "ACME")))
		if !slices.Equal(got, []string{"Acme", "beta"}) {
			t.Errorf("PUT owners [Acme acme beta ACME] answered %q, want [Acme beta]", got)
		}
		if stored := storedRecord(t, m, gh.ID).OwnerScopes; !slices.Equal(stored, got) {
			t.Errorf("stored owner scopes = %q, want the answer %q", stored, got)
		}
	})

	t.Run("TheLoginIsTheOwnerScopeOnGitHub", func(t *testing.T) {
		got := answeredOwners(t, putOwners(t, mux, gh.ID, ownersBodyOf("Bob", "acme", "bob")))
		if !slices.Equal(got, []string{"acme"}) {
			t.Errorf("PUT owners [Bob acme bob] as bob on GitHub answered %q, want [acme]: the owner scope reads bob already", got)
		}
	})

	t.Run("TheLoginMatchesInAnyCase", func(t *testing.T) {
		gitea := localGiteaRecord()
		gm, gmux := ownersRoutes(t, gitea)
		addConnections(t, gm, "Carol", gitea)
		got := answeredOwners(t, putOwners(t, gmux, gitea.ID, ownersBodyOf("carol", "acme")))
		if !slices.Equal(got, []string{"acme"}) {
			t.Errorf("PUT owners [carol acme] as Carol on Gitea answered %q, want [acme]", got)
		}
	})

	t.Run("GitLabKeepsTheLoginAndAcceptsAGroupPath", func(t *testing.T) {
		got := answeredOwners(t, putOwners(t, mux, gl.ID, ownersBodyOf("group/sub", "Group/Sub", "bob")))
		if !slices.Equal(got, []string{"group/sub", "bob"}) {
			t.Errorf("PUT owners [group/sub Group/Sub bob] as bob on GitLab answered %q, want [group/sub bob]", got)
		}
	})

	t.Run("TwentyIsTheBound", func(t *testing.T) {
		twenty := numbered(20)
		if got := answeredOwners(t, putOwners(t, mux, gh.ID, ownersBodyOf(twenty...))); !slices.Equal(got, twenty) {
			t.Errorf("PUT twenty owners answered %q, want all twenty", got)
		}
		rec := putOwners(t, mux, gh.ID, ownersBodyOf(numbered(21)...))
		if body := decodeBody(t, rec); rec.Code != http.StatusBadRequest || body["code"] != "owners_too_many" {
			t.Errorf("PUT twenty-one owners = %d %v, want 400 coded owners_too_many", rec.Code, body)
		}
		if stored := storedRecord(t, m, gh.ID).OwnerScopes; !slices.Equal(stored, twenty) {
			t.Errorf("stored owner scopes after a refused PUT = %q, want the twenty kept", stored)
		}
		withRepeats := append(numbered(20), "ORG-00", "bob")
		if got := answeredOwners(t, putOwners(t, mux, gh.ID, ownersBodyOf(withRepeats...))); !slices.Equal(got, twenty) {
			t.Errorf("PUT twenty owners plus a repeat and the login answered %q, want the twenty: the bound counts distinct scopes", got)
		}
	})

	t.Run("EmptyClears", func(t *testing.T) {
		rec := putOwners(t, mux, gh.ID, `{"owners":[]}`)
		if got := answeredOwners(t, rec); len(got) != 0 || !strings.Contains(rec.Body.String(), `"owners":[]`) {
			t.Errorf("PUT owners [] answered %s, want {owners: []}", rec.Body)
		}
		if stored := storedRecord(t, m, gh.ID).OwnerScopes; len(stored) != 0 {
			t.Errorf("stored owner scopes after PUT [] = %q, want none", stored)
		}
		if got := rowOwners(t, m, gh.ID); len(got) != 0 {
			t.Errorf("row owners after PUT [] = %q, want none", got)
		}
	})
}
