package agent

// A census gates the durable-write class: a write carrying a conversational record must run
// detached from shutdown; every other write is abandonable. A list cannot see an omission.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The three packages the class spans, read as source because agent imports translate. turnlog
// is included because every seal reaches the store through its Sink.Append.
var storeWritePackages = []string{"internal/agent", "internal/translate", "internal/turnlog"}

// What the census counts as a write: the entry store's verbs and the run log's appenders.
var storeWriteNames = []string{
	"Mutate", "Append", "AppendBetweenTurns", "OpenTurn", "Revert", "Rewrite",
	"Reconcile", "SetDraft", "SetAttachments", "WriteCounters",
	"CloseRun", "SwapMerged", "dropUnreadSteers", "appendLaneless", "appendBetweenTurns",
}

// A walk that finds nothing must fail. 44 measured; the slack covers a site moving, not a dropped callee class.
const storeWriteFloor = 35

// One function whose store writes carry a ruling.
type durableSite struct {
	file  string
	fn    string
	calls []string
	// because is what a failure loses, or what decides the context when this is not the decider.
	because string
}

// Sites carrying a conversational record. finalizeTurn is absent: its closers sit below one seam, tested below.
var durableWriteSites = []durableSite{{
	file:    "internal/agent/bridge_coord.go",
	fn:      "PersistModelSwitch",
	calls:   []string{"Mutate"},
	because: "the model_switched entry and the header's pick, which are on no KAS wire",
}, {
	file:    "internal/agent/bridge_coord.go",
	fn:      "PersistEffortChange",
	calls:   []string{"AppendBetweenTurns"},
	because: "the model_switched entry a reader's effort change leaves, which is on no KAS wire",
}, {
	file:    "internal/agent/bridge_coord.go",
	fn:      "closeTurnOnBridgeDeath",
	calls:   []string{"dropUnreadSteers"},
	because: "a dead session's dropped steers, whose text-less entries ARE the signal",
}, {
	file:    "internal/agent/bridge_coord.go",
	fn:      "PersistModeSwitch",
	calls:   []string{"AppendBetweenTurns"},
	because: "the mode_switched entry a landed mode switch leaves, which is on no KAS wire",
}, {
	file:    "internal/agent/load_projection.go",
	fn:      "swapProjectedTranscript",
	calls:   []string{"SwapMerged"},
	because: "the whole reconciled transcript",
}, {
	file:    "internal/translate/compact.go",
	fn:      "handleCompactionCompleted",
	calls:   []string{"Mutate"},
	because: "the compaction watermark that pairs with the compaction entry",
}, {
	file:    "internal/translate/compact.go",
	fn:      "handleCompactionFailed",
	calls:   []string{"appendLaneless"},
	because: "the compaction_failed entry, which is on no KAS wire",
}, {
	file:    "internal/translate/safety.go",
	fn:      "persistSafetyBlock",
	calls:   []string{"appendLaneless"},
	because: "an infrastructure-safety refusal, which is on no KAS wire",
}, {
	file:  "internal/translate/steering.go",
	fn:    "appendSteer",
	calls: []string{"appendLaneless"},
	because: "the reader's own mid-turn message and whether the agent read it — " +
		"a replay re-derives the row from KAS's log but never its delivery state",
}, {
	file:  "internal/translate/steering.go",
	fn:    "appendSteerInLane",
	calls: []string{"appendBetweenTurns"},
	because: "which agent read the steer, taken from its own ack — a replay carries the " +
		"steer's text but neither its delivery state nor the lane that consumed it",
}, {
	file:    "internal/agent/queue_drain.go",
	fn:      "HoldUnread",
	calls:   []string{"Mutate"},
	because: "a shutdown's unread steers as the Held row the next process shows",
}}

