package agent

import (
	"cmp"
	"context"
	"slices"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// pendingPair is a switch parked behind a turn: Chat.PendingModel with Chat.PendingEffort.
type pendingPair struct{ model, effort string }

// effortInput is everything a model move's effort depends on (effortInputFor gathers it).
type effortInput struct {
	// capLevels are the tiers KAS caps xhigh and max against while to's thinking is off.
	capLevels []marotte.SessionEffortLevel
	parked    pendingPair
	from, to  string
	// picked is what this gesture names: "" for nothing, a tier, or pickOff (no tier, and a
	// parked one is dropped rather than kept).
	picked string
	// stored is Chat.Effort; seed is to's last_effort_by_model entry, its shape checked.
	stored, seed string
	target       modelTiers
	// thinkingOff is to running with thinking off for this chat unless a pick turns it on;
	// fromThinkingOn is the session in force running with it on, which Off turns off.
	thinkingOff, fromThinkingOn bool
	// admit marks a switch_model command being admitted rather than a move landing.
	admit bool
}

// effortDecision is resolveEffort's whole answer, which every door applies as it stands. An
// admission sets err, else inPlace and either noop (the parked pair stays) or park (an empty one
// clearing it); a landing sets the rest.
type effortDecision struct {
	err  error
	park pendingPair
	// inPlace is the stop the session in force takes before anything parks: a tier (set_effort's
	// work), pickOff (set_thinking off's) or "". pref is the uncapped level the record keeps for
	// to; sent is what KAS is asked to run and the row names. unoffered is a wanted tier to does
	// not run; seedFor the model whose new-chat seed takes pref, "" for none.
	inPlace, from, to, pref, sent, unoffered, seedFor string
	noop, thinkingOn                                  bool
}

// resolveEffort decides the effort of a model move for every door that admits, lands or opens
// one. Pure: the doors gather in (effortInputFor) and apply the answer.
func resolveEffort(in *effortInput) effortDecision {
	pick := in.picked
	switch {
	case pick == pickOff:
		pick = ""
	case pick == "" && in.parked.model == in.to:
		pick = in.parked.effort
	}
	if in.admit {
		return admitSwitch(in, pick)
	}
	return landMove(in, pick)
}

// pickOff is the picked value of the slider's Off stop.
const pickOff = string(marotte.SwitchEffortOff)

// admitSwitch decides a switch_model request whole, its stop included, so one door applies it
// under the lock every pending-pair writer takes and no landing falls between the stop and the park.
func admitSwitch(in *effortInput, pick string) effortDecision {
	inPlace, err := stopInPlace(in)
	if err != nil {
		return effortDecision{err: err}
	}
	d := effortDecision{inPlace: inPlace}
	switch {
	case in.to != in.from:
		d.park = pendingPair{model: in.to, effort: pick}
	// A stop kept on the model in force outranks a switch parked on another (the empty park
	// clears that pair); no stop leaves it parked.
	case in.picked == "" || in.parked.model == "" || in.parked.model == in.to:
		d.noop = true
	}
	return d
}

// stopInPlace is the stop a request applies at once to the session in force: Off turns its
// thinking off, and a tier for the model in force is set unless it already runs there (a retry of
// a landed switch, an unoffered tier's included). A tier that model is known to lack is refused.
func stopInPlace(in *effortInput) (string, error) {
	switch {
	case in.picked == pickOff:
		if in.fromThinkingOn {
			return pickOff, nil
		}
	case in.picked != "" && in.to == in.from:
		landed := landMove(in, in.picked)
		switch {
		case landed.pref == in.stored && !landed.thinkingOn:
		case landed.unoffered != "":
			return "", command.ErrInvalidPayload
		default:
			return in.picked, nil
		}
	}
	return "", nil
}

func landMove(in *effortInput, pick string) effortDecision {
	d := effortDecision{from: in.from, to: in.to}
	want := cmp.Or(pick, in.stored)
	switch in.target.known {
	case tiersNone:
		d.unoffered = want
		return d
	case tiersUnknown:
		d.pref = cmp.Or(want, in.seed)
	case tiersListed:
		switch {
		case slices.Contains(in.target.levels, want):
			d.pref = want
		case slices.Contains(in.target.levels, in.seed):
			d.unoffered, d.pref = want, in.seed
		default:
			d.unoffered, d.pref = want, in.target.def
		}
	}
	if pick != "" && d.pref == pick {
		d.seedFor = in.to
		d.thinkingOn = in.thinkingOff
	}
	d.sent = d.pref
	if in.thinkingOff && !d.thinkingOn {
		d.sent = marotte.CapEffortForThinkingOff(d.pref, in.capLevels)
	}
	return d
}

// effortInputFor gathers a move of rec onto to, picked being the tier picked with it.
func (bc *bridgeCoordinator) effortInputFor(
	ctx context.Context, rec *marotte.Chat, to string, picked marotte.EffortLevel,
) effortInput {
	off, capLevels := bc.thinkingOffFor(rec, to)
	return effortInput{
		capLevels:   capLevels,
		parked:      pendingPair{model: rec.PendingModel, effort: rec.PendingEffort},
		from:        rec.Model,
		to:          to,
		picked:      string(picked),
		stored:      rec.Effort,
		seed:        bc.effortSeedFor(ctx, to),
		target:      bc.catalog.tiersOf(to),
		thinkingOff: off,
		// Not gated on the catalog's toggleable fact: Off is only offered for a toggleable model,
		// and a cold catalog would otherwise leave thinking on under an Off the reader chose.
		fromThinkingOn: !rec.ThinkingIsOff(bc.catalog.thinkingDefaultOff(rec.Model)),
	}
}

// landOn writes a landed move onto the record: the model, the preference (never sent's cap) and a
// thinking choice the pick turned on; it clears the pending pair.
func (d *effortDecision) landOn(c *marotte.Chat) {
	c.MoveToModel(d.to)
	c.PendingModel, c.PendingEffort = "", ""
	c.Effort = d.pref
	if d.thinkingOn {
		c.Thinking = marotte.ThinkingOn
	}
}

// rememberPick writes a landed pick as its model's new-chat seed, as set_effort does.
func (bc *bridgeCoordinator) rememberPick(ctx context.Context, d *effortDecision) {
	if d.seedFor != "" {
		command.RecordEffortSeed(ctx, broadcastFunc(bc.broadcast), bc.lifecycle.configDir, d.seedFor, marotte.EffortLevel(d.pref))
	}
}
