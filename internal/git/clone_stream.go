// A clone's liveness is git's own --progress stream, not a wall clock: a fixed budget kills a large
// repo downloading fine and waits out a transfer that died in its first second.

package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/cplieger/marotte/internal/logsafe"
)

// cloneCeiling bounds the whole clone, stall detection included, against a remote that drips
// progress forever; only longer than any legitimate clone.
const cloneCeiling = 60 * time.Minute

// errCloneCeiling is cloneCeiling's context cause, so a kill at the
// ceiling names itself instead of reading as a generic deadline.
var errCloneCeiling = errors.New("the transfer exceeded the 60-minute ceiling")

// cloneStallTimeout is how long a transfer may report no progress before it is killed; git reports
// many times a second while working, so this long a silence is death. A var so tests can shorten
// it.
var cloneStallTimeout = 90 * time.Second

var errCloneStalled = errors.New("transfer stalled")

// On an ordinary failure the output carries git's own message; on a stall or ceiling kill it is
// EMPTY and the error names the reason, since the tail would be a progress line.
func runTransfer(ctx context.Context, dir string, onProgress func(string), args ...string) (string, error) {
	if sub, ok := allowedSubcommand(args); !ok {
		return "", fmt.Errorf("git: subcommand not allowed: %s", sub)
	}
	tctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	cmd := gitExec(tctx, dir, args...)
	killWholeGroup(cmd)
	stdout := &cappedBuffer{cap: 64 * 1024}
	cmd.Stdout = stdout
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}

	activity := make(chan struct{}, 1)
	watchdogDone := make(chan struct{})
	defer close(watchdogDone)
	// The stall window is read ONCE, here on the caller's goroutine, and
	// handed to the watchdog as a value: the var exists for tests to
	// shorten, and a goroutine reading it directly races the restore.
	go stallWatchdog(cloneStallTimeout, activity, watchdogDone, cancel)

	tail, readErr := forwardProgress(stderr, activity, onProgress)
	waitErr := cmd.Wait()
	if waitErr != nil {
		if killErr := transferKillReason(tctx); killErr != nil {
			return "", killErr
		}
	}
	// A read that ended early outranks git's exit status as the diagnosis, but not a kill reason
	// above.
	if readErr != nil && waitErr != nil {
		return "", fmt.Errorf("reading git's progress output: %w", readErr)
	}
	out := strings.TrimSpace(stdout.String() + "\n" + strings.Join(tail, "\n"))
	if readErr != nil {
		slog.Warn("git transfer succeeded but its progress output could not be read whole",
			"error", logsafe.Field(readErr.Error()))
	}
	return out, waitErr
}

// killWholeGroup puts the transfer in its own process group and makes the context kill the GROUP: a
// head-only kill leaves git-remote-https holding the stderr pipe, blocking the read loop past the
// stall.
func killWholeGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// stallWatchdog cancels the transfer when no activity arrives for stall.
// Reset rides a coalescing channel rather than a timer.Reset from the read
// loop, so the reset and the expiry cannot race.
func stallWatchdog(stall time.Duration, activity, done <-chan struct{}, cancel context.CancelCauseFunc) {
	timer := time.NewTimer(stall)
	defer timer.Stop()
	for {
		select {
		case <-done:
			return
		case <-activity:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(stall)
		case <-timer.C:
			cancel(errCloneStalled)
			return
		}
	}
}

// forwardProgress drains the transfer's stderr, feeding the watchdog on every token and forwarding
// each to onProgress, and returns the last tokens seen. Not a bufio.Scanner: ErrTooLong is terminal
// for it, so one long line stalled the transfer, and its cut can land at an arbitrary byte, which a
// credential pattern can straddle. An oversize token is drained to its terminator and reported
// truncated.
func forwardProgress(stderr io.Reader, activity chan<- struct{}, onProgress func(string)) ([]string, error) {
	tail := &progressTail{onToken: onProgress, activity: activity}
	pr := &progressReader{r: stderr}
	for {
		token, truncated, err := pr.readToken()
		tail.add(token, truncated)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return tail.tokens, nil
			}
			return tail.tokens, err
		}
	}
}

// progressTail keeps the last progressTailLen tokens and does the two things every
// token owes: feed the stall watchdog, and reach onToken. Its own type because
// forwardProgress is otherwise one loop wrapping five concerns.
type progressTail struct {
	onToken  func(string)
	activity chan<- struct{}
	tokens   []string
}

// progressTailLen bounds the tokens kept for the transfer's error envelope. git's
// own failure message is the last few lines, so this only has to outlive the
// progress noise printed after it.
const progressTailLen = 32

// add records one token, ignoring the empty one a truncated or blank read yields.
// Activity is reported on a NON-blocking send: the watchdog coalesces, so a missed
// poke is a poke it was already going to get.
func (t *progressTail) add(token string, truncated bool) {
	if token == "" {
		return
	}
	select {
	case t.activity <- struct{}{}:
	default:
	}
	if truncated {
		token += progressTruncated
	}
	t.tokens = append(t.tokens, token)
	if len(t.tokens) > progressTailLen {
		t.tokens = t.tokens[1:]
	}
	if t.onToken != nil {
		t.onToken(token)
	}
}

// progressTokenCap bounds ONE progress token. git's own progress lines are tens
// of bytes; this is generous enough that a legitimately chatty remote always fits.
const progressTokenCap = 64 * 1024

