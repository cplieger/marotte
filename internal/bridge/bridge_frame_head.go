package bridge

import (
	"bytes"
	"encoding/json"
	"log/slog"

	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
)

// droppedKind is what a dropped frame's head says it was.
type droppedKind string

const (
	droppedResponse     droppedKind = "response"
	droppedRequest      droppedKind = "request"
	droppedNotification droppedKind = "notification"
	droppedUnknown      droppedKind = "unknown"
)

// droppedHead is what the first bytes of a dropped frame name.
type droppedHead struct {
	id        *int64
	kind      droppedKind
	method    string
	sessionID marotte.SessionID
}

// classifyDroppedHead reads the top-level keys of a truncated frame, and the top-level keys of its params, with a
// token decoder: never an unmarshal, since the frame is cut mid-JSON. Work is bounded by the head's length.
func classifyDroppedHead(head []byte) droppedHead {
	var h droppedHead
	sawResult := false
	dec := json.NewDecoder(bytes.NewReader(head))
	dec.UseNumber()
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		h.kind = droppedUnknown
		return h
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			break
		}
		key, _ := keyTok.(string)
		switch key {
		case "id":
			if n, ok := readInt(dec); ok {
				h.id = &n
			}
		case "method":
			h.method = readString(dec)
		case "result", "error":
			sawResult = true
			skipValue(dec)
		case "params":
			h.sessionID = paramsSessionID(dec)
		default:
			skipValue(dec)
		}
	}
	switch {
	case h.method != "" && h.id != nil:
		h.kind = droppedRequest
	case h.method != "":
		h.kind = droppedNotification
	case h.id != nil && sawResult:
		h.kind = droppedResponse
	default:
		h.kind = droppedUnknown
	}
	return h
}

func readInt(dec *json.Decoder) (int64, bool) {
	tok, err := dec.Token()
	if err != nil {
		return 0, false
	}
	num, ok := tok.(json.Number)
	if !ok {
		return 0, false
	}
	n, err := num.Int64()
	return n, err == nil
}

func readString(dec *json.Decoder) string {
	tok, err := dec.Token()
	if err != nil {
		return ""
	}
	s, _ := tok.(string)
	return s
}

// skipValue consumes one value, nested or not; a truncated head ends it early, which the caller's loop sees.
func skipValue(dec *json.Decoder) {
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		switch tok {
		case json.Delim('{'), json.Delim('['):
			depth++
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
		if depth <= 0 {
			return
		}
	}
}

// paramsSessionID reads params' top-level sessionId, skipping every other member.
func paramsSessionID(dec *json.Decoder) marotte.SessionID {
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return ""
	}
	var id marotte.SessionID
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return id
		}
		if key, _ := keyTok.(string); key == marotte.KeySessionID {
			s := readString(dec)
			id = marotte.SessionID(s)
			continue
		}
		skipValue(dec)
	}
	_, _ = dec.Token()
	return id
}

// stepRunner is the Router's optional answer to "which run is this step session's"; the drop line names it.
type stepRunner interface {
	StepRun(sessionID marotte.SessionID) (workflowID string, ok bool)
}

// reportDroppedFrame acts on a frame dropped for size by what its head names: a response fails its one request; a
// request is answered by its id, which is what unwedges KAS; a notification fails only its session's requests, and
// nothing for a session marotte holds no request on (a step's run idle bound recovers it); only an unattributable
// frame fails every request.
func (e *Engine) reportDroppedFrame(dropped int, head []byte) {
	h := classifyDroppedHead(head)
	attrs := []any{
		"cap", scannerLineCap, "dropped_bytes", dropped, "kind", string(h.kind),
		"method", logsafe.Field(h.method), "session_id", logsafe.Field(string(h.sessionID)),
	}
	if h.id != nil {
		attrs = append(attrs, "request_id", *h.id)
	}
	failed := 0
	switch h.kind {
	case droppedResponse:
		id := *h.id
		failed = e.failPending(frameTooLargeResp, func(pid int64, _ *pendingEntry) bool { return pid == id })
	case droppedRequest:
		e.refuseRequest(*h.id, h.method, &marotte.RPCError{
			Code: marotte.RPCCodeInternal, Message: "the request was larger than marotte's 16 MiB frame limit",
		})
	case droppedNotification:
		if h.sessionID == "" {
			failed = e.failPending(frameTooLargeResp, matchAll)
			break
		}
		e.lanesMu.Lock()
		_, registered := e.sessions[h.sessionID]
		e.lanesMu.Unlock()
		if registered {
			sid := h.sessionID
			failed = e.failPending(frameTooLargeResp, func(_ int64, entry *pendingEntry) bool { return entry.sessionID == sid })
			break
		}
		if sr, ok := e.router.(stepRunner); ok {
			if runID, step := sr.StepRun(h.sessionID); step {
				attrs = append(attrs, "run_id", runID)
			}
		}
	default:
		failed = e.failPending(frameTooLargeResp, matchAll)
	}
	slog.Error("ACP read: frame exceeds the size cap; dropped it and failed the pending requests",
		append(attrs, "failed_requests", failed)...)
}
