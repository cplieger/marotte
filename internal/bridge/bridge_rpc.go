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

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/runesafe/v2"
)

const jsonRPCVersion = "2.0"

// bridgeExitedResp is the pointer-identity sentinel readLoop's exit drain sends to each pending channel. Call maps
// it to errBridgeExited, so a dead kiro-cli and a Stop racing a fresh Call return one sentinel.
var bridgeExitedResp = &marotte.RPCResponse{
	Error: &marotte.RPCError{Code: marotte.RPCCodeBridgeExited, Message: "ACP bridge exited"},
}

// frameTooLargeResp is the sentinel pushed to every pending channel when an oversize frame was dropped. The process
// and session survive, so Call maps it to a non-retryable error, unlike bridgeExitedResp.
var frameTooLargeResp = &marotte.RPCResponse{
	Error: &marotte.RPCError{Code: marotte.RPCCodeInternal, Message: marotte.ErrFrameTooLarge.Error()},
}

// pendingReply is the answer to one of our requests plus the read-loop position when it arrived: the response skips
// the queued notifications before it, so the position is captured with it.
type pendingReply struct {
	resp *marotte.RPCResponse
	seq  uint64
}

// sendNotif stamps the next sequence on a frame and delivers it. readLoop is the only caller, which makes the
// unsynchronized counter sound.
func (b *Bridge) sendNotif(msg *marotte.RPCResponse) {
	b.deliveredSeq++
	select {
	case b.notifCh <- marotte.Notification{Msg: msg, Seq: b.deliveredSeq}:
	case <-b.done:
	}
}

func (b *Bridge) readLoop() {
	if !b.claimNotifClose() {
		return
	}
	defer b.drainPendingAndClose()
	var tracker parseErrTracker
	for {
		line, dropped, err := b.stdout.readFrame()
		if dropped > 0 {
			b.reportDroppedFrame(dropped)
		}
		if err != nil {
			logReadError(err)
			break
		}
		if dropped > 0 {
			continue // the frame's bytes are gone; there is nothing to parse
		}
		var msg marotte.RPCResponse
		if uErr := json.Unmarshal(line, &msg); uErr != nil {
			if b.recordParseError(&tracker, len(line), uErr) {
				return
			}
			continue
		}
		tracker.Reset()
		b.dispatch(&msg)
	}
	// Reap the subprocess and unblock late Call waiters, on clean and error exits alike so no zombie leaks. Async: Stop
	// waits on cmd.Wait, which waits on this goroutine's pipe drain.
	go b.Stop()
}

// reportDroppedFrame makes an oversize frame a visible loss. The dropped bytes may have been the response to a
// request, so every pending request fails: Call has no deadline and a prompt would wait forever. The prompt path
// then fails the turn with marotte.ErrFrameTooLarge's wording while process and session survive. A frame dropped
// with nothing pending shows only in this log line.
func (b *Bridge) reportDroppedFrame(dropped int) {
	failed := b.failPending(frameTooLargeResp)
	slog.Error("ACP read: frame exceeds the size cap; dropped it and failed the pending requests",
		"cap", scannerLineCap,
		"dropped_bytes", dropped,
		"failed_requests", failed)
}

// drainPendingAndClose unblocks in-flight Call waiters with bridgeExitedResp so a dead worker cannot wedge the
// bridge, then closes notifCh; readLoop's deferred cleanup. The stranded count is logged when nonzero: a wedged
// kiro-cli otherwise shows as one prompt line then silence.
func (b *Bridge) drainPendingAndClose() {
	if failed := b.failPending(bridgeExitedResp); failed > 0 {
		slog.Warn("ACP bridge exited with requests in flight; failed them",
			"failed_requests", failed)
	}
	close(b.notifCh)
}

// failPending hands resp to every waiting Call, clears the map and returns how many it answered. The send is
// non-blocking: channels hold one, and a Call that left deregisters itself.
func (b *Bridge) failPending(resp *marotte.RPCResponse) int {
	b.pendingMu.Lock()
	n := len(b.pending)
	// A failure carries the sequence too, so a settle does not wait for a position the dropped frame took.
	reply := pendingReply{resp: resp, seq: b.deliveredSeq}
	for id, ch := range b.pending {
		select {
		case ch <- reply:
		default:
		}
		delete(b.pending, id)
	}
	b.pendingMu.Unlock()
	return n
}

