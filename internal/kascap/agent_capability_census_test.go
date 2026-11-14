package kascap

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/settings"
)

// The registry rows and wire entries the Agent capabilities rows stand on, each a shape the 3.2.0 agent server
// declares: the setting's name, its kiro-cli default and the member marotte writes.
var (
	sessionToolSwitchRe = regexp.MustCompile(`new (` + jsIdent + `)\("(messaging|awareness)","(sessionTools[A-Za-z]+)","([A-Za-z]+)"\)`)
	toolSwitchClassRe   = regexp.MustCompile(`(` + jsIdent + `)=class\{constructor\((` + jsIdent + `),(` + jsIdent + `),(` +
		jsIdent + `)\)\{[^{}]*this\.setting=` + jsIdent + `\((` + jsIdent + `),"([a-z]+)"`)
	deliveryLimitRowRe = regexp.MustCompile(`sendMessageDeliveryLimit:` + jsIdent + `\("sendMessageDeliveryLimit",` + jsIdent +
		`,(` + jsIdent + `),`)
	deliveryLimitWireRe = regexp.MustCompile(`\{key:` + jsIdent + `\.sendMessageDeliveryLimit,wire:` + jsIdent +
		`\("sendMessage","deliveryLimit",\{min:(` + jsIdent + `),max:(` + jsIdent + `)\}\)\}`)
	creatorEffortRowRe  = regexp.MustCompile(`workflowsCreatorEffort:` + jsIdent + `\("workflowsCreatorEffort",(` + jsIdent + `),`)
	creatorEffortWireRe = regexp.MustCompile(`=` + jsIdent + `\("workflows","creatorEffort"\)`)
	awarenessModeRowRe  = regexp.MustCompile(`=` + jsIdent + `\("sessionToolsAwarenessMode",(` + jsIdent + `),`)
	awarenessModeWireRe = regexp.MustCompile(`let (` + jsIdent + `)=` + jsIdent + `\.awareness;return!` + jsIdent + `\((` + jsIdent +
		`)\)\|\|(` + jsIdent + `)\.mode===void 0`)
	infraSafetyRowRe   = regexp.MustCompile(`infraSafety:` + jsIdent + `\("infraSafety",(` + jsIdent + `),`)
	knowledgeWireRe    = regexp.MustCompile(`\{key:` + jsIdent + `\.knowledge([A-Za-z]+),wire:` + jsIdent + `\("knowledge","([A-Za-z]+)"\)\}`)
	knowledgeChunkWire = regexp.MustCompile(`\{keys:\["knowledge"\],read:` + jsIdent + `\(` + jsIdent + `=>\{let ` + jsIdent + `=` + jsIdent +
		`\.knowledge;if\(!` + jsIdent + `\(` + jsIdent + `\)\)return ` + jsIdent + `;let\{chunkSize:` + jsIdent + `,chunkOverlap:` + jsIdent + `\}=`)
	knowledgeTableRe    = regexp.MustCompile(`(` + jsIdent + `)=\[\{key:` + jsIdent + `\.knowledge,wire:`)
	knowledgeChunkRowRe = regexp.MustCompile(`knowledgeChunking:` + jsIdent + `\("knowledgeChunking",(` + jsIdent + `),(` + jsIdent + `),`)
	knowledgeIndexRowRe = regexp.MustCompile(`knowledgeIndexType:` + jsIdent + `\("knowledgeIndexType",(` + jsIdent + `),`)
	maxFilesFallbackRe  = regexp.MustCompile(`typeof ` + jsIdent + `=="number"&&Number\.isFinite\(` + jsIdent + `\)&&` + jsIdent +
		`>=1\?Math\.floor\(` + jsIdent + `\):(` + jsIdent + `)\}`)
	indexNormalizeRe = regexp.MustCompile(`function ` + jsIdent + `\((` + jsIdent + `)\)\{return (` + jsIdent + `)==="fast"\?"fast":"best"\}`)
	matchesIncludeRe = regexp.MustCompile(`function ` + jsIdent + `\(` + jsIdent + `,(` + jsIdent + `)\)\{return!(` + jsIdent + `)\|\|(` +
		jsIdent + `)\.some\(`)
	memoryOptInReadRe = regexp.MustCompile(`resolveMemoryEnabled\(` + jsIdent + `,(` + jsIdent + `)\)\{let (` + jsIdent +
		`)=this\.clientMeta\?\.settings\?\?\{\};return ` + jsIdent + `\(\{[^{}]*,userOptIn:(` + jsIdent + `)==="legacy"&&` +
		`Object\.prototype\.hasOwnProperty\.call\((` + jsIdent + `),"userMemoryOptIn"\)\?` + jsIdent + `\((` + jsIdent +
		`),"userMemoryOptIn"\):void 0\}\)`)
	memoryOptInVetoRe = regexp.MustCompile(`userOptIn:(` + jsIdent + `)\}=` + jsIdent + `;return ` + jsIdent + `\(\{treatment:` +
		jsIdent + `,isInsiderChannel:` + jsIdent + `\}\)\?(` + jsIdent + `)===!1\?\(` + jsIdent + `\("disabledByOptOut"\),!1\):\(` +
		jsIdent + `\("enabled"\),!0\)`)
	memorySourceRe = regexp.MustCompile(`captureMemoryConfig\((` + jsIdent + `)\)\{let (` + jsIdent + `)=typeof (` + jsIdent +
		`)=="object"&&[^,]*\?(` + jsIdent + `)\.settings:void 0,.*?,(` + jsIdent + `)=` + jsIdent + `\((` + jsIdent +
		`)=>\{[^{}]*let (` + jsIdent + `)=(` + jsIdent + `)\.memory;[^{}]*hasOwnProperty\.call\((` + jsIdent +
		`),"mode"\)[^{}]*\},"hasPreference"\);return\{[^{}]*memoryConfigSource:(` + jsIdent + `)\((` + jsIdent +
		`)\)\|\|` + jsIdent + `\(` + jsIdent + `\)\?"explicit":"legacy"`)
	sessionlessMemoryGateRe = regexp.MustCompile(`\?` + jsIdent + `\.memoryEnabled:this\.resolveMemoryEnabled\(this\.buildFeatureConfigRegistry\(` +
		jsIdent + `\),"legacy"\)\)\)throw new ` + jsIdent + `\("Memory is not enabled\."\)`)
	lspServerRowRe  = regexp.MustCompile(`([a-z]+):\{name:"([a-z-]+)",command:"[a-z-]+",args:`)
	languageLabelRe = regexp.MustCompile(`(?m)^  \["([a-z-]+)", "[^"]+"\],$`)
)

