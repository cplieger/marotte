package command

import (
	"net/http"
	"testing"

	"github.com/cplieger/marotte/internal/testsupport"
)

// The 409 for a prompt into a deleted chat names its class. Without the reason
// the client reads every 409 that is not "starting" as a steerable busy turn and
// converts the prompt to a steer, so the refusal carries chat_not_found and the
// client ends the send instead of retrying it.
func TestCmdPrompt_ATombstonedChatCarriesTheChatNotFoundReason(t *testing.T) {
	spy := &promptSpy{hostDouble: newTestHost(t, tombstonedChats{testsupport.NewInMemoryChatStore()})}
	roles := promptRolesOf(spy)
	roles.bridges = spy
	roles.bus = spy

	_, err := cmdPrompt(t.Context(), roles, promptReq(t, "c1", "do the thing"))

	if got := statusOf(err); got != http.StatusConflict {
		t.Fatalf("CmdPrompt on a tombstoned chat = %d, want %d (%s)", got, http.StatusConflict, errText(err))
	}
	if got := reasonOf(err); got != reasonChatGone {
		t.Errorf("reason = %q, want %q", got, reasonChatGone)
	}
}
