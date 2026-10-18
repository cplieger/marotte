package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/runesafe/v3"
)

const jsonRPCVersion = "2.0"

// bridgeExitedResp is the pointer-identity sentinel the exit path sends to each pending entry. callAt maps it to
// errBridgeExited, so a dead engine and a Stop return one sentinel.
var bridgeExitedResp = &marotte.RPCResponse{
	Error: &marotte.RPCError{Code: marotte.RPCCodeBridgeExited, Message: "ACP bridge exited"},
}

// frameTooLargeResp is the sentinel pushed to the entries a dropped oversize frame may have answered. The engine and
// session survive, so callAt maps it to a non-retryable error, unlike bridgeExitedResp.
var frameTooLargeResp = &marotte.RPCResponse{
	Error: &marotte.RPCError{Code: marotte.RPCCodeInternal, Message: marotte.ErrFrameTooLarge.Error()},
}

// errWriteFenced refuses a frame once the read loop exited: nothing is left to read it, so it is never written.
var errWriteFenced = errors.New("ACP bridge exited before the frame was written")

// pendingReply is the answer to one of marotte's requests plus the position of its lane when it arrived: the
// response skips the frames queued before it, so the position is captured with it.
type pendingReply struct {
	resp *marotte.RPCResponse
	seq  uint64
}

// pendingEntry is one request awaiting its response. lane is the lane whose positions the reply is stamped with. A
// caller whose ctx ended marks an entry with a late hook abandoned instead of deleting it, and the reply goes to the
// hook on its own goroutine.
type pendingEntry struct {
	ch        chan pendingReply
	lane      *lane
	late      func(resp *marotte.RPCResponse)
	sessionID marotte.SessionID
	abandoned bool
}

func (e *Engine) readLoop() {
	var tracker parseErrTracker
	var reason exitReason
	for {
		line, dropped, head, err := e.stdout.readFrame()
		if dropped > 0 {
			e.reportDroppedFrame(dropped, head)
		}
		if err != nil {
			reason = readErrorReason(err)
			logReadError(err)
			break
		}
		if dropped > 0 {
			continue // the frame's bytes are gone; there is nothing to parse
		}
		var msg marotte.RPCResponse
		if uErr := json.Unmarshal(line, &msg); uErr != nil {
			if e.recordParseError(&tracker, len(line), uErr) {
				reason = ExitParseBreaker
				break
			}
			e.reportUndecodedFrame(line)
			continue
		}
		tracker.Reset()
		if !e.dispatch(&msg) {
			reason = ExitLaneOverflow
			break
		}
	}
	e.recordExit(reason)
	e.exitPath()
	// Async: teardown waits on cmd.Wait, which waits on this goroutine's pipe drain.
	go e.teardown()
}

func readErrorReason(err error) exitReason {
	switch {
	case err == nil || errors.Is(err, io.EOF):
		return ExitEnded
	case errors.Is(err, errFrameDrainExhausted):
		return exitDrainExhausted
	default:
		return exitReadError
	}
}

// exitPath runs once per engine: the exit hook first, then every pending request fails and every lane closes, so the
// hook's snapshot sees no change the end caused.
func (e *Engine) exitPath() {
	e.exitOnce.Do(func() {
		if e.onExit != nil {
			e.mu.Lock()
			info := e.exitInfoLocked()
			e.mu.Unlock()
			e.onExit(info)
		}
		if failed := e.closePending(); failed > 0 {
			slog.Warn("the agent engine exited with requests in flight; failed them",
				"generation", e.generation, "failed_requests", failed)
		}
		e.lanesMu.Lock()
		sessions := make([]*Session, 0, len(e.sessions))
		for id, s := range e.sessions {
			sessions = append(sessions, s)
			delete(e.sessions, id)
		}
		e.lanesMu.Unlock()
		for _, s := range sessions {
			s.engineEnded()
		}
		e.engineLane.close()
		close(e.exited)
	})
}

// closePending refuses every later frame and fails every pending request as exited, so a request the engine settles
// gets exactly one reply.
func (e *Engine) closePending() int {
	// Before any lock: a fence published behind pendingMu would let a frame write after the reader is gone.
	e.readerExited.Store(true)
	// Waits out a frame admitted before the fence, so it is wholly written before the drain or refused after: a request
	// registers before its write, so every written one is in the map failPending drains.
	e.writeMu.Lock()
	e.writeMu.Unlock() //nolint:gocritic,staticcheck // badLock, SA2001: the empty section is that wait.
	return e.failPending(bridgeExitedResp, matchAll)
}