// Writes ruled not durable, each with its reason, so the omission is a decision.
var abandonableWriteSites = []durableSite{{
	file:    "internal/agent/turn_metering.go",
	fn:      "mutateUsage",
	calls:   []string{"Mutate"},
	because: "spend, re-derived from the next usage_summary",
}, {
	file:    "internal/agent/bridge_coord.go",
	fn:      "tryLoadSession",
	calls:   []string{"Mutate"},
	because: "session-chain bookkeeping — no conversational record, and the next spawn records it again",
}, {
	file:    "internal/agent/bridge_coord.go",
	fn:      "persistNewSessionMetadata",
	calls:   []string{"Mutate"},
	because: "the new session's id and facts — no conversational record, and a chat that lost them takes session/new next time",
}, {
	file:    "internal/agent/bridge_coord.go",
	fn:      "ReviseTurnBinding",
	calls:   []string{"OpenTurn", "WriteCounters"},
	because: "the agent's own turn_open on the frame's context: a bridge dying mid-frame has nothing to file under it, and the death closer closes what did open",
}, {
	file:    "internal/agent/turn_open.go",
	fn:      "OpenTurn",
	calls:   []string{"OpenTurn", "WriteCounters"},
	because: "a prompt's turn_open on the request's context: a request that died before its turn opened has no turn to record, and the caller answers the request's error",
}, {
	file:    "internal/agent/turn_open.go",
	fn:      "openWireTurn",
	calls:   []string{"OpenTurn", "WriteCounters"},
	because: "the wire bracket's turn_open on the frame's context, the same class as ReviseTurnBinding",
}, {
	file:    "internal/agent/model_switch.go",
	fn:      "cmdSwitchModel",
	calls:   []string{"Mutate"},
	because: "a model pick on the REQUEST's context, the same class as the command/* writes: a switch nobody is waiting for must not land",
}, {
	file:    "internal/agent/queue_drain.go",
	fn:      "AppendIfLive",
	calls:   []string{"Mutate"},
	because: "a queue_prompt on the REQUEST's context: a request that died never told the reader the row was queued, and the composer still holds the text",
}, {
	file:    "internal/agent/queue_drain.go",
	fn:      "Unqueue",
	calls:   []string{"Mutate"},
	because: "an unqueue_prompt on the REQUEST's context: a request that died never told the reader the row was discarded, and the dock still shows it",
}, {
	file:    "internal/translate/streaming_content.go",
	fn:      "appendSteerAcks",
	calls:   []string{"appendBetweenTurns"},
	because: "a steer acknowledgement stripped from a delta, on the frame's context: it lands or is lost with the delta it came out of",
}, {
	file:    "internal/translate/v3_updates.go",
	fn:      "persistUsage",
	calls:   []string{"Mutate"},
	because: "context usage, re-derived from the next frame",
}, {
	file:    "internal/translate/v3_updates.go",
	fn:      "HandleConfigOptionUpdate",
	calls:   []string{"Mutate"},
	because: "the model catalog, re-derived from the next frame",
}, {
	file:    "internal/translate/focus.go",
	fn:      "applyFocusTitle",
	calls:   []string{"Mutate"},
	because: "the chat title, re-derived",
}, {
	file:    "internal/translate/streaming_content.go",
	fn:      "HandleModeUpdate",
	calls:   []string{"Mutate"},
	because: "the session mode, re-derived",
}, {
	file:    "internal/translate/init_errors.go",
	fn:      "HandleAgentNotFound",
	calls:   []string{"Mutate"},
	because: "the fallback mode, re-derived",
}}

