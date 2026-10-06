// Package procout captures a spawned process's stdout or stderr under a byte cap, so a runaway
// subprocess cannot exhaust memory through a pipe marotte drains. Used by internal/auth and
// internal/server; internal/procgroup bounds the process tree, this bounds the output.
// Write always reports a full write: os/exec drains a non-*os.File writer with io.Copy, which turns
// a short write into io.ErrShortWrite returned by Cmd.Wait for a child that exited 0, or closes the
// pipe so a still-writing child dies of SIGPIPE. Dropped bytes are reported through Truncated
// instead.
package procout

// Buffer is an io.Writer that keeps at most Limit bytes and records whether anything was dropped; a
// non-positive limit captures nothing. Safe as the SAME writer on Cmd.Stdout and Cmd.Stderr
// (os/exec serializes Write) and to read after Cmd.Wait; never share one across commands or read it
// mid-run.
type Buffer struct {
	data      []byte
	limit     int
	truncated bool
}

// NewBuffer returns a Buffer that keeps at most limit bytes.
func NewBuffer(limit int) *Buffer {
	return &Buffer{limit: limit}
}

// Write keeps as much of p as the remaining budget allows and reports a full
// write regardless, so os/exec's copier never turns a reached cap into
// io.ErrShortWrite on a command that succeeded. It never returns an error.
func (b *Buffer) Write(p []byte) (int, error) {
	n := len(p)
	switch room := b.limit - len(b.data); {
	case room <= 0:
		if n > 0 {
			b.truncated = true
		}
	case n > room:
		b.truncated = true
		b.data = append(b.data, p[:room]...)
	default:
		b.data = append(b.data, p...)
	}
	return n, nil
}

// Bytes returns the captured prefix. The slice aliases the Buffer's storage, so
// a caller that retains it past the next Write must copy.
func (b *Buffer) Bytes() []byte { return b.data }

// String returns the captured prefix as a string.
func (b *Buffer) String() string { return string(b.data) }

// Len returns how many bytes were captured, which is at most the limit.
func (b *Buffer) Len() int { return len(b.data) }

// Truncated reports whether any byte written to the Buffer was dropped, so a
// caller can label the output it captured as partial. It is the only channel
// for that fact: Write cannot signal it without breaking the os/exec contract
// documented on this package.
func (b *Buffer) Truncated() bool { return b.truncated }
