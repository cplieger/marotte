package command

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
	"github.com/cplieger/marotte/internal/testsupport"
)

// fakeLife is the runtime lifetime as the runner uses it: background work on goroutines a cleanup waits for, until
// draining is set.
type fakeLife struct {
	wg       sync.WaitGroup
	draining atomic.Bool
}

func (l *fakeLife) TurnContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(context.WithoutCancel(ctx))
}
func (*fakeLife) InflightAdd(int)  {}
func (*fakeLife) InflightDone()    {}
func (l *fakeLife) Draining() bool { return l.draining.Load() }
func (l *fakeLife) GoUnlessDraining(fn func()) bool {
	if l.draining.Load() {
		return false
	}
	l.wg.Go(fn)
	return true
}

// gates parks a chat's grades until the test opens its gate.
type gates struct {
	byChat map[marotte.ChatID]chan struct{}
	mu     sync.Mutex
}

// park makes chatID's next grades wait until open(chatID).
func (g *gates) park(chatID marotte.ChatID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.byChat == nil {
		g.byChat = map[marotte.ChatID]chan struct{}{}
	}
	g.byChat[chatID] = make(chan struct{})
}

func (g *gates) open(chatID marotte.ChatID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ch, ok := g.byChat[chatID]; ok {
		close(ch)
		delete(g.byChat, chatID)
	}
}

func (g *gates) openAll() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for id, ch := range g.byChat {
		close(ch)
		delete(g.byChat, id)
	}
}

func (g *gates) wait(chatID marotte.ChatID) {
	g.mu.Lock()
	ch := g.byChat[chatID]
	g.mu.Unlock()
	if ch != nil {
		<-ch
	}
}

// gradeTeardown is a chatTeardown whose holds and delete grades go to one event log: "start <grade> <chat>" when a
// grade begins, then it waits at its chat's gate, then "end <grade> <chat>".
type gradeTeardown struct {
	log    *holdLog
	gates  *gates
	causes sync.Map // chat id -> RunStopCause of its newest by-chain grade
}

func (g *gradeTeardown) grade(name string, chatID marotte.ChatID) {
	g.log.add("start " + name + " " + string(chatID))
	g.gates.wait(chatID)
	g.log.add("end " + name + " " + string(chatID))
}

func (g *gradeTeardown) DeleteChatStateByChain(_ context.Context, chatID marotte.ChatID, _ []string, cause RunStopCause) {
	g.causes.Store(chatID, cause)
	name := "delete"
	if cause == RunStopRetention {
		name = "retention"
	}
	g.grade(name, chatID)
}

func (*gradeTeardown) CloseChatState(context.Context, marotte.ChatID)       {}
func (*gradeTeardown) BeginChatTeardown(marotte.ChatID, bool)               {}
func (*gradeTeardown) AwaitSessionLeases(context.Context, marotte.ChatID)   {}
func (*gradeTeardown) TeardownCancel(context.Context, marotte.ChatID) error { return nil }

func (g *gradeTeardown) HoldChat(chatID marotte.ChatID) ChatHold {
	g.log.add("hold " + string(chatID))
	return loggedHold{log: g.log, chatID: chatID}
}

// runnerHost is a coordinator with the real teardown runner on a fakeLife, logging every hold and grade.
type runnerHost struct {
	mem      *Membership
	tabs     *tabs.Store
	bus      *tabBus
	store    *testsupport.InMemoryChatStore
	log      *holdLog
	gates    *gates
	life     *fakeLife
	teardown *gradeTeardown
}

type runnerOpts struct {
	// wrapTabs wraps the real tab store, given the host's event log; nil uses the store.
	wrapTabs  func(*tabs.Store, *holdLog) TabSet
	retention retentionRead
	impact    closeImpactReader
	nilLife   bool
}

func newRunnerHost(t *testing.T, opts runnerOpts) *runnerHost {
	t.Helper()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("open tab store: %v", err)
	}
	h := &runnerHost{
		tabs: st, bus: &tabBus{}, store: testsupport.NewInMemoryChatStore(), log: &holdLog{}, gates: &gates{},
		life: &fakeLife{},
	}
	h.teardown = &gradeTeardown{log: h.log, gates: h.gates}
	tabSet := TabSet(st)
	if opts.wrapTabs != nil {
		tabSet = opts.wrapTabs(st, h.log)
	}
	deps := &membershipDeps{
		Chats: h.store, Tabs: tabSet, Bus: h.bus, Teardown: h.teardown, Impact: opts.impact,
		CloseChat: func(_ context.Context, chatID marotte.ChatID) { h.teardown.grade("close", chatID) },
		DeleteChat: func(_ context.Context, chatID marotte.ChatID, _ []string, cause RunStopCause) {
			h.teardown.causes.Store(chatID, cause)
			h.teardown.grade("close-delete", chatID)
		},
		Retention: opts.retention,
	}
	if !opts.nilLife {
		deps.Lifecycle = h.life
	}
	h.mem = newMembership(deps)
	t.Cleanup(func() {
		h.gates.openAll()
		h.life.wg.Wait()
	})
	return h
}

func (h *runnerHost) chatUnder(t *testing.T, opID, parent string) (marotte.ChatID, string) {
	t.Helper()
	opened := createChat(t, h.mem, opID)
	if parent != "" {
		if _, _, err := h.mem.reparent(t.Context(), opened.Subject.ID, parent, opID+"-r"); err != nil {
			t.Fatalf("reparent %s under %s: %v", opened.Subject.ID, parent, err)
		}
	}
	return marotte.ChatID(opened.Chat.ID), opened.Subject.ID
}

// awaitEvent waits until the log holds event, failing the test after a few seconds.
func (l *holdLog) awaitEvent(t *testing.T, event string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for l.count(event) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("no %q within 5s; events: %v", event, l.snapshot())
		}
		time.Sleep(time.Millisecond)
	}
}

// indexOf is the position of event in the log, -1 when absent.
func (l *holdLog) indexOf(event string) int {
	for i, e := range l.snapshot() {
		if e == event {
			return i
		}
	}
	return -1
}

// before reports whether a was logged before b, both present.
func (l *holdLog) before(a, b string) bool {
	ia, ib := l.indexOf(a), l.indexOf(b)
	return ia >= 0 && ib >= 0 && ia < ib
}
