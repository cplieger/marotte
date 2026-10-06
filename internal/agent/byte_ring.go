// A circular byte buffer keeping a terminal's last N output bytes; reads drop invalid UTF-8
// left by a rune cut at the ring boundary.

package agent

import "strings"

// byteRing is a byte-limited circular buffer keeping the most recent bytes written.
type byteRing struct {
	buf       []byte
	pos       int
	full      bool
	truncated bool
}

// newByteRing creates a ring buffer with the given capacity.
func newByteRing(capacity int) *byteRing {
	return &byteRing{buf: make([]byte, capacity)}
}

// Write appends p, overwriting the oldest bytes. Infallible: storage is preallocated, so it
// deliberately does not implement io.Writer.
func (r *byteRing) Write(p []byte) {
	n := len(p)
	if n == 0 {
		return
	}

	capacity := len(r.buf)
	if n >= capacity {
		// Keep only the trailing `capacity` bytes. An exactly-capacity write into an empty buffer loses nothing.
		if n > capacity || r.full || r.pos > 0 {
			r.truncated = true
		}
		copy(r.buf, p[n-capacity:])
		r.pos = 0
		r.full = true
		return
	}

	if r.full {
		r.truncated = true
	}
	for len(p) > 0 {
		space := capacity - r.pos
		copied := copy(r.buf[r.pos:], p[:min(len(p), space)])
		r.pos += copied
		p = p[copied:]
		if r.pos >= capacity {
			r.pos = 0
			r.full = true
			// Bytes left after the buffer fills evict; filling to exactly capacity evicts nothing.
			if len(p) > 0 {
				r.truncated = true
			}
		}
	}
}

// Bytes returns a chronological copy of the contents.
func (r *byteRing) Bytes() []byte {
	if !r.full {
		out := make([]byte, r.pos)
		copy(out, r.buf[:r.pos])
		return out
	}
	// Wrapped: [pos..end] + [0..pos].
	out := make([]byte, len(r.buf))
	n := copy(out, r.buf[r.pos:])
	copy(out[n:], r.buf[:r.pos])
	return out
}

// String returns the contents with invalid UTF-8 dropped (a rune cut at the ring boundary or
// by a partial write), so JSON persistence and replay never see a fragment.
func (r *byteRing) String() string {
	return strings.ToValidUTF8(string(r.Bytes()), "")
}

// Truncated reports whether any data was evicted.
func (r *byteRing) Truncated() bool { return r.truncated }
