package command

import (
	"context"
	"slices"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
)

// stubSteerQueue is a scripted SteerQueue: it routes a steer as itself (or parks
// it while the bridge spawns) and answers each op step from the fields a test
// sets, recording every call in order. The real record's transitions are pinned in
// internal/agent; here the subject is what the commands DO with the answers.
type stubSteerQueue struct {
	removeRes  SteerOpResult
	discardRes SteerOpResult
	// opEnd is the turn end EndOp answers: the op's turn closed under it.
	opEnd      *SteerTurnEnd
	run        func(SteerJob)
	removeRef  string
	discardRef string
	routeRef   string
	flush      []SteerSend
	jobRows    []SteerRow
	resentKeys []string
	calls      []string
	sent       []stubSent
	parked     []string
	cleared    []string
	resentText string
	mu         sync.Mutex
	needsClear bool
	postLoad   bool
	agentRows  bool
	jobGone    bool
	// unfolded makes every read-loop barrier fail.
	unfolded bool
}

type stubSent struct {
	err    error
	send   SteerSend
	queued bool
}

func newStubSteerQueue() *stubSteerQueue { return &stubSteerQueue{} }

// clearingQueue answers every op as one that needs a clear.
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

func (q *stubSteerQueue) RouteSteer(_ marotte.ChatID, key, text string, h SteerHolder) ([]SteerSend, string) {
	q.log("route " + key)
	switch {
	case q.routeRef != "":
		return nil, q.routeRef
	case !h.Held:
		return nil, SteerRefuseNoTurn
	case h.PromptClass && !h.Live:
		q.mu.Lock()
		q.parked = append(q.parked, key)
		q.mu.Unlock()
		return nil, ""
	}
	return []SteerSend{{ID: key, Text: text}}, ""
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

func (q *stubSteerQueue) BeginDiscard(marotte.ChatID, string) (bool, string) {
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

func (q *stubSteerQueue) EndOp(marotte.ChatID, string) *SteerTurnEnd {
	q.log("end-op")
	return q.opEnd
}

func (q *stubSteerQueue) OnSteerJob(run func(SteerJob)) { q.run = run }

func (q *stubSteerQueue) JobRows(marotte.ChatID, string, string) ([]SteerRow, bool) {
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

func (q *stubSteerQueue) Unsent(marotte.ChatID, string) { q.log("unsent") }

func (q *stubSteerQueue) Resent(marotte.ChatID, string, string) ([]string, string) {
	q.log("resent")
	return q.resentKeys, q.resentText
}

func (q *stubSteerQueue) Delivered(marotte.ChatID, string) { q.log("delivered") }

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

func (q *stubSteerQueue) NextParked(marotte.ChatID) (string, string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.parked) == 0 {
		return "", "", false
	}
	key := q.parked[0]
	q.parked = q.parked[1:]
	return key, "parked " + key, true
}

func (q *stubSteerQueue) SetSteerLead(_ marotte.ChatID, key string) func() {
	q.log("lead " + key)
	return func() { q.log("lead-undone " + key) }
}

// steerRolesOf wires a host double and a queue into the steer commands' roles.
func steerRolesOf(host hostDouble, ledger *SteerLedger, q SteerQueue) *promptRoles {
	roles := promptRolesOf(host)
	roles.steers = ledger
	roles.queue = q
	if jobs, ok := q.(SteerJobs); ok {
		roles.jobs = jobs
	}
	return roles
}
