package agent

// Two structural certifications over the stamping rule, as go/ast walks of the minting packages: a stamp whose
// Version came from a later read is a hole no behavioural test reliably reaches. (i) Every SubjectStamp takes
// its Version from a parameter or an allowlisted minting call; chats-kind on chat_created/updated/deleted,
// chat-kind on subject_changed/draft_changed; a Mutate caller stamps chat at most once. (ii) Only emit and
// streamInitialState publish, reaching versions only through MergeStamped or the snapshot helpers.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// stampWalkPackages are the directories the two tests walk, relative to this one.
var stampWalkPackages = []string{
	".",
	"../chat",
	"../chat/archive",
	"../translate",
	"../command",
}

// versionSources are the calls a stamp may take its Version from: lock-holding store writers, Buffer mutators and stamped snapshot helpers.
var versionSources = []string{
	"Mutate", "Remove", "setComposer", "Reconcile", "stepTurns",
	"MarkOverCap", "StartTurn", "SplitSegment", "AppendToolCall", "SetToolCall",
	"SetSteerCarry", "AppendCodeReferences", "SetModel", "SetRefusal",
	"AppendTextDelta", "AppendThinkingDelta", "AppendToolUseBlock",
	"TrackFileChanges", "RecordToolStart", "ComputeDuration", "MarkInFlightToolsAborted",
	"MergeStamped", "SnapshotStamped", "pendingSnapshotStamped", "pendingSnapshot",
	"liveRunRowsStamped", "ModesModelsStamped", "ListStamped",
}

// mintingBodies are the critical sections themselves, exempted while their callers are checked. The four page
// builders read their stamps under the lock serving the entries; live_turn and run_turn versions are newest served seqs.
var mintingBodies = []string{
	"Mutate", "broadcastMutation", "setComposer", "Remove",
	"MergeStamped", "SnapshotStamped", "registry", "statusStamp",
	"pendingSnapshot", "liveRunRowsStamped", "ModesModelsStamped", "mintPending",
	"ClearWaiting", "Clear", "SetModes", "SetModels",
	"Page", "TurnPage", "stepTurns", "turnRange",
}

// versionReads are the registry reads a stamping function may not perform itself.
var versionReads = []string{"Current", "BumpCounter"}

var (
	chatsFrames = []string{"EventChatCreated", "EventChatUpdated", "EventChatDeleted"}
	chatFrames  = []string{"EventSubjectChanged", "EventDraftChanged"}
)

type stampSite struct {
	fn      *ast.FuncDecl
	file    string
	kind    string   // "chats", "chat", ... or "" when not a literal kind
	version ast.Expr // the Version expression, nil when the whole stamp came from a call
	from    ast.Expr // the whole stamp expression
	built   ast.Expr // the construction node, so one stamp assigned twice counts once
	frames  []string // the Event* constants the frame it lands on can carry, when traceable
}

func parseStampPackages(t *testing.T) map[string][]*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	files := map[string][]*ast.File{}
	for _, dir := range stampWalkPackages {
		names, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatalf("Setup: glob %s: %v", dir, err)
		}
		for _, name := range names {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(fset, name, nil, 0)
			if err != nil {
				t.Fatalf("Setup: parse %s: %v", name, err)
			}
			files[name] = append(files[name], f)
		}
	}
	if len(files) < 20 {
		t.Fatalf("Setup: parsed %d files across %v, want the four packages", len(files), stampWalkPackages)
	}
	return files
}

func calleeName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	}
	return ""
}

// kindOf reads a stamp's kind: string(subject.KindChats) → "chats", or a literal.
func kindOf(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return strings.Trim(e.Value, `"`)
	case *ast.CallExpr:
		if id, ok := e.Fun.(*ast.Ident); ok && id.Name == "string" && len(e.Args) == 1 {
			if sel, ok := e.Args[0].(*ast.SelectorExpr); ok {
				return strings.ToLower(strings.TrimPrefix(sel.Sel.Name, "Kind"))
			}
		}
	}
	return ""
}

