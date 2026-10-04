package spec

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

const regenerate = "node internal/spec/testdata/gen-goldens.mjs <acp-server.js>"

// golden is one fixture the generator writes: the header plus Parse's answer.
type golden struct {
	Truncated       *marotte.SpecTruncated `json:"truncated"`
	InputFile       string                 `json:"input_file"`
	KASVersion      string                 `json:"kas_version"`
	SliceSHA256     string                 `json:"slice_sha256"`
	Tasks           []marotte.SpecTaskNode `json:"tasks"`
	Progress        marotte.SpecProgress   `json:"progress"`
	UnreadableLines int                    `json:"unreadable_lines"`
	CRLF            bool                   `json:"crlf"`
}

func (g golden) parsed() Parsed {
	return Parsed{Tasks: g.Tasks, Truncated: g.Truncated, Progress: g.Progress, UnreadableLines: g.UnreadableLines}
}

func loadGoldens(t *testing.T) map[string]golden {
	t.Helper()
	paths, err := filepath.Glob("testdata/goldens/*.json")
	if err != nil || len(paths) == 0 {
		t.Fatalf("Setup: no goldens under testdata/goldens (%v); run %s", err, regenerate)
	}
	out := make(map[string]golden, len(paths))
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("Setup: read %s: %v", p, err)
		}
		var g golden
		if err := json.Unmarshal(raw, &g); err != nil {
			t.Fatalf("Setup: decode %s: %v", p, err)
		}
		out[strings.TrimSuffix(filepath.Base(p), ".json")] = g
	}
	return out
}

func readInput(t *testing.T, g golden) []byte {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("testdata", "inputs", g.InputFile))
	if err != nil {
		t.Fatalf("Setup: read input: %v", err)
	}
	if g.CRLF {
		src = bytes.ReplaceAll(src, []byte("\n"), []byte("\r\n"))
	}
	return src
}

// asJSON normalises through the wire encoding so a nil and an empty Children
// slice, or an absent and a false omitempty bool, compare equal.
func asJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// withoutWaves drops the wave ids before an oracle comparison. Every other
// field of a node is KAS's parser answering, and the goldens are that parser's
// own output; a wave is marotte reading a section KAS's task parser never
// looks at, so the oracle has no value to state for it. Its own tests are
// TestParseWaves_* and TestParse_AssignsWavesByDottedNumber.
func withoutWaves(p Parsed) Parsed {
	p.Tasks = stripWaves(p.Tasks)
	return p
}

func stripWaves(ns []marotte.SpecTaskNode) []marotte.SpecTaskNode {
	for i := range ns {
		ns[i].Wave = nil
		ns[i].Children = stripWaves(ns[i].Children)
	}
	return ns
}

func firstDiff(got, want string) string {
	gl, wl := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := range min(len(gl), len(wl)) {
		if gl[i] != wl[i] {
			return "line " + strconv.Itoa(i+1) + ": got " + strings.TrimSpace(gl[i]) + " want " + strings.TrimSpace(wl[i])
		}
	}
	return "lengths differ"
}

func TestParse_MatchesOracleGoldens(t *testing.T) {
	goldens := loadGoldens(t)
	families := map[string]bool{}
	for name, g := range goldens {
		t.Run(name, func(t *testing.T) {
			if g.KASVersion != "2.21.4" || len(g.SliceSHA256) != 64 {
				t.Fatalf("golden %s header = version %q slice %q, want the 2.21.4 slice; run %s", name, g.KASVersion, g.SliceSHA256, regenerate)
			}
			got := asJSON(t, withoutWaves(Parse(readInput(t, g))))
			want := asJSON(t, g.parsed())
			if got != want {
				t.Errorf("Parse(%s) drifted from the oracle at %s.\nRegenerate with %s, then read the diff: a golden change is a parser change.", g.InputFile, firstDiff(got, want), regenerate)
			}
		})
		for _, fam := range []string{"mixed-m1", "mixed-m2", "mixed-m3", "mixed-m4", "mixed-m5", "mixed-m6", "mixed-m7", "mixed-m8", "mixed-m9"} {
			if strings.HasPrefix(name, fam) {
				families[fam] = true
			}
		}
	}
	if len(families) != 9 {
		t.Errorf("goldens cover %d of the 9 mixed families M1-M9", len(families))
	}
	for _, corpus := range []string{"seed-catalogue.tasks", "orchard-ledger.tasks", "greenhouse-controller.tasks", "greenhouse-controller.tasks.crlf"} {
		if _, ok := goldens[corpus]; !ok {
			t.Errorf("goldens lack the corpus fixture %s", corpus)
		}
	}
}

func TestParse_NBSPIndentIsRefused(t *testing.T) {
	// Go's \s is ASCII, so a non-breaking space in the indent or after the box
	// is not a task here while Kiro accepts it: the stated refusing divergence.
	cases := map[string]string{
		"nbsp_indent":    "\u00a0- [ ] 1 nbsp indent\n",
		"nbsp_after_box": "- [ ]\u00a01 nbsp after the box\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			p := Parse([]byte(src))
			if len(p.Tasks) != 0 {
				t.Errorf("Parse(%q).Tasks = %d, want 0", src, len(p.Tasks))
			}
		})
	}
}

