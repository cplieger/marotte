package agent

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
)

// A launch rule's slot that started no run reaches the push and the page under the rule, titled by its recipe, as a
// run outcome, so the workflow run switch in Settings governs it.
func TestNotifyNotStarted_NotifiesUnderTheRule(t *testing.T) {
	h, fp := newRunPushHub(t)
	rule := launchrule.Rule{ID: "r1", Name: "nightly", Source: "bundled://demo"}
	h.runs.NotifyNotStarted(t.Context(), &rule, "engine busy")

	want := runPushSent{
		title: "nightly", body: "Workflow did not start · engine busy",
		kind: marotte.PushKindRunOutcome, subject: marotte.RuleSubject("r1"),
	}
	if got := awaitRunPush(t, fp); got != want {
		t.Errorf("push = %+v, want %+v", got, want)
	}
	frames := eventsOfType(h, marotte.EventNotification)
	if len(frames) != 1 {
		t.Fatalf("notification frames = %d, want 1", len(frames))
	}
	var got marotte.NotificationPayload
	if err := json.Unmarshal(frames[0].Payload, &got); err != nil {
		t.Fatalf("decoding the frame: %v", err)
	}
	if got.PushSubject != marotte.RuleSubject("r1") || got.Body != want.body {
		t.Errorf("the page's frame = %+v, want %q under %+v", got, want.body, marotte.RuleSubject("r1"))
	}
}