// recordParseError feeds an unmarshal failure to the parse-error tracker and logs it, returning true when the
// circuit breaker tripped and readLoop should reap the bridge.
func (b *Bridge) recordParseError(tracker *parseErrTracker, lineLen int, err error) bool {
	switch tracker.Record() {
	case parseErrLog:
		slog.Error("ACP parse", "error", err, "line_len", lineLen)
	case parseErrSummarize:
		slog.Error("ACP parse storm",
			"count", tracker.SummaryCount(),
			"window_s", int(parseErrWindow/time.Second))
	case parseErrCircuitBreak:
		slog.Error("ACP parse: consecutive-error ceiling reached; reaping bridge",
			"consecutive", tracker.consecutive)
		go b.Stop()
		return true
	}
	return false
}

// dispatch routes a decoded frame: a response to its waiting Call, a kiro-cli request or notification onto notifCh.
func (b *Bridge) dispatch(msg *marotte.RPCResponse) {
	switch {
	case msg.ID != nil && msg.Method == "":
		b.pendingMu.Lock()
		ch, ok := b.pending[*msg.ID]
		if ok {
			delete(b.pending, *msg.ID)
		}
		b.pendingMu.Unlock()
		if ok {
			// Every notification already delivered: the bound this response's settle must reach before deciding the wire never
			// closed the turn.
			ch <- pendingReply{resp: msg, seq: b.deliveredSeq}
		}
	case msg.ID != nil:
		b.sendNotif(msg)
	case msg.Method != "":
		slog.Debug("ACP notification", "method", msg.Method)
		b.sendNotif(msg)
	}
}

// logReadError reports what ended the read loop. A clean EOF logs nothing; an exhausted drain budget gets its own
// message, since the stream stopped being JSON lines.
func logReadError(err error) {
	if err == nil || errors.Is(err, io.EOF) {
		return
	}
	if errors.Is(err, errFrameDrainExhausted) {
		slog.Error("ACP read: a single frame never terminated within the drain budget; reaping bridge",
			"budget_bytes", oversizeDrainCap)
		return
	}
	slog.Error("ACP read", "error", err)
}

// deregisterPending removes a pending request from the map.
func (b *Bridge) deregisterPending(id int64) {
	b.pendingMu.Lock()
	delete(b.pending, id)
	b.pendingMu.Unlock()
}

// Call sends a JSON-RPC request and waits for its response or readLoop's exit, which unblocks every waiter with a
// sentinel. Turns can run for hours, so there is no in-Call timeout; the caller cancels via
// Notify("session/cancel", ...).
func (b *Bridge) Call(ctx context.Context, method string, params any) (*marotte.RPCResponse, error) {
	resp, _, err := b.CallAt(ctx, method, params)
	return resp, err
}

// CallAt is Call plus the read-loop position the response arrived at, for callers ordering a decision against
// frames still in flight (the prompt paths).
func (b *Bridge) CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error) {
	id := b.nextID.Add(1)
	req := marotte.RPCRequest{JSONRPC: jsonRPCVersion, ID: id, Method: method, Params: params}
	ch := make(chan pendingReply, 1)
	b.pendingMu.Lock()
	b.pending[id] = ch
	b.pendingMu.Unlock()
	data, err := json.Marshal(req)
	if err != nil {
		b.deregisterPending(id)
		return nil, 0, err
	}
	data = append(data, '\n')
	if writeErr := b.writeFrame(data); writeErr != nil {
		b.deregisterPending(id)
		return nil, 0, &marotte.TransportError{Err: fmt.Errorf("write to ACP: %w", writeErr), Retryable: true}
	}
	select {
	case reply := <-ch:
		resp := reply.resp
		if resp == bridgeExitedResp {
			return nil, reply.seq, &marotte.TransportError{Err: errBridgeExited, Retryable: true}
		}
		if resp == frameTooLargeResp {
			// Not retryable: the same prompt would likely produce the same oversize payload (marotte.ErrFrameTooLarge).
			return nil, reply.seq, &marotte.TransportError{Err: marotte.ErrFrameTooLarge, Retryable: false}
		}
		if resp.Error != nil {
			// Classified here so callers use errors.Is(err, marotte.ErrNotIdle).
			if resp.Error.Code == marotte.RPCCodeNotIdle {
				return resp, reply.seq, fmt.Errorf("ACP error %d: %w", resp.Error.Code, marotte.ErrNotIdle)
			}
			return resp, reply.seq, fmt.Errorf("ACP error %d: %w", resp.Error.Code, resp.Error)
		}
		return resp, reply.seq, nil
	case <-b.done:
		b.deregisterPending(id)
		return nil, 0, &marotte.TransportError{Err: errBridgeExited, Retryable: true}
	case <-ctx.Done():
		b.deregisterPending(id)
		return nil, 0, ctx.Err()
	}
}

