package chat

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// The client's canned set must be what these handlers write: its read-budget tests replay it as the server.
func TestCannedAnswers_ReadAsTheHandlersWrite(t *testing.T) {
	type turnsAnswer struct {
		Turns []marotte.TurnSummary `json:"turns"`
	}
	testsupport.HoldCannedShape[turnsAnswer](t, "GET /api/chats/{chat}/turns")
}
