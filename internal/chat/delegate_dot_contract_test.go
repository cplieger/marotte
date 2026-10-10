package chat

import (
	"go/ast"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// delegateDotFixture is testdata/delegate_dot.json: every ToolStatus a delegate's invocation can carry, each
// surface's words for it, and whether it is terminal. The status set is scanned from producers: every reference to a
// ToolStatus constant under internal/ is classified by position, and only comparisons count as readers. The words are
// this side's contract; delegate-dot-contract.test.ts answers with the real client functions. `terminal` is derived
// from marotte.ToolStatus.Terminal(), pinning both languages' "is this call over".
type delegateDotFixture struct {
	Comment  []string           `json:"_comment"`
	Statuses []string           `json:"statuses"`
	Rows     []delegateDotRow   `json:"rows"`
	Stale    []delegateDotStale `json:"stale"`
	Sites    []delegateDotSite  `json:"sites"`
	Readers  []delegateDotReads `json:"readers"`
}

// The server settles it at the turn's close as aborted, so the client folds it onto that status;
// FoldsTo makes that checkable from either side.
type delegateDotStale struct {
	Status    string `json:"status"`
	TurnLive  bool   `json:"turn_live"`
	FoldsTo   string `json:"folds_to"`
	Dot       string `json:"dot"`
	CardWord  string `json:"card_word"`
	PageState string `json:"page_state"`
	PageWord  string `json:"page_word"`
}

type delegateDotRow struct {
	Status    string   `json:"status"`
	Dot       string   `json:"dot"`
	CardWord  string   `json:"card_word"`
	PageState string   `json:"page_state"`
	PageWord  string   `json:"page_word"`
	Terminal  bool     `json:"terminal"`
	Sites     []string `json:"sites"`
}

type delegateDotSite struct {
	Site   string `json:"site"`
	Form   string `json:"form"`
	Status string `json:"status"`
}

// delegateDotReads names one client reader and the column it answers, so a TypeScript failure names the function.
type delegateDotReads struct {
	Column string `json:"column"`
	Reader string `json:"reader"`
}

// How one reference to a ToolStatus constant is classified; every form but formCompare stamps a status.
const (
	formStatusField  = "status_field"
	formStatusAssign = "status_assign"
	formReturn       = "return"
	formMapKey       = "map_key"
	formCompare      = "compare"
	// formOther is the conservative default: an unclassified position counts as a producer, which can only widen the set
	// and tighten the assertions.
	formOther = "other"
)

var delegateDotFixtureComment = []string{
	"The delegate DOT contract: every ToolStatus a producer can stamp on a delegate's",
	"invocation call, and what each of the three client surfaces says about it.",
	"",
	"Regenerate:  UPDATE_GOLDEN=1 go test ./internal/chat/ -run TestDelegateDotContract",
	"Re-run the other reader:  npx vitest --run delegate-dot-contract",
	"",
	"`rows` is SCANNED from the producers, never listed: the scan classifies every",
	"reference to a ToolStatus constant under internal/ by its POSITION, counts every",
	"position but a comparison as a producer, and requires the producible set to be the",
	"whole declared enum. `sites` is the scan's own evidence, so a new producer or a new",
	"enum member moves this golden and the fixture has to be read before it is",
	"regenerated.",
	"",
	"The words are the SERVER's statement of the client's contract; `terminal` is derived",
	"from marotte.ToolStatus.Terminal(), the predicate the close paths settle calls by.",
	"",
	"`stale` is the one row a status alone cannot answer: an invocation the log still reads",
	"as in flight inside a chat holding no live turn of its own. The close settles such a",
	"call as aborted (Turn.Close, synthesizeCloseLocked), so the client folds it there the",
	"moment its chat's liveness says the turn is over and every surface says what it already",
	"says for that status — `folds_to` is what makes the claim checkable from both sides.",
	"",
	"ONE wire value reaches THREE announced words, deliberately: aborted is the wire's",
	"spelling, a card says 'cancelled' and the page says 'stopped'. The row surface's own word is",
	"tool-card.ts's outcomeWord, which is unexported and is therefore not a compared",
	"column here; the card and the page are.",
}

// delegateDotWordset is one status's contract: the tab dot state, the card's word, and the page's state and word.
type delegateDotWordset struct {
	Dot       string
	CardWord  string
	PageState string
	PageWord  string
}

// delegateDotWords is the client's contract, total over ToolStatus: a produced status with no entry fails rather
// than rendering wordless.
var delegateDotWords = map[marotte.ToolStatus]delegateDotWordset{
	marotte.ToolPending:    {Dot: "working", CardWord: "running", PageState: "pending", PageWord: "not started"},
	marotte.ToolInProgress: {Dot: "working", CardWord: "running", PageState: "running", PageWord: "running"},
	marotte.ToolCompleted:  {Dot: "done", CardWord: "succeeded", PageState: "ok", PageWord: "succeeded"},
	marotte.ToolFailed:     {Dot: "failed", CardWord: "failed", PageState: "fail", PageWord: "failed"},
	marotte.ToolAborted:    {Dot: "done", CardWord: "cancelled", PageState: "warn", PageWord: "stopped"},
}

// delegateDotReaders maps a fixture column to its production function, so a client failure names it.
var delegateDotReaders = []delegateDotReads{
	{Column: "dot", Reader: "store.ts subagentStatusFor"},
	{Column: "card_word", Reader: "fundamentals/subagent-block.ts stateWord, through buildSubagentCard"},
	{Column: "page_state", Reader: "subagent-exec-source.ts toolState, through subagentToExec"},
	{Column: "page_word", Reader: "exec-view/status.ts STATE_WORD"},
	{Column: "terminal", Reader: "tool-schema.ts isToolDone and isToolActive"},
	{Column: "stale.folds_to", Reader: "store.ts delegateStatusFor"},
}

// delegateDotConsts reads internal/marotte's chat-domain declarations and returns every string constant by name with
// its type. ToolStatus lives in a file the steer contract's reader does not read.
func delegateDotConsts(t *testing.T) map[string]steerConst {
	t.Helper()
	out := map[string]steerConst{}
	file, _ := steerParse(t, "../marotte/domain_chat.go")
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
	if len(out) == 0 {
		t.Fatalf("no string constants read from internal/marotte/domain_chat.go; the scan " +
			"cannot resolve anything")
	}
	return out
}

func TestDelegateDotContract(t *testing.T) {
	consts := delegateDotConsts(t)
	statuses := steerEnumMembers(t, consts, "ToolStatus")
	if strings.Join(statuses, ",") != "aborted,completed,failed,in_progress,pending" {
		t.Fatalf("ToolStatus membership moved to %v; a new member is a coordinated wire "+
			"change, so extend delegateDotWords and the client reader before "+
			"regenerating", statuses)
	}

	sites := delegateDotSites(t, consts)
	produced := map[string][]string{}
	forms := map[string]int{}
	for _, s := range sites {
		forms[s.Form]++
		if s.Form == formCompare {
			continue
		}
		produced[s.Status] = append(produced[s.Status], s.Site)
	}

	fx := delegateDotFixture{
		Comment:  delegateDotFixtureComment,
		Statuses: statuses,
		Sites:    sites,
		Readers:  delegateDotReaders,
	}
	for _, status := range statuses {
		where := produced[status]
		if len(where) == 0 {
			t.Errorf("the enum declares %q and the scan found no producer for it: either a "+
				"member is dead or the scan lost a form", status)
			continue
		}
		words, ok := delegateDotWords[marotte.ToolStatus(status)]
		if !ok {
			t.Errorf("producible status %q from %v has no contract words: a delegate renders "+
				"with no word, which is the silent failure this fixture exists to catch",
				status, where)
			continue
		}
		sort.Strings(where)
		fx.Rows = append(fx.Rows, delegateDotRow{
			Status:    status,
			Dot:       words.Dot,
			CardWord:  words.CardWord,
			PageState: words.PageState,
			PageWord:  words.PageWord,
			Terminal:  marotte.ToolStatus(status).Terminal(),
			Sites:     delegateDotDedupe(where),
		})
	}

	fx.Stale = delegateDotStaleRows(t, produced)

	delegateDotAssertNonTautological(t, fx.Rows, forms)
	pinGolden(t, "testdata/delegate_dot.json", fx, "TestDelegateDotContract", "delegate-dot-contract.test.ts")
}

// delegateDotStaleRows states the stale-spinner contract: such an invocation reads as the status the turn's close
// would have given it. It asserts the stale status is producible and that the folded words differ on every surface.
func delegateDotStaleRows(t *testing.T, produced map[string][]string) []delegateDotStale {
	t.Helper()
	const (
		from = marotte.ToolInProgress
		to   = marotte.ToolAborted
	)
	if len(produced[string(from)]) == 0 {
		t.Errorf("the stale row folds %q, which the scan found no producer for: the row "+
			"pins a state no server can reach", from)
		return nil
	}
	live, folded := delegateDotWords[from], delegateDotWords[to]
	if live.Dot == folded.Dot || live.CardWord == folded.CardWord || live.PageState == folded.PageState {
		t.Errorf("the stale fold %q -> %q is invisible on at least one surface (%+v vs %+v): "+
			"a reader cannot tell a stalled delegate from a working one", from, to, live, folded)
		return nil
	}
	return []delegateDotStale{{
		Status:    string(from),
		TurnLive:  false,
		FoldsTo:   string(to),
		Dot:       folded.Dot,
		CardWord:  folded.CardWord,
		PageState: folded.PageState,
		PageWord:  folded.PageWord,
	}}
}

// delegateDotAssertNonTautological refuses a fixture a constant reader could satisfy: every vocabulary
// discriminates, both terminal answers occur, the three-word divergence is present, and both producer forms were found.
func delegateDotAssertNonTautological(t *testing.T, rows []delegateDotRow, forms map[string]int) {
	t.Helper()
	dots, cards, pages := map[string]bool{}, map[string]bool{}, map[string]bool{}
	terminal, live := 0, 0
	for _, r := range rows {
		if r.Dot == "" || r.CardWord == "" || r.PageState == "" || r.PageWord == "" {
			t.Errorf("status %q carries an empty column: %+v", r.Status, r)
		}
		dots[r.Dot] = true
		cards[r.CardWord] = true
		pages[r.PageState] = true
		if r.Terminal {
			terminal++
		} else {
			live++
		}
	}
	if len(dots) < 3 || len(cards) < 3 || len(pages) < 3 {
		t.Errorf("the vocabularies read %d dots, %d card words and %d page states; each has "+
			"to discriminate or the client half cannot fail", len(dots), len(cards), len(pages))
	}
	if terminal == 0 || live == 0 {
		t.Errorf("rows carry %d terminal and %d live; both must occur or the client's "+
			"isToolDone assertion cannot fail", terminal, live)
	}
	if forms[formStatusField] == 0 || forms[formMapKey] == 0 {
		t.Errorf("the scan found %d Status-field sites and %d map-key sites; both forms are "+
			"live in the tree, so a zero means the scan lost an arm",
			forms[formStatusField], forms[formMapKey])
	}
	if forms[formCompare] == 0 {
		t.Errorf("the scan classified no comparison; the producer set is then every constant " +
			"the tree mentions, which is the restatement this scan exists not to be")
	}
	delegateDotAssertDivergence(t, rows)
}

// delegateDotAssertDivergence pins the deliberate one-value-three-words divergence and its fold: card and wire,
// page and card differ, and the dot folds an abort onto a completion.
func delegateDotAssertDivergence(t *testing.T, rows []delegateDotRow) {
	t.Helper()
	byStatus := map[string]delegateDotRow{}
	for _, r := range rows {
		byStatus[r.Status] = r
	}
	aborted, ok := byStatus[string(marotte.ToolAborted)]
	if !ok {
		t.Fatalf("no row for %q; it is the status the divergence is about", marotte.ToolAborted)
	}
	if aborted.CardWord == aborted.Status || aborted.PageWord == aborted.CardWord {
		t.Errorf("the abort's three words read %q (wire), %q (card) and %q (page); the "+
			"divergence is deliberate and the fixture is what keeps it stated",
			aborted.Status, aborted.CardWord, aborted.PageWord)
	}
	completed, ok := byStatus[string(marotte.ToolCompleted)]
	if ok && aborted.Dot != completed.Dot {
		t.Errorf("the abort's dot is %q and a completion's is %q; a delegate the reader "+
			"stopped is over rather than broken, which is the fold subagentStatusFor makes",
			aborted.Dot, completed.Dot)
	}
}

func delegateDotSites(t *testing.T, consts map[string]steerConst) []delegateDotSite {
	t.Helper()
	var out []delegateDotSite
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
		out = append(out, delegateDotSitesInFile(t, consts, path)...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/: %v", err)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Site != out[j].Site {
			return out[i].Site < out[j].Site
		}
		return out[i].Status < out[j].Status
	})
	if len(out) < len(delegateDotWords) {
		t.Fatalf("the scan found %d references to a ToolStatus constant; it reads the tree, "+
			"so a set this small means the scan broke rather than the code", len(out))
	}
	return out
}

