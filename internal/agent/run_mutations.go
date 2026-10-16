package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

// mutationKind tells a verb that drives a run (launch, resume, retry, step status, Continue, answer, message, the
// automatic resume) from one that stops it (pause, cancel).
type mutationKind uint8

const (
	mutationDrive mutationKind = iota + 1
	mutationStop
)

const (
	// mutationSettleCeiling is how long an entry waits for settling evidence before one inspect decides it.
	mutationSettleCeiling = 30 * time.Second
	mutationRetryMax      = 5 * time.Minute
)

// runMutations records every write that drives or stops a run until marotte observed its effect. KAS answers invoke
// before the first run_start can be read, an answer before the step's turn_start, a resume possibly before its
// run_start: neither the run clock nor the run log sees those windows, so this registry is quiescence's and the
// interrupted snapshot's third input. Its mutex guards map updates only; no I/O runs under it.
type runMutations struct {
	entries map[*runMutation]struct{}
	// consumed is each lane's consumed position, so a reply whose frames were already read settles at once.
	consumed map[laneRef]uint64
	// covered: the run clock reads the run executing, or its log has an open step turn; executing: the clock alone.
	covered   func(workflowID string) bool
	executing func(workflowID string) bool
	// inspect reads the run once and feeds its status to the run clock.
	inspect func(ctx context.Context, workflowID string) error
	ctx     func() context.Context
	// after arms a ceiling. The runtime's runs it through the drain gate on inflight: inspect drives a run's
	// deadline, which Shutdown must wait for and refuse once it began.
	after    func(d time.Duration, fn func()) *time.Timer
	settled  []func()
	ceiling  time.Duration
	retryMax time.Duration
	live     atomic.Int32
	mu       sync.Mutex
}

// laneRef names a lane: a session's (gen 0: a session lives on one generation), or the engine lane of generation gen.
type laneRef struct {
	session acpResponder
	gen     uint64
}

// runMutation is one write in the registry.
type runMutation struct {
	m          *runMutations
	timer      *time.Timer
	lane       laneRef
	workflowID string
	seq        uint64
	gen        uint64
	retry      time.Duration
	kind       mutationKind
	hasReply   bool
	drained    bool
	done       bool
	warned     bool
}

func newRunMutations() *runMutations {
	return &runMutations{
		entries:  map[*runMutation]struct{}{},
		consumed: map[laneRef]uint64{},
		ceiling:  mutationSettleCeiling,
		retryMax: mutationRetryMax,
		after:    time.AfterFunc,
	}
}

func (m *runMutations) onSettle(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settled = append(m.settled, fn)
}

// enter records a write about to be made to workflowID's run on engine generation gen. Called after every
// precondition and immediately before the write.
func (m *runMutations) enter(workflowID string, kind mutationKind, gen uint64) *runMutation {
	e := &runMutation{m: m, workflowID: workflowID, kind: kind, gen: gen}
	m.mu.Lock()
	m.entries[e] = struct{}{}
	m.live.Add(1)
	m.mu.Unlock()
	return e
}

// unwritten ends a mutation whose write never started.
func (e *runMutation) unwritten() { e.m.settle(e) }

// replied records the write's outcome; seq is the reply's position on lane, the lane the run's frames reach. An
// in-band refusal changed nothing and settles at once; a write with no reply (its ctx ended, or the engine ended)
// keeps the entry, under the ceiling from now.
func (e *runMutation) replied(resp *marotte.RPCResponse, err error, lane laneRef, seq uint64) {
	m := e.m
	if resp != nil && resp.Error != nil {
		m.settle(e)
		return
	}
	m.mu.Lock()
	if e.done {
		m.mu.Unlock()
		return
	}
	if err == nil {
		e.hasReply, e.seq, e.lane = true, seq, lane
		e.drained = m.consumed[lane] >= seq
	}
	m.armLocked(e, m.ceiling)
	evaluate := e.hasReply && e.drained
	m.mu.Unlock()
	if evaluate {
		m.evaluate(e, false)
	}
}

// observeFrame is every lane consumer's report of one consumed frame: its lane position, and the run whose lifecycle
// (or whose step's turn_start) it carried, "" for any other frame.
func (m *runMutations) observeFrame(lane laneRef, seq uint64, workflowID string) {
	if m.live.Load() == 0 {
		m.mu.Lock()
		m.consumed[lane] = seq
		m.mu.Unlock()
		return
	}
	var drained, decided []*runMutation
	m.mu.Lock()
	m.consumed[lane] = seq
	for e := range m.entries {
		if !e.hasReply || e.done || e.lane != lane {
			continue
		}
		switch {
		case workflowID != "" && workflowID == e.workflowID && seq > e.seq:
			decided = append(decided, e)
		case !e.drained && seq >= e.seq:
			e.drained = true
			drained = append(drained, e)
		}
	}
	m.mu.Unlock()
	for _, e := range drained {
		m.evaluate(e, false)
	}
	for _, e := range decided {
		m.evaluate(e, true)
	}
}