// progressDrainCap bounds the bytes discarded while draining one oversize token, per token and in
// bytes (as in bridge_frame.go), so only a blob that never terminates exhausts it.
const progressDrainCap = 16 * progressTokenCap

// progressTruncated marks a token the cap cut, so a reader can tell it from a
// complete one.
const progressTruncated = "[truncated]"

// errProgressDrainExhausted says a single stderr token consumed the whole drain
// budget without a terminator, so there is no boundary left to resynchronise on.
var errProgressDrainExhausted = errors.New("git progress output did not terminate within the drain budget")

// progressReader tokenizes on '\r' OR '\n': git rewrites a progress line in place with '\r', so a
// single-delimiter ReadSlice loop would starve the watchdog. Owned by one goroutine.
type progressReader struct {
	r io.Reader
	// pending holds bytes read but not yet tokenized. Bounded by the cap plus one
	// read window, because crossing the cap switches to draining and empties it.
	pending []byte
	dropped int
	window  [16 << 10]byte
	// draining is set once the token in progress crossed the cap: the bytes are
	// counted and discarded until a terminator arrives.
	draining bool
	eof      bool
}

// readToken returns the next token without its terminator, whether the cap cut it, and a terminal
// error (io.EOF, a read error, errProgressDrainExhausted). A cut token returns ("", true, nil): the
// prefix is dropped, since an arbitrary-offset cut can split a rune or a credential pattern.
func (pr *progressReader) readToken() (token string, truncated bool, err error) {
	for {
		// The terminator decides first and the cap applies to the token it bounds, or a token whose
		// terminator arrives with its last chunk slips past.
		if i := bytes.IndexAny(pr.pending, "\r\n"); i >= 0 {
			tok := pr.pending[:i]
			pr.pending = pr.pending[i+1:]
			return pr.finish(tok, nil)
		}
		if pr.eof {
			// A trailing unterminated token at end of stream is still git's own
			// message on an ordinary failure, so it is reported rather than dropped.
			tok := pr.pending
			pr.pending = nil
			return pr.finish(tok, io.EOF)
		}
		if dropErr := pr.dropOversizePrefix(); dropErr != nil {
			return "", true, dropErr
		}
		if readErr := pr.fill(); readErr != nil {
			return "", false, readErr
		}
	}
}

// finish grades one complete token against the cap and clears any drain state, so
// the terminator arm and the end-of-stream arm cannot disagree about what counts as
// oversize. term is the terminal error to report alongside it, nil mid-stream.
func (pr *progressReader) finish(tok []byte, term error) (token string, truncated bool, err error) {
	over := pr.draining || len(tok) > progressTokenCap
	pr.draining, pr.dropped = false, 0
	if over {
		return "", true, term
	}
	return strings.TrimSpace(string(tok)), false, term
}

// dropOversizePrefix abandons the token in progress once it crosses the cap unterminated, charging
// the drain budget; the prefix is dropped for readToken's reason.
func (pr *progressReader) dropOversizePrefix() error {
	if len(pr.pending) <= progressTokenCap {
		return nil
	}
	pr.draining = true
	pr.dropped += len(pr.pending)
	pr.pending = pr.pending[:0]
	if pr.dropped > progressDrainCap {
		pr.draining, pr.dropped = false, 0
		return errProgressDrainExhausted
	}
	return nil
}

// An end of stream is RECORDED rather than returned: the bytes already in hand are a token the
// caller still owes its reader, and readToken's own eof arm is what reports it once they are handed
// over.
func (pr *progressReader) fill() error {
	n, err := pr.r.Read(pr.window[:])
	pr.pending = append(pr.pending, pr.window[:n]...)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, io.EOF):
		pr.eof = true
		return nil
	default:
		return err
	}
}

// transferKillReason names WHY the runner killed the transfer, nil when
// the failure was git's own.
func transferKillReason(tctx context.Context) error {
	cause := context.Cause(tctx)
	switch {
	case errors.Is(cause, errCloneStalled):
		return fmt.Errorf("the transfer stalled: no progress from git for %s", cloneStallTimeout)
	case errors.Is(cause, errCloneCeiling):
		return errCloneCeiling
	}
	return nil
}

// cappedBuffer keeps the first cap bytes written and reports the rest as written, so a flooding
// subprocess cannot grow it (a short write would make exec kill the process).
// The retained text ends on a LINE BOUNDARY, a correctness property: the credential redactor's
// patterns match within one line, and an arbitrary-offset cut between `://` and `@` lets a
// credential through. A cut is marked.
type cappedBuffer struct {
	buf bytes.Buffer
	cap int
	// dropped records that at least one byte did not fit, so String can say so.
	dropped bool
}

// cappedBufferTruncated is appended to a cappedBuffer whose input did not fit.
const cappedBufferTruncated = "[output truncated]"

func (c *cappedBuffer) Write(p []byte) (int, error) {
	room := c.cap - c.buf.Len()
	if room <= 0 {
		c.dropped = len(p) > 0 || c.dropped
		return len(p), nil
	}
	if len(p) <= room {
		c.buf.Write(p)
		return len(p), nil
	}
	// Only up to the last newline that fits; a chunk with no newline in the room contributes
	// nothing rather than a partial line.
	fits := p[:room]
	if i := bytes.LastIndexByte(fits, '\n'); i >= 0 {
		c.buf.Write(fits[:i+1])
	}
	c.dropped = true
	return len(p), nil
}

func (c *cappedBuffer) String() string {
	if !c.dropped {
		return c.buf.String()
	}
	return c.buf.String() + cappedBufferTruncated
}
