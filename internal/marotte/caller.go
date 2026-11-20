package marotte

// CallerKind is what sent a caller's message; "" is a program through marotte's API.
type CallerKind string

// CallerKindRule is a Bot's rule: Label is the rule's name, Rule its id, Event the event that fired it.
const CallerKindRule CallerKind = "rule"

// CallerKindMarotte is marotte's own message, its run-attention wake or a run heal's answer. No outside caller can
// state it, so SameSender never matches an outside caller's label "marotte" against it.
const CallerKindMarotte CallerKind = "marotte"

// Caller marks a message a program sent through marotte's API, a Bot's rule sent, or marotte sent on its own, rather
// than one the user typed in its page; a message with no Caller is the user's own. Label is the name the sender gave
// itself, "" when it gave none.
type Caller struct {
	Kind  CallerKind `json:"kind,omitempty"`
	Label string     `json:"label,omitempty"`
	Rule  string     `json:"rule,omitempty"`
	Event string     `json:"event,omitempty"`
	// Depth is the rule event's chain depth, 1 when no automatic turn caused it.
	Depth int `json:"depth,omitempty"`
}

// SentBy is how the transcript names c's message: "Rule: <name>" for a rule, "Sent by <label>", or "Sent by an API
// caller" with no label.
func (c *Caller) SentBy() string {
	switch {
	case c.Kind == CallerKindRule:
		return "Rule: " + c.Label
	case c.Label == "":
		return "Sent by an API caller"
	}
	return "Sent by " + c.Label
}

// SameSender reports o as c's sender: for a rule, the same event of the same rule, whatever its name; else the same
// label from a caller that is not a rule.
func (c *Caller) SameSender(o *Caller) bool {
	if c.Kind != o.Kind {
		return false
	}
	if c.Kind == CallerKindRule {
		return c.Rule == o.Rule && c.Event == o.Event
	}
	return c.Label == o.Label
}

// SharedCaller is who a joined message's parts came from: nil when any part is the user's, so words the user wrote
// keep the message the user's; the one caller when every part equals it; an unnamed API caller when they differ.
func SharedCaller(parts []*Caller) *Caller {
	if len(parts) == 0 {
		return nil
	}
	shared := true
	for _, c := range parts {
		if c == nil {
			return nil
		}
		if *c != *parts[0] {
			shared = false
		}
	}
	if !shared {
		return &Caller{}
	}
	one := *parts[0]
	return &one
}
