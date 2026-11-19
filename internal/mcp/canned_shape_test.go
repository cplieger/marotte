package mcp

import (
	"testing"

	"github.com/cplieger/marotte/internal/testsupport"
)

// The client's canned set must be what these handlers write: its read-budget tests replay it as the server.
func TestCannedAnswers_ReadAsTheHandlersWrite(t *testing.T) {
	type serversAnswer struct {
		Servers    []*Server `json:"servers"`
		Unreadable int       `json:"unreadable"`
	}
	testsupport.HoldCannedShape[serversAnswer](t, "GET /api/mcp")
	testsupport.HoldCannedShape[identityProviderBody](t, "GET /api/mcp/identity-provider")
}
