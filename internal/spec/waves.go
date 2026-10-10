package spec

import (
	"encoding/json"
	"strings"
)

// The section is agent-authored, so its size is not a promise.
const maxWaveBytes = 128 << 10

const waveHeadingText = "task dependency graph"

// A task id is the dotted number a task line carries, so the join is on SpecTaskNode.Number
// verbatim.
type waveGraph struct {
	Waves []struct {
		Tasks []string `json:"tasks"`
		ID    int      `json:"id"`
	} `json:"waves"`
}

// parseWaves reads the "Task Dependency Graph" section's fenced JSON body and answers the
// wave id per dotted task number, or nil. Any departure from the expected shape (absent or
// unfenced section, undecodable body, no waves key, a negative id, one task in two waves)
// answers nil: a wrong wave is worse than none. A repeated heading: the first wins.
func parseWaves(lines []string) map[string]int {
	body, ok := waveBody(lines)
	if !ok {
		return nil
	}
	var g waveGraph
	if err := json.Unmarshal([]byte(body), &g); err != nil || len(g.Waves) == 0 {
		return nil
	}
	out := make(map[string]int)
	for _, w := range g.Waves {
		if w.ID < 0 {
			return nil
		}
		for _, id := range w.Tasks {
			if _, dup := out[id]; dup {
				return nil
			}
			out[id] = w.ID
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// waveBody returns the first fenced block under the first wave heading. The
// fence is required: an unfenced body is prose to every markdown reader, and
// decoding it would make the section's rendering and its meaning disagree.
func waveBody(lines []string) (string, bool) {
	at, ok := waveHeadingAt(lines)
	if !ok {
		return "", false
	}
	for i := at + 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "```"):
			return fencedBody(lines, i)
		default:
			// Anything else before the fence means this section carries no
			// graph, and a fence further down belongs to something else.
			return "", false
		}
	}
	return "", false
}

func waveHeadingAt(lines []string) (int, bool) {
	for i, l := range lines {
		if !headingRe.MatchString(l) {
			continue
		}
		text := strings.TrimSpace(strings.TrimLeft(l, "#"))
		if strings.EqualFold(text, waveHeadingText) {
			return i, true
		}
	}
	return 0, false
}

// fencedBody collects the lines between the fence opening at lines[open] and
// the next closing fence, refusing a body over maxWaveBytes and an unclosed
// fence.
func fencedBody(lines []string, open int) (string, bool) {
	var b strings.Builder
	for i := open + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
			return b.String(), true
		}
		if b.Len()+len(lines[i])+1 > maxWaveBytes {
			return "", false
		}
		b.WriteString(lines[i])
		b.WriteByte('\n')
	}
	return "", false
}
