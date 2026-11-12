package composition

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/agent"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/kirosession"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/tabs"
)

// fakeKASRuns stands in for KAS's run surface (list, inspect, the run log root, a run's owner) behind keepRuns.
type fakeKASRuns struct {
	leases      *runlease.Store
	rowsErr     error
	recordedErr error
	inspect     map[string]agent.RunRow
	owners      map[string]command.RunOwner
	ownerErr    map[string]error
	attributed  map[string]string
	rows        []agent.RunRow
	recorded    []string
	inspected   []string
	// onInspect runs inside InspectRunRow before it answers: a write landing while the reconcile waits on KAS.
	onInspect func(wf string)
	mu        sync.Mutex
}

func (f *fakeKASRuns) RunRows(context.Context) ([]agent.RunRow, error) { return f.rows, f.rowsErr }

func (f *fakeKASRuns) InspectRunRow(_ context.Context, wf string) (agent.RunRow, error) {
	if f.onInspect != nil {
		f.onInspect(wf)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inspected = append(f.inspected, wf)
	if r, ok := f.inspect[wf]; ok {
		return r, nil
	}
	return agent.RunRow{}, errors.New("workflow not found")
}

func (f *fakeKASRuns) RecordedRuns(context.Context) ([]string, error) {
	return f.recorded, f.recordedErr
}

func (f *fakeKASRuns) RunOwner(_ context.Context, wf string) (command.RunOwner, error) {
	if err := f.ownerErr[wf]; err != nil {
		return command.RunOwner{}, err
	}
	if o, ok := f.owners[wf]; ok {
		return o, nil
	}
	return command.RunOwner{Verdict: command.RunUnknown}, nil
}

func (f *fakeKASRuns) RecordLeaseHolders(ctx context.Context, wf string, holders []marotte.ChatID) {
	add := make([]string, 0, len(holders))
	for _, h := range holders {
		add = append(add, string(h))
	}
	_ = f.leases.SetKeepChats(ctx, wf, add)
}

func (f *fakeKASRuns) SetRecordedRuns(parents map[string]string) { f.attributed = maps.Clone(parents) }

// keepRig is one boot's stores over a config dir an earlier boot may have written: the real chat, tab and lease
// stores and the real membership coordinator, with KAS faked.
type keepRig struct {
	chats   *chat.Store
	tabs    *tabs.Store
	leases  *runlease.Store
	runs    *fakeKASRuns
	deps    *keepStateDeps
	states  map[string]struct{}
	chatDir string
}

func newKeepRig(t *testing.T, root string) *keepRig {
	t.Helper()
	chatDir := filepath.Join(root, "chats")
	chats, err := chat.NewStore(chatDir)
	if err != nil {
		t.Fatalf("Setup: chat store: %v", err)
	}
	tabStore := openTabStore(root)
	leases := openRunLeaseStore(root)
	h := agent.New(t.Context(), t.TempDir(), nil, chats, agent.WithTabs(tabStore), agent.WithRunLeases(leases))
	runs := &fakeKASRuns{
		leases: leases, inspect: map[string]agent.RunRow{}, owners: map[string]command.RunOwner{},
		ownerErr: map[string]error{},
	}
	r := &keepRig{chats: chats, tabs: tabStore, leases: leases, runs: runs, chatDir: chatDir, states: map[string]struct{}{}}
	ready := make(chan struct{})
	close(ready)
	r.deps = &keepStateDeps{
		chats: chats, membership: h.Membership(), tabs: tabStore, leases: leases, runs: r.runs, ready: ready,
		runStates: func() (map[string]struct{}, error) { return maps.Clone(r.states), nil },
	}
	return r
}

// seedChat creates chatID with session in its chain.
func seedChat(t *testing.T, s *chat.Store, chatID, session string) {
	t.Helper()
	if _, err := s.Mutate(t.Context(), marotte.ChatID(chatID), func(c *marotte.Chat, _ bool) bool {
		c.Name = chatID
		if session != "" {
			c.RecordSession(session)
		}
		return true
	}); err != nil {
		t.Fatalf("Setup: seed %s: %v", chatID, err)
	}
}

func hold(t *testing.T, s *chat.Store, chatID, key string) {
	t.Helper()
	if _, err := s.Hold(t.Context(), marotte.ChatID(chatID), key, chat.KeepDurable); err != nil {
		t.Fatalf("Setup: hold %s on %s: %v", key, chatID, err)
	}
}

// makePreUpgrade strips a record back to what a build before keep facts wrote.
func makePreUpgrade(t *testing.T, chatDir, chatID string) {
	t.Helper()
	path := filepath.Join(chatDir, chatID, "chat.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Setup: read %s: %v", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("Setup: parse %s: %v", path, err)
	}
	delete(doc, "keep")
	delete(doc, "keep_filled_at")
	out, _ := json.Marshal(doc)
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatalf("Setup: write %s: %v", path, err)
	}
}

