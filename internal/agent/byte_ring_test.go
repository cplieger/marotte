package agent

import "testing"

// Past capacity the ring keeps only the newest bytes, holds exactly capacity, and reports Truncated.
func TestByteRing_WrapBehaviour(t *testing.T) {
	r := newByteRing(4)
	r.Write([]byte("ab")) // partial fill, pos=2
	r.Write([]byte("cd")) // fills + wraps, pos=0, full
	r.Write([]byte("ef")) // overwrites oldest two
	if got := r.String(); got != "cdef" {
		t.Errorf("byteRing wrap String() = %q, want %q", got, "cdef")
	}
	if !r.Truncated() {
		t.Errorf("byteRing.Truncated() = false, want true after wrap")
	}
	if got := len(r.Bytes()); got != 4 {
		t.Errorf("byteRing stored bytes = %d, want 4", got)
	}
}
