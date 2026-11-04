package command

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// A runner over a fakeLife whose jobs log "start <chat> <n>", wait at their chat's gate, then log "end <chat> <n>".
func newLoggingRunner(t *testing.T, life *fakeLife, log *holdLog, g *gates) *teardownRunner {
	t.Helper()
	var lifeAccess lifecycleAccess
	if life != nil {
		lifeAccess = life
		t.Cleanup(func() { g.openAll(); life.wg.Wait() })
	}
	return newTeardownRunner(lifeAccess, func(_ context.Context, job teardownJob) {
		name := string(job.chatID) + " " + job.grade.String()
		log.add("start " + name)
		g.wait(job.chatID)
		log.add("end " + name)
	})
}

func awaitDone(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not finish within 5s", what)
	}
}

func TestTeardownRunner_RunsOneJobPerChatInCommitOrder(t *testing.T) {
	log, g := &holdLog{}, &gates{}
	r := newLoggingRunner(t, &fakeLife{}, log, g)
	g.park("c")

	r.enqueue(teardownJob{chatID: "c", grade: gradeClose, hold: loggedHold{log: log, chatID: "c"}})
	r.enqueue(teardownJob{chatID: "c", grade: gradeDelete, hold: loggedHold{log: log, chatID: "c"}})
	last, _ := r.enqueue(teardownJob{chatID: "c", grade: gradeRetention, hold: loggedHold{log: log, chatID: "c"}})
	log.awaitEvent(t, "start c close")
	if n := log.count("start c delete"); n != 0 {
		t.Fatal("the second job started while the first ran")
	}
	g.open("c")
	awaitDone(t, last, "the last job")

	want := []string{
		"start c close", "end c close", "release c",
		"start c delete", "end c delete", "release c",
		"start c retention", "end c retention", "release c",
	}
	if got := log.snapshot(); !slices.Equal(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
}

func TestTeardownRunner_RunsOtherChatsAtOnce(t *testing.T) {
	log, g := &holdLog{}, &gates{}
	r := newLoggingRunner(t, &fakeLife{}, log, g)
	g.park("a")

	r.enqueue(teardownJob{chatID: "a", grade: gradeClose})
	done, _ := r.enqueue(teardownJob{chatID: "b", grade: gradeClose})

	awaitDone(t, done, "b's job while a's is parked")
	if log.count("end a close") != 0 {
		t.Error("a's parked job ended before its gate opened")
	}
}

func TestTeardownRunner_HandsADrainingJobBackToRunAfterTheUnlock(t *testing.T) {
	log, g := &holdLog{}, &gates{}
	life := &fakeLife{}
	r := newLoggingRunner(t, life, log, g)
	g.park("c")
	r.enqueue(teardownJob{chatID: "c", grade: gradeClose})
	log.awaitEvent(t, "start c close")
	life.draining.Store(true)

	done, run := r.enqueue(teardownJob{chatID: "c", grade: gradeDelete})

	if run == nil {
		t.Fatal("a draining runner started the job; want it handed back")
	}
	if log.count("start c delete") != 0 {
		t.Fatal("enqueue ran the job itself")
	}
	ran := make(chan struct{})
	go func() { run(); close(ran) }()
	select {
	case <-ran:
		t.Fatal("the handed-back job ran before its predecessor finished")
	case <-time.After(50 * time.Millisecond):
	}
	g.open("c")
	awaitDone(t, ran, "the handed-back job")
	awaitDone(t, done, "the handed-back job's done")
	if !log.before("end c close", "start c delete") {
		t.Errorf("events = %v, want the handed-back job after its predecessor", log.snapshot())
	}
}

// With no lifetime wired every job is handed back, the same way.
func TestTeardownRunner_WithNoLifetimeHandsEveryJobBack(t *testing.T) {
	log, g := &holdLog{}, &gates{}
	r := newLoggingRunner(t, nil, log, g)

	done, run := r.enqueue(teardownJob{chatID: "c", grade: gradeClose})

	if run == nil {
		t.Fatal("an unwired runner started the job")
	}
	run()
	awaitDone(t, done, "the job")
}

func TestTeardownRunner_ReleasesTheHoldWhenAGradePanics(t *testing.T) {
	log := &holdLog{}
	r := newTeardownRunner(nil, func(context.Context, teardownJob) { panic("grade failed") })
	done, run := r.enqueue(teardownJob{chatID: "c", grade: gradeClose, hold: loggedHold{log: log, chatID: "c"}})

	func() {
		defer func() {
			if recover() == nil {
				t.Error("the grade's panic did not reach the caller")
			}
		}()
		run()
	}()

	if got := log.snapshot(); !slices.Equal(got, []string{"release c"}) {
		t.Errorf("events = %v, want the hold released", got)
	}
	awaitDone(t, done, "the panicked job's done")
}

func TestTeardownRunner_BudgetsEachJobFromItsStart(t *testing.T) {
	life := &fakeLife{}
	t.Cleanup(life.wg.Wait)
	type window struct{ start, deadline time.Time }
	var (
		mu      sync.Mutex
		windows = map[teardownGrade]window{}
	)
	r := newTeardownRunner(life, func(ctx context.Context, job teardownJob) {
		start := time.Now()
		deadline, _ := ctx.Deadline()
		mu.Lock()
		windows[job.grade] = window{start, deadline}
		mu.Unlock()
		if job.grade == gradeClose {
			time.Sleep(200 * time.Millisecond) // the first job's own work, which the second must not be charged
		}
	})

	r.enqueue(teardownJob{chatID: "c", grade: gradeClose})
	done, _ := r.enqueue(teardownJob{chatID: "c", grade: gradeDelete})
	awaitDone(t, done, "the second job")

	mu.Lock()
	defer mu.Unlock()
	w := windows[gradeDelete]
	if left := w.deadline.Sub(w.start); left < closeTeardownBudget-50*time.Millisecond {
		t.Errorf("the second job started with %v of its budget left, want the whole %v", left, closeTeardownBudget)
	}
}

// The background job is never run on the committer's goroutine: a grade that takes the commit lock gets it once the
// committer unlocks. Where the job is handed back it runs after the unlock by construction.
func TestTeardownRunner_NeverRunsAJobUnderTheCommitLock(t *testing.T) {
	t.Run("background", func(t *testing.T) {
		var mem *Membership
		got := make(chan bool, 1)
		h := newRunnerHost(t, runnerOpts{})
		mem = h.mem
		chatID, tab := h.chatUnder(t, "op-a", "")
		mem.closeChat = func(context.Context, marotte.ChatID) {
			locked := make(chan struct{})
			go func() { mem.mu.Lock(); close(locked); mem.mu.Unlock() }()
			select {
			case <-locked:
				got <- true
			case <-time.After(time.Second):
				got <- false
			}
		}

		if _, _, err := mem.closeTab(t.Context(), tab, "op-close", nil); err != nil {
			t.Fatal(err)
		}
		select {
		case ok := <-got:
			if !ok {
				t.Errorf("the grade for %s could not take the commit lock within 1s", chatID)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the grade never ran")
		}
	})
	for _, row := range []struct {
		name     string
		nilLife  bool
		draining bool
	}{{name: "draining", draining: true}, {name: "no lifetime", nilLife: true}} {
		t.Run(row.name, func(t *testing.T) {
			h := newRunnerHost(t, runnerOpts{nilLife: row.nilLife})
			h.life.draining.Store(row.draining)
			_, tab := h.chatUnder(t, "op-a", "")
			var free bool
			ran := false
			h.mem.closeChat = func(context.Context, marotte.ChatID) {
				ran = true
				if free = h.mem.mu.TryLock(); free {
					h.mem.mu.Unlock()
				}
			}

			if _, _, err := h.mem.closeTab(t.Context(), tab, "op-close", nil); err != nil {
				t.Fatal(err)
			}
			if !ran {
				t.Fatal("the handed-back job had not run when closeTab returned")
			}
			if !free {
				t.Error("the handed-back job ran under the commit lock")
			}
		})
	}
}
