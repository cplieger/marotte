package marotte

import (
	"encoding/json"
	"errors"
)

// RPCRequest is an outbound JSON-RPC 2.0 request sent to kiro-cli.
type RPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	Params  any    `json:"params,omitempty"`
	Method  string `json:"method"`
	ID      int64  `json:"id"`
}

// RPCResponse is a JSON-RPC 2.0 message from kiro-cli. A populated ID with
// Result or Error is a response; an empty ID with Method and Params is a
// server-sent notification.
type RPCResponse struct {
	Error   *RPCError       `json:"error,omitempty"`
	ID      *int64          `json:"id,omitempty"`
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Notification is one frame a lane delivered, carrying the position the
// engine's read loop stamped on it within that lane.
//
// The sequence travels WITH the frame rather than being recounted by the consumer:
// a counter incremented on receipt skews the moment a frame is dropped or filtered
// between the two goroutines, silently, because both keep rising. A local settle
// compares its captured position against the one the consumer has REACHED, so both
// numbers have to come from one source (Dijkstra and Scholten, EWD687a).
type Notification struct {
	Msg *RPCResponse
	// Seq is the lane's count of frames appended up to and including this one.
	// Monotonic per lane, and comparable only within one lane.
	Seq uint64
}

// RPCError is a JSON-RPC 2.0 error object.
type RPCError struct {
	// Data (below) is where most KAS errors actually say something: a `-32603` puts the real text
	// in Data (`{"details": …}` or a Zod issue array) with Message "Internal error", a `-32602`
	// puts it in Message, and `-32000` uses both. Raw JSON because the shapes share nothing;
	// workflow.Details unwraps both.
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
	Code    int             `json:"code"`
}

// Error makes RPCError implement the error interface by returning the
// server-provided message verbatim. Callers that need the code use
// errors.As to recover the concrete *RPCError.
func (e *RPCError) Error() string {
	return e.Message
}

// ErrorData exposes the raw `error.data` member. An accessor rather than direct
// field access so a decoder can unwrap it without importing this package's
// concrete type — see workflow.Details.
func (e *RPCError) ErrorData() json.RawMessage {
	return e.Data
}

// RPCNotification is an outbound JSON-RPC 2.0 notification (no id, no
// response expected). Used by Bridge.Notify instead of ad-hoc
// map[string]any construction.
type RPCNotification struct {
	Params  any    `json:"params,omitempty"`
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
}

// RPCResponseOut is an outbound JSON-RPC 2.0 response to a request
// received from kiro-cli. Used by Bridge.Respond instead of ad-hoc
// map[string]any construction.
type RPCResponseOut struct {
	Result  any          `json:"result,omitempty"`
	Error   *RPCErrorOut `json:"error,omitempty"`
	JSONRPC string       `json:"jsonrpc"`
	ID      int64        `json:"id"`
}

// RPCErrorOut is the error object in an outbound JSON-RPC response.
type RPCErrorOut struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
}

// ErrSessionBusy is wrapped, beside the raw RPCError, into a Call error whose reply code is
// RPCCodeSessionBusy, so callers classify it with errors.Is.
var ErrSessionBusy = errors.New("session busy")

// ErrBridgeExited is the sentinel for an engine whose read loop has closed for good, and for a
// session handle from an engine that is gone: a Call pending when it died returns it wrapped in a
// TransportError, and a Respond or write after it returns it wrapped directly. Distinct from a
// transient failure because the two want opposite actions: a retry on a dead engine only burns time.
var ErrBridgeExited = errors.New("ACP bridge exited")

// ErrFrameTooLarge is the sentinel a Call returns (wrapped in a NON-retryable TransportError) when a
// stdout frame exceeded the engine's cap and was dropped with this call's answer possibly inside it.
// The engine and session live, so nothing is torn down; the text is user-facing. Not retryable: the
// same turn would likely overflow again.
var ErrFrameTooLarge = errors.New("a message from kiro-cli was too large to read and was dropped, so this turn was stopped")

// ErrBridgeNotStarted is the sentinel every write on an engine returns when there is no process to
// write to: Start has not run, or it failed.
var ErrBridgeNotStarted = errors.New("ACP bridge has not started")

// ErrSessionMetaRefused fails a session/new whose reply named another id than the one marotte
// minted: KAS parses `_meta.kiro` all or nothing, so one field failing its schema dropped them all.
var ErrSessionMetaRefused = errors.New("kiro-cli did not accept this chat's session settings; the server log names them")

// ErrSessionClosing refuses a call on a session whose session/close is written but unconfirmed;
// KAS's requests during the close are still answered.
var ErrSessionClosing = errors.New("the agent engine did not finish closing this chat's session; restart the agent engine from Settings")

// ErrSessionClosed refuses a call on a session that is closed.
var ErrSessionClosed = errors.New("this session is closed")

// TransportError wraps engine-level transport failures (pipe closed,
// write stall, process exited) with explicit retryability semantics.
// Callers use errors.As to classify without substring matching.
type TransportError struct {
	Err       error
	Retryable bool
}

func (e *TransportError) Error() string { return e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }
