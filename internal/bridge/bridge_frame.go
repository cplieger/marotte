package bridge

// Newline-delimited frame reading off the bridge's stdout. An oversize frame is
// DRAINED to its delimiter, not fatal: ReadSlice leaves the reader mid-line on
// ErrBufferFull, so the stream resynchronises on a real frame boundary.
//
// The drained remainder is DISCARDED, never parsed (it is cut mid-JSON, maybe
// mid-rune). The budget is per FRAME in bytes, so a replay of many terminated
// oversize frames survives. Bridge.Call has no deadline, so readLoop must answer the
// pending requests a discard orphans (reportDroppedFrame); this file only reports.

import (
	"bufio"
	"errors"
)

// oversizeDrainCap bounds the bytes discarded draining one oversize frame: 16x the frame cap, as KiroCrew uses, so a
// merely large frame drains and an unterminated blob is declared garbage.
const oversizeDrainCap = 16 * scannerLineCap

// errFrameDrainExhausted ends the read loop: one frame used the whole drain budget with no terminator, leaving no
// boundary to resynchronise on. Distinct from a read error for the log line.
var errFrameDrainExhausted = errors.New("oversize ACP frame did not terminate within the drain budget")

// Not safe for concurrent use: readLoop alone owns it.
type frameReader struct {
	r   *bufio.Reader
	buf []byte

	// Per-call state reset by readFrame; fields so absorb can be a method, and the buffer is reused across calls.
	dropped  int
	draining bool
}

// newFrameReader wraps r with the stdout buffer size, the ReadSlice window rather than the frame cap.
func newFrameReader(r *bufio.Reader) *frameReader {
	return &frameReader{r: r}
}

// readFrame returns the next frame, the bytes discarded for exceeding scannerLineCap, and a terminal error. A fitting
// frame returns (bytes, 0, nil); an oversize one (nil, n>0, nil) after draining. err is io.EOF, a read error or
// errFrameDrainExhausted. The slice aliases the buffer until the next call, like Scanner.Bytes. The delimiter is
// stripped; an empty frame passes through so json.Unmarshal's failure keeps it in the parse-error breaker.
func (fr *frameReader) readFrame() (frame []byte, dropped int, err error) {
	fr.buf, fr.dropped, fr.draining = fr.buf[:0], 0, false
	for {
		chunk, readErr := fr.r.ReadSlice('\n')
		terminated := readErr == nil
		if terminated {
			chunk = chunk[:len(chunk)-1] // ReadSlice guarantees the last byte is '\n'
		}
		fr.absorb(chunk)
		// dropped is nonzero only while draining.
		if fr.dropped > oversizeDrainCap {
			return nil, fr.dropped, errFrameDrainExhausted
		}
		if terminated {
			return fr.complete()
		}
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		return fr.truncated(readErr)
	}
}

// absorb folds one chunk into the frame in progress, or into the drain count
// once this frame has crossed the cap.
func (fr *frameReader) absorb(chunk []byte) {
	switch {
	case fr.draining:
		fr.dropped += len(chunk)
	case len(fr.buf)+len(chunk) > scannerLineCap:
		// Crossing the cap: drop the prefix too, which would only hand json.Unmarshal a truncated object.
		fr.draining = true
		fr.dropped = len(fr.buf) + len(chunk)
		fr.buf = fr.buf[:0]
	default:
		fr.buf = append(fr.buf, chunk...)
	}
}

// complete answers a frame that reached its terminator: the bytes, or the drop count when over the cap.
func (fr *frameReader) complete() (frame []byte, dropped int, err error) {
	if fr.draining {
		return nil, fr.dropped, nil
	}
	return fr.buf, 0, nil
}

// truncated answers a read that ended without a terminator (io.EOF or a read error). A partial frame is returned
// like Scanner's final token; the next call answers the error.
func (fr *frameReader) truncated(cause error) (frame []byte, dropped int, err error) {
	if !fr.draining && len(fr.buf) > 0 {
		return fr.buf, 0, nil
	}
	return nil, fr.dropped, cause
}
