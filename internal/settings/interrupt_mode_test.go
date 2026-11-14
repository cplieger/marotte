package settings

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// What a message sent while a turn runs does, for every chat: steer unless the reader chose queue.
func TestEffective_InterruptMode(t *testing.T) {
	cases := []struct {
		name   string
		stored map[string]json.RawMessage
		want   marotte.InterruptMode
	}{
		{name: "absent reads as steer", want: marotte.InterruptSteer},
		{name: "queue", stored: map[string]json.RawMessage{KeyInterruptMode: json.RawMessage(`"queue"`)}, want: marotte.InterruptQueue},
		{name: "steer", stored: map[string]json.RawMessage{KeyInterruptMode: json.RawMessage(`"steer"`)}, want: marotte.InterruptSteer},
		{
			name: "a word outside the vocabulary reads as steer", want: marotte.InterruptSteer,
			stored: map[string]json.RawMessage{KeyInterruptMode: json.RawMessage(`"later"`)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := EffectiveFrom(tc.stored)
			if got.InterruptMode != tc.want {
				t.Errorf("InterruptMode = %q, want %q", got.InterruptMode, tc.want)
			}
		})
	}
}

// A patch the Settings choice cannot produce is refused, since every send would read it as steer.
func TestCheckPatch_InterruptMode(t *testing.T) {
	for raw, ok := range map[string]bool{`"steer"`: true, `"queue"`: true, `"later"`: false, `true`: false} {
		t.Run(raw, func(t *testing.T) {
			err := CheckPatch(AgentDirs{}, map[string]json.RawMessage{}, map[string]json.RawMessage{
				KeyInterruptMode: json.RawMessage(raw),
			})
			if (err == nil) != ok {
				t.Errorf("CheckPatch(%s) = %v, want accepted %v", raw, err, ok)
			}
		})
	}
}
