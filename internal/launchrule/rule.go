package launchrule

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Rule is one launch rule, the operator's fields only: what the server writes about a rule lives in its state file.
type Rule struct {
	// Inputs are fixed inputs sent at each launch; the launch drops those the recipe does not declare.
	Inputs map[string]string `json:"inputs,omitempty"`
	When   When              `json:"when"`
	// ID is minted by the store on create and never derived from the recipe, so a recipe may own many rules.
	ID     string `json:"id"`
	Source string `json:"source"`
	// Name is the recipe's name at save, for the row once the recipe is gone.
	Name    string `json:"name,omitempty"`
	Model   string `json:"model,omitempty"`
	Effort  string `json:"effort,omitempty"`
	Enabled bool   `json:"enabled"`
}

// When is when a rule launches: exactly one of a clock and a forge event.
type When struct {
	Clock *Spec      `json:"clock,omitempty"`
	Event *EventSpec `json:"event,omitempty"`
}

// ValidationError is a rule the store refuses; its text is one sentence naming the field, for the person who
// sent it.
type ValidationError struct {
	msg string
}

func (e *ValidationError) Error() string { return e.msg }

func invalid(format string, args ...any) error {
	return &ValidationError{msg: fmt.Sprintf(format, args...)}
}

// serverFields are the state file's fields, which a client body never carries.
var serverFields = []string{"status", "population", "cursor", "queue", "launched", "superseded", "attempts"}

// DecodeRule reads a client's rule body. A field the server writes is refused by name, and so is any other
// field a rule does not have, as a *ValidationError.
func DecodeRule(data []byte) (Rule, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var r Rule
	err := dec.Decode(&r)
	if err == nil {
		return r, nil
	}
	if name, ok := unknownField(err); ok {
		if slices.Contains(serverFields, name) {
			return Rule{}, invalid("%s is written by the server, never sent", name)
		}
		return Rule{}, invalid("unknown field %q", name)
	}
	return Rule{}, invalid("invalid launch rule: %v", err)
}

// unknownField is the name in encoding/json's DisallowUnknownFields error, which carries it only in its text.
func unknownField(err error) (string, bool) {
	rest, ok := strings.CutPrefix(err.Error(), `json: unknown field "`)
	if !ok {
		return "", false
	}
	name, ok := strings.CutSuffix(rest, `"`)
	return name, ok
}

const (
	// maxTextBytes bounds every one-line field a rule stores.
	maxTextBytes = 512
	// maxInputValueBytes bounds a fixed input's value; it may span lines.
	maxInputValueBytes = 4096
)

// normalize validates r and fills its event spec's defaults, in place.
func (r *Rule) normalize() error {
	if r.Source == "" {
		return invalid("source is required")
	}
	for _, f := range []struct{ name, value string }{
		{"source", r.Source}, {"name", r.Name}, {"model", r.Model}, {"effort", r.Effort},
	} {
		if err := checkText(f.name, f.value); err != nil {
			return err
		}
	}
	if (r.When.Clock == nil) == (r.When.Event == nil) {
		return invalid("when must hold exactly one of clock and event")
	}
	if r.When.Clock != nil {
		if err := r.When.Clock.Validate(); err != nil {
			return invalid("when.clock: %v", err)
		}
		return checkInputs(r.Inputs, nil)
	}
	if err := r.When.Event.normalize(); err != nil {
		return err
	}
	return checkInputs(r.Inputs, r.When.Event.Kind.eventInputs())
}

// checkInputs refuses a malformed fixed input, and one named like an input the event sets, since the event's
// value wins at launch and the operator's would never be sent.
func checkInputs(inputs map[string]string, eventNames []string) error {
	for _, name := range slices.Sorted(maps.Keys(inputs)) {
		if name == "" {
			return invalid("inputs: a name is empty")
		}
		if err := checkText(fmt.Sprintf("inputs name %q", name), name); err != nil {
			return err
		}
		if slices.Contains(eventNames, name) {
			return invalid("`%s` is set by the event", name)
		}
		value := inputs[name]
		if len(value) > maxInputValueBytes {
			return invalid("inputs.%s is longer than %d bytes", name, maxInputValueBytes)
		}
		if hasControl(value, "\t\n") {
			return invalid("inputs.%s has a control character", name)
		}
	}
	return nil
}

func checkText(field, value string) error {
	if len(value) > maxTextBytes {
		return invalid("%s is longer than %d bytes", field, maxTextBytes)
	}
	if hasControl(value, "") {
		return invalid("%s has a control character", field)
	}
	return nil
}

// hasControl reports a control character outside allowed, or bytes that are not UTF-8.
func hasControl(s, allowed string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, c := range s {
		if unicode.IsControl(c) && !strings.ContainsRune(allowed, c) {
			return true
		}
	}
	return false
}

func (r *Rule) clone() Rule {
	c := *r
	c.Inputs = maps.Clone(r.Inputs)
	if r.When.Clock != nil {
		spec := *r.When.Clock
		spec.Weekdays = slices.Clone(spec.Weekdays)
		c.When.Clock = &spec
	}
	if r.When.Event != nil {
		c.When.Event = r.When.Event.clone()
	}
	return c
}
