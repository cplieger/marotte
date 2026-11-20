package workflow

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// FuzzGlanceRead_idempotent: a glance is valid JSON whenever its input is, and a glance of a
// glance holds the same values (every repeat already holds one pass, and its fold is copied).
func FuzzGlanceRead_idempotent(f *testing.F) {
	f.Add([]byte(glanceFixture))
	f.Add([]byte(`{"state":{"root":{"nodeId":"r","type":"repeat","children":[{"nodeId":"r#0","iteration":0},{"nodeId":"r#1","iteration":1}]}}}`))
	f.Add([]byte(`{"state":{"root":{"type":"repeat","children":[]}},"x":[1,{"a":null}]}`))
	f.Add([]byte(`{"state":{"root":{}}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if !json.Valid(raw) {
			return
		}
		once, _, err := GlanceRead(raw, nil)
		if err != nil {
			return
		}
		if !json.Valid(once) {
			t.Fatalf("the glance of %s is not JSON: %s", raw, once)
		}
		twice, _, err := GlanceRead(once, nil)
		if err != nil {
			t.Fatalf("a glance could not be read again: %v (%s)", err, once)
		}
		var a, b any
		if json.Unmarshal(once, &a) != nil || json.Unmarshal(twice, &b) != nil || !reflect.DeepEqual(a, b) {
			t.Fatalf("a glance of a glance moved:\n%s\n%s", once, twice)
		}
	})
}

// FuzzHealth_pausedNeverHealthy: whatever pause and park fields a reply carries, a decoded paused run never reads
// healthy, and no drifted field fails the decode of a reply that decoded without it.
func FuzzHealth_pausedNeverHealthy(f *testing.F) {
	f.Add(`{"class":"transient-error","code":"ClientThrottleError","occurredAt":"x"}`, `{"kind":"maxIterations"}`, `"user"`, `4`, `true`, `"send_message"`, "need_input")
	f.Add(`7`, `null`, `1`, `"four"`, `"yes"`, `{}`, "")
	f.Add(`{"class":"solar-flare"}`, `{"kind":"other"}`, `null`, `null`, `null`, `null`, "success")
	f.Add(`null`, `null`, `null`, `null`, `null`, `"status_update"`, "need_input")
	f.Fuzz(func(t *testing.T, detail, pause, initiator, attempts, awaiting, source, signal string) {
		for _, v := range []string{detail, pause, initiator, attempts, awaiting, source} {
			if !json.Valid([]byte(v)) {
				return
			}
		}
		sig, _ := json.Marshal(signal)
		raw := `{"workflowId":"wf_x","state":{"workflowId":"wf_x","status":"paused","pauseDetail":` + detail +
			`,"pause":` + pause + `,"stopInitiator":` + initiator + `,"root":{"nodeId":"wf_x","type":"sequence","status":"paused",` +
			`"children":[{"nodeId":"s","type":"step","status":"paused","sessionId":"sess_x","completionSignal":` + string(sig) +
			`,"continuationAttempts":` + attempts + `,"awaitingUserInput":` + awaiting + `,"completionSignalSource":` + source +
			`,"attention":[{"kind":"question","since":"2026-11-20T00:00:00Z"}]}]}}}`
		in, err := DecodeInspect("wf_x", json.RawMessage(raw))
		if err != nil {
			t.Fatalf("a reply with drifted pause fields did not decode: %v\n%s", err, raw)
		}
		for _, launched := range []bool{false, true} {
			h := Health(in, &HealthFacts{Reachable: true, Launched: launched, Asks: OpenAsks(in, RunLog{})}, time.Unix(0, 0))
			if h.State == HealthHealthy {
				t.Fatalf("a paused run read healthy (%s):\n%s", h.Why, raw)
			}
		}
	})
}
