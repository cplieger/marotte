// Package command implements the POST /api/command dispatch table.
// Runtime registers concrete handler functions; the Dispatcher routes
// incoming commands by type and handles envelope-level concerns
// (body parsing, validation).
//
// Idempotency is the Idempotency-Key header middleware
// (internal/server/idempotency.go), not this package.
package command

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"sync"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
	"github.com/cplieger/webhttp/v3"
)

// maxCommandBody caps the whole POST /api/command envelope: the largest
// payload is a prompt's text at MaxPromptBytes (512 KiB) plus path-only
// attachment metadata, so 1 MiB is ~2x headroom.
const maxCommandBody = webhttp.MaxJSONBody

// Handler is the signature for a command handler function. It returns its
// outcome rather than writing to an http.ResponseWriter: the dispatcher
// marshals the body. A nil error means 200 with that body; an error carrying
// a status (see StatusError) sets it, and a bare error is a 500.
type Handler func(ctx context.Context, cmd *marotte.ClientCommand) (any, error)

// statusError carries the HTTP status a handler chose for a failure, plus an
// optional machine-readable reason the error envelope emits as its additive
// `reason` field. The status rides the error per call site rather than a
// sentinel-to-status table, because the same sentinel can mean different
// statuses in different places (e.g. ErrChatNotFound is 409 from a shell
// command but 404 from set_mode).
type statusError struct {
	err    error
	reason string
	// currentHash is the hash a refused compare-and-swap found instead of the
	// one that was claimed; empty elsewhere.
	currentHash string
	// runs names the live runs a refused rewind's cut would stop; nil elsewhere.
	// It sits after the string fields so the struct's pointer-bearing prefix
	// stays at govet fieldalignment's optimum.
	runs []LiveRunRef
	code int
}

func (e *statusError) Error() string { return e.err.Error() }
func (e *statusError) Unwrap() error { return e.err }

// StatusError wraps err with the HTTP status the dispatcher should answer with.
// Exported because Handler is: the runtime registers cmdSwitchModel directly.
func StatusError(code int, err error) error {
	return &statusError{code: code, err: err}
}

// StatusErrorReason is StatusError plus a machine-readable reason the error
// envelope carries beside the prose, so a client can branch on a value
// rather than on error text.
func StatusErrorReason(code int, reason string, err error) error {
	return &statusError{code: code, reason: reason, err: err}
}

// statusErrorRuns is StatusErrorReason plus the live runs a refused rewind names,
// so the client's confirmation can say which work the cut stops.
func statusErrorRuns(code int, reason string, runs []LiveRunRef, err error) error {
	return &statusError{code: code, reason: reason, runs: runs, err: err}
}

// statusErrorCurrentHash is StatusErrorReason plus the hash a refused
// compare-and-swap actually found, so the client can tell the document moved
// from a claim it merely got wrong.
//
// It is deliberately NOT a value a client may re-POST unread: the whole point of
// the swap is that an approval names a version somebody looked at, so the client
// re-reads the document and the reader approves again.
func statusErrorCurrentHash(code int, reason, currentHash string, err error) error {
	return &statusError{code: code, reason: reason, currentHash: currentHash, err: err}
}

// StatusOf answers the HTTP status and machine reason a StatusError* error names; ok is false for any
// other error.
func StatusOf(err error) (status int, reason string, ok bool) {
	se, ok := errors.AsType[*statusError](err)
	if !ok {
		return 0, "", false
	}
	return se.code, se.reason, true
}

// statusOf reports the status a handler outcome is answered with: 200 for
// success, the status the error named, and 500 for an error that named none.
func statusOf(err error) int {
	if err == nil {
		return http.StatusOK
	}
	if se, ok := errors.AsType[*statusError](err); ok {
		return se.code
	}
	return http.StatusInternalServerError
}

// Dispatcher holds the command dispatch table and serves the
// POST /api/command HTTP endpoint.
type Dispatcher struct {
	handlers map[marotte.CommandType]Handler
	// status ends a chat's retained waiting_on_user claim after a command that IS the
	// user answering. Assigned once at registration, before the dispatcher serves, so
	// it is read without mu; commandDischarges is the classification.
	status chatStatus
	// prompts is the prompt path's roles, bound at registration like status, for
	// the close pipeline's resolve and drain.
	prompts *promptRoles
	// merges is bound at registration, like prompts.
	merges *tangentMerges
	mu     sync.RWMutex
}