// failPending answers resp to every entry match selects and removes it, returning how many it answered. An abandoned
// entry is dropped: its caller is gone and nothing is left to close on an ended engine.
func (e *Engine) failPending(resp *marotte.RPCResponse, match func(id int64, entry *pendingEntry) bool) int {
	e.pendingMu.Lock()
	defer e.pendingMu.Unlock()
	n := 0
	for id, entry := range e.pending {
		if !match(id, entry) {
			continue
		}
		delete(e.pending, id)
		if entry.abandoned {
			continue
		}
		n++
		// A failure carries the lane's position too, so a settle does not wait for a position the dropped frame took.
		entry.ch <- pendingReply{resp: resp, seq: entry.lane.position()}
	}
	return n
}

func matchAll(int64, *pendingEntry) bool { return true }

// undecodedResp is what a pending request gets when its response arrived but could not be decoded.
var undecodedResp = &marotte.RPCResponse{
	Error: &marotte.RPCError{Code: marotte.RPCCodeParseError, Message: "marotte could not decode kiro-cli's answer"},
}

// reportUndecodedFrame settles what a frame that fits but fails to decode concerned, judged from its head: a request is
// refused by its id and a response fails its one request. Unlike reportDroppedFrame, a notification or an
// unattributable frame fails nothing, since a turn carries on without one update.
func (e *Engine) reportUndecodedFrame(line []byte) {
	h := classifyDroppedHead(line[:min(len(line), droppedHeadCap)])
	failed := 0
	switch h.kind {
	case droppedRequest:
		e.refuseRequest(*h.id, h.method, &marotte.RPCError{
			Code: marotte.RPCCodeParseError, Message: "marotte could not decode this request",
		})
	case droppedResponse:
		id := *h.id
		failed = e.failPending(undecodedResp, func(pid int64, _ *pendingEntry) bool { return pid == id })
	default:
		return
	}
	slog.Error("ACP parse: settled the request an undecodable frame concerned",
		"kind", string(h.kind), "method", logsafe.Field(h.method), "request_id", *h.id, "failed_requests", failed)
}

// refuseRequest answers, off the read loop, a request marotte cannot deliver, as answerRefused does.
func (e *Engine) refuseRequest(id int64, method string, rpcErr *marotte.RPCError) {
	e.goBackground(func() { e.answerRefused(id, method, rpcErr) })
}

// answerRefused answers a request marotte cannot deliver with rpcErr. A permission ask is answered cancelled, never
// with an error: KAS's turn approval treats an error answer as approval of every staged write.
func (e *Engine) answerRefused(id int64, method string, rpcErr *marotte.RPCError) {
	if method == marotte.MethodRequestPermission {
		_ = e.Respond(e.lifecycleCtx, id, marotte.PermissionOutcomeCancelled(), nil)
		return
	}
	_ = e.Respond(e.lifecycleCtx, id, nil, rpcErr)
}

// recordParseError feeds an unmarshal failure to the parse-error tracker and logs it, returning true when the circuit
// breaker tripped and the read loop should end the engine.
func (*Engine) recordParseError(tracker *parseErrTracker, lineLen int, err error) bool {
	switch tracker.record() {
	case parseErrLog:
		slog.Error("ACP parse", "error", err, "line_len", lineLen)
	case parseErrSummarize:
		slog.Error("ACP parse storm",
			"count", tracker.summaryCount(),
			"window_s", int(parseErrWindow/time.Second))
	case parseErrCircuitBreak:
		slog.Error("ACP parse: consecutive-error ceiling reached; reaping the agent engine",
			"consecutive", tracker.consecutive)
		return true
	}
	return false
}

// dispatch routes a decoded frame: a response to its pending entry, anything else to a lane. It reports false when the
// engine must end (the engine lane's notification backlog overflowed).
func (e *Engine) dispatch(msg *marotte.RPCResponse) bool {
	if msg.ID != nil && msg.Method == "" {
		e.deliverResponse(msg)
		return true
	}
	head := headOf(msg)
	e.lanesMu.Lock()
	s := e.sessions[head.SessionID]
	e.lanesMu.Unlock()
	target := marotte.SessionID("")
	if e.router != nil {
		target = e.router.Route(&head)
	}
	if s == nil && target != "" {
		e.lanesMu.Lock()
		s = e.sessions[target]
		e.lanesMu.Unlock()
	}
	if s != nil {
		return e.appendSession(s, msg)
	}
	return e.appendEngine(msg)
}

