package agent

// Models satisfies models.Snapshotter, giving the git handler a cheap model for commit messages.

import "github.com/cplieger/marotte/internal/marotte"

// Models returns the first non-empty model catalog from a live bridge (all share one set), or nil.
func (rt *Runtime) Models() []marotte.SessionModel {
	snapshot := rt.bridge.mgr.all()
	for _, sb := range snapshot {
		if ms := sb.bridge.Models(); len(ms) > 0 {
			return ms
		}
	}
	return nil
}
