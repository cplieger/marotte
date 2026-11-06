package agent

import (
	"fmt"
	"maps"
	"strings"
	"sync"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/trigger"
)

// fakeConfirmer records every TriggerConfirmer call as one line, in order, and answers from its fields. live maps an
// event key to the run its live attempt names, set by NameAttempt; a ConfirmEvent naming that run moves the key to
// launched, as the attempt table's run_start row does.
type fakeConfirmer struct {
	nameErr error
	// onName runs inside NameAttempt, before it returns.
	onName     func()
	retriedErr error
	// retriedEvent is Retried's live attempt event.
	retriedEvent *trigger.RetriedEvent
	bootHeld     map[string]bool
	live         map[string]string
	pruned       []map[string]marotte.RunStatus
	calls        []string
	launched     []string
	mu           sync.Mutex
	retried      bool
}

var _ TriggerConfirmer = (*fakeConfirmer)(nil)

func (f *fakeConfirmer) record(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

func (f *fakeConfirmer) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeConfirmer) NameAttempt(ruleID, eventKey, workflowID string) error {
	f.record("NameAttempt %s %s %s", ruleID, eventKey, workflowID)
	if f.onName != nil {
		f.onName()
	}
	if f.nameErr != nil {
		return f.nameErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.live == nil {
		f.live = map[string]string{}
	}
	f.live[eventKey] = workflowID
	return nil
}

func (f *fakeConfirmer) ConfirmEvent(ruleID, eventKey, workflowID string) {
	f.record("ConfirmEvent %s %s %s", ruleID, eventKey, workflowID)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.live[eventKey] == workflowID {
		delete(f.live, eventKey)
		f.launched = append(f.launched, eventKey)
	}
}

func (f *fakeConfirmer) launchedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.launched...)
}

// count is how many recorded calls start with prefix.
func (f *fakeConfirmer) count(prefix string) int {
	n := 0
	for _, c := range f.recorded() {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeConfirmer) BootHeld(workflowID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bootHeld[workflowID]
}

func (f *fakeConfirmer) PruneAbandoned(ruleID string, statuses map[string]marotte.RunStatus) {
	f.record("PruneAbandoned %s", ruleID)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pruned = append(f.pruned, maps.Clone(statuses))
}

func (f *fakeConfirmer) RuleChanged(ruleID string, change launchrule.Change) {
	f.record("RuleChanged %s %s", ruleID, change)
}

func (f *fakeConfirmer) Retried(workflowID string) (trigger.RetryAnswer, error) {
	f.record("Retried %s", workflowID)
	return trigger.RetryAnswer{HandedBack: f.retried, Event: f.retriedEvent}, f.retriedErr
}

func (f *fakeConfirmer) RecordOutcome(ruleID, workflowID string, outcome launchrule.Outcome) {
	f.record("RecordOutcome %s %s %s %s", ruleID, workflowID, outcome.Status, outcome.Reason)
}

func (f *fakeConfirmer) RecordEnding(ruleID, workflowID string, outcome launchrule.Outcome) {
	f.record("RecordEnding %s %s %s %s", ruleID, workflowID, outcome.Status, outcome.Reason)
}

func (f *fakeConfirmer) DismissGap(ruleID string) error {
	f.record("DismissGap %s", ruleID)
	return nil
}