func keepKeys(t *testing.T, s *chat.Store, chatID string) []string {
	t.Helper()
	c, found, err := s.Lookup(t.Context(), marotte.ChatID(chatID))
	if err != nil || !found {
		t.Fatalf("Lookup(%s) = found %v, %v", chatID, found, err)
	}
	return slices.Sorted(maps.Keys(c.Keep))
}

func runKey(wf string) string    { k, _ := marotte.KeyRun(wf); return k }
func runTabKey(wf string) string { k, _ := marotte.KeyRunTab(wf); return k }

func (r *keepRig) round(t *testing.T) bool {
	t.Helper()
	_, done := keepRound(t.Context(), r.deps, keepStateRetry, keepStateListRetry)
	return done
}

func TestKeepRound_ReconcilesNothingWhileASourceCannotBeRead(t *testing.T) {
	cases := map[string]func(t *testing.T, root string, r *keepRig){
		"lease file unread": nil,
		"run list":          func(_ *testing.T, _ string, r *keepRig) { r.runs.rowsErr = errors.New("engine draining") },
		"run records": func(_ *testing.T, _ string, r *keepRig) {
			r.runs.recordedErr = errors.New("permission denied")
		},
		"engine run state": func(_ *testing.T, _ string, r *keepRig) {
			r.runs.recorded = []string{"wf_rec"}
			r.deps.runStates = func() (map[string]struct{}, error) { return nil, errors.New("ELOOP") }
		},
		"recorded run inspect fails": func(_ *testing.T, _ string, r *keepRig) {
			r.runs.recorded = []string{"wf_rec"}
			r.states["wf_rec"] = struct{}{}
		},
		"live run names no launcher": func(_ *testing.T, _ string, r *keepRig) {
			r.runs.rows = []agent.RunRow{{WorkflowID: "wf_orphan"}}
		},
		"run tab owner unread": func(t *testing.T, _ string, r *keepRig) {
			if _, _, _, err := r.tabs.Open(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_tab"}); err != nil {
				t.Fatalf("Setup: open run tab: %v", err)
			}
			r.runs.ownerErr["wf_tab"] = errors.New("inventory unreadable")
		},
		"unresolved run tab of an unknown run": func(t *testing.T, _ string, r *keepRig) {
			if _, _, _, err := r.tabs.Open(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_tab"}); err != nil {
				t.Fatalf("Setup: open run tab: %v", err)
			}
		},
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			first := newKeepRig(t, root)
			seedChat(t, first.chats, "c-a", "sess_a")
			if arrange == nil {
				if err := os.WriteFile(filepath.Join(root, "runs.json"), []byte("{garbage"), 0o600); err != nil {
					t.Fatalf("Setup: write runs.json: %v", err)
				}
			}
			r := newKeepRig(t, root)
			if arrange != nil {
				arrange(t, root, r)
			}

			if r.round(t) {
				t.Error("keepRound reported done with a source it could not read")
			}
			if r.chats.Reconciled("c-a") {
				t.Error("a record was reconciled in a round whose sources could not all be read")
			}
		})
	}
}

func TestKeepRound_FillsAPreUpgradeRecordAndAgesItFromTheFillIn(t *testing.T) {
	root := t.TempDir()
	first := newKeepRig(t, root)
	seedChat(t, first.chats, "c-old", "sess_old")
	makePreUpgrade(t, first.chatDir, "c-old")
	r := newKeepRig(t, root)

	if !r.round(t) {
		t.Fatal("keepRound over readable sources and one readable record did not finish")
	}
	c, _, err := r.chats.Lookup(t.Context(), "c-old")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if c.KeepFilledAt <= 0 || !r.chats.Reconciled("c-old") {
		t.Errorf("after the round KeepFilledAt = %d, reconciled = %v; want the fill-in floor set and the record reconciled",
			c.KeepFilledAt, r.chats.Reconciled("c-old"))
	}
	if len(c.Keep) != 0 {
		t.Errorf("Keep = %v, want no fact on a record nothing holds", c.Keep)
	}
}