// TestSessionToolSwitchesMatchTheBundle pins sessionTools' two members: KAS's SessionToolSwitch rows named messaging
// and awareness, settings sessionToolsMessaging and sessionToolsAwareness, both default off, as the Messages between
// chats and Search other chats defaults are.
func TestSessionToolSwitchesMatchTheBundle(t *testing.T) {
	want := map[string]string{"messaging": "sessionToolsMessaging", "awareness": "sessionToolsAwareness"}
	defaults := map[string]bool{
		"messaging": settings.EffectiveDefaults().ChatMessaging,
		"awareness": settings.EffectiveDefaults().ChatAwareness,
	}
	forEachBundle(t, func(t *testing.T, b bundle) {
		classes := map[string]bool{}
		got := map[string]string{}
		for _, m := range findAround(sessionToolSwitchRe, `","sessionTools`, b.src) {
			classes[b.src[m[2]:m[3]]] = true
			got[b.src[m[4]:m[5]]] = b.src[m[6]:m[7]]
		}
		for member, setting := range want {
			if got[member] != setting {
				t.Errorf("SessionToolSwitch %q is %q in the bundle, want %q; settings.sessionTools.%s would name a switch KAS does not read",
					member, got[member], setting, member)
			}
		}
		if len(classes) != 1 {
			t.Fatalf("the tool switches are built by %d classes %v, want 1", len(classes), slices.Sorted(maps.Keys(classes)))
		}
		def, err := toolSwitchDefault(b.src, slices.Collect(maps.Keys(classes))[0])
		if err != nil {
			t.Fatal(err)
		}
		for member, on := range defaults {
			if (def == "on") != on {
				t.Errorf("kiro-cli's SessionToolSwitch default is %q, marotte's %s default is on=%v", def, member, on)
			}
		}
	})
}

