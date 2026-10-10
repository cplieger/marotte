package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// settledNow answers the gate's outcome without waiting, and whether it had one.
func settledNow(s *sessionSettle) (settled bool, err error) {
	select {
	case <-s.done:
		return true, s.err
	default:
		return false, nil
	}
}

func TestReplayProjection_SettlesTheLoadsGateOnlyOnceItsSwapRan(t *testing.T) {
	const chatID marotte.ChatID = "c1"
	errSwap := errors.New("disk full")

	t.Run("swapCommitted", func(t *testing.T) {
		rp, _ := replayWithRecorder()
		gate := newSessionSettle()
		rp.OpenReplayProjection(t.Context(), chatID, "old-acp", gate)
		feedOneTurn(t, rp, chatID)
		rp.MarkReplayLoadedAt(chatID, atLoad())
		rp.SettleReplayProjection(chatID, atFrame(testLoadSeq-1), false)
		if settled, _ := settledNow(gate); settled {
			t.Fatal("the gate settled one frame short of the load position, before the swap ran")
		}

		rp.SettleReplayProjection(chatID, atFrame(testLoadSeq), false)
		if settled, err := settledNow(gate); !settled || err != nil {
			t.Errorf("gate after the swap = (%v, settled %v), want (nil, true)", err, settled)
		}
	})

	t.Run("swapFailed", func(t *testing.T) {
		rp, rec := replayWithRecorder()
		rec.err = errSwap
		gate := newSessionSettle()
		rp.OpenReplayProjection(t.Context(), chatID, "old-acp", gate)
		feedOneTurn(t, rp, chatID)
		rp.MarkReplayLoadedAt(chatID, atLoad())
		rp.SettleReplayProjection(chatID, atFrame(testLoadSeq), false)
		if settled, err := settledNow(gate); !settled || !errors.Is(err, errSwap) {
			t.Errorf("gate after a failed swap = (%v, settled %v), want (%v, true)", err, settled, errSwap)
		}
	})

	t.Run("superseded", func(t *testing.T) {
		rp, _ := replayWithRecorder()
		first, second := newSessionSettle(), newSessionSettle()
		rp.OpenReplayProjection(t.Context(), chatID, "old-acp", first)
		rp.OpenReplayProjection(t.Context(), chatID, "old-acp", second)
		if settled, err := settledNow(first); !settled || !errors.Is(err, errReplaySuperseded) {
			t.Errorf("superseded load's gate = (%v, settled %v), want (%v, true)", err, settled, errReplaySuperseded)
		}
		if settled, _ := settledNow(second); settled {
			t.Error("the re-load's gate settled before its own replay was read")
		}
	})
}

// newReplayingHub serves chat c1 from a stored session whose replay holds one turn; withhold
// trailing frames reach Forward only after releaseWithheld.
func newReplayingHub(t *testing.T, withhold int) (*Runtime, *testChatStore) {
	t.Helper()
	cs := newTestChatStore()
	h := New(context.Background(), t.TempDir(), func() ACPBridge {
		b := newFakeBridge()
		b.notifsOnStart = []*marotte.RPCResponse{
			replayNotif(t, "user_message_chunk", "ONE", ""),
			replayNotif(t, marotte.ACPUpdateSessionInfo, "", "turn_start"),
			replayNotif(t, marotte.ACPUpdateAgentChunk, "reply", ""),
			replayNotif(t, marotte.ACPUpdateSessionInfo, "", "turn_end"),
		}
		b.withholdReplay = withhold
		return b
	}, cs)
	cs.wire(h)
	loadedChat(t, cs, "c1")
	return h, cs
}

