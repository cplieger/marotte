package forges

// The inventory routes: a read answers from memory, a connection never filled
// answers loading and asks for a cycle, and a refresh names the cycle that
// serves it, joining one in flight.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
)

const (
	inventoryPath = "/api/forges/inventory"
	refreshPath   = "/api/forges/inventory/refresh"
	watchPath     = "/api/forges/inventory/watch"
)

// inventoryRoutes is the forge routes with p's inventory behind them.
func inventoryRoutes(m *Manager, p *PRStatusPoller) *http.ServeMux {
	h := NewHTTPHandler(m, nil)
	h.SetPoller(p)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux
}

func serve(t *testing.T, mux http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, http.NoBody)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// readInventory is the entries GET /api/forges/inventory answers.
func readInventory(t *testing.T, mux http.Handler) []InventoryEntry {
	t.Helper()
	return readInventoryList(t, mux).Entries
}

func readInventoryList(t *testing.T, mux http.Handler) InventoryList {
	t.Helper()
	rec := serve(t, mux, http.MethodGet, inventoryPath)
	var body InventoryList
	if err := json.Unmarshal(rec.Body.Bytes(), &body); rec.Code != http.StatusOK || err != nil {
		t.Fatalf("GET %s = %d %s (decode: %v), want 200 and the entries", inventoryPath, rec.Code, rec.Body, err)
	}
	return body
}

// postWatch is POST /api/forges/inventory/watch with body, carrying tag as
// SSE-Client unless tag is empty.
func postWatch(t *testing.T, mux http.Handler, tag, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, watchPath, strings.NewReader(body))
	if tag != "" {
		req.Header.Set("SSE-Client", tag)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// pressRefresh is the cycle id POST /api/forges/inventory/refresh answers. It
// reports with Errorf, so it may run off the test's goroutine.
func pressRefresh(t *testing.T, mux http.Handler) string {
	t.Helper()
	rec := serve(t, mux, http.MethodPost, refreshPath)
	var body InventoryRefresh
	if err := json.Unmarshal(rec.Body.Bytes(), &body); rec.Code != http.StatusAccepted || err != nil {
		t.Errorf("POST %s = %d %s (decode: %v), want 202 and a cycle id", refreshPath, rec.Code, rec.Body, err)
	}
	return body.CycleID
}

// heldSource answers testConn's authored read, empty, holding every call until
// release is closed, and records whether each call was told a client is
// present.
type heldSource struct {
	release  chan struct{}
	presents []bool
	mu       sync.Mutex
}

func newHeldSource(held bool) *heldSource {
	s := &heldSource{release: make(chan struct{})}
	if !held {
		close(s.release)
	}
	return s
}

func (s *heldSource) Read(_ context.Context, present bool, _ func(PRConnection, Scope) forgeapi.Cursor) []ConnectionRead {
	s.mu.Lock()
	s.presents = append(s.presents, present)
	s.mu.Unlock()
	<-s.release
	return []ConnectionRead{authoredRead(testConn, ScopePage{}, nil)}
}

func (s *heldSource) reads() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.presents)
}

// runLoop starts p's loop and answers the function that stops it and waits for
// it to return. The intervals are an hour, so inside a bubble only a request
// for a cycle can start one.
func runLoop(t *testing.T, p *PRStatusPoller) (stop func()) {
	t.Helper()
	p.tick, p.discovery = time.Hour, time.Hour
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()
	return func() {
		cancel()
		<-done
	}
}

// cycleOf is the cycle id of id's entry, "" when the inventory holds none.
func cycleOf(p *PRStatusPoller, id string) string {
	entries := entriesOf(p)
	for i := range entries {
		if entries[i].ForgeID == id {
			return entries[i].CycleID
		}
	}
	return ""
}