// deliverResponse hands a response to its pending entry under pendingMu, so a caller giving up at the same instant
// either abandons the entry first or finds the reply waiting.
func (e *Engine) deliverResponse(msg *marotte.RPCResponse) {
	e.pendingMu.Lock()
	entry, ok := e.pending[*msg.ID]
	if ok {
		delete(e.pending, *msg.ID)
	}
	if !ok {
		e.pendingMu.Unlock()
		return
	}
	reply := pendingReply{resp: msg, seq: entry.lane.position()}
	if !entry.abandoned {
		entry.ch <- reply
		e.pendingMu.Unlock()
		return
	}
	e.pendingMu.Unlock()
	if entry.late != nil {
		late := entry.late
		e.goBackground(func() { late(msg) })
	}
}

// appendSession queues msg on s's lane. At the hard cap the session alone fails: its lane is detached and its queued
// requests go to the detach handler, which answers each once.
func (e *Engine) appendSession(s *Session, msg *marotte.RPCResponse) bool {
	switch s.lane.push(msg) {
	case appendedBacklog:
		slog.Warn("engine lane backlog", "session_id", s.id, "queued", laneWarnAt)
	case refusedClosed:
		return e.appendEngine(msg)
	case refusedFull:
		e.detachSession(s, msg)
	}
	return true
}

// appendEngine queues msg on the engine lane. At the hard cap a request is answered at once instead of queued, and
// a notification ends the engine: the engine lane carries workspace state a fresh engine re-sends.
func (e *Engine) appendEngine(msg *marotte.RPCResponse) bool {
	switch e.engineLane.push(msg) {
	case appendedBacklog:
		slog.Warn("engine lane backlog", "session_id", "", "queued", laneWarnAt)
	case refusedFull:
		if msg.ID == nil {
			slog.Error("the agent engine's own lane stopped draining; ending the engine",
				"generation", e.generation, "queued", laneHardCap)
			return false
		}
		id := *msg.ID
		slog.Error("marotte is overloaded and refused a request from the agent engine",
			"generation", e.generation, "method", msg.Method, "request_id", id)
		e.refuseRequest(id, msg.Method, &marotte.RPCError{
			Code: marotte.RPCCodeInternal, Message: "marotte is overloaded and could not take this request",
		})
	case refusedClosed:
		// The exit path closed it: nothing is left to read the frame.
	}
	return true
}

// detachSession cuts s's lane loose at its hard cap, as one operation: s leaves the lane table, so every later frame
// naming it reaches the engine lane; the lane refuses appends; its queued frames, the pump's hand-back and the frame
// that overflowed go to the detach handler, requests first in order, off the read loop.
func (e *Engine) detachSession(s *Session, overflow *marotte.RPCResponse) {
	e.lanesMu.Lock()
	if e.sessions[s.id] == s {
		delete(e.sessions, s.id)
	}
	e.lanesMu.Unlock()
	queued := s.lane.detach()
	e.goBackground(func() {
		frames := make([]marotte.Notification, 0, len(queued)+2)
		if hb := s.lane.handedBack(); hb != nil {
			frames = append(frames, *hb)
		}
		frames = append(frames, queued...)
		frames = append(frames, marotte.Notification{Msg: overflow})
		d := DetachedSession{SessionID: s.id}
		for _, f := range frames {
			if f.Msg.ID != nil && f.Msg.Method != "" {
				d.Requests = append(d.Requests, f.Msg)
			} else {
				d.DroppedNotifications++
			}
		}
		s.detached()
		if e.onDetach != nil {
			e.onDetach(d)
			return
		}
		for _, req := range d.Requests {
			e.answerRefused(*req.ID, req.Method, &marotte.RPCError{
				Code: marotte.RPCCodeInternal, Message: "marotte dropped this chat's session because it stopped reading; send the message again",
			})
		}
	})
}

// logReadError: a clean EOF logs nothing; an exhausted drain budget gets its own message, since the stream stopped
// being JSON lines.
func logReadError(err error) {
	if err == nil || errors.Is(err, io.EOF) {
		return
	}
	if errors.Is(err, errFrameDrainExhausted) {
		slog.Error("ACP read: a single frame never terminated within the drain budget; reaping the agent engine",
			"budget_bytes", oversizeDrainCap)
		return
	}
	slog.Error("ACP read", "error", err)
}

