package command

import (
	"context"
	"slices"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
)

// stubSteerQueue is a scripted SteerQueue: it routes a steer as itself (or holds
// it unsent while the session opens) and answers each op step from the fields a test
// sets, recording every call in order. The real record's transitions are pinned in
// internal/agent; here the subject is what the commands DO with the answers.
type stubSteerQueue struct {
	removeRes  SteerOpResult
	discardRes SteerOpResult
	ends       []SteerEnd
	run        func(SteerJob)
	removeRef  string
	discardRef string
	routeRef   string
	flush      []SteerSend
	jobRows    []SteerRow
	calls      []string
	sent       []stubSent
	cleared    []string
	// froms is the caller each RouteSteer named, in call order.
	froms      []*marotte.Caller
	mu         sync.Mutex
	needsClear bool
	postLoad   bool
	agentRows  bool
	jobGone    bool
	// leadGone makes SetSteerLead answer that no live row has the key.
	leadGone bool
	// unfolded makes every read-loop barrier fail.
	unfolded bool
}

type stubSent struct {
	err    error
	send   SteerSend
	queued bool
}

func newStubSteerQueue() *stubSteerQueue { return &stubSteerQueue{} }

func clearingQueue() *stubSteerQueue { return &stubSteerQueue{needsClear: true} }

var (
	_ SteerQueue = (*stubSteerQueue)(nil)
	_ SteerJobs  = (*stubSteerQueue)(nil)
)

func (q *stubSteerQueue) log(s string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.calls = append(q.calls, s)
}

func (q *stubSteerQueue) callLog() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.Clone(q.calls)
}

func (q *stubSteerQueue) LockSteerOps(ctx context.Context, _ marotte.ChatID) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q.log("lock")
	return func() { q.log("unlock") }, nil
}

func (q *stubSteerQueue) AwaitReadLoop(context.Context, marotte.ChatID, uint64) bool {
	q.log("await")
	return !q.unfolded
}

func (q *stubSteerQueue) RouteSteer(_ marotte.ChatID, key, text string, from *marotte.Caller, h SteerHolder) ([]SteerSend, string) {
	q.log("route " + key)
	q.mu.Lock()
	q.froms = append(q.froms, from)
	q.mu.Unlock()
	switch {
	case q.routeRef != "":
		return nil, q.routeRef
	case !h.Held:
		return nil, SteerRefuseNoTurn
	case h.PromptClass && !h.Live:
		return nil, ""
	}
	return []SteerSend{{ID: key, Text: text}}, ""
}

// RouteEntry refuses no_turn with no turn holding admission or no live bridge, as the record does, else routes the
// entry's row as RouteSteer would.
func (q *stubSteerQueue) RouteEntry(_ marotte.ChatID, s *EntrySteer, h SteerHolder) ([]SteerSend, string) {
	if !h.Held || !h.Live {
		q.log("route " + marotte.SteerIDFor(s.Entry))
		return nil, SteerRefuseNoTurn
	}
	return q.RouteSteer("", marotte.SteerIDFor(s.Entry), s.Text, s.Caller, h)
}

func (q *stubSteerQueue) SteerSent(_ marotte.ChatID, s SteerSend, queued bool, err error) {
	q.log("sent " + s.ID)
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sent = append(q.sent, stubSent{send: s, queued: queued, err: err})
}

func (q *stubSteerQueue) BeginRemove(_ marotte.ChatID, key, _ string) (bool, string) {
	q.log("begin-remove " + key)
	return q.needsClear, q.removeRef
}

func (q *stubSteerQueue) BeginQueueInstead(_ marotte.ChatID, key, _ string) (bool, string) {
	q.log("begin-queue " + key)
	return q.needsClear, q.removeRef
}

func (q *stubSteerQueue) RemoveCleared(_ marotte.ChatID, _ string, cleared []string, landed bool) SteerOpResult {
	q.mu.Lock()
	q.cleared = cleared
	q.mu.Unlock()
	if landed {
		q.log("remove-cleared")
	} else {
		q.log("remove-no-reply")
	}
	return q.removeRes
}

func (q *stubSteerQueue) BeginDiscard(_ marotte.ChatID, _ string) (needsClear bool, refuse string) {
	q.log("begin-discard")
	return q.needsClear, q.discardRef
}

func (q *stubSteerQueue) DiscardCleared(_ marotte.ChatID, _ string, landed bool) SteerOpResult {
	if landed {
		q.log("discard-cleared")
	} else {
		q.log("discard-no-reply")
	}
	return q.discardRes
}

func (q *stubSteerQueue) OpSent(_ marotte.ChatID, _ string, s SteerSend, queued bool, err error) {
	q.log("op-sent " + s.ID)
	q.mu.Lock()
	q.sent = append(q.sent, stubSent{send: s, queued: queued, err: err})
	q.mu.Unlock()
}

func (q *stubSteerQueue) EndOp(marotte.ChatID, string) { q.log("end-op") }

func (q *stubSteerQueue) OnSteerJob(run func(SteerJob)) { q.run = run }

func (q *stubSteerQueue) Ends(marotte.ChatID) []SteerEnd {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.Clone(q.ends)
}

func (q *stubSteerQueue) JobRows(marotte.ChatID, string) ([]SteerRow, bool) {
	q.log("job-rows")
	return q.jobRows, q.jobGone
}

func (q *stubSteerQueue) StraysCleared(_ marotte.ChatID, _ string, _ []string, all bool) {
	if all {
		q.log("strays-all")
		return
	}
	q.log("strays")
}

func (q *stubSteerQueue) Release(_ marotte.ChatID, _ string, inKASOnly bool) {
	if inKASOnly {
		q.log("release-in-kas")
		return
	}
	q.log("release")
}

func (*stubSteerQueue) DeathDrained(marotte.ChatID, string) {}

// EndUnsent drops the end, as the record does once its rows are unsent.
func (q *stubSteerQueue) EndUnsent(_ marotte.ChatID, owner string) {
	q.log("end-unsent")
	q.mu.Lock()
	defer q.mu.Unlock()
	q.ends = slices.DeleteFunc(q.ends, func(e SteerEnd) bool { return e.Owner == owner })
}

func (q *stubSteerQueue) PlanFlush(marotte.ChatID) []SteerSend {
	q.log("plan-flush")
	return q.flush
}

func (q *stubSteerQueue) NeedsPostLoadClear(marotte.ChatID) bool { return q.postLoad }

func (q *stubSteerQueue) PostLoadCleared(_ marotte.ChatID, _ []string, landed bool) {
	if landed {
		q.log("post-load-cleared")
	}
}

func (q *stubSteerQueue) AgentRowsWaiting(marotte.ChatID) bool { return q.agentRows }

func (q *stubSteerQueue) SetSteerLead(_ marotte.ChatID, key string) (func(), bool) {
	q.log("lead " + key)
	if q.leadGone {
		return func() {}, false
	}
	return func() { q.log("lead-undone " + key) }, true
}

func steerRolesOf(host hostDouble, ledger *SteerLedger, q SteerQueue) *promptRoles {
	roles := promptRolesOf(host)
	roles.steers = ledger
	roles.queue = q
	if jobs, ok := q.(SteerJobs); ok {
		roles.jobs = jobs
	}
	return roles
}
