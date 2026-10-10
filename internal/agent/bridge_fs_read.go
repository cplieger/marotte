// ACP fs read handlers: https://agentclientprotocol.com/protocol/file-system

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/marotte"
)

// handleFSRequest dispatches fs/* requests asynchronously, reporting whether msg was one.
// Each dispatch recovers a panic into a logged JSON-RPC error.
func (in *inbound) handleFSRequest(_ context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) bool {
	var handler func(context.Context, marotte.ChatID, *marotte.RPCResponse)
	switch msg.Method {
	case marotte.MethodFSRead:
		handler = in.respondFSRead
	case marotte.MethodFSWrite:
		handler = in.respondFSWrite
	default:
		return false
	}
	in.lifetime.inflight.Go(func() {
		// The per-event ctx is cancelled before this handler responds, and Bridge.Respond drops a
		// write on a cancelled ctx.
		ctx, cancel := in.lifetime.derivedContext()
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("fs handler panic",
					"chat_id", chatID, "method", msg.Method, "panic", r)
				in.respondBridge(ctx, chatID, msg, nil, errors.New("internal error"))
			}
		}()
		handler(ctx, chatID, msg)
	})
	return true
}

// respondFSRead handles fs/read_text_file:
//
//	{ sessionId, path, line?: int, limit?: int }
//
// answering { content }. line/limit are 1-indexed and inclusive per ACP.
func (in *inbound) respondFSRead(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	var p struct {
		Line  *int   `json:"line,omitempty"`
		Limit *int   `json:"limit,omitempty"`
		Path  string `json:"path"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		in.respondFSError(ctx, chatID, msg, fmt.Errorf("parse params: %w", err))
		return
	}
	if p.Path == "" {
		in.respondFSError(ctx, chatID, msg, errors.New("path is required"))
		return
	}
	root, rel, release, err := in.lifetime.confineReadable(p.Path)
	if err != nil {
		in.respondFSError(ctx, chatID, msg, err)
		return
	}
	defer release()
	// No ignore filter here: KAS enforces the list. The bound comes from the open descriptor,
	// a FIFO is refused, and every component resolves inside the root.
	data, err := atomicfile.ReadBoundedInRoot(ctx, root, rel, fsReadCap)
	if err != nil {
		if errors.Is(err, atomicfile.ErrFileTooLarge) {
			err = fmt.Errorf("%w: %d", errCapExceeded, fsReadCap)
		}
		in.respondFSError(ctx, chatID, msg, err)
		return
	}
	content := sliceByLines(string(data), p.Line, p.Limit)
	in.respondBridge(ctx, chatID, msg, map[string]any{"content": content}, nil)
}

// sliceByLines returns lines [line, line+limit) 1-indexed; nil means from the start or to
// the end. *limit is only compared against a count, never added to an offset, so it cannot overflow.
func sliceByLines(content string, line, limit *int) string {
	if line == nil && limit == nil {
		return content
	}
	skip := 0
	if line != nil && *line > 0 {
		skip = *line - 1
	}
	lo, n := 0, 0
	for ln := range strings.Lines(content) {
		if n == skip {
			break
		}
		lo += len(ln)
		n++
	}
	if n < skip {
		// The window starts past the last line.
		return ""
	}
	if limit == nil || *limit <= 0 {
		return content[lo:]
	}
	// Stopping on the count makes an absurd *limit harmless.
	hi, taken := lo, 0
	for ln := range strings.Lines(content[lo:]) {
		if taken == *limit {
			break
		}
		hi += len(ln)
		taken++
	}
	return content[lo:hi]
}