func TestInventoryRoute_AnswersFromMemoryWithNoForgeRequest(t *testing.T) {
	core := newScopeCore()
	core.pages[""] = page(prIn(forgeapi.FamilyGitHub, "bob/app", 1, forgeapi.CheckPending))
	rec := githubRecord()
	p, g, _, m := inventoryPoller(t, map[string]forgeapi.Core{rec.ID: core}, nil, rec)
	p.sweep(t.Context())
	whoamis, lists := core.whoamis, len(core.asked)
	mux := inventoryRoutes(m, p)

	got := readInventory(t, mux)

	if want := entriesOf(p); !reflect.DeepEqual(got, want) {
		t.Errorf("GET %s entries = %+v, want the poller's %+v", inventoryPath, got, want)
	}
	if core.whoamis != whoamis || len(core.asked) != lists {
		t.Errorf("the read made %d account reads and %d lists, want none",
			core.whoamis-whoamis, len(core.asked)-lists)
	}
	// Every connected connection is filled, so the read asked for no cycle.
	g.present = false
	p.sweep(t.Context())
	if core.whoamis != whoamis || len(core.asked) != lists {
		t.Errorf("a closed-gate sweep after reading a filled inventory made %d account reads and %d lists, "+
			"want none: the read asked for a cycle", core.whoamis-whoamis, len(core.asked)-lists)
	}
}

