package kascap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const pauseReasonSource = "../agent/run_host.go"

// pauseVerdictArmRe matches one arm of KAS's pauseVerdict switch: the case labels, the reason (a string or a
// template literal) and whether the arm sets a detailCode, which KAS turns into pauseDetail.class
// "transient-error".
var pauseVerdictArmRe = regexp.MustCompile(
	`((?:case"[a-z-]+":)+)return\{reason:("(?:[^"\\]|\\.)*"|` + "`[^`]*`" + `)(,detailCode:)?`,
)

// pauseKindResumable is every pauseVerdict kind marotte has classified: true for a stop nobody chose.
var pauseKindResumable = map[string]bool{
	"interruption":      true,
	"transient-stall":   true,
	"transient-network": true,
	"throttle":          true,
	"transient-service": true,
	"permanent":         false,
}

type pauseArm struct {
	reason   string
	kinds    []string
	detailed bool
}

// pauseVerdictArms returns the arms of the one static pauseVerdict method in src.
func pauseVerdictArms(t *testing.T, src string) []pauseArm {
	t.Helper()
	const anchor = "static pauseVerdict("
	if n := strings.Count(src, anchor); n != 1 {
		t.Fatalf("found %d %q methods in the bundle, want exactly 1; re-anchor on KAS's pause classifier", n, anchor)
	}
	at := strings.Index(src, anchor)
	open := strings.IndexByte(src[at:], '{')
	body := jsFuncBody(src, at+open)
	if body == "" {
		t.Fatal("pauseVerdict: unbalanced braces from its declaration")
	}
	var arms []pauseArm
	for _, m := range pauseVerdictArmRe.FindAllStringSubmatch(body, -1) {
		arm := pauseArm{reason: m[2][1 : len(m[2])-1], detailed: m[3] != ""}
		for label := range strings.SplitSeq(strings.TrimSuffix(m[1], ":"), ":") {
			arm.kinds = append(arm.kinds, strings.TrimSuffix(strings.TrimPrefix(label, `case"`), `"`))
		}
		arms = append(arms, arm)
	}
	if len(arms) == 0 {
		t.Fatal("pauseVerdict: no arm matched; the switch was reshaped upstream, so fix the pattern")
	}
	return arms
}

// declaredPauseReasons reads the stale-owner literal and the resumable prefixes from run_host.go's source,
// since the agent package cannot be imported here.
func declaredPauseReasons(t *testing.T) (stale string, prefixes []string) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), pauseReasonSource, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", pauseReasonSource, err)
	}
	unquote := func(e ast.Expr) string {
		lit, ok := e.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			t.Fatalf("%s: a pause reason is not a string literal", pauseReasonSource)
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			t.Fatalf("%s: %v", pauseReasonSource, err)
		}
		return s
	}
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || len(spec.Values) != 1 {
			return true
		}
		switch spec.Names[0].Name {
		case "stalePauseReason":
			stale = unquote(spec.Values[0])
		case "resumableReasonPrefixes":
			list, ok := spec.Values[0].(*ast.CompositeLit)
			if !ok {
				t.Fatalf("%s: resumableReasonPrefixes is not a slice literal", pauseReasonSource)
			}
			for _, e := range list.Elts {
				prefixes = append(prefixes, unquote(e))
			}
		}
		return true
	})
	if stale == "" || len(prefixes) == 0 {
		t.Fatalf("%s: found stalePauseReason %q and %d resumable prefixes; the declarations moved", pauseReasonSource, stale, len(prefixes))
	}
	return stale, prefixes
}

func TestPauseVerdictArmExtractor(t *testing.T) {
	const src = `x;static pauseVerdict(t){switch(t.kind){case"interruption":return{reason:"Stopped; retry."};` +
		"case\"transient-network\":return{reason:`Net (${t.code}); paused.`,detailCode:t.code};" +
		`case"throttle":case"transient-service":return{reason:"Svc \"5xx\"; retry.",detailCode:t.code}}}y`
	got := pauseVerdictArms(t, src)
	want := []pauseArm{
		{reason: "Stopped; retry.", kinds: []string{"interruption"}},
		{reason: "Net (${t.code}); paused.", kinds: []string{"transient-network"}, detailed: true},
		{reason: `Svc \"5xx\"; retry.`, kinds: []string{"throttle", "transient-service"}, detailed: true},
	}
	if !slices.EqualFunc(got, want, func(a, b pauseArm) bool {
		return a.reason == b.reason && a.detailed == b.detailed && slices.Equal(a.kinds, b.kinds)
	}) {
		t.Errorf("pauseVerdictArms = %+v, want %+v", got, want)
	}
}

// TestPauseReasonsMatchBundle fails when KAS rewords or adds a pause reason the agent package's resume
// predicate matches by text: an involuntary pause with no pauseDetail must open with a declared prefix, a
// permanent one must not, every prefix must open a reason KAS gives, and the stale-owner literal must exist.
func TestPauseReasonsMatchBundle(t *testing.T) {
	stale, prefixes := declaredPauseReasons(t)
	matched := func(reason string) bool {
		return slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(reason, p) })
	}
	forEachBundle(t, func(t *testing.T, b bundle) {
		if !strings.Contains(b.src, strconv.Quote(stale)) {
			t.Errorf("the bundle no longer carries stalePauseReason %q verbatim; the restart-cancel predicate never fires", stale)
		}
		used := make(map[string]bool)
		for _, arm := range pauseVerdictArms(t, b.src) {
			for _, p := range prefixes {
				if strings.HasPrefix(arm.reason, p) {
					used[p] = true
				}
			}
			for _, kind := range arm.kinds {
				resumable, known := pauseKindResumable[kind]
				switch {
				case !known:
					t.Errorf("pauseVerdict has a new kind %q (reason %q); classify it in pauseKindResumable and the resume predicate", kind, arm.reason)
				case resumable && !arm.detailed && !matched(arm.reason):
					t.Errorf("pause kind %q carries no pauseDetail and its reason %q opens with no resumableReasonPrefixes entry, so marotte stops resuming it", kind, arm.reason)
				case !resumable && (arm.detailed || matched(arm.reason)):
					t.Errorf("pause kind %q is not involuntary, but marotte would resume it (detail %v, reason %q)", kind, arm.detailed, arm.reason)
				}
			}
		}
		for _, p := range prefixes {
			if !used[p] {
				t.Errorf("resumableReasonPrefixes entry %q opens no pauseVerdict reason in the bundle; it is dead", p)
			}
		}
	})
}
