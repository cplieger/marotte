package marotte

// Caller marks a message a program sent through marotte's API rather than one the user typed in its page; a message
// with no Caller is the user's own. Label is the name the program gave itself, "" when it gave none.
type Caller struct {
	Label string `json:"label,omitempty"`
}

// SentBy is how the transcript names c's message: "Sent by <label>", or "Sent by an API caller" with no label.
func (c *Caller) SentBy() string {
	if c.Label == "" {
		return "Sent by an API caller"
	}
	return "Sent by " + c.Label
}

// SentByMarotte is how the transcript names a prompt marotte sent on its own (EntryPrompt.Automatic).
const SentByMarotte = "Sent by marotte"

// SharedCaller is who a joined message's parts came from: nil when any part is the user's, so words the user wrote
// keep the message the user's; the one caller when every part names it; an unnamed API caller when the callers differ.
func SharedCaller(parts []*Caller) *Caller {
	if len(parts) == 0 {
		return nil
	}
	label := ""
	for i, c := range parts {
		if c == nil {
			return nil
		}
		if i == 0 {
			label = c.Label
		} else if c.Label != label {
			label = ""
		}
	}
	return &Caller{Label: label}
}
