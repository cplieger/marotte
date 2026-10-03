package chat

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// steerLabelFixture is the envelope of testdata/steer_label.json: every
// (origin, state, reason) triple production can stamp on a steer entry, with the words
// the client has to render for it.
//
// The triple set is SCANNED from the producers rather than listed, because listing it is
// what the fixture exists to stop: four producer sites were named in the design and the
// tree holds eight, so a hand-kept enumeration was already two sites short of the code.
// The scan reads every non-test .go file under internal/, finds each construction of
// marotte.EntrySteer and each mutation of one, and resolves Origin, State and Reason
// against the constants internal/marotte declares. An origin that comes from data
// expands to both members of the closed enum; a state or reason expression the scan
// cannot resolve is a STOP rather than a widened set, so the instrument can never
// silently cover less than the code.
//
// The LABEL column is this side's statement of the contract, not a derivation: the
// server cannot ask the client for words. steer-label-contract.test.ts renders each row
// through the real buildSteerNote in a real DOM and asserts the words match. So a reason
// a producer writes and the client has no wording for fails HERE (no contract words) or
// THERE (the render disagrees), which is the class design-2 §1.3 (b) mints the fixture to
// close.
type steerLabelFixture struct {
	Comment []string         `json:"_comment"`
	Origins []string         `json:"origins"`
	States  []string         `json:"states"`
	Triples []steerLabelRow  `json:"triples"`
	Sites   []steerLabelSite `json:"sites"`
}

// steerLabelRow is one producible triple. Compared is false for a triple no client
// reader can see; Edge says why.
type steerLabelRow struct {
	Origin   string   `json:"origin"`
	State    string   `json:"state"`
	Reason   string   `json:"reason"`
	Label    string   `json:"label"`
	Compared bool     `json:"compared"`
	Edge     string   `json:"edge,omitempty"`
	Sites    []string `json:"sites"`
}

// steerLabelSite is one producer, at the path a failure should name.
type steerLabelSite struct {
	Site   string `json:"site"`
	Form   string `json:"form"`
	Origin string `json:"origin"`
	State  string `json:"state"`
	Reason string `json:"reason"`
}

var steerLabelFixtureComment = []string{
	"The steer LABEL contract: every (origin, state, reason) a producer can stamp, and",
	"the words the client renders for it.",
	"",
	"Regenerate:  UPDATE_GOLDEN=1 go test ./internal/chat/ -run TestSteerLabelContract",
	"Re-run the other reader:  npx vitest --run steer-label-contract",
	"",
	"`triples` is SCANNED from the producers, never listed: the scan walks every non-test",
	"Go file under internal/, resolves each marotte.EntrySteer construction and mutation",
	"against internal/marotte's own constants, and expands an origin that comes from data",
	"to both members of the closed enum. `sites` is the scan's own evidence, so a NEW",
	"producer moves this golden and the fixture has to be read before it is regenerated.",
	"",
	"`label` is the SERVER's statement of the client's words (parts joined with ' · ');",
	"the reason clause is worded only on a dropped row, which is labelFor's own rule.",
	"steer-label-contract.test.ts renders every `compared` row through buildSteerNote in",
	"chromium and asserts the label matches.",
	"",
	"ONE DECLARED EDGE, carried as `compared: false` with its reason: the replay",
	"projection leaves State unset, and no client reader sees that value — the merge",
	"stamps dropped/restart on a projected steer the record never held before any reader",
	"is served. Asserting a label for it would assert a render that cannot happen.",
}

// steerLabels is the base label per (origin, state), and the contract the client keeps.
// TOTAL over the two closed enums; a triple with no entry here fails the test rather
// than defaulting, because a steer rendering with no label is the silent failure.
var steerLabels = map[marotte.SteerOrigin]map[marotte.SteerState]string{
	marotte.SteerOriginUser: {
		marotte.SteerStateRead:    "Mid-turn message",
		marotte.SteerStateDropped: "Not read",
	},
	marotte.SteerOriginAgent: {
		marotte.SteerStateRead:    "Workflow result",
		marotte.SteerStateDropped: "Workflow result not delivered",
	},
}

// steerReasonClauses is the wording for each reason a producer writes, and the half the
// client's REASONS table has to match. A producible reason absent here is the defect.
var steerReasonClauses = map[marotte.SteerReason]string{
	marotte.SteerReasonRestart:  "the session restarted",
	marotte.SteerReasonBoundary: "the turn ended first",
	marotte.SteerReasonDeleted:  "you deleted it",
}

// steerProjectionEdge is the one triple no client reader can see.
const steerProjectionEdge = "the replay projection's unset state: the merge stamps " +
	"dropped/restart before any reader is served, so no render exists to pin"

