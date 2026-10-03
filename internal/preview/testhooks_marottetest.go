//go:build marotte_test

package preview

import (
	"bytes"
	"os"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

var (
	resolverUnavailable atomic.Bool
	resolverBusy        atomic.Bool
	pendingGrowth       atomic.Pointer[growth]
)

type growth struct {
	path  string
	bytes int
}

func init() {
	openat2 = func(dirfd int, path string, how *unix.OpenHow) (int, error) {
		switch {
		case resolverUnavailable.Load():
			return -1, unix.ENOSYS
		case resolverBusy.Load():
			return -1, unix.EAGAIN
		}
		return unix.Openat2(dirfd, path, how)
	}
	sizeAccepted = func() {
		if g := pendingGrowth.Swap(nil); g != nil {
			appendTo(g.path, g.bytes)
		}
	}
}

func appendTo(path string, n int) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return
	}
	_, _ = f.Write(bytes.Repeat([]byte{'+'}, n))
	_ = f.Close()
}

// SetResolverUnavailable makes every resolution answer ENOSYS while on, so an
// end-to-end check can observe the refusal a kernel without openat2 produces.
// Test builds only.
func SetResolverUnavailable(on bool) { resolverUnavailable.Store(on) }

// SetResolverBusy makes every resolution answer EAGAIN while on, the answer a
// rename racing RESOLVE_BENEATH produces, so the retry budget runs out. Test
// builds only.
func SetResolverBusy(on bool) { resolverBusy.Store(on) }

// GrowAfterNextSizeCheck appends n bytes to the file at path between the next
// asset's size check and its serve, once. Test builds only.
func GrowAfterNextSizeCheck(path string, n int) { pendingGrowth.Store(&growth{path: path, bytes: n}) }