// register adds a pending entry for id on l, owned by sessionID ("" for the engine).
func (e *Engine) register(id int64, l *lane, sessionID marotte.SessionID, late func(*marotte.RPCResponse)) *pendingEntry {
	entry := &pendingEntry{ch: make(chan pendingReply, 1), lane: l, sessionID: sessionID, late: late}
	e.pendingMu.Lock()
	e.pending[id] = entry
	e.pendingMu.Unlock()
	return entry
}

func (e *Engine) deregisterPending(id int64) {
	e.pendingMu.Lock()
	delete(e.pending, id)
	e.pendingMu.Unlock()
}

// Call sends a connection-level JSON-RPC request, positioned on the engine lane, and waits for its response or the
// engine's end. There is no in-Call timeout: ctx bounds the wait.
func (e *Engine) Call(ctx context.Context, method string, params any) (*marotte.RPCResponse, error) {
	resp, _, err := e.CallAt(ctx, method, params)
	return resp, err
}

// CallAt is Call plus the engine lane's position when the response arrived. A cancelled ctx abandons the request and
// returns position zero.
func (e *Engine) CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error) {
	return e.callAt(ctx, e.engineLane, "", method, params, nil)
}

// callAt writes one request positioned on l and waits for its outcome. Once written, every engine-side outcome (the
// response, an oversize failure, the exit path) arrives through the entry and carries l's position. A commit ctx
// carries that refuses the write is answered as is, with nothing written.
func (e *Engine) callAt(
	ctx context.Context, l *lane, sessionID marotte.SessionID, method string, params any, late func(*marotte.RPCResponse),
) (*marotte.RPCResponse, uint64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	id := e.nextID.Add(1)
	data, err := json.Marshal(marotte.RPCRequest{JSONRPC: jsonRPCVersion, ID: id, Method: method, Params: params})
	if err != nil {
		return nil, 0, err
	}
	entry := e.register(id, l, sessionID, late)
	refused, writeErr := e.writeFrameIf(append(data, '\n'), marotte.WriteCommit(ctx))
	if refused != nil {
		e.deregisterPending(id)
		return nil, 0, refused
	}
	if writeErr != nil {
		e.deregisterPending(id)
		return nil, 0, &marotte.TransportError{Err: fmt.Errorf("write to ACP: %w", writeErr), Retryable: true}
	}
	select {
	case reply := <-entry.ch:
		return replyOutcome(reply)
	case <-ctx.Done():
		e.pendingMu.Lock()
		if _, ok := e.pending[id]; ok {
			if late != nil {
				entry.abandoned = true
			} else {
				delete(e.pending, id)
			}
			e.pendingMu.Unlock()
			return nil, 0, ctx.Err()
		}
		e.pendingMu.Unlock()
		// deliverResponse took the entry before the abandon, and sends under pendingMu, so the reply is waiting.
		return replyOutcome(<-entry.ch)
	}
}

func replyOutcome(reply pendingReply) (*marotte.RPCResponse, uint64, error) {
	resp := reply.resp
	switch {
	case resp == bridgeExitedResp:
		return nil, reply.seq, &marotte.TransportError{Err: errBridgeExited, Retryable: true}
	case resp == frameTooLargeResp:
		// Not retryable: the same prompt would likely produce the same oversize payload (marotte.ErrFrameTooLarge).
		return nil, reply.seq, &marotte.TransportError{Err: marotte.ErrFrameTooLarge, Retryable: false}
	case resp.Error != nil:
		if resp.Error.Code == marotte.RPCCodeSessionBusy {
			return resp, reply.seq, fmt.Errorf("ACP error %d: %w: %w", resp.Error.Code, marotte.ErrSessionBusy, resp.Error)
		}
		return resp, reply.seq, fmt.Errorf("ACP error %d: %w", resp.Error.Code, resp.Error)
	}
	return resp, reply.seq, nil
}

// Notify sends a JSON-RPC notification (no response expected).
func (e *Engine) Notify(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(marotte.RPCNotification{JSONRPC: jsonRPCVersion, Method: method, Params: params})
	if err != nil {
		return err
	}
	return e.writeFrame(append(data, '\n'))
}

// maxRespondErrorBytes bounds a generic error message on the wire: it can interpolate a model-chosen value (an fs
// path) that KAS feeds back into the model's tool result. A *marotte.RPCError's app-authored message is exempt.
const maxRespondErrorBytes = 256

