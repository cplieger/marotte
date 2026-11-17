package rule

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// truncatedField is the block's mark that the bound cut or dropped something; no source may name a field so.
const truncatedField = "truncated"

// The trigger and trigger_name a Run now event carries; it belongs to no trigger of the rule.
const (
	manualTrigger = "manual"
	manualPhrase  = "Run now"
)

// storedEvent is QueuedEvent.Data's layout: the event's bounded fields, and the names the bound cut or dropped.
type storedEvent struct {
	Fields map[string]any `json:"fields"`
	Cut    []string       `json:"cut,omitempty"`
}

// NewEvent bounds a source's event fields into a QueuedEvent's Data. Each string, a list's included,
// is cut to maxStringBytes as the block writes it, counting the "…"; then the largest fields beside baseFields are
// dropped until the block fits maxBlockBytes. A value must be a string, a bool, an int, an int64, a finite float64
// or a []string.
func NewEvent(fields map[string]any) (json.RawMessage, error) {
	norm := make(map[string]any, len(fields))
	var cut []string
	for name, v := range fields {
		if name == truncatedField {
			return nil, fmt.Errorf("event field %q is reserved", name)
		}
		value, wasCut, err := normalise(v)
		if err != nil {
			return nil, fmt.Errorf("event field %q: %w", name, err)
		}
		norm[name] = value
		if wasCut {
			cut = append(cut, name)
		}
	}
	return boundEvent(norm, cut), nil
}

var errUnrenderable = errors.New("not a string, a bool, a finite number or a list of strings")

// SampleEvent bounds a dry run's sample, decoded from JSON, into a QueuedEvent's Data for trigger t: its trigger
// field must name t's kind, and a list arrives as []any whose items must all be strings.
func SampleEvent(t Trigger, sample map[string]any) (json.RawMessage, error) {
	if kind, _ := sample[fieldTrigger].(string); kind != string(t.Kind) {
		return nil, fmt.Errorf("this is not an event of a %s trigger", t.Kind)
	}
	fields := make(map[string]any, len(sample))
	for name, v := range sample {
		list, ok := v.([]any)
		if !ok {
			fields[name] = v
			continue
		}
		strs := make([]string, len(list))
		for i, item := range list {
			if strs[i], ok = item.(string); !ok {
				return nil, fmt.Errorf("event field %q: %w", name, errUnrenderable)
			}
		}
		fields[name] = strs
	}
	return NewEvent(fields)
}

func normalise(v any) (value any, cut bool, err error) {
	switch v := v.(type) {
	case string:
		s, c := cutString(v)
		return s, c, nil
	case bool:
		return v, false, nil
	case []string:
		out := make([]string, len(v))
		for i, s := range v {
			var c bool
			out[i], c = cutString(s)
			cut = cut || c
		}
		return out, cut, nil
	case int:
		return float64(v), false, nil
	case int64:
		return float64(v), false, nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, false, errUnrenderable
		}
		return v, false, nil
	}
	return nil, false, errUnrenderable
}

// cutString makes s valid UTF-8 and cuts it on a rune boundary so its escaped form, "…" included, is at most
// maxStringBytes; a bound on the escaped form keeps the common fields alone under maxBlockBytes.
func cutString(s string) (string, bool) {
	s = strings.ToValidUTF8(s, "\uFFFD")
	var buf [8]byte
	width, keep := 0, 0
	for i, r := range s {
		w := len(appendRune(buf[:0], r))
		if width+w > maxStringBytes {
			return s[:keep] + "…", true
		}
		width += w
		if width <= maxStringBytes-len("…") {
			keep = i + utf8.RuneLen(r)
		}
	}
	return s, false
}

// boundEvent drops the largest fields beside baseFields until the block fits, and writes the stored layout.
func boundEvent(fields map[string]any, cut []string) json.RawMessage {
	for len(encodeBlock(fields, len(cut) > 0)) > maxBlockBytes {
		name, size := "", -1
		for _, k := range slices.Sorted(maps.Keys(fields)) {
			if slices.Contains(baseFields, k) {
				continue
			}
			if s := len(appendValue(nil, fields[k])); s > size {
				name, size = k, s
			}
		}
		if name == "" {
			break
		}
		delete(fields, name)
		if !slices.Contains(cut, name) {
			cut = append(cut, name)
		}
	}
	slices.Sort(cut)
	b := append([]byte(`{"fields":`), encodeBlock(fields, false)...)
	if len(cut) > 0 {
		b = append(b, `,"cut":`...)
		b = appendValue(b, cut)
	}
	return append(b, '}')
}

// DecodeEvent reads a QueuedEvent's Data as conditions compare it: numbers as float64, lists as []string.
func DecodeEvent(data json.RawMessage) (Event, error) {
	var st storedEvent
	if err := json.Unmarshal(data, &st); err != nil {
		return Event{}, fmt.Errorf("event data: %w", err)
	}
	fields := make(map[string]any, len(st.Fields))
	for name, v := range st.Fields {
		switch v := v.(type) {
		case string, float64, bool:
			fields[name] = v
		case []any:
			list := make([]string, len(v))
			for i, item := range v {
				s, ok := item.(string)
				if !ok {
					return Event{}, fmt.Errorf("event field %q: %w", name, errUnrenderable)
				}
				list[i] = s
			}
			fields[name] = list
		default:
			return Event{}, fmt.Errorf("event field %q: %w", name, errUnrenderable)
		}
	}
	delete(fields, truncatedField)
	return Event{Fields: fields, Cut: st.Cut}, nil
}

// TimeEvent is a time trigger's event for the slot an evaluation at now is for, id "<kind>:<trigger id>:<due ms>".
func TimeEvent(spec Spec, t Trigger, slot Slot, now time.Time) QueuedEvent {
	id := string(t.Kind) + ":" + t.ID + ":" + strconv.FormatInt(slot.DueAt.UnixMilli(), 10)
	fields := eventBase(spec, id, string(t.Kind), triggerPhrase(t), now, slot.LateBy)
	fields[fieldDueAt] = slot.DueAt.Format(time.RFC3339)
	if t.Kind == KindEvery && t.Every != nil {
		anchor := t.Every.Anchor
		if anchor == "" {
			anchor = AnchorLast
		}
		fields[fieldAnchor] = string(anchor)
	}
	return QueuedEvent{Stage: StageNew, Trigger: t.ID, EventID: id, Data: stringEvent(fields), SeenAt: now.UnixMilli(), Depth: 1}
}

// ManualEvent is a Run now press, id "manual:<op id>", which belongs to no trigger.
func ManualEvent(spec Spec, opID string, now time.Time) QueuedEvent {
	id := manualTrigger + ":" + opID
	fields := eventBase(spec, id, manualTrigger, manualPhrase, now, 0)
	return QueuedEvent{Stage: StageNew, EventID: id, Data: stringEvent(fields), SeenAt: now.UnixMilli(), Depth: 1}
}

func eventBase(spec Spec, id, trigger, phrase string, now time.Time, late time.Duration) map[string]string {
	return map[string]string{
		fieldEventID: id, fieldTrigger: trigger, fieldTriggerName: phrase, fieldRule: spec.Name,
		fieldAt: now.Format(time.RFC3339), fieldLateBy: late.Round(time.Second).String(),
	}
}

func stringEvent(fields map[string]string) json.RawMessage {
	norm := make(map[string]any, len(fields))
	var cut []string
	for name, v := range fields {
		s, c := cutString(v)
		norm[name] = s
		if c {
			cut = append(cut, name)
		}
	}
	return boundEvent(norm, cut)
}
