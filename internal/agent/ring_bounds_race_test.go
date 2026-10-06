package agent

import (
	"testing"
)

// TestByteRing_BoundsAfterOverflow pins the ring at capacity after overflow.
func TestByteRing_BoundsAfterOverflow(t *testing.T) {
	const cap = 16
	r := newByteRing(cap)

	bigData := make([]byte, cap*3)
	for i := range bigData {
		bigData[i] = byte(i % 256)
	}
	r.Write(bigData)

	if len(r.Bytes()) != cap {
		t.Fatalf("len(Bytes()) = %d, want %d", len(r.Bytes()), cap)
	}
}

// TestByteRing_ConcurrentWriteRead documents that byteRing is single-goroutine by design;
// callers must synchronize.
func TestByteRing_ConcurrentWriteRead(t *testing.T) {
	// Skipped: the unsynchronized ring would race the test itself.
	t.Skip("byteRing is single-goroutine by design; test documents the contract")
}

// TestByteRing_StringDropsPartialUTF8Leader pins dropping an incomplete leading sequence as well as continuation bytes.
func TestByteRing_StringDropsPartialUTF8Leader(t *testing.T) {
	// 😀 is F0 9F 98 80; a 3-byte ring keeps only continuation bytes, so String() is "".
	r := newByteRing(3)
	r.Write([]byte{0xF0, 0x9F, 0x98, 0x80})
	s := r.String()
	if s != "" {
		t.Fatalf("expected empty string when only continuation bytes remain, got %q (%x)", s, s)
	}
}

// TestByteRing_ExactCapacityWrite pins that an exactly-capacity write fills without evicting.
func TestByteRing_ExactCapacityWrite(t *testing.T) {
	const cap = 8
	r := newByteRing(cap)
	data := []byte("12345678")
	r.Write(data)

	got := r.Bytes()
	if len(got) != cap {
		t.Fatalf("len(Bytes()) = %d, want %d", len(got), cap)
	}
	if string(got) != "12345678" {
		t.Fatalf("Bytes() = %q, want %q", got, "12345678")
	}
	if r.Truncated() {
		t.Fatal("Truncated() = true after an exact-capacity write; nothing was evicted")
	}

	// One more byte overflows.
	r.Write([]byte("9"))
	if !r.Truncated() {
		t.Fatal("Truncated() = false after writing past capacity; data was evicted")
	}
}

// TestByteRing_ExactCapacityAcrossWrites pins that filling exactly across writes also evicts nothing.
func TestByteRing_ExactCapacityAcrossWrites(t *testing.T) {
	const cap = 8
	r := newByteRing(cap)
	r.Write([]byte("1234"))
	r.Write([]byte("5678")) // fills to exactly cap; pos wraps but nothing dropped
	if len(r.Bytes()) != cap {
		t.Fatalf("len(Bytes()) = %d, want %d", len(r.Bytes()), cap)
	}
	if string(r.Bytes()) != "12345678" {
		t.Fatalf("Bytes() = %q, want %q", r.Bytes(), "12345678")
	}
	if r.Truncated() {
		t.Fatal("Truncated() = true after filling to exactly capacity across writes; nothing was evicted")
	}
	// The next byte overwrites the oldest data.
	r.Write([]byte("9"))
	if !r.Truncated() {
		t.Fatal("Truncated() = false after overwriting the full buffer; data was evicted")
	}
}
