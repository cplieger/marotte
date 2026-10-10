package spec

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
)

// maxNodes caps the returned tree; the counts still cover the whole file.
const maxNodes = 1000

var (
	taskLineRe    = regexp.MustCompile(`^(\s*)([-*+])\s+\[([ xX\-~])\](\\?\*?)\s+(.+)$`)
	numberSpaceRe = regexp.MustCompile(`^(\d+(?:\.\d+)*)\s`)
	numberDotRe   = regexp.MustCompile(`^(\d+(?:\.\d+)*)\.`)
	headingRe     = regexp.MustCompile(`^#{1,6}\s`)
	nearMissRe    = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+\[[^\]]*\]`)
)

// parsed is what Parse reads out of one tasks.md.
type parsed struct {
	// Truncated is set when the tree was cut.
	Truncated *marotte.SpecTruncated
	// Tasks is the tree in the order Kiro walks it, never nil, cut at maxNodes.
	Tasks []marotte.SpecTaskNode
	// Progress counts the required leaves of the whole file.
	Progress marotte.SpecProgress
	// UnreadableLines counts task-shaped lines Kiro's parser refuses.
	UnreadableLines int
}

type task struct {
	text     string
	number   string
	status   marotte.PlanStatus
	content  []string
	children []*task
	line     int
	indent   int
	queued   bool
	optional bool
}

// parse reads src as Kiro's tasks.md parser does: CRLF normalised, the KAS
// line regex, the per-task walk, the number re-parenting, then the three
// additions the goldens model (detail cut at the first heading, U+2028 and
// U+2029 rejected from a task text, widened near-miss count). The wave id is
// outside that contract: KAS's task parser never reads the dependency-graph
// section, so no golden states one.
func parse(src []byte) parsed {
	lines := splitLines(src)
	roots := walk(lines)
	budget := maxNodes
	p := parsed{
		Tasks:           toNodes(roots, &budget, parseWaves(lines)),
		UnreadableLines: countNearMisses(lines),
	}
	tally(roots, &p.Progress)
	if total := countTasks(roots); total > maxNodes {
		p.Truncated = &marotte.SpecTruncated{Returned: maxNodes, Total: total}
	}
	return p
}

func splitLines(src []byte) []string {
	s := strings.ReplaceAll(string(src), "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Split(s, "\n")
}

// matchTaskLine is jTr. A match whose text carries U+2028 or U+2029 is not a task: JavaScript's `.`
// never matched it, so Kiro reads the line as content.
func matchTaskLine(line string) (*task, bool) {
	m := taskLineRe.FindStringSubmatch(line)
	if len(m) == 0 || strings.ContainsAny(m[5], "\u2028\u2029") {
		return nil, false
	}
	t := &task{
		text:     m[5],
		number:   taskNumber(m[5]),
		indent:   len(m[1]),
		optional: m[4] == "*" || m[4] == `\*`,
	}
	switch m[3] {
	case "x", "X":
		t.status = marotte.PlanCompleted
	case "-":
		t.status = marotte.PlanInProgress
	case "~":
		t.status = marotte.PlanPending
		t.queued = true
	default:
		t.status = marotte.PlanPending
	}
	return t, true
}

// taskNumber is _gt: the dotted number a text starts with, or "".
func taskNumber(text string) string {
	if m := numberSpaceRe.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	if m := numberDotRe.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

// walk is uw: every top-level task line starts a parseTask, and the number
// re-parenting runs when any top-level survivor is numbered.
func walk(lines []string) []*task {
	var tops []*task
	numbered := false
	for i := 0; i < len(lines); {
		t, ok := matchTaskLine(lines[i])
		if !ok {
			i++
			continue
		}
		i = parseTask(lines, i, t)
		tops = append(tops, t)
		numbered = numbered || t.number != ""
	}
	if numbered {
		return reparent(tops)
	}
	return tops
}

// parseTask is jqi over the task at lines[at], returning the index of the
// first line it did not consume.
func parseTask(lines []string, at int, t *task) int {
	t.line = at
	i := at + 1
	for i < len(lines) {
		line := lines[i]
		child, isTask := matchTaskLine(line)
		if t.breaksAt(line, isTask) {
			break
		}
		if isTask {
			i = parseTask(lines, i, child)
			t.children = append(t.children, child)
			continue
		}
		t.pushContent(line)
		i++
	}
	t.content = trimTrailingBlank(t.content)
	return i
}

// breaksAt is the two arms of jqi's loop: a numbered task stops at the next
// task line at any indent; an unnumbered one stops at the first non-blank
// line at its own indent or less, and takes deeper task lines as children.
func (t *task) breaksAt(line string, isTask bool) bool {
	if t.number != "" {
		return isTask
	}
	return !isBlank(line) && leadingSpace(line) <= t.indent
}

func (t *task) pushContent(line string) {
	if isBlank(line) {
		if len(t.content) > 0 {
			t.content = append(t.content, "")
		}
		return
	}
	t.content = append(t.content, dropLeading(line, min(leadingSpace(line), t.indent+2)))
}

func trimTrailingBlank(lines []string) []string {
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// reparent is Llc: numbered top-level survivors sorted by dot depth then
// numerically, each hung under the survivor its number names, a missing
// parent making a root; unnumbered survivors keep the walk's children.
func reparent(tops []*task) []*task {
	var numbered []*task
	for _, t := range tops {
		if t.number != "" {
			numbered = append(numbered, t)
		}
	}
	// Stable, like Array.prototype.sort: equal numbers keep file order.
	sortNumbers(numbered)
	byNumber := make(map[string]*task, len(numbered))
	orphan := make(map[string]bool)
	for _, t := range numbered {
		t.children = nil
		byNumber[t.number] = t
		if depth(t.number) == 1 {
			continue
		}
		parent := byNumber[t.number[:strings.LastIndex(t.number, ".")]]
		if parent == nil {
			orphan[t.number] = true
			continue
		}
		parent.children = append(parent.children, t)
	}
	var roots []*task
	for _, t := range tops {
		if t.number == "" || depth(t.number) == 1 || orphan[t.number] {
			roots = append(roots, t)
		}
	}
	return roots
}

func depth(number string) int {
	return strings.Count(number, ".") + 1
}

func (t *task) detail() string {
	lines := t.content
	for i, l := range lines {
		if headingRe.MatchString(l) {
			lines = lines[:i]
			break
		}
	}
	return strings.Join(trimTrailingBlank(lines), "\n")
}

func (t *task) node(waves map[string]int) marotte.SpecTaskNode {
	sum := sha256.Sum256([]byte(t.text))
	var wave *int
	if id, ok := waves[t.number]; ok && t.number != "" {
		wave = &id
	}
	return marotte.SpecTaskNode{
		ID:       "L" + strconv.Itoa(t.line+1),
		Line:     t.line + 1,
		Indent:   t.indent,
		Number:   t.number,
		Text:     t.text,
		Status:   t.status,
		Queued:   t.queued,
		Optional: t.optional,
		Hash:     hex.EncodeToString(sum[:]),
		Detail:   t.detail(),
		Wave:     wave,
	}
}

// A parent whose children did not all fit says so.
func toNodes(ts []*task, budget *int, waves map[string]int) []marotte.SpecTaskNode {
	out := make([]marotte.SpecTaskNode, 0, len(ts))
	for _, t := range ts {
		if *budget == 0 {
			break
		}
		*budget--
		n := t.node(waves)
		n.Children = toNodes(t.children, budget, waves)
		n.TruncatedChildren = len(n.Children) < len(t.children)
		out = append(out, n)
	}
	return out
}

func countTasks(ts []*task) int {
	n := 0
	for _, t := range ts {
		n += 1 + countTasks(t.children)
	}
	return n
}

// tally counts the required leaves: queued is a subset of pending.
func tally(ts []*task, p *marotte.SpecProgress) {
	for _, t := range ts {
		if len(t.children) > 0 {
			tally(t.children, p)
			continue
		}
		if t.optional {
			continue
		}
		p.Total++
		switch t.status {
		case marotte.PlanCompleted:
			p.Completed++
		case marotte.PlanInProgress:
			p.InProgress++
		default:
			p.Pending++
			if t.queued {
				p.Queued++
			}
		}
	}
}

// countNearMisses counts lines shaped like a task under the widened grammar
// that the line regex refuses. A line the regex matches but parse rejects for
// a line separator is neither.
func countNearMisses(lines []string) int {
	n := 0
	for _, l := range lines {
		if nearMissRe.MatchString(l) && !taskLineRe.MatchString(l) {
			n++
		}
	}
	return n
}
