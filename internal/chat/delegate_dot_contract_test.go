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

// delegateDotFixture is the envelope of testdata/delegate_dot.json: every ToolStatus the
// server can stamp on a delegate's invocation call, the words the three client surfaces
// say for it, and whether the status is terminal.
//
// The status set is SCANNED from the producers rather than listed, for the reason the
// steer-label scan is: a listed producer set goes stale silently. The scan reads every
// non-test .go file under internal/, finds every reference to a ToolStatus constant, and
// classifies each by the POSITION it sits in — a `Status:` field of a composite literal,
// an assignment to a `.Status`, a return, a map key, a comparison, or none of those. A
// comparison is a READER and is excluded; everything else stamps a status somewhere, so
// the producible set is the union of the rest. That discrimination is what stops the scan
// being a restatement of "every constant the tree mentions": `Terminal`'s own body and
// four arms of the translator compare these constants without producing one.
//
// The WORDS are this side's statement of the contract, not a derivation: the server
// cannot ask the client for words. delegate-dot-contract.test.ts answers with the real
// subagentStatusFor, the real stateWord (through a card it builds in chromium) and the
// real toolState, so a status one of the three has no answer for fails THERE, and a
// status the server stamps with no contract words fails HERE.
//
// `terminal` is the one DERIVED column: it is marotte.ToolStatus.Terminal(), the
// predicate entrylog.go and turnlog.go settle a turn's calls by, and the client half
// asserts its own isToolDone/isToolActive against it. So the two languages' answers to
// "is this call over" are pinned against each other rather than each being restated.
type delegateDotFixture struct {
	Comment  []string           `json:"_comment"`
	Statuses []string           `json:"statuses"`
	Rows     []delegateDotRow   `json:"rows"`
	Stale    []delegateDotStale `json:"stale"`
	Sites    []delegateDotSite  `json:"sites"`
	Readers  []delegateDotReads `json:"readers"`
}

// delegateDotStale is the one row the status alone cannot answer: an invocation the log
// still reads as in flight inside a chat that holds no live turn of its own. The server
// settles such a call at the turn's close (Turn.Close and synthesizeCloseLocked both append
// tool_result{status: aborted}), so the client folds it onto that same status the moment its
// chat's liveness says the turn is over, and every surface then says what it already says
// for an aborted call. FoldsTo is what makes that claim checkable from either side.
type delegateDotStale struct {
	Status    string `json:"status"`
	TurnLive  bool   `json:"turn_live"`
	FoldsTo   string `json:"folds_to"`
	Dot       string `json:"dot"`
	CardWord  string `json:"card_word"`
	PageState string `json:"page_state"`
	PageWord  string `json:"page_word"`
}

// delegateDotRow is one producible status with the words each surface says for it.
type delegateDotRow struct {
	Status    string   `json:"status"`
	Dot       string   `json:"dot"`
	CardWord  string   `json:"card_word"`
	PageState string   `json:"page_state"`
	PageWord  string   `json:"page_word"`
	Terminal  bool     `json:"terminal"`
	Sites     []string `json:"sites"`
}

// delegateDotSite is one reference to a ToolStatus constant, at the path a failure should
// name, with the position that decided whether it produces or reads.
type delegateDotSite struct {
	Site   string `json:"site"`
	Form   string `json:"form"`
	Status string `json:"status"`
}

// delegateDotReads names one client reader and the column it answers, so a failure in the
// TypeScript half names the function rather than a JSON key.
type delegateDotReads struct {
	Column string `json:"column"`
	Reader string `json:"reader"`
}

