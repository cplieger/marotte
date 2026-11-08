package trigger

import (
	"fmt"
	"slices"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// blocker is a run that answered busy for the rule's launch with nothing of marotte's naming it: no rule's record,
// no lease, and not the row's last run.
type blocker struct {
	since      time.Time
	workflowID string
	// warned is set once the run has blocked the rule for launchConfirmBound and its one WARN is logged.
	warned bool
}

// busy is the rule's launch refused because run workflowID holds the recipe. The rule holding the run's record
// decides it (A12, A16, A17); when that is another rule, the launching rule's row then names that rule (A38), and
// when no rule holds one, the launching rule's A24 does. The two steps never run under both rules' mutexes.
func (c *Confirmer) busy(ruleID, workflowID string) {
	ev := attemptEvent{kind: evBusy, workflowID: workflowID}
	if holder := c.otherHolder(ruleID, workflowID); holder != "" {
		c.deliver(holder, &attemptEvent{kind: evBusy, workflowID: workflowID, launchingRule: ruleID})
		ev.heldBy, ev.heldLive = c.holding(holder, workflowID)
	}
	c.deliver(ruleID, &ev)
}

// otherHolder is the rule other than ruleID whose records hold workflowID, empty when ruleID holds it or none does.
func (c *Confirmer) otherHolder(ruleID, workflowID string) string {
	if c.rule(ruleID).holds(workflowID) {
		return ""
	}
	rules := c.loaded()
	ids := make([]string, 0, len(rules))
	for id := range rules {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if id != ruleID && rules[id].holds(workflowID) {
			return id
		}
	}
	return ""
}

func (rv *ruleValue) holds(workflowID string) bool {
	rv.mu.Lock()
	defer rv.mu.Unlock()
	return rv.find(workflowID) != nil
}

// holding is the holder's name for a row, and whether its record of workflowID is live, once its own busy step
// ran; an empty name when the record left meanwhile.
func (c *Confirmer) holding(holder, workflowID string) (name string, live bool) {
	rv := c.rule(holder)
	rv.mu.Lock()
	rec := rv.find(workflowID)
	live = rec != nil && rec.state.live()
	rv.mu.Unlock()
	if rec == nil {
		return "", false
	}
	def, _ := c.store.Get(holder)
	name = def.Name
	if name == "" {
		name = holder
	}
	return name, live
}

// launchEnded is the launcher clearing the key's launching mark; err is the call's when it created no run.
func (c *Confirmer) launchEnded(ruleID, key string, err error) {
	c.deliver(ruleID, &attemptEvent{kind: evLaunchEnded, key: key, err: err})
}

// busyAbandoned is A16 and A17: the rule's abandoned run still holds the recipe, which counts on the 24-hour clock.
// A cancel KAS has not taken is sent again.
func (s *stepper) busyAbandoned(rec *record) {
	st := s.status()
	if rec.state == stateAbandonedCancelling {
		s.cancel(rec)
		st.RowText = fmt.Sprintf("run %s was abandoned by this rule and its cancel has not landed; retrying",
			rec.workflowID)
	} else {
		st.RowText = fmt.Sprintf("run %s was cancelled by this rule and has not ended yet", rec.workflowID)
	}
	s.failedAttempt(rec.workflowID, st.RowText)
}

// busyUnheld is A38 for a run another rule's record holds, else A24. Neither counts, and neither ever cancels: a run
// no record names may be a person's, or a TUI run that has not started.
func (s *stepper) busyUnheld(ev *attemptEvent) {
	st, wf := s.status(), ev.workflowID
	switch {
	case ev.heldBy != "" && ev.heldLive:
		s.rv.blocker = blocker{}
		st.RowText = fmt.Sprintf("waiting for run %s, launched by rule %s", wf, ev.heldBy)
	case ev.heldBy != "":
		s.rv.blocker = blocker{}
		st.RowText = fmt.Sprintf("waiting for run %s, abandoned by rule %s, to be cancelled", wf, ev.heldBy)
	case s.c.leased(wf) || st.LastRunID == wf:
		s.rv.blocker = blocker{}
		st.RowText = fmt.Sprintf("waiting for the running %s run to finish", s.recipe())
	default:
		s.unnamedBlocker(wf)
	}
}

// unnamedBlocker is A24's run nothing names. Once it has blocked the rule for launchConfirmBound the row tells the
// operator, and one WARN pages: marotte cannot tell it from a run it should not touch, so a person decides.
func (s *stepper) unnamedBlocker(workflowID string) {
	b, st := &s.rv.blocker, s.status()
	if b.workflowID != workflowID {
		*b = blocker{workflowID: workflowID, since: s.now}
	}
	if s.now.Sub(b.since) < launchConfirmBound {
		st.RowText = waitingForText(workflowID)
		return
	}
	st.RowText = unnamedBlockingText(workflowID, s.recipe())
	if !b.warned {
		logRunNotStarted(s.ruleID, workflowID, notStartedBlocked, st.RowText)
		b.warned = true
	}
}

// clearGoneBlocker is A34: a run list that no longer shows the blocker clears it and the row text it set.
func (s *stepper) clearGoneBlocker(statuses map[string]marotte.RunStatus) {
	b := s.rv.blocker
	if b.workflowID == "" || listedActive(statuses, b.workflowID) {
		return
	}
	st := s.status()
	if st.RowText == waitingForText(b.workflowID) || st.RowText == unnamedBlockingText(b.workflowID, s.recipe()) {
		st.RowText = ""
	}
	s.rv.blocker = blocker{}
}

// launchEnded is E9: an error from before workflow/new is classed, and a flight that ended with its key still
// queued and no live record takes Q, which a newest rule may resolve by superseding it; otherwise the entry stays
// launchable and the launcher is woken.
func (s *stepper) launchEnded(key string, err error) {
	if err != nil {
		s.launchFailed(err)
	}
	es := &s.rv.es
	i := slices.IndexFunc(es.queue, func(e queueEntry) bool { return e.Key == key })
	if i < 0 || es.queue[i].Live != "" {
		return
	}
	held := len(es.queue)
	s.requeue(key)
	if len(es.queue) != held {
		s.res.write = true
		return
	}
	s.wakeLauncher()
}

// leased reports a lease of any origin naming the run.
func (c *Confirmer) leased(workflowID string) bool {
	if c.leases == nil {
		return false
	}
	_, ok := c.leases.Get(workflowID)
	return ok
}

// recipe is the rule's recipe as its row names it.
func (s *stepper) recipe() string {
	if s.def.Name != "" {
		return s.def.Name
	}
	return s.def.Source
}

func waitingForText(workflowID string) string {
	return fmt.Sprintf("waiting for run %s to finish", workflowID)
}

func unnamedBlockingText(workflowID, recipe string) string {
	return fmt.Sprintf("run %s is blocking %s and this rule has no record of launching it", workflowID, recipe)
}