// toolSwitchDefault returns the default the switch class passes for its setting (the second constructor parameter).
func toolSwitchDefault(src, class string) (string, error) {
	for _, m := range findAround(toolSwitchClassRe, class+"=class{", src) {
		if src[m[2]:m[3]] == class && src[m[6]:m[7]] == src[m[10]:m[11]] {
			return src[m[12]:m[13]], nil
		}
	}
	return "", fmt.Errorf("no `%s=class{constructor(...){...this.setting=f(<name>,\"<default>\"` in the bundle", class)
}

// TestDeliveryLimitMatchesTheBundle pins sendMessageDeliveryLimit's default and bounds against marotte's, and its wire
// member on the initialize table: the default withholds the key, and the bounds are what the setting accepts.
func TestDeliveryLimitMatchesTheBundle(t *testing.T) {
	forEachBundle(t, func(t *testing.T, b bundle) {
		defIdent := discoverOneAround(t, "sendMessageDeliveryLimit registry row", deliveryLimitRowRe, `"sendMessageDeliveryLimit",`, b.src)
		wires := findAround(deliveryLimitWireRe, `("sendMessage","deliveryLimit"`, b.src)
		if len(wires) != 1 {
			t.Fatalf("found %d `{key:<r>.sendMessageDeliveryLimit,wire:<f>(\"sendMessage\",\"deliveryLimit\",{min,max})}` entries, want 1", len(wires))
		}
		w := wires[0]
		for _, c := range []struct {
			name  string
			ident string
			want  int
		}{
			{"default", defIdent, settings.DefaultChatMessageDeliveryLimit},
			{"min", b.src[w[2]:w[3]], 1},
			{"max", b.src[w[4]:w[5]], 500},
		} {
			got, err := numberBinding(b.src, c.ident)
			if err != nil {
				t.Fatalf("sendMessageDeliveryLimit %s: %v", c.name, err)
			}
			if got != c.want {
				t.Errorf("sendMessageDeliveryLimit %s is %d in the bundle, want %d (settings' delivery limit)", c.name, got, c.want)
			}
		}
		for _, n := range []int{0, 1, 500, 501} {
			if settings.ValidChatMessageDeliveryLimit(n) != (n >= 1 && n <= 500) {
				t.Errorf("settings.ValidChatMessageDeliveryLimit(%d) disagrees with the bundle's 1..500", n)
			}
		}
	})
}

// TestCreatorEffortVocabularyMatchesTheBundle pins the workflowsCreatorEffort vocabulary and its wire member
// settings.workflows.creatorEffort: KAS drops any value outside the vocabulary to no effort.
func TestCreatorEffortVocabularyMatchesTheBundle(t *testing.T) {
	forEachBundle(t, func(t *testing.T, b bundle) {
		ident := discoverOneAround(t, "workflowsCreatorEffort registry row", creatorEffortRowRe, `"workflowsCreatorEffort",`, b.src)
		levels, err := stringArrayBinding(b.src, ident)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range levels {
			if !settings.ValidWorkflowCreatorEffort(l) {
				t.Errorf("kiro-cli's creator effort %q is not a value workflow_creator_effort accepts", l)
			}
		}
		for _, l := range []string{"low", "medium", "high", "xhigh", "max"} {
			if !slices.Contains(levels, l) {
				t.Errorf("workflow_creator_effort accepts %q, which kiro-cli's %v does not", l, levels)
			}
		}
		if len(findAround(creatorEffortWireRe, `("workflows","creatorEffort")`, b.src)) != 1 {
			t.Error(`the bundle has no one wire reader <f>("workflows","creatorEffort"); creatorEffort would ride a member KAS does not read`)
		}
	})
}

