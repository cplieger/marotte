package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"sync"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
)

type stepSteerTarget struct {
	workflowID string
	nodePath   string
	// host is the chat whose bridge carries the run: the launching chat, or `run:<id>`.
	host marotte.ChatID
}

// A nil *stepSteers routes nothing.
//
// gate orders a step message's transition against the step's turn ends: the verb decision and its
// delivery, every step `_session/steer` and every step steering turn end take it, so nothing decided
// before an end reaches KAS after it. Its holders never wait on a carrier's read loop or a steer op
// lock, because the ends run on that read loop.
type stepSteers struct {
	recs    *steerRecords
	queue   steerQueue
	ledger  command.SteerRecorder
	targets map[string]stepSteerTarget
	gate    runLocks
	mu      sync.Mutex
}

func newStepSteers(recs *steerRecords, ledger command.SteerRecorder) *stepSteers {
	return &stepSteers{recs: recs, ledger: ledger, targets: make(map[string]stepSteerTarget)}
}

func (s *stepSteers) lock(ctx context.Context, workflowID string) (unlock func(), err error) {
	if s == nil {
		return func() {}, nil
	}
	return s.gate.acquire(ctx, workflowID)
}

func (s *stepSteers) target(key marotte.ChatID) (stepSteerTarget, bool) {
	if s == nil {
		return stepSteerTarget{}, false
	}
	session, ok := key.StepSession()
	if !ok {
		return stepSteerTarget{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.targets[session]
	return t, ok
}

func (s *stepSteers) sessionsAt(workflowID, nodePath string) []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for session, t := range s.targets {
		if t.workflowID == workflowID && (nodePath == "" || t.nodePath == nodePath) {
			out = append(out, session)
		}
	}
	slices.Sort(out)
	return out
}

func (s *stepSteers) bind(workflowID, nodePath, session string, host marotte.ChatID, turnID string) {
	if s == nil || session == "" || nodePath == "" {
		return
	}
	want := stepSteerTarget{workflowID: workflowID, nodePath: nodePath, host: host}
	s.mu.Lock()
	prev, had := s.targets[session]
	s.targets[session] = want
	s.mu.Unlock()
	key := marotte.StepSteerKey(session)
	// KAS re-sends node_start for the execution already bound after a retry wait; a rebind would end
	// its open channel and retire the steers it still holds.
	if had && prev == want && s.recs.boundTo(key, turnID) {
		return
	}
	s.recs.turnBound(key, turnID)
}

// A cancelled ctx still ends, ungated: ending is the safe side.
func (s *stepSteers) lockEnd(ctx context.Context, workflowID string) (unlock func()) {
	unlock, err := s.lock(ctx, workflowID)
	if err != nil {
		return func() {}
	}
	return unlock
}

func (s *stepSteers) end(ctx context.Context, workflowID, nodePath string, forget bool) {
	unlock := s.lockEnd(ctx, workflowID)
	defer unlock()
	s.endLocked(workflowID, nodePath, forget)
}

// The caller holds the run's gate and ends before the step's run turn closes, so each unread row's
// "not read" note lands inside it.
func (s *stepSteers) endLocked(workflowID, nodePath string, forget bool) {
	if s == nil {
		return
	}
	for _, session := range s.sessionsAt(workflowID, nodePath) {
		key := marotte.StepSteerKey(session)
		s.recs.turnEnded(key, command.SteerTurnEnd{})
		if !forget {
			continue
		}
		s.recs.forgetStep(key)
		s.mu.Lock()
		delete(s.targets, session)
		s.mu.Unlock()
	}
}

func (rs *Runs) noteStepSteer(ctx context.Context, key marotte.ChatID, steerID string, steer *marotte.EntrySteer) {
	t, ok := rs.steers.target(key)
	if !ok {
		slog.Debug("a step steer note has no step to land in", "key", key, "steer", steerID)
		return
	}
	rs.RunSteer(ctx, t.workflowID, t.nodePath, steerID, steer)
}

func (s *stepSteers) event(key marotte.ChatID, p *marotte.SteerQueuedPayload) (marotte.ServerEvent, bool) {
	t, ok := s.target(key)
	if !ok {
		return marotte.ServerEvent{}, false
	}
	q := *p
	q.WorkflowID, q.NodePath = t.workflowID, t.nodePath
	return marotte.NewEvent(marotte.EventSteerQueued, "", q), true
}

// steerTarget addresses a step's steers. Its lease is the run's admission of the whole operation, so
// a step's session is mutated only by a caller holding one.
func (s *stepSteers) steerTarget(_ runLease, carrier acpSessionCaller, session string) command.SteerTarget {
	t := command.SteerTarget{Queue: s.queue, Ledger: s.ledger, Key: marotte.StepSteerKey(session)}
	if carrier != nil {
		t.Caller = stepCaller{carrier: carrier, steers: s, session: marotte.SessionID(session)}
	}
	return t
}

var errStepSteerEnded = errors.New("the step's turn ended before the message was sent")

// Every step session is resident on its run's carrier. A `_session/steer` goes out under the run's gate,
// and only while the step's steering turn is bound.
type stepCaller struct {
	carrier acpSessionCaller
	steers  *stepSteers
	session marotte.SessionID
}

var _ command.SessionCaller = stepCaller{}

func (c stepCaller) Call(ctx context.Context, method string, params any) (*marotte.RPCResponse, error) {
	if method != marotte.MethodSessionSteer {
		return c.carrier.Call(ctx, method, params)
	}
	key := marotte.StepSteerKey(string(c.session))
	t, ok := c.steers.target(key)
	if !ok {
		return nil, errStepSteerEnded
	}
	unlock, err := c.steers.lock(ctx, t.workflowID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if !c.steers.recs.stepOpen(key) {
		return nil, errStepSteerEnded
	}
	return c.carrier.Call(ctx, method, params)
}

func (c stepCaller) CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error) {
	return c.carrier.CallAt(ctx, method, params)
}

func (c stepCaller) SessionID() marotte.SessionID { return c.session }

// RunSteer files a step steer's entry in the path's open run turn, else after its newest closed turn.
func (rs *Runs) RunSteer(ctx context.Context, runID, nodePath, steerID string, steer *marotte.EntrySteer) {
	if rs.log == nil {
		return
	}
	ctx = durable.Context(ctx)
	if t := rs.log.turn(runID, nodePath); t != nil {
		sealed, err := t.Steer(ctx, steerID, steer)
		translate.PublishSealed(ctx, rs.bus, "", runID, sealed)
		if err != nil {
			slog.Error("run log: a step steer was not recorded in its turn",
				"run", runID, "node_path", nodePath, "steer", steerID, "error", err)
		}
		return
	}
	raw, err := json.Marshal(steer)
	if err != nil {
		return
	}
	e := &marotte.Entry{Kind: marotte.EntryKindSteer, ID: steerID, Payload: raw}
	if err := rs.RunAppendAfterClosed(ctx, runID, nodePath, e); err != nil {
		slog.Error("run log: a step steer has no turn to land in",
			"run", runID, "node_path", nodePath, "steer", steerID, "error", err)
	}
}

// RunSteerDelivered files a workflow message's take-up in the path's open run turn, else after its newest closed turn.
func (rs *Runs) RunSteerDelivered(ctx context.Context, runID, nodePath string, d *marotte.EntrySteerDelivered) {
	if rs.log == nil {
		return
	}
	ctx = durable.Context(ctx)
	if t := rs.log.turn(runID, nodePath); t != nil {
		sealed, err := t.SteerDelivered(ctx, d)
		translate.PublishSealed(ctx, rs.bus, "", runID, sealed)
		if err != nil {
			slog.Error("run log: a workflow message's take-up was not recorded in its turn",
				"run", runID, "node_path", nodePath, "steer", d.SteerID, "error", err)
		}
		return
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return
	}
	e := &marotte.Entry{Kind: marotte.EntryKindSteerDelivered, ID: marotte.SteerDeliveredID(d.SteerID), Payload: raw}
	if err := rs.RunAppendAfterClosed(ctx, runID, nodePath, e); err != nil {
		slog.Error("run log: a workflow message's take-up has no turn to land in",
			"run", runID, "node_path", nodePath, "steer", d.SteerID, "error", err)
	}
}

// RunWaitingWorkflowMessages answers the run log's workflow messages no take-up has settled, oldest
// first; none for a run with no log.
func (rs *Runs) RunWaitingWorkflowMessages(ctx context.Context, runID string) ([]marotte.WorkflowMessage, error) {
	if rs.log == nil {
		return nil, nil
	}
	l, err := rs.log.log(ctx, runID)
	if err != nil || l == nil {
		return nil, err
	}
	return l.WaitingWorkflowMessages(), nil
}

// RunStepReading reports whether the step session key names is bound to a live execution.
func (rs *Runs) RunStepReading(key marotte.ChatID) bool {
	return rs.steers != nil && rs.steers.recs.stepOpen(key)
}

// RunNodePaused ends the paused step's steering turn: the execution that read its buffer has stopped.
func (rs *Runs) RunNodePaused(ctx context.Context, runID string, path []string) {
	rs.steers.end(ctx, runID, workflow.PathKey(path), false)
}

func (s *stepSteers) runsHostedBy(host marotte.ChatID) []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	runs := map[string]struct{}{}
	for _, t := range s.targets {
		if t.host == host {
			runs[t.workflowID] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(runs))
}
