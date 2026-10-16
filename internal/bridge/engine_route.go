package bridge

import (
	"encoding/json"

	"github.com/cplieger/marotte/internal/marotte"
)

// FrameHead is what the read loop decodes of a request or notification to route it, before the next frame is read.
type FrameHead struct {
	Method          string
	SessionID       marotte.SessionID
	WorkflowID      string
	ParentSessionID marotte.SessionID
	// Replay is a session/update KAS marked as part of a session/load replay.
	Replay bool
}

// Router places the frames no session lane claims, and learns its tables from every frame. It is called on the read
// loop for every request and notification, in pipe order, so a table it writes is visible to the next frame. It must
// not block or call back into the Engine.
type Router interface {
	// Route returns the session whose lane takes the frame, or "" for the engine lane. An answer naming no session lane
	// also means the engine lane.
	Route(h *FrameHead) marotte.SessionID
}

// frameParams is the part of a frame's params the head reads; one decode per frame, beside the full one.
type frameParams struct {
	SessionID       marotte.SessionID `json:"sessionId"`
	WorkflowID      string            `json:"workflowId"`
	ParentSessionID marotte.SessionID `json:"parentSessionId"`
	Update          struct {
		Meta struct {
			Kiro struct {
				Replay bool `json:"replay"`
			} `json:"kiro"`
		} `json:"_meta"`
	} `json:"update"`
}

// headOf decodes msg's head. Params that do not decode leave the ids empty, which routes the frame to the engine lane.
func headOf(msg *marotte.RPCResponse) FrameHead {
	h := FrameHead{Method: msg.Method}
	if len(msg.Params) == 0 {
		return h
	}
	var p frameParams
	if json.Unmarshal(msg.Params, &p) != nil {
		return h
	}
	h.SessionID, h.WorkflowID, h.ParentSessionID = p.SessionID, p.WorkflowID, p.ParentSessionID
	h.Replay = msg.Method == marotte.MethodSessionUpdate && p.Update.Meta.Kiro.Replay
	return h
}