// stampExpr recognises NewSubjectStamp(kind, ref, version) or a SubjectStamp literal, returning (kind, version, ok).
func stampExpr(expr ast.Expr) (kind string, version ast.Expr, ok bool) {
	if u, isAddr := expr.(*ast.UnaryExpr); isAddr && u.Op == token.AND {
		expr = u.X
	}
	switch e := expr.(type) {
	case *ast.CallExpr:
		if calleeName(e) == "NewSubjectStamp" && len(e.Args) == 3 {
			return kindOf(e.Args[0]), e.Args[2], true
		}
	case *ast.CompositeLit:
		if sel, isSel := e.Type.(*ast.SelectorExpr); isSel && sel.Sel.Name == "SubjectStamp" {
			for _, elt := range e.Elts {
				kv, isKV := elt.(*ast.KeyValueExpr)
				if !isKV {
					continue
				}
				switch kv.Key.(*ast.Ident).Name {
				case "Kind":
					kind = kindOf(kv.Value)
				case "Version":
					version = kv.Value
				}
			}
			return kind, version, true
		}
	}
	return "", nil, false
}

// definitions maps each identifier a body defines to its defining expression (positional), plus parameter names.
type definitions struct {
	params map[string]bool
	defs   map[string][]ast.Expr
}

func collectDefinitions(fn *ast.FuncDecl) definitions {
	d := definitions{params: map[string]bool{}, defs: map[string][]ast.Expr{}}
	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			d.params[name.Name] = true
		}
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range as.Lhs {
			id, isID := lhs.(*ast.Ident)
			if !isID {
				continue
			}
			if len(as.Rhs) == len(as.Lhs) {
				d.defs[id.Name] = append(d.defs[id.Name], as.Rhs[i])
			} else if len(as.Rhs) == 1 {
				// a, b := call(): every name comes from the one call.
				d.defs[id.Name] = append(d.defs[id.Name], as.Rhs[0])
			}
		}
		return true
	})
	return d
}

// versionIsHonest reports whether the Version expression traces to a parameter or
// to an allowlisted call inside fn, and names why not otherwise.
func versionIsHonest(expr ast.Expr, d definitions) (bool, string) {
	switch e := expr.(type) {
	case *ast.Ident:
		if d.params[e.Name] {
			return true, ""
		}
		defs := d.defs[e.Name]
		if len(defs) == 0 {
			return false, "Version " + e.Name + " has no definition in the function"
		}
		for _, def := range defs {
			if call, ok := def.(*ast.CallExpr); ok && slices.Contains(versionSources, calleeName(call)) {
				continue
			}
			if id, ok := def.(*ast.Ident); ok && d.params[id.Name] {
				continue
			}
			return false, "Version " + e.Name + " is defined from something other than an allowlisted call"
		}
		return true, ""
	case *ast.SelectorExpr:
		// state.Version from a parameter or an allowlisted call's result.
		if e.Sel.Name != "Version" {
			return false, "Version read off a field that is not Version"
		}
		base, ok := e.X.(*ast.Ident)
		if !ok {
			return false, "Version read off a compound expression"
		}
		if d.params[base.Name] {
			return true, ""
		}
		for _, def := range d.defs[base.Name] {
			if call, ok := def.(*ast.CallExpr); ok {
				if name := calleeName(call); name == "SetDraft" || name == "SetAttachments" {
					continue
				}
			}
			return false, base.Name + ".Version is read off a value that is not SetDraft's or SetAttachments' return"
		}
		return true, ""
	case *ast.CallExpr:
		if slices.Contains(versionSources, calleeName(e)) {
			return true, ""
		}
		return false, "Version comes from a call to " + calleeName(e) + ", not an allowlisted source"
	}
	return false, "Version is not an identifier, a field read or an allowlisted call"
}