// delegateDotSitesInFile classifies one file's references by position in two passes, so a constant in a comparison
// is never read as stamped; unmatched references default to formOther.
func delegateDotSitesInFile(t *testing.T, consts map[string]steerConst, path string) []delegateDotSite {
	t.Helper()
	file, fset := steerParse(t, path)
	form := map[token.Pos]string{}

	mark := func(e ast.Expr, f string) {
		if _, ok := delegateDotStatus(consts, e); !ok {
			return
		}
		if _, seen := form[e.Pos()]; !seen {
			form[e.Pos()] = f
		}
	}

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CompositeLit:
			for _, elt := range node.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				// A ToolStatus as a map key declares a set: knownToolStatuses is what the settle path stamps.
				mark(kv.Key, formMapKey)
				if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Status" {
					mark(kv.Value, formStatusField)
				}
			}
		case *ast.AssignStmt:
			for _, rhs := range node.Rhs {
				mark(rhs, formStatusAssign)
			}
		case *ast.ReturnStmt:
			for _, res := range node.Results {
				mark(res, formReturn)
			}
		case *ast.BinaryExpr:
			if node.Op == token.EQL || node.Op == token.NEQ {
				mark(node.X, formCompare)
				mark(node.Y, formCompare)
			}
		case *ast.CaseClause:
			for _, v := range node.List {
				mark(v, formCompare)
			}
		}
		return true
	})

	var out []delegateDotSite
	ast.Inspect(file, func(n ast.Node) bool {
		e, ok := n.(ast.Expr)
		if !ok {
			return true
		}
		status, ok := delegateDotStatus(consts, e)
		if !ok {
			return true
		}
		f, seen := form[e.Pos()]
		if !seen {
			f = formOther
		}
		out = append(out, delegateDotSite{
			Site:   steerSiteName(fset, path, e.Pos()),
			Form:   f,
			Status: status,
		})
		return true
	})
	return out
}

// delegateDotStatus answers the value of a qualified ToolStatus reference (marotte.ToolAborted). A bare ident is
// internal/marotte's own spelling, which stamps nothing, and accepting it double-counted each selector's Sel.
func delegateDotStatus(consts map[string]steerConst, e ast.Expr) (string, bool) {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "marotte" {
		return "", false
	}
	c, ok := consts[sel.Sel.Name]
	if !ok || c.Type != "ToolStatus" {
		return "", false
	}
	return c.Value, true
}

func delegateDotDedupe(in []string) []string {
	out := in[:0:0]
	for i, v := range in {
		if i == 0 || v != in[i-1] {
			out = append(out, v)
		}
	}
	return out
}