func TestParse_LineSeparatorInTextIsNeitherTaskNorNearMiss(t *testing.T) {
	for name, sep := range map[string]string{"u2028": "\u2028", "u2029": "\u2029"} {
		t.Run(name, func(t *testing.T) {
			src := "- [ ] 1 Before\n- [ ] 1.1 split" + sep + "text\n- [x] 2 After\n"
			p := Parse([]byte(src))
			if len(p.Tasks) != 2 || p.Tasks[0].Number != "1" || p.Tasks[1].Number != "2" {
				t.Fatalf("Parse(%q) roots = %+v, want 1 and 2", src, p.Tasks)
			}
			if p.UnreadableLines != 0 {
				t.Errorf("Parse(%q).UnreadableLines = %d, want 0", src, p.UnreadableLines)
			}
			if want := "- [ ] 1.1 split" + sep + "text"; p.Tasks[0].Detail != want {
				t.Errorf("Parse(%q).Tasks[0].Detail = %q, want %q", src, p.Tasks[0].Detail, want)
			}
		})
	}
}

func TestParse_DetailEndsAtTheFirstHeading(t *testing.T) {
	src := "- [x] 1 Last\n  - Verify: tail\n\n## Notes\n\nprose\n\n  ### indented heading\n"
	p := Parse([]byte(src))
	if len(p.Tasks) != 1 {
		t.Fatalf("Parse(%q) = %d tasks, want 1", src, len(p.Tasks))
	}
	if got, want := p.Tasks[0].Detail, "- Verify: tail"; got != want {
		t.Errorf("Parse(%q).Tasks[0].Detail = %q, want %q", src, got, want)
	}
	indented := "- [ ] 1 Task\n  prose\n  ## sub\n  gone\n"
	if got, want := Parse([]byte(indented)).Tasks[0].Detail, "prose"; got != want {
		t.Errorf("Parse(%q).Tasks[0].Detail = %q, want %q", indented, got, want)
	}
}

func TestParse_CountsWidenedNearMisses(t *testing.T) {
	cases := map[string]struct {
		src  string
		want int
	}{
		"bare_box":       {"- [] bare\n", 1},
		"ordered_marker": {"1. [ ] ordered\n1) [ ] paren\n", 2},
		"no_text":        {"- [x]\n- [ ] \n", 2},
		"real_tasks":     {"- [ ] a\n* [X] b\n+ [~] c\n- [-] d\n", 0},
		"two_letter_box": {"- [ab] two\n", 1},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Parse([]byte(c.src)).UnreadableLines; got != c.want {
				t.Errorf("Parse(%q).UnreadableLines = %d, want %d", c.src, got, c.want)
			}
		})
	}
}

func TestParse_CapsTheTreeAndMarksTheCutParent(t *testing.T) {
	// Unnumbered roots, so the indentation arm hangs 400 children under each.
	var b strings.Builder
	for _, root := range []string{"one", "two", "three"} {
		b.WriteString("- [ ] " + root + "\n")
		for range 400 {
			b.WriteString("  - [ ] child\n")
		}
	}
	p := Parse([]byte(b.String()))
	if p.Truncated == nil || p.Truncated.Returned != maxNodes || p.Truncated.Total != 1203 {
		t.Fatalf("Parse(cap).Truncated = %+v, want {1000 1203}", p.Truncated)
	}
	if len(p.Tasks) != 3 {
		t.Fatalf("Parse(cap) roots = %d, want 3 (the third root is the 803rd node)", len(p.Tasks))
	}
	if !p.Tasks[2].TruncatedChildren || len(p.Tasks[2].Children) != 197 {
		t.Errorf("Parse(cap).Tasks[2] = truncated %v with %d children, want true and 197", p.Tasks[2].TruncatedChildren, len(p.Tasks[2].Children))
	}
	if p.Tasks[0].TruncatedChildren || p.Tasks[1].TruncatedChildren {
		t.Errorf("Parse(cap) marked an uncut root as truncated")
	}
	if p.Progress.Total != 1200 || p.Progress.Pending != 1200 {
		t.Errorf("Parse(cap).Progress = %+v, want 1200 pending of 1200 from the whole file", p.Progress)
	}
}

func TestParse_CRLFHashesEqualTheLFTwin(t *testing.T) {
	lf := []byte("- [ ] 1 one\n  - detail\n- [x] 2 two\n")
	crlf := bytes.ReplaceAll(lf, []byte("\n"), []byte("\r\n"))
	a, b := Parse(lf), Parse(crlf)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("Parse(crlf) = %s\nwant the LF twin %s", asJSON(t, b), asJSON(t, a))
	}
}

func TestParse_ProgressInvariants(t *testing.T) {
	for name, g := range loadGoldens(t) {
		t.Run(name, func(t *testing.T) {
			p := Parse(readInput(t, g)).Progress
			if p.Pending+p.InProgress+p.Completed != p.Total {
				t.Errorf("Parse(%s).Progress = %+v: pending+in_progress+completed != total", g.InputFile, p)
			}
			if p.Queued > p.Pending {
				t.Errorf("Parse(%s).Progress = %+v: queued exceeds pending", g.InputFile, p)
			}
		})
	}
	src := "- [ ] 1 root\n- [~] 1.1 queued\n- [ ]* 1.2 optional\n- [-] 1.3 going\n- [x] 1.4 done\n- [ ] 1.5 open\n"
	got := Parse([]byte(src)).Progress
	want := marotte.SpecProgress{Pending: 2, InProgress: 1, Completed: 1, Queued: 1, Total: 4}
	if got != want {
		t.Errorf("Parse(%q).Progress = %+v, want %+v", src, got, want)
	}
}