// Sites that take the caller's context and must not wrap it: the decision is not theirs.
var inheritedWriteSites = []durableSite{{
	file:    "internal/agent/turn_finalize.go",
	fn:      "closeTurn",
	calls:   []string{"WriteCounters"},
	because: "finalizeTurn's seam and closeTurnOnBridgeDeath's detach, its two callers",
}, {
	file:    "internal/agent/bridge_coord.go",
	fn:      "recordSteer",
	calls:   []string{"AppendBetweenTurns"},
	because: "its callers decide: dropUnreadSteers's caller's detach, holdUnreadSteers's detach, and the run bound's own derived lifecycle context",
}, {
	file:    "internal/agent/bridge_coord.go",
	fn:      "AppendBetweenTurns",
	calls:   []string{"AppendBetweenTurns"},
	because: "a forwarder for the command path; the command decides",
}, {
	file:    "internal/agent/command_deps.go",
	fn:      "OpenTurn",
	calls:   []string{"OpenTurn"},
	because: "a forwarder for the command path; the command decides",
}, {
	file:    "internal/agent/bridge_coord.go",
	fn:      "persistModelPick",
	calls:   []string{"Mutate"},
	because: "applyPendingModel's detach, and the spawn's own ctx at its fold",
}, {
	file:    "internal/agent/queue_drain.go",
	fn:      "NextUserRow",
	calls:   []string{"Mutate"},
	because: "the drain's context; its only write is a removal the store already owes, retried by the next mutation",
}, {
	file:    "internal/agent/model_switch.go",
	fn:      "clearPendingModel",
	calls:   []string{"Mutate"},
	because: "applyPendingModel's detach",
}, {
	file:    "internal/agent/entry_merge.go",
	fn:      "SwapMerged",
	calls:   []string{"Rewrite"},
	because: "swapProjectedTranscript's detach, the only caller",
}, {
	file:    "internal/agent/run_log.go",
	fn:      "Open",
	calls:   []string{"OpenTurn"},
	because: "the run appender's frame context",
}, {
	file:    "internal/agent/run_log.go",
	fn:      "Append",
	calls:   []string{"Append"},
	because: "the turnlog sink's caller, the run appender's frame context",
}, {
	file:    "internal/agent/run_log.go",
	fn:      "AppendAfterClosed",
	calls:   []string{"Append"},
	because: "the run appender's frame context",
}, {
	file:    "internal/agent/run_appender.go",
	fn:      "closeRun",
	calls:   []string{"CloseRun"},
	because: "the run_complete frame's context; the death arm through host closes what a cancelled frame left open",
}, {
	file:    "internal/translate/entries.go",
	fn:      "AppendBetweenTurns",
	calls:   []string{"AppendBetweenTurns"},
	because: "a helper every between-turns writer shares; each caller detaches or not",
}, {
	file:    "internal/translate/entries.go",
	fn:      "appendBetweenTurns",
	calls:   []string{"AppendBetweenTurns"},
	because: "appendLaneless's caller decides",
}, {
	file:    "internal/translate/entries.go",
	fn:      "appendLaneless",
	calls:   []string{"appendBetweenTurns"},
	because: "every caller hands it a detached context; a wrap here would hide a caller that forgot",
}, {
	file:    "internal/turnlog/turnlog.go",
	fn:      "append",
	calls:   []string{"Append"},
	because: "the seal path; the frame's context, refused on a dead ctx by EntryLog.Append, which the turnlog and entrylog tests pin",
}}

// The seam: one detach, below the position wait, above every closer. Either misplacement fails
// (refused at shutdown, or an unbounded wait).
func TestFinalizeTurn_EveryDurableWriteRunsDetached(t *testing.T) {
	const rel = "internal/agent/turn_finalize.go"
	fset, file := parseModuleFile(t, rel)
	fn := funcDeclNamed(t, file, rel, "finalizeTurn")

	var seams []token.Pos
	var closerSwitch, positionWait token.Pos
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			if isDurableReassign(node) {
				seams = append(seams, node.Pos())
			}
		case *ast.SwitchStmt:
			if node.Tag != nil && exprText(fset, node.Tag) == "tc.Closer" {
				closerSwitch = node.Pos()
			}
		case *ast.CallExpr:
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "awaitPosition" {
				positionWait = node.Pos()
			}
		}
		return true
	})

	if len(seams) != 1 {
		t.Fatalf("finalizeTurn holds %d `ctx = durable.Context(ctx)` seams, want exactly 1: "+
			"two of them means part of the dispatch is still attached", len(seams))
	}
	seam := seams[0]
	if !positionWait.IsValid() {
		t.Fatal("finalizeTurn no longer calls awaitPosition, so the seam has nothing to sit below")
	}
	if positionWait > seam {
		t.Errorf("awaitPosition at %s runs BELOW the seam at %s; ctx.Done() is its only timed escape, "+
			"so a wedged agent process now hangs the shutdown instead of losing one turn",
			fset.Position(positionWait), fset.Position(seam))
	}
	if !closerSwitch.IsValid() {
		t.Fatal("finalizeTurn no longer dispatches on tc.Closer; the seam covers an unknown set")
	}
	if closerSwitch < seam {
		t.Errorf("the closer switch at %s runs ABOVE the seam at %s, so every closer's writes are attached",
			fset.Position(closerSwitch), fset.Position(seam))
	}

	// The helpers take finalizeTurn's ctx, so a call above the seam is refused.
	persistHelpers := map[string]bool{"closeTurn": true}
	var seen, above []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !persistHelpers[sel.Sel.Name] {
			return true
		}
		at := sel.Sel.Name + " at " + fset.Position(call.Pos()).String()
		seen = append(seen, at)
		if call.Pos() < seam {
			above = append(above, at)
		}
		return true
	})
	if len(seen) < len(persistHelpers) {
		t.Errorf("found %d persist-helper calls in %s (%v), want at least one per helper %v; "+
			"the walk stopped looking rather than found nothing", len(seen), rel, seen, persistHelpers)
	}
	if len(above) > 0 {
		t.Errorf("these persist calls run ABOVE the seam, so a shutdown refuses them:\n  %s",
			strings.Join(above, "\n  "))
	}
}

