package trigger

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/runlease"
)

// Leases is the run-lease store the boot load reads. *runlease.Store implements it.
type Leases interface {
	List() []runlease.Lease
}

var _ Leases = (*runlease.Store)(nil)

// causeBootUnleased and causeBootStale are the abandons of the boot load: a pending record whose lease was never
// written, and a triggered lease on a queued key that no record names.
const (
	causeBootUnleased = "boot_unleased"
	causeBootStale    = "boot_stale"
)

// bootLoad is what row E7 reads besides the rule's own state: the leases on disk, by workflow id, and the state
// file of a rule launch-rules.json no longer holds.
type bootLoad struct {
	leases map[string]runlease.Lease
	orphan *launchrule.State
}

// load is the boot load: row E7 for every event rule and every state file whose rule is gone, before the
// Confirmer answers anyone, so BootHeld and ConfirmEvent answer from the engine's first frame.
func (c *Confirmer) load(leases Leases) {
	byID := map[string]runlease.Lease{}
	if leases != nil {
		ls := leases.List()
		for i := range ls {
			byID[ls[i].WorkflowID] = ls[i]
		}
	}
	for _, r := range c.store.List() {
		if r.When.Event != nil {
			c.deliver(r.ID, &attemptEvent{kind: evBoot, boot: &bootLoad{leases: byID}})
		}
	}
	orphans := c.store.Orphans()
	for id := range orphans {
		st := orphans[id]
		c.deliver(id, &attemptEvent{kind: evBoot, boot: &bootLoad{leases: byID, orphan: &st}})
	}
}

// boot is E7. A rule launch-rules.json no longer holds is E3 over its file's records. Otherwise each record on disk
// takes its boot row (A27 to A30) and each triggered lease naming the rule with no record its own (A31, A36);
// then Q for every queued key with no live record; then E2 when the stored population is the old when's.
func (s *stepper) boot(in *bootLoad) {
	if !s.held {
		if in.orphan != nil {
			s.rv.records = s.loadRecords(in.orphan.Attempts)
		}
		s.deleteRule()
		return
	}
	es := &s.rv.es
	s.rv.records = s.loadRecords(es.raw.Attempts)
	for i := range es.queue {
		es.queue[i].Live = ""
	}
	for _, rec := range slices.Clone(s.rv.records) {
		s.bootRecord(rec, in.leases)
	}
	s.bootLeases(in.leases)
	before := len(es.queue)
	for _, e := range slices.Clone(es.queue) {
		if e.Live == "" {
			s.requeue(e.Key)
		}
	}
	if len(es.queue) != before {
		s.res.write = true
	}
	switch want := launchrule.Population(&s.def); es.raw.Population {
	case want:
	case "":
		es.raw.Population = want
	default:
		s.rebaseline()
	}
}

// bootRecord is the boot row of one record as the file holds it.
func (s *stepper) bootRecord(rec *record, leases map[string]runlease.Lease) {
	switch {
	case rec.state.abandoned():
		if rec.cancelLandedAt.IsZero() { // A29: the cancel is still owed
			rec.cancelsOut++
			s.res.effects = append(s.res.effects, attemptEffect{kind: effectCancel, workflowID: rec.workflowID})
		} // A30: KAS took the cancel
	case hasLease(leases, rec.workflowID): // A27: the run may report on a resume; A7 arms its bound
		s.c.holdAtBoot(rec.workflowID, s.ruleID)
		s.setLive(rec)
	default: // A28: marotte stopped before the lease write, so the invoke was never written
		s.abandon(rec, causeBootUnleased)
		s.requeue(rec.key)
	}
}

// bootLeases is A31 and A36 for each triggered lease naming the rule whose run no record names: a lease on a queued
// key belongs to an earlier attempt, since the record on disk is the newest, and is abandoned; one on a launched
// key is its confirmed run's, which the restart rules decide.
func (s *stepper) bootLeases(leases map[string]runlease.Lease) {
	for _, id := range slices.Sorted(maps.Keys(leases)) {
		l := leases[id]
		if l.Origin != runlease.OriginTriggered || l.RuleID != s.ruleID || s.rv.find(id) != nil {
			continue
		}
		if !slices.ContainsFunc(s.rv.es.queue, func(e queueEntry) bool { return e.Key == l.EventKey }) {
			continue // A36
		}
		rec := &record{workflowID: id, key: l.EventKey, coalesceKey: l.CoalesceKey}
		s.rv.records = append(s.rv.records, rec)
		s.abandon(rec, causeBootStale)
	}
}

// setLive makes rec its queue entry's live attempt.
func (s *stepper) setLive(rec *record) {
	for i := range s.rv.es.queue {
		if s.rv.es.queue[i].Key == rec.key {
			s.rv.es.queue[i].Live = rec.workflowID
		}
	}
}

func hasLease(leases map[string]runlease.Lease, workflowID string) bool {
	_, ok := leases[workflowID]
	return ok
}

// loadRecords is the file's attempts as records: a pending one boot-held until its boot row decides it, an
// abandoned one cancelled once its cancel landed. A list that does not decode blocks the rule and loads none.
func (s *stepper) loadRecords(raw json.RawMessage) []*record {
	if len(raw) == 0 {
		return nil
	}
	var entries []attemptEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		s.status().Blocked = fmt.Sprintf("could not read launch-state/%s.json: attempts: %v", s.ruleID, err)
		return nil
	}
	out := make([]*record, 0, len(entries))
	for _, e := range entries {
		rec := &record{workflowID: e.WorkflowID, key: e.Key, cause: e.Cause, at: e.At, cancelLandedAt: e.CancelLandedAt}
		switch {
		case e.State == persistedPending:
			rec.state = stateBootHeld
		case e.CancelLandedAt.IsZero():
			rec.state = stateAbandonedCancelling
		default:
			rec.state = stateAbandonedCancelled
		}
		if i := slices.IndexFunc(s.rv.es.queue, func(q queueEntry) bool { return q.Key == e.Key }); i >= 0 {
			rec.inputs, rec.coalesceKey = maps.Clone(s.rv.es.queue[i].Inputs), s.rv.es.queue[i].CoalesceKey
		}
		out = append(out, rec)
	}
	return out
}

// holdAtBoot puts the run in the boot-held set, which BootHeld reads and engine ready walks.
func (c *Confirmer) holdAtBoot(workflowID, ruleID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bootHeld[workflowID] = ruleID
}

// armBootHeld delivers engine ready to every boot-held record (A7): its bound starts now, not at the load, since a
// resume needs a started engine.
func (c *Confirmer) armBootHeld() {
	c.mu.Lock()
	held := maps.Clone(c.bootHeld)
	c.mu.Unlock()
	for wf, ruleID := range held {
		c.deliver(ruleID, &attemptEvent{kind: evEngineReady, workflowID: wf})
	}
}

// bootBoundReached is A8: as A6, except that a restart is not the rule's fault, so the bound count does not grow.
func (s *stepper) bootBoundReached(rec *record) {
	s.abandon(rec, causeBound)
	s.requeue(rec.key)
	st := s.status()
	st.RowText = notConfirmedText(rec.workflowID)
	s.failedAttempt(rec, st.RowText)
	s.wakeLauncher()
}
