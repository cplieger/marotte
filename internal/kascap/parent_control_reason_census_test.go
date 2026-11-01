package kascap

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// parentReasonsPath is shared with the client, which reads it in static-src/run-stop-sentence.node.test.ts.
const parentReasonsPath = "testdata/parent_control_reasons.json"

type parentControlReasons struct {
	Pause  string `json:"pause"`
	Resume string `json:"resume"`
}

func readParentControlReasons(t *testing.T) parentControlReasons {
	t.Helper()
	raw, err := os.ReadFile(parentReasonsPath)
	if err != nil {
		t.Fatalf("read %s: %v", parentReasonsPath, err)
	}
	var r parentControlReasons
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("decode %s: %v", parentReasonsPath, err)
	}
	if r.Pause == "" || r.Resume == "" {
		t.Fatalf("%s names pause %q and resume %q, want both", parentReasonsPath, r.Pause, r.Resume)
	}
	return r
}

// parentControlFindings is every way src stops matching the client's reading of the parent-run controls: each
// reason bound to exactly one mangled constant, each stamped `initiator:"user"` by its control, and the pause's
// constant classified as the orchestrator's.
func parentControlFindings(src string, want parentControlReasons) []string {
	var out []string
	bound := func(literal string) string {
		re := regexp.MustCompile(`(` + jsIdent + `)=` + regexp.QuoteMeta(strconv.Quote(literal)))
		found := re.FindAllStringSubmatch(src, -1)
		if len(found) != 1 {
			out = append(out, "found "+strconv.Itoa(len(found))+" bindings of "+strconv.Quote(literal)+", want exactly 1")
			return ""
		}
		return found[0][1]
	}
	pause, resume := bound(want.Pause), bound(want.Resume)
	for _, c := range []struct{ what, ident string }{{"pause", pause}, {"resume", resume}} {
		if c.ident != "" && !strings.Contains(src, `initiator:"user",reason:`+c.ident+`}`) {
			out = append(out, "the parent "+c.what+" no longer stamps its reason as the user's")
		}
	}
	if pause != "" {
		q := regexp.QuoteMeta(pause)
		classifier := regexp.MustCompile(`\.stopReason===` + q + `\|\|` + jsIdent + `\.pausePending\?\.reason===` + q +
			`\?"orchestrator":"user"`)
		if !classifier.MatchString(src) {
			out = append(out, "the pause classifier no longer names the parent's pause the orchestrator's")
		}
	}
	return out
}

func TestParentControlFindings(t *testing.T) {
	want := parentControlReasons{Pause: "P.", Resume: "R."}
	const good = `var Zq="P.";x={initiator:"user",reason:Zq};var yS="R.";y({initiator:"user",reason:yS});` +
		`z=WJ(e)?e.stopReason===Zq||e.pausePending?.reason===Zq?"orchestrator":"user":"other"`
	for _, c := range []struct {
		name string
		src  string
		want int
	}{
		{"the shape the client reads", good, 0},
		{"a reworded pause", strings.Replace(good, `"P."`, `"P!"`, 1), 1},
		{"a second binding", good + `;var Q2="R."`, 1},
		{"an unstamped resume", strings.Replace(good, `reason:yS}`, `reason:void 0}`, 1), 1},
		{"a pause no longer the orchestrator's", strings.Replace(good, `"orchestrator"`, `"parent"`, 1), 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := parentControlFindings(c.src, want); len(got) != c.want {
				t.Errorf("parentControlFindings = %q, want %d finding(s)", got, c.want)
			}
		})
	}
}

// TestParentControlReasonsMatchBundle fails when KAS rewords the reason its parent-run pause or resume stamps, or
// stops classifying the pause as the orchestrator's, either of which makes the client name the reader as the one
// who paused or resumed a run its chat did. On a finding, update testdata/parent_control_reasons.json and
// run-store.ts's PARENT_PAUSE_REASON and PARENT_RESUME_REASON together.
func TestParentControlReasonsMatchBundle(t *testing.T) {
	want := readParentControlReasons(t)
	forEachBundle(t, func(t *testing.T, b bundle) {
		if got := parentControlFindings(b.src, want); len(got) != 0 {
			t.Errorf("the bundle no longer matches the client's parent-control attribution: %q", got)
		}
	})
}
