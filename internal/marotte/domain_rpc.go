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

// Notification is one frame a bridge delivered, carrying the monotonic
// sequence its read loop stamped on it.
//
// The sequence travels WITH the frame rather than being recounted by the consumer:
// a counter incremented on receipt skews the moment a frame is dropped or filtered
// between the two goroutines, silently, because both keep rising. A local settle
// compares its captured position against the one the consumer has REACHED, so both
// numbers have to come from one source (Dijkstra and Scholten, EWD687a).
type Notification struct {
	Msg *RPCResponse
	// Seq is the read loop's count of frames delivered up to and including this
	// one. Monotonic per bridge and restarting at zero for a new one.
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

// ErrNotIdle is the sentinel for "session not idle" errors from
// kiro-cli. Wraps the raw RPC error when the message contains "not
// idle" but the code isn't RPCCodeNotIdle, unifying the detection
// path for callers that need errors.Is classification.
var ErrNotIdle = errors.New("session not idle")

// ErrBridgeExited is the sentinel a Call returns (wrapped in a TransportError) when the ACP
// subprocess died with the request pending. Distinct from a transient write failure because the two
// want opposite actions: a dead bridge's readLoop has closed for good, so a retry only burns time.
var ErrBridgeExited = errors.New("ACP bridge exited")

// ErrFrameTooLarge is the sentinel a Call returns (wrapped in a NON-retryable TransportError) when
// one stdout frame exceeded the bridge's cap and was dropped. The process and session live, so
// nothing is torn down. Every pending request fails, since the dropped frame cannot be attributed;
// the text is user-facing. Not retryable: the same turn would likely overflow again.
var ErrFrameTooLarge = errors.New("a message from kiro-cli was too large to read and was dropped, so this turn was stopped")

// ErrBridgeNotStarted is the sentinel every write on a bridge returns when there is no subprocess
// to write to: Start has not run, or it failed. A bridge is registered before Start so opens
// coalesce, so a command can hold one mid-spawn. Call wraps it retryable, letting the caller's
// policy decide.
var ErrBridgeNotStarted = errors.New("ACP bridge has not started")

// TransportError wraps bridge-level transport failures (pipe closed,
// write timeout, process exited) with explicit retryability semantics.
// Callers use errors.As to classify without substring matching.
type TransportError struct {
	Err       error
	Retryable bool
}

func (e *TransportError) Error() string { return e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }
