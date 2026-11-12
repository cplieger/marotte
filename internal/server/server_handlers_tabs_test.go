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

// getRawTabs answers GET /api/tabs as its top-level keys with their raw JSON values.
func getRawTabs(t *testing.T, s *Server) map[string]string {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleTabs(rec, httptest.NewRequest(http.MethodGet, "/api/tabs", http.NoBody))
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		out[k] = string(v)
	}
	return out
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
	if string(raw["recent"]) != "[]" {
		t.Errorf("recent = %s, want []", raw["recent"])
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
	raw := getRawTabs(t, s)
	if raw["tabs"] != "[]" || raw["recent"] != "[]" {
		t.Errorf("tabs = %s, recent = %s, want [] and []: the decoder reads arrays", raw["tabs"], raw["recent"])
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

// movingTabs is a tab set that ADVANCES on every Snapshot (call n returns n tabs and n recently
// closed entries at version n), making a two-call handler's mismatch deterministic.
type movingTabs struct {
	calls uint64
}

func (m *movingTabs) Snapshot() ([]marotte.TabSubject, []marotte.RecentClose, uint64) {
	m.calls++
	open := make([]marotte.TabSubject, 0, m.calls)
	recent := make([]marotte.RecentClose, 0, m.calls)
	for i := range m.calls {
		n := strconv.FormatUint(i, 10)
		open = append(open, marotte.TabSubject{ID: "t" + n, Kind: marotte.TabKindSettings})
		recent = append(recent, marotte.RecentClose{Ref: "c-" + n, ClosedAt: int64(i) + 1})
	}
	return open, recent, m.calls
}

// TestTabs_TheSetTheRecentListAndTheVersionComeFromONECall pins the paired read deterministically.
func TestTabs_TheSetTheRecentListAndTheVersionComeFromONECall(t *testing.T) {
	moving := &movingTabs{}
	s := &Server{tabs: moving}

	got, code := getTabs(t, s)

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if moving.calls != 1 {
		t.Errorf("the handler called Snapshot %d times, want exactly 1: the set, the list and the version are ONE fact", moving.calls)
	}
	if uint64(len(got.Tabs)) != got.Version {
		t.Errorf("answered %d tabs at version %d; this store answers n tabs at version n, so a mismatch "+
			"means the two came from different calls", len(got.Tabs), got.Version)
	}
	if uint64(len(got.Recent)) != got.Version {
		t.Errorf("answered %d recently closed entries at version %d; this store answers n at version n, so a "+
			"mismatch means the list and the version came from different calls", len(got.Recent), got.Version)
	}
}

// nilTabs is a tab set whose Snapshot answers nil slices, as an empty store's may.
type nilTabs struct{}

func (nilTabs) Snapshot() ([]marotte.TabSubject, []marotte.RecentClose, uint64) { return nil, nil, 7 }

// TestTabs_ANilRecentListIsAnArray pins `[]` rather than `null` when Snapshot answers nil
// slices: the generated decoder reads an array on the boot path.
func TestTabs_ANilRecentListIsAnArray(t *testing.T) {
	s := &Server{tabs: nilTabs{}}

	raw := getRawTabs(t, s)

	if raw["tabs"] != "[]" || raw["recent"] != "[]" {
		t.Errorf("tabs = %s, recent = %s, want [] and []", raw["tabs"], raw["recent"])
	}
	if raw["version"] != "7" {
		t.Errorf("version = %s, want 7 from the snapshot", raw["version"])
	}
}

// TestTabs_GetReturnsTheRecentlyClosedList closes a named, pinned chat in the real store and
// reads the row back through GET /api/tabs.
func TestTabs_GetReturnsTheRecentlyClosedList(t *testing.T) {
	s, st := newTabsServer(t)
	tab, _, _, err := st.Open(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-a"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := st.SetPinned(t.Context(), tab.ID, true); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if _, _, _, err := st.Close(t.Context(), tab.ID, nil, map[string]tabs.RecentFacts{"c-a": {Name: "Alpha"}}); err != nil {
		t.Fatalf("close: %v", err)
	}

	got, code := getTabs(t, s)

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(got.Tabs) != 0 {
		t.Errorf("tabs = %+v, want none after the close", got.Tabs)
	}
	if len(got.Recent) != 1 {
		t.Fatalf("recent = %+v, want the closed chat", got.Recent)
	}
	if r := got.Recent[0]; r.Ref != "c-a" || r.Name != "Alpha" || !r.Pinned || r.ClosedAt <= 0 {
		t.Errorf("recent[0] = %+v, want c-a named Alpha, pinned, with its close time", r)
	}
	if got.Version != 3 {
		t.Errorf("version = %d, want 3: open, pin and close", got.Version)
	}
}

// TestTabs_AReadRacingACloseNeverPairsTheOldListWithTheNewVersion runs open-then-close cycles of
// one chat against the real store: an odd version is the open tab with an empty list, an even
// one past 0 is no tab with the chat listed. Any other pairing is a torn read.
func TestTabs_AReadRacingACloseNeverPairsTheOldListWithTheNewVersion(t *testing.T) {
	s, st := newTabsServer(t)
	const cycles = 60

	var wg sync.WaitGroup
	wg.Go(func() {
		for range cycles {
			tab, _, _, err := st.Open(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-a"})
			if err != nil {
				return
			}
			if _, _, _, err := st.Close(t.Context(), tab.ID, nil, nil); err != nil {
				return
			}
		}
	})

	for range 400 {
		got, code := getTabs(t, s)
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		wantTabs, wantRecent := 0, 0
		switch {
		case got.Version == 0:
		case got.Version%2 == 1:
			wantTabs = 1
		default:
			wantRecent = 1
		}
		if len(got.Tabs) != wantTabs || len(got.Recent) != wantRecent {
			t.Fatalf("read %d tabs and %d entries at version %d, want %d and %d: the three were captured separately",
				len(got.Tabs), len(got.Recent), got.Version, wantTabs, wantRecent)
		}
	}
	wg.Wait()

	got, _ := getTabs(t, s)
	if got.Version != 2*cycles {
		t.Fatalf("version = %d after the writer, want %d: the cycles did not all land", got.Version, 2*cycles)
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