// TestAwarenessModeMatchesTheBundle pins the sessionToolsAwarenessMode vocabulary chat_awareness_mode offers and its
// wire member settings.sessionTools.awareness.mode: KAS reads any other value as malformed and keeps its default.
func TestAwarenessModeMatchesTheBundle(t *testing.T) {
	forEachBundle(t, func(t *testing.T, b bundle) {
		ident := discoverOneAround(t, "sessionToolsAwarenessMode registry row", awarenessModeRowRe, `("sessionToolsAwarenessMode",`, b.src)
		modes, err := stringArrayBinding(b.src, ident)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range modes {
			if !settings.ValidChatAwarenessMode(m) {
				t.Errorf("kiro-cli's awareness mode %q is not a value chat_awareness_mode accepts", m)
			}
		}
		for _, m := range []string{"direct", "consult"} {
			if !slices.Contains(modes, m) {
				t.Errorf("chat_awareness_mode accepts %q, which kiro-cli's %v does not", m, modes)
			}
		}
		readers := 0
		for _, m := range findAround(awarenessModeWireRe, `.awareness;return!`, b.src) {
			if n := b.src[m[2]:m[3]]; b.src[m[4]:m[5]] == n && b.src[m[6]:m[7]] == n {
				readers++
			}
		}
		if readers != 1 {
			t.Errorf("found %d wire readers `let <n>=<e>.awareness;return!<f>(<n>)||<n>.mode===void 0`, want 1; mode would ride a member KAS does not read",
				readers)
		}
	})
}

// TestInfraSafetyEnforceMatchesTheBundle pins the CloudFormation check's enforce value: the infraSafety registry
// enum carries enforce, initialize reads infraSafetyEnforce, and an enforce block asks through the override shape
// internal/agent's unattended floor refuses by its tool call id.
func TestInfraSafetyEnforceMatchesTheBundle(t *testing.T) {
	agentSrc, err := os.ReadFile("../agent/run_unattended.go")
	if err != nil {
		t.Fatal(err)
	}
	forEachBundle(t, func(t *testing.T, b bundle) {
		ident := discoverOneAround(t, "infraSafety registry row", infraSafetyRowRe, `"infraSafety",`, b.src)
		modes, err := stringArrayBinding(b.src, ident)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(modes, []string{"off", "monitor", "enforce"}) {
			t.Errorf("kiro-cli's infraSafety modes are %v, want [off monitor enforce]", modes)
		}
		if !strings.Contains(b.src, `("infraSafetyEnforce")`) {
			t.Error(`initialize no longer reads ("infraSafetyEnforce"); the enforce row would send a key nothing reads`)
		}
		for _, lit := range []string{
			"`safety-override-${",
			`[{optionId:"reject",name:"Keep blocked",kind:"reject_once"},{optionId:"accept",name:"Allow anyway",kind:"allow_once"}]`,
		} {
			if !strings.Contains(b.src, lit) {
				t.Errorf("the CloudFormation override ask no longer carries %s; re-read requestSafetyOverridePermission and "+
					"internal/agent's infraSafetyBlockAsk", lit)
			}
		}
	})
	if !strings.Contains(string(agentSrc), `"safety-override-"`) {
		t.Error(`internal/agent/run_unattended.go no longer matches the "safety-override-" tool call id`)
	}
}

