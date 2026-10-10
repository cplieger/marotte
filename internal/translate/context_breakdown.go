package translate

import (
	"cmp"
	"encoding/json"
	"log/slog"
	"math"
	"slices"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
)

const contextBreakdownSchema = 1

// The decoder's own bounds, held whatever KAS's are (12 items, 160-char names at 2.28).
const (
	maxBreakdownCategories = 32
	maxBreakdownItems      = 12
	maxBreakdownParts      = 8
)

type contextBreakdownWire struct {
	Categories map[string]json.RawMessage `json:"categories"`
	Media      *struct {
		Images        float64 `json:"images"`
		ImageBytes    float64 `json:"imageBytes"`
		Documents     float64 `json:"documents"`
		DocumentBytes float64 `json:"documentBytes"`
	} `json:"media"`
	Schema     int     `json:"schema"`
	ModelCalls float64 `json:"modelCalls"`
	TotalChars float64 `json:"totalChars"`
	Compacted  bool    `json:"compacted"`
}

type contextItemWire struct {
	Name      string  `json:"name"`
	URI       string  `json:"uri"`
	Inclusion string  `json:"inclusion"`
	Chars     float64 `json:"chars"`
	Percent   float64 `json:"percent"`
	Count     float64 `json:"count"`
}

// contextBreakdownFrom is the decode door for a turn_completion's contextBreakdown, nil when absent,
// undecodable or of another schema. The window block is not kept: the ring's percentage has its
// own owner. Category keys stay open (an unknown one is kept under its wire key), every numeric
// `*Chars` member beside `chars` becomes a part, and an item's uri survives only as a file: link.
func contextBreakdownFrom(raw json.RawMessage) *marotte.ContextBreakdown {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var w contextBreakdownWire
	if err := json.Unmarshal(raw, &w); err != nil {
		slog.Debug("turn_completion: contextBreakdown did not decode, dropped", "error", err)
		return nil
	}
	if w.Schema != contextBreakdownSchema {
		slog.Debug("turn_completion: contextBreakdown has an unread schema, dropped", "schema", w.Schema)
		return nil
	}
	out := &marotte.ContextBreakdown{
		TotalChars: wholeNumber(w.TotalChars),
		ModelCalls: int(wholeNumber(w.ModelCalls)),
		Compacted:  w.Compacted,
		Categories: []marotte.ContextCategory{},
	}
	for key, body := range w.Categories {
		if c, ok := contextCategoryFrom(key, body); ok {
			out.Categories = append(out.Categories, c)
		}
	}
	slices.SortFunc(out.Categories, func(a, b marotte.ContextCategory) int {
		return cmp.Or(cmp.Compare(b.Chars, a.Chars), cmp.Compare(a.Key, b.Key))
	})
	if len(out.Categories) > maxBreakdownCategories {
		out.Categories = out.Categories[:maxBreakdownCategories]
	}
	if m := w.Media; m != nil && m.Images+m.Documents > 0 {
		out.Media = &marotte.ContextMedia{
			Images:        int(wholeNumber(m.Images)),
			ImageBytes:    wholeNumber(m.ImageBytes),
			Documents:     int(wholeNumber(m.Documents)),
			DocumentBytes: wholeNumber(m.DocumentBytes),
		}
	}
	return out
}

func contextCategoryFrom(key string, body json.RawMessage) (marotte.ContextCategory, bool) {
	var members map[string]json.RawMessage
	if json.Unmarshal(body, &members) != nil {
		return marotte.ContextCategory{}, false
	}
	c := marotte.ContextCategory{
		Key:     displayText(key),
		Chars:   wholeNumber(numberOf(members["chars"])),
		Percent: finite(numberOf(members["percent"])),
		Count:   int(wholeNumber(numberOf(members["count"]))),
	}
	if c.Key == "" || c.Chars <= 0 {
		return marotte.ContextCategory{}, false
	}
	var omitted struct {
		Count float64 `json:"count"`
		Chars float64 `json:"chars"`
	}
	if json.Unmarshal(members["omitted"], &omitted) == nil {
		c.OmittedCount, c.OmittedChars = int(wholeNumber(omitted.Count)), wholeNumber(omitted.Chars)
	}
	var items []contextItemWire
	if json.Unmarshal(members["items"], &items) == nil {
		for i := range items {
			if len(c.Items) == maxBreakdownItems {
				break
			}
			if item, ok := contextItemFrom(&items[i]); ok {
				c.Items = append(c.Items, item)
			}
		}
	}
	c.Parts = contextPartsOf(members)
	return c, true
}

func contextItemFrom(w *contextItemWire) (marotte.ContextItem, bool) {
	name := displayText(w.Name)
	if name == "" {
		return marotte.ContextItem{}, false
	}
	item := marotte.ContextItem{
		Name:      name,
		Inclusion: displayText(w.Inclusion),
		Chars:     wholeNumber(w.Chars),
		Percent:   finite(w.Percent),
		Count:     int(wholeNumber(w.Count)),
	}
	if strings.HasPrefix(w.URI, "file:") && displayText(w.URI) == w.URI {
		item.URI = w.URI
	}
	return item, true
}

// contextPartsOf lists a category's `*Chars` sub-totals by key, so a part KAS adds survives.
func contextPartsOf(members map[string]json.RawMessage) []marotte.ContextPart {
	var parts []marotte.ContextPart
	for key, raw := range members {
		name, isPart := strings.CutSuffix(key, "Chars")
		if !isPart || name == "" {
			continue
		}
		if chars := wholeNumber(numberOf(raw)); chars > 0 {
			parts = append(parts, marotte.ContextPart{Key: displayText(name), Chars: chars})
		}
	}
	slices.SortFunc(parts, func(a, b marotte.ContextPart) int {
		return cmp.Or(cmp.Compare(b.Chars, a.Chars), cmp.Compare(a.Key, b.Key))
	})
	if len(parts) > maxBreakdownParts {
		parts = parts[:maxBreakdownParts]
	}
	return parts
}

func numberOf(raw json.RawMessage) float64 {
	var f float64
	if json.Unmarshal(raw, &f) != nil {
		return 0
	}
	return f
}

func wholeNumber(f float64) int64 {
	if math.IsNaN(f) || f <= 0 {
		return 0
	}
	if f >= math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(f)
}

func finite(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return f
}