func TestInventoryRoute_NeverFilledIsLoadingAndAsksForACycle(t *testing.T) {
	core := newScopeCore()
	gh, gl := githubRecord(), gitlabRecord()
	p, g, _, m := inventoryPoller(t, map[string]forgeapi.Core{gh.ID: core}, nil, gh)
	// gl has no stored credential, so it is not connected and no cycle fills it.
	saveRecords(t, m.conns, gh, gl)
	m.Invalidate()
	g.present = false
	mux := inventoryRoutes(m, p)

	got := readInventory(t, mux)

	want := []InventoryEntry{{
		ForgeID: gh.ID, State: "loading", CycleID: "0", Credential: "unknown",
		Scopes: []InventoryScope{}, Clones: []CloneRepo{},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GET %s with nothing filled = %+v, want %+v", inventoryPath, got, want)
	}
	if core.whoamis != 0 || len(core.asked) != 0 {
		t.Errorf("the read made %d account reads and %d lists, want none", core.whoamis, len(core.asked))
	}
	p.sweep(t.Context())
	if e := entryFor(t, p, gh.ID); core.whoamis != 1 || e.State != inventoryReady || e.CycleID != "1" {
		t.Errorf("the sweep after the read, gate closed: %d account reads, entry %q from cycle %q; "+
			"want 1 and a ready entry from cycle 1: a read of a connection never filled asks for a cycle",
			core.whoamis, e.State, e.CycleID)
	}
}

func TestInventoryRoute_ANeverFilledReadDuringACycleAsksForTheNext(t *testing.T) {
	m := sourceManager(t, nil, githubRecord())
	synctest.Test(t, func(t *testing.T) {
		src := newHeldSource(true)
		p := NewPRStatusPoller(src, &fakeNotifier{}, (&fakeGate{}).Open)
		mux := inventoryRoutes(m, p)
		stop := runLoop(t, p)
		defer stop()
		if id := pressRefresh(t, mux); id != "1" {
			t.Fatalf("Setup: the first press answered cycle %q, want 1", id)
		}
		synctest.Wait()

		// Cycle 1 started before the read, so it may not include the connection.
		if got := readInventory(t, mux); len(got) != 1 || got[0].State != "loading" {
			t.Errorf("GET %s during the first cycle = %+v, want the connection loading", inventoryPath, got)
		}
		close(src.release)
		synctest.Wait()

		if got := src.reads(); !slices.Equal(got, []bool{true, true}) || cycleOf(p, testConn.ID) != "2" {
			t.Errorf("reads %v, entry from cycle %q; want two present reads and the entry from cycle 2: "+
				"a loading read during a cycle asks for the one after it", got, cycleOf(p, testConn.ID))
		}
	})
}

func TestInventoryRoute_StampsEachEntryWithItsVersion(t *testing.T) {
	gh, gl := githubRecord(), gitlabRecord()
	p, _, _, _, m := pushPoller(t, map[string]forgeapi.Core{gh.ID: newScopeCore()}, nil, gh)
	p.sweep(t.Context())
	seedStoreRecord(t, m.configDir, gl.ID, "bob")
	saveRecords(t, m.conns, gh, gl)
	m.Invalidate()

	rec := serve(t, inventoryRoutes(m, p), http.MethodGet, inventoryPath)
	var body InventoryList
	if err := json.Unmarshal(rec.Body.Bytes(), &body); rec.Code != http.StatusOK || err != nil {
		t.Fatalf("GET %s = %d %s (decode: %v), want 200", inventoryPath, rec.Code, rec.Body, err)
	}

	stamp := func(id, version string) *marotte.SubjectStamp {
		return &marotte.SubjectStamp{Kind: string(subject.KindForgeInventory), Ref: id, Version: version, Epoch: "epoch-1"}
	}
	want := []*marotte.SubjectStamp{stamp(gh.ID, "1"), stamp(gl.ID, subject.Unminted)}
	if !reflect.DeepEqual(body.Subject, want) || len(body.Entries) != 2 || body.Entries[1].State != inventoryLoading {
		t.Errorf("GET %s stamps %s over %d entries, want one per entry in their order, %s, "+
			"the loading one at the version the cycle that fills it moves", inventoryPath, stampsOf(body.Subject), len(body.Entries),
			stampsOf(want))
	}
}

func stampsOf(stamps []*marotte.SubjectStamp) string {
	out := make([]string, 0, len(stamps))
	for _, s := range stamps {
		if s == nil {
			out = append(out, "nil")
			continue
		}
		out = append(out, fmt.Sprintf("%+v", *s))
	}
	return "[" + strings.Join(out, " ") + "]"
}

func TestInventoryRoute_LeavesOutAConnectionNoLongerConnected(t *testing.T) {
	rec := githubRecord()
	p, _, _, m := inventoryPoller(t, map[string]forgeapi.Core{rec.ID: newScopeCore()}, nil, rec)
	p.sweep(t.Context())
	if err := m.store.Delete(rec.ID); err != nil {
		t.Fatalf("Setup: delete the credential: %v", err)
	}
	m.Invalidate()

	if got := readInventory(t, inventoryRoutes(m, p)); len(got) != 0 {
		t.Errorf("GET %s after the connection lost its credential = %+v, want no entry before the next sweep drops it",
			inventoryPath, got)
	}
}

func TestInventoryRoutes_RefuseOtherMethodsAndAnUnwiredPoller(t *testing.T) {
	mux := inventoryRoutes(sourceManager(t, nil), NewPRStatusPoller(newHeldSource(false), &fakeNotifier{}, (&fakeGate{}).Open))
	if rec := serve(t, mux, http.MethodPost, inventoryPath); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST %s = %d, want 405", inventoryPath, rec.Code)
	}
	if rec := serve(t, mux, http.MethodGet, refreshPath); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET %s = %d, want 405", refreshPath, rec.Code)
	}
	if rec := serve(t, mux, http.MethodGet, watchPath); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET %s = %d, want 405", watchPath, rec.Code)
	}
	h := NewHTTPHandler(nil, nil)
	unwired := http.NewServeMux()
	h.RegisterRoutes(unwired)
	for _, req := range []struct{ method, path string }{
		{http.MethodGet, inventoryPath}, {http.MethodPost, refreshPath}, {http.MethodPost, watchPath},
	} {
		rec := serve(t, unwired, req.method, req.path)
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "inventory") {
			t.Errorf("%s %s with no poller = %d %s, want 503 naming the inventory", req.method, req.path, rec.Code, rec.Body)
		}
	}
}