// TestKnowledgeTuningMatchesTheBundle pins the Knowledge base indexing rows against kiro-cli's knowledge rows: the
// members settings.knowledge carries on the initialize table, each default marotte withholds, the two index types
// (accurate is best), and the empty include list KAS reads as including nothing, which is why marotte withholds one.
func TestKnowledgeTuningMatchesTheBundle(t *testing.T) {
	defaults := settings.EffectiveDefaults()
	forEachBundle(t, func(t *testing.T, b bundle) {
		members := map[string]string{}
		for _, m := range findAround(knowledgeWireRe, `("knowledge","`, b.src) {
			members[b.src[m[2]:m[3]]] = b.src[m[4]:m[5]]
		}
		for row, member := range map[string]string{
			"IncludePatterns": "includePatterns", "ExcludePatterns": "excludePatterns",
			"MaxFiles": "maxFiles", "IndexType": "indexType",
		} {
			if members[row] != member {
				t.Errorf("knowledge%s rides settings.knowledge.%q in the bundle, want %q (kascap knowledgeValue)", row, members[row], member)
			}
		}
		if len(findAround(knowledgeChunkWire, `{chunkSize:`, b.src)) != 1 {
			t.Error("the bundle has no one knowledge reader taking {chunkSize, chunkOverlap} off settings.knowledge")
		}
		table := discoverOneAround(t, "knowledge wire table", knowledgeTableRe, `.knowledge,wire:`, b.src)
		if !regexp.MustCompile(`kiroAgent:\[\.\.\.` + regexp.QuoteMeta(table) + `[,\]]`).MatchString(b.src) {
			t.Errorf("the knowledge entries %s are not on the kiroAgent (initialize) table; the members would ride the wrong door", table)
		}

		fallback := discoverOneAround(t, "resolveMaxFiles fallback", maxFilesFallbackRe, `?Math.floor(`, b.src)
		if got, err := numberLiteralBinding(b.src, fallback); err != nil || got != float64(defaults.KnowledgeMaxFiles) {
			t.Errorf("resolveMaxFiles falls back to %s = %v (%v), want %d (settings.DefaultKnowledgeMaxFiles)",
				fallback, got, err, defaults.KnowledgeMaxFiles)
		}

		rows := findAround(knowledgeChunkRowRe, `"knowledgeChunking",`, b.src)
		if len(rows) != 1 {
			t.Fatalf("found %d knowledgeChunking registry rows, want 1", len(rows))
		}
		schema, def := b.src[rows[0][2]:rows[0][3]], b.src[rows[0][4]:rows[0][5]]
		wantDef := fmt.Sprintf("%s={size:%d,overlap:%d}", def, defaults.KnowledgeChunking.Size, defaults.KnowledgeChunking.Overlap)
		if !containsBinding(b.src, wantDef) {
			t.Errorf("the knowledgeChunking default is not %s", wantDef)
		}
		schemaRe := regexp.MustCompile(`(?:^|[^A-Za-z0-9_$.])` + regexp.QuoteMeta(schema) + `=` + jsIdent + `\.object\(\{size:` + jsIdent +
			`\.number\(\)\.min\(64\),overlap:` + jsIdent + `\.number\(\)\.min\(0\)\}\)\.refine\(` + jsIdent + `=>` + jsIdent +
			`\.overlap<` + jsIdent + `\.size\)`)
		if !schemaRe.MatchString(b.src) {
			t.Errorf("the knowledgeChunking schema %s is no longer {size >= 64, overlap >= 0, overlap < size}, which "+
				"settings.validKnowledgeChunking mirrors", schema)
		}

		ident := discoverOneAround(t, "knowledgeIndexType registry row", knowledgeIndexRowRe, `"knowledgeIndexType",`, b.src)
		types, err := stringArrayBinding(b.src, ident)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(types, []string{"fast", "best", "accurate"}) {
			t.Errorf("kiro-cli's knowledge index types are %v, want [fast best accurate]", types)
		}
		for _, v := range types {
			if settings.ValidKnowledgeIndexType(v) == (v == "accurate") {
				t.Errorf("settings.ValidKnowledgeIndexType(%q) = %v; marotte offers fast and best, never the legacy alias",
					v, settings.ValidKnowledgeIndexType(v))
			}
		}
		normalized := false
		for _, m := range findAround(indexNormalizeRe, `==="fast"?"fast":"best"}`, b.src) {
			normalized = normalized || b.src[m[2]:m[3]] == b.src[m[4]:m[5]]
		}
		if !normalized {
			t.Error(`normalizeIndexType no longer reads every type but "fast" as "best"; "accurate" may now differ from best`)
		}

		empty := false
		for _, m := range findAround(matchesIncludeRe, `.some(`, b.src) {
			p := b.src[m[2]:m[3]]
			empty = empty || (b.src[m[4]:m[5]] == p && b.src[m[6]:m[7]] == p)
		}
		if !empty {
			t.Error("matchesInclude no longer has the shape `return!e||e.some(`; re-read whether an empty include list still " +
				"includes nothing, which agent.knowledgePatterns withholds for")
		}
	})
}