func TestKeepRound_AddsTheFactsItsSourcesShow(t *testing.T) {
	root := t.TempDir()
	first := newKeepRig(t, root)
	for _, c := range []struct{ id, session string }{
		{"c-tab", "sess_tab"},
		{"c-lease", "sess_lease"},
		{"c-listed", "sess_listed"},
		{"c-recorded", "sess_rec"},
		{"c-runtab", "sess_runtab"},
		{"c-stateless", "sess_stateless"},
	} {
		seedChat(t, first.chats, c.id, c.session)
		makePreUpgrade(t, first.chatDir, c.id)
	}
	r := newKeepRig(t, root)
	if _, _, _, err := r.tabs.Open(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-tab"}); err != nil {
		t.Fatalf("Setup: open chat tab: %v", err)
	}
	if _, _, _, err := r.tabs.Open(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_shown"}); err != nil {
		t.Fatalf("Setup: open run tab: %v", err)
	}
	r.runs.owners["wf_shown"] = command.RunOwner{Verdict: command.RunOwned, Holders: []marotte.ChatID{"c-runtab"}}
	if err := r.leases.Put(t.Context(), &runlease.Lease{WorkflowID: "wf_lease", Origin: runlease.OriginManual, ParentSessionID: "sess_lease"}); err != nil {
		t.Fatalf("Setup: lease: %v", err)
	}
	r.runs.rows = []agent.RunRow{
		{WorkflowID: "wf_listed", ParentSessionID: "sess_listed"},
		{WorkflowID: "wf_shown", ParentSessionID: "sess_runtab", Terminal: true},
	}
	r.runs.recorded = []string{"wf_recorded", "wf_stateless", "wf_listed"}
	r.states["wf_recorded"] = struct{}{}
	r.runs.inspect["wf_recorded"] = agent.RunRow{WorkflowID: "wf_recorded", ParentSessionID: "sess_rec"}

	if !r.round(t) {
		t.Fatal("keepRound did not finish")
	}
	for id, want := range map[string][]string{
		"c-tab":       {marotte.KeyTab},
		"c-lease":     {runKey("wf_lease")},
		"c-listed":    {runKey("wf_listed")},
		"c-recorded":  {runKey("wf_recorded")},
		"c-runtab":    {runTabKey("wf_shown")},
		"c-stateless": nil,
	} {
		if got := keepKeys(t, r.chats, id); !slices.Equal(got, want) {
			t.Errorf("%s Keep = %v, want %v", id, got, want)
		}
	}
	if l, _ := r.leases.Get("wf_lease"); !slices.Equal(l.KeepChats, []string{"c-lease"}) {
		t.Errorf("lease KeepChats = %v, want the attributed holder recorded", l.KeepChats)
	}
	open, _ := r.tabs.List()
	for _, tab := range open {
		if tab.Kind == marotte.TabKindRun && (!tab.LaunchResolved || !slices.Equal(tab.LaunchChats, []string{"c-runtab"})) {
			t.Errorf("run tab after the round = %+v, want it resolved to its holder", tab)
		}
	}
	if got := r.runs.attributed; !maps.Equal(got, map[string]string{"wf_recorded": "sess_rec"}) {
		t.Errorf("recorded runs attributed = %v, want only the live run KAS keeps state for", got)
	}
	if slices.Contains(r.runs.inspected, "wf_stateless") {
		t.Error("a run record KAS keeps no state for was inspected; it needs no protection")
	}
}

// KAS finds a run by id in the workspace's own .kiro/workflows too, so a recorded run whose state sits there is live.
func TestKeepRound_InspectsARecordedRunWhoseStateSitsInTheWorkspaceBase(t *testing.T) {
	r := newKeepRig(t, t.TempDir())
	workDir := t.TempDir()
	writeFixture(t, filepath.Join(workDir, ".kiro", "workflows", "wf_ws", "workflow-state.json"),
		`{"workflowId":"wf_ws","status":"running","parentSessionId":"sess_ws"}`)
	r.deps.runStates = kirosession.New(filepath.Join(t.TempDir(), "sessions"), workDir).RunStates
	r.runs.recorded = []string{"wf_ws"}
	r.runs.inspect["wf_ws"] = agent.RunRow{WorkflowID: "wf_ws", ParentSessionID: "sess_ws"}

	if !r.round(t) {
		t.Fatal("keepRound did not finish")
	}
	if got := r.runs.attributed; !maps.Equal(got, map[string]string{"wf_ws": "sess_ws"}) {
		t.Errorf("recorded runs attributed = %v, want the live run whose state sits in the workspace base", got)
	}
	if !slices.Contains(r.runs.inspected, "wf_ws") {
		t.Error("a recorded run whose state sits in the workspace base was not inspected, so it counted as stateless")
	}
}