// Respond writes a JSON-RPC response to a request from kiro-cli. Set exactly one of result or err. Errors use -32603
// unless err unwraps to a *marotte.RPCError with its own code. Once the read loop has exited it writes nothing and
// returns an error wrapping marotte.ErrBridgeExited: no agent is left to read it.
func (e *Engine) Respond(ctx context.Context, id int64, result any, err error) error {
	if cErr := ctx.Err(); cErr != nil {
		return cErr
	}
	resp := marotte.RPCResponseOut{JSONRPC: jsonRPCVersion, ID: id}
	if err != nil {
		code := marotte.RPCCodeInternal
		msg := runesafe.SanitizeSingleLineBounded(err.Error(), maxRespondErrorBytes)
		if re, ok := errors.AsType[*marotte.RPCError](err); ok {
			code = re.Code
			msg = re.Message
		}
		resp.Error = &marotte.RPCErrorOut{Code: code, Message: msg}
	} else {
		resp.Result = result
	}
	data, mErr := json.Marshal(resp)
	if mErr != nil {
		return mErr
	}
	wErr := e.writeFrame(append(data, '\n'))
	if errors.Is(wErr, errWriteFenced) {
		return fmt.Errorf("respond on ACP: %w", errBridgeExited)
	}
	return wErr
}

// writeChunk is the unit each no-progress bound covers.
const writeChunk = 64 << 10

// writeStall bounds one chunk's write to kiro-cli's stdin; a backstop, so not a setting, and a var for tests. writeMu
// serialises every outbound frame, so one wedged write silently blocks every later Respond, Call and Notify,
// session/cancel included. A reader that keeps draining is never reaped, however long the frame takes.
var writeStall = 30 * time.Second

// writeFrame serialises stdin writes, refuses each once the read loop exited (readerExited, loaded under writeMu so
// closePending's wait orders it), and writes in chunks, each bounded by writeStall. Any write failure ends the engine:
// a truncated frame desyncs kiro-cli's stdin scanner for good, and an error means it can read nothing after.
func (e *Engine) writeFrame(data []byte) error {
	_, err := e.writeFrameIf(data, nil)
	return err
}

// writeFrameIf is writeFrame with commit (marotte.WithWriteCommit) run under writeMu just before the first byte: its
// error is answered as refused and nothing is written.
func (e *Engine) writeFrameIf(data []byte, commit func() error) (refused, err error) {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	if e.readerExited.Load() {
		return nil, errWriteFenced
	}
	// Before anything touches the handle: a write before Start once panicked on a nil interface.
	pipe := e.stdin.Load()
	if pipe == nil {
		return nil, errBridgeNotStarted
	}
	if commit != nil {
		if r := commit(); r != nil {
			return r, nil
		}
	}
	dw, hasDeadline := pipe.w.(deadlineWriter)
	if hasDeadline {
		// Cleared on exit: a stale absolute deadline would expire a later healthy write.
		defer func() { _ = dw.SetWriteDeadline(time.Time{}) }()
	}
	for off := 0; off < len(data); {
		chunk := data[off:min(off+writeChunk, len(data))]
		if hasDeadline {
			_ = dw.SetWriteDeadline(time.Now().Add(writeStall))
		}
		n, err := pipe.w.Write(chunk)
		off += n
		if err == nil && n == len(chunk) {
			continue
		}
		reason := exitWriteFailed
		switch {
		case errors.Is(err, os.ErrDeadlineExceeded):
			reason = ExitStdinStalled
			slog.Error("ACP write: kiro-cli stopped draining stdin within the stall window; reaping the agent engine",
				"stall_s", int(writeStall/time.Second), "wrote_bytes", off, "frame_bytes", len(data))
		case err == nil:
			err = fmt.Errorf("short write to ACP stdin: %d of %d bytes", off, len(data))
			slog.Error("ACP write: short write left a partial frame in kiro-cli's stdin; reaping the agent engine",
				"wrote_bytes", off, "frame_bytes", len(data))
		default:
			slog.Error("ACP write failed; reaping the agent engine", "wrote_bytes", off, "frame_bytes", len(data), "error", err)
		}
		e.fail(reason)
		return nil, fmt.Errorf("%w: %w", errBridgeExited, err)
	}
	return nil, nil
}

// deadlineWriter is the part of a stdin handle a write deadline needs: cmd.StdinPipe's *os.File has it, the test
// fixtures do not.
type deadlineWriter interface {
	SetWriteDeadline(time.Time) error
}