// TestUserMemoryOptInIsALegacyVeto pins why userMemoryOptIn stays withheld with no Settings row: KAS reads it from the
// initialize settings only for a session whose memory source is legacy, a session request carrying settings.memory
// (marotte's memory row) is explicit, true grants nothing because eligibility is the memory experiment's, and the
// sessionless _kiro/memory gate the Memories tab passes resolves as legacy, so false would refuse that tab.
func TestUserMemoryOptInIsALegacyVeto(t *testing.T) {
	forEachBundle(t, func(t *testing.T, b bundle) {
		read := false
		for _, m := range findAround(memoryOptInReadRe, `,"userMemoryOptIn")?`, b.src) {
			source, settings := b.src[m[2]:m[3]], b.src[m[4]:m[5]]
			read = read || (b.src[m[6]:m[7]] == source && b.src[m[8]:m[9]] == settings && b.src[m[10]:m[11]] == settings)
		}
		if !read {
			t.Error(`resolveMemoryEnabled no longer reads userMemoryOptIn from clientMeta.settings only when the source is "legacy"; ` +
				"re-read whether a marotte session (explicit) now reads it")
		}

		veto := false
		for _, m := range findAround(memoryOptInVetoRe, `("disabledByOptOut")`, b.src) {
			veto = veto || b.src[m[2]:m[3]] == b.src[m[4]:m[5]]
		}
		if !veto {
			t.Error("the memory gate no longer reads the opt-in only as `optIn===false` after the experiment's eligibility; " +
				"re-read whether userMemoryOptIn can now turn memory on")
		}

		explicit := false
		for _, m := range findAround(memorySourceRe, `?"explicit":"legacy"`, b.src) {
			param, request := b.src[m[2]:m[3]], b.src[m[4]:m[5]]
			hasPref, prefArg, memVar := b.src[m[10]:m[11]], b.src[m[12]:m[13]], b.src[m[14]:m[15]]
			explicit = explicit || (b.src[m[6]:m[7]] == param && b.src[m[8]:m[9]] == param &&
				b.src[m[16]:m[17]] == prefArg && b.src[m[18]:m[19]] == memVar &&
				b.src[m[20]:m[21]] == hasPref && b.src[m[22]:m[23]] == request)
		}
		if !explicit {
			t.Error("captureMemoryConfig no longer marks a request whose settings.memory has a mode as explicit; marotte's " +
				"memory row may no longer stop KAS reading userMemoryOptIn")
		}

		if len(findAround(sessionlessMemoryGateRe, `("Memory is not enabled.")`, b.src)) != 1 {
			t.Error(`requireMemoryEnabled no longer resolves a sessionless _kiro/memory call as "legacy"; re-read whether ` +
				"sending userMemoryOptIn false would still refuse the Memories tab")
		}
	})
}

