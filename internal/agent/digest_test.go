package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
	"github.com/cplieger/marotte/internal/tabs"
	"github.com/cplieger/sse"
)

func held(kind subject.Kind, ref string) sse.Held {
	return sse.Held{Kind: string(kind), Ref: ref}
}

// stateFor picks the answer for (kind, ref) out of a resolution, whatever its order.
func stateFor(t *testing.T, states []sse.State, kind subject.Kind, ref string) sse.State {
	t.Helper()
	for _, s := range states {
		if s.Kind == string(kind) && s.Ref == ref {
			return s
		}
	}
	t.Fatalf("no State for %s:%s among %+v", kind, ref, states)
	return sse.State{}
}

func TestResolveDigest_OneStatePerHeldFromTheRegistry(t *testing.T) {
	h, cs, _ := newTestHub()
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "x"; return true }); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// The fake store mints into its own counter; the resolver reads the runtime's
	// registry, so mint the subjects there the way the real writers would.
	h.versions.BumpCounter(subject.KindChat, "c1")
	h.versions.BumpCounter(subject.KindChat, "c1")
	h.versions.BumpCounter(subject.KindChats, "")
	h.versions.BumpCounter(subject.KindStatus, "")

	req := []sse.Held{
		held(subject.KindStatus, ""), held(subject.KindChat, "c1"), held(subject.KindChats, ""),
		held(subject.KindPending, ""), held(subject.KindRuns, ""), held(subject.KindCatalog, ""),
	}
	states, err := h.resolveDigest(t.Context(), req)
	if err != nil {
		t.Fatalf("resolveDigest: %v", err)
	}
	if len(states) != len(req) {
		t.Fatalf("states = %d, want one per held (%d)", len(states), len(req))
	}
	for _, tc := range []struct {
		kind subject.Kind
		ref  string
		want string
	}{
		{subject.KindChat, "c1", "2"},
		{subject.KindChats, "", "1"},
		{subject.KindStatus, "", "1"},
		// Never minted this process: "0", matching what the REST envelope stamps.
		{subject.KindPending, "", subject.Unminted},
		{subject.KindRuns, "", subject.Unminted},
		{subject.KindCatalog, "", subject.Unminted},
	} {
		st := stateFor(t, states, tc.kind, tc.ref)
		if st.Version != tc.want || st.Status != sse.StatusCurrent {
			t.Errorf("%s:%s = {%q %q}, want {%q current}", tc.kind, tc.ref, st.Version, st.Status, tc.want)
		}
	}
}

func TestResolveDigest_ChatWhoseRecordIsGoneAnswersGone(t *testing.T) {
	h, cs, _ := newTestHub()
	if _, err := cs.Mutate(t.Context(), "kept", func(c *marotte.Chat, _ bool) bool { c.Name = "x"; return true }); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h.versions.BumpCounter(subject.KindChat, "deleted") // a version the registry still remembers
	states, err := h.resolveDigest(t.Context(), []sse.Held{held(subject.KindChat, "kept"), held(subject.KindChat, "deleted")})
	if err != nil {
		t.Fatalf("resolveDigest: %v", err)
	}
	if st := stateFor(t, states, subject.KindChat, "deleted"); st.Status != sse.StatusGone {
		t.Errorf("deleted chat = %+v, want gone: the record, not the counter, decides existence", st)
	}
	if st := stateFor(t, states, subject.KindChat, "kept"); st.Status != sse.StatusCurrent {
		t.Errorf("kept chat = %+v, want current", st)
	}
}

