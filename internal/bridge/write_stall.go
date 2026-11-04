package bridge

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"time"
)

// stallVerdict is a stallWatch's answer at one write-deadline expiry.
type stallVerdict int

const (
	// stallWait keeps waiting: the window has not ended, or bytes moved in it.
	stallWait stallVerdict = iota
	// stallBusy keeps waiting on an engine that read nothing and sent no frame in the window but whose CPU advanced:
	// an event-loop stall (kirodotdev/Kiro#12261).
	stallBusy
	// stallWedged reaps: nothing moved in the window, and the engine's CPU was flat or unreadable or it sent a frame.
	stallWedged
	// stallLivelock reaps: nothing moved for writeStallCeiling, however busy the engine was.
	stallLivelock
)

// stallWatch decides, at each write-deadline expiry on one chunk, whether the write keeps waiting. No CPU reading
// buys no time: a probe that cannot read the engine's /proc stat reaps as flat CPU does, since that is almost always
// an engine already gone, and waiting holds writeMu against every other frame. Nor does a frame read in the window:
// node's event loop runs, so the reader not moving is the kiro-cli relay, which node's CPU says nothing about.
type stallWatch struct {
	lastMoved time.Time // when the chunk's bytes last moved
	windowEnd time.Time
	next      time.Time // the deadline to arm
	cpu       func() (uint64, bool)
	frame     func() int64 // when readLoop last read a frame
	base      uint64       // the reading the window compares against, valid when haveBase
	baseFrame int64        // frame() when base was read
	haveBase  bool
	moved     bool // bytes moved in the current window
}

func newStallWatch(cpu func() (uint64, bool), frame func() int64, now time.Time) *stallWatch {
	w := &stallWatch{cpu: cpu, frame: frame, lastMoved: now, windowEnd: now.Add(writeStall)}
	w.next = earlier(now.Add(stallProbeAfter), w.windowEnd)
	return w
}

// expired takes an expiry at now after n more bytes moved, arms w.next, and answers whether to keep waiting.
func (w *stallWatch) expired(now time.Time, n int) stallVerdict {
	if n > 0 {
		w.moved, w.lastMoved = true, now
	}
	ceiling := w.lastMoved.Add(writeStallCeiling)
	if !now.Before(ceiling) {
		return stallLivelock
	}
	frame := w.frame()
	cpu, ok := w.cpu()
	if now.Before(w.windowEnd) {
		// The baseline is taken lazily, at stallProbeAfter, so a write that finishes sooner never reads /proc.
		w.base, w.baseFrame, w.haveBase = cpu, frame, ok
		w.next = earlier(w.windowEnd, ceiling)
		return stallWait
	}
	busy := ok && w.haveBase && cpu > w.base && frame == w.baseFrame
	moved := w.moved
	w.base, w.baseFrame, w.haveBase, w.moved = cpu, frame, ok, false
	w.windowEnd = now.Add(writeStall)
	w.next = earlier(w.windowEnd, ceiling)
	switch {
	case moved:
		return stallWait
	case busy:
		return stallBusy
	}
	return stallWedged
}

func earlier(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// writeWatched writes the chunk of data at off under write deadlines and returns the bytes of it written. A deadline
// error comes back, already logged, only once the stallWatch reaps; every other error comes back as Write gave it.
func (e *Engine) writeWatched(w io.Writer, dw deadlineWriter, data []byte, off int) (int, error) {
	chunk := data[off:min(off+writeChunk, len(data))]
	watch := newStallWatch(e.cpuTicks, e.lastFrame.Load, time.Now())
	written := 0
	for {
		_ = dw.SetWriteDeadline(watch.next)
		n, err := w.Write(chunk[written:])
		written += n
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			return written, err
		}
		now := time.Now()
		switch watch.expired(now, n) {
		case stallWait:
		case stallBusy:
			slog.Warn("ACP write: kiro-cli is slow to read its stdin but its engine is working; waiting",
				"waited_s", int(now.Sub(watch.lastMoved)/time.Second), "wrote_bytes", off+written, "frame_bytes", len(data))
		case stallWedged:
			slog.Error("ACP write: kiro-cli read no stdin within the stall window while its engine was idle or still sending frames; reaping the agent engine",
				"stall_s", int(writeStall/time.Second), "wrote_bytes", off+written, "frame_bytes", len(data))
			return written, err
		case stallLivelock:
			slog.Error("ACP write: kiro-cli's engine kept working but read no stdin up to the stall ceiling; reaping the agent engine",
				"ceiling_s", int(writeStallCeiling/time.Second), "wrote_bytes", off+written, "frame_bytes", len(data))
			return written, err
		}
	}
}
