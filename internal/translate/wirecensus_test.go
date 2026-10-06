package translate

// Serial, never parallel: these tests swap slog's process-global default and reset the
// package-global ledger.

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// resetCensus empties and un-latches the ledger for one test; a leftover ledger would let
// a test pass vacuously.
func resetCensus(t *testing.T) {
	t.Helper()
	census.mu.Lock()
	census.reported = make(map[string]struct{}, maxCensusKeys)
	census.off = false
	census.mu.Unlock()
	t.Cleanup(func() {
		census.mu.Lock()
		census.reported = make(map[string]struct{}, maxCensusKeys)
		census.off = false
		census.mu.Unlock()
	})
}

// TestCensusMeta_ReportsAnUnknownFieldOnce pins that an unread member reaches the log, by
// name and JSON type, once however many frames carry it.
func TestCensusMeta_ReportsAnUnknownFieldOnce(t *testing.T) {
	resetCensus(t)
	var logbuf bytes.Buffer
	defer captureSlog(&logbuf)()

	raw := json.RawMessage(`{"kind":"agent-subtask","cacheReadTokens":41}`)
	for range 3 {
		censusMeta("_meta.kiro", raw, reflect.TypeFor[acpKiroBlockShadow]())
	}

	out := logbuf.String()
	if n := strings.Count(out, "UNKNOWN _meta.kiro field"); n != 1 {
		t.Errorf("reported %d times for 3 identical frames, want 1: %s", n, out)
	}
	for _, want := range []string{"cachereadtokens", "type=number"} {
		if !strings.Contains(strings.ToLower(out), want) {
			t.Errorf("report does not carry %q: %s", want, out)
		}
	}
	// A known field must not be reported, or the probe is noise on every frame.
	if strings.Contains(out, `field=kind`) {
		t.Errorf("reported a field the struct reads: %s", out)
	}
}

// TestCensusMeta_NeverLogsAValue pins the safety property: no field's contents reach the log.
func TestCensusMeta_NeverLogsAValue(t *testing.T) {
	resetCensus(t)
	var logbuf bytes.Buffer
	defer captureSlog(&logbuf)()

	const secret = "ya29.A0ARrdaM-NOT-A-REAL-TOKEN"
	censusMeta("_meta.kiro", json.RawMessage(`{"someNewAuthField":"`+secret+`"}`),
		reflect.TypeFor[acpKiroBlockShadow]())

	out := logbuf.String()
	if !strings.Contains(out, "UNKNOWN") {
		t.Fatalf("the field was not reported at all: %s", out)
	}
	if strings.Contains(out, secret) {
		t.Errorf("the census logged a field VALUE: %s", out)
	}
	if !strings.Contains(out, "type=string") {
		t.Errorf("the census dropped the type, which is the whole payload: %s", out)
	}
}

// TestCensusMeta_FoldsCase pins the case fold: encoding/json consumes `MessageId` with the
// `messageId` field, so a case-sensitive compare reports a false finding.
func TestCensusMeta_FoldsCase(t *testing.T) {
	resetCensus(t)
	var logbuf bytes.Buffer
	defer captureSlog(&logbuf)()

	censusMeta("_meta.kiro", json.RawMessage(`{"MessageId":"m-1","AGENTSUBTASKID":"s-1"}`),
		reflect.TypeFor[acpKiroBlockShadow]())

	if out := logbuf.String(); strings.Contains(out, "UNKNOWN") {
		t.Errorf("reported a field encoding/json consumed by case-insensitive match: %s", out)
	}
}

// TestCensusMeta_DeclinedFieldsAreQuiet pins that `preview` is not reported, or the probe
// fires on every file write and mutes itself.
func TestCensusMeta_DeclinedFieldsAreQuiet(t *testing.T) {
	resetCensus(t)
	var logbuf bytes.Buffer
	defer captureSlog(&logbuf)()

	censusMeta("_meta.kiro", json.RawMessage(`{"preview":{"originalContent":"..."}}`),
		reflect.TypeFor[acpKiroBlockShadow](), "preview")

	if out := logbuf.String(); strings.Contains(out, "UNKNOWN") {
		t.Errorf("reported a deliberately-declined field: %s", out)
	}
}

