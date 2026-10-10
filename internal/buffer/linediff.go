package buffer

import (
	"slices"
	"strings"
)

// maxDiffCells bounds the counts pass by its m×n cells; past it the answer degrades to delete-all/add-all of the
// middle. Must match diff.ts's TIME_BUDGET_CELLS, or the turn and delegate footers disagree.
const maxDiffCells = 25_000_000

// maxHunkCells bounds the traceback, whose dense table costs 8 bytes a cell: 4M cells is about 32 MB, matching
// diff.ts's SPACE_THRESHOLD.
const maxHunkCells = 4_000_000

type lineHunk struct {
	StartLine int
	EndLine   int
}

// splitDiffLines splits s into lines: on "\n", stripping one trailing "\r" so CRLF-to-LF is not a whole-file change,
// and dropping the empty element after a final newline. Twin of splitLines in static-src/diff.ts.
func splitDiffLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for i, l := range lines {
		if strings.HasSuffix(l, "\r") {
			lines[i] = l[:len(l)-1]
		}
	}
	return lines
}

// Every LCS-optimal implementation yields the same length, so Go and TypeScript agree by
// construction.
func lcsLen(a, b []string) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	// Symmetric, so the rows take the shorter side.
	if len(b) > len(a) {
		a, b = b, a
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for _, x := range a {
		for j, y := range b {
			if x == y {
				curr[j+1] = prev[j] + 1
			} else {
				curr[j+1] = max(curr[j], prev[j+1])
			}
		}
		prev, curr = curr, prev
		clear(curr)
	}
	return prev[len(b)]
}

// LineDelta reports how many lines a diff added and removed. KAS sends whole-file text, so a newline count reported
// whole-file churn for a one-line edit. added and removed derive from the LCS length; trimming the common prefix and
// suffix keeps that cheap.
func LineDelta(oldText, newText string) (added, removed int) {
	if oldText == newText {
		return 0, 0
	}
	a := splitDiffLines(oldText)
	b := splitDiffLines(newText)

	p := 0
	maxTrim := min(len(a), len(b))
	for p < maxTrim && a[p] == b[p] {
		p++
	}
	s := 0
	for s < maxTrim-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	midOld := a[p : len(a)-s]
	midNew := b[p : len(b)-s]

	// One side empty, or over budget: nothing is common, so the trimmed lengths are the answer.
	if len(midOld) == 0 || len(midNew) == 0 || len(midOld)*len(midNew) > maxDiffCells {
		return len(midNew), len(midOld)
	}
	k := lcsLen(midOld, midNew)
	return len(midNew) - k, len(midOld) - k
}

// A pure deletion is the single new-text line it landed at. Nothing when the texts are equal.
func lineHunks(oldText, newText string) []lineHunk {
	if oldText == newText {
		return nil
	}
	a := splitDiffLines(oldText)
	b := splitDiffLines(newText)

	p := 0
	maxTrim := min(len(a), len(b))
	for p < maxTrim && a[p] == b[p] {
		p++
	}
	s := 0
	for s < maxTrim-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	midOld := a[p : len(a)-s]
	midNew := b[p : len(b)-s]
	if len(midOld) == 0 && len(midNew) == 0 {
		return nil
	}

	// Coarse fallback: one hunk over the whole middle, exact when one side is empty.
	if len(midOld) == 0 || len(midNew) == 0 || len(midOld)*len(midNew) > maxHunkCells {
		return []lineHunk{newRunHunk(p, p+len(midNew), len(b))}
	}
	return traceHunks(midOld, midNew, p, len(b))
}

// traceHunks walks a minimal edit script over the trimmed middles and groups non-context steps into hunks, within
// the cell budget. offset is the trimmed front context; newLen clamps a deletion at end of file.
func traceHunks(midOld, midNew []string, offset, newLen int) []lineHunk {
	t := denseLCS(midOld, midNew)
	var hunks []lineHunk
	var run runAcc
	run.reset()
	i, j := 0, 0
	for i < len(midOld) || j < len(midNew) {
		takeOld, takeNew := diffStep(midOld, midNew, t, i, j)
		switch {
		case takeOld && takeNew:
			// A context line closes the open run.
			if lo, hi, ok := run.flush(); ok {
				hunks = append(hunks, newRunHunk(offset+lo, offset+hi, newLen))
			}
			i++
			j++
		case takeOld:
			// A deletion consumes no new line, so the run stays anchored.
			run.anchor(j)
			i++
		default:
			run.extend(j)
			j++
		}
	}
	if lo, hi, ok := run.flush(); ok {
		hunks = append(hunks, newRunHunk(offset+lo, offset+hi, newLen))
	}
	return hunks
}

// Its O(len(a)*len(b)) space is why maxHunkCells bounds it.
func denseLCS(a, b []string) [][]int {
	t := make([][]int, len(a)+1)
	for i := range t {
		t[i] = make([]int, len(b)+1)
	}
	for i, x := range slices.Backward(a) {
		for j, y := range slices.Backward(b) {
			if x == y {
				t[i][j] = t[i+1][j+1] + 1
			} else {
				t[i][j] = max(t[i+1][j], t[i][j+1])
			}
		}
	}
	return t
}

// diffStep decides one traceback step from (i, j): old, new, or both (a context match).
func diffStep(a, b []string, t [][]int, i, j int) (takeOld, takeNew bool) {
	if i < len(a) && j < len(b) && a[i] == b[j] {
		return true, true
	}
	if j >= len(b) || (i < len(a) && t[i+1][j] >= t[i][j+1]) {
		return true, false
	}
	return false, true
}

type runAcc struct {
	lo int
	hi int
}

func (r *runAcc) reset() { r.lo, r.hi = -1, -1 }

// anchor opens a run at j without consuming a new line (a deletion).
func (r *runAcc) anchor(j int) {
	if r.lo < 0 {
		r.lo, r.hi = j, j
	}
}

func (r *runAcc) extend(j int) {
	if r.lo < 0 {
		r.lo = j
	}
	r.hi = j + 1
}

// flush reports the open run and closes it, or false when none is open.
func (r *runAcc) flush() (lo, hi int, ok bool) {
	if r.lo < 0 {
		return 0, 0, false
	}
	lo, hi = r.lo, r.hi
	r.reset()
	return lo, hi, true
}

// An empty range is a deletion, marked where the removed lines were.
func newRunHunk(lo, hi, newLen int) lineHunk {
	if newLen <= 0 {
		// The file was emptied, so line 1 is all a gutter can mark.
		return lineHunk{StartLine: 1, EndLine: 1}
	}
	start := min(lo+1, newLen)
	end := min(max(hi, start), newLen)
	return lineHunk{StartLine: start, EndLine: end}
}