func TestLoadSession_AnswersOnlyOnceTheReplayMergedIntoTheLog(t *testing.T) {
	h, cs := newReplayingHub(t, 1)

	// The load returns while Forward is a frame short, so only the gate can hold this.
	short, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if err := h.coord.LoadSession(short, "c1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("LoadSession with the replay a frame short = %v, want it still waiting at the deadline", err)
	}
	entries, err := cs.All(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if got := textsOf(t, entries); slices.Contains(got, "reply") {
		t.Fatalf("log texts before the last replay frame was read = %q, want the replayed turn absent", got)
	}

	br, ok := h.coord.bridgeFor("c1").bridge.(*fakeBridge)
	if !ok {
		t.Fatalf("the chat's bridge is %T, want the fake", h.coord.bridgeFor("c1").bridge)
	}
	br.releaseWithheld()
	if err := h.coord.LoadSession(t.Context(), "c1"); err != nil {
		t.Fatalf("LoadSession once the replay was read = %v, want nil", err)
	}
	entries, err = cs.All(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if got := textsOf(t, entries); !slices.Contains(got, "reply") {
		t.Errorf("log texts when LoadSession answered = %q, want the replayed turn merged", got)
	}
}

// A failed replay swap fails its generation, which leaves the registry stopped, so the next load
// reconciles through a fresh bridge once storage recovers rather than inheriting the failure.
func TestLoadSession_AFailedReplaySwapRecoversThroughASecondGeneration(t *testing.T) {
	h, cs := newReplayingHub(t, 0)
	errSwap := errors.New("disk full")
	var mu sync.Mutex
	failNext := true
	serialized := h.replay.underLifecycle
	h.replay.underLifecycle = func(ctx context.Context, chatID marotte.ChatID, fn func() error) error {
		mu.Lock()
		fail := failNext
		failNext = false
		mu.Unlock()
		if fail {
			return errSwap
		}
		return serialized(ctx, chatID, fn)
	}

	if err := h.coord.LoadSession(t.Context(), "c1"); !errors.Is(err, errSwap) {
		t.Fatalf("LoadSession over a failed replay swap = %v, want %v", err, errSwap)
	}
	if sb := h.coord.bridgeFor("c1"); sb != nil {
		if settled, err := settledNow(sb.settled); settled && err != nil {
			t.Fatal("the failed generation is still registered, so every later load inherits its error")
		}
	}

	if err := h.coord.LoadSession(t.Context(), "c1"); err != nil {
		t.Fatalf("LoadSession once storage recovered = %v, want nil", err)
	}
	entries, err := cs.All(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if got := textsOf(t, entries); !slices.Contains(got, "reply") {
		t.Errorf("log texts after the recovered load = %q, want the replayed turn merged", got)
	}
}

// A failed generation answers that failure, never a reopen in the same call, and resumes no runs:
// either would respawn the same failure over and over.
func TestLoadSession_AFailedGenerationIsNotReopenedOrResumed(t *testing.T) {
	cs := newTestChatStore()
	var spawns atomic.Int32
	h := New(context.Background(), t.TempDir(), func() ACPBridge {
		b := newFakeBridge()
		// Bounds a respawn loop, so a regression fails here rather than hanging the package.
		if spawns.Add(1) > 3 {
			b.startErr = errors.New("spawn cap reached")
		}
		b.notifsOnStart = []*marotte.RPCResponse{
			replayNotif(t, "user_message_chunk", "ONE", ""),
			replayNotif(t, marotte.ACPUpdateSessionInfo, "", "turn_start"),
			replayNotif(t, marotte.ACPUpdateAgentChunk, "reply", ""),
			replayNotif(t, marotte.ACPUpdateSessionInfo, "", "turn_end"),
		}
		return b
	}, cs)
	cs.wire(h)
	loadedChat(t, cs, "c1")
	errSwap := errors.New("disk full")
	h.replay.underLifecycle = func(context.Context, marotte.ChatID, func() error) error { return errSwap }
	var resumed atomic.Int32
	h.coord.onSessionRehydrated = func(marotte.ChatID) { resumed.Add(1) }
	before := spawns.Load()

	// A reopen loop spins until this deadline rather than hanging the package.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := h.coord.LoadSession(ctx, "c1"); !errors.Is(err, errSwap) {
		t.Fatalf("LoadSession over a failing replay swap = %v, want %v", err, errSwap)
	}
	joinInflight(t, h)
	if n := spawns.Load() - before; n != 1 {
		t.Errorf("bridges spawned by one failing load = %d, want 1", n)
	}
	if n := resumed.Load(); n != 0 {
		t.Errorf("run resumes fired for a generation whose replay never merged = %d, want 0", n)
	}
}

// No opener receives a generation before its replay merged, so a prompt cannot hold one whose swap then
// fails, and the failure leaves nothing registered for the next load to inherit.
func TestOpenBridge_AdmitsNoCallerBeforeTheReplayMerged(t *testing.T) {
	cs := newTestChatStore()
	var spawns atomic.Int32
	h := New(context.Background(), t.TempDir(), func() ACPBridge {
		b := newFakeBridge()
		b.notifsOnStart = []*marotte.RPCResponse{
			replayNotif(t, "user_message_chunk", "ONE", ""),
			replayNotif(t, marotte.ACPUpdateSessionInfo, "", "turn_start"),
			replayNotif(t, marotte.ACPUpdateAgentChunk, "reply", ""),
			replayNotif(t, marotte.ACPUpdateSessionInfo, "", "turn_end"),
		}
		// Only the first generation's replay is held a frame short.
		if spawns.Add(1) == 1 {
			b.withholdReplay = 1
		}
		return b
	}, cs)
	cs.wire(h)
	loadedChat(t, cs, "c1")
	errSwap := errors.New("disk full")
	var failed atomic.Bool
	serialized := h.replay.underLifecycle
	h.replay.underLifecycle = func(ctx context.Context, chatID marotte.ChatID, fn func() error) error {
		if failed.CompareAndSwap(false, true) {
			return errSwap
		}
		return serialized(ctx, chatID, fn)
	}

	short, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	sb, err := h.coord.openBridge(short, "c1", "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("OpenBridge with the replay a frame short = (%v, %v), want no bridge at the deadline", sb, err)
	}
	if sb != nil && !sb.TryAcquireForPrompt() {
		t.Error("the bridge handed out before its replay merged is not even idle")
	}
	first := h.coord.bridgeFor("c1")
	if first == nil {
		t.Fatal("the load registered no generation")
	}
	br, ok := first.bridge.(*fakeBridge)
	if !ok {
		t.Fatalf("the chat's bridge is %T, want the fake", first.bridge)
	}
	br.releaseWithheld()

	if err := first.settled.wait(t.Context()); !errors.Is(err, errSwap) {
		t.Fatalf("first generation's readiness = %v, want its swap failure %v", err, errSwap)
	}
	if err := h.coord.LoadSession(t.Context(), "c1"); err != nil {
		t.Fatalf("LoadSession after the first generation's swap failed = %v, want a fresh generation's nil", err)
	}
	if got := h.coord.bridgeFor("c1"); got == first {
		t.Error("the generation whose swap failed is still the chat's bridge")
	}
	entries, err := cs.All(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if got := textsOf(t, entries); !slices.Contains(got, "reply") {
		t.Errorf("log texts after the second generation = %q, want the replayed turn merged", got)
	}
}

// The caller's context bounds its own wait from the first instant, while the spawn it started runs
// on under the process lifetime and keeps the chat's session.
func TestLoadSession_ACancelledWaiterReturnsBeforeAHeldSpawnCompletes(t *testing.T) {
	cs := newTestChatStore()
	gate := make(chan struct{})
	h := New(context.Background(), t.TempDir(), func() ACPBridge {
		b := newFakeBridge()
		b.startGate = gate
		return b
	}, cs)
	cs.wire(h)
	loadedChat(t, cs, "c1")

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- h.coord.LoadSession(ctx, "c1") }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("LoadSession for a cancelled waiter = %v, want %v", err, context.Canceled)
		}
	case <-time.After(2 * time.Second):
		close(gate)
		t.Fatal("LoadSession waited out the held spawn after its caller cancelled")
	}

	close(gate)
	deadline := time.Now().Add(5 * time.Second)
	for sb := h.coord.bridgeFor("c1"); sb == nil || !sb.startedPastSpawn(); sb = h.coord.bridgeFor("c1") {
		if time.Now().After(deadline) {
			t.Fatal("the spawn the cancelled waiter started never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if c, _ := cs.Get(t.Context(), "c1"); c.ACPSessionID != "old-acp" {
		t.Errorf("chat session after a cancelled waiter = %q, want old-acp kept", c.ACPSessionID)
	}
}

// A chat whose record is gone is a terminal answer, distinct from every retryable failure.
func TestLoadSession_AMissingChatIsTerminal(t *testing.T) {
	h, _ := newReplayingHub(t, 0)

	if err := h.coord.LoadSession(t.Context(), "c-deleted"); !errors.Is(err, command.ErrChatGone) {
		t.Errorf("LoadSession of a deleted chat = %v, want %v", err, command.ErrChatGone)
	}
}

// unreadableOnce answers its first Get for chatID as unreadable while the record stays on disk.
type unreadableOnce struct {
	*testChatStore
	chatID marotte.ChatID
	failed atomic.Bool
}

func (s *unreadableOnce) Get(ctx context.Context, id marotte.ChatID) (*marotte.Chat, bool) {
	if id == s.chatID && s.failed.CompareAndSwap(false, true) {
		return nil, false
	}
	return s.testChatStore.Get(ctx, id)
}

// A record the spawn could not read is not the chat's absence: the answer is retryable, and the
// next load opens the chat.
func TestLoadSession_AnUnreadableRecordIsRetryable(t *testing.T) {
	cs := newTestChatStore()
	store := &unreadableOnce{testChatStore: cs, chatID: "c1"}
	h := New(context.Background(), t.TempDir(), func() ACPBridge { return newFakeBridge() }, store)
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	cs.seed(t, "c1", func(c *marotte.Chat) { c.Name = "A" })

	if err := h.coord.LoadSession(t.Context(), "c1"); err == nil || errors.Is(err, command.ErrChatGone) {
		t.Fatalf("LoadSession with an unreadable record = %v, want a retryable failure", err)
	}
	if err := h.coord.LoadSession(t.Context(), "c1"); err != nil {
		t.Errorf("LoadSession once the record reads again = %v, want nil", err)
	}
}

// A header the real store cannot stat is a storage fault: the load is retryable, never the
// chat's deletion, and a fresh generation opens the chat once storage recovers.
func TestLoadSession_AnUnstattableHeaderIsRetryable(t *testing.T) {
	cs := newTestChatStore()
	h := New(context.Background(), t.TempDir(), func() ACPBridge { return newFakeBridge() }, cs)
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	cs.seed(t, "c1", func(c *marotte.Chat) { c.Name = "A" })
	dir := filepath.Join(cs.Dir(), "c1")
	if err := os.Rename(dir, dir+".aside"); err != nil {
		t.Fatalf("Setup: move the chat directory aside: %v", err)
	}
	// A file where the directory was: the header's stat fails with ENOTDIR, without root.
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatalf("Setup: file in place of the chat directory: %v", err)
	}

	if err := h.coord.LoadSession(t.Context(), "c1"); err == nil || errors.Is(err, command.ErrChatGone) {
		t.Fatalf("LoadSession with the header unstattable = %v, want a retryable failure", err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatalf("repair: %v", err)
	}
	if err := os.Rename(dir+".aside", dir); err != nil {
		t.Fatalf("repair: %v", err)
	}
	if err := h.coord.LoadSession(t.Context(), "c1"); err != nil {
		t.Errorf("LoadSession once storage recovered = %v, want nil", err)
	}
}

// deletingAfterPresence removes chatID right after a lock-free Presence read answers present,
// once armed: the window a readiness verdict taken outside the chat's lock leaves open.
type deletingAfterPresence struct {
	*testChatStore
	chatID marotte.ChatID
	armed  atomic.Bool
}

func (s *deletingAfterPresence) Presence(id marotte.ChatID) chat.Presence {
	p := s.testChatStore.Presence(id)
	if id == s.chatID && p == chat.PresencePresent && s.armed.CompareAndSwap(true, false) {
		if _, err := s.Delete(context.Background(), id); err != nil {
			panic("deletingAfterPresence: " + err.Error())
		}
	}
	return p
}

// A generation is admitted only while its record is present at the publication itself: a delete
// can land before it (the open answers ErrChatGone) or after it, never between the two.
func TestOpenBridge_AdmitsNoBridgeForARecordDeletedBeforeItsReadinessPublished(t *testing.T) {
	cs := newTestChatStore()
	store := &deletingAfterPresence{testChatStore: cs, chatID: "c1"}
	h := New(context.Background(), t.TempDir(), func() ACPBridge { return newFakeBridge() }, store)
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	cs.seed(t, "c1", func(c *marotte.Chat) { c.Name = "A" })
	store.armed.Store(true)

	sb, err := h.coord.openBridge(t.Context(), "c1", "")
	switch {
	case err == nil && cs.Presence("c1") != chat.PresencePresent:
		t.Errorf("OpenBridge(c1) admitted bridge %p, but the record was deleted before its readiness published", sb)
	case err != nil && !errors.Is(err, command.ErrChatGone):
		t.Errorf("OpenBridge(c1) = %v, want a bridge or %v", err, command.ErrChatGone)
	}
}

// refusedAtPublication answers chatID's next publication with verdict once armed, after the
// generation's replay merged: absent deletes the record first, unreadable reports a storage fault.
type refusedAtPublication struct {
	*testChatStore
	chatID  marotte.ChatID
	verdict chat.Presence
	armed   atomic.Bool
}

func (s *refusedAtPublication) PublishIfPresent(ctx context.Context, id marotte.ChatID, publish func()) chat.Presence {
	if id != s.chatID || !s.armed.CompareAndSwap(true, false) {
		return s.testChatStore.PublishIfPresent(ctx, id, publish)
	}
	if s.verdict != chat.PresenceAbsent {
		return s.verdict
	}
	if _, err := s.Delete(context.Background(), id); err != nil {
		panic("refusedAtPublication: " + err.Error())
	}
	return s.testChatStore.PublishIfPresent(ctx, id, publish)
}

// newResumingHub serves resumed chat c1 through store, counting the run resumes its loads fire.
func newResumingHub(t *testing.T, store *refusedAtPublication) (*Runtime, *atomic.Int32) {
	t.Helper()
	h := New(context.Background(), t.TempDir(), func() ACPBridge {
		b := newFakeBridge()
		b.notifsOnStart = []*marotte.RPCResponse{
			replayNotif(t, "user_message_chunk", "ONE", ""),
			replayNotif(t, marotte.ACPUpdateSessionInfo, "", "turn_start"),
			replayNotif(t, marotte.ACPUpdateAgentChunk, "reply", ""),
			replayNotif(t, marotte.ACPUpdateSessionInfo, "", "turn_end"),
		}
		return b
	}, store)
	store.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	loadedChat(t, store.testChatStore, "c1")
	var resumed atomic.Int32
	h.coord.onSessionRehydrated = func(marotte.ChatID) { resumed.Add(1) }
	return h, &resumed
}

// A resumed generation whose replay merged and then failed admission at publication resumes no
// runs: only the readiness it published decides, and it published a failure.
func TestLoadSession_AResumedGenerationRefusedAtPublicationResumesNoRuns(t *testing.T) {
	cases := map[string]struct {
		verdict  chat.Presence
		wantGone bool
	}{
		"deleted":    {verdict: chat.PresenceAbsent, wantGone: true},
		"unreadable": {verdict: chat.PresenceUnreadable},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store := &refusedAtPublication{testChatStore: newTestChatStore(), chatID: "c1", verdict: tc.verdict}
			h, resumed := newResumingHub(t, store)
			store.armed.Store(true)

			err := h.coord.LoadSession(t.Context(), "c1")
			joinInflight(t, h)

			if err == nil || errors.Is(err, command.ErrChatGone) != tc.wantGone {
				t.Errorf("LoadSession(c1) refused at publication = %v, want a failure (chat gone %v)", err, tc.wantGone)
			}
			if n := resumed.Load(); n != 0 {
				t.Errorf("run resumes after a generation refused at publication = %d, want 0", n)
			}
		})
	}
}