func TestKeepRound_ATabArrangementItCouldNotReadHoldsEveryPreUpgradeRecord(t *testing.T) {
	root := t.TempDir()
	first := newKeepRig(t, root)
	seedChat(t, first.chats, "c-old", "sess_old")
	makePreUpgrade(t, first.chatDir, "c-old")
	seedChat(t, first.chats, "c-new", "sess_new")
	if err := os.WriteFile(filepath.Join(root, "tabs.json"), []byte("{garbage"), 0o600); err != nil {
		t.Fatalf("Setup: write tabs.json: %v", err)
	}
	r := newKeepRig(t, root)
	if r.tabs.Unread() == "" {
		t.Fatal("Setup: the tab store read a garbage arrangement")
	}

	if !r.round(t) {
		t.Fatal("keepRound did not finish")
	}
	if got := keepKeys(t, r.chats, "c-old"); !slices.Equal(got, []string{marotte.KeyTab}) {
		t.Errorf("pre-upgrade Keep = %v, want tab: its tab may have been in the lost arrangement", got)
	}
	if got := keepKeys(t, r.chats, "c-new"); len(got) != 0 {
		t.Errorf("filled record Keep = %v, want none: its tab facts were already written by this design's writers", got)
	}
}

// A previous boot's turn fact and an owed tab release are settled; its run marks stay, since they keep by time and
// no reconcile read ends one.
func TestKeepRound_SettlesAPreviousBootsTurnAndTabFactsAndKeepsItsRunMarks(t *testing.T) {
	root := t.TempDir()
	first := newKeepRig(t, root)
	for _, id := range []string{"c-turn", "c-terminal", "c-live", "c-inspected", "c-lost", "c-leased", "c-tab", "c-owed", "c-mine"} {
		seedChat(t, first.chats, id, "sess_"+id[2:])
	}
	if err := first.chats.HoldTurn(t.Context(), "c-turn"); err != nil {
		t.Fatalf("Setup: turn: %v", err)
	}
	hold(t, first.chats, "c-terminal", runKey("wf_terminal"))
	hold(t, first.chats, "c-live", runKey("wf_live"))
	hold(t, first.chats, "c-inspected", runKey("wf_inspected"))
	hold(t, first.chats, "c-lost", runKey("wf_lost"))
	hold(t, first.chats, "c-leased", runKey("wf_leased"))
	hold(t, first.chats, "c-tab", marotte.KeyTab)
	hold(t, first.chats, "c-owed", marotte.KeyTab)
	owedTab, _, _, err := first.tabs.Open(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-owed"})
	if err != nil {
		t.Fatalf("Setup: open: %v", err)
	}
	if _, _, _, err := first.tabs.Close(t.Context(), owedTab.ID, nil, nil); err != nil {
		t.Fatalf("Setup: close: %v", err)
	}
	if err := first.leases.Put(t.Context(), &runlease.Lease{
		WorkflowID: "wf_leased", Origin: runlease.OriginManual,
		ParentSessionID: "sess_leased", KeepChats: []string{"c-leased"},
	}); err != nil {
		t.Fatalf("Setup: lease: %v", err)
	}
	r := newKeepRig(t, root)
	hold(t, r.chats, "c-mine", runKey("wf_mine"))
	r.runs.rows = []agent.RunRow{
		{WorkflowID: "wf_terminal", ParentSessionID: "sess_terminal", Terminal: true},
		{WorkflowID: "wf_live", ParentSessionID: "sess_live"},
		{WorkflowID: "wf_leased", ParentSessionID: "sess_leased", Terminal: true},
	}
	r.runs.inspect["wf_inspected"] = agent.RunRow{WorkflowID: "wf_inspected", Terminal: true}

	if !r.round(t) {
		t.Fatal("keepRound did not finish")
	}
	for id, want := range map[string][]string{
		"c-turn":      nil,
		"c-terminal":  {runKey("wf_terminal")},
		"c-live":      {runKey("wf_live")},
		"c-inspected": {runKey("wf_inspected")},
		"c-lost":      {runKey("wf_lost")},
		"c-leased":    {runKey("wf_leased")},
		"c-tab":       {marotte.KeyTab},
		"c-owed":      nil,
		"c-mine":      {runKey("wf_mine")},
	} {
		if got := keepKeys(t, r.chats, id); !slices.Equal(got, want) {
			t.Errorf("%s Keep = %v, want %v", id, got, want)
		}
	}
	if len(r.tabs.Owed()) != 0 {
		t.Errorf("Owed() = %v, want the close's entry settled", r.tabs.Owed())
	}
	if c, _, _ := r.chats.Lookup(t.Context(), "c-leased"); c.Keep[runKey("wf_leased")].Boot != first.chats.Boot() {
		t.Errorf("c-leased fact = %+v, want the earlier boot's fact untouched: a fill-in is not a sign of life", c.Keep)
	}
}

func TestKeepRound_LeavesARecordItCouldNotReadForALaterRound(t *testing.T) {
	root := t.TempDir()
	first := newKeepRig(t, root)
	seedChat(t, first.chats, "c-a", "sess_a")
	path := filepath.Join(first.chatDir, "c-a", "chat.json")
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Setup: read: %v", err)
	}
	if err := os.WriteFile(path, []byte("{garbage"), 0o600); err != nil {
		t.Fatalf("Setup: corrupt: %v", err)
	}
	r := newKeepRig(t, root)

	if r.round(t) {
		t.Error("keepRound reported done with an unreadable record left")
	}
	if r.chats.Reconciled("c-a") {
		t.Fatal("an unreadable record was reconciled")
	}
	if err := os.WriteFile(path, good, 0o600); err != nil {
		t.Fatalf("repair: %v", err)
	}
	if !r.round(t) || !r.chats.Reconciled("c-a") {
		t.Error("the round after the record became readable did not reconcile it")
	}
}