// TestCensusMeta_IsBounded covers the per-frame bound and the per-process latch.
func TestCensusMeta_IsBounded(t *testing.T) {
	t.Run("an oversized object is skipped", func(t *testing.T) {
		resetCensus(t)
		var logbuf bytes.Buffer
		defer captureSlog(&logbuf)()

		big := `{"novelField":"` + strings.Repeat("x", maxCensusObjectBytes) + `"}`
		censusMeta("_meta.kiro", json.RawMessage(big), reflect.TypeFor[acpKiroBlockShadow]())

		if out := logbuf.String(); strings.Contains(out, "UNKNOWN") {
			t.Errorf("scanned an object past the per-frame bound: %s", out)
		}
	})

	t.Run("the key budget latches the probe off", func(t *testing.T) {
		resetCensus(t)
		var logbuf bytes.Buffer
		defer captureSlog(&logbuf)()

		// One novel field per frame, well past the cap.
		for i := range maxCensusKeys * 2 {
			censusMeta("_meta.kiro",
				json.RawMessage(`{"f`+string(rune('a'+i%26))+string(rune('a'+i/26))+`":1}`),
				reflect.TypeFor[acpKiroBlockShadow]())
		}

		out := logbuf.String()
		if !strings.Contains(out, "field budget spent") {
			t.Errorf("the budget was never reported as spent: %s", out)
		}
		if !census.disabled() {
			t.Error("the ledger did not latch off at its cap; the map grows unbounded on backend-controlled keys")
		}
		if got := strings.Count(out, "UNKNOWN _meta.kiro field"); got > maxCensusKeys {
			t.Errorf("reported %d fields, want at most the %d-key cap", got, maxCensusKeys)
		}
	})
}

// TestCensusMeta_SanitizesTheFieldName pins the bound and the absence of an escaped
// newline: slog's TextHandler escapes a raw newline itself, so asserting only its absence
// passes with the sanitizer removed.
func TestCensusMeta_SanitizesTheFieldName(t *testing.T) {
	resetCensus(t)
	var logbuf bytes.Buffer
	defer captureSlog(&logbuf)()

	// A name carrying a newline, a forged record after it, and far more bytes
	// than the cap allows.
	name := "evil\nlevel=ERROR msg=\"forged\" " + strings.Repeat("x", 4096)
	raw, err := json.Marshal(map[string]int{name: 1})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	censusMeta("_meta.kiro", raw, reflect.TypeFor[acpKiroBlockShadow]())

	out := logbuf.String()
	if !strings.Contains(out, "UNKNOWN") {
		t.Fatalf("the field was not reported at all: %s", out)
	}
	// The escaped form slog renders an unsanitized newline as; runesafe makes it a space.
	if strings.Contains(out, `\n`) {
		t.Errorf("an unsanitized newline reached the log line: %q", out)
	}
	// The cap is this package's own, so this fails when the sanitize call is removed.
	if len(out) > len(name) {
		t.Errorf("the log line is %d bytes for a %d-byte name; the field name was not bounded",
			len(out), maxCensusNameBytes)
	}
}

