package marotte

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// turnSourceCount is the number of TurnOpenSource members domain_turn.go declares, read
// from its const block so a member added there is counted without a sentinel.
func turnSourceCount(t *testing.T) TurnOpenSource {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "domain_turn.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse domain_turn.go: %v", err)
	}
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST || len(gen.Specs) == 0 {
			continue
		}
		first, ok := gen.Specs[0].(*ast.ValueSpec)
		if !ok || len(first.Names) == 0 || first.Names[0].Name != "TurnSourcePrompt" {
			continue
		}
		var n TurnOpenSource
		for _, spec := range gen.Specs {
			n += TurnOpenSource(len(spec.(*ast.ValueSpec).Names))
		}
		return n
	}
	t.Fatal("domain_turn.go declares no TurnOpenSource const block starting at TurnSourcePrompt")
	return 0
}

// TestTurnSourcePredicates decides all three predicates for every member of the
// enum. The count is DERIVED from the const block rather than written twice, so a
// member added to the const block fails here instead of silently answering false
// for a predicate nobody decided about it — the mutation that measured this gap
// was widening PromptClass to a wire-opened source, which left the whole tree
// green.
func TestTurnSourcePredicates(t *testing.T) {
	rows := []struct {
		name                                       string
		src                                        TurnOpenSource
		promptClass, userAnswered, acknowledgeable bool
	}{
		{"prompt", TurnSourcePrompt, true, true, true},
		{"localShell", TurnSourceLocalShell, false, false, false},
		{"wireTurnStart", TurnSourceWireTurnStart, false, false, false},
		{"emptyRetry", TurnSourceEmptyRetry, true, true, true},
		{"workflowStep", turnSourceWorkflowStep, false, false, false},
	}
	if count := turnSourceCount(t); len(rows) != int(count) {
		t.Fatalf("the table covers %d sources, the enum has %d: decide every predicate for the new member",
			len(rows), count)
	}
	// A duplicated src would let a member go unasserted while the count still passes.
	seen := make(map[TurnOpenSource]string, len(rows))
	for _, row := range rows {
		if prev, dup := seen[row.src]; dup {
			t.Fatalf("row %q repeats the source row %q already covers", row.name, prev)
		}
		seen[row.src] = row.name
	}

	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			if got := row.src.PromptClass(); got != row.promptClass {
				t.Errorf("PromptClass() = %v, want %v", got, row.promptClass)
			}
			if got := row.src.UserAnswered(); got != row.userAnswered {
				t.Errorf("UserAnswered() = %v, want %v", got, row.userAnswered)
			}
			if got := row.src.Acknowledgeable(); got != row.acknowledgeable {
				t.Errorf("Acknowledgeable() = %v, want %v", got, row.acknowledgeable)
			}
		})
	}
}