// The control for the test above: a resumed generation that published ready resumes its runs once.
func TestLoadSession_AResumedGenerationPublishedReadyResumesItsRuns(t *testing.T) {
	store := &refusedAtPublication{testChatStore: newTestChatStore(), chatID: "c1"}
	h, resumed := newResumingHub(t, store)

	if err := h.coord.LoadSession(t.Context(), "c1"); err != nil {
		t.Fatalf("LoadSession(c1) = %v, want nil", err)
	}
	for deadline := time.Now().Add(5 * time.Second); resumed.Load() == 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("a resumed generation that published ready resumed no runs")
		}
	}
	if n := resumed.Load(); n != 1 {
		t.Errorf("run resumes after one ready load = %d, want 1", n)
	}
}

// generationProbe reports each chat spawn's fake as it reaches Start, after it read the record,
// and parks the spawn after it recorded the new session, until released.
type generationProbe struct {
	*fakeBridge
	entered  chan<- *fakeBridge
	recorded chan<- struct{}
	release  <-chan struct{}
}

func (b generationProbe) Start(ctx context.Context, opts *marotte.StartOpts) error {
	if opts.EnableHooks {
		select {
		case b.entered <- b.fakeBridge:
		default:
		}
	}
	return b.fakeBridge.Start(ctx, opts)
}

