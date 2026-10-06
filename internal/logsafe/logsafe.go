// Package logsafe binds runesafe's single-line preset to this app's log surface, so every untrusted
// string reaches slog through one policy. Its durable value is the BOUND: no slog handler caps an
// attribute, so a hostile value pushes useful attributes off the line and balloons the record.
// The rune half is insurance: the Text handler marotte installs already escapes every class
// (go1.27.1), but JSONHandler passes C1 and bidi runes raw, and a non-slog sink escapes nothing.
// One surface, so one constant bound here rather than per caller. Multi-line agent output is
// internal/sanitize's.
package logsafe

import "github.com/cplieger/runesafe/v2"

// MaxFieldBytes bounds one sanitized attribute. Long enough for a workspace
// path or an upstream error sentence, short enough that a hostile value cannot
// push the useful attributes off the end of a log line.
const MaxFieldBytes = 256

// Field prepares one untrusted string for a slog attribute: runesafe's single-line preset, then a
// rune-boundary cap with a "..." marker. Route every untrusted attribute through it, so no reader
// has to prove which ones are safe.
func Field(s string) string {
	return runesafe.SanitizeSingleLineBounded(s, MaxFieldBytes)
}
