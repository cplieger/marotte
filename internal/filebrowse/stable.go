package filebrowse

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"
)

// errChanged reports a file that moved under two consecutive reads.
var errChanged = errors.New("filebrowse: file changed while it was read")

// tooLargeError reports a file over WholeFileMax, carrying its size.
type tooLargeError struct{ size int64 }

func (e *tooLargeError) Error() string {
	return fmt.Sprintf("filebrowse: %d bytes is over the %d-byte viewer cap", e.size, WholeFileMax)
}

// beforeRead is a test seam: it runs between the opening fstat and the read.
var beforeRead = func(*os.File) {}

// readStable reads the whole of f, publishing the bytes only when an fstat before and after
// the read agree (size, mtime and ctime), so a writer caught mid-read never yields a mixture
// under one identity. It retries once, then returns errChanged.
func readStable(ctx context.Context, f *os.File) ([]byte, fs.FileInfo, error) {
	for range 2 {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		before, err := f.Stat()
		if err != nil {
			return nil, nil, err
		}
		if before.Size() > WholeFileMax {
			return nil, before, &tooLargeError{size: before.Size()}
		}
		beforeRead(f)
		data := make([]byte, before.Size())
		n, err := f.ReadAt(data, 0)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, nil, err
		}
		after, err := f.Stat()
		if err != nil {
			return nil, nil, err
		}
		if n == len(data) && sameStat(before, after) {
			return data, after, nil
		}
	}
	return nil, nil, errChanged
}

// sameStat compares the fields any write moves. ctime is in the set because a writer can
// restore mtime with utimes, which itself sets ctime.
func sameStat(a, b fs.FileInfo) bool {
	if a.Size() != b.Size() || !a.ModTime().Equal(b.ModTime()) {
		return false
	}
	sa, okA := a.Sys().(*syscall.Stat_t)
	sb, okB := b.Sys().(*syscall.Stat_t)
	if !okA || !okB {
		return okA == okB
	}
	return sa.Ctim == sb.Ctim && sa.Ino == sb.Ino && sa.Dev == sb.Dev
}

// sniffBinary reads the first binarySniffN bytes of f and reports a NUL among them.
func sniffBinary(f *os.File) (bool, error) {
	head := make([]byte, binarySniffN)
	n, err := f.ReadAt(head, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return bytes.IndexByte(head[:n], 0) >= 0, nil
}
