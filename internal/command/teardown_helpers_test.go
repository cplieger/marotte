package command

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
	"github.com/cplieger/marotte/internal/testsupport"
)

// holdLog records every hold, commit and release a teardown's HoldChat handed out, in order, as "hold c",
// "commit c", "release c".
type holdLog struct {
	// onEvent runs with each event before it is recorded, under no lock of the log's.
	onEvent func(event string)
	events  []string
	mu      sync.Mutex
}

func (l *holdLog) add(event string) {
	if l.onEvent != nil {
		l.onEvent(event)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *holdLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events)
}

// count is how many events equal event.
func (l *holdLog) count(event string) int {
	n := 0
	for _, e := range l.snapshot() {
		if e == event {
			n++
		}
	}
	return n
}

type loggedHold struct {
	log    *holdLog
	chatID marotte.ChatID
}

func (h loggedHold) Commit()  { h.log.add("commit " + string(h.chatID)) }
func (h loggedHold) Release() { h.log.add("release " + string(h.chatID)) }

// holdTeardown is recordingTeardown whose holds are logged.
type holdTeardown struct {
	*recordingTeardown
	log *holdLog
}

func (h holdTeardown) HoldChat(chatID marotte.ChatID) ChatHold {
	h.log.add("hold " + string(chatID))
	return loggedHold{log: h.log, chatID: chatID}
}

// fakeImpact answers a fixed CloseWork per chat and records each read.
type fakeImpact struct {
	// onRead runs at each read, inside the reading caller's critical section.
	onRead func()
	work   map[marotte.ChatID]CloseWork
	reads  [][]marotte.ChatID
	mu     sync.Mutex
}

func (f *fakeImpact) CloseImpact(_ context.Context, chatIDs []marotte.ChatID) map[marotte.ChatID]CloseWork {
	if f.onRead != nil {
		f.onRead()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, slices.Clone(chatIDs))
	out := make(map[marotte.ChatID]CloseWork, len(chatIDs))
	for _, id := range chatIDs {
		out[id] = f.work[id]
	}
	return out
}

func (f *fakeImpact) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reads)
}

// recordingQueue is a teardownQueue that records each job and runs none; done is closed at once.
type recordingQueue struct {
	// onEnqueue runs inside enqueue, so a test can observe the caller's lock.
	onEnqueue func(*teardownJob)
	jobs      []teardownJob
	mu        sync.Mutex
}

func (q *recordingQueue) enqueue(job *teardownJob) (<-chan struct{}, func()) {
	if q.onEnqueue != nil {
		q.onEnqueue(job)
	}
	q.mu.Lock()
	q.jobs = append(q.jobs, *job)
	q.mu.Unlock()
	done := make(chan struct{})
	close(done)
	return done, nil
}

func (q *recordingQueue) snapshot() []teardownJob {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.Clone(q.jobs)
}

// closeHost is a coordinator over a real tab store, an in-memory chat store, logged holds, a fake impact read and
// a recording queue.
type closeHost struct {
	mem    *Membership
	tabs   *tabs.Store
	bus    *tabBus
	store  *testsupport.InMemoryChatStore
	holds  *holdLog
	impact *fakeImpact
	queue  *recordingQueue
}

func newCloseHost(t *testing.T) *closeHost {
	t.Helper()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("open tab store: %v", err)
	}
	h := &closeHost{
		tabs: st, bus: &tabBus{}, store: testsupport.NewInMemoryChatStore(), holds: &holdLog{},
		impact: &fakeImpact{work: map[marotte.ChatID]CloseWork{}}, queue: &recordingQueue{},
	}
	h.mem = newMembership(&membershipDeps{
		Chats: withKeep(h.store), Tabs: st, Bus: h.bus,
		Teardown:  holdTeardown{recordingTeardown: &recordingTeardown{}, log: h.holds},
		Impact:    h.impact,
		Teardowns: h.queue,
	})
	return h
}

// chatUnder creates a chat whose tab sits under parent ("" for top level) and returns the chat id and tab id.
func (h *closeHost) chatUnder(t *testing.T, opID, parent string) (marotte.ChatID, string) {
	t.Helper()
	opened := createChat(t, h.mem, opID)
	if parent != "" {
		if _, _, err := h.mem.reparent(t.Context(), opened.Subject.ID, parent, opID+"-r"); err != nil {
			t.Fatalf("reparent %s under %s: %v", opened.Subject.ID, parent, err)
		}
	}
	return marotte.ChatID(opened.Chat.ID), opened.Subject.ID
}

// chatsWith is the chats whose hold saw verb ("hold", "commit", "release"), in order.
func (l *holdLog) chatsWith(verb string) []string {
	var out []string
	for _, e := range l.snapshot() {
		if rest, ok := strings.CutPrefix(e, verb+" "); ok {
			out = append(out, rest)
		}
	}
	return out
}
