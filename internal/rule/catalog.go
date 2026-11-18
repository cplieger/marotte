package rule

import (
	"encoding/json"
	"maps"
	"slices"
)

// NewCatalog answers the catalogue read's body, built from the tables Validate and Eval read; each call answers a
// fresh copy.
func NewCatalog() Catalog {
	w := words
	w.Days, w.DaysPlural = slices.Clone(words.Days), slices.Clone(words.DaysPlural)
	c := Catalog{
		Words:  w,
		Common: slices.Clone(baseFields),
		Rule: Bounds{
			MaxRules: MaxRules, MaxTriggers: maxTriggers, MaxNameRunes: maxNameRunes, MaxPromptBytes: maxPromptBytes,
			MaxLeaves: maxLeaves, MaxGroupDepth: maxGroupDepth, MaxGroupChildren: maxGroupChildren,
			MaxOneOfValues: maxOneOfValues, MaxValueBytes: maxValueBytes,
		},
		Limits: LimitBounds{
			Holds:      []HoldPolicy{HoldFIFO, holdNewest},
			MinPerHour: minPerHour, MaxPerHour: maxPerHour, DefaultPerHour: defaultPerHour, MaxPerDay: maxPerDay,
			MinHoldCap: minHoldCap, MaxHoldCap: maxHoldCap, DefaultHoldCap: defaultHoldCap,
			GapMinutes: int(MinGap.Minutes()), BotMaxPerHour: BotMaxPerHour,
		},
	}
	for _, k := range kindOrder {
		def := kinds[k]
		sample := map[string]any{
			fieldEventID: string(k) + ":sample", fieldTrigger: string(k), fieldRule: "Sample", fieldAt: sampleAt, fieldLateBy: "0s",
		}
		maps.Copy(sample, def.sample)
		// A sample holds only strings and finite numbers, which always encode.
		enc, _ := json.Marshal(sample)
		c.Kinds = append(c.Kinds, KindEntry{
			Kind: k, Group: def.group, Label: def.label, Time: def.time, Sample: enc,
			Phrases: maps.Clone(def.phrases), Fields: slices.Clone(def.fields), Lists: slices.Clone(def.lists),
			Params: cloneParams(def.params),
		})
	}
	for _, k := range leafOrder {
		def := leafDefs[k]
		c.Leaves = append(c.Leaves, LeafEntry{
			Kind: k, Group: def.group, Phrases: maps.Clone(def.phrases), Params: cloneParams(def.params),
		})
	}
	for _, d := range opDefs {
		c.Ops = append(c.Ops, OpEntry{Op: d.op, Phrase: d.phrase, Negative: d.negative, Value: d.value, List: d.list})
	}
	return c
}

func cloneParams(ps []Param) []Param {
	if ps == nil {
		return nil
	}
	out := make([]Param, len(ps))
	for i, p := range ps {
		p.Choices = slices.Clone(p.Choices)
		p.Named = maps.Clone(p.Named)
		out[i] = p
	}
	return out
}
