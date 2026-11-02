package agent

import (
	"slices"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

// chatHoldsLiveRun reports whether any of leases belongs to a run chatID launched, asked before
// a push claims a turn's work is finished. Presence over marotte's own leases: no KAS round
// trip. Bounded() is not consulted: it reads false for a parked or restored run, exactly the
// case the push must not call over. An empty ChatID or chatID matches nothing.
func chatHoldsLiveRun(leases []runlease.Lease, chatID marotte.ChatID) bool {
	if chatID == "" {
		return false
	}
	for i := range leases {
		if leases[i].ChatID != "" && marotte.ChatID(leases[i].ChatID) == chatID {
			return true
		}
	}
	return false
}

// chatHoldsPresentRun is chatHoldsLiveRun without the leases whose run stayed absent past the lease budget (absent),
// for the idle-bridge release alone: a bridge pinned forever by a run KAS cannot show is the memory that budget bounds.
func chatHoldsPresentRun(leases []runlease.Lease, chatID marotte.ChatID, absent func(workflowID string) bool) bool {
	return chatHoldsLiveRun(slices.DeleteFunc(leases, func(l runlease.Lease) bool { return absent(l.WorkflowID) }), chatID)
}
