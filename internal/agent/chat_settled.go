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
	return len(chatLiveRuns(leases, chatID)) > 0
}

// chatLiveRuns is the workflow ids of the leases chatID launched, the runs its close names.
func chatLiveRuns(leases []runlease.Lease, chatID marotte.ChatID) []string {
	if chatID == "" {
		return nil
	}
	var ids []string
	for i := range leases {
		if leases[i].ChatID != "" && marotte.ChatID(leases[i].ChatID) == chatID {
			ids = append(ids, leases[i].WorkflowID)
		}
	}
	return ids
}

// sessionHoldsLiveRun is chatHoldsLiveRun for the runs chatID launched from acpSessionID. A lease that has not recorded
// its launching session yet counts too: it may be this session's, and a new session would cut that run off.
func sessionHoldsLiveRun(leases []runlease.Lease, chatID marotte.ChatID, acpSessionID string) bool {
	return chatID != "" && slices.ContainsFunc(leases, func(l runlease.Lease) bool {
		return marotte.ChatID(l.ChatID) == chatID && (l.ParentSessionID == "" || l.ParentSessionID == acpSessionID)
	})
}
