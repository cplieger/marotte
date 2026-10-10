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

// In memory, as Kiro's run view keeps it: KAS persists no revision.
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
	if prev, ok := rs.plans.byRun[runID]; ok && u.Outcome != marotte.RunPlanQueued {
		u.Pending, u.After = prev.Pending, prev.After
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

func (rs *Runs) planUpdate(runID string) (marotte.RunPlanUpdate, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	u, ok := rs.plans.byRun[runID]
	return u, ok
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