func TestSteerLabelContract(t *testing.T) {
	consts := steerPackageConsts(t)
	origins := steerEnumMembers(t, consts, "SteerOrigin")
	states := steerEnumMembers(t, consts, "SteerState")
	if strings.Join(origins, ",") != "agent,user" {
		t.Fatalf("SteerOrigin membership moved to %v; the fixture and its client reader "+
			"cover the two it had, so extend both before regenerating", origins)
	}
	if strings.Join(states, ",") != "dropped,read" {
		t.Fatalf("SteerState membership moved to %v; the fixture and its client reader "+
			"cover the two it had, so extend both before regenerating", states)
	}

	sites := steerProducerSites(t, consts)
	if len(sites) < 2 {
		t.Fatalf("the producer scan found %d sites; it reads the tree, so a set this "+
			"small means the scan broke rather than the code", len(sites))
	}

	rows := map[string]*steerLabelRow{}
	order := []string{}
	for _, s := range sites {
		for _, origin := range steerSiteOrigins(s, origins) {
			key := origin + "|" + s.State + "|" + s.Reason
			row, seen := rows[key]
			if !seen {
				row = &steerLabelRow{Origin: origin, State: s.State, Reason: s.Reason}
				rows[key] = row
				order = append(order, key)
			}
			row.Sites = append(row.Sites, s.Site)
		}
	}
	sort.Strings(order)

	fx := steerLabelFixture{
		Comment: steerLabelFixtureComment,
		Origins: origins,
		States:  states,
		Sites:   sites,
	}
	for _, key := range order {
		row := rows[key]
		sort.Strings(row.Sites)
		row.Sites = steerDedupe(row.Sites)
		if row.State == "" {
			row.Compared = false
			row.Edge = steerProjectionEdge
			fx.Triples = append(fx.Triples, *row)
			continue
		}
		label, ok := steerLabelFor(row.Origin, row.State, row.Reason)
		if !ok {
			t.Errorf("producible triple (%s, %s, reason %q) from %v has no contract "+
				"words: a steer renders with no label, which is the silent failure this "+
				"fixture exists to catch", row.Origin, row.State, row.Reason, row.Sites)
			continue
		}
		row.Label = label
		row.Compared = true
		fx.Triples = append(fx.Triples, *row)
	}

	steerAssertNonTautological(t, fx.Triples, origins, states)
	pinGolden(t, "testdata/steer_label.json", fx, "TestSteerLabelContract", "steer-label-contract.test.ts")
}

// steerLabelFor is the contract: the base label, plus the reason clause on a dropped row
// only. Answers false where no words exist for the pair.
func steerLabelFor(origin, state, reason string) (string, bool) {
	byState, ok := steerLabels[marotte.SteerOrigin(origin)]
	if !ok {
		return "", false
	}
	base, ok := byState[marotte.SteerState(state)]
	if !ok {
		return "", false
	}
	if reason == "" || state != string(marotte.SteerStateDropped) {
		return base, true
	}
	clause, ok := steerReasonClauses[marotte.SteerReason(reason)]
	if !ok {
		return "", false
	}
	return base + " · " + clause, true
}

// steerAssertNonTautological refuses a fixture a constant reader could satisfy: both
// states, both origins, a reason clause and a bare row all have to occur among the
// compared rows, and every compared row has to carry words.
func steerAssertNonTautological(t *testing.T, rows []steerLabelRow, origins, states []string) {
	t.Helper()
	pairs := map[string]bool{}
	reasoned, bare := 0, 0
	for _, r := range rows {
		if !r.Compared {
			continue
		}
		if r.Label == "" {
			t.Errorf("compared triple (%s, %s, reason %q) carries no label", r.Origin, r.State, r.Reason)
		}
		pairs[r.Origin+"|"+r.State] = true
		if r.Reason == "" {
			bare++
		} else {
			reasoned++
		}
	}
	for _, o := range origins {
		for _, s := range states {
			if !pairs[o+"|"+s] {
				t.Errorf("no compared triple for (%s, %s); the label table is total over "+
					"the enums, so a missing pair means the scan lost a producer", o, s)
			}
		}
	}
	if reasoned == 0 || bare == 0 {
		t.Errorf("compared rows carry %d reasoned and %d bare; both must occur or the "+
			"client reader cannot tell a worded clause from a missing one", reasoned, bare)
	}
}

// steerSiteOrigins expands a site's origin: the stated one, or both members where the
// origin comes from data. The enum is closed and every unresolved site reads an origin
// off a payload or an id, so both are genuinely producible there.
func steerSiteOrigins(s steerLabelSite, origins []string) []string {
	if s.Origin != "" {
		return []string{s.Origin}
	}
	return origins
}

