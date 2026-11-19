package agent

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func TestListedConversation_AnswersInAStepHeavyWorkspace(t *testing.T) {
	t.Parallel()
	h, _, br := newTestHub()
	stepHeavyKAS(br, json.RawMessage(`{"sessions":[{"sessionId":"sess_chat","title":"Fix the rail"}]}`))

	if listed, err := h.ListedConversation(t.Context(), "sess_chat"); err != nil || !listed {
		t.Errorf("ListedConversation(chat) = %v, %v; want listed", listed, err)
	}
	if listed, err := h.ListedConversation(t.Context(), "sess_step"); err != nil || listed {
		t.Errorf("ListedConversation(step) = %v, %v; want unlisted: the filtered answer leaves steps out", listed, err)
	}
}

// The rows are trimmed from a kiro-cli 2.29.1 session/list, the shape KAS answers when it drops the step filter:
// the step row carries `_meta.kiro.workflow`, the parent row does not.
func TestListedConversation_RefusesAStepRowTheFilterLetThrough(t *testing.T) {
	t.Parallel()
	h, _, br := newTestHub()
	br.setCallResult(marotte.MethodSessionList, json.RawMessage(`{"sessions":[
		{"sessionId":"sess_parent","title":"New Session","_meta":{"kiro":{"status":"waiting_on_user"}}},
		{"sessionId":"sess_step","title":"askloop · ask","_meta":{"kiro":{
			"workflow":{"workflowId":"wf_1","workflowName":"askloop","nodeId":"ask","nodePath":["wf_1","ask"],"type":"step"},
			"serverSession":{"role":"workflow-step","ownerSessionId":"sess_parent"}}}}]}`))

	if listed, err := h.ListedConversation(t.Context(), "sess_step"); err != nil || listed {
		t.Errorf("ListedConversation(step) = %v, %v; want unlisted", listed, err)
	}
	if listed, err := h.ListedConversation(t.Context(), "sess_parent"); err != nil || !listed {
		t.Errorf("ListedConversation(parent) = %v, %v; want listed", listed, err)
	}
}

func TestListedConversation_AnUnreadableListIsAnError(t *testing.T) {
	t.Parallel()
	h, _, br := newTestHub()
	br.callErrs = map[string]error{marotte.MethodSessionList: errors.New("kas gone")}

	if listed, err := h.ListedConversation(t.Context(), "sess_chat"); err == nil || listed {
		t.Errorf("ListedConversation = %v, %v; want the read failure", listed, err)
	}
}
