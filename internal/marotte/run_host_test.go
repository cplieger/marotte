package marotte

import "testing"

// A real chat id never reads as a run host's, and a run host's names its run back.
func TestRunHostChatID_Namespace(t *testing.T) {
	if got := RunHostChatID("wf_1"); got != "run:wf_1" {
		t.Errorf("RunHostChatID = %q, want run:wf_1", got)
	}
	if !IsRunHostChat("run:wf_1") {
		t.Error("run:wf_1 not recognised as a run host chat")
	}
	if got := RunHostWorkflowID("run:wf_1"); got != "wf_1" {
		t.Errorf("RunHostWorkflowID(run:wf_1) = %q, want wf_1", got)
	}
	for _, id := range []ChatID{"c-abc123", "", "wf_1", "running-jokes"} {
		if IsRunHostChat(id) {
			t.Errorf("%q misread as a run host chat", id)
		}
		if got := RunHostWorkflowID(id); got != "" {
			t.Errorf("RunHostWorkflowID(%q) = %q, want empty", id, got)
		}
	}
}