func (b generationProbe) SupervisedApplied() bool {
	if b.recorded != nil {
		b.recorded <- struct{}{}
		<-b.release
	}
	return b.fakeBridge.SupervisedApplied()
}

// A header that becomes unstattable after the spawn read it fails the generation retryably at
// publication, never as a success and never as the chat's deletion.
func TestLoadSession_AHeaderUnstattableAtPublicationIsRetryable(t *testing.T) {
	cs := newTestChatStore()
	recorded := make(chan struct{}, 2)
	release := make(chan struct{})
	h := New(context.Background(), t.TempDir(), func() ACPBridge {
		return generationProbe{fakeBridge: newFakeBridge(), recorded: recorded, release: release}
	}, cs)
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	cs.seed(t, "c1", func(c *marotte.Chat) { c.Name = "A" })
	dir := filepath.Join(cs.Dir(), "c1")

	done := make(chan error, 1)
	go func() { done <- h.coord.LoadSession(t.Context(), "c1") }()
	select {
	case <-recorded:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the spawn never recorded its session")
	}
	if err := os.Rename(dir, dir+".aside"); err != nil {
		close(release)
		t.Fatalf("Setup: move the chat directory aside: %v", err)
	}
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		close(release)
		t.Fatalf("Setup: file in place of the chat directory: %v", err)
	}
	close(release)
	if err := <-done; err == nil || errors.Is(err, command.ErrChatGone) {
		t.Fatalf("LoadSession with the header unstattable at publication = %v, want a retryable failure", err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatalf("repair: %v", err)
	}
	if err := os.Rename(dir+".aside", dir); err != nil {
		t.Fatalf("repair: %v", err)
	}
	if err := h.coord.LoadSession(t.Context(), "c1"); err != nil {
		t.Errorf("LoadSession once storage recovered = %v, want nil", err)
	}
}

