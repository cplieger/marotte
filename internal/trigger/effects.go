package trigger

import (
	"context"
	"sync"
)

// Effects is what the Confirmer asks of the runs it abandoned. *agent.Runs implements it.
type Effects interface {
	// CancelAbandoned cancels the run; nil only when its own cancel verb returned nil.
	CancelAbandoned(ctx context.Context, workflowID string) error
	// ReleaseTriggered ends marotte's hold on a run whose cancel landed: its lease, bounds and host session.
	ReleaseTriggered(ctx context.Context, workflowID string)
}

type effectKind int

const (
	effectCancel effectKind = iota + 1
	effectRelease
	effectWakeLauncher
)

// attemptEffect is one effect a row returns, run by the Confirmer once the rule's mutex is released.
type attemptEffect struct {
	workflowID string
	kind       effectKind
}

type queuedEffect struct {
	ruleID string
	effect attemptEffect
}

// effectQueue holds the cancels and releases waiting for the effect goroutine. push never blocks: a row's caller
// may own the run the effect is for (agent's runOwners), and the effect would wait for that same owner.
type effectQueue struct {
	signal  chan struct{}
	pending []queuedEffect
	mu      sync.Mutex
}

func newEffectQueue() *effectQueue {
	return &effectQueue{signal: make(chan struct{}, 1)}
}

func (q *effectQueue) push(e queuedEffect) {
	q.mu.Lock()
	q.pending = append(q.pending, e)
	q.mu.Unlock()
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

func (q *effectQueue) take() []queuedEffect {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.pending
	q.pending = nil
	return out
}

// Run runs the queued cancels and releases through fx, one at a time and outside every lock, handing each cancel's
// answer back to step, until ctx ends. Nothing runs before engineReady closes; what is queued before then waits.
func (c *Confirmer) Run(ctx context.Context, engineReady <-chan struct{}, fx Effects) {
	select {
	case <-engineReady:
	case <-ctx.Done():
		return
	}
	for {
		for _, e := range c.effects.take() {
			c.runEffect(ctx, fx, e)
		}
		select {
		case <-c.effects.signal:
		case <-ctx.Done():
			return
		}
	}
}

func (c *Confirmer) runEffect(ctx context.Context, fx Effects, e queuedEffect) {
	switch e.effect.kind {
	case effectCancel:
		ev := attemptEvent{kind: evCancelOK, workflowID: e.effect.workflowID}
		if err := fx.CancelAbandoned(ctx, e.effect.workflowID); err != nil {
			ev.kind, ev.err = evCancelError, err
		}
		c.deliver(e.ruleID, &ev)
	case effectRelease:
		fx.ReleaseTriggered(ctx, e.effect.workflowID)
	}
}

// dispatch runs a row's effects: cancels and releases go to the effect goroutine, a wake to the launcher.
func (c *Confirmer) dispatch(ruleID string, effects []attemptEffect) {
	for _, e := range effects {
		if e.kind == effectWakeLauncher {
			select {
			case c.wake <- struct{}{}:
			default:
			}
			continue
		}
		c.effects.push(queuedEffect{ruleID: ruleID, effect: e})
	}
}
