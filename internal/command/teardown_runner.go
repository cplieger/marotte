package command

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
)

// teardownGrade is what a committed close owes the chat it took.
type teardownGrade uint8

const (
	// gradeClose keeps the record: a tab close.
	gradeClose teardownGrade = iota
	// gradeCloseDelete is a retention-off tab close that deleted the record.
	gradeCloseDelete
	// gradeDelete is a History delete.
	gradeDelete
	// gradeRetention is the retention purge's delete.
	gradeRetention
)

var gradeNames = [...]string{gradeClose: "close", gradeCloseDelete: "close-delete", gradeDelete: "delete", gradeRetention: "retention"}

func (g teardownGrade) String() string { return gradeNames[g] }

// teardownJob is one chat's teardown, built inside the commit that owes it.
type teardownJob struct {
	// hold is the committed life-gate hold; the job releases it when its grade returned.
	hold ChatHold
	// stop is a person's close's stop hold on the chat's runs, which the close grade's run stop ends; nil leaves
	// the runs running.
	stop   *chat.Claim
	chatID marotte.ChatID
	// chain is the removed record's session chain, for the delete grades.
	chain []string
	// cause is the run stop the delete grades pass on.
	cause RunStopCause
	grade teardownGrade
}

// teardownQueue is the runner as Membership uses it.
type teardownQueue interface {
	enqueue(job *teardownJob) (done <-chan struct{}, runAfterUnlock func())
}

// teardownRunner runs every committed teardown in the background, one job at a time per chat in commit order.
//
// Invariant: Membership calls enqueue only inside the m.mu section of the commit the job tears down, so a chat's
// tail order is its commit order, and enqueue never runs a job, so no job runs under m.mu.
type teardownRunner struct {
	// goBackground runs work on the runtime's lifetime, false once Shutdown began (lifecycleAccess.GoUnlessDraining).
	goBackground func(func()) bool
	// lifetime is the context a job's budget counts under, and its release.
	lifetime func() (context.Context, context.CancelFunc)
	run      func(context.Context, *teardownJob)
	// tails is the newest job's done channel per chat.
	tails map[marotte.ChatID]chan struct{}
	mu    sync.Mutex
}

func newTeardownRunner(life lifecycleAccess, run func(context.Context, *teardownJob)) *teardownRunner {
	r := &teardownRunner{run: run, tails: map[marotte.ChatID]chan struct{}{}}
	if life != nil {
		r.goBackground = life.GoUnlessDraining
		r.lifetime = func() (context.Context, context.CancelFunc) { return life.TurnContext(context.Background()) }
	}
	return r
}

// enqueue puts job behind its chat's newest job and starts it in the background. While the runtime drains (or with
// no lifetime wired) it starts nothing and returns the job as runAfterUnlock, which the caller runs after it
// released m.mu; the job still waits for its predecessor, so commit order holds.
func (r *teardownRunner) enqueue(job *teardownJob) (done <-chan struct{}, runAfterUnlock func()) {
	own := make(chan struct{})
	r.mu.Lock()
	prev := r.tails[job.chatID]
	r.tails[job.chatID] = own
	r.mu.Unlock()
	work := func() {
		defer r.finish(job, own)
		if prev != nil {
			<-prev
		}
		r.runBudgeted(job)
	}
	if r.goBackground != nil && r.goBackground(work) {
		return own, nil
	}
	return own, work
}

// runBudgeted runs job on a budget counted from its own start.
func (r *teardownRunner) runBudgeted(job *teardownJob) {
	base, release := context.Background(), context.CancelFunc(func() {})
	if r.lifetime != nil {
		base, release = r.lifetime()
	}
	defer release()
	ctx, cancel := context.WithTimeout(base, closeTeardownBudget)
	defer cancel()
	r.run(ctx, job)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		slog.Warn("a chat's teardown ran past its budget", "chat_id", job.chatID, "grade", job.grade.String())
	}
}

// finish releases job's hold, then marks it done and drops its tail when no later job replaced it. Deferred, so a
// panicking grade still releases the gate.
func (r *teardownRunner) finish(job *teardownJob, own chan struct{}) {
	if job.hold != nil {
		job.hold.Release()
	}
	r.mu.Lock()
	if r.tails[job.chatID] == own {
		delete(r.tails, job.chatID)
	}
	r.mu.Unlock()
	close(own)
}
