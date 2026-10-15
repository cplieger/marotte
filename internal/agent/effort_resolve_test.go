package agent

import (
	"errors"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// effortOutcome is effortDecision with its error reduced to whether the request was refused.
type effortOutcome struct {
	park                                              pendingPair
	inPlace, from, to, pref, sent, unoffered, seedFor string
	refused, noop, thinkingOn                         bool
}

func outcomeOf(d *effortDecision) effortOutcome {
	return effortOutcome{
		park: d.park, inPlace: d.inPlace, from: d.from, to: d.to, pref: d.pref, sent: d.sent, unoffered: d.unoffered,
		seedFor: d.seedFor, refused: errors.Is(d.err, command.ErrInvalidPayload), noop: d.noop, thinkingOn: d.thinkingOn,
	}
}

// off is the switch effort of the slider's Off stop, as the wire carries it.
const off = "thinking:off"

// offCap is every case's thinking-off cap list: xhigh and max run as high.
var offCap = []marotte.SessionEffortLevel{{ID: "low"}, {ID: "high"}}

// specEffort is the effort rule restated as prose order, written apart from resolveEffort.
func specEffort(in *effortInput) effortOutcome {
	// The tier the gesture asks for: the one picked, else one parked with this very model. Off
	// asks for none, whatever is parked.
	asked := in.picked
	if asked == "" && in.parked.model == in.to {
		asked = in.parked.effort
	}
	if in.picked == off {
		asked = ""
	}
	if !in.admit {
		return specLand(in, asked)
	}
	// The request applies its stop to the session in force before anything parks.
	var stop string
	switch {
	case in.picked == off:
		// Off turns thinking off wherever the switch goes, when it is on.
		if in.fromThinkingOn {
			stop = off
		}
	case in.picked != "" && in.to == in.from:
		// A tier for the model in force is set there, unless it already runs (a retry); one that
		// model lacks cannot be.
		landed := specLand(in, in.picked)
		switch {
		case landed.pref == in.stored && !landed.thinkingOn:
		case landed.unoffered != "":
			return effortOutcome{refused: true}
		default:
			stop = in.picked
		}
	}
	if in.to != in.from {
		return effortOutcome{inPlace: stop, park: pendingPair{model: in.to, effort: asked}}
	}
	if in.picked != "" && in.parked.model != "" && in.parked.model != in.to {
		// A stop kept on the running model cancels the switch parked on another.
		return effortOutcome{inPlace: stop}
	}
	return effortOutcome{inPlace: stop, noop: true}
}

func specLand(in *effortInput, asked string) effortOutcome {
	out := effortOutcome{from: in.from, to: in.to}
	offered := func(level string) bool {
		return in.target.known == tiersListed && level != "" && slices.Contains(in.target.levels, level)
	}
	wanted := asked
	if wanted == "" {
		wanted = in.stored
	}
	switch {
	case in.target.known == tiersNone:
		// KAS runs no tier on it: none is stored or sent, and the wanted one is named.
		out.unoffered = wanted
	case in.target.known == tiersUnknown:
		// Nothing can refuse a level yet: keep the wanted one, else the seed.
		out.pref = wanted
		if out.pref == "" {
			out.pref = in.seed
		}
	case offered(wanted):
		out.pref = wanted
	default:
		out.unoffered = wanted
		out.pref = in.target.def
		if offered(in.seed) {
			out.pref = in.seed
		}
	}
	pickLanded := asked != "" && out.pref == asked
	if pickLanded {
		out.seedFor = in.to
		out.thinkingOn = in.thinkingOff
	}
	out.sent = out.pref
	if in.thinkingOff && !out.thinkingOn && (out.pref == "xhigh" || out.pref == "max") {
		out.sent = "high"
	}
	return out
}

// Every combination of what a move's effort depends on, against the spec.
func TestResolveEffort_MatchesTheSpecOverEveryInput(t *testing.T) {
	targets := map[string]modelTiers{
		"unknown":           {},
		"tierless":          {known: tiersNone},
		"three tiers":       {known: tiersListed, levels: []string{"low", "medium", "high"}, def: "medium"},
		"five tiers":        {known: tiersListed, levels: []string{"low", "medium", "high", "xhigh", "max"}, def: "high"},
		"three, no default": {known: tiersListed, levels: []string{"low", "medium", "high"}},
	}
	picks := []string{"", "low", "high", "max", off}
	stored := []string{"", "medium", "max"}
	seeds := []string{"", "low", "max"}
	parked := map[string]pendingPair{
		"none":          {},
		"this model":    {model: "tgt", effort: "low"},
		"this, no tier": {model: "tgt"},
		"another":       {model: "other", effort: "high"},
	}
	reached := map[string]int{}
	cases := 0
	for tName, target := range targets {
		for _, picked := range picks {
			for _, st := range stored {
				for _, seed := range seeds {
					for pName, pair := range parked {
						for _, thinkingOff := range []bool{false, true} {
							for _, fromOn := range []bool{false, true} {
								for _, sameModel := range []bool{false, true} {
									for _, admit := range []bool{false, true} {
										in := effortInput{
											target: target, capLevels: offCap, parked: pair, from: "cur", to: "tgt",
											picked: picked, stored: st, seed: seed, thinkingOff: thinkingOff,
											fromThinkingOn: fromOn, admit: admit,
										}
										if sameModel {
											in.from = "tgt"
										}
										cases++
										d := resolveEffort(&in)
										got, want := outcomeOf(&d), specEffort(&in)
										if got != want {
											t.Errorf("target %s, picked %q, stored %q, seed %q, parked %s, thinking off %v, from on %v, same model %v, admit %v:\n got  %+v\n want %+v",
												tName, picked, st, seed, pName, thinkingOff, fromOn, sameModel, admit, got, want)
										}
										tally(reached, &in, &got)
									}
								}
							}
						}
					}
				}
			}
		}
	}
	// The space must reach every kind of answer, or a dimension is dead.
	for _, kind := range []string{"refused", "noop", "park", "park with a parked tier", "unoffered", "seed", "thinking on", "capped", "unoffered with no level", "a parked tier dropped", "a parked switch cancelled", "a tier set in place", "thinking turned off in place", "a cancel with its stop"} {
		if reached[kind] == 0 {
			t.Errorf("no case reached %q among %d", kind, cases)
		}
	}
}

func tally(reached map[string]int, in *effortInput, o *effortOutcome) {
	for kind, hit := range map[string]bool{
		"a parked tier dropped":        o.park.model == in.parked.model && in.parked.effort != "" && o.park.effort == "",
		"a parked switch cancelled":    in.admit && !o.refused && !o.noop && o.park == pendingPair{},
		"a tier set in place":          o.inPlace != "" && o.inPlace != off,
		"thinking turned off in place": o.inPlace == off,
		"a cancel with its stop":       in.admit && o.inPlace != "" && !o.noop && o.park == pendingPair{},
		"refused":                      o.refused,
		"noop":                         o.noop,
		"park":                         o.park.model != "",
		"park with a parked tier":      o.park.effort == "low",
		"unoffered":                    o.unoffered != "" && o.pref != "",
		"seed":                         o.seedFor != "",
		"thinking on":                  o.thinkingOn,
		"capped":                       o.sent != o.pref,
		"unoffered with no level":      o.unoffered != "" && o.pref == "" && o.to != "",
	} {
		if hit {
			reached[kind]++
		}
	}
}

// The rules, one explicit row each.
func TestResolveEffort_Rulings(t *testing.T) {
	three := modelTiers{known: tiersListed, levels: []string{"low", "medium", "high"}, def: "medium"}
	tests := []struct {
		name string
		in   effortInput
		want effortOutcome
	}{{
		name: "a cold catalog keeps the chat's carried level",
		in:   effortInput{from: "cur", to: "tgt", stored: "high", seed: "medium"},
		want: effortOutcome{from: "cur", to: "tgt", pref: "high", sent: "high"},
	}, {
		name: "a tierless model names the wanted tier and runs none, its stale seed unread",
		in:   effortInput{target: modelTiers{known: tiersNone}, from: "cur", to: "tgt", stored: "high", seed: "max"},
		want: effortOutcome{from: "cur", to: "tgt", unoffered: "high"},
	}, {
		name: "a tier the model lacks takes its first level and is named",
		in:   effortInput{target: three, from: "cur", to: "tgt", picked: "max", stored: "low", seed: "max"},
		want: effortOutcome{from: "cur", to: "tgt", pref: "medium", sent: "medium", unoffered: "max"},
	}, {
		name: "a retry of a landed switch is already in force",
		in:   effortInput{target: three, from: "tgt", to: "tgt", picked: "high", stored: "high", admit: true},
		want: effortOutcome{noop: true},
	}, {
		name: "a retry of a switch whose tier was not offered is already in force",
		in:   effortInput{target: three, from: "tgt", to: "tgt", picked: "max", stored: "medium", admit: true},
		want: effortOutcome{noop: true},
	}, {
		name: "a tier change on the model in force is set in place",
		in:   effortInput{target: three, from: "tgt", to: "tgt", picked: "high", stored: "low", admit: true},
		want: effortOutcome{inPlace: "high", noop: true},
	}, {
		name: "a tier repeated on the model in force while thinking is off is set, turning it on",
		in: effortInput{
			target: three, from: "tgt", to: "tgt", picked: "medium", stored: "medium", thinkingOff: true, admit: true,
		},
		want: effortOutcome{inPlace: "medium", noop: true},
	}, {
		name: "a tier the model in force lacks is refused",
		in:   effortInput{target: three, from: "tgt", to: "tgt", picked: "max", stored: "low", admit: true},
		want: effortOutcome{refused: true},
	}, {
		name: "the thinking-off cap is sent and never stored",
		in: effortInput{
			target: modelTiers{known: tiersListed, levels: []string{"low", "high", "max"}}, capLevels: offCap,
			from: "cur", to: "tgt", stored: "max", thinkingOff: true,
		},
		want: effortOutcome{from: "cur", to: "tgt", pref: "max", sent: "high"},
	}, {
		name: "a model-only re-pick keeps the tier parked with that model",
		in:   effortInput{target: three, from: "cur", to: "tgt", parked: pendingPair{model: "tgt", effort: "high"}, admit: true},
		want: effortOutcome{park: pendingPair{model: "tgt", effort: "high"}},
	}, {
		name: "Off re-picking the parked model drops its tier",
		in: effortInput{
			target: three, from: "cur", to: "tgt", picked: off,
			parked: pendingPair{model: "tgt", effort: "high"}, thinkingOff: true, admit: true,
		},
		want: effortOutcome{park: pendingPair{model: "tgt"}},
	}, {
		name: "Off on another model lands the chat's level and leaves thinking off",
		in: effortInput{
			target: three, capLevels: offCap, from: "cur", to: "tgt", picked: off,
			stored: "medium", thinkingOff: true,
		},
		want: effortOutcome{from: "cur", to: "tgt", pref: "medium", sent: "medium"},
	}, {
		name: "Off on the model in force with thinking off changes nothing",
		in:   effortInput{target: three, from: "tgt", to: "tgt", picked: off, stored: "low", admit: true},
		want: effortOutcome{noop: true},
	}, {
		name: "Off on the model in force turns its thinking off",
		in: effortInput{
			target: three, from: "tgt", to: "tgt", picked: off, stored: "low", fromThinkingOn: true, admit: true,
		},
		want: effortOutcome{inPlace: off, noop: true},
	}, {
		name: "Off with a switch to another model turns thinking off first and parks no tier",
		in: effortInput{
			target: three, from: "cur", to: "tgt", picked: off, stored: "low", fromThinkingOn: true, admit: true,
		},
		want: effortOutcome{inPlace: off, park: pendingPair{model: "tgt"}},
	}, {
		name: "a tier kept on the model in force cancels a switch parked on another",
		in: effortInput{
			target: three, from: "tgt", to: "tgt", picked: "low", stored: "low",
			parked: pendingPair{model: "other", effort: "high"}, admit: true,
		},
		want: effortOutcome{},
	}, {
		name: "Off kept on the model in force cancels a switch parked on another",
		in: effortInput{
			target: three, from: "tgt", to: "tgt", picked: off, stored: "low",
			parked: pendingPair{model: "other", effort: "high"}, thinkingOff: true, admit: true,
		},
		want: effortOutcome{},
	}, {
		name: "Off turning thinking off on the model in force cancels a switch parked on another",
		in: effortInput{
			target: three, from: "tgt", to: "tgt", picked: off, stored: "low",
			parked: pendingPair{model: "other", effort: "high"}, fromThinkingOn: true, admit: true,
		},
		want: effortOutcome{inPlace: off},
	}, {
		name: "a tier moved on the model in force is set and cancels a switch parked on another",
		in: effortInput{
			target: three, from: "tgt", to: "tgt", picked: "high", stored: "low",
			parked: pendingPair{model: "other", effort: "high"}, admit: true,
		},
		want: effortOutcome{inPlace: "high"},
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := resolveEffort(&tc.in)
			if got := outcomeOf(&d); got != tc.want {
				t.Errorf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}
