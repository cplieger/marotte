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

// splitSteerAcks removes complete markers from carry+incoming and returns the text safe to emit
// in stream order, the acks, and the bytes still withheld (always a suffix of the input). texts has
// one more element than acks: texts[i] comes before acks[i] and the last element after the last
// ack, so a caller that seals at each ack files every span on the side of it the stream put it. A
// `[` before a complete marker can never become one, so it is emitted.
func splitSteerAcks(carry, incoming string) (texts []string, acks []steerAck, newCarry string) {
	joined := carry + incoming
	last := 0
	for _, m := range steerAckRe.FindAllStringSubmatchIndex(joined, -1) {
		texts = append(texts, joined[last:m[0]])
		acks = append(acks, steerAck{
			SteerID: joined[m[2]:m[3]],
			// Trimmed: rendered as a label, and the marker's separator leaves a leading space.
			Text: strings.TrimSpace(joined[m[4]:m[5]]),
		})
		last = m[1]
	}
	tail := joined[last:]
	// Every complete marker is gone; hold from the first candidate after the last one that could
	// still become a marker.
	for i := 0; i < len(tail); {
		j := strings.IndexByte(tail[i:], '[')
		if j < 0 {
			break
		}
		at := i + j
		if couldBecomeSteerAck(tail[at:]) {
			if len(tail)-at > maxSteerCarry {
				break
			}
			// `[` is ASCII, so this is a rune boundary and neither side can be a
			// torn code point.
			return append(texts, tail[:at]), acks, tail[at:]
		}
		i = at + 1
	}
	return append(texts, tail), acks, ""
}

// stripSteerAcks is splitSteerAcks for a caller that discards the acks' positions: the emitted
// text joined, the withheld bytes, and the acks in order.
func stripSteerAcks(carry, incoming string) (emit, newCarry string, acks []steerAck) {
	texts, acks, newCarry := splitSteerAcks(carry, incoming)
	return strings.Join(texts, ""), newCarry, acks
}

// couldBecomeSteerAck reports whether s, starting at `[` and holding no complete marker (the
// caller removes those first), could still be extended into a string steerAckRe matches from
// its start. It must track steerAckRe: the id may hold `]`, and so may the body's first character.
func couldBecomeSteerAck(s string) bool {
	p := turnlog.SteerAckPrefix
	if len(s) <= len(p) {
		return p[:len(s)] == s
	}
	if !strings.HasPrefix(s, p) {
		return false
	}
	rest := s[len(p):]
	// `\s` in RE2 is [\t\n\f\r ]; the id ends at the first of these or `:`.
	end := strings.IndexAny(rest, ":\t\n\f\r ")
	switch {
	case end < 0:
		return true
	case end == 0 || rest[end] != ':':
		return false
	}
	body := rest[end+1:]
	if body == "" {
		return true
	}
	if body[0] != ' ' {
		return false
	}
	body = body[1:]
	// A `]` after the body's first character would close the marker; `]` is ASCII, so no
	// multi-byte first character hides one past byte 0.
	return body == "" || !strings.Contains(body[1:], "]")
}
