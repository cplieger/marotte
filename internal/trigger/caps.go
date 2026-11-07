package trigger

import (
	"context"
	"fmt"
	"time"

	"github.com/cplieger/marotte/internal/runlease"
)

// capRecheck is how often a launch held by the live cap looks again: a triggered run ending wakes nothing.
const capRecheck = time.Minute

// fleetCaps holds every event rule's launches together under two caps: the triggered runs live at once, counted
// over the held leases, and the runs launched in any rolling hour. The launcher reads and records it under its
// launch mutex, so two launches never both pass a cap only one fits.
type fleetCaps struct {
	leases Leases
	limits func(ctx context.Context) (live, perHour int)
	// started are the launches that created a run in the last hour, oldest first.
	started []time.Time
}

// capHold is a launch the caps hold: the row's sentence and how long until the caps may let it through.
type capHold struct {
	text string
	wait time.Duration
}

// hold answers whether the caps hold a launch at now. A nil fleetCaps holds nothing.
func (fc *fleetCaps) hold(ctx context.Context, now time.Time) (capHold, bool) {
	if fc == nil {
		return capHold{}, false
	}
	live, perHour := fc.limits(ctx)
	if n := fc.live(); n >= live {
		return capHold{text: fmt.Sprintf("waiting: %s live (the limit)", runsAre(n)), wait: capRecheck}, true
	}
	fc.prune(now)
	if n := len(fc.started); n >= perHour {
		text := fmt.Sprintf("waiting: %s started in the last hour (the limit)", runs(n))
		return capHold{text: text, wait: fc.started[n-perHour].Add(time.Hour).Sub(now)}, true
	}
	return capHold{}, false
}

// launched records a launch that created a run at now.
func (fc *fleetCaps) launched(now time.Time) {
	if fc != nil {
		fc.started = append(fc.started, now)
	}
}

func (fc *fleetCaps) prune(now time.Time) {
	i := 0
	for i < len(fc.started) && !fc.started[i].After(now.Add(-time.Hour)) {
		i++
	}
	fc.started = fc.started[i:]
}

func (fc *fleetCaps) live() int {
	if fc.leases == nil {
		return 0
	}
	n := 0
	leases := fc.leases.List()
	for i := range leases {
		if leases[i].Origin == runlease.OriginTriggered {
			n++
		}
	}
	return n
}

func runs(n int) string {
	if n == 1 {
		return "1 triggered run"
	}
	return fmt.Sprintf("%d triggered runs", n)
}

func runsAre(n int) string {
	if n == 1 {
		return "1 triggered run is"
	}
	return runs(n) + " are"
}

// capHeld shows the cap holding the rule's launch on its row.
func (c *Confirmer) capHeld(ruleID, text string) {
	c.update(ruleID, anyGeneration, func(es *eventState) bool {
		if es.raw.Status.RowText == text {
			return false
		}
		es.raw.Status.RowText = text
		return true
	})
}
