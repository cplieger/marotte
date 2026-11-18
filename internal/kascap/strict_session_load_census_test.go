package kascap

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

var (
	// strictLoadSchemaRe is session/load's `_meta.kiro` load schema, which takes requireExisting only as a literal true.
	strictLoadSchemaRe = regexp.MustCompile(
		jsIdent + `\.object\(\{requireExisting:` + jsIdent + `\.literal\(!0\)\.optional\(\)\}\)`,
	)
	// strictLoadCapabilityLit is the agent capability KAS advertises beside the strict load.
	strictLoadCapabilityLit = "strictSessionLoad:!0"
	// sessionNotFoundRe is the class KAS names SessionNotFoundError: its constructor's parameter (group 1), its
	// JSON-RPC code (group 2), its message template (group 3) and the template's interpolated name (group 4).
	sessionNotFoundRe = regexp.MustCompile(
		`class extends ` + jsIdent + `\{constructor\((` + jsIdent + `)\)\{super\((-?[0-9.e]+),` +
			"`(Session '\\$\\{(" + jsIdent + ")\\}' not found)`" +
			`\);[^}]*\}[^{}]*static\{` + jsIdent + `\(this,"SessionNotFoundError"\)\}`,
	)
)

// TestStrictSessionLoadMatchesTheBundle pins the premise of the requireExisting row and of
// marotte.SessionRecordGone: KAS's session/load takes requireExisting: true, advertises strictSessionLoad, and
// refuses a missing record with SessionNotFoundError, whose rendered code and message the predicate must match.
func TestStrictSessionLoadMatchesTheBundle(t *testing.T) {
	forEachBundle(t, func(t *testing.T, b bundle) {
		if n := len(strictLoadSchemaRe.FindAllStringIndex(b.src, -1)); n != 1 {
			t.Errorf("found %d requireExisting: literal(true) load schemas, want exactly 1; "+
				"without it every load of a lost session creates it blank again", n)
		}
		if n := strings.Count(b.src, strictLoadCapabilityLit); n != 1 {
			t.Errorf("found %d %s agent capabilities, want exactly 1", n, strictLoadCapabilityLit)
		}
		ms := sessionNotFoundRe.FindAllStringSubmatch(b.src, -1)
		if len(ms) != 1 {
			t.Fatalf("found %d SessionNotFoundError classes, want exactly 1; re-anchor on KAS's refusal", len(ms))
		}
		param, rawCode, template, interpolated := ms[0][1], ms[0][2], ms[0][3], ms[0][4]
		if interpolated != param {
			t.Fatalf("SessionNotFoundError interpolates %q, not its constructor's session id %q", interpolated, param)
		}
		code, err := strconv.ParseFloat(rawCode, 64)
		if err != nil {
			t.Fatalf("SessionNotFoundError code %q: %v", rawCode, err)
		}
		const id = "sess_00000000-0000-4000-8000-000000000000"
		refusal := &marotte.RPCError{Code: int(code), Message: strings.Replace(template, "${"+param+"}", id, 1)}
		if !marotte.SessionRecordGone(refusal, id) {
			t.Errorf("marotte.SessionRecordGone does not match KAS's refusal (code %d, message %q) for %q",
				refusal.Code, refusal.Message, id)
		}
	})
}
