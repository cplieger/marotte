package agent

import (
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