func TestWatchRoute_BadTagIs400(t *testing.T) {
	bad := []string{"", "bad tag", strings.Repeat("a", 65), "tab/a", "tab\u00e9"}
	pres := newLivePresence(bad...)
	p := viewerPoller(newHeldSource(false), &fakeGate{}, pres)
	mux := inventoryRoutes(sourceManager(t, nil), p)

	for _, tag := range bad {
		rec := postWatch(t, mux, tag, `{"watching":true}`)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":"watch_invalid"`) {
			t.Errorf("POST %s with SSE-Client %q = %d %s, want 400 watch_invalid", watchPath, tag, rec.Code, rec.Body)
		}
	}
	if p.viewers.any() {
		t.Error("a watch refused for its tag made a viewer")
	}
}

// Two pages of one browser profile share its stream tag, so the page names itself.
func TestWatchRoute_OnePageLeavingKeepsASiblingOnTheSameTag(t *testing.T) {
	p := viewerPoller(newHeldSource(false), &fakeGate{}, newLivePresence("tab-a"))
	mux := inventoryRoutes(sourceManager(t, nil), p)

	for _, body := range []string{
		`{"watching":true,"page":"page-1"}`,
		`{"watching":true,"page":"page-2"}`,
		`{"watching":false,"page":"page-1"}`,
	} {
		if rec := postWatch(t, mux, "tab-a", body); rec.Code != http.StatusNoContent {
			t.Fatalf("POST %s %s = %d %s, want 204", watchPath, body, rec.Code, rec.Body)
		}
	}
	if !p.viewers.any() {
		t.Fatal("page-1 leaving removed page-2, which shares its stream tag and still shows the view")
	}
	postWatch(t, mux, "tab-a", `{"watching":false,"page":"page-2"}`)
	if p.viewers.any() {
		t.Error("a viewer stayed after both pages left")
	}
	if rec := postWatch(t, mux, "tab-a", `{"watching":true,"page":"bad page"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("POST %s with page %q = %d, want 400", watchPath, "bad page", rec.Code)
	}
}

func TestWatchRoute_BodyMustSayWatching(t *testing.T) {
	p := viewerPoller(newHeldSource(false), &fakeGate{}, newLivePresence("tab-a"))
	mux := inventoryRoutes(sourceManager(t, nil), p)

	for _, body := range []string{``, `{}`, `not json`, `{"watching":"yes"}`, `{"watching":null}`} {
		rec := postWatch(t, mux, "tab-a", body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":"watch_invalid"`) {
			t.Errorf("POST %s with body %q = %d %s, want 400 watch_invalid", watchPath, body, rec.Code, rec.Body)
		}
	}
	if p.viewers.any() {
		t.Fatal("a refused body made a viewer")
	}
	if rec := postWatch(t, mux, "tab-a", `{"watching":true}`); rec.Code != http.StatusNoContent || !p.viewers.any() {
		t.Errorf("POST %s {watching: true} = %d, viewing %v; want 204 and a viewer", watchPath, rec.Code, p.viewers.any())
	}
}

func TestInventoryRoute_ViewingNamesAViewer(t *testing.T) {
	p := viewerPoller(newHeldSource(false), &fakeGate{}, newLivePresence("tab-a"))
	mux := inventoryRoutes(sourceManager(t, nil), p)

	rec := serve(t, mux, http.MethodGet, inventoryPath)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil || string(raw["viewing"]) != "false" {
		t.Errorf("GET %s with no viewer = %s, want viewing present and false", inventoryPath, rec.Body)
	}
	if rec := postWatch(t, mux, "tab-a", `{"watching":true}`); rec.Code != http.StatusNoContent {
		t.Fatalf("Setup: POST %s = %d %s, want 204", watchPath, rec.Code, rec.Body)
	}
	if got := readInventoryList(t, mux); !got.Viewing {
		t.Errorf("GET %s after a live tag said watching: viewing = false, want true", inventoryPath)
	}
	if rec := postWatch(t, mux, "tab-a", `{"watching":false}`); rec.Code != http.StatusNoContent {
		t.Fatalf("Setup: POST %s = %d %s, want 204", watchPath, rec.Code, rec.Body)
	}
	if got := readInventoryList(t, mux); got.Viewing {
		t.Errorf("GET %s after the tag said watching: false: viewing = true, want false", inventoryPath)
	}
}