func TestResolveDigest_LiveTurnFollowsTheTurnRegistry(t *testing.T) {
	h, _, _ := newTestHub()
	states, err := h.resolveDigest(t.Context(), []sse.Held{held(subject.KindLiveTurn, "t-never-opened")})
	if err != nil {
		t.Fatalf("resolveDigest: %v", err)
	}
	if st := stateFor(t, states, subject.KindLiveTurn, "t-never-opened"); st.Status != sse.StatusGone {
		t.Errorf("no such open turn: %+v, want gone so the client clears its open tail", st)
	}

	id, log := h.stagePromptTurn(t, "c1")
	if _, err := log.TextDelta(t.Context(), "", "say-1", "still coalescing"); err != nil {
		t.Fatalf("TextDelta: %v", err)
	}
	states, err = h.resolveDigest(t.Context(), []sse.Held{held(subject.KindLiveTurn, id)})
	if err != nil {
		t.Fatalf("resolveDigest: %v", err)
	}
	if st := stateFor(t, states, subject.KindLiveTurn, id); st.Status != sse.StatusCurrent || st.Version != turnVersion(id, 0) {
		t.Errorf("open turn with an unsealed delta: %+v, want current at %q (the turn_open is the newest sealed seq)", st, turnVersion(id, 0))
	}

	// A kind change seals the text, so the newest sealed seq moves to 1.
	if _, err := log.ThinkingDelta(t.Context(), "", "say-1", "then reasoning"); err != nil {
		t.Fatalf("ThinkingDelta: %v", err)
	}
	states, err = h.resolveDigest(t.Context(), []sse.Held{held(subject.KindLiveTurn, id)})
	if err != nil {
		t.Fatalf("resolveDigest: %v", err)
	}
	if st := stateFor(t, states, subject.KindLiveTurn, id); st.Status != sse.StatusCurrent || st.Version != turnVersion(id, 1) {
		t.Errorf("open turn after a seal: %+v, want current at %q", st, turnVersion(id, 1))
	}

	endTurn(t, h, "c1", id)
	states, err = h.resolveDigest(t.Context(), []sse.Held{held(subject.KindLiveTurn, id)})
	if err != nil {
		t.Fatalf("resolveDigest: %v", err)
	}
	if st := stateFor(t, states, subject.KindLiveTurn, id); st.Status != sse.StatusGone {
		t.Errorf("closed turn: %+v, want gone: the registry no longer holds it", st)
	}
}

func TestResolveDigest_RunTurnFollowsTheRunRegistry(t *testing.T) {
	h := newBudgetRuntime(t)
	turn, _, err := h.runs.log.Open(t.Context(), "wf-1", "wf-1/step", "sess-step", "c1")
	if err != nil {
		t.Fatalf("Open(step turn): %v", err)
	}
	ref := "wf-1/" + turn.ID()
	states, err := h.resolveDigest(t.Context(), []sse.Held{held(subject.KindRunTurn, ref)})
	if err != nil {
		t.Fatalf("resolveDigest: %v", err)
	}
	if st := stateFor(t, states, subject.KindRunTurn, ref); st.Status != sse.StatusCurrent || st.Version != turnVersion(turn.ID(), 0) {
		t.Errorf("open step turn: %+v, want current at %q", st, turnVersion(turn.ID(), 0))
	}

	if _, _, err := h.runs.log.CloseNode(t.Context(), "wf-1", "wf-1/step", "completed", ""); err != nil {
		t.Fatalf("CloseNode: %v", err)
	}
	states, err = h.resolveDigest(t.Context(), []sse.Held{held(subject.KindRunTurn, ref)})
	if err != nil {
		t.Fatalf("resolveDigest: %v", err)
	}
	if st := stateFor(t, states, subject.KindRunTurn, ref); st.Status != sse.StatusGone {
		t.Errorf("closed step turn: %+v, want gone", st)
	}
}

func TestResolveDigest_TabsIsTheStoresCollectionVersion(t *testing.T) {
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("tabs store: %v", err)
	}
	h, _, _ := newTestHub()
	WithTabs(st)(h)
	if _, _, _, err := st.Open(t.Context(), marotte.OpenTab{Kind: marotte.TabKindSettings}); err != nil {
		t.Fatalf("open: %v", err)
	}
	states, err := h.resolveDigest(t.Context(), []sse.Held{held(subject.KindTabs, "")})
	if err != nil {
		t.Fatalf("resolveDigest: %v", err)
	}
	_, version := st.List()
	if got := stateFor(t, states, subject.KindTabs, ""); got.Version != "1" || version != 1 {
		t.Errorf("tabs = %+v (store at %d), want version 1 after one open", got, version)
	}
}

