package agent

import "github.com/cplieger/marotte/internal/marotte"

// noticeRole is the tab set's view of the notice ledger (command.Roles.Notices): set whether or not a push service is
// wired, because a chat tab's last close also drops the coordinator's parked finish.
type noticeRole struct {
	push  pushService
	coord *bridgeCoordinator
}

func (r noticeRole) Seen(kind marotte.TabKind, ref string, marker uint64) {
	if r.push != nil {
		r.push.Seen(kind, ref, marker)
	}
}

func (r noticeRole) TabClosed(kind marotte.TabKind, ref string) {
	if r.push != nil {
		r.push.TabClosed(kind, ref)
	}
	if kind == marotte.TabKindChat {
		r.coord.dropFinish(marotte.ChatID(ref))
	}
}
