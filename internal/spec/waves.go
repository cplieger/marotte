package spec

import (
	"encoding/json"
	"strings"
)

// maxWaveBytes bounds the fenced body handed to the JSON decoder. The section
// is agent-authored, so its size is not a promise.
const maxWaveBytes = 128 << 10

const waveHeadingText = "task dependency graph"

// waveGraph is the fenced body's shape. A task id is the dotted number a task
// line carries, so the join is on SpecTaskNode.Number verbatim.
type waveGraph struct {
	Waves []struct {
		Tasks []string `json:"tasks"`
		ID    int      `json:"id"`
	} `json:"waves"`
}

// parseWaves reads the "Task Dependency Graph" section's fenced JSON body and
// answers the wave id per dotted task number, or nil.
//
// A wave is the set Kiro's orchestrator runs in parallel, so a row's wave is
// how a reader tells work that can start now from work that is waiting. The
// section is optional and agent-authored, which is why every departure from
// the expected shape answers nil rather than a partial map: an absent or
// unfenced section, a body the decoder refuses, a missing waves key, a
// negative id, or one task id claimed by two waves. A row carrying the wrong
// wave is worse than a row carrying none, and a contradictory graph cannot say
// which wave ANY task is in — the order it describes is what is broken.
//
// A repeated heading is not that class: the first section wins, because a
// heading written twice is a documentation slip where a task in two waves is a
// broken graph.
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