// A header that stops decoding after the spawn read it fails the generation retryably at
// publication: the stat still finds the file, but no record stands under the readiness.
func TestLoadSession_AHeaderThatStopsDecodingAtPublicationIsRetryable(t *testing.T) {
	cs := newTestChatStore()
	recorded := make(chan struct{}, 2)
	release := make(chan struct{})
	h := New(context.Background(), t.TempDir(), func() ACPBridge {
		return generationProbe{fakeBridge: newFakeBridge(), recorded: recorded, release: release}
	}, cs)
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	cs.seed(t, "c1", func(c *marotte.Chat) { c.Name = "A" })
	header := filepath.Join(cs.Dir(), "c1", "chat.json")

	done := make(chan error, 1)
	go func() { done <- h.coord.LoadSession(t.Context(), "c1") }()
	select {
	case <-recorded:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the spawn never recorded its session")
	}
	good, err := os.ReadFile(header)
	if err == nil {
		err = os.WriteFile(header, []byte("{"), 0o600)
	}
	close(release)
	if err != nil {
		t.Fatalf("Setup: malformed header: %v", err)
	}
	if err := <-done; err == nil || errors.Is(err, command.ErrChatGone) {
		t.Fatalf("LoadSession with the header malformed at publication = %v, want a retryable failure", err)
	}
	if err := os.WriteFile(header, good, 0o600); err != nil {
		t.Fatalf("repair: %v", err)
	}
	if err := h.coord.LoadSession(t.Context(), "c1"); err != nil {
		t.Errorf("LoadSession once the header decodes again = %v, want nil", err)
	}
}