// The classification of one reference to a ToolStatus constant. Every form but
// formCompare stamps a status somewhere.
const (
	formStatusField  = "status_field"
	formStatusAssign = "status_assign"
	formReturn       = "return"
	formMapKey       = "map_key"
	formCompare      = "compare"
	// formOther is the conservative default: a status constant sitting somewhere this
	// classifier does not name (an argument, a slice element) is counted as a PRODUCER, so
	// an unclassified position can only widen the producible set and make the words and
	// totality assertions stricter. A comparison is the one position read as a reader, and
	// it is recognised rather than assumed.
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

// delegateDotWordset is the contract for one status: the tab dot state, the card's
// announced word, and the page's state with the word it is announced as.
type delegateDotWordset struct {
	Dot       string
	CardWord  string
	PageState string
	PageWord  string
}

// delegateDotWords is the contract the client keeps, TOTAL over the ToolStatus enum. A
// produced status with no entry fails the test rather than defaulting, because a delegate
// rendering with no word is the silent failure this fixture exists to catch.
var delegateDotWords = map[marotte.ToolStatus]delegateDotWordset{
	marotte.ToolPending:    {Dot: "working", CardWord: "running", PageState: "pending", PageWord: "not started"},
	marotte.ToolInProgress: {Dot: "working", CardWord: "running", PageState: "running", PageWord: "running"},
	marotte.ToolCompleted:  {Dot: "done", CardWord: "succeeded", PageState: "ok", PageWord: "succeeded"},
	marotte.ToolFailed:     {Dot: "failed", CardWord: "failed", PageState: "fail", PageWord: "failed"},
	marotte.ToolAborted:    {Dot: "done", CardWord: "cancelled", PageState: "warn", PageWord: "stopped"},
}

// delegateDotReaders is the map from a fixture column to the production function that
// answers it, carried in the golden so a client-side failure names the function.
var delegateDotReaders = []delegateDotReads{
	{Column: "dot", Reader: "store.ts subagentStatusFor"},
	{Column: "card_word", Reader: "fundamentals/subagent-block.ts stateWord, through buildSubagentCard"},
	{Column: "page_state", Reader: "subagent-exec-source.ts toolState, through subagentToExec"},
	{Column: "page_word", Reader: "exec-view/status.ts STATE_WORD"},
	{Column: "terminal", Reader: "tool-schema.ts isToolDone and isToolActive"},
	{Column: "stale.folds_to", Reader: "store.ts delegateStatusFor"},
}

// delegateDotConsts reads internal/marotte's chat-domain declarations and answers every
// string constant by name with the type it was declared under. Its own reader rather than
// the steer contract's: that one names the two files a steer's fields are declared in, and
// ToolStatus is declared in a third.
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

// delegateDotStaleRows states the stale-spinner contract: an in-flight invocation whose
// chat holds no live turn of its own reads as the status the turn's close WOULD have
// settled it to, so the three surfaces say what they already say for that status and no
// surface gains a word of its own.
//
// It asserts rather than assumes the two facts that make the row non-vacuous: the stale
// status has to be one a producer can stamp (a fold from a status nothing produces pins
// nothing), and the folded status's words have to DIFFER from it on every surface (a fold
// onto identical words is a fold no reader can see).
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

// delegateDotAssertNonTautological refuses a fixture a constant reader could satisfy:
// each of the three vocabularies has to discriminate, both terminal answers have to
// occur, the deliberate three-words divergence has to be present, and the two
// structurally different producer forms have to have been found.
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

// delegateDotAssertDivergence pins the deliberate one-value-three-words divergence and
// the fold that goes with it: the card does not say the wire's word, the page does not
// say the card's, and the dot folds an abort onto a completion.
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

// delegateDotSites scans every non-test Go file under internal/ for a reference to a
// ToolStatus constant and answers what each one does with it.
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

// delegateDotSitesInFile is the per-file half: classify by POSITION in two passes, so a
// constant inside a comparison is never read as the thing a return or an assignment
// stamps. Pass one records the form of every constant a classifying node directly holds;
// pass two answers for every constant reference, defaulting to formOther.
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
				// A ToolStatus as a KEY declares a set: knownToolStatuses is what the
				// translator's settle path stamps whatever the wire carried.
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

// delegateDotStatus answers the value of a ToolStatus constant reference.
//
// The QUALIFIED form only (marotte.ToolAborted). A bare ToolAborted is the spelling inside
// internal/marotte itself, where the enum is declared and compared and nothing stamps a
// status on an entry; accepting it also double-counted every qualified reference, because a
// SelectorExpr's own Sel is an Ident at a different position, so each site arrived once
// classified and once as formOther.
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
