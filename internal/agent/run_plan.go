package agent

import (
	"context"
	"strconv"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

const severityError = "error"

// maxRunPlans bounds the remembered revisions: one per run, kept past the run's end so its tab
// still says what became of the last one.
const maxRunPlans = 256

// runPlans is what KAS does not persist of a run's plan revisions: the top-level step a queued one
// was stamped after, and the last one's settled outcome. KAS keeps the queued revision itself and
// answers it on inspect (servedPlan).
type runPlans struct {
	byRun map[string]marotte.RunPlanUpdate
	order []string
}

// RunPlanUpdate records a queued revision, naming the top-level step it replaces the plan after,
// or settles the queued one. A rejection also reaches the launching chat.
func (rs *Runs) RunPlanUpdate(ctx context.Context, chatID marotte.ChatID, runID string, u marotte.RunPlanUpdate) {
	if u.Outcome == marotte.RunPlanQueued {
		u.After = rs.runningTopStep(runID)
	}
	rs.mu.Lock()
	if prev, ok := rs.plans.byRun[runID]; ok {
		switch {
		case u.Outcome != marotte.RunPlanQueued:
			u.Pending, u.After = prev.Pending, prev.After
		case u.After == "" && prev.Outcome == marotte.RunPlanQueued:
			// KAS replays the queued frame when a client verb loads the run, after its step turns closed.
			u.After = prev.After
		}
	}
	rs.plans.put(runID, u)
	rs.mu.Unlock()
	if u.Outcome == marotte.RunPlanRejected {
		rs.notePlanRejected(ctx, chatID, runID, u.Reason)
	}
}

func (p *runPlans) put(runID string, u marotte.RunPlanUpdate) {
	if p.byRun == nil {
		p.byRun = map[string]marotte.RunPlanUpdate{}
	}
	if _, ok := p.byRun[runID]; !ok {
		p.order = append(p.order, runID)
		if len(p.order) > maxRunPlans {
			delete(p.byRun, p.order[0])
			p.order = p.order[1:]
		}
	}
	p.byRun[runID] = u
}

// servedPlan is a run read's `plan_update`, given the count of top-level steps KAS's inspect says are
// queued (-1 when unknown). KAS is the authority for a queued revision: one it no longer holds was
// applied or dropped, and a settled outcome is served from memory until another is queued.
func (rs *Runs) servedPlan(runID string, pending int) *marotte.RunPlanUpdate {
	rs.mu.Lock()
	mem, ok := rs.plans.byRun[runID]
	rs.mu.Unlock()
	switch {
	case pending > 0:
		u := marotte.RunPlanUpdate{Outcome: marotte.RunPlanQueued, Pending: pending}
		if ok && mem.Outcome == marotte.RunPlanQueued {
			u.After = mem.After
		}
		return &u
	case !ok, pending == 0 && mem.Outcome == marotte.RunPlanQueued:
		return nil
	}
	return &mem
}

// runningTopStep is the top-level step an open step turn sits under: KAS applies a queued
// revision after the top-level step running when it was queued.
func (rs *Runs) runningTopStep(runID string) string {
	if rs.log == nil {
		return ""
	}
	if tops := rs.log.openTops(runID); len(tops) > 0 {
		return tops[0]
	}
	return ""
}

// topStepOf is the top-level step a step's path sits under: the segment after the run's own root.
func topStepOf(step *translate.RunStep) string {
	if len(step.Path) < 2 || step.Path[0] != step.RunID {
		return ""
	}
	return step.Path[1]
}

func planRejectedNoteID(runID string) string {
	return "notify-" + runID + ":plan-rejected:" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

// A parentless run has no chat to tell.
func (rs *Runs) notePlanRejected(ctx context.Context, chatID marotte.ChatID, runID, reason string) {
	if chatID == "" || workflowIDOf(chatID) != "" || rs.coord == nil {
		return
	}
	text := "Plan update rejected"
	if reason != "" {
		text += ": " + reason
	}
	rs.coord.recordSteer(ctx, chatID, planRejectedNoteID(runID), &marotte.EntrySteer{
		Text: text, Origin: marotte.SteerOriginAgent, State: marotte.SteerStateRead,
		Severity: severityError, OriginRun: runID, ProducedTs: time.Now().UnixMilli(),
	})
}
