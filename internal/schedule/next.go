package schedule

import "time"

// NextRunFrom is the ONE derivation of a schedule's next run, shared by the runner and the
// REST view. It measures from the anchor (last fire or skip) and floors at notBefore. The
// runner passes a zero notBefore because its sweep must SEE a stale slot to tell "may still
// fire" (inside missGrace) from "missed while down"; the view floors at now.
func NextRunFrom(s Spec, anchor, notBefore time.Time) (time.Time, error) {
	from := anchor
	if notBefore.After(from) {
		from = notBefore
	}
	return nextRun(s, from)
}
