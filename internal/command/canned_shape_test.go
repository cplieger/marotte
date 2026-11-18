package command

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// The client's canned set must be what these handlers write: its read-budget tests replay it as the server.
func TestCannedAnswers_ReadAsTheHandlersWrite(t *testing.T) {
	type openTabAnswer struct {
		Subject marotte.TabSubject `json:"subject"`
		Version uint64             `json:"version"`
		OK      bool               `json:"ok"`
		Created bool               `json:"created"`
	}
	testsupport.HoldCanned(t, "POST /api/command", func(a *testsupport.CannedAnswer) error {
		var cmd marotte.ClientCommand
		if err := json.Unmarshal(a.Request, &cmd); err != nil {
			return fmt.Errorf("its request is no command: %w", err)
		}
		if cmd.Type != marotte.CmdOpenTab {
			return fmt.Errorf("records a %s command, whose answer no test here holds", cmd.Type)
		}
		var v openTabAnswer
		return testsupport.ReadsBackAs(a.Body, &v)
	})
}