// frameEventsOf traces X in `X.Subject = ...` to `X := marotte.NewEvent(Event*, ...)` and names the possible Event* constants.
func frameEventsOf(x ast.Expr, d definitions) []string {
	id, ok := x.(*ast.Ident)
	if !ok {
		return nil
	}
	var out []string
	for _, def := range d.defs[id.Name] {
		call, ok := def.(*ast.CallExpr)
		if !ok || calleeName(call) != "NewEvent" || len(call.Args) == 0 {
			continue
		}
		switch arg := call.Args[0].(type) {
		case *ast.SelectorExpr:
			out = append(out, arg.Sel.Name)
		case *ast.Ident:
			for _, argDef := range d.defs[arg.Name] {
				if sel, ok := argDef.(*ast.SelectorExpr); ok {
					out = append(out, sel.Sel.Name)
				}
			}
		}
	}
	return out
}

// collectStampSites finds every stamp a function constructs or assigns.
func collectStampSites(file string, fn *ast.FuncDecl, d definitions) []stampSite {
	var sites []stampSite
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range node.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Subject" || len(node.Rhs) <= i {
					continue
				}
				site := stampSite{fn: fn, file: file, from: node.Rhs[i], frames: frameEventsOf(sel.X, d)}
				if kind, version, isStamp := stampExpr(node.Rhs[i]); isStamp {
					site.kind, site.version, site.built = kind, version, node.Rhs[i]
				} else if id, isID := node.Rhs[i].(*ast.Ident); isID {
					// The stamp is a local traced to its construction or an allowlisted call.
					for _, def := range d.defs[id.Name] {
						if kind, version, isStamp := stampExpr(def); isStamp {
							site.kind, site.version, site.built = kind, version, def
						}
					}
				}
				sites = append(sites, site)
			}
		}
		return true
	})
	// A stamp built inline as an argument or return value.
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		kind, version, isStamp := stampExpr(call)
		if !isStamp {
			return true
		}
		for _, site := range sites {
			if site.built == ast.Expr(call) || site.from == ast.Expr(call) {
				return true
			}
		}
		sites = append(sites, stampSite{fn: fn, file: file, kind: kind, version: version, from: call, built: call})
		return true
	})
	return sites
}

func callsAny(body *ast.BlockStmt, names []string) []string {
	var found []string
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if name := calleeName(call); slices.Contains(names, name) && !slices.Contains(found, name) {
				found = append(found, name)
			}
		}
		return true
	})
	return found
}

func TestStamping_EveryVersionComesFromItsMint(t *testing.T) {
	files := parseStampPackages(t)
	seen := 0
	for file, fs := range files {
		for _, f := range fs {
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				d := collectDefinitions(fn)
				sites := collectStampSites(file, fn, d)
				// A minting body's bare BumpCounter is the mint; only the kind checks apply.
				minting := slices.Contains(mintingBodies, fn.Name.Name)
				if len(sites) > 0 && !minting {
					if reads := callsAny(fn.Body, versionReads); len(reads) > 0 {
						t.Errorf("%s: %s stamps a frame and reads the registry directly (%v); a stamp takes the version its mint returned", file, fn.Name.Name, reads)
					}
				}
				chatStamps := map[ast.Expr]bool{}
				for _, site := range sites {
					seen++
					switch {
					case minting:
					case site.version != nil:
						if honest, why := versionIsHonest(site.version, d); !honest {
							t.Errorf("%s: %s: %s", file, fn.Name.Name, why)
						}
					default:
						// The whole stamp came from an allowlisted call or a parameter's field.
						if honest, _ := stampFromHonestSource(site.from, d); !honest {
							t.Errorf("%s: %s assigns Subject from an expression that is neither a stamp construction, a parameter nor an allowlisted call", file, fn.Name.Name)
						}
					}
					if site.kind == "chat" && site.built != nil {
						chatStamps[site.built] = true
					}
					// Kind checks need a literal kind; a borrowed stamp is judged by its source. The one exempt site is sse.go's
					// emit; a second must be justified here.
					if site.kind != "" {
						for _, frame := range site.frames {
							switch {
							case slices.Contains(chatsFrames, frame) && site.kind != "chats":
								t.Errorf("%s: %s stamps %s with kind %q, want chats (the header frame completes the list projection)", file, fn.Name.Name, frame, site.kind)
							case slices.Contains(chatFrames, frame) && site.kind != "chat":
								t.Errorf("%s: %s stamps %s with kind %q, want chat", file, fn.Name.Name, frame, site.kind)
							}
						}
					}
				}
				if callsMutate := callsAny(fn.Body, []string{"Mutate"}); len(callsMutate) > 0 && len(chatStamps) > 1 {
					t.Errorf("%s: %s calls Mutate and constructs %d chat stamps; one Mutate stamps chat on its LAST frame only", file, fn.Name.Name, len(chatStamps))
				}
			}
		}
	}
	if seen < 8 {
		t.Fatalf("found %d stamp sites across the walk, want at least the store's and the command path's; the walk is not seeing the tree", seen)
	}
}