func TestRefreshRoute_Answers202WithTheCycleID(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := newHeldSource(false)
		p := NewPRStatusPoller(src, &fakeNotifier{}, (&fakeGate{push: true}).Open)
		// The refresh route reads no connection.
		mux := inventoryRoutes(nil, p)
		stop := runLoop(t, p)
		defer stop()

		first := pressRefresh(t, mux)
		synctest.Wait()
		if got := src.reads(); first != "1" || len(got) != 1 || cycleOf(p, testConn.ID) != first {
			t.Errorf("first press answered %q, then %d reads and the entry from cycle %q; "+
				"want 1, one read at once and the entry from that cycle", first, len(got), cycleOf(p, testConn.ID))
		}
		second := pressRefresh(t, mux)
		synctest.Wait()
		if got := src.reads(); second != "2" || len(got) != 2 || cycleOf(p, testConn.ID) != second {
			t.Errorf("second press answered %q, then %d reads and the entry from cycle %q; want 2, 2 and 2",
				second, len(got), cycleOf(p, testConn.ID))
		}
	})
}

func TestRefreshRoute_BurstJoinsOneCycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := newHeldSource(true)
		p := NewPRStatusPoller(src, &fakeNotifier{}, (&fakeGate{push: true}).Open)
		mux := inventoryRoutes(nil, p)
		stop := runLoop(t, p)
		defer stop()

		ids := make([]string, 10)
		var wg sync.WaitGroup
		for i := range ids {
			wg.Go(func() { ids[i] = pressRefresh(t, mux) })
		}
		wg.Wait()
		synctest.Wait()
		close(src.release)
		synctest.Wait()

		if got := src.reads(); len(got) != 1 || slices.ContainsFunc(ids, func(id string) bool { return id != "1" }) {
			t.Errorf("ten concurrent presses answered %q and made %d reads, want every one cycle 1 and one read",
				ids, len(got))
		}
	})
}

func TestRefreshRoute_APressATimedSweepServedWakesNoSecondCycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := newHeldSource(false)
		p := NewPRStatusPoller(src, &fakeNotifier{}, (&fakeGate{push: true}).Open)
		mux := inventoryRoutes(nil, p)

		id := pressRefresh(t, mux)
		// The timer fired first: its sweep is the next cycle, so it serves the press.
		p.sweep(t.Context())
		stop := runLoop(t, p)
		defer stop()
		synctest.Wait()

		if got := src.reads(); id != "1" || !slices.Equal(got, []bool{true}) || cycleOf(p, testConn.ID) != "1" {
			t.Errorf("a press then a timed sweep: press answered %q, reads %v, entry from cycle %q; "+
				"want cycle 1, one present read and no second cycle once the loop runs", id, got, cycleOf(p, testConn.ID))
		}
	})
}

func TestRefreshRoute_GateClosedStillRunsOneCycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := newHeldSource(false)
		p := NewPRStatusPoller(src, &fakeNotifier{}, (&fakeGate{}).Open)
		mux := inventoryRoutes(nil, p)
		stop := runLoop(t, p)
		defer stop()

		id := pressRefresh(t, mux)
		synctest.Wait()

		if got := src.reads(); id != "1" || !slices.Equal(got, []bool{true}) || cycleOf(p, testConn.ID) != "1" {
			t.Errorf("a press with the gate closed answered %q, then reads %v and the entry from cycle %q; "+
				"want cycle 1, one present read and its entry: a press is a present client", id, got, cycleOf(p, testConn.ID))
		}
		stop()
		p.sweep(t.Context())
		if got := src.reads(); len(got) != 1 || len(entriesOf(p)) != 0 {
			t.Errorf("the next sweep with the gate still closed made %d reads and left %d entries, want none: "+
				"a press is served once", len(got)-1, len(entriesOf(p)))
		}
	})
}
