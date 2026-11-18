package git

import (
	"testing"

	"github.com/cplieger/marotte/internal/testsupport"
)

// The client's canned set must be what these handlers write: its read-budget tests replay it as the server.
func TestCannedAnswers_ReadAsTheHandlersWrite(t *testing.T) {
	type reposAnswer struct {
		Repos []string `json:"repos"`
	}
	testsupport.HoldCannedShape[reposAnswer](t, "GET /api/git/repos")
	testsupport.HoldCannedShape[statusAllResp](t, "GET /api/git/status-all")
	testsupport.HoldCannedShape[statusAllResp](t, "GET /api/git/status-all?fetch")
	testsupport.HoldCannedShape[statusAllResp](t, "GET /api/git/status-all?paths")
}
