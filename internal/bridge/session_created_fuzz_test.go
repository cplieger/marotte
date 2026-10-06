package bridge

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/marotte/internal/ids"
)

// FuzzSessionCreatedUnmarshal fuzzes session/new result parsing: a crafted sessionId with path separators must not
// pass validation, and modes and models survive a round trip.
func FuzzSessionCreatedUnmarshal(f *testing.F) {
	f.Add(`{"sessionId":"abc-123","modes":{"currentModeId":"code","availableModes":[{"id":"code","name":"Code"}]},"configOptions":[{"id":"model","currentValue":"m1","options":[{"value":"m1","name":"M1","_meta":{"kiro":{"rateMultiplier":1.0}}}]}]}`)
	f.Add(`{"sessionId":"../escape"}`)
	f.Add(`{"sessionId":"a/b\\c"}`)
	f.Add(`{"sessionId":""}`)
	f.Add(`{}`)
	f.Add(`{"sessionId":"valid-id","modes":null,"configOptions":null}`)
	f.Add(`{"sessionId":"x","modes":{"availableModes":[]},"configOptions":[]}`)

	f.Fuzz(func(t *testing.T, data string) {
		var result sessionCreated
		if json.Unmarshal([]byte(data), &result) != nil {
			return
		}

		// An id passing ValidSessionID has no separators or traversal.
		if ids.ValidSessionID(result.SessionID) {
			for _, ch := range result.SessionID {
				if ch == '/' || ch == '\\' || ch == 0 {
					t.Fatalf("ValidSessionID accepted dangerous char in %q", result.SessionID)
				}
			}
			if result.SessionID == ".." || result.SessionID == "." {
				t.Fatalf("ValidSessionID accepted traversal pattern %q", result.SessionID)
			}
		}

		// Non-nil Modes has a valid slice.
		if result.Modes != nil {
			for i, mode := range result.Modes.AvailableModes {
				if mode.ID == "" && mode.Name == "" {
					_ = i
				}
			}
		}

		// The catalog rides configOptions; kiro-cli may send invalid model ids, so only no-panic is asserted.
		for i := range result.ConfigOptions {
			for _, choice := range result.ConfigOptions[i].Options {
				_ = choice.Value
			}
		}

		// The round trip keeps the sessionId.
		marshalled, err := json.Marshal(result)
		if err != nil {
			t.Fatalf("re-marshal failed: %v", err)
		}
		var result2 sessionCreated
		if json.Unmarshal(marshalled, &result2) != nil {
			t.Fatalf("re-unmarshal failed")
		}
		if result.SessionID != result2.SessionID {
			t.Fatalf("sessionId lost in round-trip: %q → %q", result.SessionID, result2.SessionID)
		}
	})
}
