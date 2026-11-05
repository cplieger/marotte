package agent

import (
	"context"
	"fmt"
	"io/fs"
	"math"
	"strings"
	"testing"
)

// TestSliceByLines_Table covers sliceByLines' edge cases.
func TestSliceByLines_Table(t *testing.T) {
	t.Parallel()
	line2 := 2
	line99 := 99
	line0 := 0
	limitMax := math.MaxInt
	limit100 := 100
	limit0 := 0
	limit2 := 2

	cases := []struct {
		name    string
		content string
		line    *int
		limit   *int
		want    string
	}{
		{"nil line and limit returns whole", "a\nb\n", nil, nil, "a\nb\n"},
		{"start beyond end returns empty", "a\nb\n", &line99, nil, ""},
		{"limit larger than remaining returns tail", "a\nb\nc\n", &line2, &limit100, "b\nc\n"},
		{"integer overflow does not panic", "a\nb\nc\n", &line2, &limitMax, "b\nc\n"},
		{"line zero keeps full content", "a\nb\nc\n", &line0, nil, "a\nb\nc\n"},
		{"limit zero does not narrow", "a\nb\nc\n", nil, &limit0, "a\nb\nc\n"},
		{"limit without line narrows from start", "a\nb\nc\nd\n", nil, &limit2, "a\nb\n"},
		{"midrange narrow within window", "a\nb\nc\nd\n", &line2, &limit2, "b\nc\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sliceByLines(tc.content, tc.line, tc.limit)
			if got != tc.want {
				t.Errorf("sliceByLines = %q, want %q", got, tc.want)
			}
		})
	}
}

// FuzzSliceByLines pins that the two offset walks never yield lo > hi or an out-of-range
// index, which would panic.
func FuzzSliceByLines(f *testing.F) {
	f.Add("a\nb\nc\n", 2, 2)
	f.Add("a\nb\n", 99, 1)
	f.Add("a\nb\nc\n", 2, math.MaxInt)
	f.Add("", 1, 1)
	f.Add("single", 1, 1)
	// Terminator shapes strings.Lines and strings.SplitAfter disagree about.
	f.Add("a\r\nb\r\n", 1, 1)
	f.Add("a\rb\rc", 2, 1)
	f.Add("no trailing newline", 1, 2)
	f.Add("\n\n\n", 2, 1)

	f.Fuzz(func(t *testing.T, content string, line, limit int) {
		if line < 1 || limit < 1 {
			return
		}
		lp := &line
		limp := &limit
		got := sliceByLines(content, lp, limp)
		if len(got) > len(content) {
			t.Errorf("output longer than input: len(got)=%d > len(content)=%d", len(got), len(content))
		}
		if got != "" && !strings.Contains(content, got) {
			t.Errorf("output %q is not a substring of content %q", got, content)
		}
	})
}

// TestFsErrorIsRoutine pins which rejections log at Debug rather than Warn.
func TestFsErrorIsRoutine(t *testing.T) {
	t.Parallel()
	for name, err := range map[string]error{
		"errCapExceeded":     errCapExceeded,
		"errRejectedByUser":  errRejectedByUser,
		"errNoUndoDirectory": errNoUndoDirectory,
		"errNoUndoTooLarge":  errNoUndoTooLarge,
		"a missing path":     notFoundError{fs.ErrNotExist},
	} {
		if got := fsErrorIsRoutine(fmt.Errorf("wrapped: %w", err)); !got {
			t.Errorf("fsErrorIsRoutine(%s) = %v, want true", name, got)
		}
	}
	if got := fsErrorIsRoutine(nil); got {
		t.Errorf("fsErrorIsRoutine(nil) = %v, want false", got)
	}
	if got := fsErrorIsRoutine(context.Canceled); got {
		t.Errorf("fsErrorIsRoutine(context.Canceled) = %v, want false", got)
	}
}

// TestNoUndoRefusalsFitTheWire pins that the shell advice survives the trip: bridge.Engine.Respond
// truncates an error message past 256 bytes (maxRespondErrorBytes) and folds it to one line.
func TestNoUndoRefusalsFitTheWire(t *testing.T) {
	t.Parallel()
	for _, err := range []error{errNoUndoDirectory, errNoUndoTooLarge} {
		msg := err.Error()
		if len(msg) > 256 || strings.ContainsAny(msg, "\r\n") {
			t.Errorf("%q is %d bytes or multi-line; want one line of at most 256", msg, len(msg))
		}
		if !strings.HasSuffix(msg, noUndoAdvice) {
			t.Errorf("%q does not end with the shell advice", msg)
		}
	}
	if !strings.Contains(errNoUndoTooLarge.Error(), "over the 8 MiB limit") {
		t.Errorf("errNoUndoTooLarge = %q, want the limit derived from fsReadCap", errNoUndoTooLarge)
	}
}

// TestSliceByLines_offsetLimitOvershootReturnsTail pins the clamped tail just past the
// remaining-line count, where flipping end-start to end+start panics.
func TestSliceByLines_offsetLimitOvershootReturnsTail(t *testing.T) {
	t.Parallel()
	line, limit := 2, 4 // start at line 2; only 2 lines remain in a 3-line file
	got := sliceByLines("a\nb\nc\n", &line, &limit)
	if got != "b\nc\n" {
		t.Errorf("sliceByLines(\"a\\nb\\nc\\n\", line=2, limit=4) = %q, want %q (tail, no over-read)",
			got, "b\nc\n")
	}
}