// failingReconcile is the real coordinator with one record's reconcile write failing.
type failingReconcile struct {
	keepMembership
	fail marotte.ChatID
}

func (f *failingReconcile) ReconcileTabs(ctx context.Context, id marotte.ChatID, fn func(*chat.KeepRecord, []marotte.TabSubject)) error {
	if id == f.fail {
		return errors.New("chat.json: read-only file system")
	}
	return f.keepMembership.ReconcileTabs(ctx, id, fn)
}

func TestKeepRound_AFailedReconcileWriteLeavesTheRoundUnfinished(t *testing.T) {
	root := t.TempDir()
	first := newKeepRig(t, root)
	seedChat(t, first.chats, "c-a", "sess_a")
	seedChat(t, first.chats, "c-b", "sess_b")
	r := newKeepRig(t, root)
	r.deps.membership = &failingReconcile{keepMembership: r.deps.membership, fail: "c-a"}

	if r.round(t) {
		t.Error("keepRound reported done with a record whose reconcile failed")
	}
	if r.chats.Reconciled("c-a") || !r.chats.Reconciled("c-b") {
		t.Errorf("reconciled c-a = %v, c-b = %v; want only the record whose write succeeded",
			r.chats.Reconciled("c-a"), r.chats.Reconciled("c-b"))
	}
}

// deletingReconcile is the real coordinator with one record deleted after the round's scan read it.
type deletingReconcile struct {
	keepMembership
	chats *chat.Store
	gone  marotte.ChatID
}

func (f *deletingReconcile) ReconcileTabs(ctx context.Context, id marotte.ChatID, fn func(*chat.KeepRecord, []marotte.TabSubject)) error {
	if id == f.gone {
		if _, err := f.chats.Delete(ctx, id); err != nil {
			return err
		}
	}
	return f.keepMembership.ReconcileTabs(ctx, id, fn)
}

func TestKeepRound_ARecordDeletedSinceTheScanIsNotLeftUnreconciled(t *testing.T) {
	root := t.TempDir()
	first := newKeepRig(t, root)
	seedChat(t, first.chats, "c-a", "sess_a")
	seedChat(t, first.chats, "c-b", "sess_b")
	r := newKeepRig(t, root)
	r.deps.membership = &deletingReconcile{keepMembership: r.deps.membership, chats: r.chats, gone: "c-a"}
	logs := captureSeamLogs(t)

	if !r.round(t) {
		t.Error("keepRound reported a record deleted since its scan as unreconciled")
	}
	if logs.has("a chat could not be reconciled") {
		t.Error("the reconcile warned about a record that was deleted")
	}
	if !r.chats.Reconciled("c-b") {
		t.Error("c-b was not reconciled")
	}
}

func TestReconcileKeepState_StopsOnceEveryRecordIsReconciled(t *testing.T) {
	root := t.TempDir()
	first := newKeepRig(t, root)
	seedChat(t, first.chats, "c-a", "sess_a")
	r := newKeepRig(t, root)
	done := make(chan struct{})
	go func() {
		defer close(done)
		reconcileKeepState(context.WithoutCancel(t.Context()), r.deps)
	}()
	<-done
	if !r.chats.Reconciled("c-a") {
		t.Error("reconcileKeepState returned with a readable record unreconciled")
	}
}
