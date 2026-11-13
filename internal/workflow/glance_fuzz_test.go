package workflow

import (
	"encoding/json"
	"reflect"
	"testing"
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