// A store write in a function no list names is the omission the gate exists for.
func TestDurableWrites_EveryStoreWriteCarriesARuling(t *testing.T) {
	ruled := map[string]string{}
	for _, list := range []struct {
		name  string
		sites []durableSite
	}{
		{"durableWriteSites", durableWriteSites},
		{"abandonableWriteSites", abandonableWriteSites},
		{"inheritedWriteSites", inheritedWriteSites},
	} {
		for _, site := range list.sites {
			key := site.file + "/" + site.fn
			if other, dup := ruled[key]; dup {
				t.Errorf("%s is listed in both %s and %s; one site carries one ruling, "+
					"and two of them means the gate asserts both a detach and an attach", key, other, list.name)
			}
			ruled[key] = list.name
		}
	}

	var writes []storeWrite
	for _, dir := range storeWritePackages {
		writes = append(writes, censusStoreWrites(t, dir)...)
	}
	if len(writes) < storeWriteFloor {
		t.Fatalf("the census found %d store writes across %v, fewer than the floor of %d: "+
			"the walk stopped finding them rather than the class shrinking",
			len(writes), storeWritePackages, storeWriteFloor)
	}

	var unruled []string
	seen := map[string]bool{}
	for _, w := range writes {
		seen[w.site] = true
		if _, ok := ruled[w.site]; !ok {
			unruled = append(unruled, w.name+" in "+w.site+" at "+w.at)
		}
	}
	if len(unruled) > 0 {
		t.Errorf("these store writes carry no ruling; rule each one durable, abandonable or inherited "+
			"before it ships, because a shutdown decides it either way:\n  %s", strings.Join(unruled, "\n  "))
	}
	for key, list := range ruled {
		if !seen[key] {
			t.Errorf("%s is listed in %s and performs no store write; drop the row rather than "+
				"leaving a ruling for code that has moved", key, list)
		}
	}
}

// The non-finalize half: handlers called from the translate cascade and Forward.
func TestDurableWrites_EverySiteCarryingAConversationalRecordIsDetached(t *testing.T) {
	for _, site := range durableWriteSites {
		t.Run(site.file+"/"+site.fn, func(t *testing.T) {
			found, attached := siteWrites(t, site)
			if missing := missingWrites(found, site.calls); len(missing) > 0 {
				t.Fatalf("%s makes none of these writes it is listed for (%v); the list describes "+
					"code that has moved", site.fn, missing)
			}
			if len(attached) > 0 {
				t.Errorf("these writes run on an attached context, so a shutdown discards %s:\n  %s",
					site.because, strings.Join(formatWrites(attached), "\n  "))
			}
		})
	}
}

// Each abandonable ruling must still describe real code, or a rename empties the list.
func TestDurableWrites_TheAbandonableSitesAreExemptedNotForgotten(t *testing.T) {
	for _, site := range abandonableWriteSites {
		t.Run(site.file+"/"+site.fn, func(t *testing.T) {
			found, attached := siteWrites(t, site)
			if missing := missingWrites(found, site.calls); len(missing) > 0 {
				t.Fatalf("%s makes none of these writes it is exempted for (%v); re-judge it rather "+
					"than leaving a stale exemption", site.fn, missing)
			}
			if len(attached) != len(found) {
				t.Errorf("%s was detached without its exemption being revisited; it writes %s, "+
					"so the detach buys nothing and moves the site out of this list silently",
					site.fn, site.because)
			}
		})
	}
}

// A detach inside a shared persist helper would change shutdown behaviour unreviewed.
func TestDurableWrites_TheInheritedSitesDecideNothing(t *testing.T) {
	for _, site := range inheritedWriteSites {
		t.Run(site.file+"/"+site.fn, func(t *testing.T) {
			found, attached := siteWrites(t, site)
			if missing := missingWrites(found, site.calls); len(missing) > 0 {
				t.Fatalf("%s makes none of these writes it is listed for (%v); the list describes "+
					"code that has moved", site.fn, missing)
			}
			if len(attached) != len(found) {
				t.Errorf("%s wraps a context it does not own; %s decides it, and a wrap here changes "+
					"every caller at once instead of the one under review", site.fn, site.because)
			}
		})
	}
}