// A fresh session the record could not take because its header stopped decoding fails the
// generation retryably, removes it, and is reaped: no record holds it, so no delete's chain will.
func TestSpawnGeneration_ASessionAnUnreadableRecordRefusedIsReapedAndRetryable(t *testing.T) {
	cs := newTestChatStore()
	gate := make(chan struct{})
	entered := make(chan *fakeBridge, 2)
	h := New(context.Background(), t.TempDir(), func() ACPBridge {
		b := newFakeBridge()
		b.startGate = gate
		return generationProbe{fakeBridge: b, entered: entered}
	}, cs)
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	var mu sync.Mutex
	var reaped []string
	h.coord.reapSession = func(id string) { mu.Lock(); reaped = append(reaped, id); mu.Unlock() }
	cs.seed(t, "c1", func(c *marotte.Chat) { c.Name = "A" })
	header := filepath.Join(cs.Dir(), "c1", "chat.json")

	done := make(chan error, 1)
	go func() { done <- h.coord.LoadSession(t.Context(), "c1") }()
	var fb *fakeBridge
	select {
	case fb = <-entered:
	case <-time.After(5 * time.Second):
		close(gate)
		t.Fatal("the load started no spawn")
	}
	good, err := os.ReadFile(header)
	if err == nil {
		err = os.WriteFile(header, []byte("{"), 0o600)
	}
	if err != nil {
		close(gate)
		t.Fatalf("Setup: malformed header: %v", err)
	}
	close(gate)
	if err := <-done; err == nil || errors.Is(err, command.ErrChatGone) {
		t.Fatalf("LoadSession whose record could not take the new session = %v, want a retryable failure", err)
	}
	joinInflight(t, h)
	if sb := h.coord.bridgeFor("c1"); sb != nil {
		t.Errorf("Bridge(c1) after the failed generation = %p, want none: the next open must start fresh", sb)
	}
	mu.Lock()
	if want := []string{string(fb.SessionID())}; !slices.Equal(reaped, want) {
		t.Errorf("sessions the generation reaped = %v, want its unrecorded %v", reaped, want)
	}
	mu.Unlock()
	if err := os.WriteFile(header, good, 0o600); err != nil {
		t.Fatalf("repair: %v", err)
	}
	if err := h.coord.LoadSession(t.Context(), "c1"); err != nil {
		t.Errorf("LoadSession once the header decodes again = %v, want nil", err)
	}
}

