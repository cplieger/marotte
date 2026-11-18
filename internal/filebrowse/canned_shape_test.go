package filebrowse

import (
	"testing"

	"github.com/cplieger/marotte/internal/testsupport"
)

// The client's canned set must be what these handlers write: its read-budget tests replay it as the server.
func TestCannedAnswers_ReadAsTheHandlersWrite(t *testing.T) {
	type filesAnswer struct {
		Path     string      `json:"path"`
		Files    []fileEntry `json:"files"`
		Writable bool        `json:"writable"`
	}
	testsupport.HoldCannedShape[filesAnswer](t, "GET /api/files?path")
}