// TestTangentModeAndCheckpointHaveNoReader pins why the table has no row for tangentMode and checkpoint, the TUI's
// tangent and checkpoint switches (both default off): KAS takes both on the session door and reads neither, so
// marotte's fork (session/fork, createdReason tangent) and Rewind (_kiro/checkpoint/revertMultiple) work without them.
// A reader is a get through the registry row, a read of the raw settings object, or a call naming the key.
func TestTangentModeAndCheckpointHaveNoReader(t *testing.T) {
	forEachBundle(t, func(t *testing.T, b bundle) {
		for _, key := range []string{"tangentMode", "checkpoint"} {
			t.Run(key, func(t *testing.T) {
				wire := regexp.MustCompile(`\{key:(` + jsIdent + `)\.` + key + `,wire:(` + jsIdent + `)\("` + key + `"\),messages:`)
				ms := findAround(wire, "."+key+",wire:", b.src)
				if len(ms) != 1 {
					t.Fatalf("found %d session-door wire entries for %s, want 1; re-read where KAS takes it", len(ms), key)
				}
				registry, wireFn := b.src[ms[0][2]:ms[0][3]], b.src[ms[0][4]:ms[0][5]]

				if n := len(memberSites(b.src, registry, key)); n != 1 {
					t.Errorf("%s.%s occurs %d times, want only the wire entry; a new reader may gate fork or Rewind",
						registry, key, n)
				}

				for _, read := range []string{".data." + key, ".data?." + key, `["` + key + `"]`, "." + key + ".enabled", "." + key + "?.enabled"} {
					if n := len(memberSites(b.src, "", read)); n != 0 {
						t.Errorf("found %d reads %s; a new reader may gate fork or Rewind", n, read)
					}
				}

				named := regexp.MustCompile(`(new )?(` + jsIdent + `)\((?:[^()]*,)?"` + key + `"\)`)
				for _, m := range findAround(named, `"`+key+`")`, b.src) {
					if m[2] < 0 && b.src[m[4]:m[5]] != wireFn {
						t.Errorf("%s names %q; a new reader may gate fork or Rewind", b.src[m[0]:m[1]], key)
					}
				}
			})
		}
	})
}

// memberSites returns where src holds obj followed by member, with no identifier byte after it and, for a named
// obj, none before it. An empty obj matches member wherever it ends a name.
func memberSites(src, obj, member string) []int {
	lit := member
	if obj != "" {
		lit = obj + "." + member
	}
	var sites []int
	for _, at := range indexesOf(src, lit) {
		end := at + len(lit)
		if obj != "" && at > 0 && (isIdentByte(src[at-1], true) || src[at-1] == '.') {
			continue
		}
		if end == len(src) || !isIdentByte(src[end], true) {
			sites = append(sites, at)
		}
	}
	return sites
}

// numberLiteralBinding returns the numeric literal, exponent forms included, assigned to ident, failing unless there
// is exactly one.
func numberLiteralBinding(src, ident string) (float64, error) {
	re := regexp.MustCompile(`(?:^|[^A-Za-z0-9_$.])` + regexp.QuoteMeta(ident) + `=([0-9]+(?:\.[0-9]+)?(?:e[0-9]+)?)[,;)]`)
	ms := re.FindAllStringSubmatch(src, -1)
	if len(ms) != 1 {
		return 0, fmt.Errorf("%s: found %d numeric assignments, want exactly 1", ident, len(ms))
	}
	return strconv.ParseFloat(ms[0][1], 64)
}

// containsBinding reports whether src assigns exactly the text binding, with no identifier byte before its name.
func containsBinding(src, binding string) bool {
	for _, at := range indexesOf(src, binding) {
		if at == 0 || !isIdentByte(src[at-1], true) {
			return true
		}
	}
	return false
}