// Notify sends a JSON-RPC notification (no response expected).
func (b *Bridge) Notify(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	req := marotte.RPCNotification{JSONRPC: jsonRPCVersion, Method: method, Params: params}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return b.writeFrame(data)
}

// maxRespondErrorBytes bounds a generic error message on the wire: it can interpolate a model-chosen value (an fs
// path) that KAS feeds back into the model's tool result. Fits an os error with a workspace path. A
// *marotte.RPCError's app-authored message is exempt.
const maxRespondErrorBytes = 256

// Respond writes a JSON-RPC response to a kiro-cli request (fs/read_text_file, fs/write_text_file). Set exactly one
// of result or err. Errors use -32603 unless err unwraps to a *marotte.RPCError with its own code.
func (b *Bridge) Respond(ctx context.Context, id int64, result any, err error) error {
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
	data = append(data, '\n')
	return b.writeFrame(data)
}

// writeFrame serialises stdin writes across Call/Notify/Respond, bounds each by writeDeadline, and treats a partial
// frame as the end of the bridge: a truncated frame desyncs kiro-cli's stdin scanner for good, whether from a short
// write or a peer that stopped reading. Without the deadline one wedged write blocks every later frame,
// session/cancel included, with nothing logged.
func (b *Bridge) writeFrame(data []byte) error {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	// Before anything touches the handle: a write before Start once panicked on a nil interface
	// (marotte.ErrBridgeNotStarted).
	pipe := b.stdin.Load()
	if pipe == nil {
		return errBridgeNotStarted
	}
	// Inside writeMu, so no other frame is in flight on this handle.
	if dw, ok := pipe.w.(deadlineWriter); ok {
		if err := dw.SetWriteDeadline(time.Now().Add(writeDeadline)); err == nil {
			// Cleared on exit: a stale absolute deadline would expire a later healthy write.
			defer func() { _ = dw.SetWriteDeadline(time.Time{}) }()
		}
	}
	n, err := pipe.w.Write(data)
	switch {
	case errors.Is(err, os.ErrDeadlineExceeded):
		// The accepted bytes are a truncated frame, so the bridge is unusable. Reaping fails pending Calls with the
		// retryable dead-bridge sentinel and the session stays loadable for the next prompt.
		slog.Error("ACP write: kiro-cli stopped draining stdin within the deadline; reaping bridge",
			"deadline_s", int(writeDeadline/time.Second),
			"wrote_bytes", n, "frame_bytes", len(data))
		go b.Stop()
		return err
	case err != nil:
		return err
	case n != len(data):
		slog.Error("ACP write: short write left a partial frame in kiro-cli's stdin; reaping bridge",
			"wrote_bytes", n, "frame_bytes", len(data))
		go b.Stop()
		return fmt.Errorf("short write to ACP stdin: %d of %d bytes", n, len(data))
	}
	return nil
}

// deadlineWriter is the part of a stdin handle a write deadline needs: cmd.StdinPipe's *os.File has it, the test
// fixtures do not.
type deadlineWriter interface {
	SetWriteDeadline(time.Time) error
}