// A fresh session a removed record refused is in no delete's chain, so its own generation
// reaps it; one the record took before the delete is the teardown's, through that chain.
func TestSpawnGeneration_ReapsOnlyTheFreshSessionNoRecordHolds(t *testing.T) {
	removals := map[string]func(cs *testChatStore) error{
		"refused by the deleted record": func(cs *testChatStore) error {
			_, err := cs.Delete(context.Background(), "c1")
			return err
		},
		// No tombstone: the record write finds no header and declines rather than refusing.
		"refused by a record gone from disk": func(cs *testChatStore) error {
			return os.RemoveAll(filepath.Join(cs.Dir(), "c1"))
		},
	}
	for name, remove := range removals {
		t.Run(name, func(t *testing.T) { testReapsTheSessionARemovedRecordRefused(t, remove) })
	}

	t.Run("recorded before the delete", func(t *testing.T) {
		cs := newTestChatStore()
		entered := make(chan *fakeBridge, 1)
		recorded := make(chan struct{}, 1)
		release := make(chan struct{})
		h := New(context.Background(), t.TempDir(), func() ACPBridge {
			return generationProbe{fakeBridge: newFakeBridge(), entered: entered, recorded: recorded, release: release}
		}, cs)
		cs.wire(h)
		t.Cleanup(func() { shutdownHub(t, h) })
		var mu sync.Mutex
		var reaped []string
		h.coord.reapSession = func(id string) { mu.Lock(); reaped = append(reaped, id); mu.Unlock() }
		cs.seed(t, "c1", func(c *marotte.Chat) { c.Name = "A" })

		done := make(chan error, 1)
		go func() { done <- h.coord.LoadSession(t.Context(), "c1") }()
		var fb *fakeBridge
		select {
		case fb = <-entered:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("the load started no spawn")
		}
		select {
		case <-recorded:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("the spawn never recorded its session")
		}
		chain, err := cs.Delete(t.Context(), "c1")
		close(release)
		if err != nil {
			t.Fatalf("Delete(c1) = %v", err)
		}
		if sid := string(fb.SessionID()); !slices.Contains(chain, sid) {
			t.Errorf("Delete(c1) chain = %v, want the recorded session %s for the teardown to remove", chain, sid)
		}
		if err := <-done; !errors.Is(err, command.ErrChatGone) {
			t.Fatalf("LoadSession of a chat deleted before its readiness = %v, want %v", err, command.ErrChatGone)
		}
		joinInflight(t, h)
		mu.Lock()
		defer mu.Unlock()
		if len(reaped) != 0 {
			t.Errorf("sessions the generation reaped = %v, want none: the delete's chain owns a recorded one", reaped)
		}
	})
}

func testReapsTheSessionARemovedRecordRefused(t *testing.T, remove func(cs *testChatStore) error) {
	t.Helper()
	cs := newTestChatStore()
	gate := make(chan struct{})
	entered := make(chan *fakeBridge, 1)
	h := New(context.Background(), t.TempDir(), func() ACPBridge {
		b := newFakeBridge()
		b.startGate = gate
		return generationProbe{fakeBridge: b, entered: entered}
	}, cs)
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	var mu sync.Mutex
	var reaped []string
	h.coord.reapSession = func(id string) { mu.Lock(); reaped = append(reaped, id); mu.Unlock() }
	cs.seed(t, "c1", func(c *marotte.Chat) { c.Name = "A" })

	done := make(chan error, 1)
	go func() { done <- h.coord.LoadSession(t.Context(), "c1") }()
	var fb *fakeBridge
	select {
	case fb = <-entered:
	case <-time.After(5 * time.Second):
		close(gate)
		t.Fatal("the load started no spawn")
	}
	if err := remove(cs); err != nil {
		close(gate)
		t.Fatalf("Setup: remove c1: %v", err)
	}
	close(gate)
	if err := <-done; !errors.Is(err, command.ErrChatGone) {
		t.Fatalf("LoadSession of a chat removed mid-spawn = %v, want %v", err, command.ErrChatGone)
	}
	joinInflight(t, h)
	mu.Lock()
	defer mu.Unlock()
	if want := []string{string(fb.SessionID())}; !slices.Equal(reaped, want) {
		t.Errorf("sessions the generation reaped = %v, want its unrecorded %v", reaped, want)
	}
}

