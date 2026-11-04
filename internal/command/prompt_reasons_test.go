package command

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// clientPromptPath is relative to this package's directory, which is the cwd of a Go test.
const clientPromptPath = "../../static-src/actions/chat.ts"

// Every reason a refused prompt answers is named in the page's prompt action, so a new reason cannot fall into
// the arm another reason owns (a closed chat read as busy steers the text into a chat that is gone).
func TestPromptReasons_EachHasAPageArm(t *testing.T) {
	src, err := os.ReadFile(clientPromptPath)
	if err != nil {
		t.Fatalf("read %s: %v", clientPromptPath, err)
	}
	for _, reason := range []string{reasonTurnLive, reasonStarting, reasonChatGone, reasonChatClosed} {
		eq, arm := "r.reason === "+strconv.Quote(reason), "case "+strconv.Quote(reason)+":"
		if !strings.Contains(string(src), eq) && !strings.Contains(string(src), arm) {
			t.Errorf("%s names no arm (`%s` or `%s`) for the prompt's 409 reason %q", clientPromptPath, eq, arm, reason)
		}
	}
}
