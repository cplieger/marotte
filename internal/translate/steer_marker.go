package translate

// Stripping the steering acknowledgement marker out of assistant text. KAS has
// the agent close its response with `[STEERING steer-<id>: what I did about it]`,
// reads it back with `recordSteeringAcks`, and never removes it, so it arrives
// verbatim inside ordinary text deltas, split across any number of chunks. Hence
// a carry: trailing bytes that could still grow into a marker are withheld and
// released once the next chunk proves they were not one; turnlog settles the
// carry at the seal. Text only, matching KAS's `getLastAssistantText`.

import (
	"regexp"
	"strings"

	"github.com/cplieger/marotte/internal/turnlog"
)

// maxSteerCarry bounds what may be withheld. A marker the model opens and never
// closes would otherwise swallow the rest of the turn; past this the carry is
// released as ordinary text on the reasoning that a marker this long is not one.
const maxSteerCarry = 8 << 10

// steerAckRe matches a COMPLETE marker, deliberately the same shape as KAS's
// STEERING_RESPONSE_PATTERN (`(?s)` for its `s` flag, a lazy body ending at the
// first `]`): what marotte hides must be exactly what KAS treats as an
// acknowledgement. The two groups name the steer and the agent's own statement of
// what it did about it, which steerAck surfaces on the steer's chip.
var steerAckRe = regexp.MustCompile(`(?s)\[STEERING (steer-[^\s:]+): (.+?)\]`)

// steerAck is one acknowledgement lifted out of the text: which steer, and what
// the agent says it did about it. Hidden from the transcript and surfaced on the
// steer's own chip, because the marker is a fact about the steer, not the reply.
type steerAck struct {
	SteerID string
	Text    string
}

// stripSteerAcks removes complete acknowledgement markers from carry+incoming
// and returns the text safe to emit, the bytes still withheld, and the
// acknowledgements it lifted out, in order. The returned carry is always a
// suffix of the input: every byte is emitted, withheld, or part of a matched
// marker. A `[` before a complete marker can never grow into one, since a
// complete marker sits between it and every later byte, so it is emitted.
func stripSteerAcks(carry, incoming string) (emit, newCarry string, acks []steerAck) {
	joined := carry + incoming
	buf := joined
	// settled is where the scan for a still-open candidate may begin. Everything
	// before it is followed by a complete marker, so it is finished text.
	settled := 0
	if matches := steerAckRe.FindAllStringSubmatchIndex(joined, -1); matches != nil {
		var b strings.Builder
		last := 0
		for _, m := range matches {
			b.WriteString(joined[last:m[0]])
			acks = append(acks, steerAck{
				SteerID: joined[m[2]:m[3]],
				// Trimmed because the body is rendered as a label, and the
				// marker's own separator leaves it with leading space in the
				// shapes KAS produces.
				Text: strings.TrimSpace(joined[m[4]:m[5]]),
			})
			last = m[1]
		}
		settled = b.Len()
		b.WriteString(joined[last:])
		buf = b.String()
	}
	// Every complete marker is gone, so any candidate left in the tail is still
	// open. Hold from the first one that could still become a marker; a `[` that
	// cannot is ordinary text and must not delay the rest of the sentence behind
	// it.
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

// couldBecomeSteerAck reports whether s, which starts at a `[`, might still turn
// into a complete marker once more text arrives.
//
// Two cases. Shorter than the committing prefix: it qualifies only while it is
// still a prefix OF that prefix, so `[STE` waits and `[doc` does not. At or past
// that length: it must actually carry the prefix, and it stays open until a `]`
// shows up — a `]` already present means the full pattern did not match it back
// in stripSteerAcks, so it never was a marker.
func couldBecomeSteerAck(s string) bool {
	if len(s) < len(turnlog.SteerAckPrefix) {
		return turnlog.SteerAckPrefix[:len(s)] == s
	}
	if !strings.HasPrefix(s, turnlog.SteerAckPrefix) {
		return false
	}
	return !strings.Contains(s, "]")
}