// TestCensusMeta_NeverBreaksADecode pins, through the real decode, that the probe never
// contributes an error: every call site drops the frame on one.
func TestCensusMeta_NeverBreaksADecode(t *testing.T) {
	resetCensus(t)
	for name, frame := range map[string]string{
		"unknown field":     `{"content":{"type":"text","text":"hi"},"_meta":{"kiro":{"brandNew":1}}}`,
		"null meta":         `{"content":{"type":"text","text":"hi"},"_meta":null}`,
		"null kiro":         `{"content":{"type":"text","text":"hi"},"_meta":{"kiro":null}}`,
		"kiro is a string":  `{"content":{"type":"text","text":"hi"},"_meta":{"kiro":"nope"}}`,
		"kiro is an array":  `{"content":{"type":"text","text":"hi"},"_meta":{"kiro":[1,2]}}`,
		"empty kiro object": `{"content":{"type":"text","text":"hi"},"_meta":{"kiro":{}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var chunk ACPChunkWire
			err := json.Unmarshal([]byte(frame), &chunk)
			// A wrongly-typed block is the caller's decode error; the probe must never ADD one.
			if err != nil && !strings.Contains(name, "string") && !strings.Contains(name, "array") {
				t.Fatalf("decode failed: %v", err)
			}
			if err == nil && chunk.Content.Text != "hi" {
				t.Errorf("text = %q, want the frame decoded normally", chunk.Content.Text)
			}
		})
	}
}

// TestCensusMeteringUnit_ReportsAnUnsummedUnit pins the one VALUE report: an unrecognised
// unit is otherwise silently dropped from the spend total.
func TestCensusMeteringUnit_ReportsAnUnsummedUnit(t *testing.T) {
	resetCensus(t)
	var logbuf bytes.Buffer
	defer captureSlog(&logbuf)()

	censusMeteringUnit("cacheRead")
	censusMeteringUnit("cacheRead")
	censusMeteringUnit(meteringUnitCredit)
	censusMeteringUnit("")

	out := logbuf.String()
	if n := strings.Count(out, "UNKNOWN metering unit"); n != 1 {
		t.Errorf("reported %d times, want 1 (once per process per label): %s", n, out)
	}
	if !strings.Contains(out, "cacheRead") {
		t.Errorf("the report does not name the unit, which is the whole finding: %s", out)
	}
	if strings.Contains(out, "unit=credit") {
		t.Errorf("reported the unit that IS summed: %s", out)
	}
}

// TestKnownKeysOf_CoversTheWholeWireBlock pins the derived set against its two carriers,
// catching a missed field or a failed lowercase in the walk.
func TestKnownKeysOf_CoversTheWholeWireBlock(t *testing.T) {
	cases := map[string]struct {
		typ  reflect.Type
		want []string
	}{
		"_meta.kiro": {
			typ: reflect.TypeFor[acpKiroBlockShadow](),
			want: []string{
				"refusal", "checkpoint", "disclosedcontext", "policydenial", "kind",
				"agentsubtaskid", "replayid", "usermessagetag", "messageid", "timestamp",
				"notification", "workflow", "hookask",
			},
		},
		"session_info_update._meta.kiro": {
			typ: reflect.TypeFor[sessionInfoKiroShadow](),
			want: []string{
				"summarization", "usagepercentage", "workflow", "contextusage", "focus", "hook",
				"messageids", "messageid", "content", "notificationseverity", "kind",
				"promptturnsummaries", "elapsedtime",
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := knownKeysOf(tc.typ)
			for _, key := range tc.want {
				if _, ok := got[key]; !ok {
					// Report the whole set once rather than per missing key.
					t.Errorf("derived key set is missing %q; keys=%v", key, sortedKeys(got))
				}
			}
			for key := range got {
				if key != strings.ToLower(key) {
					t.Errorf("derived key %q is not lowercased; encoding/json matches members case-insensitively", key)
				}
			}
		})
	}
}

// sortedKeys renders a set for a failure message.
func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestJSONKindOf names every shape a member can take: the type is a report's whole payload.
func TestJSONKindOf(t *testing.T) {
	cases := map[string]string{
		`{"a":1}`:  "object",
		`[1,2]`:    "array",
		`"text"`:   "string",
		`true`:     "bool",
		`false`:    "bool",
		`null`:     "null",
		`42`:       "number",
		`-1.5e3`:   "number",
		"  \n\t{}": "object",
		``:         "empty",
	}
	for raw, want := range cases {
		if got := jsonKindOf(json.RawMessage(raw)); got != want {
			t.Errorf("jsonKindOf(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestSessionInfoUpdate_CensusRunsOnTheRealFrame pins that the probe fires through the
// ordinary handler, not only when called directly.
func TestSessionInfoUpdate_CensusRunsOnTheRealFrame(t *testing.T) {
	resetCensus(t)
	var logbuf bytes.Buffer
	defer captureSlog(&logbuf)()

	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	tr.HandleSessionInfoUpdate(t.Context(), marotte.ChatID("c1"), mustJSON(t, map[string]any{
		"_meta": map[string]any{"kiro": map[string]any{
			"kind": "turn_end", "brandNewBlock": map[string]any{"x": 1},
		}},
	}), FrameAttribution{})

	out := logbuf.String()
	if !strings.Contains(out, "UNKNOWN _meta.kiro field") {
		t.Errorf("the census did not run on a real session_info_update: %s", out)
	}
	if !strings.Contains(out, "type=object") {
		t.Errorf("the report does not name the new member's type: %s", out)
	}
}

// TestKnownKeys_IsCachedPerType pins by identity that the cache returns the SAME map:
// agent_message_chunk arrives per token.
func TestKnownKeys_IsCachedPerType(t *testing.T) {
	first := knownKeys(reflect.TypeFor[acpKiroBlockShadow]())
	second := knownKeys(reflect.TypeFor[acpKiroBlockShadow]())
	if len(first) == 0 {
		t.Fatal("derived no keys at all")
	}
	// Go forbids comparing maps, so compare reflect.ValueOf pointers.
	if reflect.ValueOf(first).Pointer() != reflect.ValueOf(second).Pointer() {
		t.Error("knownKeys rebuilt the set; the walk and its allocation run per frame")
	}
}

// TestCensusMeta_DoesNotMutateTheCachedSet pins that one caller's declined list does not
// leak into the shared cached set.
func TestCensusMeta_DoesNotMutateTheCachedSet(t *testing.T) {
	resetCensus(t)
	var logbuf bytes.Buffer
	defer captureSlog(&logbuf)()

	typ := reflect.TypeFor[acpKiroBlockShadow]()
	before := len(knownKeys(typ))

	censusMeta("_meta.kiro", json.RawMessage(`{"preview":1}`), typ, "preview")

	if after := len(knownKeys(typ)); after != before {
		t.Errorf("the shared key set grew from %d to %d; a declined name leaked into it",
			before, after)
	}
	// The decline still has to work, or the test above passes for the wrong reason.
	if strings.Contains(logbuf.String(), "UNKNOWN") {
		t.Errorf("the declined name was reported: %s", logbuf.String())
	}
}

// BenchmarkCensusMeta prices the probe on the streaming hot path; run with -benchmem.
func BenchmarkCensusMeta(b *testing.B) {
	raw := json.RawMessage(`{"kind":"agent-subtask","agentSubtaskId":"s-1",` +
		`"messageId":"m-1-say","timestamp":"2026-08-21T10:00:00.000Z"}`)
	typ := reflect.TypeFor[acpKiroBlockShadow]()
	b.ReportAllocs()
	for b.Loop() {
		censusMeta("_meta.kiro", raw, typ, "preview")
	}
}
