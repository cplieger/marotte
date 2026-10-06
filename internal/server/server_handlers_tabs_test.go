package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
)

// newTabsServer is a server wired to a real tab store over a temp dir: the one-critical-section
// property is the store's own.
func newTabsServer(t *testing.T) (*Server, *tabs.Store) {
	t.Helper()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("open tab store: %v", err)
	}
	return &Server{tabs: st}, st
}

// getTabs drives the handler and decodes its body.
func getTabs(t *testing.T, s *Server) (marotte.TabList, int) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleTabs(rec, httptest.NewRequest(http.MethodGet, "/api/tabs", http.NoBody))
	var out marotte.TabList
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode %s: %v", rec.Body.String(), err)
		}
	}
	return out, rec.Code
}

func TestTabs_GetReturnsTheSetAndItsVersion(t *testing.T) {
	s, st := newTabsServer(t)
	first, _, _, err := st.Open(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-a"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, _, _, err := st.Open(t.Context(), marotte.OpenTab{Kind: marotte.TabKindSettings}); err != nil {
		t.Fatalf("open: %v", err)
	}

	got, code := getTabs(t, s)

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(got.Tabs) != 2 {
		t.Fatalf("tabs = %+v, want 2", got.Tabs)
	}
	if got.Tabs[0].ID != first.ID {
		t.Errorf("first tab = %q, want the one opened first %q: the slice IS the order", got.Tabs[0].ID, first.ID)
	}
	if got.Version != 2 {
		t.Errorf("version = %d, want 2: two mutations of an empty collection", got.Version)
	}
}

// TestTabs_AnEmptySetIsAnArray pins `[]` rather than `null`.
func TestTabs_AnEmptySetIsAnArray(t *testing.T) {
	s, _ := newTabsServer(t)
	rec := httptest.NewRecorder()

	s.handleTabs(rec, httptest.NewRequest(http.MethodGet, "/api/tabs", http.NoBody))

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if string(raw["tabs"]) != "[]" {
		t.Errorf("tabs = %s, want []", raw["tabs"])
	}
	if string(raw["version"]) != "0" {
		t.Errorf("version = %s, want 0", raw["version"])
	}
}

// TestTabs_AnUnwiredStoreAnswersTheEmptyCollection pins that a client that cannot read the
// arrangement still boots.
func TestTabs_AnUnwiredStoreAnswersTheEmptyCollection(t *testing.T) {
	s := &Server{}

	got, code := getTabs(t, s)

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 rather than 404", code)
	}
	if len(got.Tabs) != 0 || got.Version != 0 {
		t.Errorf("body = %+v, want the empty collection at version 0", got)
	}
}

func TestTabs_RejectsEveryMethodButGET(t *testing.T) {
	s, _ := newTabsServer(t)
	// Mutations ride POST /api/command, so every write verb is a 405.
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			rec := httptest.NewRecorder()

			s.handleTabs(rec, httptest.NewRequest(method, "/api/tabs", http.NoBody))

			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want 405", rec.Code)
			}
		})
	}
}

// movingTabs is a tab set that ADVANCES on every List (call n returns n tabs at version n),
// making a two-call handler's mismatch deterministic.
type movingTabs struct {
	calls uint64
}

func (m *movingTabs) List() ([]marotte.TabSubject, uint64) {
	m.calls++
	out := make([]marotte.TabSubject, 0, m.calls)
	for i := range m.calls {
		out = append(out, marotte.TabSubject{ID: "t" + strconv.FormatUint(i, 10), Kind: marotte.TabKindSettings})
	}
	return out, m.calls
}

// TestTabs_TheSetAndTheVersionComeFromONECall pins the paired read deterministically.
func TestTabs_TheSetAndTheVersionComeFromONECall(t *testing.T) {
	moving := &movingTabs{}
	s := &Server{tabs: moving}

	got, code := getTabs(t, s)

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if moving.calls != 1 {
		t.Errorf("the handler called List %d times, want exactly 1: the set and the version are ONE fact", moving.calls)
	}
	if uint64(len(got.Tabs)) != got.Version {
		t.Errorf("answered %d tabs at version %d; this store answers n tabs at version n, so a mismatch "+
			"means the two came from different calls", len(got.Tabs), got.Version)
	}
}

// TestTabs_AReadRacingAMutationPairsTheVersionWithItsOwnSet samples the same contract against
// the REAL store and a concurrent writer: version N always describes N tabs.
func TestTabs_AReadRacingAMutationPairsTheVersionWithItsOwnSet(t *testing.T) {
	s, st := newTabsServer(t)
	const opens = 60

	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range opens {
			if _, _, _, err := st.Open(t.Context(), marotte.OpenTab{
				Kind: marotte.TabKindEditor,
				Ref:  "/workspace/f" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".go",
			}); err != nil {
				return // the store's own limit; the reads are what this asserts
			}
		}
	})

	for range 400 {
		got, code := getTabs(t, s)
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if uint64(len(got.Tabs)) != got.Version {
			t.Fatalf("read %d tabs at version %d; each mutation opens exactly one tab, so a set and a "+
				"version that disagree means they were captured separately", len(got.Tabs), got.Version)
		}
	}
	wg.Wait()

	// Once more after the writer finishes, so the case cannot pass on the empty collection alone.
	got, _ := getTabs(t, s)
	if got.Version == 0 {
		t.Fatal("the writer produced no mutation, so nothing was actually raced")
	}
}
