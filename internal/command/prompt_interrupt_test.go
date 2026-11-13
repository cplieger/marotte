package command

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// A prompt names what it does beside a running turn: steer, queue, or nothing, which leaves it to the instance's
// interrupt_mode setting. Any other word is refused before anything is placed.
func TestValidatePromptPayload_InterruptMode(t *testing.T) {
	cases := []struct {
		name   string
		extra  string
		want   marotte.InterruptMode
		status int
	}{
		{name: "absent", want: ""},
		{name: "steer", extra: `,"interrupt_mode":"steer"`, want: marotte.InterruptSteer},
		{name: "queue", extra: `,"interrupt_mode":"queue"`, want: marotte.InterruptQueue},
		{name: "an unknown word", extra: `,"interrupt_mode":"later"`, status: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &marotte.ClientCommand{Payload: json.RawMessage(`{"text":"hi","message_id":"msg-1"` + tc.extra + `}`)}
			p, status, err := validatePromptPayload(cmd)
			if status != tc.status {
				t.Fatalf("status = %d (%v), want %d", status, err, tc.status)
			}
			if tc.status == 0 && p.InterruptMode != tc.want {
				t.Errorf("InterruptMode = %q, want %q", p.InterruptMode, tc.want)
			}
		})
	}
}