func steerDedupe(in []string) []string {
	out := in[:0:0]
	for i, v := range in {
		if i == 0 || v != in[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// steerPackageConsts reads internal/marotte's steer and entry declarations and answers
// every string constant by name, with the type it was declared under.
func steerPackageConsts(t *testing.T) map[string]steerConst {
	t.Helper()
	out := map[string]steerConst{}
	for _, path := range []string{"../marotte/steer.go", "../marotte/entry.go"} {
		file, _ := steerParse(t, path)
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				typeName := ""
				if id, ok := vs.Type.(*ast.Ident); ok {
					typeName = id.Name
				}
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					v, err := strconv.Unquote(lit.Value)
					if err != nil {
						continue
					}
					out[name.Name] = steerConst{Type: typeName, Value: v}
				}
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("no string constants read from internal/marotte; the scan cannot resolve anything")
	}
	return out
}

type steerConst struct {
	Type  string
	Value string
}

// steerEnumMembers answers the sorted values of one closed enum.
func steerEnumMembers(t *testing.T, consts map[string]steerConst, typeName string) []string {
	t.Helper()
	var out []string
	for _, c := range consts {
		if c.Type == typeName {
			out = append(out, c.Value)
		}
	}
	if len(out) == 0 {
		t.Fatalf("no members read for %s", typeName)
	}
	sort.Strings(out)
	return out
}

// steerProducerSites scans every non-test Go file under internal/ for a construction or
// mutation of marotte.EntrySteer and answers what each one stamps.
func steerProducerSites(t *testing.T, consts map[string]steerConst) []steerLabelSite {
	t.Helper()
	var out []steerLabelSite
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" || d.Name() == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		out = append(out, steerSitesInFile(t, consts, path)...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/: %v", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Site < out[j].Site })
	return out
}

// steerSitesInFile is the per-file half: composite literals of marotte.EntrySteer, then
// the mutation form (a declared value whose State and Reason are assigned).
func steerSitesInFile(t *testing.T, consts map[string]steerConst, path string) []steerLabelSite {
	t.Helper()
	file, fset := steerParse(t, path)
	var out []steerLabelSite
	mutated := map[string]*steerLabelSite{}
	declared := map[string]bool{}

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CompositeLit:
			if !steerIsEntrySteer(node.Type) {
				return true
			}
			site := steerLabelSite{Site: steerSiteName(fset, path, node.Pos()), Form: "literal"}
			for _, elt := range node.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				steerSetField(t, consts, &site, key.Name, kv.Value)
			}
			out = append(out, site)
		case *ast.ValueSpec:
			if steerIsEntrySteer(node.Type) {
				for _, name := range node.Names {
					declared[name.Name] = true
				}
			}
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				base, ok := sel.X.(*ast.Ident)
				if !ok || !declared[base.Name] {
					continue
				}
				if sel.Sel.Name != "State" && sel.Sel.Name != "Reason" {
					continue
				}
				site, seen := mutated[base.Name]
				if !seen {
					site = &steerLabelSite{
						Site: steerSiteName(fset, path, node.Pos()),
						Form: "mutation",
					}
					mutated[base.Name] = site
				}
				steerSetField(t, consts, site, sel.Sel.Name, node.Rhs[0])
			}
		}
		return true
	})

	names := make([]string, 0, len(mutated))
	for name := range mutated {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out = append(out, *mutated[name])
	}
	return out
}

// steerSetField resolves one stamped field. An unresolvable Origin is left empty and
// expands to the whole enum later, because every such site reads an origin off data; an
// unresolvable State or Reason is a STOP, because widening those would let the fixture
// cover a value the code does not write, or miss one it does.
func steerSetField(t *testing.T, consts map[string]steerConst, site *steerLabelSite, field string, value ast.Expr) {
	t.Helper()
	switch field {
	case "Origin":
		if v, ok := steerResolve(consts, value); ok {
			site.Origin = v
		}
	case "State":
		v, ok := steerResolve(consts, value)
		if !ok {
			t.Fatalf("%s: State is an expression this scan cannot resolve; resolve it or "+
				"the fixture stops covering the code", site.Site)
		}
		site.State = v
	case "Reason":
		v, ok := steerResolve(consts, value)
		if !ok {
			t.Fatalf("%s: Reason is an expression this scan cannot resolve; resolve it or "+
				"the fixture stops covering the code", site.Site)
		}
		site.Reason = v
	}
}

// steerResolve answers a string literal, or a marotte constant by name.
func steerResolve(consts map[string]steerConst, e ast.Expr) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		if err != nil {
			return "", false
		}
		return s, true
	case *ast.SelectorExpr:
		pkg, ok := v.X.(*ast.Ident)
		if !ok || pkg.Name != "marotte" {
			return "", false
		}
		c, ok := consts[v.Sel.Name]
		if !ok {
			return "", false
		}
		return c.Value, true
	case *ast.Ident:
		c, ok := consts[v.Name]
		if !ok {
			return "", false
		}
		return c.Value, true
	}
	return "", false
}

func steerIsEntrySteer(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "marotte" && sel.Sel.Name == "EntrySteer"
}

// steerSiteName is the site as a reader of the repo would write it.
func steerSiteName(fset *token.FileSet, path string, pos token.Pos) string {
	return "internal/" + filepath.ToSlash(strings.TrimPrefix(filepath.Clean(path), "../")) +
		":" + strconv.Itoa(fset.Position(pos).Line)
}

func steerParse(t *testing.T, path string) (*ast.File, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return file, fset
}
