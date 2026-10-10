package translate

import (
	"encoding/json"
	"testing"
)

// FuzzACPWireDecode decodes arbitrary bytes into every ACP wire struct, asserting no panic
// and an explicit error for invalid input.
func FuzzACPWireDecode(f *testing.F) {
	seeds := []string{
		`{"sessionId":"sess-1","update":{"sessionUpdate":"session_started"}}`,
		`{"sessionId":"sess-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello"}}}`,
		`{"sessionId":"sess-1","update":{"sessionUpdate":"request_permission"}}`,
		`{"toolCallId":"tc-1","title":"Read file","kind":"file_read","status":"running","rawInput":{},"locations":[{"path":"/tmp/x"}],"content":[{"type":"text","content":{"text":"data"}}]}`,
		`{"toolCallId":"tc-2","status":"completed","locations":[],"content":[]}`,
		`{"entries":[{"title":"Step 1","status":"pending"}]}`,
		// current_mode_update (KAS keys the mode on currentModeId)
		`{"currentModeId":"code"}`,
		`{"content":{"type":"text","text":""}}`,
		`{}`,
		`{"sessionId":"","update":null}`,
		`null`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		var chunk acpChunkWire
		decodeAndCheck(t, data, &chunk)

		var tc acpToolCallWire
		decodeAndCheck(t, data, &tc)

		var tu acpToolCallUpdateWire
		decodeAndCheck(t, data, &tu)

		var plan acpPlanWire
		decodeAndCheck(t, data, &plan)

		var mode acpModeUpdateWire
		decodeAndCheck(t, data, &mode)

		var env ACPSessionUpdateEnvelope
		decodeAndCheck(t, data, &env)

		var base ACPSessionUpdateBase
		decodeAndCheck(t, data, &base)
	})
}

// decodeAndCheck unmarshals data into dst and fails when invalid JSON yields no error.
func decodeAndCheck(t *testing.T, data []byte, dst any) {
	t.Helper()
	err := json.Unmarshal(data, dst)
	if !json.Valid(data) && err == nil {
		t.Errorf("invalid JSON produced nil error for %T", dst)
	}
}