// One store write the census found, keyed by the function that performs it.
type storeWrite struct {
	site string
	name string
	at   string
}

// Every store write in one package directory's production files.
func censusStoreWrites(t *testing.T, dir string) []storeWrite {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(moduleRoot(t), dir))
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []storeWrite
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		rel := dir + "/" + name
		fset, file := parseModuleFile(t, rel)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			for _, w := range writeCallsIn(fset, fn.Body) {
				out = append(out, storeWrite{site: rel + "/" + fn.Name.Name, name: w.name, at: w.at})
			}
		}
	}
	return out
}

type writeCall struct {
	name     string
	at       string
	detached bool
}

// One listed site's writes, and the subset running on an ATTACHED context.
func siteWrites(t *testing.T, site durableSite) (found, attached []writeCall) {
	t.Helper()
	fset, file := parseModuleFile(t, site.file)
	fn := funcDeclNamed(t, file, site.file, site.fn)
	found = writeCallsIn(fset, fn.Body)
	for _, w := range found {
		if !w.detached {
			attached = append(attached, w)
		}
	}
	return found, attached
}

// A write is detached when its ctx argument is durable.Context(...) or ctx was reassigned from it.
func writeCallsIn(fset *token.FileSet, body *ast.BlockStmt) []writeCall {
	want := make(map[string]bool, len(storeWriteNames))
	for _, n := range storeWriteNames {
		want[n] = true
	}
	var out []writeCall
	reassigned := token.NoPos
	ast.Inspect(body, func(n ast.Node) bool {
		if as, ok := n.(*ast.AssignStmt); ok && isDurableReassign(as) && !reassigned.IsValid() {
			reassigned = as.Pos()
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := calleeName(call)
		if !want[name] {
			return true
		}
		out = append(out, writeCall{
			name:     name,
			at:       fset.Position(call.Pos()).String(),
			detached: isDurableCall(call.Args) || (reassigned.IsValid() && reassigned < call.Pos()),
		})
		return true
	})
	return out
}

func missingWrites(found []writeCall, want []string) []string {
	have := make(map[string]bool, len(found))
	for _, w := range found {
		have[w.name] = true
	}
	var missing []string
	for _, n := range want {
		if !have[n] {
			missing = append(missing, n)
		}
	}
	return missing
}

func formatWrites(calls []writeCall) []string {
	out := make([]string, 0, len(calls))
	for _, w := range calls {
		out = append(out, w.name+" at "+w.at)
	}
	return out
}

// Whether stmt is `ctx = durable.Context(…)` or `ctx := durable.Context(…)`.
func isDurableReassign(stmt *ast.AssignStmt) bool {
	if (stmt.Tok != token.ASSIGN && stmt.Tok != token.DEFINE) || len(stmt.Lhs) != 1 || len(stmt.Rhs) != 1 {
		return false
	}
	id, ok := stmt.Lhs[0].(*ast.Ident)
	if !ok || id.Name != "ctx" {
		return false
	}
	return isDurableCall(stmt.Rhs)
}

// Whether the first expression is a durable.Context call.
func isDurableCall(args []ast.Expr) bool {
	if len(args) == 0 {
		return false
	}
	call, ok := args[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "durable" && sel.Sel.Name == "Context"
}

func parseModuleFile(t *testing.T, rel string) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(moduleRoot(t), rel), nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	return fset, file
}

func funcDeclNamed(t *testing.T, file *ast.File, rel, name string) *ast.FuncDecl {
	t.Helper()
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name && fn.Body != nil {
			return fn
		}
	}
	t.Fatalf("%s declares no %s; the site list names code that no longer exists", rel, name)
	return nil
}

// An expression rendered back to source, for comparing a switch tag.
func exprText(fset *token.FileSet, expr ast.Expr) string {
	start := fset.Position(expr.Pos())
	end := fset.Position(expr.End())
	data, err := os.ReadFile(start.Filename)
	if err != nil || end.Offset > len(data) {
		return ""
	}
	return string(data[start.Offset:end.Offset])
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}