// enteringBridge reports each chat spawn as it reaches Start, before the fake's gate parks it. The
// utility bridge, which shares the factory, opts out of hooks and is not reported.
type enteringBridge struct {
	*fakeBridge
	entered chan<- struct{}
}

func (b enteringBridge) Start(ctx context.Context, opts *marotte.StartOpts) error {
	if opts.EnableHooks {
		select {
		case b.entered <- struct{}{}:
		default:
		}
	}
	return b.fakeBridge.Start(ctx, opts)
}

// A parent deleted while a tangent's recovery is loading its session, after the spawn read the
// record and before it settled, is the terminal answer: the merge fails once and no bridge survives.
func TestTangentMerge_AParentDeletedWhileItsSessionLoadsFailsTheMergeOnce(t *testing.T) {
	seeds := map[string]func(c *marotte.Chat){
		"a resumed parent": func(c *marotte.Chat) { c.RecordSession("old-acp") },
		"a fresh parent":   func(*marotte.Chat) {},
	}
	for name, seed := range seeds {
		t.Run(name, func(t *testing.T) {
			cs := newTestChatStore()
			gate := make(chan struct{})
			entered := make(chan struct{}, 8)
			h := New(context.Background(), t.TempDir(), func() ACPBridge {
				b := newFakeBridge()
				b.startGate = gate
				return enteringBridge{fakeBridge: b, entered: entered}
			}, cs)
			cs.wire(h)
			t.Cleanup(func() { shutdownHub(t, h) })
			if _, err := cs.Mutate(t.Context(), "c-parent", func(c *marotte.Chat, _ bool) bool {
				c.Name = "parent"
				seed(c)
				return true
			}); err != nil {
				t.Fatalf("Setup: seed the parent: %v", err)
			}
			if _, err := cs.Mutate(t.Context(), "c-tangent", func(c *marotte.Chat, _ bool) bool {
				c.Name = "tangent"
				c.TangentMerges = []marotte.TangentMerge{{OpID: "op-1", Parent: "c-parent", DeliveryID: "msg-1", State: marotte.TangentMergeRunning}}
				return true
			}); err != nil {
				t.Fatalf("Setup: seed the running merge: %v", err)
			}

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/chats/c-tangent/merges/op-1", http.NoBody)
			req.SetPathValue("id", "c-tangent")
			req.SetPathValue("op", "op-1")
			h.dispatcher.ServeMergeStatus(httptest.NewRecorder(), req)
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				close(gate)
				t.Fatal("the merge status read started no load of the parent's session")
			}

			deleted := make(chan error, 1)
			go func() { deleted <- h.membership.DeleteChatAndCloseTabs(context.Background(), "c-parent", "op-del") }()
			for deadline := time.Now().Add(5 * time.Second); cs.Presence("c-parent") != chat.PresenceAbsent; time.Sleep(5 * time.Millisecond) {
				if time.Now().After(deadline) {
					close(gate)
					t.Fatal("the parent's record was not removed while its spawn was held")
				}
			}
			close(gate)
			select {
			case err := <-deleted:
				if err != nil {
					t.Fatalf("DeleteChatAndCloseTabs(c-parent) = %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("DeleteChatAndCloseTabs(c-parent) never returned")
			}

			var state marotte.TangentMergeState
			for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
				c, _ := cs.Get(t.Context(), "c-tangent")
				if state = c.TangentMerges[0].State; state != marotte.TangentMergeRunning || time.Now().After(deadline) {
					break
				}
			}
			if state != marotte.TangentMergeFailed {
				t.Errorf("merge state after its parent was deleted mid-load = %q, want %q", state, marotte.TangentMergeFailed)
			}
			// conclude records the state before it broadcasts, so the frame can trail the state read above.
			countFailures := func() (n int) {
				for _, p := range errorPayloadsSince(t, h, 0) {
					if p.Code == marotte.ErrCodeTangentMergeFailed && p.OpID == "op-1" {
						n++
					}
				}
				return n
			}
			for deadline := time.Now().Add(5 * time.Second); countFailures() == 0 && time.Now().Before(deadline); {
				time.Sleep(5 * time.Millisecond)
			}
			joinInflight(t, h)
			if failures := countFailures(); failures != 1 {
				t.Errorf("tangent merge failure frames = %d, want 1", failures)
			}
			if sb := h.coord.bridgeFor("c-parent"); sb != nil {
				t.Error("a bridge is still registered for the deleted parent")
			}
		})
	}
}
