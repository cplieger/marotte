package marotte

import (
	"errors"
	"fmt"
	"testing"
)

// TestSessionRecordGone pins the strict-load refusal against KAS's SessionNotFoundError, which is code -32000 with
// the message `Session '<id>' not found` and no data.
func TestSessionRecordGone(t *testing.T) {
	const id = "sess_old"
	refusal := &RPCError{Code: -32000, Message: "Session 'sess_old' not found"}
	tests := []struct {
		err  error
		name string
		want bool
	}{
		{name: "the refusal for this id", err: refusal, want: true},
		{name: "the refusal wrapped", err: fmt.Errorf("load session: %w", refusal), want: true},
		{name: "the refusal for another id", err: &RPCError{Code: -32000, Message: "Session 'sess_other' not found"}},
		{name: "the same text under another code", err: &RPCError{Code: -32602, Message: "Session 'sess_old' not found"}},
		{name: "another -32000", err: &RPCError{Code: -32000, Message: "Authentication required"}},
		{name: "a plain error with the same text", err: errors.New("Session 'sess_old' not found")},
		{name: "nil", err: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SessionRecordGone(tt.err, id); got != tt.want {
				t.Errorf("SessionRecordGone(%v, %q) = %v, want %v", tt.err, id, got, tt.want)
			}
		})
	}
}