// discoverOneAround returns the one distinct group-1 match of re around lit.
func discoverOneAround(t *testing.T, what string, re *regexp.Regexp, lit, src string) string {
	t.Helper()
	seen := map[string]bool{}
	for _, m := range findAround(re, lit, src) {
		seen[src[m[2]:m[3]]] = true
	}
	if len(seen) != 1 {
		t.Fatalf("%s: found %d candidates %v in the bundle, want exactly 1", what, len(seen), slices.Sorted(maps.Keys(seen)))
	}
	return slices.Collect(maps.Keys(seen))[0]
}

// numberBinding returns the integer literal assigned to ident, failing unless there is exactly one.
func numberBinding(src, ident string) (int, error) {
	var vals []int
	for _, at := range literalSites(src, ident+"=", false) {
		rest := src[at+len(ident)+1:]
		end := 0
		for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
			end++
		}
		if end == 0 || end < len(rest) && isIdentByte(rest[end], true) {
			continue
		}
		n, err := strconv.Atoi(rest[:end])
		if err != nil {
			return 0, err
		}
		vals = append(vals, n)
	}
	if len(vals) != 1 {
		return 0, fmt.Errorf("%s: found %d integer assignments %v, want exactly 1", ident, len(vals), vals)
	}
	return vals[0], nil
}

// stringArrayBinding returns the array of string literals assigned to ident, failing unless there is exactly one.
func stringArrayBinding(src, ident string) ([]string, error) {
	var found [][]string
	for _, at := range literalSites(src, ident+`=["`, false) {
		arr := jsBracketed(src, at+len(ident)+1)
		var vals []string
		for item := range strings.SplitSeq(strings.Trim(arr, "[]"), ",") {
			v, err := strconv.Unquote(item)
			if err != nil {
				return nil, fmt.Errorf("%s: %q is not a string literal", ident, item)
			}
			vals = append(vals, v)
		}
		found = append(found, vals)
	}
	if len(found) != 1 {
		return nil, fmt.Errorf("%s: found %d string-array assignments, want exactly 1", ident, len(found))
	}
	return found[0], nil
}

// TestBundledLanguageServersAreKiroServers pins the Code intelligence row's claim: every language server
// bundled-tools.json marks lsp is a server in kiro-cli's language table, so installing it is what turns code
// intelligence on for that language, and the row names its language (code-intel-status.ts LANGUAGE_OF).
func TestBundledLanguageServersAreKiroServers(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "bundled-tools.json"))
	if err != nil {
		t.Fatal(err)
	}
	ts, err := os.ReadFile(filepath.Join("..", "..", "static-src", "code-intel-status.ts"))
	if err != nil {
		t.Fatal(err)
	}
	labelled := map[string]bool{}
	for _, m := range languageLabelRe.FindAllStringSubmatch(string(ts), -1) {
		labelled[m[1]] = true
	}
	var doc struct {
		Entries map[string]struct {
			Lsp bool `json:"lsp"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode bundled-tools.json: %v", err)
	}
	var bundled []string
	for name, e := range doc.Entries {
		if e.Lsp {
			bundled = append(bundled, name)
		}
	}
	if len(bundled) == 0 {
		t.Fatal("bundled-tools.json marks no entry lsp")
	}
	for _, name := range slices.Sorted(slices.Values(bundled)) {
		if !labelled[name] {
			t.Errorf("bundled language server %s has no language in code-intel-status.ts LANGUAGE_OF %v", name, labelled)
		}
	}
	forEachBundle(t, func(t *testing.T, b bundle) {
		servers := map[string]string{}
		for _, m := range lspServerRowRe.FindAllStringSubmatch(b.src, -1) {
			servers[m[2]] = m[1]
		}
		if servers["gopls"] != "go" {
			t.Fatalf("kiro-cli's language table maps gopls to %q, want go: the table's shape moved (%d rows read)",
				servers["gopls"], len(servers))
		}
		for _, name := range slices.Sorted(slices.Values(bundled)) {
			if _, ok := servers[name]; !ok {
				t.Errorf("bundled language server %s is not in kiro-cli's language table %v", name, servers)
			}
		}
	})
}