// New constructs a Dispatcher. A handler's own collaborators arrive at
// registration (see RegisterDefaults).
func New() *Dispatcher {
	return &Dispatcher{handlers: make(map[marotte.CommandType]Handler)}
}

// Register adds a handler for the given command type.
func (d *Dispatcher) Register(t marotte.CommandType, h Handler) {
	d.mu.Lock()
	d.handlers[t] = h
	d.mu.Unlock()
}

// ResolveAfterClose resolves the pending turn ends of the closed turn and older
// under the chat's steer lock, then routes the rows it made unsent into a prompt
// that already started. The zero EndFacts answers when nothing is registered.
func (d *Dispatcher) ResolveAfterClose(ctx context.Context, chatID marotte.ChatID, fence TurnFence) EndFacts {
	if d.prompts == nil || d.prompts.queue == nil {
		return EndFacts{}
	}
	return resolveAfterClose(ctx, d.prompts, chatID, fence)
}

// DrainAfterClose sends at most one prompt for a close: the chat's unread steers,
// else one queued user row after a clean close (see drainAfterClose).
func (d *Dispatcher) DrainAfterClose(ctx context.Context, chatID marotte.ChatID, closed CloseFacts, ends EndFacts) {
	if d.prompts == nil || d.prompts.queue == nil {
		return
	}
	drainAfterClose(ctx, d.prompts, chatID, closed, ends)
}

type errorResponse struct {
	Error string `json:"error"`
	// Reason is the machine-readable refusal class, additive; existing
	// clients ignore it. See StatusErrorReason.
	Reason string `json:"reason,omitempty"`
	// CurrentHash is what a refused compare-and-swap found. See
	// statusErrorCurrentHash.
	CurrentHash string `json:"current_hash,omitempty"`
	// Runs are the live runs a refused rewind's cut would stop. See
	// statusErrorRuns. It sits after the string fields so the struct's
	// pointer-bearing prefix stays at govet fieldalignment's optimum.
	Runs []LiveRunRef `json:"runs,omitempty"`
}

// writeError writes a JSON error response at the status the handler chose (a StatusError*
// error's code and reason, else 500).
// rpcerr.Text (rather than err.Error()) unwraps a bridge Call's -32603
// "Internal error" to the real cause in error.data; it is a no-op for an
// ordinary Go error.
func writeError(w http.ResponseWriter, err error) {
	resp := errorResponse{Error: rpcerr.Text(err)}
	if se, ok := errors.AsType[*statusError](err); ok {
		resp.Reason = se.reason
		resp.Runs = se.runs
		resp.CurrentHash = se.currentHash
	}
	webhttp.WriteJSONStatus(w, statusOf(err), resp)
}

func requireChatID(cmd *marotte.ClientCommand) error {
	if cmd.ChatID == "" {
		return StatusError(http.StatusBadRequest, ErrMissingChatID)
	}
	return nil
}

// ServeHTTP is the POST /api/command HTTP handler.
func (d *Dispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	webhttp.LimitBody(w, r, maxCommandBody)
	var cmd marotte.ClientCommand
	if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
		if maxErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
			slog.Warn("command body too large",
				"limit", maxCommandBody, keyError, maxErr)
			webhttp.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				errorResponse{Error: "request body too large"})
			return
		}
		httpreply.BadRequest(w, "invalid json")
		return
	}

	if cmd.ChatID != "" && !validChatID(cmd.ChatID) {
		httpreply.BadRequest(w, ids.ErrMsgInvalidChatID)
		return
	}

	d.mu.RLock()
	fn, ok := d.handlers[cmd.Type]
	d.mu.RUnlock()
	if !ok {
		httpreply.BadRequest(w, "unknown command: "+string(cmd.Type))
		return
	}

	body, err := fn(r.Context(), &cmd)
	if err != nil {
		writeError(w, err)
		return
	}
	if body == nil {
		body = responseOK
	}
	webhttp.WriteJSON(w, body)
	// After the write, so a fan-out cannot sit between the handler and the ack.
	d.noteAnswer(r.Context(), &cmd)
}

// sessionParams builds the base ACP parameter map with the "sessionId" key
// set from the bridge. Extra key-value pairs are merged in (last-wins).
// Takes the 1-method sessionScoped rather than a whole Bridge: reading an id
// is not a licence to call, notify or take the turn slot.
func sessionParams(b sessionScoped, extra ...map[string]any) map[string]any {
	m := map[string]any{keySessionID: b.SessionID()}
	for _, e := range extra {
		maps.Copy(m, e)
	}
	return m
}