// stampFromHonestSource judges a non-construction Subject assignment: from an allowlisted call, or a parameter's own Subject.
func stampFromHonestSource(expr ast.Expr, d definitions) (bool, string) {
	switch e := expr.(type) {
	case *ast.Ident:
		if d.params[e.Name] {
			return true, ""
		}
		defs := d.defs[e.Name]
		if len(defs) == 0 {
			return false, "no definition"
		}
		for _, def := range defs {
			call, ok := def.(*ast.CallExpr)
			if !ok || !slices.Contains(versionSources, calleeName(call)) {
				return false, "defined from a non-allowlisted expression"
			}
		}
		return true, ""
	case *ast.SelectorExpr:
		if base, ok := e.X.(*ast.Ident); ok && e.Sel.Name == "Subject" && d.params[base.Name] {
			return true, ""
		}
	case *ast.CallExpr:
		if slices.Contains(versionSources, calleeName(e)) {
			return true, ""
		}
	}
	return false, "not a stamp source"
}

func TestStamping_OnlyEmitAndTheConnectHookPublish(t *testing.T) {
	files := parseStampPackages(t)
	publishers := map[string][]string{}
	var emitBody, hookBody *ast.FuncDecl
	for file, fs := range files {
		for _, f := range fs {
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				if fn.Recv != nil {
					switch fn.Name.Name {
					case "emit":
						emitBody = fn
					case "streamInitialState":
						hookBody = fn
					}
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					if sel.Sel.Name == "Publish" || (sel.Sel.Name == "Event" && isIdent(sel.X, "sw")) {
						publishers[fn.Name.Name] = append(publishers[fn.Name.Name], file+":"+sel.Sel.Name)
					}
					return true
				})
			}
		}
	}
	for name, sites := range publishers {
		if name != "emit" && name != "streamInitialState" {
			t.Errorf("%s publishes or writes to the connection (%v); only emit and streamInitialState may", name, sites)
		}
	}
	if emitBody == nil || hookBody == nil {
		t.Fatalf("emit (%v) or streamInitialState (%v) not found in the walk", emitBody != nil, hookBody != nil)
	}
	if len(publishers["emit"]) == 0 || len(publishers["streamInitialState"]) == 0 {
		t.Errorf("emit publishes at %d sites and streamInitialState writes at %d, want both non-zero", len(publishers["emit"]), len(publishers["streamInitialState"]))
	}
	if reads := callsAny(emitBody.Body, versionReads); len(reads) > 0 {
		t.Errorf("emit reads the registry (%v); its one version source is MergeStamped", reads)
	}
	for _, src := range callsAny(emitBody.Body, versionSources) {
		if src != "MergeStamped" {
			t.Errorf("emit obtains a version through %s; MergeStamped is its one exception", src)
		}
	}
	if reads := callsAny(hookBody.Body, versionReads); len(reads) > 0 {
		t.Errorf("streamInitialState reads the registry (%v); it reaches versions only through the stamped snapshot helpers", reads)
	}
	if srcs := callsAny(hookBody.Body, versionSources); !slices.Contains(srcs, "pendingSnapshotStamped") || !slices.Contains(srcs, "SnapshotStamped") {
		t.Errorf("streamInitialState reaches versions through %v, want pendingSnapshotStamped and SnapshotStamped", srcs)
	}
}

func isIdent(expr ast.Expr, name string) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == name
}
