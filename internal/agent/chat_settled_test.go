package agent

import (
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

// TestChatHoldsLiveRun guards the false negative: a push claiming work finished while a
// launched run continues. Parked and restored leases read Bounded() false.
func TestChatHoldsLiveRun(t *testing.T) {
	t.Parallel()
	// Bounded() is Deadline non-zero: with one is executing, without is parked or restored.
	executing := runlease.Lease{WorkflowID: "wf_1", ChatID: "c1", Deadline: time.Now().Add(time.Hour)}
	parked := runlease.Lease{WorkflowID: "wf_2", ChatID: "c1"}
	parentless := runlease.Lease{WorkflowID: "wf_3"}
	otherChat := runlease.Lease{WorkflowID: "wf_4", ChatID: "c2", Deadline: time.Now().Add(time.Hour)}

	tests := map[string]struct {
		leases []runlease.Lease
		chatID marotte.ChatID
		want   bool
		why    string
	}{
		"an executing run of this chat holds it": {
			leases: []runlease.Lease{executing},
			chatID: "c1",
			want:   true,
			why:    "the launching turn ended and the run did not",
		},
		"a PARKED run of this chat holds it too": {
			leases: []runlease.Lease{parked},
			chatID: "c1",
			want:   true,
			why: "a run stopped on a person is exactly what a finished notification must not " +
				"claim is over; Bounded() reads false here, which is why it is not consulted",
		},
		"a run of a DIFFERENT chat does not hold it": {
			leases: []runlease.Lease{otherChat},
			chatID: "c1",
			want:   false,
			why:    "one chat's run may never withhold another chat's notification",
		},
		"a parentless run holds nothing": {
			leases: []runlease.Lease{parentless},
			chatID: "c1",
			want:   false,
			why:    "a manual or scheduled launch has no chat, so its empty ChatID matches nobody",
		},
		"an empty chat id matches no lease": {
			leases: []runlease.Lease{parentless, executing},
			chatID: "",
			want:   false,
			why:    "the empty id is not a chat, and it must not collide with a parentless lease",
		},
		"no leases at all": {
			leases: nil,
			chatID: "c1",
			want:   false,
			why:    "nothing on the wire means nothing outstanding",
		},
		"this chat's run is found behind other chats' runs": {
			leases: []runlease.Lease{otherChat, parentless, parked},
			chatID: "c1",
			want:   true,
			why:    "the scan reads every lease rather than only the first",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := chatHoldsLiveRun(tc.leases, tc.chatID); got != tc.want {
				t.Errorf("chatHoldsLiveRun(%d leases, %q) = %v, want %v (%s)",
					len(tc.leases), tc.chatID, got, tc.want, tc.why)
			}
		})
	}
}