// observeSessionFrame is observeFrame for a session lane, whose frames are decoded only while a mutation is live.
func (m *runMutations) observeSessionFrame(
	lane laneRef, seq uint64, msg *marotte.RPCResponse, steps func(marotte.SessionID) (string, bool),
) {
	workflowID := ""
	if m.live.Load() > 0 {
		workflowID = frameWorkflow(msg, steps)
	}
	m.observeFrame(lane, seq, workflowID)
}

// evaluate settles e once the evidence decides it: a drive is settled once the run is covered (the clock or the log
// keeps it in quiescence and in the snapshot) or a later lifecycle frame decided it; a stop once the run is no
// longer executing.
func (m *runMutations) evaluate(e *runMutation, decided bool) {
	switch e.kind {
	case mutationDrive:
		if decided || m.covered(e.workflowID) {
			m.settle(e)
		}
	case mutationStop:
		if !m.executing(e.workflowID) {
			m.settle(e)
		}
	}
}

func (m *runMutations) settle(e *runMutation) {
	m.mu.Lock()
	if e.done {
		m.mu.Unlock()
		return
	}
	e.done = true
	if e.timer != nil {
		e.timer.Stop()
	}
	delete(m.entries, e)
	m.live.Add(-1)
	fns := slices.Clone(m.settled)
	m.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// armLocked starts e's ceiling: one inspect decides an entry no frame settled.
func (m *runMutations) armLocked(e *runMutation, d time.Duration) {
	if e.timer != nil {
		e.timer.Stop()
	}
	e.timer = m.after(d, func() { m.ceilingFired(e) })
}

func (m *runMutations) ceilingFired(e *runMutation) {
	m.mu.Lock()
	if e.done {
		m.mu.Unlock()
		return
	}
	warn := !e.warned
	e.warned = true
	m.mu.Unlock()
	if warn {
		slog.Warn("a run's state stayed unknown after marotte drove it", "workflow_id", e.workflowID)
	}
	ctx := context.Background()
	if m.ctx != nil {
		ctx = m.ctx()
	}
	if ctx.Err() != nil {
		return
	}
	if err := m.inspect(ctx, e.workflowID); err != nil {
		m.mu.Lock()
		if !e.done {
			e.retry = min(max(e.retry*2, m.ceiling), m.retryMax)
			m.armLocked(e, e.retry)
		}
		m.mu.Unlock()
		return
	}
	m.settle(e)
}

// empty reports no mutation in flight or unsettled.
func (m *runMutations) empty() bool { return m.live.Load() == 0 }

// snapshot is generation gen's unsettled mutations, each workflow once: drive ids join the resume set, stop ids
// leave it, since a pause or cancel KAS may not have read must not be undone by a resume.
func (m *runMutations) snapshot(gen uint64) (drive, stop []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, s := map[string]bool{}, map[string]bool{}
	for e := range m.entries {
		if e.gen != gen || e.done {
			continue
		}
		if e.kind == mutationStop {
			s[e.workflowID] = true
		} else {
			d[e.workflowID] = true
		}
	}
	for id := range d {
		drive = append(drive, id)
	}
	for id := range s {
		stop = append(stop, id)
	}
	slices.Sort(drive)
	slices.Sort(stop)
	return drive, stop
}

// forgetLane drops a session lane's consumed position once its forward ended.
func (m *runMutations) forgetLane(lane laneRef) {
	m.mu.Lock()
	delete(m.consumed, lane)
	m.mu.Unlock()
}

// dropGeneration clears a dead generation's entries, so a verb whose reply now fails meets a cleared entry.
func (m *runMutations) dropGeneration(gen uint64) {
	var dropped []*runMutation
	m.mu.Lock()
	for e := range m.entries {
		if e.gen == gen {
			dropped = append(dropped, e)
		}
	}
	for lane := range m.consumed {
		if lane.gen == gen {
			delete(m.consumed, lane)
		}
	}
	m.mu.Unlock()
	for _, e := range dropped {
		m.settle(e)
	}
}

// frameWorkflow is the run a consumed frame decides for: a lifecycle frame's workflowId, or the run of the step
// session whose turn_start it carries; "" otherwise. steps names a step session's run.
func frameWorkflow(msg *marotte.RPCResponse, steps func(marotte.SessionID) (string, bool)) string {
	if msg == nil || msg.ID != nil || len(msg.Params) == 0 {
		return ""
	}
	if strings.HasPrefix(msg.Method, workflowMethodPrefix) {
		var p struct {
			WorkflowID string `json:"workflowId"`
		}
		if json.Unmarshal(msg.Params, &p) != nil {
			return ""
		}
		return p.WorkflowID
	}
	if msg.Method != marotte.MethodSessionUpdate || steps == nil {
		return ""
	}
	var env translate.ACPSessionUpdateEnvelope
	if json.Unmarshal(msg.Params, &env) != nil || env.Update == nil {
		return ""
	}
	var base translate.ACPSessionUpdateBase
	if json.Unmarshal(env.Update, &base) != nil || base.Edge() != translate.TurnEdgeStart {
		return ""
	}
	wf, _ := steps(marotte.SessionID(env.SessionID))
	return wf
}
