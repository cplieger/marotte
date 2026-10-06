package agent

import (
	"net/http"

	"github.com/cplieger/sse"
	"github.com/cplieger/webhttp/v3"
)

// presenceTable is the push presence table: hub connect/disconnect events plus keepalive acks. *push.Presence satisfies it.
type presenceTable interface {
	Observe(ev *sse.PresenceEvent)
	Alive(tag string)
}

// aliveInvalidCode is answered when the SSE-Client header is absent or outside the tag grammar.
const aliveInvalidCode webhttp.ErrorCode = "alive_invalid"

// forwardPresence hands every presence event to the table WithPresence wired, or drops it when none was.
func (b *bus) forwardPresence(ev *sse.PresenceEvent) {
	if b.presence != nil {
		b.presence.Observe(ev)
	}
}

// handleAlive is POST /api/events/alive, the client's receipt for one keepalive, tagged by
// SSE-Client. The tag is validated against the hub's WithClientTag grammar, so a hostile
// header puts no bytes in the table. Answers 204.
func (rt *Runtime) handleAlive(w http.ResponseWriter, r *http.Request) {
	tag := r.Header.Get(clientTagHeader)
	if !webhttp.ValidRequestID(tag) {
		webhttp.WriteError(w, r, http.StatusBadRequest, aliveInvalidCode,
			"SSE-Client header absent or outside [A-Za-z0-9_-]{1,64}")
		return
	}
	if rt.bus.presence != nil {
		rt.bus.presence.Alive(tag)
	}
	w.WriteHeader(http.StatusNoContent)
}