func TestResolveDigest_ForgeInventorySubjectResolves(t *testing.T) {
	h, _, _ := newTestHub()
	h.versions.BumpCounter(subject.KindForgeInventory, "github:github.com")
	h.versions.BumpCounter(subject.KindForgeInventory, "github:github.com")
	states, err := h.resolveDigest(t.Context(), []sse.Held{
		held(subject.KindForgeInventory, "github:github.com"), held(subject.KindForgeInventory, "gitlab:gitlab.com"),
	})
	if err != nil {
		t.Fatalf("resolveDigest: %v", err)
	}
	for _, tc := range []struct{ ref, want string }{
		{"github:github.com", "2"},
		{"gitlab:gitlab.com", subject.Unminted},
	} {
		if st := stateFor(t, states, subject.KindForgeInventory, tc.ref); st.Version != tc.want || st.Status != sse.StatusCurrent {
			t.Errorf("forge_inventory:%s = {%q %q}, want {%q current}: the inventory's version is the registry's",
				tc.ref, st.Version, st.Status, tc.want)
		}
	}
}

func TestResolveDigest_UnknownKindIsGone(t *testing.T) {
	h, _, _ := newTestHub()
	states, err := h.resolveDigest(t.Context(), []sse.Held{{Kind: "weather", Ref: "x"}})
	if err != nil {
		t.Fatalf("resolveDigest: %v", err)
	}
	if len(states) != 1 || states[0].Status != sse.StatusGone {
		t.Errorf("unknown kind = %+v, want one gone State so the client forgets it", states)
	}
}

// Five concurrent requests hold at most four resolutions at once: the hook parks
// every resolution inside its slot until the test releases them.
func TestResolveDigest_AtMostFourResolutionsAtOnce(t *testing.T) {
	h, _, _ := newTestHub()
	var inside, peak atomic.Int32
	release := make(chan struct{})
	h.digestHook = func() {
		n := inside.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-release
		inside.Add(-1)
	}
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if _, err := h.resolveDigest(t.Context(), []sse.Held{held(subject.KindChats, "")}); err != nil {
				t.Errorf("resolveDigest: %v", err)
			}
		})
	}
	// Wait for the four slots to fill and the fifth to park on acquire.
	deadline := time.Now().Add(5 * time.Second)
	for inside.Load() < digestConcurrency {
		if time.Now().After(deadline) {
			t.Fatalf("only %d resolutions entered, want %d", inside.Load(), digestConcurrency)
		}
		time.Sleep(time.Millisecond)
	}
	if got := len(h.digestSlots); got != digestConcurrency {
		t.Errorf("slots held = %d, want %d: the fifth request must be waiting, not resolving", got, digestConcurrency)
	}
	close(release)
	wg.Wait()
	if p := peak.Load(); p != digestConcurrency {
		t.Errorf("peak concurrent resolutions = %d, want exactly %d", p, digestConcurrency)
	}
}

func TestResolveDigest_ContextExpiryWhileWaitingReturnsCtxErr(t *testing.T) {
	h, _, _ := newTestHub()
	for range digestConcurrency {
		if err := h.digestSlots.acquire(t.Context()); err != nil {
			t.Fatalf("fill slot: %v", err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	states, err := h.resolveDigest(ctx, []sse.Held{held(subject.KindChats, "")})
	if !errors.Is(err, context.DeadlineExceeded) || states != nil {
		t.Errorf("resolveDigest with every slot held = (%v, %v), want (nil, DeadlineExceeded)", states, err)
	}
	for range digestConcurrency {
		h.digestSlots.release()
	}
}

// The mounted route is the library's handler under the route timeout: method and
// content type are its refusals, asserted once through the runtime's own mux.
func TestSyncRoute_RefusesWrongMethodAndContentType(t *testing.T) {
	h, _, _ := newTestHub()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sync", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/sync = %d, want 405", rec.Code)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sync", strings.NewReader(`{}`)))
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("POST /api/sync without application/json = %d, want 415", rec.Code)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync", strings.NewReader(`{"epoch":"`+h.Epoch()+`","subjects":[]}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("well-formed POST /api/sync = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}
