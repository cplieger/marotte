package agent

import (
	"errors"
	"net/http"
)

// principal is who a request acts for, the owner a request-scoped connection records.
type principal string

// operator is marotte's one principal: with no sign-in of its own, every request acts for the operator.
const operator principal = "operator"

// principalOf is where a request's principal is read; a sign-in changes this function alone.
func principalOf(*http.Request) principal { return operator }

var errNotOwner = errors.New("scope request from a principal that does not own the connection")

// ownsConnection is the scope route's authorize: a caller may replace only the scope of a connection its own
// principal opened. It judges ownership, never topics (sse's ScopeHandler contract).
func ownsConnection(r *http.Request, owner any) error {
	if owner != any(principalOf(r)) {
		return errNotOwner
	}
	return nil
}
