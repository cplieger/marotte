package agent

import (
	"bytes"
	"errors"
	"net/http"
	"sync/atomic"
)

// The SSE probe: counters and the close-after hook internal/server's test-only control surface (-tags
// marotte_test) reads and arms. Exported because the server reaches the runtime only through role interfaces.

// SSEClientCount is the number of connections the hub is serving right now.
func (rt *Runtime) SSEClientCount() int { return rt.bus.fanout.ClientCount() }

// SSEConnects reports connects per wire generation: legacy (no SSE-Wire header) and v3.
func (rt *Runtime) SSEConnects() (legacy, v3 uint64) {
	return rt.bus.legacyConnects.Load(), rt.bus.v3Connects.Load()
}

// CloseNextSSEAfter arms the next SSE connection to be cut after its n-th data frame: the write of frame n+1
// fails and the peer sees the stream close between frames. n <= 0 disarms.
func (rt *Runtime) CloseNextSSEAfter(n int) {
	rt.bus.closeAfter.Store(int64(n))
}

// errCloseAfter is what the cut connection's writer answers once its budget is spent.
var errCloseAfter = errors.New("sse: connection cut by the close-after hook")

// The named keepalive carries data:, so it is skipped by name.
var keepaliveFrameStart = []byte("event: " + keepaliveEventName + "\n")

// closeAfterWriter counts data frames and fails the first write past the budget; the retry line and keepalives are not counted.
type closeAfterWriter struct {
	http.ResponseWriter
	remaining int
}

func (w *closeAfterWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("data: ")) && !bytes.HasPrefix(p, keepaliveFrameStart) {
		if w.remaining <= 0 {
			return 0, errCloseAfter
		}
		w.remaining--
	}
	return w.ResponseWriter.Write(p)
}

// Unwrap exposes the underlying writer to http.ResponseController (Flusher, write deadlines).
func (w *closeAfterWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// armedWriter wraps w for the cut when armed, and disarms: the hook is for the next connection only.
func armedWriter(closeAfter *atomic.Int64, w http.ResponseWriter) http.ResponseWriter {
	n := closeAfter.Swap(0)
	if n <= 0 {
		return w
	}
	return &closeAfterWriter{ResponseWriter: w, remaining: int(n)}
}
