package translate

// Stripping the steering ack marker from assistant text: KAS has the agent close with
// `[STEERING steer-<id>: what I did]` and never removes it, so it arrives inside text deltas
// split across chunks. A carry withholds bytes that could still become a marker; turnlog
// settles it at the seal. Text only, like KAS's `getLastAssistantText`.

import (
	"regexp"
	"strings"

	"github.com/cplieger/marotte/internal/turnlog"
)

// A marker the model opens and never closes would otherwise swallow the rest of the turn; past this
// the carry is released as ordinary text on the reasoning that a marker this long is not one.
const maxSteerCarry = 8 << 10

// steerAckRe matches a COMPLETE marker, the same shape as KAS's STEERING_RESPONSE_PATTERN
// (`(?s)`, lazy body to the first `]`): marotte hides exactly what KAS reads as an ack. The
// groups are the steer id and the agent's statement.
var steerAckRe = regexp.MustCompile(`(?s)\[STEERING (steer-[^\s:]+): (.+?)\]`)

// steerAck is one ack lifted out of the text: which steer, and what the agent says it did,
// surfaced on the steer's chip.
type steerAck struct {
	SteerID string
	Text    string
}

// stripSteerAcks removes complete markers from carry+incoming and returns the text safe to
// emit, the bytes still withheld (always a suffix of the input), and the acks in order. A `[`
// before a complete marker can never become one, so it is emitted.
func stripSteerAcks(carry, incoming string) (emit, newCarry string, acks []steerAck) {
	joined := carry + incoming
	buf := joined
	// Everything before settled is followed by a complete marker: finished text.
	settled := 0
	if matches := steerAckRe.FindAllStringSubmatchIndex(joined, -1); matches != nil {
		var b strings.Builder
		last := 0
		for _, m := range matches {
			b.WriteString(joined[last:m[0]])
			acks = append(acks, steerAck{
				SteerID: joined[m[2]:m[3]],
				// Trimmed: rendered as a label, and the marker's separator leaves a leading space.
				Text: strings.TrimSpace(joined[m[4]:m[5]]),
			})
			last = m[1]
		}
		settled = b.Len()
		b.WriteString(joined[last:])
		buf = b.String()
	}
	// Every complete marker is gone; hold from the first candidate that could still become one.
	for i := settled; i < len(buf); {
		j := strings.IndexByte(buf[i:], '[')
		if j < 0 {
			break
		}
		at := i + j
		if couldBecomeSteerAck(buf[at:]) {
			if len(buf)-at > maxSteerCarry {
				return buf, "", acks
			}
			// `[` is ASCII, so this is a rune boundary and neither side can be a
			// torn code point.
			return buf[:at], buf[at:], acks
		}
		i = at + 1
	}
	return buf, "", acks
}

// couldBecomeSteerAck reports whether s (starting at `[`) might still become a complete
// marker: shorter than the committing prefix, it must be a prefix of it; at or past it, it
// must carry it and have no `]` yet.
func couldBecomeSteerAck(s string) bool {
	if len(s) < len(turnlog.SteerAckPrefix) {
		return turnlog.SteerAckPrefix[:len(s)] == s
	}
	if !strings.HasPrefix(s, turnlog.SteerAckPrefix) {
		return false
	}
	return !strings.Contains(s, "]")
}
